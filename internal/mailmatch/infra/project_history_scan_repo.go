package infra

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/donnel666/remail/internal/mailmatch/app"
	"github.com/donnel666/remail/internal/mailmatch/domain"
	"github.com/donnel666/remail/internal/platform"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type ProjectHistoryScanStateModel struct {
	ProjectID     uint       `gorm:"primaryKey;column:id"`
	Status        string     `gorm:"column:history_scan_status"`
	Generation    uint64     `gorm:"column:history_scan_generation"`
	Failures      int        `gorm:"column:history_scan_failures"`
	ScannedCount  int        `gorm:"column:history_scan_scanned_count"`
	MatchedCount  int        `gorm:"column:history_scan_matched_count"`
	SkippedCount  int        `gorm:"column:history_scan_skipped_count"`
	RequestID     string     `gorm:"column:history_scan_request_id"`
	LastSafeError string     `gorm:"column:history_scan_last_safe_error"`
	RequestedAt   *time.Time `gorm:"column:history_scan_requested_at"`
	StartedAt     *time.Time `gorm:"column:history_scan_started_at"`
	FinishedAt    *time.Time `gorm:"column:history_scan_finished_at"`
	UpdatedAt     time.Time  `gorm:"column:updated_at"`
}

func (ProjectHistoryScanStateModel) TableName() string { return "projects" }

type ProjectHistoryScanRepo struct{ db *gorm.DB }

func NewProjectHistoryScanRepo(db *gorm.DB) *ProjectHistoryScanRepo {
	return &ProjectHistoryScanRepo{db: db}
}

func (r *ProjectHistoryScanRepo) dbFor(ctx context.Context) *gorm.DB {
	if tx, ok := platform.GormTxFromContext(ctx); ok {
		return tx.WithContext(ctx)
	}
	return r.db.WithContext(ctx)
}

func (r *ProjectHistoryScanRepo) withTx(ctx context.Context, fn func(*gorm.DB) error) error {
	if tx, ok := platform.GormTxFromContext(ctx); ok {
		return fn(tx.WithContext(ctx))
	}
	return r.db.WithContext(ctx).Transaction(fn)
}

func (r *ProjectHistoryScanRepo) RequestProjectHistoryScan(ctx context.Context, projectID uint, requestID string) (*app.ProjectHistoryScanState, error) {
	if r == nil || r.db == nil || projectID == 0 {
		return nil, fmt.Errorf("request project history scan: invalid project")
	}
	var state app.ProjectHistoryScanState
	err := r.withTx(ctx, func(tx *gorm.DB) error {
		var model ProjectHistoryScanStateModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&model, "id = ?", projectID).Error; err != nil {
			return err
		}
		now := time.Now().UTC()
		model.Generation++
		result := tx.Model(&ProjectHistoryScanStateModel{}).Where("id = ?", projectID).Updates(map[string]any{
			"history_scan_status":          "pending",
			"history_scan_generation":      model.Generation,
			"history_scan_failures":        0,
			"history_scan_scanned_count":   0,
			"history_scan_matched_count":   0,
			"history_scan_skipped_count":   0,
			"history_scan_request_id":      strings.TrimSpace(requestID),
			"history_scan_last_safe_error": "",
			"history_scan_requested_at":    now,
			"history_scan_started_at":      nil,
			"history_scan_finished_at":     nil,
		})
		if result.Error != nil {
			return result.Error
		}
		state = app.ProjectHistoryScanState{
			ProjectID: projectID, Status: "pending", Generation: model.Generation,
			RequestID: strings.TrimSpace(requestID), RequestedAt: now,
		}
		return nil
	})
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("request project history scan: project %d not found", projectID)
	}
	if err != nil {
		return nil, fmt.Errorf("request project history scan: %w", err)
	}
	return &state, nil
}

func (r *ProjectHistoryScanRepo) ListPendingProjectHistoryScans(ctx context.Context, limit int) ([]app.ProjectHistoryScanState, error) {
	if limit <= 0 {
		limit = 16
	}
	var models []ProjectHistoryScanStateModel
	if err := r.dbFor(ctx).
		Where("history_scan_status = ?", "pending").
		Order("history_scan_requested_at ASC, id ASC").
		Limit(limit).
		Find(&models).Error; err != nil {
		return nil, fmt.Errorf("list pending project history scans: %w", err)
	}
	states := make([]app.ProjectHistoryScanState, len(models))
	for i := range models {
		states[i] = projectHistoryStateToApp(models[i])
	}
	return states, nil
}

func (r *ProjectHistoryScanRepo) MarkProjectHistoryProcessing(ctx context.Context, projectID uint, generation uint64) (bool, error) {
	now := time.Now().UTC()
	result := r.dbFor(ctx).Model(&ProjectHistoryScanStateModel{}).
		Where("id = ? AND history_scan_generation = ? AND history_scan_status = ?", projectID, generation, "pending").
		Updates(map[string]any{
			"history_scan_status":          "processing",
			"history_scan_last_safe_error": "",
			"history_scan_started_at":      now,
			"history_scan_finished_at":     nil,
		})
	if result.Error != nil {
		return false, fmt.Errorf("mark project history processing: %w", result.Error)
	}
	if result.RowsAffected == 1 {
		return true, nil
	}
	var count int64
	err := r.dbFor(ctx).Model(&ProjectHistoryScanStateModel{}).
		Where("id = ? AND history_scan_generation = ? AND history_scan_status = ?", projectID, generation, "processing").
		Count(&count).Error
	if err != nil {
		return false, fmt.Errorf("verify project history processing: %w", err)
	}
	return count == 1, nil
}

func (r *ProjectHistoryScanRepo) AssertProjectHistoryFence(ctx context.Context, projectID uint, generation uint64) error {
	var state ProjectHistoryScanStateModel
	err := r.dbFor(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ? AND history_scan_generation = ? AND history_scan_status = ?", projectID, generation, "processing").
		First(&state).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return domain.ErrFetchJobConflict
	}
	if err != nil {
		return fmt.Errorf("assert project history fence: %w", err)
	}
	return nil
}

func (r *ProjectHistoryScanRepo) CompleteProjectHistoryScan(ctx context.Context, projectID uint, generation uint64, scanned int, matched int, skipped int) (bool, error) {
	now := time.Now().UTC()
	result := r.dbFor(ctx).Model(&ProjectHistoryScanStateModel{}).
		Where("id = ? AND history_scan_generation = ? AND history_scan_status = ?", projectID, generation, "processing").
		Updates(map[string]any{
			"history_scan_status":          "normal",
			"history_scan_failures":        0,
			"history_scan_scanned_count":   max(scanned, 0),
			"history_scan_matched_count":   max(matched, 0),
			"history_scan_skipped_count":   max(skipped, 0),
			"history_scan_last_safe_error": "",
			"history_scan_finished_at":     now,
		})
	if result.Error != nil {
		return false, fmt.Errorf("complete project history scan: %w", result.Error)
	}
	return result.RowsAffected == 1, nil
}

func (r *ProjectHistoryScanRepo) ReleaseProjectHistoryInfrastructureFailure(ctx context.Context, projectID uint, generation uint64, safeError string) (bool, error) {
	result := r.dbFor(ctx).Model(&ProjectHistoryScanStateModel{}).
		Where("id = ? AND history_scan_generation = ? AND history_scan_status = ?", projectID, generation, "processing").
		Updates(map[string]any{
			"history_scan_status":          "pending",
			"history_scan_generation":      gorm.Expr("history_scan_generation + 1"),
			"history_scan_last_safe_error": safeDiagnostic(safeError),
			"history_scan_started_at":      nil,
		})
	if result.Error != nil {
		return false, fmt.Errorf("release project history infrastructure failure: %w", result.Error)
	}
	return result.RowsAffected == 1, nil
}

func (r *ProjectHistoryScanRepo) RecordProjectHistoryFailure(ctx context.Context, projectID uint, generation uint64, safeError string) (recorded bool, abnormal bool, err error) {
	err = r.withTx(ctx, func(tx *gorm.DB) error {
		var model ProjectHistoryScanStateModel
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND history_scan_generation = ? AND history_scan_status = ?", projectID, generation, "processing").
			First(&model).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		failures := model.Failures + 1
		status := "pending"
		updates := map[string]any{
			"history_scan_status":          status,
			"history_scan_failures":        failures,
			"history_scan_last_safe_error": safeDiagnostic(safeError),
			"history_scan_started_at":      nil,
		}
		if failures >= 3 {
			status = "abnormal"
			updates["history_scan_status"] = status
			updates["history_scan_failures"] = 3
			updates["history_scan_finished_at"] = time.Now().UTC()
		}
		result := tx.Model(&ProjectHistoryScanStateModel{}).
			Where("id = ? AND history_scan_generation = ? AND history_scan_status = ?", projectID, generation, "processing").
			Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		recorded = result.RowsAffected == 1
		abnormal = recorded && status == "abnormal"
		return nil
	})
	if err != nil {
		return false, false, fmt.Errorf("record project history failure: %w", err)
	}
	return recorded, abnormal, nil
}

func projectHistoryStateToApp(model ProjectHistoryScanStateModel) app.ProjectHistoryScanState {
	requestedAt := model.UpdatedAt
	if model.RequestedAt != nil {
		requestedAt = *model.RequestedAt
	}
	return app.ProjectHistoryScanState{
		ProjectID: model.ProjectID, Status: model.Status, Generation: model.Generation,
		Failures: model.Failures, RequestID: model.RequestID, RequestedAt: requestedAt,
	}
}

var _ app.ProjectHistoryScanRepository = (*ProjectHistoryScanRepo)(nil)
