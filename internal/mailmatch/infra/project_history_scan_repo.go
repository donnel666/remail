package infra

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/donnel666/remail/internal/mailmatch/app"
	"github.com/donnel666/remail/internal/platform"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type ProjectHistoryScanJobModel struct {
	ID                   uint       `gorm:"primaryKey;autoIncrement"`
	ProjectID            uint       `gorm:"not null;column:project_id"`
	Shard                int        `gorm:"not null"`
	Status               string     `gorm:"type:varchar(32);not null;default:'queued'"`
	StartResourceID      uint       `gorm:"not null;column:start_resource_id"`
	CheckpointResourceID uint       `gorm:"not null;column:checkpoint_resource_id"`
	EndResourceID        uint       `gorm:"not null;column:end_resource_id"`
	Attempts             int        `gorm:"not null;default:0"`
	MaxAttempts          int        `gorm:"not null;default:3;column:max_attempts"`
	ScannedCount         int        `gorm:"not null;default:0;column:scanned_count"`
	MatchedCount         int        `gorm:"not null;default:0;column:matched_count"`
	SkippedCount         int        `gorm:"not null;default:0;column:skipped_count"`
	ClaimToken           string     `gorm:"type:char(36);not null;default:'';column:claim_token"`
	DispatchToken        string     `gorm:"type:char(36);not null;default:'';column:dispatch_token"`
	DispatchedAt         *time.Time `gorm:"column:dispatched_at"`
	LastSafeError        string     `gorm:"type:varchar(500);not null;default:'';column:last_safe_error"`
	RequestID            string     `gorm:"type:varchar(64);not null;default:'';column:request_id"`
	StartedAt            *time.Time `gorm:"column:started_at"`
	FinishedAt           *time.Time `gorm:"column:finished_at"`
	CreatedAt            time.Time  `gorm:"not null;autoCreateTime;column:created_at"`
	UpdatedAt            time.Time  `gorm:"not null;autoUpdateTime;column:updated_at"`
}

func (ProjectHistoryScanJobModel) TableName() string { return "mailmatch_project_history_scan_jobs" }

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

func (r *ProjectHistoryScanRepo) withTx(ctx context.Context, fn func(context.Context, *gorm.DB) error) error {
	if tx, ok := platform.GormTxFromContext(ctx); ok {
		return fn(ctx, tx.WithContext(ctx))
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return fn(platform.WithGormTx(ctx, tx), tx)
	})
}

func (r *ProjectHistoryScanRepo) CreatePlanner(ctx context.Context, projectID uint, requestID string) error {
	if r == nil || r.db == nil || projectID == 0 {
		return fmt.Errorf("create project history planner: invalid project")
	}
	model := &ProjectHistoryScanJobModel{
		ProjectID: projectID, Shard: -1, Status: "queued", MaxAttempts: 3,
		RequestID: strings.TrimSpace(requestID),
	}
	if err := r.dbFor(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(model).Error; err != nil {
		return fmt.Errorf("create project history planner: %w", err)
	}
	return nil
}

func (r *ProjectHistoryScanRepo) EnsureMissingPlanners(ctx context.Context, limit int) (int, error) {
	if limit <= 0 {
		limit = 16
	}
	result := r.dbFor(ctx).Exec(`
INSERT IGNORE INTO mailmatch_project_history_scan_jobs(
    project_id, shard, status, max_attempts
)
SELECT p.id, -1, 'queued', 3
FROM projects AS p
WHERE p.status IN ('listed', 'delisted')
  AND EXISTS (
      SELECT 1 FROM project_products AS pp
      WHERE pp.project_id = p.id AND pp.type = 'microsoft'
  )
  AND NOT EXISTS (
      SELECT 1 FROM mailmatch_project_history_scan_jobs AS scan
      WHERE scan.project_id = p.id AND scan.shard = -1
  )
ORDER BY p.id ASC
LIMIT ?`, limit)
	if result.Error != nil {
		return 0, fmt.Errorf("ensure missing project history planners: %w", result.Error)
	}
	return int(result.RowsAffected), nil
}

func (r *ProjectHistoryScanRepo) ClaimDispatchable(ctx context.Context, limit int, runningStaleBefore, dispatchStaleBefore time.Time) ([]app.ProjectHistoryScanJob, error) {
	if limit <= 0 {
		limit = 16
	}
	models := make([]ProjectHistoryScanJobModel, 0, limit)
	now := time.Now().UTC()
	err := r.withTx(ctx, func(_ context.Context, tx *gorm.DB) error {
		if err := tx.Where(
			"(status = ? AND (dispatched_at IS NULL OR dispatched_at < ?)) OR (status = ? AND updated_at < ?)",
			"queued", dispatchStaleBefore, "running", runningStaleBefore,
		).
			Order("updated_at ASC, id ASC").
			Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).
			Limit(limit).
			Find(&models).Error; err != nil {
			return fmt.Errorf("claim project history scans: %w", err)
		}
		for i := range models {
			token := platform.NewUUIDV4String()
			if err := tx.Model(&ProjectHistoryScanJobModel{}).Where("id = ?", models[i].ID).UpdateColumns(map[string]any{
				"dispatch_token": token,
				"dispatched_at":  now,
				"updated_at":     gorm.Expr("updated_at"),
			}).Error; err != nil {
				return fmt.Errorf("fence project history dispatch: %w", err)
			}
			models[i].DispatchToken = token
			models[i].DispatchedAt = &now
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	jobs := make([]app.ProjectHistoryScanJob, len(models))
	for i := range models {
		jobs[i] = projectHistoryJobToApp(models[i])
	}
	return jobs, nil
}

func (r *ProjectHistoryScanRepo) MarkRunning(ctx context.Context, jobID uint, dispatchToken string) (*app.ProjectHistoryScanJob, bool, error) {
	var job *app.ProjectHistoryScanJob
	claimed := false
	err := r.withTx(ctx, func(_ context.Context, tx *gorm.DB) error {
		var model ProjectHistoryScanJobModel
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND dispatch_token = ? AND status IN ?", jobID, strings.TrimSpace(dispatchToken), []string{"queued", "running"}).
			First(&model).Error
		if err == gorm.ErrRecordNotFound {
			return nil
		}
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		claimToken := platform.NewUUIDV4String()
		attempts := min(model.Attempts+1, max(model.MaxAttempts, 1))
		updates := map[string]any{
			"status": "running", "attempts": attempts, "claim_token": claimToken,
			"dispatch_token": "", "dispatched_at": nil, "last_safe_error": "", "updated_at": now,
		}
		if model.StartedAt == nil {
			updates["started_at"] = now
			model.StartedAt = &now
		}
		result := tx.Model(&ProjectHistoryScanJobModel{}).
			Where("id = ? AND dispatch_token = ?", jobID, strings.TrimSpace(dispatchToken)).
			Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return nil
		}
		model.Status = "running"
		model.Attempts = attempts
		model.ClaimToken = claimToken
		model.DispatchToken = ""
		model.DispatchedAt = nil
		model.UpdatedAt = now
		mapped := projectHistoryJobToApp(model)
		job = &mapped
		claimed = true
		return nil
	})
	return job, claimed, err
}

func (r *ProjectHistoryScanRepo) PlanShards(ctx context.Context, plannerID uint, claimToken string, shards []app.ProjectHistoryScanJob) error {
	return r.withTx(ctx, func(_ context.Context, tx *gorm.DB) error {
		var planner ProjectHistoryScanJobModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND status = ? AND claim_token = ?", plannerID, "running", strings.TrimSpace(claimToken)).
			First(&planner).Error; err != nil {
			return err
		}
		models := make([]ProjectHistoryScanJobModel, len(shards))
		for i := range shards {
			models[i] = projectHistoryJobFromApp(shards[i])
			models[i].ProjectID = planner.ProjectID
		}
		if len(models) > 0 {
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&models).Error; err != nil {
				return err
			}
		}
		return r.complete(tx, plannerID, claimToken)
	})
}

func (r *ProjectHistoryScanRepo) Advance(ctx context.Context, jobID uint, claimToken string, resourceID uint, matched bool, skipped bool, safeError string) error {
	updates := map[string]any{
		"status": "queued", "checkpoint_resource_id": resourceID, "attempts": 0,
		"claim_token": "", "dispatch_token": "", "dispatched_at": nil,
		"last_safe_error": safeDiagnostic(safeError), "updated_at": time.Now().UTC(),
		"scanned_count": gorm.Expr("scanned_count + 1"),
	}
	if matched {
		updates["matched_count"] = gorm.Expr("matched_count + 1")
	}
	if skipped {
		updates["skipped_count"] = gorm.Expr("skipped_count + 1")
	}
	result := r.dbFor(ctx).Model(&ProjectHistoryScanJobModel{}).
		Where("id = ? AND status = ? AND claim_token = ?", jobID, "running", strings.TrimSpace(claimToken)).
		Updates(updates)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("advance project history scan: stale claim")
	}
	return nil
}

func (r *ProjectHistoryScanRepo) Complete(ctx context.Context, jobID uint, claimToken string) error {
	return r.complete(r.dbFor(ctx), jobID, claimToken)
}

func (r *ProjectHistoryScanRepo) complete(db *gorm.DB, jobID uint, claimToken string) error {
	now := time.Now().UTC()
	result := db.Model(&ProjectHistoryScanJobModel{}).
		Where("id = ? AND status = ? AND claim_token = ?", jobID, "running", strings.TrimSpace(claimToken)).
		Updates(map[string]any{
			"status": "succeeded", "attempts": 0, "claim_token": "", "dispatch_token": "",
			"dispatched_at": nil, "last_safe_error": "", "finished_at": now, "updated_at": now,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("complete project history scan: stale claim")
	}
	return nil
}

func (r *ProjectHistoryScanRepo) MarkFailure(ctx context.Context, job app.ProjectHistoryScanJob, resourceID uint, retryable bool, safeError string) error {
	return r.withTx(ctx, func(_ context.Context, tx *gorm.DB) error {
		var model ProjectHistoryScanJobModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND status = ? AND claim_token = ?", job.ID, "running", strings.TrimSpace(job.ClaimToken)).
			First(&model).Error; err != nil {
			return err
		}
		updates := map[string]any{
			"status": "queued", "claim_token": "", "dispatch_token": "", "dispatched_at": nil,
			"last_safe_error": safeDiagnostic(safeError), "updated_at": time.Now().UTC(),
		}
		if !retryable || model.Attempts >= max(model.MaxAttempts, 1) {
			updates["attempts"] = 0
			if resourceID > 0 {
				updates["checkpoint_resource_id"] = resourceID
				updates["scanned_count"] = gorm.Expr("scanned_count + 1")
				updates["skipped_count"] = gorm.Expr("skipped_count + 1")
			}
		}
		result := tx.Model(&ProjectHistoryScanJobModel{}).
			Where("id = ? AND status = ? AND claim_token = ?", job.ID, "running", strings.TrimSpace(job.ClaimToken)).
			Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return fmt.Errorf("mark project history scan failure: stale claim")
		}
		return nil
	})
}

func (r *ProjectHistoryScanRepo) ReleaseDispatch(ctx context.Context, jobID uint, dispatchToken string) error {
	return r.dbFor(ctx).Model(&ProjectHistoryScanJobModel{}).
		Where("id = ? AND dispatch_token = ? AND status IN ?", jobID, strings.TrimSpace(dispatchToken), []string{"queued", "running"}).
		UpdateColumns(map[string]any{"dispatch_token": "", "dispatched_at": nil, "updated_at": gorm.Expr("updated_at")}).Error
}

func (r *ProjectHistoryScanRepo) MarkDispatchFailed(ctx context.Context, jobID uint, dispatchToken string, safeError string) error {
	return r.dbFor(ctx).Model(&ProjectHistoryScanJobModel{}).
		Where("id = ? AND dispatch_token = ? AND status IN ?", jobID, strings.TrimSpace(dispatchToken), []string{"queued", "running"}).
		UpdateColumns(map[string]any{
			"dispatch_token": "", "dispatched_at": nil,
			"last_safe_error": safeDiagnostic(safeError), "updated_at": gorm.Expr("updated_at"),
		}).Error
}

func projectHistoryJobToApp(model ProjectHistoryScanJobModel) app.ProjectHistoryScanJob {
	return app.ProjectHistoryScanJob{
		ID: model.ID, ProjectID: model.ProjectID, Shard: model.Shard, Status: model.Status,
		StartResourceID: model.StartResourceID, CheckpointResourceID: model.CheckpointResourceID, EndResourceID: model.EndResourceID,
		Attempts: model.Attempts, MaxAttempts: model.MaxAttempts, ScannedCount: model.ScannedCount,
		MatchedCount: model.MatchedCount, SkippedCount: model.SkippedCount,
		ClaimToken: model.ClaimToken, DispatchToken: model.DispatchToken, RequestID: model.RequestID,
		DispatchedAt: model.DispatchedAt, UpdatedAt: model.UpdatedAt,
	}
}

func projectHistoryJobFromApp(job app.ProjectHistoryScanJob) ProjectHistoryScanJobModel {
	return ProjectHistoryScanJobModel{
		ProjectID: job.ProjectID, Shard: job.Shard, Status: job.Status,
		StartResourceID: job.StartResourceID, CheckpointResourceID: job.CheckpointResourceID, EndResourceID: job.EndResourceID,
		Attempts: job.Attempts, MaxAttempts: max(job.MaxAttempts, 1), RequestID: strings.TrimSpace(job.RequestID),
	}
}

var _ app.ProjectHistoryScanRepository = (*ProjectHistoryScanRepo)(nil)
