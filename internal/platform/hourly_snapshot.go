package platform

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"
)

// ponytail: two concurrent snapshot refreshes per process; use the job queue
// if the number of independently deployed API replicas grows substantially.
var hourlySnapshotSlots = make(chan struct{}, 2)

type HourlySnapshotCache[T any] struct {
	redis redis.UniversalClient
}

type hourlySnapshot[T any] struct {
	UpdatedAt time.Time `json:"updatedAt"`
	Data      T         `json:"data"`
}

func NewHourlySnapshotCache[T any](client redis.UniversalClient) *HourlySnapshotCache[T] {
	if client == nil {
		return nil
	}
	if standalone, ok := client.(*redis.Client); ok {
		// go-redis ignores context deadlines for socket I/O by default.
		client = standalone.WithTimeout(200 * time.Millisecond)
	}
	return &HourlySnapshotCache[T]{redis: client}
}

// Get returns the last snapshot immediately, including when it is stale.
// A cold cache returns nil; the caller must supply a cheap fallback, never a
// synchronous full-statistics query. Redis failure also leaves reads usable.
func (c *HourlySnapshotCache[T]) Get(ctx context.Context, key string, load func(context.Context) (T, error)) *T {
	cacheCtx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	cached, fresh, err := c.Read(cacheCtx, key)
	if err != nil {
		slog.WarnContext(ctx, "read hourly snapshot failed", "key", key, "error", err)
		return nil
	}
	if fresh || load == nil {
		return cached
	}
	select {
	case hourlySnapshotSlots <- struct{}{}:
	default:
		return cached
	}
	// Keep this marker on failure too, so a failing aggregate is not retried on
	// every page load. This is a Redis refresh guard, not a database lock.
	claimed, err := c.redis.SetNX(cacheCtx, key+":refresh", "1", time.Hour).Result()
	if err != nil || !claimed {
		<-hourlySnapshotSlots
		return cached
	}
	go func() {
		defer func() { <-hourlySnapshotSlots }()
		refreshCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
		defer stop()
		data, err := load(refreshCtx)
		if err == nil {
			err = c.store(refreshCtx, key, data, false)
		}
		if err != nil {
			slog.WarnContext(refreshCtx, "refresh hourly snapshot failed", "key", key, "error", err)
		}
	}()
	return cached
}

// Read returns a snapshot and its freshness without scheduling any work.
func (c *HourlySnapshotCache[T]) Read(ctx context.Context, key string) (*T, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	payload, err := c.redis.Get(ctx, key).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var snapshot hourlySnapshot[T]
	if json.Unmarshal(payload, &snapshot) != nil {
		return nil, false, nil
	}
	return &snapshot.Data, time.Since(snapshot.UpdatedAt) < time.Hour, nil
}

// Refresh is called by a durable queue worker. Busy capacity and loader errors
// are returned to the queue for retry; no one-hour failure marker is written.
func (c *HourlySnapshotCache[T]) Refresh(ctx context.Context, key string, load func(context.Context) (T, error)) error {
	_, fresh, err := c.Read(ctx, key)
	if err != nil || fresh {
		return err
	}
	select {
	case hourlySnapshotSlots <- struct{}{}:
		defer func() { <-hourlySnapshotSlots }()
	default:
		return ErrBackgroundExecutionDeferred
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	data, err := load(ctx)
	if err != nil {
		return err
	}
	return c.store(ctx, key, data, false)
}

// Seed publishes a cheap partial result without replacing a completed snapshot.
func (c *HourlySnapshotCache[T]) Seed(ctx context.Context, key string, data T) {
	if err := c.store(ctx, key, data, true); err != nil {
		slog.WarnContext(ctx, "seed hourly snapshot failed", "key", key, "error", err)
	}
}

func (c *HourlySnapshotCache[T]) store(ctx context.Context, key string, data T, seed bool) error {
	snapshot := hourlySnapshot[T]{Data: data}
	if !seed {
		snapshot.UpdatedAt = time.Now().UTC()
	}
	payload, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	if seed {
		return c.redis.SetNX(ctx, key, payload, 24*time.Hour).Err()
	}
	return c.redis.Set(ctx, key, payload, 24*time.Hour).Err()
}
