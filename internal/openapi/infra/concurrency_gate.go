package infra

import (
	"context"
	"fmt"
	"time"

	openapiapp "github.com/donnel666/remail/internal/openapi/app"
	"github.com/redis/go-redis/v9"
)

const (
	apiKeyConcurrencyKeyPrefix = "remail:openapi:apikey-concurrency:"
	apiKeyConcurrencyLeaseTTL  = 5 * time.Minute
)

type APIKeyConcurrencyGate struct {
	redis redis.UniversalClient
}

var _ openapiapp.APIKeyConcurrencyGate = (*APIKeyConcurrencyGate)(nil)

func NewAPIKeyConcurrencyGate(client redis.UniversalClient) *APIKeyConcurrencyGate {
	return &APIKeyConcurrencyGate{redis: client}
}

func (g *APIKeyConcurrencyGate) Acquire(ctx context.Context, keyID uint, limit int, leaseID string) (int, bool, error) {
	if g == nil || g.redis == nil || keyID == 0 || limit <= 0 || leaseID == "" {
		return 0, false, fmt.Errorf("api key concurrency gate is unavailable")
	}
	now := time.Now().UnixMilli()
	active, err := apiKeyConcurrencyAcquireScript.Run(ctx, g.redis, []string{apiKeyConcurrencyKey(keyID)}, now, limit, leaseID, now+apiKeyConcurrencyLeaseTTL.Milliseconds(), apiKeyConcurrencyLeaseTTL.Milliseconds()).Int()
	if err != nil {
		return 0, false, fmt.Errorf("acquire api key concurrency lease: %w", err)
	}
	return active, active > 0, nil
}

func (g *APIKeyConcurrencyGate) Release(ctx context.Context, keyID uint, leaseID string) error {
	if g == nil || g.redis == nil || keyID == 0 || leaseID == "" {
		return nil
	}
	if err := apiKeyConcurrencyReleaseScript.Run(ctx, g.redis, []string{apiKeyConcurrencyKey(keyID)}, leaseID).Err(); err != nil {
		return fmt.Errorf("release api key concurrency lease: %w", err)
	}
	return nil
}

func apiKeyConcurrencyKey(keyID uint) string {
	return fmt.Sprintf("%s%d", apiKeyConcurrencyKeyPrefix, keyID)
}

var apiKeyConcurrencyAcquireScript = redis.NewScript(`
redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', ARGV[1])
local active = redis.call('ZCARD', KEYS[1])
if active >= tonumber(ARGV[2]) then
  return 0
end
redis.call('ZADD', KEYS[1], ARGV[4], ARGV[3])
redis.call('PEXPIRE', KEYS[1], ARGV[5])
return active + 1
`)

var apiKeyConcurrencyReleaseScript = redis.NewScript(`
redis.call('ZREM', KEYS[1], ARGV[1])
if redis.call('ZCARD', KEYS[1]) == 0 then
  redis.call('DEL', KEYS[1])
end
return 1
`)
