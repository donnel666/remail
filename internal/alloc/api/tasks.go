package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	allocapp "github.com/donnel666/remail/internal/alloc/app"
	"github.com/donnel666/remail/internal/alloc/domain"
	allocinfra "github.com/donnel666/remail/internal/alloc/infra"
	"github.com/hibiken/asynq"
)

const candidateRefreshDispatcherInterval = 30 * time.Second

func RegisterAllocationTaskHandlers(mux *asynq.ServeMux, module *Module) {
	mux.HandleFunc(allocinfra.TypeCandidateRefreshDispatcher, func(ctx context.Context, _ *asynq.Task) error {
		if module == nil || module.UseCase == nil {
			return nil
		}
		defer module.UseCase.ScheduleCandidateRefreshDispatcher(context.Background(), candidateRefreshDispatcherInterval)
		result, err := module.UseCase.DispatchCandidateRefreshJobs(ctx, 0)
		if err != nil {
			slog.Warn("candidate refresh dispatcher failed", "error", err)
			return err
		}
		if result != nil && (result.Attempted > 0 || result.Expired > 0) {
			slog.Info(
				"candidate refresh dispatcher finished",
				"attempted", result.Attempted,
				"queued", result.Queued,
				"failed", result.Failed,
				"expired", result.Expired,
			)
		}
		return nil
	})
	if module != nil && module.UseCase != nil {
		module.UseCase.ScheduleCandidateRefreshDispatcher(context.Background(), 0)
		startCandidateRefreshDispatcherSeeder(module)
	}

	mux.HandleFunc(allocinfra.TypeCandidateRefresh, func(ctx context.Context, task *asynq.Task) error {
		var payload allocapp.CandidateRefreshTask
		if err := json.Unmarshal(task.Payload(), &payload); err != nil {
			return fmt.Errorf("decode candidate refresh task: %w: %w", err, asynq.SkipRetry)
		}
		slog.Info("processing candidate refresh task", "job_id", payload.JobID, "request_id", payload.RequestID)
		if err := module.UseCase.ProcessCandidateRefresh(ctx, payload); err != nil {
			slog.Warn("candidate refresh task failed", "job_id", payload.JobID, "request_id", payload.RequestID, "error", err)
			if errors.Is(err, domain.ErrAllocationNotFound) || errors.Is(err, domain.ErrInvalidAllocationRequest) {
				return fmt.Errorf("non-retryable candidate refresh task failure: %w: %w", err, asynq.SkipRetry)
			}
			return err
		}
		slog.Info("candidate refresh task finished", "job_id", payload.JobID, "request_id", payload.RequestID)
		return nil
	})
}

func startCandidateRefreshDispatcherSeeder(module *Module) {
	if module == nil || module.UseCase == nil {
		return
	}
	go func() {
		ticker := time.NewTicker(candidateRefreshDispatcherInterval)
		defer ticker.Stop()
		for range ticker.C {
			module.UseCase.ScheduleCandidateRefreshDispatcher(context.Background(), 0)
		}
	}()
}
