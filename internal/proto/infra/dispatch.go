package infra

import (
	"context"
	"errors"
	"time"

	protoapp "github.com/donnel666/remail/internal/proto/app"
	"github.com/donnel666/remail/internal/proto/domain"
	"gorm.io/gorm"
)

func (s *Service) DispatchPendingValidations(ctx context.Context, q protoapp.Queue, limit int) (int, error) {
	if s == nil || s.DB == nil || q == nil {
		return 0, domain.ErrDependency
	}
	limit = min(max(limit, 1), 100)
	if s.BackgroundExecution != nil {
		limit = min(limit, max(1, s.BackgroundExecution.Snapshot().Limit))
	}
	var rows []Resource
	if err := protoResourceQuery(s.dbFor(ctx)).Where("(status = ? OR (status = ? AND updated_at < ?))", domain.StatusPending, domain.StatusValidating, s.Now().UTC().Add(-16*time.Minute)).
		Where("NOT EXISTS (SELECT 1 FROM proto_maintenance_runs AS run WHERE run.resource_id = proto_resources.id AND run.validation_generation = proto_resources.validation_generation AND run.kind = 'validation' AND run.status IN ('succeeded','failed','uncertain','canceled'))").Order("updated_at ASC, id ASC").Limit(limit).Find(&rows).Error; err != nil {
		return 0, err
	}
	queued := 0
	var result error
	for _, row := range rows {
		run, err := s.EnsureValidationRun(ctx, row.ID, row.ValidationGeneration, row.CredentialRevision, row.ValidationRequestID)
		if err != nil {
			result = errors.Join(result, err)
			continue
		}
		task := protoapp.ValidationTaskPayload{ResourceID: row.ID, OwnerUserID: row.OwnerUserID, ValidationGeneration: row.ValidationGeneration, CredentialRevision: row.CredentialRevision, MaintenanceRunID: run.ID, RequestID: row.ValidationRequestID}
		if err := protoapp.EnqueueValidation(ctx, q, task); err != nil {
			result = errors.Join(result, err)
			continue
		}
		update := s.dbFor(ctx).Model(&Resource{}).
			Where("id = ? AND status = ? AND validation_generation = ? AND credential_revision = ?", row.ID, domain.StatusPending, row.ValidationGeneration, row.CredentialRevision).
			Where("EXISTS (SELECT 1 FROM proto_maintenance_runs AS run WHERE run.id = ? AND run.status IN ('queued','running'))", run.ID).
			Updates(map[string]any{"status": domain.StatusValidating, "updated_at": s.Now().UTC()})
		if update.Error != nil {
			result = errors.Join(result, update.Error)
			continue
		}
		queued++
	}
	return queued, result
}
func (s *Service) DispatchPendingHistory(ctx context.Context, q protoapp.Queue, limit int) (int, error) {
	if s == nil || s.DB == nil || q == nil {
		return 0, domain.ErrDependency
	}
	limit = min(max(limit, 1), 100)
	var rows []Resource
	if err := protoResourceQuery(s.dbFor(ctx)).Where("status = ?", domain.StatusIdentifying).
		Where("NOT EXISTS (SELECT 1 FROM proto_maintenance_runs AS run WHERE run.resource_id = proto_resources.id AND run.validation_generation = proto_resources.validation_generation AND run.kind = 'history' AND run.status IN ('succeeded','failed','uncertain','canceled'))").Order("updated_at ASC, id ASC").Limit(limit).Find(&rows).Error; err != nil {
		return 0, err
	}
	queued := 0
	var result error
	for _, row := range rows {
		run, err := s.EnsureHistoryRun(ctx, row.ID, row.ValidationGeneration, row.CredentialRevision, row.ValidationRequestID)
		if err != nil {
			result = errors.Join(result, err)
			continue
		}
		if err := protoapp.EnqueueHistory(ctx, q, protoapp.HistoryTaskPayload{ResourceID: row.ID, OwnerUserID: row.OwnerUserID, ValidationGeneration: row.ValidationGeneration, CredentialRevision: row.CredentialRevision, MaintenanceRunID: run.ID, RequestID: row.ValidationRequestID}); err != nil {
			result = errors.Join(result, err)
			continue
		}
		queued++
	}
	return queued, result
}
func (s *Service) ProcessValidationTODO(ctx context.Context, task protoapp.ValidationTaskPayload) error {
	return s.transaction(ctx, func(ctx context.Context, tx *gorm.DB) error {
		row, err := lockResource(tx, task.ResourceID, &task.OwnerUserID)
		if err != nil {
			return err
		}
		if row.ValidationGeneration != task.ValidationGeneration || row.CredentialRevision != task.CredentialRevision || (row.Status != domain.StatusPending && row.Status != domain.StatusValidating) {
			return domain.ErrInvalidClaim
		}
		run, err := ensureMaintenanceRunTx(ctx, tx, row.ID, row.ValidationGeneration, row.CredentialRevision, maintenanceKindValidation, task.RequestID, s.Now().UTC())
		if err != nil {
			return err
		}
		if run.Status != maintenanceQueued && run.Status != maintenanceRunning {
			return domain.ErrInvalidClaim
		}
		if err := tx.Model(&Resource{}).Where("id = ?", row.ID).Update("status", domain.StatusValidating).Error; err != nil {
			return err
		}
		if err := tx.Model(&MaintenanceRun{}).Where("id = ?", run.ID).Updates(map[string]any{"status": maintenanceRunning, "attempts": gorm.Expr("attempts + 1"), "started_at": s.Now().UTC()}).Error; err != nil {
			return err
		}
		// TODO: invoke the Proto credential validator. Unimplemented is a terminal
		// observation for this generation, never a successful credential check.
		return s.validationResult(ctx, row.ID, row.ValidationGeneration, row.CredentialRevision, domain.StatusPending, "proto_validation_todo")
	})
}
func (s *Service) ProcessHistoryTODO(ctx context.Context, task protoapp.HistoryTaskPayload) error {
	return s.transaction(ctx, func(ctx context.Context, tx *gorm.DB) error {
		row, err := lockResource(tx, task.ResourceID, &task.OwnerUserID)
		if err != nil {
			return err
		}
		if row.Status != domain.StatusIdentifying || row.ValidationGeneration != task.ValidationGeneration || row.CredentialRevision != task.CredentialRevision {
			return domain.ErrInvalidClaim
		}
		run, err := ensureMaintenanceRunTx(ctx, tx, row.ID, row.ValidationGeneration, row.CredentialRevision, maintenanceKindHistory, task.RequestID, s.Now().UTC())
		if err != nil {
			return err
		}
		if run.Status != maintenanceQueued && run.Status != maintenanceRunning {
			return domain.ErrInvalidClaim
		}
		if err := tx.Model(&MaintenanceRun{}).Where("id = ?", run.ID).Updates(map[string]any{"status": maintenanceRunning, "attempts": gorm.Expr("attempts + 1"), "started_at": s.Now().UTC()}).Error; err != nil {
			return err
		}
		// TODO: fetch and identify Proto history, then atomically commit history
		// through the shared Trade/Alloc contract before calling CompleteHistorySuccess.
		return s.historyResult(ctx, row.ID, row.ValidationGeneration, false)
	})
}

func (s *Service) ReleaseMaintenanceAssignment(ctx context.Context, id uint, generation, revision uint64, kind string) error {
	return s.transaction(ctx, func(_ context.Context, tx *gorm.DB) error {
		row, err := lockResource(tx, id, nil)
		if err != nil {
			return err
		}
		expected := domain.StatusValidating
		if kind == maintenanceKindHistory {
			expected = domain.StatusIdentifying
		} else if kind != maintenanceKindValidation {
			return domain.ErrInvalidResource
		}
		if row.Status != expected || row.ValidationGeneration != generation || row.CredentialRevision != revision {
			return domain.ErrInvalidClaim
		}
		now := s.Now().UTC()
		if err := cancelMaintenanceRunsTx(tx, id, "Infrastructure retry exhausted; a new generation will resume.", now); err != nil {
			return err
		}
		nextStatus := domain.StatusPending
		if kind == maintenanceKindHistory {
			nextStatus = domain.StatusIdentifying
		}
		if err := tx.Model(&Resource{}).Where("id = ?", id).Updates(map[string]any{"status": nextStatus, "validation_generation": generation + 1, "last_safe_error": "Maintenance infrastructure is temporarily unavailable.", "version": row.Version + 1, "updated_at": now}).Error; err != nil {
			return err
		}
		return bumpRoot(tx, id, now)
	})
}
