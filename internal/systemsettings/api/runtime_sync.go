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
	redis redis.UniversalClient
	repo  *infra.Repository

	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
}

func newRuntimeSettingsSync(client redis.UniversalClient, repo *infra.Repository) *runtimeSettingsSync {
	return &runtimeSettingsSync{redis: client, repo: repo}
}

func (s *runtimeSettingsSync) Start(ctx context.Context) {
	if s == nil || s.redis == nil || s.repo == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done != nil {
		return
	}
	listenCtx, cancel := context.WithCancel(ctx)
	pubsub := s.redis.Subscribe(listenCtx, runtimeSettingsChannel)
	subscribeCtx, cancelSubscribe := context.WithTimeout(listenCtx, 2*time.Second)
	_, subscribeErr := pubsub.Receive(subscribeCtx)
	cancelSubscribe()
	if subscribeErr != nil && !errors.Is(subscribeErr, context.Canceled) {
		slog.Warn("subscribe to system settings runtime updates failed", "error", subscribeErr)
	}
	// Reload after the subscription is active so an update racing with startup
	// cannot be missed between the initial database load and Redis Subscribe.
	if err := s.reload(listenCtx); err != nil && !errors.Is(err, context.Canceled) {
		slog.Warn("reload system settings after Redis subscribe failed", "error", err)
	}
	messages := pubsub.Channel(redis.WithChannelSize(16))
	done := make(chan struct{})
	s.cancel = cancel
	s.done = done
	go func() {
		defer close(done)
		defer pubsub.Close()
		for {
			select {
			case <-listenCtx.Done():
				return
			case _, ok := <-messages:
				if !ok {
					return
				}
				if err := s.reload(listenCtx); err != nil && !errors.Is(err, context.Canceled) {
					slog.Warn("reload system settings after Redis update failed", "error", err)
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
