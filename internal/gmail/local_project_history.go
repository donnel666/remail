package gmail

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/donnel666/remail/internal/platform"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"github.com/hibiken/asynq"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	typeGmailProjectHistoryScan        = "gmail:project_history_scan"
	typeGmailProjectHistoryDispatcher  = "gmail:project_history_dispatcher"
	localGmailProjectHistoryLimit      = 16
	localGmailProjectHistoryMaxLimit   = 100
	localGmailProjectHistoryTimeout    = 20 * time.Minute
	localGmailProjectHistoryMaxTimeout = 2 * time.Hour
	localGmailHistoryDispatchTimeout   = 30 * time.Second
)

var errLocalGmailProjectHistoryInfrastructure = errors.New("gmail: project history infrastructure failure")

type localGmailProjectHistoryTask struct {
	ProjectID       uint   `json:"projectId"`
	Generation      uint64 `json:"generation"`
	RequestID       string `json:"requestId,omitempty"`
	AfterResourceID uint   `json:"afterResourceId,omitempty"`
	MaxResourceID   uint   `json:"maxResourceId,omitempty"`
	ScannedCount    int    `json:"scannedCount,omitempty"`
	MatchedCount    int    `json:"matchedCount,omitempty"`
	SkippedCount    int    `json:"skippedCount,omitempty"`
}

type localGmailProjectHistoryStateModel struct {
	ProjectID     uint       `gorm:"primaryKey;column:id"`
	Status        string     `gorm:"column:gmail_history_scan_status"`
	Generation    uint64     `gorm:"column:gmail_history_scan_generation"`
	Failures      int        `gorm:"column:gmail_history_scan_failures"`
	ScannedCount  int        `gorm:"column:gmail_history_scan_scanned_count"`
	MatchedCount  int        `gorm:"column:gmail_history_scan_matched_count"`
	SkippedCount  int        `gorm:"column:gmail_history_scan_skipped_count"`
	RequestID     string     `gorm:"column:gmail_history_scan_request_id"`
	LastSafeError string     `gorm:"column:gmail_history_scan_last_safe_error"`
	RequestedAt   *time.Time `gorm:"column:gmail_history_scan_requested_at"`
	StartedAt     *time.Time `gorm:"column:gmail_history_scan_started_at"`
	FinishedAt    *time.Time `gorm:"column:gmail_history_scan_finished_at"`
	UpdatedAt     time.Time  `gorm:"column:updated_at"`
}

func (localGmailProjectHistoryStateModel) TableName() string { return "projects" }

func (s *Service) ScheduleProjectHistory(ctx context.Context, projectID uint, requestID string) error {
	if s == nil || s.db == nil || s.queue == nil || projectID == 0 {
		return ErrLocalValidationDependency
	}
	requestID = strings.TrimSpace(requestID)
	err := s.dbFor(ctx).Transaction(func(tx *gorm.DB) error {
		var state localGmailProjectHistoryStateModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&state, "id = ?", projectID).Error; err != nil {
			return err
		}
		now := s.now().UTC()
		return tx.Model(&localGmailProjectHistoryStateModel{}).Where("id = ?", projectID).Updates(map[string]any{
			"gmail_history_scan_status":          "pending",
			"gmail_history_scan_generation":      state.Generation + 1,
			"gmail_history_scan_failures":        0,
			"gmail_history_scan_scanned_count":   0,
			"gmail_history_scan_matched_count":   0,
			"gmail_history_scan_skipped_count":   0,
			"gmail_history_scan_request_id":      requestID,
			"gmail_history_scan_last_safe_error": "",
			"gmail_history_scan_requested_at":    now,
			"gmail_history_scan_started_at":      nil,
			"gmail_history_scan_finished_at":     nil,
		}).Error
	})
	if err != nil {
		return fmt.Errorf("request Gmail project history scan: %w", err)
	}
	_ = s.scheduleLocalGmailProjectHistoryDispatcher(context.WithoutCancel(ctx), 0)
	return nil
}

func (s *Service) DispatchLocalGmailProjectHistory(ctx context.Context, limit int) error {
	if s == nil || s.db == nil || s.queue == nil {
		return ErrLocalValidationDependency
	}
	if limit <= 0 {
		limit = localGmailProjectHistoryLimit
	}
	limit = min(limit, localGmailProjectHistoryMaxLimit)
	var rows []localGmailProjectHistoryStateModel
	if err := s.dbFor(ctx).Where("gmail_history_scan_status = ?", "pending").
		Order("gmail_history_scan_requested_at ASC, id ASC").Limit(limit).Find(&rows).Error; err != nil {
		return fmt.Errorf("list pending Gmail project history scans: %w", err)
	}
	var result error
	for _, row := range rows {
		accepted, err := s.enqueueLocalGmailProjectHistory(ctx, localGmailProjectHistoryTask{
			ProjectID: row.ProjectID, Generation: row.Generation, RequestID: row.RequestID,
		})
		if err != nil {
			result = errors.Join(result, err)
			continue
		}
		if !accepted {
			continue
		}
		if _, err := s.markLocalGmailProjectHistoryProcessing(ctx, row.ProjectID, row.Generation); err != nil {
			result = errors.Join(result, err)
		}
	}
	return result
}

func (s *Service) ProcessLocalGmailProjectHistory(ctx context.Context, task localGmailProjectHistoryTask) error {
	if s == nil || s.db == nil || s.queue == nil || s.fetch == nil || task.ProjectID == 0 || task.Generation == 0 {
		return ErrLocalValidationConflict
	}
	current, err := s.markLocalGmailProjectHistoryProcessing(ctx, task.ProjectID, task.Generation)
	if err != nil || !current {
		return err
	}
	done, next, err := s.scanLocalGmailProjectHistoryPage(ctx, task)
	if err == nil && done {
		_, err = s.completeLocalGmailProjectHistory(ctx, task.ProjectID, task.Generation, next)
		return err
	}
	if err == nil {
		if _, enqueueErr := s.enqueueLocalGmailProjectHistory(ctx, next); enqueueErr == nil {
			return nil
		} else {
			err = fmt.Errorf("%w: enqueue Gmail project history continuation: %v", errLocalGmailProjectHistoryInfrastructure, enqueueErr)
		}
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, errLocalGmailProjectHistoryInfrastructure) {
		released, releaseErr := s.releaseLocalGmailProjectHistory(
			context.WithoutCancel(ctx), task.ProjectID, task.Generation, "Gmail project history infrastructure is temporarily unavailable.",
		)
		if released {
			_ = s.scheduleLocalGmailProjectHistoryDispatcher(context.WithoutCancel(ctx), 0)
		}
		return errors.Join(err, releaseErr)
	}
	recorded, abnormal, recordErr := s.recordLocalGmailProjectHistoryFailure(
		context.WithoutCancel(ctx), task.ProjectID, task.Generation, localGmailProjectHistorySafeError(err),
	)
	if recordErr != nil {
		return errors.Join(err, recordErr)
	}
	if recorded && !abnormal {
		_ = s.scheduleLocalGmailProjectHistoryDispatcher(context.WithoutCancel(ctx), time.Second)
	}
	return nil
}

func (s *Service) ReleaseLocalGmailProjectHistoryDispatch(ctx context.Context, task localGmailProjectHistoryTask) error {
	if s == nil || task.ProjectID == 0 || task.Generation == 0 {
		return nil
	}
	released, err := s.releaseLocalGmailProjectHistory(
		ctx, task.ProjectID, task.Generation, "Gmail project history execution capacity is temporarily unavailable.",
	)
	if released {
		_ = s.scheduleLocalGmailProjectHistoryDispatcher(context.WithoutCancel(ctx), 0)
	}
	return err
}

func (s *Service) scanLocalGmailProjectHistoryPage(ctx context.Context, task localGmailProjectHistoryTask) (bool, localGmailProjectHistoryTask, error) {
	next := task
	scope, err := s.findLocalGmailProjectHistoryScope(ctx, task.ProjectID)
	if err != nil {
		return false, next, fmt.Errorf("%w: load Gmail project history scope: %v", errLocalGmailProjectHistoryInfrastructure, err)
	}
	if scope == nil {
		return true, next, nil
	}
	if next.MaxResourceID == 0 {
		if err := s.dbFor(ctx).Table("gmail_resources").Select("COALESCE(MAX(id), 0)").
			Where("status <> ?", LocalResourceDeleted).Scan(&next.MaxResourceID).Error; err != nil {
			return false, next, fmt.Errorf("%w: read Gmail resource high-water mark: %v", errLocalGmailProjectHistoryInfrastructure, err)
		}
	}
	if next.AfterResourceID >= next.MaxResourceID {
		return true, next, nil
	}
	var resource localResourceModel
	result := s.dbFor(ctx).Where("id > ? AND id <= ? AND status <> ?", next.AfterResourceID, next.MaxResourceID, LocalResourceDeleted).
		Order("id ASC").Limit(1).Find(&resource)
	if result.Error != nil {
		return false, next, fmt.Errorf("%w: find next Gmail resource: %v", errLocalGmailProjectHistoryInfrastructure, result.Error)
	}
	if result.RowsAffected == 0 {
		return true, next, nil
	}
	next.AfterResourceID = resource.ID
	next.ScannedCount++
	if strings.TrimSpace(resource.Email) == "" || strings.TrimSpace(resource.AppPassword) == "" ||
		resource.Status == LocalResourcePending || resource.Status == LocalResourceValidating {
		next.SkippedCount++
		return false, next, nil
	}
	matched, skipped, err := s.scanLocalGmailProjectHistoryResource(ctx, task, *scope, resource)
	if matched {
		next.MatchedCount++
	}
	if skipped {
		next.SkippedCount++
	}
	return false, next, err
}

func (s *Service) scanLocalGmailProjectHistoryResource(
	ctx context.Context,
	task localGmailProjectHistoryTask,
	scope localGmailHistoryScope,
	resource localResourceModel,
) (bool, bool, error) {
	matches := make([]localGmailHistoryMatch, 0)
	matchIndex := make(map[localGmailHistoryMatchKey]int)
	cursors := localGmailFolderCursors{}
	for {
		messages, nextCursors, err := s.fetch(ctx, resource.Email, resource.AppPassword, cursors, time.Time{}, true)
		if errors.Is(err, errLocalGmailAuthentication) {
			return false, true, nil
		}
		if err != nil {
			return false, false, fmt.Errorf("fetch Gmail project history: %w", err)
		}
		for _, fetched := range messages {
			message := parseLocalGmailHistoryMessage(fetched.Raw, fetched.ReceivedAt)
			mailbox, recipient, ok := localGmailHistoryRecipient(resource.Email, []string{fetched.Recipient})
			if !ok || !localGmailHistoryMatchesScope(message, mailbox, scope) {
				continue
			}
			matchedAt := message.ReceivedAt.UTC()
			if matchedAt.IsZero() {
				matchedAt = s.now().UTC()
			}
			key := localGmailHistoryMatchKey{ProjectID: scope.ProjectID, Mailbox: mailbox, Email: recipient}
			index, exists := matchIndex[key]
			if !exists {
				matchIndex[key] = len(matches)
				matches = append(matches, localGmailHistoryMatch{
					ResourceID: resource.ID, ProjectID: scope.ProjectID, ProductID: scope.ProductID,
					CodeWindowMinutes: scope.CodeWindowMinutes, ActivationWindowMinutes: scope.ActivationWindowMinutes,
					WarrantyMinutes: scope.WarrantyMinutes, Mailbox: mailbox, Email: recipient,
					FirstMatchedAt: matchedAt, LastMatchedAt: matchedAt, EvidenceCount: 1,
				})
				continue
			}
			if matchedAt.Before(matches[index].FirstMatchedAt) {
				matches[index].FirstMatchedAt = matchedAt
			}
			if matchedAt.After(matches[index].LastMatchedAt) {
				matches[index].LastMatchedAt = matchedAt
			}
			matches[index].EvidenceCount++
		}
		if nextCursors == cursors {
			break
		}
		cursors = nextCursors
	}
	stale := false
	err := s.dbFor(ctx).Transaction(func(tx *gorm.DB) error {
		if err := s.assertLocalGmailProjectHistoryFence(tx, task.ProjectID, task.Generation); err != nil {
			return err
		}
		currentScope, err := s.findLocalGmailProjectHistoryScope(platform.WithGormTx(ctx, tx), task.ProjectID)
		if err != nil {
			return err
		}
		if currentScope == nil || !sameLocalGmailHistoryScopes([]localGmailHistoryScope{scope}, []localGmailHistoryScope{*currentScope}) {
			return errLocalGmailHistoryScopeChanged
		}
		var current localResourceModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&current, resource.ID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				stale = true
				return nil
			}
			return err
		}
		if current.Status == LocalResourceDeleted || current.CredentialRevision != resource.CredentialRevision ||
			!strings.EqualFold(current.Email, resource.Email) || current.AppPassword != resource.AppPassword {
			stale = true
			return nil
		}
		for _, match := range matches {
			if err := s.importLocalGmailHistoryMatch(ctx, tx, match); err != nil {
				return err
			}
		}
		return nil
	})
	if stale {
		return false, true, nil
	}
	if err != nil {
		return false, false, err
	}
	return len(matches) > 0, false, nil
}

func (s *Service) findLocalGmailProjectHistoryScope(ctx context.Context, projectID uint) (*localGmailHistoryScope, error) {
	if projectID == 0 {
		return nil, nil
	}
	scopes, err := s.listLocalGmailHistoryScopes(ctx)
	if err != nil {
		return nil, err
	}
	for i := range scopes {
		if scopes[i].ProjectID == projectID {
			return &scopes[i], nil
		}
	}
	return nil, nil
}

func (s *Service) enqueueLocalGmailProjectHistory(ctx context.Context, task localGmailProjectHistoryTask) (bool, error) {
	if s == nil || s.queue == nil || task.ProjectID == 0 || task.Generation == 0 {
		return false, ErrLocalValidationDependency
	}
	payload, err := json.Marshal(task)
	if err != nil {
		return false, fmt.Errorf("encode Gmail project history task: %w", err)
	}
	timeout := min(runtimeconfig.Duration("project_history_timeout_minutes", localGmailProjectHistoryTimeout, time.Minute, 1), localGmailProjectHistoryMaxTimeout)
	_, err = s.queue.EnqueueContext(ctx, asynq.NewTask(typeGmailProjectHistoryScan, payload),
		asynq.Queue(platform.QueueBackgroundProjectHistory), asynq.Unique(timeout),
		asynq.MaxRetry(platform.BackgroundTaskMaxRetryValue()), asynq.Timeout(timeout), asynq.Retention(0))
	if errors.Is(err, asynq.ErrDuplicateTask) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("enqueue Gmail project history task: %w", err)
	}
	return true, nil
}

func (s *Service) scheduleLocalGmailProjectHistoryDispatcher(ctx context.Context, delay time.Duration) error {
	if s == nil || s.queue == nil {
		return ErrLocalValidationDependency
	}
	uniqueTTL := localGmailHistoryDispatchTimeout + delay
	options := []asynq.Option{
		asynq.Queue(platform.QueueBackgroundProjectHistory), asynq.Unique(uniqueTTL), asynq.MaxRetry(0),
		asynq.Timeout(localGmailHistoryDispatchTimeout), asynq.Retention(0),
	}
	if delay > 0 {
		options = append(options, asynq.ProcessIn(delay))
	}
	_, err := s.queue.EnqueueContext(ctx, asynq.NewTask(typeGmailProjectHistoryDispatcher, nil), options...)
	if errors.Is(err, asynq.ErrDuplicateTask) {
		return nil
	}
	return err
}

func (s *Service) markLocalGmailProjectHistoryProcessing(ctx context.Context, projectID uint, generation uint64) (bool, error) {
	now := s.now().UTC()
	result := s.dbFor(ctx).Model(&localGmailProjectHistoryStateModel{}).
		Where("id = ? AND gmail_history_scan_generation = ? AND gmail_history_scan_status = ?", projectID, generation, "pending").
		Updates(map[string]any{
			"gmail_history_scan_status": "processing", "gmail_history_scan_last_safe_error": "",
			"gmail_history_scan_started_at": now, "gmail_history_scan_finished_at": nil,
		})
	if result.Error != nil {
		return false, result.Error
	}
	if result.RowsAffected == 1 {
		return true, nil
	}
	var count int64
	err := s.dbFor(ctx).Model(&localGmailProjectHistoryStateModel{}).
		Where("id = ? AND gmail_history_scan_generation = ? AND gmail_history_scan_status = ?", projectID, generation, "processing").Count(&count).Error
	return count == 1, err
}

func (s *Service) assertLocalGmailProjectHistoryFence(tx *gorm.DB, projectID uint, generation uint64) error {
	var state localGmailProjectHistoryStateModel
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ? AND gmail_history_scan_generation = ? AND gmail_history_scan_status = ?", projectID, generation, "processing").First(&state).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrLocalValidationConflict
	}
	return err
}

func (s *Service) completeLocalGmailProjectHistory(ctx context.Context, projectID uint, generation uint64, task localGmailProjectHistoryTask) (bool, error) {
	now := s.now().UTC()
	result := s.dbFor(ctx).Model(&localGmailProjectHistoryStateModel{}).
		Where("id = ? AND gmail_history_scan_generation = ? AND gmail_history_scan_status = ?", projectID, generation, "processing").
		Updates(map[string]any{
			"gmail_history_scan_status": "normal", "gmail_history_scan_failures": 0,
			"gmail_history_scan_scanned_count":   max(task.ScannedCount, 0),
			"gmail_history_scan_matched_count":   max(task.MatchedCount, 0),
			"gmail_history_scan_skipped_count":   max(task.SkippedCount, 0),
			"gmail_history_scan_last_safe_error": "", "gmail_history_scan_finished_at": now,
		})
	return result.RowsAffected == 1, result.Error
}

func (s *Service) releaseLocalGmailProjectHistory(ctx context.Context, projectID uint, generation uint64, safeError string) (bool, error) {
	result := s.dbFor(ctx).Model(&localGmailProjectHistoryStateModel{}).
		Where("id = ? AND gmail_history_scan_generation = ? AND gmail_history_scan_status = ?", projectID, generation, "processing").
		Updates(map[string]any{
			"gmail_history_scan_status": "pending", "gmail_history_scan_generation": gorm.Expr("gmail_history_scan_generation + 1"),
			"gmail_history_scan_last_safe_error": localGmailProjectHistoryDiagnostic(safeError), "gmail_history_scan_started_at": nil,
		})
	return result.RowsAffected == 1, result.Error
}

func (s *Service) recordLocalGmailProjectHistoryFailure(ctx context.Context, projectID uint, generation uint64, safeError string) (recorded bool, abnormal bool, err error) {
	err = s.dbFor(ctx).Transaction(func(tx *gorm.DB) error {
		var state localGmailProjectHistoryStateModel
		findErr := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND gmail_history_scan_generation = ? AND gmail_history_scan_status = ?", projectID, generation, "processing").First(&state).Error
		if errors.Is(findErr, gorm.ErrRecordNotFound) {
			return nil
		}
		if findErr != nil {
			return findErr
		}
		failures := state.Failures + 1
		status := "pending"
		updates := map[string]any{
			"gmail_history_scan_status": status, "gmail_history_scan_failures": failures,
			"gmail_history_scan_last_safe_error": localGmailProjectHistoryDiagnostic(safeError),
			"gmail_history_scan_started_at":      nil,
		}
		if failures >= 3 {
			status = "abnormal"
			updates["gmail_history_scan_status"] = status
			updates["gmail_history_scan_failures"] = 3
			updates["gmail_history_scan_finished_at"] = s.now().UTC()
		}
		result := tx.Model(&localGmailProjectHistoryStateModel{}).
			Where("id = ? AND gmail_history_scan_generation = ? AND gmail_history_scan_status = ?", projectID, generation, "processing").Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		recorded = result.RowsAffected == 1
		abnormal = recorded && status == "abnormal"
		return nil
	})
	return recorded, abnormal, err
}

func localGmailProjectHistorySafeError(err error) string {
	if errors.Is(err, errLocalGmailHistoryScopeChanged) {
		return "Gmail project history scope changed while mail was being fetched."
	}
	return "Gmail project history scan failed."
}

func localGmailProjectHistoryDiagnostic(value string) string {
	runes := []rune(strings.TrimSpace(value))
	if len(runes) > 500 {
		runes = runes[:500]
	}
	return string(runes)
}
