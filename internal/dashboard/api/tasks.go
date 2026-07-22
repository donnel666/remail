package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	dashboardapp "github.com/donnel666/remail/internal/dashboard/app"
	"github.com/donnel666/remail/internal/platform"
	"github.com/hibiken/asynq"
)

const (
	typeRankingRefresh               = "dashboard:ranking_refresh"
	typeAdminDashboardRefresh        = "dashboard:admin_refresh"
	rankingRefreshInterval           = 5 * time.Minute
	adminDashboardRefreshInterval    = 10 * time.Minute
	rankingRefreshUniqueTTL          = 3 * rankingRefreshInterval
	adminDashboardRefreshUniqueTTL   = 3 * adminDashboardRefreshInterval
	rankingRefreshTaskTimeout        = 4 * time.Minute
	adminDashboardRefreshTaskTimeout = 9 * time.Minute
)

func RegisterTaskHandlers(mux *asynq.ServeMux, module *Module) func(context.Context) {
	mux.HandleFunc(typeRankingRefresh, func(ctx context.Context, _ *asynq.Task) error {
		if module == nil || module.view == nil {
			return nil
		}
		if err := module.view.RefreshLeaderboardCache(ctx, dashboardapp.TodayStart(time.Now())); err != nil {
			return fmt.Errorf("refresh dashboard leaderboard cache: %w", err)
		}
		return nil
	})
	mux.HandleFunc(typeAdminDashboardRefresh, func(ctx context.Context, _ *asynq.Task) error {
		if module == nil || module.adminCache == nil || module.AdminQuery == nil {
			return nil
		}
		if err := module.adminCache.refresh(ctx, module.AdminQuery.AdminDashboard); err != nil {
			return fmt.Errorf("refresh admin dashboard cache: %w", err)
		}
		return nil
	})
	if module == nil || module.asynq == nil {
		return func(context.Context) {}
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		rankingTicker := time.NewTicker(rankingRefreshInterval)
		adminTicker := time.NewTicker(adminDashboardRefreshInterval)
		defer rankingTicker.Stop()
		defer adminTicker.Stop()
		enqueueRankingRefresh(ctx, module.asynq)
		enqueueAdminDashboardRefresh(ctx, module.asynq)
		for {
			select {
			case <-ctx.Done():
				return
			case <-rankingTicker.C:
				enqueueRankingRefresh(ctx, module.asynq)
			case <-adminTicker.C:
				enqueueAdminDashboardRefresh(ctx, module.asynq)
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

func enqueueAdminDashboardRefresh(ctx context.Context, client *asynq.Client) {
	_, err := client.EnqueueContext(
		ctx,
		asynq.NewTask(typeAdminDashboardRefresh, nil),
		asynq.Queue(platform.QueueBackgroundInventory),
		asynq.Unique(adminDashboardRefreshUniqueTTL),
		asynq.MaxRetry(platform.BackgroundTaskMaxRetry),
		asynq.Timeout(adminDashboardRefreshTaskTimeout),
		asynq.Retention(0),
	)
	if err != nil && !errors.Is(err, asynq.ErrDuplicateTask) && !errors.Is(err, context.Canceled) {
		slog.Warn("enqueue admin dashboard refresh failed", "error", err)
	}
}

func enqueueRankingRefresh(ctx context.Context, client *asynq.Client) {
	_, err := client.EnqueueContext(
		ctx,
		asynq.NewTask(typeRankingRefresh, nil),
		asynq.Queue(platform.QueueBackgroundInventory),
		asynq.Unique(rankingRefreshUniqueTTL),
		asynq.MaxRetry(platform.BackgroundTaskMaxRetry),
		asynq.Timeout(rankingRefreshTaskTimeout),
		asynq.Retention(0),
	)
	if err != nil && !errors.Is(err, asynq.ErrDuplicateTask) && !errors.Is(err, context.Canceled) {
		slog.Warn("enqueue dashboard leaderboard refresh failed", "error", err)
	}
}
