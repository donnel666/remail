package infra

import (
	"context"
	"errors"
	"strings"
	"time"

	daumapp "github.com/donnel666/remail/internal/daum/app"
	"github.com/donnel666/remail/internal/daum/domain"
	"github.com/donnel666/remail/internal/daum/infra/protocol"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (s *Service) ProcessValidation(ctx context.Context, task daumapp.ValidationTaskPayload) (resultErr error) {
	if s == nil || s.Protocol == nil {
		return domain.ErrDependency
	}
	var resource *Resource
	var run *MaintenanceRun
	err := s.transaction(ctx, func(ctx context.Context, tx *gorm.DB) error {
		var err error
		resource, err = lockResource(tx, task.ResourceID, &task.OwnerUserID)
		if err != nil {
			return err
		}
		if resource.ValidationGeneration != task.ValidationGeneration || resource.CredentialRevision != task.CredentialRevision || (resource.Status != domain.StatusPending && resource.Status != domain.StatusValidating) {
			return domain.ErrInvalidClaim
		}
		now := s.Now().UTC()
		run, err = ensureMaintenanceRunTx(ctx, tx, resource.ID, resource.ValidationGeneration, resource.CredentialRevision, maintenanceKindValidation, task.RequestID, now)
		if err != nil {
			return err
		}
		if (task.MaintenanceRunID != 0 && task.MaintenanceRunID != run.ID) || (run.Status != maintenanceQueued && run.Status != maintenanceRunning) {
			return domain.ErrInvalidClaim
		}
		if run.Status == maintenanceRunning && run.StartedAt != nil && run.StartedAt.Add(validationRunLease).After(now) {
			return domain.ErrInvalidClaim
		}
		run.Attempts++
		if err := tx.Model(run).Updates(map[string]any{"status": maintenanceRunning, "attempts": run.Attempts, "started_at": now, "updated_at": now}).Error; err != nil {
			return err
		}
		return tx.Model(&Resource{}).Where("id = ?", resource.ID).Updates(map[string]any{"status": domain.StatusValidating, "updated_at": now}).Error
	})
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer stop()
		if err := s.releaseValidationAttempt(cleanup, task, run); err != nil {
			resultErr = errors.Join(resultErr, err)
		}
	}()

	loginErr := s.validateProtocol(ctx, *resource, task.RequestID)
	if maintenanceDeferred(loginErr) {
		cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer stop()
		err := s.deferBusyMaintenance(cleanup, resource.ID, task.ValidationGeneration, task.CredentialRevision, run.ID, run.Attempts, maintenanceKindValidation)
		if err == nil {
			committed = true
		}
		return err
	}
	if ctx.Err() != nil {
		return domain.ErrDependency
	}
	var failure *protocol.Failure
	if loginErr != nil && !errors.As(loginErr, &failure) {
		return domain.ErrDependency
	}
	err = s.commitValidation(ctx, task, run, failure)
	if err == nil {
		committed = true
	}
	return err
}

func (s *Service) releaseValidationAttempt(ctx context.Context, task daumapp.ValidationTaskPayload, claimed *MaintenanceRun) error {
	return s.transaction(ctx, func(_ context.Context, tx *gorm.DB) error {
		row, err := lockResource(tx, task.ResourceID, nil)
		if errors.Is(err, domain.ErrResourceMissing) {
			return nil
		}
		if err != nil {
			return err
		}
		if row.ValidationGeneration != task.ValidationGeneration || row.CredentialRevision != task.CredentialRevision || row.Status != domain.StatusValidating {
			return nil
		}
		return tx.Model(&MaintenanceRun{}).Where("id = ? AND status = ? AND attempts = ?", claimed.ID, maintenanceRunning, claimed.Attempts).
			Updates(map[string]any{"status": maintenanceQueued, "updated_at": s.Now().UTC()}).Error
	})
}

func (s *Service) commitValidation(ctx context.Context, task daumapp.ValidationTaskPayload, claimed *MaintenanceRun, failure *protocol.Failure) error {
	return s.transaction(ctx, func(ctx context.Context, tx *gorm.DB) error {
		resource, err := lockResource(tx, task.ResourceID, &task.OwnerUserID)
		if err != nil {
			return err
		}
		if resource.Status != domain.StatusValidating || resource.ValidationGeneration != task.ValidationGeneration || resource.CredentialRevision != task.CredentialRevision {
			return domain.ErrInvalidClaim
		}
		var run MaintenanceRun
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND status = ? AND attempts = ?", claimed.ID, maintenanceRunning, claimed.Attempts).Take(&run).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return domain.ErrInvalidClaim
			}
			return err
		}
		now := s.Now().UTC()
		status, runStatus, safeError := domain.StatusIdentifying, maintenanceSucceeded, ""
		failures, generation, quality := 0, resource.ValidationGeneration, 100
		if failure == nil {
			if err := ensureFixedAliasesTx(tx, *resource, now); err != nil {
				return err
			}
			if _, err := ensureMaintenanceRunTx(ctx, tx, resource.ID, generation, resource.CredentialRevision, maintenanceKindHistory, task.RequestID, now); err != nil {
				return err
			}
		} else {
			maximum := min(runtimeconfig.Int("resource_validation_max_failures", 3, 1), 100)
			failures, quality = min(resource.ValidationFailures+1, maximum), 0
			status, runStatus = domain.StatusAbnormal, maintenanceFailed
			safeError = safeSessionError(failure.Category + ": " + failure.SafeMessage)
			// Match Microsoft: only a retry with remaining budget returns to pending.
			switch failure.Category {
			case "invalid_credentials", "identity_mismatch", "account_disabled", "action_required":
				// Explicit account failures and human actions never retry automatically.
			default:
				if failures < maximum {
					status = domain.StatusPending
					generation++
				}
			}

		}
		if err := tx.Model(&Resource{}).Where("id = ?", resource.ID).Updates(map[string]any{"status": status, "validation_generation": generation, "validation_failures": failures, "quality_score": quality, "failure_category": permanentValidationFailure(failure), "last_safe_error": safeError, "last_checked_at": now, "version": resource.Version + 1, "updated_at": now}).Error; err != nil {
			return err
		}
		if err := tx.Model(&MaintenanceRun{}).Where("id = ?", run.ID).Updates(map[string]any{"status": runStatus, "last_safe_error": safeError, "finished_at": now, "updated_at": now}).Error; err != nil {
			return err
		}
		return bumpRoot(tx, resource.ID, now)
	})
}

func safeSessionError(message string) string {
	runes := []rune(strings.TrimSpace(message))
	return string(runes[:min(len(runes), 500)])
}

func permanentValidationFailure(failure *protocol.Failure) string {
	if failure == nil || failure.Retryable {
		return ""
	}
	switch failure.Category {
	case "invalid_credentials", "identity_mismatch", "account_disabled":
		return failure.Category
	}
	return ""
}
