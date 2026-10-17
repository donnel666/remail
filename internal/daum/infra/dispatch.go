package infra

import (
	"context"
	"errors"
	"time"

	daumapp "github.com/donnel666/remail/internal/daum/app"
	"github.com/donnel666/remail/internal/daum/domain"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (s *Service) DispatchPendingValidations(ctx context.Context, q daumapp.Queue, limit int) (int, error) {
	if s == nil || s.DB == nil || q == nil {
		return 0, domain.ErrDependency
	}
	if limit <= 0 {
		limit = runtimeconfig.Int("validation_dispatch_maximum", 128, 1)
	}
	limit = min(max(limit, 1), 10000)
	if s.BackgroundExecution != nil {
		limit = min(limit, max(0, s.BackgroundExecution.Snapshot().Limit))
	}
	if limit == 0 {
		return 0, nil
	}
	var rows []Resource
	if err := daumResourceQuery(s.dbFor(ctx)).Where("(status = ? OR (status = ? AND updated_at < ?))", domain.StatusPending, domain.StatusValidating, s.Now().UTC().Add(-validationRunLease)).
		Where("NOT EXISTS (SELECT 1 FROM daum_maintenance_runs AS run WHERE run.resource_id = daum_resources.id AND run.validation_generation = daum_resources.validation_generation AND run.kind = 'validation' AND run.status IN ('succeeded','failed','uncertain','canceled'))").Order("updated_at ASC, id ASC").Limit(limit).Find(&rows).Error; err != nil {
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
		task := daumapp.ValidationTaskPayload{ResourceID: row.ID, OwnerUserID: row.OwnerUserID, ValidationGeneration: row.ValidationGeneration, CredentialRevision: row.CredentialRevision, MaintenanceRunID: run.ID, RequestID: row.ValidationRequestID}
		queuedAt, err := s.reserveValidationDispatch(ctx, row, run.ID)
		if err != nil {
			result = errors.Join(result, err)
			continue
		}
		accepted, err := daumapp.EnqueueValidationAccepted(ctx, q, task)
		if err != nil {
			result = errors.Join(result, err)
			continue
		}
		if !accepted || queuedAt == nil {
			queued++
			continue
		}
		// A worker that already deferred keeps started_at, so enqueue cannot strand it.
		update := s.dbFor(ctx).Model(&Resource{}).
			Where("id = ? AND status = ? AND validation_generation = ? AND credential_revision = ?", row.ID, domain.StatusPending, row.ValidationGeneration, row.CredentialRevision).
			Where("EXISTS (SELECT 1 FROM daum_maintenance_runs AS run WHERE run.id = ? AND run.status = 'queued' AND run.started_at IS NULL AND run.queued_at = ?)", run.ID, *queuedAt).
			Updates(map[string]any{"status": domain.StatusValidating, "updated_at": s.Now().UTC()})
		if update.Error != nil {
			result = errors.Join(result, update.Error)
			continue
		}
		queued++
	}
	return queued, result
}

func (s *Service) reserveValidationDispatch(ctx context.Context, resource Resource, runID uint64) (*time.Time, error) {
	var queuedAt *time.Time
	err := s.transaction(ctx, func(_ context.Context, tx *gorm.DB) error {
		row, err := lockResource(tx, resource.ID, nil)
		if err != nil {
			return err
		}
		if row.ValidationGeneration != resource.ValidationGeneration || row.CredentialRevision != resource.CredentialRevision || (row.Status != domain.StatusPending && row.Status != domain.StatusValidating) {
			return domain.ErrInvalidClaim
		}
		var run MaintenanceRun
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", runID).Take(&run).Error; err != nil {
			return err
		}
		if run.Status != maintenanceQueued {
			return nil
		}
		now := s.Now().UTC().Truncate(time.Second)
		if !now.After(run.QueuedAt) {
			now = run.QueuedAt.Add(time.Second).Truncate(time.Second)
		}
		queuedAt = &now
		return tx.Model(&MaintenanceRun{}).Where("id = ?", run.ID).Updates(map[string]any{"queued_at": now, "started_at": nil}).Error
	})
	return queuedAt, err
}
func (s *Service) DispatchPendingHistory(ctx context.Context, q daumapp.Queue, limit int) (int, error) {
	if s == nil || s.DB == nil || q == nil {
		return 0, domain.ErrDependency
	}
	limit = min(max(limit, 1), 100)
	var rows []Resource
	if err := daumResourceQuery(s.dbFor(ctx)).Where("status = ?", domain.StatusIdentifying).
		Where("NOT EXISTS (SELECT 1 FROM daum_maintenance_runs AS run WHERE run.resource_id = daum_resources.id AND run.validation_generation = daum_resources.validation_generation AND run.kind = 'history' AND run.status IN ('succeeded','failed','uncertain','canceled'))").Order("updated_at ASC, id ASC").Limit(limit).Find(&rows).Error; err != nil {
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
		if err := daumapp.EnqueueHistory(ctx, q, daumapp.HistoryTaskPayload{ResourceID: row.ID, OwnerUserID: row.OwnerUserID, ValidationGeneration: row.ValidationGeneration, CredentialRevision: row.CredentialRevision, MaintenanceRunID: run.ID, RequestID: row.ValidationRequestID}); err != nil {
			result = errors.Join(result, err)
			continue
		}
		queued++
	}
	return queued, result
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
