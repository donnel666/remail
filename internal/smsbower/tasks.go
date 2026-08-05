package smsbower

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/donnel666/remail/internal/platform"
	"github.com/hibiken/asynq"
)

const (
	typeSync       = "smsbower:sync"
	typeDispatch   = "smsbower:dispatch"
	typeProvision  = "smsbower:provision"
	typePoll       = "smsbower:poll"
	syncTimeout    = 45 * time.Second
	dispatchPeriod = 5 * time.Second
)

type orderTaskPayload struct {
	OrderID uint `json:"orderId"`
}

func (s *Service) ScheduleSync(ctx context.Context) error {
	if s == nil || s.queue == nil {
		return errors.New("smsbower: task queue unavailable")
	}
	interval := s.syncInterval(ctx)
	_, err := s.queue.EnqueueContext(ctx, asynq.NewTask(typeSync, nil),
		asynq.Queue(platform.QueueBackgroundInventory), asynq.Unique(max(interval, syncTimeout)),
		asynq.MaxRetry(platform.BackgroundTaskMaxRetryValue()), asynq.Timeout(syncTimeout), asynq.Retention(0))
	if errors.Is(err, asynq.ErrDuplicateTask) {
		return nil
	}
	return err
}

func (s *Service) syncInterval(ctx context.Context) time.Duration {
	var minutes uint
	if s == nil || s.db == nil || s.dbFor(ctx).Model(&configModel{}).Where("id = 1").Pluck("sync_interval_minutes", &minutes).Error != nil || minutes == 0 {
		minutes = 5
	}
	return time.Duration(minutes) * time.Minute
}

func (s *Service) scheduleDispatcher(ctx context.Context) error {
	if s == nil || s.queue == nil {
		return errors.New("smsbower: task queue unavailable")
	}
	_, err := s.queue.EnqueueContext(ctx, asynq.NewTask(typeDispatch, nil),
		asynq.Queue(platform.QueueBackgroundInventory), asynq.Unique(dispatchPeriod),
		asynq.MaxRetry(platform.BackgroundTaskMaxRetryValue()), asynq.Timeout(30*time.Second), asynq.Retention(0))
	if errors.Is(err, asynq.ErrDuplicateTask) {
		return nil
	}
	return err
}

func (s *Service) scheduleProvision(ctx context.Context, orderID uint) error {
	if orderID == 0 || s == nil || s.queue == nil {
		return errors.New("smsbower: task queue unavailable")
	}
	payload, _ := json.Marshal(orderTaskPayload{OrderID: orderID})
	_, err := s.queue.EnqueueContext(ctx, asynq.NewTask(typeProvision, payload),
		asynq.Queue(platform.QueueDefault), asynq.Unique(time.Minute), asynq.MaxRetry(2),
		asynq.Timeout(30*time.Second), asynq.Retention(0))
	if errors.Is(err, asynq.ErrDuplicateTask) {
		return nil
	}
	return err
}

func (s *Service) schedulePoll(ctx context.Context, orderID uint) error {
	if orderID == 0 || s == nil || s.queue == nil {
		return errors.New("smsbower: task queue unavailable")
	}
	payload, _ := json.Marshal(orderTaskPayload{OrderID: orderID})
	_, err := s.queue.EnqueueContext(ctx, asynq.NewTask(typePoll, payload),
		asynq.Queue(platform.QueueMailfetch), asynq.Unique(pollLease), asynq.MaxRetry(2),
		asynq.Timeout(20*time.Second), asynq.Retention(0))
	if errors.Is(err, asynq.ErrDuplicateTask) {
		return nil
	}
	return err
}

func RegisterTaskHandlers(mux *asynq.ServeMux, service *Service) func(context.Context) {
	mux.HandleFunc(typeSync, func(ctx context.Context, _ *asynq.Task) error {
		return syncTaskError(service.Sync(ctx))
	})
	mux.HandleFunc(typeDispatch, func(ctx context.Context, _ *asynq.Task) error {
		_, err := service.DispatchDueOrders(ctx, 200)
		return err
	})
	mux.HandleFunc(typeProvision, func(ctx context.Context, task *asynq.Task) error {
		payload, err := decodeOrderTask(task)
		if err != nil {
			return err
		}
		return service.Provision(ctx, payload.OrderID)
	})
	mux.HandleFunc(typePoll, func(ctx context.Context, task *asynq.Task) error {
		payload, err := decodeOrderTask(task)
		if err != nil {
			return err
		}
		return service.Poll(ctx, payload.OrderID)
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
			if lastSync.IsZero() || now.Sub(lastSync) >= service.syncInterval(ctx) {
				if err := service.ScheduleSync(ctx); err != nil && !errors.Is(err, context.Canceled) {
					slog.Warn("schedule SMSBower sync failed", "error", err)
				}
				lastSync = now
			}
			if lastDispatch.IsZero() || now.Sub(lastDispatch) >= dispatchPeriod {
				if err := service.scheduleDispatcher(ctx); err != nil && !errors.Is(err, context.Canceled) {
					slog.Warn("schedule SMSBower dispatcher failed", "error", err)
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

func decodeOrderTask(task *asynq.Task) (orderTaskPayload, error) {
	var payload orderTaskPayload
	if task == nil || json.Unmarshal(task.Payload(), &payload) != nil || payload.OrderID == 0 {
		return payload, fmt.Errorf("decode SMSBower order task: %w", asynq.SkipRetry)
	}
	return payload, nil
}

func syncTaskError(err error) error {
	if errors.Is(err, ErrBadKey) {
		return fmt.Errorf("SMSBower API key rejected: %w", asynq.SkipRetry)
	}
	return err
}
