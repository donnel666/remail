package icloud

import (
	"context"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	iCloudMaintenanceValidation = "validation"
	iCloudMaintenanceAlias      = "alias"

	iCloudMaintenanceQueued    = "queued"
	iCloudMaintenanceRunning   = "running"
	iCloudMaintenanceSucceeded = "succeeded"
	iCloudMaintenanceFailed    = "failed"
	iCloudMaintenanceCanceled  = "canceled"

	iCloudMaintenanceFinishTimeout = 5 * time.Second
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
			Where("resource_id = ? AND kind = ? AND validation_generation = ?", resourceID, iCloudMaintenanceValidation, generation).
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
		query = query.Where("kind <> ? OR validation_generation <> ?", iCloudMaintenanceValidation, exceptGeneration)
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
		query = query.Where("resource_id = ? AND kind = ? AND validation_generation = ?", task.ResourceID, iCloudMaintenanceValidation, task.ValidationGeneration)
	}
	result := query.Limit(1).Find(&run)
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected == 0 {
		return nil, nil
	}
	if run.ResourceID != task.ResourceID || run.Kind != iCloudMaintenanceValidation || run.ValidationGeneration != task.ValidationGeneration {
		return nil, nil
	}
	return &run, nil
}

func startICloudProvisionRunTx(ctx context.Context, tx *gorm.DB, resource iCloudResourceModel, now time.Time) (*iCloudMaintenanceRunModel, error) {
	if tx == nil || resource.ID == 0 || resource.CredentialRevision == 0 {
		return nil, ErrICloudValidationTemp
	}
	var generation uint64
	if err := tx.WithContext(ctx).Model(&iCloudMaintenanceRunModel{}).
		Where("resource_id = ? AND kind = ?", resource.ID, iCloudMaintenanceAlias).
		Select("COALESCE(MAX(validation_generation), 0)").Scan(&generation).Error; err != nil {
		return nil, err
	}
	run := iCloudMaintenanceRunModel{
		ResourceID: resource.ID, ValidationGeneration: generation + 1, Kind: iCloudMaintenanceAlias,
		Status: iCloudMaintenanceRunning, Attempts: 1, MaxAttempts: 1,
		CredentialRevision: resource.CredentialRevision, QueuedAt: now, StartedAt: &now,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := tx.WithContext(ctx).Create(&run).Error; err != nil {
		return nil, err
	}
	return &run, nil
}

func (s *Service) recoverStaleICloudProvisionRuns(ctx context.Context, now time.Time) error {
	if s == nil || s.db == nil {
		return ErrICloudValidationTemp
	}
	if err := recoverStaleICloudProvisionRunsTx(ctx, s.db, 0, now); err != nil {
		return ErrICloudValidationTemp
	}
	return nil
}

func recoverStaleICloudProvisionRunsTx(ctx context.Context, tx *gorm.DB, resourceID uint, now time.Time) error {
	if tx == nil {
		return ErrICloudValidationTemp
	}
	query := tx.WithContext(ctx).Model(&iCloudMaintenanceRunModel{}).
		Where("kind = ? AND status = ? AND started_at IS NOT NULL AND started_at <= ?",
			iCloudMaintenanceAlias, iCloudMaintenanceRunning, now.Add(-iCloudProvisionLease))
	if resourceID > 0 {
		query = query.Where("resource_id = ?", resourceID)
	}
	return query.Updates(map[string]any{
		"status": iCloudMaintenanceFailed, "last_safe_error": "Provision worker lease expired.",
		"finished_at": now, "updated_at": now,
	}).Error
}

func (s *Service) finishICloudProvisionRun(ctx context.Context, runID uint64, status, safeError string, now time.Time) error {
	if s == nil || s.db == nil {
		return ErrICloudValidationTemp
	}
	finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), iCloudMaintenanceFinishTimeout)
	defer cancel()
	if err := finishICloudMaintenanceRunTx(finishCtx, s.db, runID, status, safeError, now); err != nil {
		return ErrICloudValidationTemp
	}
	return nil
}

func finishICloudMaintenanceRunTx(ctx context.Context, tx *gorm.DB, runID uint64, status string, safeError string, now time.Time) error {
	if runID == 0 {
		return nil
	}
	updates := map[string]any{
		"status": status, "last_safe_error": safeICloudImportMessage(safeError), "updated_at": now,
	}
	if status == iCloudMaintenanceQueued {
		updates["finished_at"] = nil
	} else {
		updates["finished_at"] = now
	}
	result := tx.WithContext(ctx).Model(&iCloudMaintenanceRunModel{}).
		Where("id = ? AND status IN ?", runID, []string{iCloudMaintenanceQueued, iCloudMaintenanceRunning}).
		Updates(updates)
	return result.Error
}
