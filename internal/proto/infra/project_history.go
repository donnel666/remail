package infra

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	"github.com/donnel666/remail/internal/platform"
	protoapp "github.com/donnel666/remail/internal/proto/app"
	"github.com/donnel666/remail/internal/proto/domain"
	"github.com/donnel666/remail/internal/proto/infra/proton"
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
	AfterID    uint   `json:"afterId,omitempty"`
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
	if err := s.dbFor(ctx).Where("status = 'pending' OR (status = 'processing' AND updated_at < ?)", s.Now().UTC().Add(-35*time.Minute)).Order("requested_at ASC, project_id ASC").Limit(min(max(limit, 1), 100)).Find(&rows).Error; err != nil {
		return err
	}
	for _, row := range rows {
		if err := s.enqueueProjectHistory(ctx, ProjectHistoryTask{ProjectID: row.ProjectID, Generation: row.Generation, AfterID: row.AfterID}); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) enqueueProjectHistory(ctx context.Context, task ProjectHistoryTask) error {
	if s.Queue == nil {
		return domain.ErrDependency
	}
	payload, err := json.Marshal(task)
	if err != nil {
		return err
	}
	_, err = s.Queue.EnqueueContext(ctx, asynq.NewTask(protoapp.TaskProjectHistory, payload),
		asynq.Queue(protoapp.QueueProtoHistory), asynq.Unique(30*time.Minute), asynq.Timeout(30*time.Minute),
		asynq.MaxRetry(platform.BackgroundTaskMaxRetryValue()), asynq.Retention(0))
	if errors.Is(err, asynq.ErrDuplicateTask) {
		return nil
	}
	return err
}

func (s *Service) ProcessProjectHistory(ctx context.Context, task ProjectHistoryTask) error {
	return s.processProjectHistory(ctx, task, s.FetchMailbox)
}

func (s *Service) processProjectHistory(ctx context.Context, task ProjectHistoryTask, fetch historyMailboxFetch) error {
	if task.ProjectID == 0 || task.Generation == 0 {
		return domain.ErrInvalidResource
	}
	var state ProjectHistoryState
	var claimedAt time.Time
	err := s.transaction(ctx, func(_ context.Context, tx *gorm.DB) error {
		row, err := lockProjectHistory(tx, task)
		if err != nil {
			return err
		}
		now := s.Now().UTC().Truncate(time.Millisecond)
		if row.Status == "processing" && row.StartedAt != nil && row.StartedAt.Add(sessionLeaseDuration).After(now) {
			return domain.ErrInvalidClaim
		}
		// DATETIME(3) is the existing durable attempt fence. Advance even if two
		// sequential retries share a clock tick, without adding another table column.
		if row.StartedAt != nil && !now.After(*row.StartedAt) {
			now = row.StartedAt.Add(time.Millisecond)
		}
		if row.ThroughID == 0 {
			if err := protoResourceQuery(tx).Select("COALESCE(MAX(id), 0)").Where("status <> ?", domain.StatusDeleted).Scan(&row.ThroughID).Error; err != nil {
				return err
			}
		}
		updates := map[string]any{"status": "processing", "through_id": row.ThroughID, "last_safe_error": "", "started_at": now, "updated_at": now}
		claimedAt = now
		state = *row
		return tx.Model(row).Updates(updates).Error
	})
	if err != nil {
		return err
	}
	if state.RequestID != "" {
		ctx = context.WithValue(ctx, platform.RequestIDKey, state.RequestID)
	}
	scopes, err := s.historyProjectScopes(ctx, task.ProjectID)
	var resource *Resource
	if err == nil && len(scopes) > 0 {
		var row Resource
		err = protoResourceQuery(s.dbFor(ctx)).Where("id > ? AND id <= ? AND status <> ?", state.AfterID, state.ThroughID, domain.StatusDeleted).
			Select(safeResourceColumns).Order("id ASC").Take(&row).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			err = nil
		} else if err == nil {
			resource = &row
		}
	}
	var matches []HistoricalUsage
	skipped := false
	if err == nil && resource != nil {
		skipped = resource.Status != domain.StatusIdentifying && resource.Status != domain.StatusNormal
		if !skipped {
			matches, err = s.fetchHistoryMatches(ctx, *resource, scopes, fetch)
			var failure *proton.Failure
			unavailable := errors.Is(err, domain.ErrInvalidClaim) || errors.Is(err, domain.ErrResourceMissing) || errors.Is(err, ErrSessionUnavailable)
			if errors.As(err, &failure) && !failure.Retryable {
				unavailable = failure.Category == "invalid_credentials" || failure.Category == "session_revoked" || failure.Category == "identity_mismatch"
			}
			if unavailable {
				skipped, err = true, nil
			}
		}
	}
	if err == nil {
		err = s.commitProjectHistoryResource(ctx, task, claimedAt, scopes, resource, matches, skipped)
	}
	if err != nil {
		if errors.Is(err, domain.ErrInvalidClaim) {
			return err
		}
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		terminal, recordErr := s.recordProjectHistoryFailure(cleanup, task, claimedAt, err)
		if recordErr != nil {
			return errors.Join(err, recordErr)
		}
		if terminal {
			return nil
		}
		return err
	}
	if resource == nil {
		return nil
	}
	// Progress commits before handoff. A lost enqueue is resumed from this
	// durable cursor by DispatchProjectHistory, never by skipping a resource.
	return s.enqueueProjectHistory(ctx, ProjectHistoryTask{ProjectID: task.ProjectID, Generation: task.Generation, AfterID: resource.ID})
}

func lockProjectHistory(tx *gorm.DB, task ProjectHistoryTask) (*ProjectHistoryState, error) {
	var state ProjectHistoryState
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("project_id = ? AND generation = ? AND after_id = ? AND status IN ?", task.ProjectID, task.Generation, task.AfterID, []string{"pending", "processing"}).Take(&state).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, domain.ErrInvalidClaim
	}
	return &state, err
}

func lockProjectHistoryAttempt(tx *gorm.DB, task ProjectHistoryTask, claimedAt time.Time) (*ProjectHistoryState, error) {
	state, err := lockProjectHistory(tx, task)
	if err != nil {
		return nil, err
	}
	if state.Status != "processing" || state.StartedAt == nil || !state.StartedAt.Equal(claimedAt) {
		return nil, domain.ErrInvalidClaim
	}
	return state, nil
}

func (s *Service) commitProjectHistoryResource(ctx context.Context, task ProjectHistoryTask, claimedAt time.Time, scopes []historyProjectScope, resource *Resource, matches []HistoricalUsage, skipped bool) error {
	return s.transaction(ctx, func(ctx context.Context, tx *gorm.DB) error {
		if resource != nil {
			current, err := lockResource(tx, resource.ID, nil)
			if errors.Is(err, domain.ErrResourceMissing) {
				skipped = true
			} else if err != nil {
				return err
			} else if current.OwnerUserID != resource.OwnerUserID || current.CredentialRevision != resource.CredentialRevision ||
				current.ValidationGeneration != resource.ValidationGeneration || current.EmailAddress != resource.EmailAddress ||
				(current.Status != domain.StatusIdentifying && current.Status != domain.StatusNormal) {
				skipped = true
			}
		}
		state, err := lockProjectHistoryAttempt(tx, task, claimedAt)
		if err != nil {
			return err
		}
		currentScopes, err := s.historyProjectScopes(ctx, task.ProjectID)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(scopes, currentScopes) {
			return errHistoryScopeChanged
		}
		now := s.Now().UTC()
		updates := map[string]any{"status": "pending", "last_safe_error": "", "failures": 0, "updated_at": now}
		if resource == nil {
			updates["status"], updates["finished_at"], updates["failures"] = "normal", now, 0
		} else {
			if !skipped {
				if err := s.commitHistoricalUsage(ctx, matches); err != nil {
					return err
				}
			}
			updates["after_id"] = resource.ID
			updates["scanned_count"] = state.ScannedCount + 1
			if skipped {
				updates["skipped_count"] = state.SkippedCount + 1
			} else if len(matches) > 0 {
				updates["matched_count"] = state.MatchedCount + 1
			}
		}
		return tx.Model(state).Updates(updates).Error
	})
}

func (s *Service) recordProjectHistoryFailure(ctx context.Context, task ProjectHistoryTask, claimedAt time.Time, cause error) (bool, error) {
	terminal := false
	err := s.transaction(ctx, func(_ context.Context, tx *gorm.DB) error {
		state, err := lockProjectHistoryAttempt(tx, task, claimedAt)
		if err != nil {
			return err
		}
		updates := map[string]any{"status": "pending", "last_safe_error": safeHistoryError(cause), "updated_at": s.Now().UTC()}
		var failure *proton.Failure
		if errors.As(cause, &failure) {
			failures := min(state.Failures+1, 3)
			updates["failures"] = failures
			terminal = !failure.Retryable || failures >= 3
			if terminal {
				updates["status"], updates["finished_at"] = "abnormal", s.Now().UTC()
				if !failure.Retryable {
					updates["status"] = "uncertain"
				}
			}
		}
		return tx.Model(state).Updates(updates).Error
	})
	return terminal, err
}
