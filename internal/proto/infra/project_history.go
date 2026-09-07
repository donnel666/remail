package infra

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	protoapp "github.com/donnel666/remail/internal/proto/app"
	"github.com/donnel666/remail/internal/proto/domain"
	"github.com/hibiken/asynq"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type ProjectHistoryState struct {
	ProjectID     uint       `gorm:"primaryKey;autoIncrement:false" json:"projectId"`
	Generation    uint64     `json:"generation"`
	Status        string     `json:"status"`
	AfterID       uint       `json:"afterId"`
	ThroughID     uint       `json:"throughId"`
	Failures      int        `json:"failures"`
	ScannedCount  int        `json:"scannedCount"`
	MatchedCount  int        `json:"matchedCount"`
	SkippedCount  int        `json:"skippedCount"`
	RequestID     string     `json:"requestId"`
	LastSafeError string     `json:"lastSafeError"`
	RequestedAt   *time.Time `json:"requestedAt"`
	StartedAt     *time.Time `json:"startedAt"`
	FinishedAt    *time.Time `json:"finishedAt"`
	CreatedAt     time.Time  `json:"createdAt"`
	UpdatedAt     time.Time  `json:"updatedAt"`
}

func (ProjectHistoryState) TableName() string { return "proto_project_history_scan_states" }

type ProjectHistoryTask struct {
	ProjectID  uint   `json:"projectId"`
	Generation uint64 `json:"generation"`
}

func (s *Service) ScheduleProjectHistory(ctx context.Context, projectID uint, requestID string) error {
	if projectID == 0 {
		return domain.ErrInvalidResource
	}
	err := s.transaction(ctx, func(_ context.Context, tx *gorm.DB) error {
		now := s.Now().UTC()
		initial := ProjectHistoryState{ProjectID: projectID, Generation: 1, Status: "pending", RequestID: requestID, RequestedAt: &now}
		created := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&initial)
		if created.Error != nil {
			return created.Error
		}
		if created.RowsAffected == 1 {
			return nil
		}
		var current ProjectHistoryState
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("project_id = ?", projectID).Take(&current).Error; err != nil {
			return err
		}
		return tx.Model(&current).Updates(map[string]any{"generation": current.Generation + 1, "status": "pending", "after_id": 0, "through_id": 0, "failures": 0, "scanned_count": 0, "matched_count": 0, "skipped_count": 0, "request_id": requestID, "last_safe_error": "", "requested_at": now, "started_at": nil, "finished_at": nil, "updated_at": now}).Error
	})
	if err != nil {
		return err
	}
	if s.Queue != nil {
		_ = s.DispatchProjectHistory(ctx, 100)
	}
	return nil
}
func (s *Service) DispatchProjectHistory(ctx context.Context, limit int) error {
	if s.Queue == nil {
		return domain.ErrDependency
	}
	var rows []ProjectHistoryState
	if err := s.dbFor(ctx).Where("status = 'pending'").Order("requested_at ASC, project_id ASC").Limit(min(max(limit, 1), 100)).Find(&rows).Error; err != nil {
		return err
	}
	for _, row := range rows {
		payload, err := json.Marshal(ProjectHistoryTask{row.ProjectID, row.Generation})
		if err != nil {
			return err
		}
		_, err = s.Queue.EnqueueContext(ctx, asynq.NewTask(protoapp.TaskProjectHistory, payload), asynq.Queue(protoapp.QueueProtoHistory), asynq.Unique(time.Minute), asynq.Timeout(time.Minute), asynq.MaxRetry(3), asynq.Retention(0))
		if err != nil && !errors.Is(err, asynq.ErrDuplicateTask) {
			return err
		}
	}
	return nil
}
func (s *Service) ProcessProjectHistoryTODO(ctx context.Context, task ProjectHistoryTask) error {
	return s.transaction(ctx, func(_ context.Context, tx *gorm.DB) error {
		var row ProjectHistoryState
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("project_id = ? AND generation = ?", task.ProjectID, task.Generation).Take(&row).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return domain.ErrInvalidClaim
			}
			return err
		}
		if row.Status != "pending" && row.Status != "processing" {
			return domain.ErrInvalidClaim
		}
		now := s.Now().UTC()
		// TODO: page through Proto resources and commit historical usage through
		// Trade. No resource state or business fact may be guessed here.
		return tx.Model(&row).Updates(map[string]any{"status": "uncertain", "last_safe_error": "proto_project_history_todo", "started_at": now, "finished_at": now, "updated_at": now}).Error
	})
}
