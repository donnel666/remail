package infra

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

const pickupFetchStateKeyPrefix = "remail:mailmatch:pickup-fetch:"

type PickupFetchState struct {
	redis redis.UniversalClient
}

func NewPickupFetchState(client redis.UniversalClient) *PickupFetchState {
	return &PickupFetchState{redis: client}
}

func (s *PickupFetchState) Acquire(ctx context.Context, emailResourceID uint, token string, ttl time.Duration) (bool, error) {
	if s == nil || s.redis == nil || emailResourceID == 0 || token == "" || ttl <= 0 {
		return false, fmt.Errorf("pickup fetch state is unavailable")
	}
	acquired, err := s.redis.SetNX(ctx, pickupFetchStateKey(emailResourceID), token, ttl).Result()
	if err != nil {
		return false, fmt.Errorf("acquire pickup fetch state: %w", err)
	}
	return acquired, nil
}

func (s *PickupFetchState) Owns(ctx context.Context, emailResourceID uint, token string) (bool, error) {
	if s == nil || s.redis == nil || emailResourceID == 0 || token == "" {
		return false, nil
	}
	value, err := s.redis.Get(ctx, pickupFetchStateKey(emailResourceID)).Result()
	if err == redis.Nil {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read pickup fetch state: %w", err)
	}
	return value == token, nil
}

func (s *PickupFetchState) Extend(ctx context.Context, emailResourceID uint, token string, ttl time.Duration) (bool, error) {
	if s == nil || s.redis == nil || emailResourceID == 0 || token == "" || ttl <= 0 {
		return false, nil
	}
	updated, err := pickupFetchExpireScript.Run(
		ctx, s.redis, []string{pickupFetchStateKey(emailResourceID)}, token, ttl.Milliseconds(),
	).Int64()
	if err != nil {
		return false, fmt.Errorf("extend pickup fetch state: %w", err)
	}
	return updated == 1, nil
}

func (s *PickupFetchState) Release(ctx context.Context, emailResourceID uint, token string) error {
	if s == nil || s.redis == nil || emailResourceID == 0 || token == "" {
		return nil
	}
	if err := pickupFetchReleaseScript.Run(ctx, s.redis, []string{pickupFetchStateKey(emailResourceID)}, token).Err(); err != nil {
		return fmt.Errorf("release pickup fetch state: %w", err)
	}
	return nil
}

func pickupFetchStateKey(emailResourceID uint) string {
	return fmt.Sprintf("%s%d", pickupFetchStateKeyPrefix, emailResourceID)
}

var pickupFetchExpireScript = redis.NewScript(`
if redis.call("GET", KEYS[1]) ~= ARGV[1] then
  return 0
end
return redis.call("PEXPIRE", KEYS[1], ARGV[2])
`)

var pickupFetchReleaseScript = redis.NewScript(`
if redis.call("GET", KEYS[1]) ~= ARGV[1] then
  return 0
end
return redis.call("DEL", KEYS[1])
`)
