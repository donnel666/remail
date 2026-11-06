package platform

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"
)

const (
	dashboardQueueMigrationLockKey     = "remail:migrations:dashboard-refresh-v1"
	dashboardQueueMigrationLockTTL     = 10 * time.Minute
	dashboardQueueMigrationPageSize    = 1000
	dashboardRankingMigrationUniqueTTL = 72 * time.Hour
	dashboardAdminMigrationUniqueTTL   = 30 * time.Minute
)

var dashboardRefreshTaskTypes = map[string]struct{}{
	"dashboard:ranking_refresh": {},
	"dashboard:admin_refresh":   {},
}

// migrateLegacyDashboardTasks moves refresh tasks left in the old inventory
// queue during a rolling deployment. It runs before workers start, preserves
// the task ID and schedule, and leaves the old task in place if enqueue fails.
func migrateLegacyDashboardTasks(ctx context.Context, client *asynq.Client, rdb redis.UniversalClient) error {
	if client == nil || rdb == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	locked, err := rdb.SetNX(ctx, dashboardQueueMigrationLockKey, time.Now().UTC().Format(time.RFC3339Nano), dashboardQueueMigrationLockTTL).Result()
	if err != nil {
		return err
	}
	if !locked {
		return nil
	}
	defer rdb.Del(context.WithoutCancel(ctx), dashboardQueueMigrationLockKey)

	inspector := asynq.NewInspectorFromRedisClient(rdb)
	states := []func(string, ...asynq.ListOption) ([]*asynq.TaskInfo, error){
		inspector.ListPendingTasks,
		inspector.ListScheduledTasks,
		inspector.ListRetryTasks,
	}
	moved := 0
	for _, list := range states {
		page := 1
		for {
			tasks, listErr := list(QueueBackgroundInventory, asynq.PageSize(dashboardQueueMigrationPageSize), asynq.Page(page))
			if errors.Is(listErr, asynq.ErrQueueNotFound) {
				break
			}
			if listErr != nil {
				return listErr
			}
			if len(tasks) == 0 {
				break
			}
			deleted := false
			for _, info := range tasks {
				if info == nil {
					continue
				}
				if _, ok := dashboardRefreshTaskTypes[info.Type]; !ok {
					continue
				}
				if err := migrateDashboardTask(ctx, client, inspector, info); err != nil {
					return err
				}
				moved++
				deleted = true
			}
			if deleted {
				// Deleting page one shifts later tasks into it.
				page = 1
			} else if len(tasks) < dashboardQueueMigrationPageSize {
				break
			} else {
				page++
			}
		}
	}
	if moved > 0 {
		slog.Info("migrated legacy dashboard refresh tasks", "count", moved, "queue", QueueBackgroundDashboard)
	}
	return nil
}

func migrateDashboardTask(ctx context.Context, client *asynq.Client, inspector *asynq.Inspector, info *asynq.TaskInfo) error {
	options := []asynq.Option{
		asynq.Queue(QueueBackgroundDashboard),
		asynq.MaxRetry(max(0, info.MaxRetry-info.Retried)),
		asynq.Unique(dashboardTaskUniqueTTL(info.Type)),
	}
	if info.Timeout > 0 {
		options = append(options, asynq.Timeout(info.Timeout))
	}
	if info.Retention > 0 {
		options = append(options, asynq.Retention(info.Retention))
	}
	if !info.Deadline.IsZero() && info.Deadline.After(time.Now()) {
		options = append(options, asynq.Deadline(info.Deadline))
	}
	if !info.NextProcessAt.IsZero() {
		if delay := time.Until(info.NextProcessAt); delay > 0 {
			options = append(options, asynq.ProcessIn(delay))
		}
	}
	_, err := client.EnqueueContext(ctx, asynq.NewTask(info.Type, info.Payload), options...)
	if err != nil && !errors.Is(err, asynq.ErrDuplicateTask) {
		return err
	}
	if err := inspector.DeleteTask(QueueBackgroundInventory, info.ID); err != nil && !errors.Is(err, asynq.ErrTaskNotFound) {
		return err
	}
	return nil
}

func dashboardTaskUniqueTTL(taskType string) time.Duration {
	if taskType == "dashboard:ranking_refresh" {
		// The runtime setting allows a 24-hour refresh interval; the scheduler
		// uses three intervals for uniqueness.
		return dashboardRankingMigrationUniqueTTL
	}
	return dashboardAdminMigrationUniqueTTL
}
