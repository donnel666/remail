package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	governanceinfra "github.com/donnel666/remail/internal/governance/infra"
	mailinfra "github.com/donnel666/remail/internal/mailtransport/infra"
	"github.com/donnel666/remail/internal/mailtransport/infra/msacl"
	"github.com/donnel666/remail/internal/platform"
	proxyapp "github.com/donnel666/remail/internal/proxy/app"
	proxydomain "github.com/donnel666/remail/internal/proxy/domain"
	"gorm.io/gorm"
)

type mailCheckpoint struct {
	Snapshot               snapshot
	ClientID, RefreshToken string
}

func sameMailSnapshot(a, b snapshot) bool {
	return a.ID == b.ID && a.OwnerID == b.OwnerID && a.Version == b.Version &&
		a.ValidationGeneration == b.ValidationGeneration && a.CredentialRevision == b.CredentialRevision &&
		a.BindingID == b.BindingID && a.BindingAddress == b.BindingAddress && a.BindingUpdatedAt.Equal(b.BindingUpdatedAt) &&
		a.Status == b.Status && digest(a) == digest(b)
}

func migratedMailbox(ctx context.Context, db *gorm.DB, s snapshot) error {
	if s.Status != "normal" && s.Status != "abnormal" {
		return errFence
	}
	var count int64
	err := db.WithContext(ctx).Table(table+" l").Joins("JOIN microsoft_binding_mailboxes b ON b.resource_id=l.resource_id").
		Where("l.resource_id=? AND l.owner_id=? AND l.email=? AND l.binding_id=? AND l.state IN ('success','mail_pending') AND l.phase='remote_complete' AND l.target_address=? AND b.binding_address=l.target_address AND b.status='verified'", s.ID, s.OwnerID, s.Email, s.BindingID, s.BindingAddress).Count(&count).Error
	if err != nil || count != 1 {
		return errors.New("mail verification requires a committed, verified migration")
	}
	return nil
}

func (r *runtime) verifyMail(ctx context.Context, id uint, skipLocked bool) (runErr error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	s, err := loadSnapshot(ctx, r.db, id, false)
	if err != nil {
		return err
	}
	if err := migratedMailbox(ctx, r.db, s); err != nil {
		return err
	}
	defer func() {
		var auth *msacl.AuthError
		if skipLocked && errors.As(runErr, &auth) && auth.Status == msacl.AuthStatusAccountLocked {
			saveCtx, saveCancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
			defer saveCancel()
			if err := skipLockedMail(saveCtx, r.db, s); err != nil {
				runErr = errors.Join(runErr, err)
			}
		}
	}()
	path := filepath.Join(r.dir, fmt.Sprintf("mail-%d.json", id))
	saved := mailCheckpoint{Snapshot: s, ClientID: strings.TrimSpace(s.ClientID), RefreshToken: strings.TrimSpace(s.RefreshToken)}
	if data, err := os.ReadFile(path); err == nil {
		if json.Unmarshal(data, &saved) != nil || !sameMailSnapshot(saved.Snapshot, s) {
			return errors.New("mail credential checkpoint differs from the current resource; reconciliation required")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	proxy, err := r.proxies.Acquire(ctx, proxyapp.AcquireProxyRequest{Key: s.Email, IPVersion: proxydomain.ProxyIPv4, Purpose: proxydomain.ProxyPurposeBinding, AllowSystemFallback: true, RequestID: platform.NewUUIDV7String()})
	if err != nil || proxy == nil || proxy.Direct || proxy.URL == "" {
		return errors.New("a healthy production proxy is required for mail verification")
	}
	release, err := r.lockRoute(ctx, proxy.URL)
	if err != nil {
		return err
	}
	defer release()
	ctx = msacl.WithExistingRecoveryProofOnly(msacl.WithConcreteRecoveryLeaseKeys(msacl.WithRecoveryLeaseScope(ctx, id, "")))
	if saved.ClientID == "" || saved.RefreshToken == "" {
		slog.Info("authorizing missing mail credentials", "resource_id", id)
		result, err := mailinfra.NewMicrosoftOAuthClient().AcquireToken(ctx, mailinfra.MicrosoftOAuthRequest{EmailAddress: s.Email, Password: s.Password, BindingAddress: s.BindingAddress, ProxyURL: proxy.URL})
		if err != nil {
			return errors.New("microsoft mail authorization failed")
		}
		if !result.Valid || result.ClientID == "" || result.RefreshToken == "" {
			if result.Category == "locked" {
				return &msacl.AuthError{Status: msacl.AuthStatusAccountLocked, Message: result.SafeMessage}
			}
			return fmt.Errorf("mail authorization incomplete (%s): %s", result.Category, result.SafeMessage)
		}
		if result.BindingAddress != "" {
			resolved := msacl.ResolveBindingAddress(ctx, result.BindingAddress, s.Email, proxy.URL, s.BindingAddress)
			if !strings.EqualFold(resolved, s.BindingAddress) {
				slog.Warn("mail authorization binding mismatch", "resource_id", id, "expected_binding", s.BindingAddress, "observed_binding", result.BindingAddress, "resolved_binding", resolved)
				return errors.New("mail authorization observed a different recovery mailbox")
			}
		}
		saved.ClientID, saved.RefreshToken = result.ClientID, result.RefreshToken
	}
	if err := writeCheckpoint(path, saved); err != nil {
		return err
	}
	// Exchange the stored refresh token again so the credentials being saved also work without the login session.
	result, fetchErr := mailinfra.NewMicrosoftMailFetchClient().FetchAll(ctx, mailinfra.MicrosoftMailFetchRequest{EmailAddress: s.Email, ClientID: saved.ClientID, RefreshToken: saved.RefreshToken, ProxyURL: proxy.URL, MaxMessages: 1, StopAfterLimit: true, MetadataOnly: true})
	if token := strings.TrimSpace(result.RefreshToken); token != "" {
		saved.RefreshToken = token
	}
	if err := writeCheckpoint(path, saved); err != nil {
		return err
	}
	if fetchErr != nil || !result.Valid {
		if result.Category == "locked" {
			return &msacl.AuthError{Status: msacl.AuthStatusAccountLocked, Message: result.SafeMessage}
		}
		return fmt.Errorf("mail fetch verification incomplete (%s): %s", result.Category, result.SafeMessage)
	}
	commitCtx, cancelCommit := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancelCommit()
	if err := commitMailVerification(commitCtx, r.db, saved, result); err != nil {
		return err
	}
	releaseRecoveryLeases(ctx, id)
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		slog.Warn("verified mail checkpoint cleanup deferred", "resource_id", id)
	}
	slog.Info("mail fetch verified and committed", "resource_id", id, "protocol", result.Protocol, "message_count", result.MessageCount)
	return nil
}

func skipLockedMail(ctx context.Context, db *gorm.DB, expected snapshot) error {
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		s, err := loadSnapshot(ctx, tx, expected.ID, true)
		if err != nil || !sameMailSnapshot(expected, s) {
			return errFence
		}
		if err := migratedMailbox(ctx, tx, s); err != nil {
			return err
		}
		now := time.Now().UTC()
		message := "Mail verification skipped: Microsoft account requires unlock; confirmed replacement binding retained."
		result := tx.Model(&item{}).Where("resource_id=? AND state='mail_pending'", s.ID).Updates(map[string]any{"state": "skipped", "phase": "mail_account_locked", "safe_error": message, "retry_at": nil, "updated_at": now})
		if result.Error != nil || result.RowsAffected != 1 {
			return errFence
		}
		if err := tx.Table("microsoft_resources").Where("id=?", s.ID).Updates(map[string]any{"status": "abnormal", "last_safe_error": message, "updated_at": now}).Error; err != nil {
			return err
		}
		if err := tx.Table("email_resources").Where("id=?", s.ID).Updates(map[string]any{"version": gorm.Expr("version+1"), "updated_at": now}).Error; err != nil {
			return err
		}
		return governanceinfra.NewOperationLogRepo(db).Create(platform.WithGormTx(ctx, tx), &governancedomain.OperationLog{OperatorUserID: 1, OperationType: "mailtransport.microsoft.binding_domain_skip", ResourceType: "microsoft_resource", ResourceID: fmt.Sprint(s.ID), Path: "cmd/msbindingmigrate", Result: "skipped", SafeSummary: message, RequestID: platform.NewUUIDV7String()})
	})
}

func commitMailVerification(ctx context.Context, db *gorm.DB, saved mailCheckpoint, result mailinfra.MicrosoftMailFetchResult) error {
	if !result.Valid || (result.Protocol != "graph" && result.Protocol != "imap") || strings.TrimSpace(saved.ClientID) == "" || strings.TrimSpace(saved.RefreshToken) == "" {
		return errors.New("a successful real mail fetch with complete credentials is required")
	}
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		s, err := loadSnapshot(ctx, tx, saved.Snapshot.ID, true)
		if err != nil || !sameMailSnapshot(saved.Snapshot, s) {
			return errFence
		}
		if err := migratedMailbox(ctx, tx, s); err != nil {
			return err
		}
		now := time.Now().UTC()
		requestID := platform.NewUUIDV7String()
		revision := s.CredentialRevision
		values := map[string]any{"status": "normal", "last_safe_error": "", "validation_failures": 0, "graph_available": result.Protocol == "graph", "validation_generation": s.ValidationGeneration + 1, "updated_at": now}
		if saved.ClientID != s.ClientID || saved.RefreshToken != s.RefreshToken {
			revision++
			values["client_id"], values["refresh_token"] = saved.ClientID, saved.RefreshToken
			values["credential_revision"], values["credential_updated_at"] = revision, now
			values["token_last_refreshed_at"], values["token_last_request_id"] = now, requestID
		}
		if err := tx.Table("microsoft_resources").Where("id=?", s.ID).Updates(values).Error; err != nil {
			return err
		}
		if err := tx.Table("email_resources").Where("id=?", s.ID).Updates(map[string]any{"version": gorm.Expr("version+1"), "updated_at": now}).Error; err != nil {
			return err
		}
		if err := tx.Model(&item{}).Where("resource_id=? AND state IN ('success','mail_pending')", s.ID).Updates(map[string]any{"state": "success", "safe_error": "", "retry_at": nil, "retry_count": 0, "updated_at": now}).Error; err != nil {
			return err
		}
		return governanceinfra.NewOperationLogRepo(db).Create(platform.WithGormTx(ctx, tx), &governancedomain.OperationLog{OperatorUserID: 1, OperationType: "mailtransport.microsoft.binding_domain_mail_verify", ResourceType: "microsoft_resource", ResourceID: fmt.Sprint(s.ID), Path: "cmd/msbindingmigrate", Result: "success", SafeSummary: fmt.Sprintf("Real %s mailbox fetch passed and working OAuth credentials were persisted at credential revision %d. Password, recovery binding, sale switch and allocations retained.", result.Protocol, revision), RequestID: requestID})
	})
}
