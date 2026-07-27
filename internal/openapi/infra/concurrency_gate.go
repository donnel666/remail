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
	apiKeyUserUsageKeyPrefix   = "remail:openapi:user-usage:"
	apiKeyConcurrencyLeaseTTL  = 5 * time.Minute
	apiKeyRPMWindow            = time.Minute
	apiKeyRPMHashTTL           = apiKeyRPMWindow + time.Second
)

type APIKeyConcurrencyGate struct {
	redis redis.UniversalClient
}

var _ openapiapp.APIKeyConcurrencyGate = (*APIKeyConcurrencyGate)(nil)

func NewAPIKeyConcurrencyGate(client redis.UniversalClient) *APIKeyConcurrencyGate {
	return &APIKeyConcurrencyGate{redis: client}
}

func (g *APIKeyConcurrencyGate) Acquire(ctx context.Context, userID, keyID uint, limit int, leaseID string) (int, bool, error) {
	if g == nil || g.redis == nil || userID == 0 || keyID == 0 || limit <= 0 || leaseID == "" {
		return 0, false, fmt.Errorf("api key concurrency gate is unavailable")
	}
	now := time.Now()
	nowMillis := now.UnixMilli()
	active, err := apiKeyConcurrencyAcquireScript.Run(ctx, g.redis, []string{apiKeyConcurrencyKey(keyID)}, nowMillis, limit, leaseID, nowMillis+apiKeyConcurrencyLeaseTTL.Milliseconds(), apiKeyConcurrencyLeaseTTL.Milliseconds()).Int()
	if err != nil {
		return 0, false, fmt.Errorf("acquire api key concurrency lease: %w", err)
	}
	if active == 0 {
		return 0, false, nil
	}
	if err := apiKeyUserUsageBeginScript.Run(ctx, g.redis, apiKeyUserUsageKeys(userID), nowMillis, leaseID, nowMillis+apiKeyConcurrencyLeaseTTL.Milliseconds(), apiKeyConcurrencyLeaseTTL.Milliseconds(), now.Unix(), apiKeyRPMHashTTL.Milliseconds()).Err(); err != nil {
		_ = g.Release(ctx, userID, keyID, leaseID)
		return 0, false, fmt.Errorf("record api key realtime usage: %w", err)
	}
	return active, true, nil
}

func (g *APIKeyConcurrencyGate) Release(ctx context.Context, userID, keyID uint, leaseID string) error {
	if g == nil || g.redis == nil || keyID == 0 || leaseID == "" {
		return nil
	}
	if err := apiKeyConcurrencyReleaseScript.Run(ctx, g.redis, []string{apiKeyConcurrencyKey(keyID)}, leaseID).Err(); err != nil {
		return fmt.Errorf("release api key concurrency lease: %w", err)
	}
	if userID != 0 {
		if err := g.redis.ZRem(ctx, apiKeyUserActiveKey(userID), leaseID).Err(); err != nil {
			return fmt.Errorf("release user concurrency lease: %w", err)
		}
	}
	return nil
}

func (g *APIKeyConcurrencyGate) RealtimeUsage(ctx context.Context, userID uint) (int64, int64, error) {
	if g == nil || g.redis == nil || userID == 0 {
		return 0, 0, fmt.Errorf("api key concurrency gate is unavailable")
	}
	now := time.Now()
	counts, err := apiKeyUserUsageReadScript.Run(ctx, g.redis, apiKeyUserUsageKeys(userID), now.UnixMilli(), now.Unix()).Int64Slice()
	if err != nil {
		return 0, 0, fmt.Errorf("read api key realtime usage: %w", err)
	}
	if len(counts) != 2 {
		return 0, 0, fmt.Errorf("read api key realtime usage: invalid result")
	}
	return counts[0], counts[1], nil
}

func apiKeyConcurrencyKey(keyID uint) string {
	return fmt.Sprintf("%s%d", apiKeyConcurrencyKeyPrefix, keyID)
}

func apiKeyUserUsageKeys(userID uint) []string {
	return []string{apiKeyUserActiveKey(userID), apiKeyUserRPMKey(userID)}
}

func apiKeyUserActiveKey(userID uint) string {
	return fmt.Sprintf("%s{%d}:active", apiKeyUserUsageKeyPrefix, userID)
}

func apiKeyUserRPMKey(userID uint) string {
	return fmt.Sprintf("%s{%d}:rpm", apiKeyUserUsageKeyPrefix, userID)
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

var apiKeyUserUsageBeginScript = redis.NewScript(`
redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', ARGV[1])
redis.call('ZADD', KEYS[1], ARGV[3], ARGV[2])
redis.call('PEXPIRE', KEYS[1], ARGV[4])
local second = tonumber(ARGV[5])
local slot = tostring(second % 60)
local timestampField = 't:' .. slot
local countField = 'c:' .. slot
if tonumber(redis.call('HGET', KEYS[2], timestampField)) == second then
  redis.call('HINCRBY', KEYS[2], countField, 1)
else
  redis.call('HSET', KEYS[2], timestampField, second, countField, 1)
end
redis.call('PEXPIRE', KEYS[2], ARGV[6])
return 1
`)

var apiKeyUserUsageReadScript = redis.NewScript(`
redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', ARGV[1])
local now = tonumber(ARGV[2])
local cutoff = now - 60
local rpm = 0
for slot = 0, 59 do
  local timestamp = tonumber(redis.call('HGET', KEYS[2], 't:' .. slot))
  if timestamp and timestamp > cutoff and timestamp <= now then
    rpm = rpm + (tonumber(redis.call('HGET', KEYS[2], 'c:' .. slot)) or 0)
  end
end
return {redis.call('ZCARD', KEYS[1]), rpm}
`)
