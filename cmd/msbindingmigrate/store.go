package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"

	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	governanceinfra "github.com/donnel666/remail/internal/governance/infra"
	mailinfra "github.com/donnel666/remail/internal/mailtransport/infra"
	"github.com/donnel666/remail/internal/mailtransport/infra/msacl"
	"github.com/donnel666/remail/internal/platform"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const originalTable = "microsoft_binding_migration_wsu_aishop6"

// Selected once at process startup, before GORM caches the model's table name.
var table, sourceDomain = originalTable, "wsu.edu.kg"

//go:embed schema.sql
var schema string

var errFence = errors.New("resource identity, credentials, status or binding changed; migration retained for reconciliation")

type item struct {
	ResourceID                                      uint `gorm:"primaryKey;autoIncrement:false"`
	OwnerID, BindingID                              uint
	Email, SourceAddress, OriginalStatus            string
	BindingUpdatedAt                                time.Time
	ResourceVersion, Generation, CredentialRevision uint64
	CredentialDigest, ResolvedSource, TargetAddress string
	BatchID, State, Phase, SafeError                string
	Attempts                                        int
	RetryCount                                      uint
	RetryAt                                         *time.Time
	CreatedAt, UpdatedAt                            time.Time
}

func (item) TableName() string { return table }

type snapshot struct {
	ID, OwnerID, BindingID                            uint
	Email, Password, ClientID, RefreshToken           string
	Status, BindingAddress                            string
	Version, ValidationGeneration, CredentialRevision uint64
	BindingUpdatedAt                                  time.Time
}

func digest(s snapshot) string {
	data, _ := json.Marshal([]string{s.Email, s.Password, s.ClientID, s.RefreshToken})
	return fmt.Sprintf("%x", sha256.Sum256(data))
}

func destination(source string) (string, error) {
	a, err := mail.ParseAddress(source)
	local, domain, ok := strings.Cut(source, "@")
	if err != nil || a.Address != source || !ok || local == "" || domain != sourceDomain || strings.ContainsAny(source, "*\r\n\x00") {
		return "", fmt.Errorf("source must be a concrete %s mailbox; masked local parts cannot be migrated", sourceDomain)
	}
	return local + "@aishop6.com", nil
}

func seed(ctx context.Context, db *gorm.DB) error {
	if err := db.WithContext(ctx).Exec(strings.ReplaceAll(schema, originalTable, table)).Error; err != nil {
		return errors.New("migration table initialization failed")
	}
	for _, column := range []struct{ name, definition string }{{"retry_count", "INT UNSIGNED NOT NULL DEFAULT 0"}, {"retry_at", "DATETIME(3) NULL"}} {
		if !db.Migrator().HasColumn(&item{}, column.name) {
			if err := db.WithContext(ctx).Exec("ALTER TABLE " + table + " ADD COLUMN " + column.name + " " + column.definition).Error; err != nil {
				return errors.New("migration retry schedule initialization failed")
			}
		}
	}
	var count int64
	if err := db.WithContext(ctx).Model(&item{}).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	// Freeze the inventory once; READ COMMITTED avoids locking all source rows while copying it.
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return tx.Exec(`INSERT INTO `+table+`
 (resource_id,email,owner_id,source_address,binding_id,binding_updated_at,original_status)
 SELECT m.id,m.email_address,e.owner_user_id,LOWER(TRIM(b.binding_address)),b.id,b.updated_at,m.status
 FROM microsoft_resources m JOIN email_resources e ON e.id=m.id AND e.type='microsoft'
 JOIN microsoft_binding_mailboxes b ON b.resource_id=m.id
 WHERE SUBSTRING_INDEX(LOWER(TRIM(b.binding_address)),'@',-1)=?
 AND m.status IN ('normal','abnormal','pending','validating')`, sourceDomain).Error
	}, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
}

func loadSnapshot(ctx context.Context, db *gorm.DB, id uint, lock bool) (snapshot, error) {
	var s snapshot
	q := db.WithContext(ctx)
	if lock {
		if err := q.Table("email_resources").Select("id").Where("id=? AND type='microsoft'", id).Clauses(clause.Locking{Strength: "UPDATE"}).Take(&struct{ ID uint }{}).Error; err != nil {
			return s, errFence
		}
		if err := q.Table("microsoft_resources").Select("id").Where("id=?", id).Clauses(clause.Locking{Strength: "UPDATE"}).Take(&struct{ ID uint }{}).Error; err != nil {
			return s, errFence
		}
		if err := q.Table("microsoft_binding_mailboxes").Select("id").Where("resource_id=?", id).Clauses(clause.Locking{Strength: "UPDATE"}).Take(&struct{ ID uint }{}).Error; err != nil {
			return s, errFence
		}
	}
	err := q.Raw(`SELECT m.id,m.email_address AS email,e.owner_user_id AS owner_id,e.version,
 m.password,m.client_id,m.refresh_token,m.status,m.validation_generation,m.credential_revision,
 b.id AS binding_id,b.binding_address,b.updated_at AS binding_updated_at
 FROM email_resources e JOIN microsoft_resources m ON m.id=e.id
 JOIN microsoft_binding_mailboxes b ON b.resource_id=m.id
 WHERE e.id=? AND e.type='microsoft'`, id).Scan(&s).Error
	if err != nil || s.ID != id {
		return s, errFence
	}
	return s, nil
}

func matches(i item, s snapshot) bool {
	return i.ResourceID == s.ID && i.Email == s.Email && i.OwnerID == s.OwnerID &&
		i.BindingID == s.BindingID && strings.EqualFold(i.SourceAddress, s.BindingAddress) && i.BindingUpdatedAt.Equal(s.BindingUpdatedAt) &&
		i.ResourceVersion == s.Version && i.Generation == s.ValidationGeneration && i.CredentialRevision == s.CredentialRevision &&
		i.CredentialDigest == digest(s) && s.Status == "pending"
}

func claim(ctx context.Context, db *gorm.DB, size int, ids []uint, continuous bool) (rows []item, err error) {
	if size < 1 || size > 1000 {
		return nil, errors.New("claim size must be between 1 and 1000")
	}
	err = db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		q := tx.Where("state='queued'").Order("resource_id").Limit(size).Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"})
		if len(ids) > 0 {
			q = q.Where("resource_id IN ?", ids)
		}
		var selected []item
		if err := q.Find(&selected).Error; err != nil {
			return err
		}
		batch := platform.NewUUIDV7String()
		for n := range selected {
			i := &selected[n]
			s, e := loadSnapshot(ctx, tx, i.ResourceID, true)
			if e != nil && (!continuous || !errors.Is(e, errFence)) {
				return e
			}
			if e != nil || s.Email != i.Email || s.OwnerID != i.OwnerID || s.BindingID != i.BindingID || !strings.EqualFold(s.BindingAddress, i.SourceAddress) || !s.BindingUpdatedAt.Equal(i.BindingUpdatedAt) || s.Status == "disabled" || s.Status == "deleted" {
				if !continuous {
					return errFence
				}
				now := time.Now().UTC()
				if err := tx.Model(i).Updates(map[string]any{"state": "failed", "safe_error": safeError(errFence), "retry_count": gorm.Expr("retry_count+1"), "retry_at": now.Add(24 * time.Hour), "updated_at": now}).Error; err != nil {
					return err
				}
				continue
			}
			i.OriginalStatus = s.Status
			i.ResourceVersion = s.Version + 1
			i.Generation = s.ValidationGeneration + 1
			i.CredentialRevision = s.CredentialRevision
			i.CredentialDigest = digest(s)
			i.BatchID = batch
			i.State = "claimed"
			i.UpdatedAt = time.Now().UTC()
			if err := tx.Table("microsoft_resources").Where("id=?", i.ResourceID).Updates(map[string]any{"status": "pending", "validation_generation": i.Generation, "validation_failures": 0, "last_safe_error": "Auxiliary mailbox domain migration pending.", "updated_at": i.UpdatedAt}).Error; err != nil {
				return err
			}
			if err := tx.Table("email_resources").Where("id=?", i.ResourceID).Updates(map[string]any{"version": i.ResourceVersion, "updated_at": i.UpdatedAt}).Error; err != nil {
				return err
			}
			if err := tx.Save(i).Error; err != nil {
				return err
			}
			rows = append(rows, *i)
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	return
}

func saveItem(ctx context.Context, db *gorm.DB, i item, values map[string]any) error {
	values["updated_at"] = time.Now().UTC()
	result := db.WithContext(ctx).Model(&item{}).Where("resource_id=? AND generation=? AND state<>'success'", i.ResourceID, i.Generation).Updates(values)
	if result.Error != nil {
		return errors.New("migration progress could not be saved")
	}
	if result.RowsAffected != 1 {
		return errFence
	}
	return nil
}

func commit(ctx context.Context, db *gorm.DB, expected item, evidence checkpoint) error {
	if !evidence.validFor(expected) || !evidence.Result.Verified || !evidence.Result.OldProofRemoved || evidence.Result.Stage != "remote_complete" {
		return errors.New("verified Microsoft replacement evidence is required")
	}
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current item
		if err := tx.Where("resource_id=?", expected.ResourceID).Clauses(clause.Locking{Strength: "UPDATE"}).Take(&current).Error; err != nil {
			return err
		}
		if current.Generation != expected.Generation || current.TargetAddress != expected.TargetAddress {
			return errFence
		}
		if current.State == "success" || current.State == "mail_pending" {
			return nil
		}
		s, err := loadSnapshot(ctx, tx, current.ResourceID, true)
		if err != nil {
			return err
		}
		if !matches(current, s) {
			return errFence
		}
		txCtx := platform.WithGormTx(ctx, tx)
		_, err = mailinfra.NewMicrosoftBindingRepo(db).ApplyValidationBindingObservation(txCtx, mailinfra.MicrosoftValidationBindingObservationInput{ResourceID: current.ResourceID, OwnerUserID: current.OwnerID, AccountEmail: current.Email, Address: current.TargetAddress, Status: "verified"})
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		status, safeMessage := "abnormal", "Auxiliary mailbox migrated; mail fetch verification is pending."
		if strings.TrimSpace(s.ClientID) == "" || strings.TrimSpace(s.RefreshToken) == "" {
			status, safeMessage = "abnormal", "Microsoft mail fetch credentials are incomplete."
		}
		if err := tx.Table("microsoft_resources").Where("id=?", current.ResourceID).Updates(map[string]any{"status": status, "validation_failures": 0, "last_safe_error": safeMessage, "updated_at": now}).Error; err != nil {
			return err
		}
		if err := tx.Table("email_resources").Where("id=?", current.ResourceID).Updates(map[string]any{"version": gorm.Expr("version+1"), "updated_at": now}).Error; err != nil {
			return err
		}
		if err := tx.Model(&item{}).Where("resource_id=?", current.ResourceID).Updates(map[string]any{"state": "mail_pending", "phase": "remote_complete", "safe_error": "", "retry_at": nil, "retry_count": 0, "updated_at": now}).Error; err != nil {
			return err
		}
		return governanceinfra.NewOperationLogRepo(db).Create(txCtx, &governancedomain.OperationLog{OperatorUserID: 1, OperationType: "mailtransport.microsoft.binding_domain_migrate", ResourceType: "microsoft_resource", ResourceID: fmt.Sprint(current.ResourceID), Path: "cmd/msbindingmigrate", Result: "success", SafeSummary: fmt.Sprintf("Microsoft confirmed the aishop6.com recovery mailbox and removal of the previous recovery mailbox. Resource status: %s; credentials, sale switch and allocations retained.", status), RequestID: current.BatchID})
	})
}

func skipExternal(ctx context.Context, db *gorm.DB, expected item, address string) error {
	parsed, err := mail.ParseAddress(address)
	if err != nil || parsed.Address != address || msacl.UsesActiveAuxiliaryDomain(address) {
		return errors.New("a confirmed external recovery address is required")
	}
	return skipUnchanged(ctx, db, expected, "external_binding", safeError(fmt.Errorf("migration skipped: Microsoft reports external recovery mailbox %s", address)))
}

func skipLocked(ctx context.Context, db *gorm.DB, expected item) error {
	return skipUnchanged(ctx, db, expected, "account_locked", "Migration skipped: Microsoft account is locked and requires user unlock.")
}

func skipUnchanged(ctx context.Context, db *gorm.DB, expected item, phase, message string) error {
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current item
		if err := tx.Where("resource_id=?", expected.ResourceID).Clauses(clause.Locking{Strength: "UPDATE"}).Take(&current).Error; err != nil {
			return err
		}
		if current.Generation != expected.Generation {
			return errFence
		}
		if current.State == "skipped" {
			return nil
		}
		if current.State == "success" || current.State == "partial" || current.State == "mail_pending" || !beforeChange(current.Phase) {
			return errFence
		}
		s, err := loadSnapshot(ctx, tx, current.ResourceID, true)
		if err != nil {
			return err
		}
		if !matches(current, s) {
			return errFence
		}
		status := current.OriginalStatus
		if status != "normal" && status != "abnormal" {
			status = "pending"
		}
		if phase == "account_locked" {
			status = "abnormal"
		}
		now := time.Now().UTC()
		if err := tx.Table("microsoft_resources").Where("id=?", current.ResourceID).Updates(map[string]any{"status": status, "last_safe_error": message, "updated_at": now}).Error; err != nil {
			return err
		}
		if err := tx.Table("email_resources").Where("id=?", current.ResourceID).Updates(map[string]any{"version": gorm.Expr("version+1"), "updated_at": now}).Error; err != nil {
			return err
		}
		if err := tx.Model(&item{}).Where("resource_id=?", current.ResourceID).Updates(map[string]any{"state": "skipped", "phase": phase, "target_address": "", "safe_error": message, "retry_at": nil, "updated_at": now}).Error; err != nil {
			return err
		}
		return governanceinfra.NewOperationLogRepo(db).Create(platform.WithGormTx(ctx, tx), &governancedomain.OperationLog{OperatorUserID: 1, OperationType: "mailtransport.microsoft.binding_domain_skip", ResourceType: "microsoft_resource", ResourceID: fmt.Sprint(current.ResourceID), Path: "cmd/msbindingmigrate", Result: "skipped", SafeSummary: message, RequestID: current.BatchID})
	})
}
