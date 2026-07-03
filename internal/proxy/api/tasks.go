package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	proxyapp "github.com/donnel666/remail/internal/proxy/app"
	proxydomain "github.com/donnel666/remail/internal/proxy/domain"
	proxyinfra "github.com/donnel666/remail/internal/proxy/infra"
	"github.com/hibiken/asynq"
)

const proxyCheckDispatcherInterval = 15 * time.Second

func RegisterProxyTaskHandlers(mux *asynq.ServeMux, module *ProxyModule) {
	mux.HandleFunc(proxyinfra.TypeProxyCheckDispatcher, func(ctx context.Context, task *asynq.Task) error {
		defer module.ProxyUseCase.ScheduleProxyCheckDispatcher(context.Background(), proxyCheckDispatcherInterval)
		result, err := module.ProxyUseCase.DispatchPendingProxyCheckJobs(ctx, 0)
		if err != nil {
			module.ProxyUseCase.LogTaskFailure(ctx, "proxy.check_dispatcher_failed", "", "proxy_check_job", "dispatcher", "Proxy check dispatcher failed.", err)
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

	mux.HandleFunc(proxyinfra.TypeProxyCheckBatch, func(ctx context.Context, task *asynq.Task) error {
		var payload proxyapp.ProxyCheckBatchTask
		if err := json.Unmarshal(task.Payload(), &payload); err != nil {
			return fmt.Errorf("decode proxy check batch task: %w: %w", err, asynq.SkipRetry)
		}

		slog.Info(
			"processing proxy check batch task",
			"operator_user_id", payload.OperatorUserID,
			"request_id", payload.RequestID,
		)
		result, err := module.ProxyUseCase.RunCheckBatch(ctx, payload)
		if err != nil {
			module.ProxyUseCase.LogTaskFailure(ctx, "proxy.check_batch_task_failed", payload.RequestID, "proxy", "batch", "Proxy check batch task failed.", err)
			if errors.Is(err, proxydomain.ErrInvalidProxyFilter) {
				return fmt.Errorf("non-retryable proxy check batch task failure: %w: %w", err, asynq.SkipRetry)
			}
			slog.Warn(
				"proxy check batch task failed",
				"operator_user_id", payload.OperatorUserID,
				"request_id", payload.RequestID,
				"error", err,
			)
			return err
		}
		slog.Info(
			"proxy check batch task finished",
			"operator_user_id", payload.OperatorUserID,
			"request_id", payload.RequestID,
			"requested", result.Requested,
			"queued", result.Queued,
		)
		return nil
	})

	mux.HandleFunc(proxyinfra.TypeProxyCheck, func(ctx context.Context, task *asynq.Task) error {
		var payload proxyapp.ProxyCheckTask
		if err := json.Unmarshal(task.Payload(), &payload); err != nil {
			return fmt.Errorf("decode proxy check task: %w: %w", err, asynq.SkipRetry)
		}

		slog.Info(
			"processing proxy check task",
			"proxy_id", payload.ProxyID,
			"operator_user_id", payload.OperatorUserID,
			"request_id", payload.RequestID,
		)
		updated, err := module.ProxyUseCase.RunCheck(ctx, payload)
		if err != nil {
			module.ProxyUseCase.LogTaskFailure(ctx, "proxy.check_task_failed", payload.RequestID, "proxy", fmt.Sprintf("%d", payload.ProxyID), "Proxy check task failed.", err)
			if errors.Is(err, proxydomain.ErrProxyNotFound) || errors.Is(err, proxydomain.ErrInvalidProxyStatus) {
				return fmt.Errorf("non-retryable proxy check task failure: %w: %w", err, asynq.SkipRetry)
			}
			slog.Warn(
				"proxy check task failed",
				"proxy_id", payload.ProxyID,
				"operator_user_id", payload.OperatorUserID,
				"request_id", payload.RequestID,
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
			"operator_user_id", payload.OperatorUserID,
			"request_id", payload.RequestID,
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
		ticker := time.NewTicker(proxyCheckDispatcherInterval)
		defer ticker.Stop()
		for range ticker.C {
			module.ProxyUseCase.ScheduleProxyCheckDispatcher(context.Background(), 0)
		}
	}()
}
