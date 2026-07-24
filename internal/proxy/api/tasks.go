package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/donnel666/remail/internal/platform"
	proxyapp "github.com/donnel666/remail/internal/proxy/app"
	proxydomain "github.com/donnel666/remail/internal/proxy/domain"
	proxyinfra "github.com/donnel666/remail/internal/proxy/infra"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"github.com/hibiken/asynq"
)

const proxyCheckDispatcherInterval = 15 * time.Second

func RegisterProxyTaskHandlers(mux *asynq.ServeMux, module *ProxyModule) {
	mux.HandleFunc(proxyinfra.TypeProxyCheckDispatcher, func(ctx context.Context, _ *asynq.Task) error {
		result, err := module.ProxyUseCase.DispatchPendingProxyChecks(ctx, 0)
		if err != nil {
			module.ProxyUseCase.LogTaskFailure(ctx, "proxy.check_dispatcher_failed", "", "proxy", "dispatcher", "Proxy check dispatcher failed.", err)
			return err
		}
		if result != nil && result.Attempted > 0 {
			slog.Info(
				"proxy check dispatcher finished",
				"attempted", result.Attempted,
				"queued", result.Queued,
				"failed", result.Failed,
			)
		}
		return nil
	})
	module.ProxyUseCase.ScheduleProxyCheckDispatcher(context.Background(), 0)
	startProxyCheckDispatcherSeeder(module)

	mux.HandleFunc(proxyinfra.TypeProxyCheck, func(ctx context.Context, task *asynq.Task) error {
		var payload proxyapp.ProxyCheckTask
		if err := json.Unmarshal(task.Payload(), &payload); err != nil {
			return fmt.Errorf("decode proxy check task: %w: %w", err, asynq.SkipRetry)
		}

		slog.Info(
			"processing proxy check task",
			"proxy_id", payload.ProxyID,
			"check_generation", payload.CheckGeneration,
		)
		updated, err := module.ProxyUseCase.RunCheck(ctx, payload, !platform.BackgroundTaskHasRetryHeadroom(ctx))
		if err != nil {
			module.ProxyUseCase.LogTaskFailure(ctx, "proxy.check_task_failed", "", "proxy", fmt.Sprintf("%d", payload.ProxyID), "Proxy check task failed.", err)
			if errors.Is(err, proxydomain.ErrProxyNotFound) || errors.Is(err, proxydomain.ErrInvalidProxyStatus) {
				return fmt.Errorf("non-retryable proxy check task failure: %w: %w", err, asynq.SkipRetry)
			}
			slog.Warn(
				"proxy check task failed",
				"proxy_id", payload.ProxyID,
				"check_generation", payload.CheckGeneration,
				"error", err,
			)
			return err
		}
		status := ""
		if updated != nil {
			status = string(updated.Status)
		}
		slog.Info(
			"proxy check task finished",
			"proxy_id", payload.ProxyID,
			"check_generation", payload.CheckGeneration,
			"status", status,
		)
		return nil
	})
}

func startProxyCheckDispatcherSeeder(module *ProxyModule) {
	if module == nil || module.ProxyUseCase == nil {
		return
	}
	go func() {
		// Poll once per second so a saved interval takes effect without restarting.
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		lastRun := time.Now()
		for now := range ticker.C {
			interval := runtimeconfig.Duration("proxy_check_interval_seconds", proxyCheckDispatcherInterval, time.Second, 1)
			if now.Sub(lastRun) >= interval {
				module.ProxyUseCase.ScheduleProxyCheckDispatcher(context.Background(), 0)
				lastRun = now
			}
		}
	}()
}
