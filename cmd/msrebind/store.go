package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"time"

	coredomain "github.com/donnel666/remail/internal/core/domain"
	coreinfra "github.com/donnel666/remail/internal/core/infra"
	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	governanceinfra "github.com/donnel666/remail/internal/governance/infra"
	mailinfra "github.com/donnel666/remail/internal/mailtransport/infra"
	"github.com/donnel666/remail/internal/platform"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type candidate struct {
	ID             uint   `json:"id"`
	Email          string `json:"email"`
	ExpectedStatus string `json:"status,omitempty"`
	Password       string `json:"password,omitempty"`
}

func accountKey(id uint, email string) string {
	if id != 0 {
		return fmt.Sprint(id)
	}
	return fmt.Sprintf("external-%x", sha256.Sum256([]byte(email)))
}

type snapshot struct {
	ID                 uint
	Email              string
	Version            uint64
	OwnerID            uint
	Password           string
	ClientID           string
	RefreshToken       string
	CredentialRevision uint64
	Status             string
	ForSale            bool
	BindingID          uint
	BindingAddress     string
	BindingUpdatedAt   time.Time
}

type receipt struct {
	Operation        string `json:",omitempty"`
	Snapshot         snapshot
	Target           string
	RemoteVerified   bool
	Committed        bool
	OldProofRetained bool
	RequestID        string
}

func loadSnapshot(ctx context.Context, db *gorm.DB, target candidate) (snapshot, error) {
	var result snapshot
	if target.ID == 0 {
		result = snapshot{Email: target.Email, Password: target.Password, Status: "external"}
		if _, err := exportLine(result, targetAddress(target.Email)); err != nil {
			return result, err
		}
		return result, checkExternalAccountAbsent(ctx, db, target.Email)
	}
	err := db.WithContext(ctx).Raw(`SELECT m.id,m.email_address AS email,er.version,er.owner_user_id AS owner_id,
 m.password,m.client_id,m.refresh_token,m.credential_revision,m.status,m.for_sale,
 COALESCE(b.id,0) AS binding_id,COALESCE(b.binding_address,'') AS binding_address,b.updated_at AS binding_updated_at
 FROM microsoft_resources m JOIN email_resources er ON er.id=m.id AND er.type='microsoft'
 LEFT JOIN microsoft_binding_mailboxes b ON b.resource_id=m.id WHERE m.id=?`, target.ID).Scan(&result).Error
	if err != nil {
		return result, errors.New("resource snapshot could not be read")
	}
	if result.ID != target.ID || result.Email != target.Email {
		return result, errors.New("manifest no longer matches resource identity")
	}
	expectedStatus := target.ExpectedStatus
	if expectedStatus == "" {
		expectedStatus = "normal"
	}
	if (expectedStatus != "normal" && expectedStatus != "abnormal") || result.Status != expectedStatus || result.ForSale {
		return result, errors.New("resource must match the manifest status and be unpublished before binding")
	}
	if _, err := exportLine(result, targetAddress(result.Email)); err != nil {
		return result, err
	}
	if err := checkNoAllocations(ctx, db, result.ID); err != nil {
		return result, err
	}
	return result, nil
}

func checkExternalAccountAbsent(ctx context.Context, db *gorm.DB, email string) error {
	var count int64
	if err := db.WithContext(ctx).Table("microsoft_resources").Where("email_address=?", email).Count(&count).Error; err != nil {
		return errors.New("external account identity check failed")
	}
	if count != 0 {
		return errors.New("account exists in the system; use its exact resource ID")
	}
	return nil
}

func checkNoAllocations(ctx context.Context, db *gorm.DB, id uint) error {
	var count int64
	if err := db.WithContext(ctx).Table("microsoft_allocations").Where("resource_id=? AND status='allocated'", id).Count(&count).Error; err != nil {
		return errors.New("active allocation check failed")
	}
	if count != 0 {
		return errors.New("resource has an active allocation")
	}
	return nil
}

func commitRebind(ctx context.Context, db *gorm.DB, saved receipt) error {
	if !saved.RemoteVerified || saved.Target != targetAddress(saved.Snapshot.Email) {
		return errors.New("remote rebind evidence is incomplete")
	}
	if saved.Snapshot.ID == 0 {
		if saved.Snapshot.Status != "external" {
			return errors.New("external account receipt is invalid")
		}
		return checkExternalAccountAbsent(ctx, db, saved.Snapshot.Email)
	}
	admin := coreinfra.NewAdminResourceRepo(db)
	return admin.WithTx(ctx, func(txCtx context.Context) error {
		root, resource, err := admin.LockAdminMicrosoft(txCtx, saved.Snapshot.ID)
		if err != nil {
			return err
		}
		tx, _ := platform.GormTxFromContext(txCtx)
		var binding mailinfra.MicrosoftBindingMailboxModel
		err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("resource_id=?", resource.ID).Take(&binding).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return errors.New("binding snapshot could not be locked")
		}
		if resource.EmailAddress != saved.Snapshot.Email || root.OwnerUserID != saved.Snapshot.OwnerID || resource.Password != saved.Snapshot.Password || resource.ClientID != saved.Snapshot.ClientID || resource.RefreshToken != saved.Snapshot.RefreshToken {
			return errors.New("resource credentials changed after remote rebinding")
		}
		if resource.Status == coredomain.MicrosoftStatusDisabled && binding.BindingAddress == saved.Target && binding.Status == "verified" {
			return nil
		}
		if root.Version != saved.Snapshot.Version || resource.CredentialRevision != saved.Snapshot.CredentialRevision || string(resource.Status) != saved.Snapshot.Status || (saved.Snapshot.Status != "normal" && saved.Snapshot.Status != "abnormal") || resource.ForSale || binding.ID != saved.Snapshot.BindingID || binding.BindingAddress != saved.Snapshot.BindingAddress || !binding.UpdatedAt.Equal(saved.Snapshot.BindingUpdatedAt) {
			return errors.New("resource changed after remote rebinding; durable receipt retained")
		}
		if err := checkNoAllocations(txCtx, tx, resource.ID); err != nil {
			return err
		}
		_, err = mailinfra.NewMicrosoftBindingRepo(db).ApplyValidationBindingObservation(txCtx, mailinfra.MicrosoftValidationBindingObservationInput{ResourceID: resource.ID, OwnerUserID: root.OwnerUserID, AccountEmail: resource.EmailAddress, Address: saved.Target, Status: "verified"})
		if err != nil {
			return err
		}
		if err := resource.DisableAdmin(); err != nil {
			return err
		}
		if err := admin.SaveAdminMicrosoft(txCtx, root, resource, root.Version); err != nil {
			return err
		}
		summary := "Recovery mailbox was verified at Microsoft, the previous email proof was removed, and the resource was disabled."
		if saved.Operation == "bind" {
			summary = "The initial recovery mailbox was added and verified at Microsoft, and the resource was disabled."
		} else if saved.Operation == "already_bound" {
			summary = "The requested recovery mailbox was already present and was verified at Microsoft, and the resource was disabled."
		} else if saved.OldProofRetained {
			summary = "The additional recovery mailbox was verified at Microsoft, the previous email proof was retained as requested, and the resource was disabled."
		}
		return governanceinfra.NewOperationLogRepo(db).Create(txCtx, &governancedomain.OperationLog{OperatorUserID: 1, OperationType: "mailtransport.microsoft.rebind_disable", ResourceType: "microsoft_resource", ResourceID: fmt.Sprint(resource.ID), Path: "cmd/msrebind", Result: "success", SafeSummary: summary, RequestID: saved.RequestID})
	})
}

func targetAddress(email string) string { return strings.SplitN(email, "@", 2)[0] + "@fan7z.com" }

func exportLine(s snapshot, target string) (string, error) {
	fields := []string{s.Email, s.Password, s.ClientID, s.RefreshToken, target}
	for _, index := range []int{2, 3} {
		if fields[index] == "" {
			fields[index] = "x"
		}
	}
	for _, field := range fields {
		if field == "" || strings.ContainsAny(field, "\r\n\x00") || strings.Contains(field, "----") {
			return "", errors.New("credential cannot be exported in the requested five-field format")
		}
	}
	return strings.Join(fields, "----") + "\n", nil
}
