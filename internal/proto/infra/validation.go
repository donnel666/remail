package infra

import (
	"context"
	"errors"
	"strings"
	"time"

	protoapp "github.com/donnel666/remail/internal/proto/app"
	"github.com/donnel666/remail/internal/proto/domain"
	"github.com/donnel666/remail/internal/proto/infra/proton"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (s *Service) ProcessValidation(ctx context.Context, task protoapp.ValidationTaskPayload) (resultErr error) {
	if s == nil || s.Protocol == nil || s.SessionSecret == "" {
		return domain.ErrDependency
	}
	ctx, cancel := context.WithTimeout(ctx, sessionOperationTimeout)
	defer cancel()
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
		if run.Status == maintenanceRunning && run.StartedAt != nil && run.StartedAt.Add(sessionLeaseDuration).After(now) {
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

	// Password login and key unlocking must never hold database row locks.
	session, loginErr := s.loginProtocol(ctx, *resource, task.RequestID)
	if ctx.Err() != nil {
		return domain.ErrDependency
	}
	var failure *proton.Failure
	if loginErr != nil && !errors.As(loginErr, &failure) {
		return domain.ErrDependency
	}
	if loginErr == nil && !validSession(session, resource.EmailAddress) {
		failure = &proton.Failure{Category: "invalid_session", SafeMessage: "Proto login did not produce usable mailbox keys."}
	}
	var payload []byte
	if failure == nil {
		payload, err = s.encryptSession(resource.ID, task.CredentialRevision, session)
		if err != nil {
			return err
		}
	}
	err = s.commitValidation(ctx, task, run, payload, failure)
	if err == nil {
		committed = true
	}
	return err
}

func (s *Service) releaseValidationAttempt(ctx context.Context, task protoapp.ValidationTaskPayload, claimed *MaintenanceRun) error {
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

func (s *Service) commitValidation(ctx context.Context, task protoapp.ValidationTaskPayload, claimed *MaintenanceRun, payload []byte, failure *proton.Failure) error {
	return s.transaction(ctx, func(ctx context.Context, tx *gorm.DB) error {
		resource, err := lockResource(tx, task.ResourceID, &task.OwnerUserID)
		if err != nil {
			return err
		}
		if resource.Status != domain.StatusValidating || resource.ValidationGeneration != task.ValidationGeneration || resource.CredentialRevision != task.CredentialRevision {
			return domain.ErrInvalidClaim
		}
		stored, err := lockSessionTx(tx, resource.ID)
		if err != nil && !errors.Is(err, ErrSessionUnavailable) {
			return err
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
			version := uint64(1)
			if stored != nil {
				version = stored.Version + 1
			}
			session := sessionRecord{ResourceID: resource.ID, CredentialRevision: resource.CredentialRevision, Version: version, Payload: payload, CreatedAt: now, UpdatedAt: now}
			if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "resource_id"}}, DoUpdates: clause.AssignmentColumns([]string{"credential_revision", "version", "payload", "lease_token", "lease_expires_at", "updated_at"})}).Create(&session).Error; err != nil {
				return err
			}
			if _, err := ensureMaintenanceRunTx(ctx, tx, resource.ID, generation, resource.CredentialRevision, maintenanceKindHistory, task.RequestID, now); err != nil {
				return err
			}
		} else {
			maximum := min(runtimeconfig.Int("resource_validation_max_failures", 3, 1), 100)
			failures, quality = min(resource.ValidationFailures+1, maximum), 0
			status, runStatus = domain.StatusPending, maintenanceFailed
			safeError = safeSessionError(failure.Category + ": " + failure.SafeMessage)
			// A failed validation is not necessarily a permanently invalid account.
			// Reads project pending + this generation's failed run as validation_failed;
			// Trade's permanent-refund scanner continues to consume only abnormal.
			switch failure.Category {
			case "invalid_credentials", "identity_mismatch":
				status = domain.StatusAbnormal
			case "action_required":
				// Human verification must not be retried automatically, even if an
				// upstream adapter incorrectly marks this failure retryable.
			default:
				if failure.Retryable && failures < maximum {
					generation++
				}
			}
			// A malformed response or temporary login failure does not invalidate
			// previously verified keys used by existing orders. New allocations
			// remain blocked by pending; credential edits already remove old keys.
			if status == domain.StatusAbnormal {
				if err := deleteSessionTx(tx, resource.ID); err != nil {
					return err
				}
			}
		}
		if err := tx.Model(&Resource{}).Where("id = ?", resource.ID).Updates(map[string]any{"status": status, "validation_generation": generation, "validation_failures": failures, "quality_score": quality, "last_safe_error": safeError, "last_checked_at": now, "version": resource.Version + 1, "updated_at": now}).Error; err != nil {
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
