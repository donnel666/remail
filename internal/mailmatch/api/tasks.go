package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	mailmatchapp "github.com/donnel666/remail/internal/mailmatch/app"
	mailmatchinfra "github.com/donnel666/remail/internal/mailmatch/infra"
	"github.com/hibiken/asynq"
)

const fetchDispatcherInterval = 15 * time.Second

func RegisterTaskHandlers(mux *asynq.ServeMux, module *Module) {
	mux.HandleFunc(mailmatchinfra.TypeMailmatchFetchDispatcher, func(ctx context.Context, _ *asynq.Task) error {
		if module == nil || module.UseCase == nil {
			return nil
		}
		defer module.UseCase.ScheduleFetchDispatcher(context.Background(), fetchDispatcherInterval)
		queued, err := module.UseCase.DispatchFetchJobs(ctx, 0)
		if err != nil {
			slog.Warn("mailmatch fetch dispatcher failed", "error", err)
			return err
		}
		if queued > 0 {
			slog.Info("mailmatch fetch dispatcher finished", "queued", queued)
		}
		return nil
	})
	if module != nil && module.UseCase != nil {
		module.UseCase.ScheduleFetchDispatcher(context.Background(), 0)
		startFetchDispatcherSeeder(module)
	}

	mux.HandleFunc(mailmatchinfra.TypeMailmatchFetch, func(ctx context.Context, task *asynq.Task) error {
		if module == nil || module.UseCase == nil {
			return nil
		}
		var payload mailmatchapp.FetchTask
		if err := json.Unmarshal(task.Payload(), &payload); err != nil {
			return fmt.Errorf("decode mailmatch fetch task: %w: %w", err, asynq.SkipRetry)
		}
		if err := module.UseCase.ProcessFetch(ctx, payload); err != nil {
			slog.Warn("mailmatch fetch task failed", "job_id", payload.JobID, "error", err)
			return err
		}
		slog.Info("mailmatch fetch task finished", "job_id", payload.JobID)
		return nil
	})
}

func startFetchDispatcherSeeder(module *Module) {
	if module == nil || module.UseCase == nil {
		return
	}
	go func() {
		ticker := time.NewTicker(fetchDispatcherInterval)
		defer ticker.Stop()
		for range ticker.C {
			module.UseCase.ScheduleFetchDispatcher(context.Background(), 0)
		}
	}()
}
