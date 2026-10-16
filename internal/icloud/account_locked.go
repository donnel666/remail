package icloud

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const iCloudAccountLockedCategory = "account_locked"
const iCloudAccountLockedMessage = "Apple Account is locked or not active. Login, Cookie keepalive and alias creation are permanently stopped. Mail delivery is checked separately."

func isICloudAccountLocked(resource iCloudResourceModel) bool {
	return resource.WorkflowLastErrorCategory == iCloudAccountLockedCategory
}

// The caller locks the root and resource first; mail health and existing aliases are independent of login access.
func stopLockedICloudAccountTx(ctx context.Context, tx *gorm.DB, resource iCloudResourceModel, now time.Time) error {
	if err := tx.WithContext(ctx).Model(&iCloudResourceModel{}).Where("id = ?", resource.ID).Updates(map[string]any{
		"last_error_category": iCloudAccountLockedCategory,
		"onboarding_status":   iCloudOnboardingFailed, "dispatch_status": "failed", "claim_token": "",
		"generation":      gorm.Expr("generation + 1"),
		"next_attempt_at": nil, "next_validation_at": nil, "next_provision_at": nil, "family_next_sync_at": nil,
		"secret_payload": nil, "session_payload": nil, "manual_verification_code": "", "pending_sms_purpose": "",
		"sms_sent_at": nil, "sms_poll_deadline": nil, "forward_preparation_id": nil,
		"finished_at": now, "updated_at": now,
	}).Error; err != nil {
		return err
	}
	if err := tx.WithContext(ctx).Model(&iCloudResourceChannelModel{}).Where("resource_id = ?", resource.ID).Updates(map[string]any{
		"session_status": iCloudSessionLocked, "next_keepalive_at": nil, "cooldown_until": nil, "updated_at": now,
	}).Error; err != nil {
		return err
	}
	// The lock fences Cookie writers; keep mail checks and their revisions independent.
	if err := tx.WithContext(ctx).Model(&iCloudMaintenanceRunModel{}).
		Where("resource_id = ? AND kind IN ? AND status IN ?", resource.ID,
			[]string{iCloudMaintenanceValidation, iCloudMaintenanceAlias}, []string{iCloudMaintenanceQueued, iCloudMaintenanceRunning}).
		Updates(map[string]any{"status": iCloudMaintenanceCanceled, "last_safe_error": iCloudAccountLockedMessage, "finished_at": now, "updated_at": now}).Error; err != nil {
		return err
	}
	return tx.WithContext(ctx).Model(&iCloudRootModel{}).Where("id = ?", resource.ID).
		Updates(map[string]any{"version": gorm.Expr("version + 1"), "updated_at": now}).Error
}

func failLockedICloudOnboardingTaskTx(tx *gorm.DB, task *iCloudOnboardingTaskModel, attempts int, now time.Time) (bool, error) {
	var root iCloudRootModel
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Take(&root, task.ID).Error; err != nil {
		return false, err
	}
	var resource iCloudResourceModel
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Take(&resource, task.ID).Error; err != nil {
		return false, err
	}
	if resource.WorkflowGeneration != task.Generation || resource.WorkflowClaimToken != task.ClaimToken ||
		resource.WorkflowDispatchStatus != task.DispatchStatus ||
		(task.ExpectedCredentialRevision != 0 && resource.CredentialRevision != task.ExpectedCredentialRevision) ||
		resource.OnboardingStatus == iCloudOnboardingFailed || resource.OnboardingStatus == iCloudOnboardingCompleted {
		return false, nil
	}
	if err := stopLockedICloudAccountTx(tx.Statement.Context, tx, resource, now); err != nil {
		return false, err
	}
	return true, tx.Model(&iCloudResourceModel{}).Where("id = ?", resource.ID).
		Update("attempts", min(attempts, task.MaxAttempts)).Error
}

func (s *Service) stopLockedICloudAccount(ctx context.Context, expected iCloudResourceModel) error {
	finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), iCloudMaintenanceFinishTimeout)
	defer cancel()
	return s.db.WithContext(finishCtx).Transaction(func(tx *gorm.DB) error {
		var root iCloudRootModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Take(&root, expected.ID).Error; err != nil {
			return err
		}
		var resource iCloudResourceModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Take(&resource, expected.ID).Error; err != nil {
			return err
		}
		if resource.CredentialRevision != expected.CredentialRevision || resource.PrimaryEmail != expected.PrimaryEmail {
			return errICloudRefreshStale
		}
		if isICloudAccountLocked(resource) {
			return nil
		}
		return stopLockedICloudAccountTx(finishCtx, tx, resource, s.now().UTC().Truncate(time.Millisecond))
	})
}

func isAppleAccountLockedError(err error) bool {
	var appleErr *AppleOnboardingError
	return errors.As(err, &appleErr) && appleErr.Category == iCloudAccountLockedCategory
}
