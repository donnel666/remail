package infra

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/donnel666/remail/internal/proto/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	maintenanceKindValidation = "validation"
	maintenanceKindHistory    = "history"
	maintenanceQueued         = "queued"
	maintenanceRunning        = "running"
	maintenanceSucceeded      = "succeeded"
	maintenanceFailed         = "failed"
	maintenanceUncertain      = "uncertain"
	maintenanceCanceled       = "canceled"
)

type MaintenanceRun struct {
	ID                   uint64     `gorm:"column:id;primaryKey"`
	ResourceID           uint       `gorm:"column:resource_id"`
	ValidationGeneration uint64     `gorm:"column:validation_generation"`
	Kind                 string     `gorm:"column:kind"`
	Status               string     `gorm:"column:status"`
	Attempts             int        `gorm:"column:attempts"`
	MaxAttempts          int        `gorm:"column:max_attempts"`
	CredentialRevision   uint64     `gorm:"column:credential_revision"`
	RequestID            string     `gorm:"column:request_id"`
	LastSafeError        string     `gorm:"column:last_safe_error"`
	QueuedAt             time.Time  `gorm:"column:queued_at"`
	StartedAt            *time.Time `gorm:"column:started_at"`
	FinishedAt           *time.Time `gorm:"column:finished_at"`
	CreatedAt            time.Time  `gorm:"column:created_at"`
	UpdatedAt            time.Time  `gorm:"column:updated_at"`
}

func (MaintenanceRun) TableName() string { return "proto_maintenance_runs" }

func validMaintenanceKind(kind string) bool {
	return kind == maintenanceKindValidation || kind == maintenanceKindHistory
}

func ensureMaintenanceRunTx(ctx context.Context, tx *gorm.DB, resourceID uint, generation, credentialRevision uint64, kind, requestID string, now time.Time) (*MaintenanceRun, error) {
	if !validMaintenanceKind(kind) || resourceID == 0 || generation == 0 || credentialRevision == 0 {
		return nil, domain.ErrInvalidResource
	}
	var active MaintenanceRun
	err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("resource_id = ? AND validation_generation = ? AND kind = ?", resourceID, generation, kind).
		Order("id DESC").Take(&active).Error
	if err == nil {
		return &active, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	// A newer fenced generation supersedes older queued/running work. Keep the
	// old row as an audit fact and make its terminal state explicit.
	if err := tx.WithContext(ctx).Model(&MaintenanceRun{}).
		Where("resource_id = ? AND kind = ? AND status IN ?", resourceID, kind, []string{maintenanceQueued, maintenanceRunning}).
		Updates(map[string]any{"status": maintenanceCanceled, "last_safe_error": "Superseded by a newer Proto maintenance generation.", "finished_at": now, "updated_at": now}).Error; err != nil {
		return nil, err
	}
	run := &MaintenanceRun{
		ResourceID: resourceID, ValidationGeneration: generation, Kind: kind,
		Status: maintenanceQueued, Attempts: 0, MaxAttempts: 3,
		CredentialRevision: credentialRevision, RequestID: strings.TrimSpace(requestID),
		QueuedAt: now, CreatedAt: now, UpdatedAt: now,
	}
	if err := tx.WithContext(ctx).Create(run).Error; err != nil {
		return nil, err
	}
	return run, nil
}

func (s *Service) ensureMaintenanceRun(ctx context.Context, resourceID uint, generation, credentialRevision uint64, kind, requestID string) (*MaintenanceRun, error) {
	if s == nil || s.DB == nil {
		return nil, domain.ErrInvalidResource
	}
	now := s.Now().UTC()
	var run *MaintenanceRun
	err := s.transaction(ctx, func(ctx context.Context, tx *gorm.DB) error {
		row, err := lockResource(tx, resourceID, nil)
		if err != nil {
			return err
		}
		if row.ValidationGeneration != generation || row.CredentialRevision != credentialRevision {
			return domain.ErrInvalidClaim
		}
		run, err = ensureMaintenanceRunTx(ctx, tx, resourceID, generation, credentialRevision, kind, requestID, now)
		return err
	})
	return run, err
}

func (s *Service) EnsureValidationRun(ctx context.Context, resourceID uint, generation, credentialRevision uint64, requestID string) (*MaintenanceRun, error) {
	return s.ensureMaintenanceRun(ctx, resourceID, generation, credentialRevision, maintenanceKindValidation, requestID)
}

func (s *Service) EnsureHistoryRun(ctx context.Context, resourceID uint, generation, credentialRevision uint64, requestID string) (*MaintenanceRun, error) {
	return s.ensureMaintenanceRun(ctx, resourceID, generation, credentialRevision, maintenanceKindHistory, requestID)
}

func (s *Service) StartMaintenanceRun(ctx context.Context, runID uint64, resourceID uint, generation uint64, kind string) error {
	return s.startMaintenanceRun(ctx, runID, resourceID, generation, kind)
}

func (s *Service) FinishMaintenanceRun(ctx context.Context, runID uint64, status, safeError string) error {
	return s.finishMaintenanceRun(ctx, runID, status, safeError)
}

func (s *Service) FindMaintenanceRun(ctx context.Context, resourceID uint, generation uint64, kind string) (*MaintenanceRun, error) {
	if s == nil || s.DB == nil || resourceID == 0 || generation == 0 || !validMaintenanceKind(kind) {
		return nil, domain.ErrInvalidResource
	}
	var run MaintenanceRun
	if err := s.DB.WithContext(ctx).Where("resource_id = ? AND validation_generation = ? AND kind = ?", resourceID, generation, kind).Order("id DESC").Take(&run).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, domain.ErrResourceMissing
		}
		return nil, err
	}
	return &run, nil
}

type MaintenancePage struct {
	Items []MaintenanceRun
	Total int64
}

func (s *Service) ListMaintenanceRuns(ctx context.Context, resourceID uint, offset, limit int) (*MaintenancePage, error) {
	if s == nil || s.DB == nil || resourceID == 0 || offset < 0 || limit < 1 || limit > 100 {
		return nil, domain.ErrInvalidResource
	}
	q := s.DB.WithContext(ctx).Model(&MaintenanceRun{}).Where("resource_id = ?", resourceID)
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, err
	}
	var items []MaintenanceRun
	if err := q.Order("id DESC").Offset(offset).Limit(limit).Find(&items).Error; err != nil {
		return nil, err
	}
	return &MaintenancePage{Items: items, Total: total}, nil
}

func (s *Service) startMaintenanceRun(ctx context.Context, runID uint64, resourceID uint, generation uint64, kind string) error {
	if s == nil || s.DB == nil || runID == 0 || resourceID == 0 || generation == 0 || !validMaintenanceKind(kind) {
		return domain.ErrInvalidResource
	}
	now := s.Now().UTC()
	result := s.DB.WithContext(ctx).Model(&MaintenanceRun{}).
		Where("id = ? AND resource_id = ? AND validation_generation = ? AND kind = ? AND status IN ?", runID, resourceID, generation, kind, []string{maintenanceQueued, maintenanceRunning}).
		Updates(map[string]any{"status": maintenanceRunning, "attempts": gorm.Expr("CASE WHEN attempts < max_attempts THEN attempts + 1 ELSE max_attempts END"), "started_at": now, "updated_at": now})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return domain.ErrResourceMissing
	}
	return nil
}

func (s *Service) finishMaintenanceRun(ctx context.Context, runID uint64, status, safeError string) error {
	if s == nil || s.DB == nil || runID == 0 || !validMaintenanceStatus(status) {
		return domain.ErrInvalidResource
	}
	now := s.Now().UTC()
	result := s.DB.WithContext(ctx).Model(&MaintenanceRun{}).
		Where("id = ? AND status IN ?", runID, []string{maintenanceQueued, maintenanceRunning}).
		Updates(map[string]any{"status": status, "last_safe_error": strings.TrimSpace(safeError), "finished_at": now, "updated_at": now})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return domain.ErrResourceMissing
	}
	return nil
}

func validMaintenanceStatus(status string) bool {
	switch status {
	case maintenanceSucceeded, maintenanceFailed, maintenanceUncertain, maintenanceCanceled:
		return true
	default:
		return false
	}
}

func cancelMaintenanceRunsTx(tx *gorm.DB, resourceID uint, safeError string, now time.Time) error {
	if resourceID == 0 {
		return nil
	}
	return tx.Model(&MaintenanceRun{}).
		Where("resource_id = ? AND status IN ?", resourceID, []string{maintenanceQueued, maintenanceRunning}).
		Updates(map[string]any{"status": maintenanceCanceled, "last_safe_error": strings.TrimSpace(safeError), "finished_at": now, "updated_at": now}).Error
}
