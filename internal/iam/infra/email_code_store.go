package infra

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	emailCodeKeyPrefix         = "email_code:"
	emailCodeCooldownKeyPrefix = "email_code_cooldown:"
)

// EmailCodeStore stores email verification codes in Redis.
type EmailCodeStore struct {
	rdb redis.UniversalClient
}

// NewEmailCodeStore creates a Redis-backed email code store.
func NewEmailCodeStore(rdb redis.UniversalClient) *EmailCodeStore {
	return &EmailCodeStore{rdb: rdb}
}

func emailCodeRedisKey(key string) string {
	return emailCodeKeyPrefix + key
}

func emailCodeCooldownRedisKey(key string) string {
	return emailCodeCooldownKeyPrefix + key
}

// StartCooldown sets a cooldown marker for the key when absent, with the given
// TTL. When a cooldown is already active it returns started=false and the
// remaining seconds.
func (s *EmailCodeStore) StartCooldown(ctx context.Context, key string, seconds int) (bool, int, error) {
	redisKey := emailCodeCooldownRedisKey(key)
	started, err := s.rdb.SetNX(ctx, redisKey, "1", time.Duration(seconds)*time.Second).Result()
	if err != nil {
		return false, 0, fmt.Errorf("redis email code cooldown setnx: %w", err)
	}
	if started {
		return true, 0, nil
	}

	ttl, err := s.rdb.TTL(ctx, redisKey).Result()
	if err != nil {
		return false, 0, fmt.Errorf("redis email code cooldown ttl: %w", err)
	}
	retryAfter := int(ttl / time.Second)
	if retryAfter < 1 {
		// The key may have expired between SetNX and TTL, or carry no expiry.
		retryAfter = 1
	}
	return false, retryAfter, nil
}

// ClearCooldown removes the cooldown marker for the key.
func (s *EmailCodeStore) ClearCooldown(ctx context.Context, key string) error {
	return s.rdb.Del(ctx, emailCodeCooldownRedisKey(key)).Err()
}

func (s *EmailCodeStore) CreateIfAbsent(ctx context.Context, key, code string, ttlSeconds int) (string, bool, error) {
	redisKey := emailCodeRedisKey(key)
	created, err := s.rdb.SetNX(ctx, redisKey, code, time.Duration(ttlSeconds)*time.Second).Result()
	if err != nil {
		return "", false, fmt.Errorf("redis email code setnx: %w", err)
	}
	if created {
		return code, false, nil
	}

	existing, err := s.Get(ctx, key)
	if err != nil {
		return "", false, err
	}
	if existing == "" {
		return "", false, fmt.Errorf("email code disappeared during idempotent send")
	}
	return existing, true, nil
}

func (s *EmailCodeStore) Get(ctx context.Context, key string) (string, error) {
	val, err := s.rdb.Get(ctx, emailCodeRedisKey(key)).Result()
	if err != nil {
		if err == redis.Nil {
			return "", nil
		}
		return "", fmt.Errorf("redis email code get: %w", err)
	}
	if strings.HasPrefix(val, "claimed:") {
		if separator := strings.LastIndexByte(val, ':'); separator > len("claimed:") {
			return val[separator+1:], nil
		}
	}
	return val, nil
}

func (s *EmailCodeStore) Claim(ctx context.Context, key, expected, claimToken string) (bool, error) {
	claimed, err := emailCodeClaimScript.Run(ctx, s.rdb, []string{emailCodeRedisKey(key)}, expected, claimToken).Int64()
	if err != nil {
		return false, fmt.Errorf("redis email code claim: %w", err)
	}
	return claimed == 1, nil
}

func (s *EmailCodeStore) Commit(ctx context.Context, key, claimToken string) (bool, error) {
	committed, err := emailCodeCommitScript.Run(ctx, s.rdb, []string{emailCodeRedisKey(key)}, claimToken).Int64()
	if err != nil {
		return false, fmt.Errorf("redis email code commit: %w", err)
	}
	return committed == 1, nil
}

func (s *EmailCodeStore) Restore(ctx context.Context, key, claimToken, code string) (bool, error) {
	restored, err := emailCodeRestoreScript.Run(ctx, s.rdb, []string{emailCodeRedisKey(key)}, claimToken, code).Int64()
	if err != nil {
		return false, fmt.Errorf("redis email code restore: %w", err)
	}
	return restored == 1, nil
}

func (s *EmailCodeStore) Delete(ctx context.Context, key string) error {
	return s.rdb.Del(ctx, emailCodeRedisKey(key)).Err()
}

var emailCodeClaimScript = redis.NewScript(`
if redis.call('GET', KEYS[1]) ~= ARGV[1] then
    return 0
end
redis.call('SET', KEYS[1], 'claimed:' .. ARGV[2] .. ':' .. ARGV[1], 'KEEPTTL')
return 1
`)

var emailCodeCommitScript = redis.NewScript(`
local value = redis.call('GET', KEYS[1])
local prefix = 'claimed:' .. ARGV[1] .. ':'
if not value or string.sub(value, 1, string.len(prefix)) ~= prefix then
    return 0
end
return redis.call('DEL', KEYS[1])
`)

var emailCodeRestoreScript = redis.NewScript(`
if redis.call('GET', KEYS[1]) ~= 'claimed:' .. ARGV[1] .. ':' .. ARGV[2] then
    return 0
end
redis.call('SET', KEYS[1], ARGV[2], 'KEEPTTL')
return 1
`)
