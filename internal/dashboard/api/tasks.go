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
	typeRankingRefresh        = "dashboard:ranking_refresh"
	rankingRefreshInterval    = 5 * time.Minute
	rankingRefreshUniqueTTL   = 3 * rankingRefreshInterval
	rankingRefreshTaskTimeout = 4 * time.Minute
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
	if module == nil || module.asynq == nil {
		return func(context.Context) {}
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(rankingRefreshInterval)
		defer ticker.Stop()
		enqueueRankingRefresh(ctx, module.asynq)
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				enqueueRankingRefresh(ctx, module.asynq)
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
