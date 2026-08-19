package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	lotteryinfra "github.com/donnel666/remail/internal/lottery/infra"
	"github.com/hibiken/asynq"
)

const lotteryReconcileInterval = 30 * time.Second

func RegisterTaskHandlers(mux *asynq.ServeMux, module *Module) func(context.Context) {
	mux.HandleFunc(lotteryinfra.TypeDrawLottery, func(ctx context.Context, task *asynq.Task) error {
		if module == nil || module.Service == nil {
			return nil
		}
		var payload lotteryinfra.DrawTask
		if err := json.Unmarshal(task.Payload(), &payload); err != nil || payload.LotteryID == 0 {
			return fmt.Errorf("decode lottery draw task: %w", asynq.SkipRetry)
		}
		return module.Service.Draw(ctx, payload.LotteryID)
	})
	if module == nil || module.Service == nil {
		return func(context.Context) {}
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(lotteryReconcileInterval)
		defer ticker.Stop()
		reconcile := func() {
			if err := module.Service.DispatchDue(ctx); err != nil && ctx.Err() == nil {
				slog.Warn("lottery due dispatch failed", "error", err)
			}
			if err := module.Service.ReconcileSettling(ctx, 100); err != nil && ctx.Err() == nil {
				slog.Warn("lottery settling reconciliation failed", "error", err)
			}
		}
		reconcile()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				reconcile()
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
