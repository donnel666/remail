package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	billingapp "github.com/donnel666/remail/internal/billing/app"
	billinginfra "github.com/donnel666/remail/internal/billing/infra"
	"github.com/hibiken/asynq"
)

const rechargeDispatcherInterval = time.Second

func RegisterBillingTaskHandlers(mux *asynq.ServeMux, module *BillingModule) func(context.Context) {
	mux.HandleFunc(billinginfra.TypeRechargeReconcile, func(ctx context.Context, task *asynq.Task) error {
		if module == nil || module.RechargeUseCase == nil {
			return nil
		}
		var payload billingapp.RechargeTask
		if err := json.Unmarshal(task.Payload(), &payload); err != nil || payload.RechargeNo == "" {
			return fmt.Errorf("decode recharge task: %w", asynq.SkipRetry)
		}
		return module.RechargeUseCase.Reconcile(ctx, payload)
	})
	if module == nil || module.RechargeUseCase == nil {
		return func(context.Context) {}
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(rechargeDispatcherInterval)
		defer ticker.Stop()
		dispatch := func() {
			if err := module.RechargeUseCase.Dispatch(ctx); err != nil && ctx.Err() == nil {
				slog.Warn("recharge dispatcher failed", "error", err)
			}
		}
		dispatch()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				dispatch()
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
