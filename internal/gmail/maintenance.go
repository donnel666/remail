package gmail

import (
	"context"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	gmailMaintenanceValidation = "validation"
	gmailMaintenanceHistory    = "history"

	gmailMaintenanceQueued    = "queued"
	gmailMaintenanceRunning   = "running"
	gmailMaintenanceSucceeded = "succeeded"
	gmailMaintenanceFailed    = "failed"
	gmailMaintenanceCanceled  = "canceled"
)

func ensureGmailMaintenanceRunTx(
	ctx context.Context,
	tx *gorm.DB,
	resourceID uint,
	generation uint64,
	kind string,
	credentialRevision uint64,
	attempts int,
	now time.Time,
) (*gmailMaintenanceRunModel, error) {
	if tx == nil || resourceID == 0 || generation == 0 || credentialRevision == 0 ||
		kind != gmailMaintenanceValidation && kind != gmailMaintenanceHistory {
		return nil, ErrLocalValidationConflict
	}
	var run gmailMaintenanceRunModel
	result := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("resource_id = ? AND validation_generation = ? AND kind = ? AND status IN ?",
			resourceID, generation, kind, []string{gmailMaintenanceQueued, gmailMaintenanceRunning}).
		Order("id DESC").Limit(1).Find(&run)
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected > 0 {
		return &run, nil
	}
	if err := cancelActiveGmailMaintenanceRunsTx(ctx, tx, resourceID, now, "Superseded by a newer Gmail maintenance run."); err != nil {
		return nil, err
	}
	attempts = max(0, min(attempts, localGmailValidationMaxFailures))
	run = gmailMaintenanceRunModel{
		ResourceID: resourceID, ValidationGeneration: generation, Kind: kind,
		Status: gmailMaintenanceQueued, Attempts: attempts, MaxAttempts: localGmailValidationMaxFailures,
		CredentialRevision: credentialRevision, QueuedAt: now, CreatedAt: now, UpdatedAt: now,
	}
	if err := tx.WithContext(ctx).Create(&run).Error; err != nil {
		return nil, err
	}
	return &run, nil
}

func findGmailMaintenanceRunTx(
	ctx context.Context,
	tx *gorm.DB,
	runID uint64,
	resourceID uint,
	generation uint64,
	kind string,
) (*gmailMaintenanceRunModel, error) {
	if tx == nil || resourceID == 0 || generation == 0 {
		return nil, nil
	}
	var run gmailMaintenanceRunModel
	query := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"})
	if runID > 0 {
		query = query.Where("id = ?", runID)
	} else {
		query = query.Where("resource_id = ? AND validation_generation = ? AND kind = ? AND status IN ?",
			resourceID, generation, kind, []string{gmailMaintenanceQueued, gmailMaintenanceRunning}).Order("id DESC")
	}
	result := query.Limit(1).Find(&run)
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected == 0 {
		return nil, nil
	}
	if run.ResourceID != resourceID || run.ValidationGeneration != generation || run.Kind != kind {
		return nil, nil
	}
	return &run, nil
}

func cancelActiveGmailMaintenanceRunsTx(
	ctx context.Context,
	tx *gorm.DB,
	resourceID uint,
	now time.Time,
	safeError string,
) error {
	if tx == nil || resourceID == 0 {
		return nil
	}
	return tx.WithContext(ctx).Model(&gmailMaintenanceRunModel{}).
		Where("resource_id = ? AND status IN ?", resourceID, []string{gmailMaintenanceQueued, gmailMaintenanceRunning}).
		Updates(map[string]any{
			"status": gmailMaintenanceCanceled, "last_safe_error": safeGmailMaintenanceMessage(safeError),
			"finished_at": now, "updated_at": now,
		}).Error
}

func (s *Service) ensureGmailMaintenanceRun(
	ctx context.Context,
	resourceID uint,
	generation uint64,
	kind string,
	credentialRevision uint64,
	attempts int,
) (*gmailMaintenanceRunModel, error) {
	if s == nil || s.db == nil {
		return nil, ErrLocalValidationDependency
	}
	var run *gmailMaintenanceRunModel
	err := s.dbFor(ctx).Transaction(func(tx *gorm.DB) error {
		var resource localResourceModel
		result := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
			Select("id", "status", "validation_generation", "credential_revision").
			Where("id = ?", resourceID).Limit(1).Find(&resource)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 || resource.ValidationGeneration != generation || resource.CredentialRevision != credentialRevision {
			return ErrLocalValidationConflict
		}
		if kind == gmailMaintenanceValidation && resource.Status != LocalResourcePending ||
			kind == gmailMaintenanceHistory && resource.Status != LocalResourceIdentifying &&
				resource.Status != LocalResourceNormal && resource.Status != localResourceRollbackNormal {
			return ErrLocalValidationConflict
		}
		var err error
		run, err = ensureGmailMaintenanceRunTx(
			ctx, tx, resourceID, generation, kind, credentialRevision, attempts, s.now().UTC(),
		)
		return err
	})
	return run, err
}

func (s *Service) startGmailMaintenanceRun(
	ctx context.Context,
	runID uint64,
	resourceID uint,
	generation uint64,
	kind string,
	credentialRevision uint64,
) (uint64, bool, error) {
	if s == nil || s.db == nil {
		return 0, false, ErrLocalValidationDependency
	}
	now := s.now().UTC()
	startedID := uint64(0)
	started := false
	err := s.dbFor(ctx).Transaction(func(tx *gorm.DB) error {
		run, err := findGmailMaintenanceRunTx(ctx, tx, runID, resourceID, generation, kind)
		if err != nil {
			return err
		}
		if run == nil {
			run, err = ensureGmailMaintenanceRunTx(ctx, tx, resourceID, generation, kind, credentialRevision, 0, now)
			if err != nil {
				return err
			}
		}
		startedID = run.ID
		if run.Status != gmailMaintenanceQueued && run.Status != gmailMaintenanceRunning {
			return nil
		}
		attempts := min(run.Attempts+1, run.MaxAttempts)
		updates := map[string]any{
			"status": gmailMaintenanceRunning, "attempts": attempts,
			"finished_at": nil, "last_safe_error": "", "updated_at": now,
		}
		if run.StartedAt == nil {
			updates["started_at"] = now
		}
		if err := tx.Model(&gmailMaintenanceRunModel{}).Where("id = ?", run.ID).Updates(updates).Error; err != nil {
			return err
		}
		started = true
		return nil
	})
	return startedID, started, err
}

func finishGmailMaintenanceRunTx(
	ctx context.Context,
	tx *gorm.DB,
	runID uint64,
	status string,
	safeError string,
	now time.Time,
) error {
	if tx == nil || runID == 0 {
		return nil
	}
	return tx.WithContext(ctx).Model(&gmailMaintenanceRunModel{}).
		Where("id = ? AND status IN ?", runID, []string{gmailMaintenanceQueued, gmailMaintenanceRunning}).
		Updates(map[string]any{
			"status": status, "last_safe_error": safeGmailMaintenanceMessage(safeError),
			"finished_at": now, "updated_at": now,
		}).Error
}

func (s *Service) finishGmailMaintenanceRun(ctx context.Context, runID uint64, status, safeError string) error {
	if s == nil || s.db == nil || runID == 0 {
		return nil
	}
	return finishGmailMaintenanceRunTx(ctx, s.dbFor(ctx), runID, status, safeError, s.now().UTC())
}

func (s *Service) finishGmailMaintenanceRunForTask(
	ctx context.Context,
	runID uint64,
	resourceID uint,
	generation uint64,
	kind string,
	status string,
	safeError string,
) error {
	if s == nil || s.db == nil || resourceID == 0 || generation == 0 {
		return nil
	}
	return s.dbFor(ctx).Transaction(func(tx *gorm.DB) error {
		run, err := findGmailMaintenanceRunTx(ctx, tx, runID, resourceID, generation, kind)
		if err != nil || run == nil {
			return err
		}
		return finishGmailMaintenanceRunTx(ctx, tx, run.ID, status, safeError, s.now().UTC())
	})
}

func safeGmailMaintenanceMessage(value string) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if len(runes) > 500 {
		value = string(runes[:500])
	}
	return value
}
