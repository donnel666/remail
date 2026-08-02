package gmail

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/donnel666/remail/internal/platform"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"github.com/hibiken/asynq"
)

const (
	typeGmailSync       = "gmail:smsbower_sync"
	typeGmailDispatch   = "gmail:dispatch"
	typeGmailProvision  = "gmail:provision"
	typeGmailPoll       = "gmail:poll"
	gmailSyncTimeout    = 45 * time.Second
	gmailDispatchPeriod = 5 * time.Second
)

func (s *Service) ScheduleSync(ctx context.Context) error {
	if s == nil || s.queue == nil {
		return errors.New("gmail: task queue unavailable")
	}
	interval := time.Duration(runtimeconfig.Int("smsbower_sync_interval_minutes", 5, 1)) * time.Minute
	_, err := s.queue.EnqueueContext(ctx, asynq.NewTask(typeGmailSync, nil),
		asynq.Queue(platform.QueueBackgroundInventory), asynq.Unique(max(interval, gmailSyncTimeout)),
		asynq.MaxRetry(platform.BackgroundTaskMaxRetryValue()), asynq.Timeout(gmailSyncTimeout), asynq.Retention(0))
	if errors.Is(err, asynq.ErrDuplicateTask) {
		return nil
	}
	return err
}

func (s *Service) scheduleDispatcher(ctx context.Context) error {
	if s == nil || s.queue == nil {
		return errors.New("gmail: task queue unavailable")
	}
	_, err := s.queue.EnqueueContext(ctx, asynq.NewTask(typeGmailDispatch, nil),
		asynq.Queue(platform.QueueBackgroundInventory), asynq.Unique(gmailDispatchPeriod),
		asynq.MaxRetry(platform.BackgroundTaskMaxRetryValue()), asynq.Timeout(30*time.Second), asynq.Retention(0))
	if errors.Is(err, asynq.ErrDuplicateTask) {
		return nil
	}
	return err
}

func RegisterTaskHandlers(mux *asynq.ServeMux, service *Service) func(context.Context) {
	mux.HandleFunc(typeGmailSync, func(ctx context.Context, _ *asynq.Task) error {
		return gmailSyncTaskError(service.Sync(ctx))
	})
	mux.HandleFunc(typeGmailDispatch, func(ctx context.Context, _ *asynq.Task) error {
		_, err := service.DispatchDueSessions(ctx, 200)
		return err
	})
	mux.HandleFunc(typeGmailProvision, func(ctx context.Context, task *asynq.Task) error {
		payload, err := decodeSessionTask(task)
		if err != nil {
			return err
		}
		return service.Provision(ctx, payload.SessionID)
	})
	mux.HandleFunc(typeGmailPoll, func(ctx context.Context, task *asynq.Task) error {
		payload, err := decodeSessionTask(task)
		if err != nil {
			return err
		}
		return service.Poll(ctx, payload.SessionID)
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		lastSync := time.Time{}
		lastDispatch := time.Time{}
		for {
			now := time.Now()
			if lastSync.IsZero() || now.Sub(lastSync) >= time.Duration(runtimeconfig.Int("smsbower_sync_interval_minutes", 5, 1))*time.Minute {
				if err := service.ScheduleSync(ctx); err != nil && !errors.Is(err, context.Canceled) {
					slog.Warn("schedule SMSBower sync failed", "error", err)
				}
				lastSync = now
			}
			if lastDispatch.IsZero() || now.Sub(lastDispatch) >= gmailDispatchPeriod {
				if err := service.scheduleDispatcher(ctx); err != nil && !errors.Is(err, context.Canceled) {
					slog.Warn("schedule Gmail dispatcher failed", "error", err)
				}
				lastDispatch = now
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

func gmailSyncTaskError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrBadKey) {
		return fmt.Errorf("sync SMSBower: %w: %w", err, asynq.SkipRetry)
	}
	return fmt.Errorf("sync SMSBower: %w", err)
}

func decodeSessionTask(task *asynq.Task) (*sessionTaskPayload, error) {
	var payload sessionTaskPayload
	if err := json.Unmarshal(task.Payload(), &payload); err != nil || payload.SessionID == 0 {
		return nil, fmt.Errorf("decode Gmail session task: %w", asynq.SkipRetry)
	}
	return &payload, nil
}
