package kitesim

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/donnel666/remail/internal/platform"
	"github.com/hibiken/asynq"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	typeAccountSync           = "kitesim:account_sync"
	typeUpstreamRefresh       = "kitesim:upstream_refresh"
	typeUpstreamOperation     = "kitesim:upstream_operation"
	typeOperationReconcile    = "kitesim:operation_reconcile"
	syncTaskTimeout           = 8 * time.Minute
	refreshTaskTimeout        = 15 * time.Minute
	operationTaskTimeout      = 20 * time.Minute
	operationSettlementGrace  = 5 * time.Minute
	queuedRechargeSecretTTL   = 10 * time.Minute
	expiredRechargeSafeError  = "Kitesim 充值任务等待执行超时，CVC 已清除；请重新提交。"
	reconcileRetryInterval    = time.Minute
	syncTaskDelay             = time.Second
	operationDispatchInterval = 5 * time.Second
)

var (
	errTaskNotQueued  = errors.New("kitesim: task is not queued")
	errSyncSuperseded = errors.New("kitesim: account sync superseded")
)

type SyncQueue interface {
	Enqueue(context.Context, uint) (bool, error)
}

type UpstreamRefreshQueue interface {
	EnqueueUpstreamRefresh(context.Context) (bool, error)
}

type OperationQueue interface {
	EnqueueOperation(context.Context, uint64) (bool, error)
}

type OperationReconcileQueue interface {
	EnqueueOperationReconcile(context.Context, uint64) (bool, error)
}

type AsynqSyncQueue struct{ client *asynq.Client }

func NewSyncQueue(client *asynq.Client) *AsynqSyncQueue {
	return &AsynqSyncQueue{client: client}
}

func (q *AsynqSyncQueue) Enqueue(ctx context.Context, accountID uint) (bool, error) {
	if q == nil || q.client == nil {
		return false, errors.New("kitesim: task queue unavailable")
	}
	payload, err := json.Marshal(struct {
		AccountID uint `json:"accountId"`
	}{AccountID: accountID})
	if err != nil {
		return false, fmt.Errorf("marshal Kitesim sync task: %w", err)
	}
	_, err = q.client.EnqueueContext(
		ctx,
		asynq.NewTask(typeAccountSync, payload),
		asynq.Queue(platform.QueueDefault),
		asynq.Unique(syncTaskTimeout+syncTaskDelay),
		asynq.MaxRetry(platform.BackgroundTaskMaxRetryValue()),
		asynq.Timeout(syncTaskTimeout),
		asynq.ProcessIn(syncTaskDelay),
		asynq.Retention(0),
	)
	if errors.Is(err, asynq.ErrDuplicateTask) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("enqueue Kitesim sync task: %w", err)
	}
	return true, nil
}

func (q *AsynqSyncQueue) EnqueueUpstreamRefresh(ctx context.Context) (bool, error) {
	if q == nil || q.client == nil {
		return false, errors.New("kitesim: task queue unavailable")
	}
	_, err := q.client.EnqueueContext(
		ctx,
		asynq.NewTask(typeUpstreamRefresh, nil),
		asynq.Queue(platform.QueueDefault),
		asynq.Unique(refreshTaskTimeout+syncTaskDelay),
		asynq.MaxRetry(platform.BackgroundTaskMaxRetryValue()),
		asynq.Timeout(refreshTaskTimeout),
		asynq.ProcessIn(syncTaskDelay),
		asynq.Retention(0),
	)
	if errors.Is(err, asynq.ErrDuplicateTask) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("enqueue Kitesim upstream refresh: %w", err)
	}
	return true, nil
}

func (q *AsynqSyncQueue) EnqueueOperation(ctx context.Context, operationID uint64) (bool, error) {
	if q == nil || q.client == nil {
		return false, errors.New("kitesim: task queue unavailable")
	}
	payload, err := json.Marshal(struct {
		OperationID uint64 `json:"operationId"`
	}{OperationID: operationID})
	if err != nil {
		return false, fmt.Errorf("marshal Kitesim operation task: %w", err)
	}
	_, err = q.client.EnqueueContext(
		ctx,
		asynq.NewTask(typeUpstreamOperation, payload),
		asynq.Queue(platform.QueueDefault),
		asynq.Unique(operationTaskTimeout),
		asynq.MaxRetry(platform.BackgroundTaskMaxRetryValue()),
		asynq.Timeout(operationTaskTimeout),
		asynq.ProcessIn(syncTaskDelay),
		asynq.Retention(0),
	)
	if errors.Is(err, asynq.ErrDuplicateTask) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("enqueue Kitesim operation task: %w", err)
	}
	return true, nil
}

func (q *AsynqSyncQueue) EnqueueOperationReconcile(ctx context.Context, operationID uint64) (bool, error) {
	if q == nil || q.client == nil {
		return false, errors.New("kitesim: task queue unavailable")
	}
	payload, err := json.Marshal(struct {
		OperationID uint64 `json:"operationId"`
	}{OperationID: operationID})
	if err != nil {
		return false, fmt.Errorf("marshal Kitesim reconcile task: %w", err)
	}
	_, err = q.client.EnqueueContext(
		ctx,
		asynq.NewTask(typeOperationReconcile, payload),
		asynq.Queue(platform.QueueDefault),
		asynq.Unique(operationTaskTimeout),
		asynq.MaxRetry(platform.BackgroundTaskMaxRetryValue()),
		asynq.Timeout(operationTaskTimeout),
		asynq.ProcessIn(syncTaskDelay),
		asynq.Retention(0),
	)
	if errors.Is(err, asynq.ErrDuplicateTask) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("enqueue Kitesim reconcile task: %w", err)
	}
	return true, nil
}

func RegisterTaskHandlers(mux *asynq.ServeMux, service *Service) {
	mux.HandleFunc(typeAccountSync, func(ctx context.Context, task *asynq.Task) error {
		var payload struct {
			AccountID uint `json:"accountId"`
		}
		if err := json.Unmarshal(task.Payload(), &payload); err != nil || payload.AccountID == 0 {
			return fmt.Errorf("decode Kitesim sync task: %w", asynq.SkipRetry)
		}
		if service == nil {
			return nil
		}
		claim, err := service.markSyncRunning(ctx, payload.AccountID)
		if err != nil {
			if errors.Is(err, ErrAccountMissing) || errors.Is(err, errTaskNotQueued) {
				return nil
			}
			return err
		}
		if err := service.processAccountSync(ctx, claim); err != nil {
			if errors.Is(err, errSyncSuperseded) {
				return nil
			}
			willRetry := !errors.Is(err, ErrLoginFailed) && platform.BackgroundTaskHasRetryHeadroom(ctx)
			service.recordSyncTaskFailure(ctx, claim, safeSyncError(err), willRetry)
			if willRetry {
				return err
			}
			return nil
		}
		return nil
	})
	mux.HandleFunc(typeUpstreamRefresh, func(ctx context.Context, _ *asynq.Task) error {
		if service == nil {
			return nil
		}
		claim, err := service.markUpstreamRefreshRunning(ctx)
		if err != nil {
			if errors.Is(err, errTaskNotQueued) {
				return nil
			}
			return err
		}
		if err := service.processUpstreamRefresh(ctx, claim); err != nil {
			if errors.Is(err, errRefreshSuperseded) {
				return nil
			}
			willRetry := !errors.Is(err, ErrLoginFailed) && platform.BackgroundTaskHasRetryHeadroom(ctx)
			service.recordUpstreamRefreshFailure(ctx, claim, err, willRetry)
			if willRetry {
				return err
			}
		}
		return nil
	})
	mux.HandleFunc(typeUpstreamOperation, func(ctx context.Context, task *asynq.Task) error {
		var payload struct {
			OperationID uint64 `json:"operationId"`
		}
		if err := json.Unmarshal(task.Payload(), &payload); err != nil || payload.OperationID == 0 {
			return fmt.Errorf("decode Kitesim operation task: %w", asynq.SkipRetry)
		}
		if service != nil {
			return service.processOperation(ctx, payload.OperationID)
		}
		return nil
	})
	mux.HandleFunc(typeOperationReconcile, func(ctx context.Context, task *asynq.Task) error {
		var payload struct {
			OperationID uint64 `json:"operationId"`
		}
		if err := json.Unmarshal(task.Payload(), &payload); err != nil || payload.OperationID == 0 {
			return fmt.Errorf("decode Kitesim reconcile task: %w", asynq.SkipRetry)
		}
		if service == nil {
			return nil
		}
		return service.processOperationReconcile(ctx, payload.OperationID)
	})
}

func StartOperationDispatcher(service *Service) func(context.Context) {
	if service == nil {
		return func(context.Context) {}
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(operationDispatchInterval)
		defer ticker.Stop()
		for {
			if err := service.DispatchQueuedOperations(ctx); err != nil && ctx.Err() == nil {
				slog.Warn("failed to dispatch Kitesim operations", "error", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return func(shutdownCtx context.Context) {
		cancel()
		select {
		case <-done:
		case <-shutdownCtx.Done():
		}
	}
}

func (s *Service) DispatchQueuedOperations(ctx context.Context) error {
	queue, ok := s.queue.(OperationQueue)
	if !ok || queue == nil {
		return errors.New("kitesim: operation queue unavailable")
	}
	now := s.now().UTC().Truncate(time.Millisecond)
	if err := s.db.WithContext(ctx).Model(&operationModel{}).
		Where("status = ? AND started_at IS NOT NULL AND started_at <= ?", OperationRunning, now.Add(-operationTaskTimeout-operationSettlementGrace)).
		Updates(map[string]any{
			"status": OperationUncertain, "secret_payload": nil, "finished_at": now,
			"reconcile_requested_at": now,
			"last_safe_error":        "Kitesim 任务执行超时，已停止自动重放并等待只读对账。",
		}).Error; err != nil {
		return fmt.Errorf("recover stale Kitesim operations: %w", err)
	}
	if err := s.db.WithContext(ctx).Model(&operationModel{}).
		Where("kind = ? AND status = ? AND queued_at <= ?", OperationRecharge, OperationQueued, now.Add(-queuedRechargeSecretTTL)).
		Updates(map[string]any{
			"status": OperationFailed, "secret_payload": nil, "finished_at": now,
			"last_safe_error": expiredRechargeSafeError,
		}).Error; err != nil {
		return fmt.Errorf("expire queued Kitesim recharge secrets: %w", err)
	}
	if err := s.db.WithContext(ctx).Model(&accountModel{}).
		Where(`sync_status NOT IN ? AND EXISTS (
			SELECT 1 FROM kitesim_operations AS operation
			WHERE operation.account_id = kitesim_accounts.id
				AND operation.status NOT IN ('queued', 'running')
				AND operation.finished_at IS NOT NULL
				AND (kitesim_accounts.sync_started_at IS NULL OR operation.finished_at > kitesim_accounts.sync_started_at)
		)`, []SyncTaskStatus{SyncTaskQueued, SyncTaskRunning}).
		Updates(map[string]any{
			"sync_status": SyncTaskQueued, "sync_queued_at": now,
			"sync_started_at": nil, "sync_finished_at": nil, "last_safe_error": "",
		}).Error; err != nil {
		return fmt.Errorf("queue post-operation Kitesim sync: %w", err)
	}
	if err := s.db.WithContext(ctx).Model(&accountModel{}).
		Where("sync_status = ? AND sync_started_at IS NOT NULL AND sync_started_at <= ?", SyncTaskRunning, now.Add(-syncTaskTimeout)).
		Updates(map[string]any{
			"sync_status": SyncTaskQueued, "sync_queued_at": now,
			"sync_started_at": nil, "sync_finished_at": nil,
			"last_safe_error": "Kitesim 同步任务超时，已重新排队。",
		}).Error; err != nil {
		return fmt.Errorf("recover stale Kitesim sync tasks: %w", err)
	}
	var syncAccountIDs []uint
	if err := s.db.WithContext(ctx).Model(&accountModel{}).
		Where("sync_status = ?", SyncTaskQueued).Order("sync_queued_at ASC, id ASC").Limit(100).
		Pluck("id", &syncAccountIDs).Error; err != nil {
		return fmt.Errorf("list queued Kitesim sync tasks: %w", err)
	}
	for _, accountID := range syncAccountIDs {
		if _, err := s.queue.Enqueue(ctx, accountID); err != nil {
			return err
		}
	}
	if refreshQueue, ok := s.queue.(UpstreamRefreshQueue); ok && refreshQueue != nil {
		if err := s.db.WithContext(ctx).Model(&upstreamSettingsModel{}).
			Where(`id = ? AND refresh_status NOT IN ? AND EXISTS (
				SELECT 1 FROM kitesim_operations AS operation
				WHERE operation.status NOT IN ('queued', 'running')
					AND operation.finished_at IS NOT NULL
					AND (kitesim_upstream_settings.refresh_started_at IS NULL OR operation.finished_at > kitesim_upstream_settings.refresh_started_at)
			)`, upstreamSettingsID, []SyncTaskStatus{SyncTaskQueued, SyncTaskRunning}).
			Updates(map[string]any{
				"refresh_status": SyncTaskQueued, "refresh_queued_at": now,
				"refresh_started_at": nil, "refresh_finished_at": nil, "last_safe_error": "",
			}).Error; err != nil {
			return fmt.Errorf("queue post-operation Kitesim upstream refresh: %w", err)
		}
		if err := s.db.WithContext(ctx).Model(&upstreamSettingsModel{}).
			Where("refresh_status = ? AND refresh_started_at IS NOT NULL AND refresh_started_at <= ?", SyncTaskRunning, now.Add(-refreshTaskTimeout)).
			Updates(map[string]any{
				"refresh_status": SyncTaskQueued, "refresh_queued_at": now,
				"refresh_started_at": nil, "refresh_finished_at": nil,
				"last_safe_error": "Kitesim 上游刷新任务超时，已重新排队。",
			}).Error; err != nil {
			return fmt.Errorf("recover stale Kitesim upstream refresh: %w", err)
		}
		var refreshCount int64
		if err := s.db.WithContext(ctx).Model(&upstreamSettingsModel{}).
			Where("id = ? AND refresh_status = ?", upstreamSettingsID, SyncTaskQueued).
			Count(&refreshCount).Error; err != nil {
			return fmt.Errorf("load queued Kitesim upstream refresh: %w", err)
		}
		if refreshCount > 0 {
			if _, err := refreshQueue.EnqueueUpstreamRefresh(ctx); err != nil {
				return err
			}
		}
	}
	var queuedIDs []uint64
	if err := s.db.WithContext(ctx).Model(&operationModel{}).
		Where("status = ?", OperationQueued).Order("queued_at ASC, id ASC").Limit(100).
		Pluck("id", &queuedIDs).Error; err != nil {
		return fmt.Errorf("list queued Kitesim operations: %w", err)
	}
	for _, operationID := range queuedIDs {
		if _, err := queue.EnqueueOperation(ctx, operationID); err != nil {
			return err
		}
	}
	reconcileQueue, ok := s.queue.(OperationReconcileQueue)
	if !ok || reconcileQueue == nil {
		return nil
	}
	var reconcileIDs []uint64
	if err := s.db.WithContext(ctx).Model(&operationModel{}).
		Where("status IN ? AND reconcile_requested_at IS NOT NULL AND reconcile_requested_at <= ? AND (last_reconciled_at IS NULL OR last_reconciled_at < reconcile_requested_at)",
			[]OperationStatus{OperationUncertain, OperationRequiresAction}, now).
		Order("reconcile_requested_at ASC, id ASC").Limit(100).Pluck("id", &reconcileIDs).Error; err != nil {
		return fmt.Errorf("list Kitesim operations awaiting reconciliation: %w", err)
	}
	for _, operationID := range reconcileIDs {
		if _, err := reconcileQueue.EnqueueOperationReconcile(ctx, operationID); err != nil {
			return err
		}
	}
	return nil
}

type accountSyncClaim struct {
	AccountID uint
	StartedAt time.Time
}

func (s *Service) markSyncRunning(ctx context.Context, accountID uint) (accountSyncClaim, error) {
	claim := accountSyncClaim{AccountID: accountID}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var account accountModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Select("id", "sync_status", "sync_queued_at", "sync_started_at").
			First(&account, accountID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrAccountMissing
			}
			return fmt.Errorf("load Kitesim sync task: %w", err)
		}
		if SyncTaskStatus(account.SyncStatus) != SyncTaskQueued {
			return errTaskNotQueued
		}
		startedAt := s.now().UTC().Truncate(time.Millisecond)
		for _, previous := range []*time.Time{account.SyncQueuedAt, account.SyncStartedAt} {
			if previous != nil && !startedAt.After(*previous) {
				startedAt = previous.Add(time.Millisecond)
			}
		}
		result := tx.Model(&accountModel{}).
			Where("id = ? AND sync_status = ?", accountID, SyncTaskQueued).
			Updates(map[string]any{
				"sync_status": SyncTaskRunning, "sync_started_at": startedAt,
				"sync_finished_at": nil, "sync_attempts": gorm.Expr("sync_attempts + 1"),
			})
		if result.Error != nil {
			return fmt.Errorf("start Kitesim sync task: %w", result.Error)
		}
		if result.RowsAffected == 0 {
			return errTaskNotQueued
		}
		claim.StartedAt = startedAt
		return nil
	})
	if err != nil {
		return accountSyncClaim{}, err
	}
	return claim, nil
}

func (s *Service) recordSyncTaskFailure(ctx context.Context, claim accountSyncClaim, message string, retry bool) {
	status := SyncTaskFailed
	now := s.now().UTC().Truncate(time.Millisecond)
	var finishedAt any = now
	updates := map[string]any{
		"last_safe_error": message, "sync_status": status,
		"sync_finished_at": finishedAt,
	}
	if retry {
		status = SyncTaskQueued
		finishedAt = nil
		updates["sync_status"] = status
		updates["sync_queued_at"] = now
		updates["sync_started_at"] = nil
		updates["sync_finished_at"] = finishedAt
	}
	_ = s.db.WithContext(context.WithoutCancel(ctx)).Model(&accountModel{}).
		Where("id = ? AND sync_status = ? AND sync_started_at = ?", claim.AccountID, SyncTaskRunning, claim.StartedAt).
		Updates(updates).Error
}

type upstreamRefreshClaim struct {
	AccountID uint
	StartedAt time.Time
}

func (s *Service) markUpstreamRefreshRunning(ctx context.Context) (upstreamRefreshClaim, error) {
	now := s.now().UTC().Truncate(time.Millisecond)
	claim := upstreamRefreshClaim{StartedAt: now}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var settings upstreamSettingsModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&settings, upstreamSettingsID).Error; err != nil {
			return fmt.Errorf("load Kitesim upstream refresh: %w", err)
		}
		if SyncTaskStatus(settings.RefreshStatus) != SyncTaskQueued || settings.AccountID == nil || *settings.AccountID == 0 {
			return errTaskNotQueued
		}
		claim.AccountID = *settings.AccountID
		result := tx.Model(&upstreamSettingsModel{}).
			Where("id = ? AND refresh_status = ?", upstreamSettingsID, SyncTaskQueued).
			Updates(map[string]any{
				"refresh_status": SyncTaskRunning, "refresh_started_at": now,
				"refresh_finished_at": nil, "refresh_attempts": gorm.Expr("refresh_attempts + 1"),
			})
		if result.Error != nil {
			return fmt.Errorf("start Kitesim upstream refresh: %w", result.Error)
		}
		if result.RowsAffected == 0 {
			return errTaskNotQueued
		}
		return nil
	})
	if err != nil {
		return upstreamRefreshClaim{}, err
	}
	return claim, nil
}

func (s *Service) recordUpstreamRefreshFailure(ctx context.Context, claim upstreamRefreshClaim, err error, retry bool) {
	status := SyncTaskFailed
	var finishedAt any = s.now().UTC().Truncate(time.Millisecond)
	if retry {
		status = SyncTaskQueued
		finishedAt = nil
	}
	message := "Kitesim 上游余额和产品同步失败，请稍后重试。"
	if errors.Is(err, ErrLoginFailed) {
		message = "Kitesim 登录失败，请检查系统平台账号。"
	}
	_ = s.db.WithContext(context.WithoutCancel(ctx)).Model(&upstreamSettingsModel{}).
		Where(
			"id = ? AND account_id = ? AND refresh_status = ? AND refresh_started_at = ?",
			upstreamSettingsID,
			claim.AccountID,
			SyncTaskRunning,
			claim.StartedAt,
		).
		Updates(map[string]any{
			"last_safe_error": message, "refresh_status": status,
			"refresh_finished_at": finishedAt,
		}).Error
}
