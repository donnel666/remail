package icloud

import (
	"context"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ProcessICloudValidation owns only the account health dimension. An Apple
// session is a provisioning credential; it must never make the resource
// abnormal. The only health probe is IMAP LOGIN with the app-specific password.
func (s *Service) ProcessICloudValidation(ctx context.Context, task iCloudValidationTask) error {
	if s == nil || s.db == nil || task.ResourceID == 0 || task.OwnerUserID == 0 ||
		task.ValidationGeneration == 0 || task.ExpectedCredentialRevision == 0 {
		return ErrICloudValidationTemp
	}
	resource, found, err := s.iCloudValidationResource(ctx, task)
	if err != nil || !found {
		return err
	}
	now := s.now().UTC()
	retryAt := now.Add(iCloudValidationRetryInterval)
	result := iCloudIMAPValidationResult{Status: iCloudResourcePending, NextAt: &retryAt}
	if s.imap == nil {
		result.Message = "iCloud IMAP client is unavailable."
		return s.applyICloudIMAPValidationResult(ctx, task, result)
	}
	loginErr := s.imap.Login(ctx, resource.PrimaryEmail, resource.IMAPAppPassword)
	switch {
	case loginErr == nil:
		result.Status = iCloudResourceNormal
		result.Message = ""
		result.NextAt = nil
	case errors.Is(loginErr, errICloudIMAPAuthentication):
		result.Status = iCloudResourceAbnormal
		result.Message = "iCloud IMAP app password cannot receive mail."
		result.NextAt = &retryAt
	default:
		result.Status = iCloudResourcePending
		result.Message = "iCloud IMAP service is temporarily unavailable."
		result.NextAt = &retryAt
	}
	return s.applyICloudIMAPValidationResult(ctx, task, result)
}

type iCloudIMAPValidationResult struct {
	Status  string
	Message string
	NextAt  *time.Time
}

func (s *Service) applyICloudIMAPValidationResult(ctx context.Context, task iCloudValidationTask, result iCloudIMAPValidationResult) error {
	if result.Status != iCloudResourceNormal && (result.NextAt == nil || result.NextAt.IsZero()) {
		next := s.now().UTC().Add(iCloudValidationRetryInterval)
		result.NextAt = &next
	}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var root iCloudRootModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND type = ? AND owner_user_id = ?", task.ResourceID, "icloud", task.OwnerUserID).
			First(&root).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}
		var resource iCloudResourceModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&resource, task.ResourceID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}
		if resource.ValidationGeneration != task.ValidationGeneration ||
			resource.CredentialRevision != task.ExpectedCredentialRevision ||
			resource.Status != iCloudResourceValidating {
			return nil
		}
		now := s.now().UTC()
		failures := resource.ValidationFailures
		if result.Status == iCloudResourceNormal {
			failures = 0
		} else if failures < iCloudValidationMaxFailures {
			failures++
		}
		updates := map[string]any{
			"status": result.Status, "validation_failures": failures,
			"last_safe_error": safeICloudImportMessage(result.Message),
			"last_checked_at": now, "next_validation_at": result.NextAt,
			"updated_at": now,
		}
		if result.Status == iCloudResourceNormal {
			updates["last_valid_at"] = now
			if resource.ExpireAt.After(now) && resource.AliasCount < iCloudMaxAliases {
				updates["next_provision_at"] = now
			} else {
				updates["next_provision_at"] = nil
			}
		} else {
			updates["next_provision_at"] = nil
		}
		if err := tx.Model(&iCloudResourceModel{}).
			Where("id = ? AND status = ? AND validation_generation = ? AND credential_revision = ?",
				task.ResourceID, iCloudResourceValidating, task.ValidationGeneration, task.ExpectedCredentialRevision).
			Updates(updates).Error; err != nil {
			return err
		}
		if rootUpdated := tx.Model(&iCloudRootModel{}).
			Where("id = ? AND version = ?", root.ID, root.Version).
			Updates(map[string]any{"version": gorm.Expr("version + 1"), "updated_at": now}); rootUpdated.Error != nil {
			return rootUpdated.Error
		} else if rootUpdated.RowsAffected != 1 {
			return errICloudValidationStale
		}
		if run, err := findICloudMaintenanceRunTx(ctx, tx, task); err != nil {
			return err
		} else if run != nil {
			runStatus := iCloudMaintenanceSucceeded
			if result.Status == iCloudResourcePending {
				runStatus = iCloudMaintenanceFailed
			}
			if err := finishICloudMaintenanceRunTx(ctx, tx, run.ID, runStatus, result.Message, now); err != nil {
				return err
			}
		}
		return nil
	})
	if errors.Is(err, errICloudValidationStale) {
		return nil
	}
	if err != nil {
		return ErrICloudValidationTemp
	}
	if result.Status == iCloudResourceNormal {
		_ = s.ScheduleICloudProvisionDispatcher(context.WithoutCancel(ctx), 0)
	}
	return nil
}

func normalizeICloudChannelStatus(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value != iCloudSessionValid && value != iCloudSessionInvalid {
		return iCloudSessionUnchecked
	}
	return value
}
