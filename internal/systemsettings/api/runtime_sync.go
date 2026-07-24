package api

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/donnel666/remail/internal/systemsettings/infra"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"github.com/redis/go-redis/v9"
)

const runtimeSettingsChannel = "remail:system_settings:changed"
const runtimeSettingsReconcileInterval = 30 * time.Second

type redisRuntimeSettingsPublisher struct {
	redis redis.UniversalClient
}

func newRedisRuntimeSettingsPublisher(client redis.UniversalClient) *redisRuntimeSettingsPublisher {
	return &redisRuntimeSettingsPublisher{redis: client}
}

func (p *redisRuntimeSettingsPublisher) Publish(ctx context.Context) error {
	if p == nil || p.redis == nil {
		return nil
	}
	return p.redis.Publish(ctx, runtimeSettingsChannel, "changed").Err()
}

type runtimeSettingsSync struct {
	redis             redis.UniversalClient
	repo              *infra.Repository
	reconcileInterval time.Duration

	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
}

func newRuntimeSettingsSync(client redis.UniversalClient, repo *infra.Repository) *runtimeSettingsSync {
	return &runtimeSettingsSync{redis: client, repo: repo, reconcileInterval: runtimeSettingsReconcileInterval}
}

func (s *runtimeSettingsSync) Start(ctx context.Context) {
	if s == nil || s.repo == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done != nil {
		return
	}
	listenCtx, cancel := context.WithCancel(ctx)
	var pubsub *redis.PubSub
	var messages <-chan *redis.Message
	if s.redis != nil {
		pubsub = s.redis.Subscribe(listenCtx, runtimeSettingsChannel)
		subscribeCtx, cancelSubscribe := context.WithTimeout(listenCtx, 2*time.Second)
		_, subscribeErr := pubsub.Receive(subscribeCtx)
		cancelSubscribe()
		if subscribeErr != nil && !errors.Is(subscribeErr, context.Canceled) {
			slog.Warn("subscribe to system settings runtime updates failed", "error", subscribeErr)
		}
		messages = pubsub.Channel(redis.WithChannelSize(16))
	}
	// Reload after the subscription is active so an update racing with startup
	// cannot be missed between the initial database load and Redis Subscribe.
	if err := s.reload(listenCtx); err != nil && !errors.Is(err, context.Canceled) {
		slog.Warn("reload system settings after Redis subscribe failed", "error", err)
	}
	done := make(chan struct{})
	s.cancel = cancel
	s.done = done
	interval := s.reconcileInterval
	if interval <= 0 {
		interval = runtimeSettingsReconcileInterval
	}
	go func() {
		defer close(done)
		if pubsub != nil {
			defer pubsub.Close()
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-listenCtx.Done():
				return
			case _, ok := <-messages:
				if !ok {
					messages = nil
					continue
				}
				if err := s.reload(listenCtx); err != nil && !errors.Is(err, context.Canceled) {
					slog.Warn("reload system settings after Redis update failed", "error", err)
				}
			case <-ticker.C:
				if err := s.reload(listenCtx); err != nil && !errors.Is(err, context.Canceled) {
					slog.Warn("periodic system settings reconciliation failed", "error", err)
				}
			}
		}
	}()
}

func (s *runtimeSettingsSync) reload(ctx context.Context) error {
	settings, err := s.repo.List(ctx)
	if err != nil {
		return err
	}
	runtimeconfig.Replace(settings)
	return nil
}

func (s *runtimeSettingsSync) Close(ctx context.Context) {
	if s == nil {
		return
	}
	s.mu.Lock()
	cancel, done := s.cancel, s.done
	s.cancel, s.done = nil, nil
	s.mu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	select {
	case <-done:
	case <-ctx.Done():
	}
}
