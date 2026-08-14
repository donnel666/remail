package icloud

import (
	"context"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	iCloudMaintenanceValidation = "validation"

	iCloudMaintenanceQueued    = "queued"
	iCloudMaintenanceRunning   = "running"
	iCloudMaintenanceSucceeded = "succeeded"
	iCloudMaintenanceFailed    = "failed"
	iCloudMaintenanceCanceled  = "canceled"
)

func ensureICloudMaintenanceRunTx(
	ctx context.Context,
	tx *gorm.DB,
	resourceID uint,
	generation uint64,
	credentialRevision uint64,
	attempts int,
	now time.Time,
) (*iCloudMaintenanceRunModel, error) {
	if tx == nil || resourceID == 0 || generation == 0 || credentialRevision == 0 {
		return nil, ErrICloudValidationTemp
	}
	if attempts < 0 {
		attempts = 0
	}
	if attempts > iCloudValidationMaxFailures {
		attempts = iCloudValidationMaxFailures
	}
	if err := cancelActiveICloudMaintenanceRunsTx(ctx, tx, resourceID, generation, now); err != nil {
		return nil, err
	}
	run := iCloudMaintenanceRunModel{
		ResourceID: resourceID, ValidationGeneration: generation, Kind: iCloudMaintenanceValidation,
		Status: iCloudMaintenanceQueued, Attempts: attempts, MaxAttempts: iCloudValidationMaxFailures,
		CredentialRevision: credentialRevision, QueuedAt: now, CreatedAt: now, UpdatedAt: now,
	}
	if err := tx.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&run).Error; err != nil {
		return nil, err
	}
	if run.ID == 0 {
		if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("resource_id = ? AND validation_generation = ?", resourceID, generation).
			Take(&run).Error; err != nil {
			return nil, err
		}
	}
	return &run, nil
}

func cancelActiveICloudMaintenanceRunsTx(ctx context.Context, tx *gorm.DB, resourceID uint, exceptGeneration uint64, now time.Time) error {
	query := tx.WithContext(ctx).Model(&iCloudMaintenanceRunModel{}).
		Where("resource_id = ? AND status IN ?", resourceID, []string{iCloudMaintenanceQueued, iCloudMaintenanceRunning})
	if exceptGeneration > 0 {
		query = query.Where("validation_generation <> ?", exceptGeneration)
	}
	return query.Updates(map[string]any{
		"status": iCloudMaintenanceCanceled, "finished_at": now, "updated_at": now,
	}).Error
}

func findICloudMaintenanceRunTx(ctx context.Context, tx *gorm.DB, task iCloudValidationTask) (*iCloudMaintenanceRunModel, error) {
	var run iCloudMaintenanceRunModel
	query := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"})
	if task.MaintenanceRunID > 0 {
		query = query.Where("id = ?", task.MaintenanceRunID)
	} else {
		query = query.Where("resource_id = ? AND validation_generation = ?", task.ResourceID, task.ValidationGeneration)
	}
	result := query.Limit(1).Find(&run)
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected == 0 {
		return nil, nil
	}
	if run.ResourceID != task.ResourceID || run.ValidationGeneration != task.ValidationGeneration {
		return nil, nil
	}
	return &run, nil
}

func finishICloudMaintenanceRunTx(ctx context.Context, tx *gorm.DB, runID uint64, status string, safeError string, now time.Time) error {
	if runID == 0 {
		return nil
	}
	result := tx.WithContext(ctx).Model(&iCloudMaintenanceRunModel{}).
		Where("id = ? AND status IN ?", runID, []string{iCloudMaintenanceQueued, iCloudMaintenanceRunning}).
		Updates(map[string]any{
			"status": status, "last_safe_error": safeICloudImportMessage(safeError),
			"finished_at": now, "updated_at": now,
		})
	return result.Error
}
