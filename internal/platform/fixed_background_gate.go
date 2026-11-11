package platform

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	BackgroundValidationConcurrency = 10
	BackgroundAliasConcurrency      = 20
	fixedBackgroundLeaseTTL         = 90 * time.Minute
)

var (
	fixedGateReserveScript = redis.NewScript(`
local now = tonumber(ARGV[1])
local limit = tonumber(ARGV[2])
local ttl = tonumber(ARGV[3])
local requested = tonumber(ARGV[4])
local reserved = KEYS[1]
local active = KEYS[2]
redis.call('ZREMRANGEBYSCORE', reserved, '-inf', now)
redis.call('ZREMRANGEBYSCORE', active, '-inf', now)
local used = redis.call('ZCARD', reserved) + redis.call('ZCARD', active)
local count = math.min(requested, math.max(0, limit - used))
for i = 1, count do
  redis.call('ZADD', reserved, now + ttl, ARGV[5] .. ':' .. i)
end
if count > 0 then
  redis.call('EXPIRE', reserved, math.ceil(ttl / 1000))
  redis.call('EXPIRE', active, math.ceil(ttl / 1000))
end
return count
`)
	fixedGateAcquireScript = redis.NewScript(`
local now = tonumber(ARGV[1])
local limit = tonumber(ARGV[2])
local ttl = tonumber(ARGV[3])
local active = KEYS[1]
local reserved = KEYS[2]
redis.call('ZREMRANGEBYSCORE', reserved, '-inf', now)
redis.call('ZREMRANGEBYSCORE', active, '-inf', now)
local member = redis.call('ZRANGE', reserved, 0, 0)[1]
if member then
  redis.call('ZREM', reserved, member)
else
  if redis.call('ZCARD', active) + redis.call('ZCARD', reserved) >= limit then
    return ''
  end
  member = ARGV[4]
end
redis.call('ZADD', active, now + ttl, member)
redis.call('EXPIRE', active, math.ceil(ttl / 1000))
redis.call('EXPIRE', reserved, math.ceil(ttl / 1000))
return member
`)
	fixedGateReleaseScript = redis.NewScript(`
return redis.call('ZREM', KEYS[1], ARGV[1])
`)
	fixedGateReleaseReservedScript = redis.NewScript(`
local members = redis.call('ZRANGE', KEYS[1], 0, tonumber(ARGV[1]) - 1)
for _, member in ipairs(members) do
  redis.call('ZREM', KEYS[1], member)
end
return #members
`)
	fixedGateAvailableScript = redis.NewScript(`
local now = tonumber(ARGV[1])
local limit = tonumber(ARGV[2])
local reserved = KEYS[1]
local active = KEYS[2]
redis.call('ZREMRANGEBYSCORE', reserved, '-inf', now)
redis.call('ZREMRANGEBYSCORE', active, '-inf', now)
return math.max(0, limit - redis.call('ZCARD', reserved) - redis.call('ZCARD', active))
`)
	fixedGateStatsScript = redis.NewScript(`
local now = tonumber(ARGV[1])
local reserved = KEYS[1]
local active = KEYS[2]
redis.call('ZREMRANGEBYSCORE', reserved, '-inf', now)
redis.call('ZREMRANGEBYSCORE', active, '-inf', now)
return {redis.call('ZCARD', active), redis.call('ZCARD', reserved)}
`)
	fixedGatePrimeScript = redis.NewScript(`
local now = tonumber(ARGV[1])
local limit = tonumber(ARGV[2])
local ttl = tonumber(ARGV[3])
local desired = tonumber(ARGV[4])
local reserved = KEYS[1]
local active = KEYS[2]
redis.call('ZREMRANGEBYSCORE', reserved, '-inf', now)
redis.call('ZREMRANGEBYSCORE', active, '-inf', now)
local used = redis.call('ZCARD', reserved) + redis.call('ZCARD', active)
local count = math.min(math.max(0, desired - used), math.max(0, limit - used))
for i = 1, count do
  redis.call('ZADD', reserved, now + ttl, ARGV[5] .. ':' .. i)
end
if count > 0 then
  redis.call('EXPIRE', reserved, math.ceil(ttl / 1000))
  redis.call('EXPIRE', active, math.ceil(ttl / 1000))
end
return count
`)
)

// FixedBackgroundExecutionGate is a fixed execution window shared through
// Redis when available. Reservations cover the gap between dispatch and worker
// start, and leases make abandoned reservations recover automatically.
type FixedBackgroundExecutionGate struct {
	mu          sync.Mutex
	limit       int
	active      int
	reserved    int
	redis       redis.UniversalClient
	reservedKey string
	activeKey   string
}

func NewFixedBackgroundExecutionGate(limit int, clients ...redis.UniversalClient) *FixedBackgroundExecutionGate {
	if limit < 1 {
		limit = 1
	}
	gate := &FixedBackgroundExecutionGate{limit: limit}
	if len(clients) > 0 && clients[0] != nil {
		gate.redis = clients[0]
	}
	return gate
}

func (g *FixedBackgroundExecutionGate) WithName(name string) *FixedBackgroundExecutionGate {
	if g != nil && g.redis != nil {
		g.reservedKey = "remail:fixed_slots:{" + name + "}:reserved"
		g.activeKey = "remail:fixed_slots:{" + name + "}:active"
	}
	return g
}

func (g *FixedBackgroundExecutionGate) TryAcquire() (func(), bool) {
	if g == nil {
		return func() {}, true
	}
	if g.redis != nil {
		if g.reservedKey == "" || g.activeKey == "" {
			return func() {}, false
		}
		member, err := fixedGateAcquireScript.Run(
			context.Background(), g.redis,
			[]string{g.activeKey, g.reservedKey},
			time.Now().UnixMilli(), g.limit, fixedBackgroundLeaseTTL.Milliseconds(), NewUUIDV7String(),
		).Text()
		if err != nil || member == "" {
			return func() {}, false
		}
		var once sync.Once
		return func() {
			once.Do(func() {
				_ = fixedGateReleaseScript.Run(context.Background(), g.redis, []string{g.activeKey}, member).Err()
			})
		}, true
	}

	g.mu.Lock()
	if g.reserved == 0 && g.active >= g.limit {
		g.mu.Unlock()
		return func() {}, false
	}
	if g.reserved > 0 {
		g.reserved--
	}
	g.active++
	g.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			g.mu.Lock()
			if g.active > 0 {
				g.active--
			}
			g.mu.Unlock()
		})
	}, true
}

func (g *FixedBackgroundExecutionGate) TryAcquireQueue(string) (func(), bool) {
	return g.TryAcquire()
}

func (g *FixedBackgroundExecutionGate) Available() int {
	if g == nil {
		return 0
	}
	if g.redis != nil {
		value, err := fixedGateAvailableScript.Run(
			context.Background(), g.redis, []string{g.reservedKey, g.activeKey}, time.Now().UnixMilli(), g.limit,
		).Int()
		if err != nil {
			return 0
		}
		return value
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return max(0, g.limit-g.active-g.reserved)
}

func (g *FixedBackgroundExecutionGate) Reserve(maximum int) int {
	if g == nil || maximum <= 0 {
		return 0
	}
	if g.redis != nil {
		value, err := fixedGateReserveScript.Run(
			context.Background(), g.redis, []string{g.reservedKey, g.activeKey},
			time.Now().UnixMilli(), g.limit, fixedBackgroundLeaseTTL.Milliseconds(), maximum, NewUUIDV7String(),
		).Int()
		if err != nil {
			return 0
		}
		return value
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	reserved := min(maximum, max(0, g.limit-g.active-g.reserved))
	g.reserved += reserved
	return reserved
}

func (g *FixedBackgroundExecutionGate) ReleaseReserved(count int) {
	if g == nil || count <= 0 {
		return
	}
	if g.redis != nil {
		_, _ = fixedGateReleaseReservedScript.Run(context.Background(), g.redis, []string{g.reservedKey}, count).Int()
		return
	}
	g.mu.Lock()
	g.reserved = max(0, g.reserved-count)
	g.mu.Unlock()
}

func (g *FixedBackgroundExecutionGate) Stats() (limit, active, reserved int) {
	if g == nil {
		return 0, 0, 0
	}
	if g.redis != nil {
		values, err := fixedGateStatsScript.Run(context.Background(), g.redis, []string{g.reservedKey, g.activeKey}, time.Now().UnixMilli()).Slice()
		if err != nil || len(values) != 2 {
			return g.limit, 0, 0
		}
		active, _ = redisToInt(values[0])
		reserved, _ = redisToInt(values[1])
		return g.limit, active, reserved
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.limit, g.active, g.reserved
}

func (g *FixedBackgroundExecutionGate) Prime(ctx context.Context, desired int) error {
	if g == nil || g.redis == nil || desired <= 0 {
		return nil
	}
	if g.reservedKey == "" || g.activeKey == "" {
		return fmt.Errorf("fixed background gate name is missing")
	}
	_, err := fixedGatePrimeScript.Run(
		ctx, g.redis, []string{g.reservedKey, g.activeKey},
		time.Now().UnixMilli(), g.limit, fixedBackgroundLeaseTTL.Milliseconds(), desired, NewUUIDV7String(),
	).Int()
	return err
}

func redisToInt(value any) (int, bool) {
	switch typed := value.(type) {
	case int64:
		return int(typed), true
	case string:
		var parsed int
		_, err := fmt.Sscan(typed, &parsed)
		return parsed, err == nil
	default:
		return 0, false
	}
}

// ReserveBackgroundDispatch reserves free fixed-gate slots before a dispatcher
// queries the database. The returned function releases slots not represented by
// successfully queued business tasks.
func ReserveBackgroundDispatch(gate interface{}, maximum int) (int, func(used int)) {
	if maximum <= 0 {
		return 0, func(int) {}
	}
	if fixed, ok := gate.(interface {
		Reserve(int) int
		ReleaseReserved(int)
	}); ok {
		reserved := fixed.Reserve(maximum)
		return reserved, func(used int) {
			fixed.ReleaseReserved(max(0, reserved-used))
		}
	}
	if available, ok := gate.(interface{ Available() int }); ok {
		return min(maximum, max(0, available.Available())), func(int) {}
	}
	return maximum, func(int) {}
}
