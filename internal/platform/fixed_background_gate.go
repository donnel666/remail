package platform

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"
)

const (
	BackgroundValidationConcurrency = 10
	BackgroundAliasConcurrency      = 20
	fixedBackgroundDispatchTimeout  = time.Minute
	fixedBackgroundDispatchLockTTL  = 2 * time.Minute
)

var (
	fixedGateOutstandingScript = redis.NewScript(`
local outstanding = 0
for _, prefix in ipairs(KEYS) do
  outstanding = outstanding + redis.call('LLEN', prefix .. 'pending')
    + redis.call('LLEN', prefix .. 'active')
    + redis.call('ZCARD', prefix .. 'scheduled')
    + redis.call('ZCARD', prefix .. 'retry')
end
return outstanding
`)
	fixedGateUnlockScript = redis.NewScript(`
if redis.call('GET', KEYS[1]) == ARGV[1] then
  return redis.call('DEL', KEYS[1])
end
return 0
`)
	fixedGateAcquireScript = redis.NewScript(`
for _, task in ipairs(redis.call('HKEYS', KEYS[1])) do
  if redis.call('HGET', task, 'state') ~= 'active' then
    redis.call('HDEL', KEYS[1], task)
  end
end
if redis.call('HGET', KEYS[2], 'state') ~= 'active' then
  return -1
end
if redis.call('HEXISTS', KEYS[1], KEYS[2]) == 0 and redis.call('HLEN', KEYS[1]) >= tonumber(ARGV[1]) then
  return 0
end
redis.call('HSET', KEYS[1], KEYS[2], ARGV[2])
return 1
`)
	fixedGateReleaseScript = redis.NewScript(`
if redis.call('HGET', KEYS[1], ARGV[1]) == ARGV[2] then
  return redis.call('HDEL', KEYS[1], ARGV[1])
end
return 0
`)
)

// FixedBackgroundExecutionGate counts durable Asynq work instead of anonymous
// reservations. Dispatch is serialized until selected tasks have been enqueued.
type FixedBackgroundExecutionGate struct {
	limit       int
	redis       redis.UniversalClient
	queues      []string
	dispatchKey string
	activeKey   string
}

func NewFixedBackgroundExecutionGate(limit int, client redis.UniversalClient, name string, queues ...string) *FixedBackgroundExecutionGate {
	return &FixedBackgroundExecutionGate{
		limit: max(1, limit), redis: client, queues: queues,
		dispatchKey: "remail:fixed_slots:{" + name + "}:dispatch",
		// Old anonymous leases cannot identify running tasks and must not be reused.
		activeKey: "remail:fixed_slots:{" + name + "}:tasks",
	}
}

func (g *FixedBackgroundExecutionGate) Dispatch(ctx context.Context, maximum int, dispatch func(context.Context, int) error) error {
	if g == nil || g.redis == nil || len(g.queues) == 0 {
		return fmt.Errorf("fixed background gate is unavailable")
	}
	// Dispatch must stop before its mutex can expire and admit another dispatcher.
	ctx, cancel := context.WithTimeout(ctx, fixedBackgroundDispatchTimeout)
	defer cancel()
	owner := NewUUIDV7String()
	locked, err := g.redis.SetNX(ctx, g.dispatchKey, owner, fixedBackgroundDispatchLockTTL).Result()
	if err != nil {
		return fmt.Errorf("lock fixed background dispatch: %w", err)
	}
	if !locked {
		return nil
	}
	defer func() {
		releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if err := fixedGateUnlockScript.Run(releaseCtx, g.redis, []string{g.dispatchKey}, owner).Err(); err != nil {
			slog.Warn("release fixed background dispatch lock failed", "key", g.dispatchKey, "error", err)
		}
	}()
	keys := make([]string, len(g.queues))
	for i, queue := range g.queues {
		keys[i] = "asynq:{" + queue + "}:"
	}
	outstanding, err := fixedGateOutstandingScript.Run(ctx, g.redis, keys).Int()
	if err != nil {
		return fmt.Errorf("count fixed background tasks: %w", err)
	}
	if available := min(maximum, g.limit-outstanding); available > 0 {
		return dispatch(ctx, available)
	}
	return nil
}

// TryAcquire satisfies legacy gate callers, which cannot safely own a fixed slot without a task ID.
func (g *FixedBackgroundExecutionGate) TryAcquire() (func(), bool) {
	return func() {}, false
}

func (g *FixedBackgroundExecutionGate) TryAcquireContext(ctx context.Context) (func(), bool, error) {
	id, _ := asynq.GetTaskID(ctx)
	queue, _ := asynq.GetQueueName(ctx)
	return g.acquireTask(ctx, queue, id)
}

func (g *FixedBackgroundExecutionGate) acquireTask(ctx context.Context, queue, id string) (func(), bool, error) {
	if g == nil || g.redis == nil || id == "" || !slices.Contains(g.queues, queue) {
		return func() {}, false, fmt.Errorf("fixed background task identity is missing or outside its pool")
	}
	taskKey := "asynq:{" + queue + "}:t:" + id
	owner := NewUUIDV7String()
	admitted, err := fixedGateAcquireScript.Run(ctx, g.redis, []string{g.activeKey, taskKey}, g.limit, owner).Int()
	if err != nil {
		return func() {}, false, fmt.Errorf("acquire fixed background execution: %w", err)
	}
	if admitted < 0 {
		return func() {}, false, fmt.Errorf("fixed background task is no longer active")
	}
	if admitted == 0 {
		return func() {}, false, nil
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			if err := fixedGateReleaseScript.Run(releaseCtx, g.redis, []string{g.activeKey}, taskKey, owner).Err(); err != nil {
				slog.Warn("release fixed background execution failed", "queue", queue, "task_id", id, "error", err)
			}
		})
	}, true, nil
}

// DispatchBackgroundTasks treats a full fixed pool as ordinary backpressure.
func DispatchBackgroundTasks(ctx context.Context, gate interface{}, maximum int, dispatch func(context.Context, int) error) error {
	if maximum <= 0 {
		return nil
	}
	if fixed, ok := gate.(*FixedBackgroundExecutionGate); ok {
		return fixed.Dispatch(ctx, maximum, dispatch)
	}
	if available, ok := gate.(interface{ Available() int }); ok {
		maximum = min(maximum, available.Available())
	}
	if maximum > 0 {
		return dispatch(ctx, maximum)
	}
	return nil
}
