package icloud

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/donnel666/remail/internal/platform"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"gorm.io/gorm"
)

type iCloudIMAPResourceScope struct {
	ID                 uint       `gorm:"column:id"`
	PrimaryEmail       string     `gorm:"column:primary_email"`
	AppPassword        string     `gorm:"column:imap_app_password"`
	UIDValidity        string     `gorm:"column:imap_uid_validity"`
	LastUID            uint64     `gorm:"column:imap_last_uid"`
	LastSyncAt         *time.Time `gorm:"column:imap_last_sync_at"`
	CredentialRevision uint64     `gorm:"column:credential_revision"`
}

type MailFetchRequest struct {
	ResourceID  uint
	SinceAt     time.Time
	UntilAt     time.Time
	MaxMessages int
	FullHistory bool
}

type MailFetchCursor struct {
	ResourceID                 uint
	ExpectedCredentialRevision uint64
	ExpectedUIDValidity        string
	ExpectedLastUID            uint64
	UIDValidity                string
	LastUID                    uint64
}

type MailFetchResult struct {
	Messages []iCloudIMAPMessage
	Cursor   *MailFetchCursor
}

// FetchMail performs one account-level IMAP read. The alias list is loaded
// once from local inventory; receiving mail never calls Apple's HME list API.
func (s *Service) FetchMail(ctx context.Context, request MailFetchRequest) (*MailFetchResult, error) {
	if s == nil || s.db == nil || request.ResourceID == 0 {
		return nil, ErrICloudMailUnavailable
	}
	scope, aliases, err := s.loadICloudIMAPScope(ctx, request.ResourceID)
	if err != nil {
		return nil, err
	}
	if len(aliases) == 0 {
		return &MailFetchResult{}, nil
	}
	client := s.imap
	if client == nil {
		client = &iCloudIMAPClient{}
	}
	sinceAt, untilAt := request.SinceAt, request.UntilAt
	if !request.FullHistory {
		// The cursor belongs to the whole iCloud account, not to the order that
		// triggered this read. Order windows are applied later by MailMatch.
		sinceAt = s.now().UTC().Add(-runtimeconfig.Duration("fetch_lookback_window_days", 90*24*time.Hour, 24*time.Hour, 1))
		untilAt = time.Time{}
	}
	messages, uidValidity, lastUID, err := client.Fetch(ctx, iCloudIMAPFetchRequest{
		Email: scope.PrimaryEmail, AppPassword: scope.AppPassword,
		UIDValidity: scope.UIDValidity, LastUID: scope.LastUID, Aliases: aliases,
		SinceAt: sinceAt, UntilAt: untilAt,
		MaxMessages: request.MaxMessages,
		FullHistory: request.FullHistory,
	})
	if err != nil {
		if errors.Is(err, errICloudIMAPAuthentication) {
			failureCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			_ = s.markICloudIMAPAuthenticationFailure(failureCtx, *scope)
		}
		return nil, fmt.Errorf("%w: IMAP fetch", ErrICloudMailUnavailable)
	}
	result := &MailFetchResult{Messages: messages}
	if !request.FullHistory {
		result.Cursor = &MailFetchCursor{
			ResourceID: scope.ID, ExpectedCredentialRevision: scope.CredentialRevision,
			ExpectedUIDValidity: scope.UIDValidity, ExpectedLastUID: scope.LastUID,
			UIDValidity: uidValidity, LastUID: lastUID,
		}
	}
	return result, nil
}

// CommitMailCursor advances the incremental IMAP cursor only after MailMatch
// has durably ingested the fetched batch and its task fence is still current.
func (s *Service) CommitMailCursor(ctx context.Context, cursor MailFetchCursor, fence func(context.Context) error) error {
	if s == nil || s.db == nil || cursor.ResourceID == 0 || cursor.ExpectedCredentialRevision == 0 {
		return ErrICloudMailUnavailable
	}
	now := s.now().UTC()
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		txCtx := platform.WithGormTx(ctx, tx)
		if fence != nil {
			if err := fence(txCtx); err != nil {
				return err
			}
		}
		result := tx.WithContext(txCtx).Model(&iCloudResourceModel{}).
			Where("id = ? AND credential_revision = ? AND imap_uid_validity = ? AND imap_last_uid = ?",
				cursor.ResourceID, cursor.ExpectedCredentialRevision,
				strings.TrimSpace(cursor.ExpectedUIDValidity), cursor.ExpectedLastUID).
			Updates(map[string]any{
				"imap_uid_validity": strings.TrimSpace(cursor.UIDValidity),
				"imap_last_uid":     cursor.LastUID,
				"imap_last_sync_at": now,
				"updated_at":        now,
			})
		if result.Error != nil {
			return result.Error
		}
		// Another current scan may already have advanced the cursor. Never move it
		// backwards; MailMatch deduplication makes the stale batch harmless.
		return nil
	})
	if err != nil {
		return err
	}
	return nil
}

func (s *Service) loadICloudIMAPScope(ctx context.Context, resourceID uint) (*iCloudIMAPResourceScope, []string, error) {
	var scope iCloudIMAPResourceScope
	err := s.db.WithContext(ctx).Table("icloud_resources").
		Select("id, primary_email, imap_app_password, imap_uid_validity, imap_last_uid, imap_last_sync_at, credential_revision").
		Where("id = ? AND status NOT IN ?", resourceID, []string{iCloudResourceDisabled, iCloudResourceDeleted}).
		Take(&scope).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil, ErrICloudResourceNotFound
	}
	if err != nil {
		return nil, nil, fmt.Errorf("%w: load resource", ErrICloudMailUnavailable)
	}
	if strings.TrimSpace(scope.PrimaryEmail) == "" || strings.TrimSpace(scope.AppPassword) == "" {
		return nil, nil, fmt.Errorf("%w: IMAP credentials missing", ErrICloudMailUnavailable)
	}
	var rows []struct {
		Email string `gorm:"column:email"`
	}
	if err := s.db.WithContext(ctx).Table("icloud_aliases").
		Select("email").Where("resource_id = ? AND status = ?", resourceID, iCloudResourceNormal).
		Find(&rows).Error; err != nil {
		return nil, nil, fmt.Errorf("%w: load aliases", ErrICloudMailUnavailable)
	}
	aliases := make([]string, 0, len(rows))
	for _, row := range rows {
		if value := strings.ToLower(strings.TrimSpace(row.Email)); value != "" {
			aliases = append(aliases, value)
		}
	}
	return &scope, aliases, nil
}

func (s *Service) markICloudIMAPAuthenticationFailure(ctx context.Context, scope iCloudIMAPResourceScope) error {
	if s == nil || s.db == nil || scope.ID == 0 || scope.CredentialRevision == 0 {
		return ErrICloudMailUnavailable
	}
	now := s.now().UTC()
	retryAt := now.Add(iCloudValidationRetryInterval)
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		updated := tx.Model(&iCloudResourceModel{}).
			Where("id = ? AND credential_revision = ? AND status IN ?", scope.ID, scope.CredentialRevision,
				[]string{iCloudResourcePending, iCloudResourceNormal, iCloudResourceAbnormal}).
			Updates(map[string]any{
				"status": iCloudResourceAbnormal, "validation_failures": iCloudValidationMaxFailures,
				"last_safe_error": "iCloud IMAP app password cannot receive mail.",
				"last_checked_at": now, "next_validation_at": retryAt, "next_provision_at": nil,
				"updated_at": now,
			})
		if updated.Error != nil || updated.RowsAffected == 0 {
			return updated.Error
		}
		rootUpdated := tx.Model(&iCloudRootModel{}).Where("id = ? AND type = ?", scope.ID, "icloud").
			Updates(map[string]any{"version": gorm.Expr("version + 1"), "updated_at": now})
		if rootUpdated.Error != nil {
			return rootUpdated.Error
		}
		if rootUpdated.RowsAffected != 1 {
			return errICloudValidationStale
		}
		return nil
	})
	if err != nil {
		return ErrICloudMailUnavailable
	}
	_ = s.ScheduleICloudValidationDispatcher(context.WithoutCancel(ctx), 0)
	return nil
}
