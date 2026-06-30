package infra

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	emailCodeKeyPrefix = "email_code:"
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
	return val, nil
}

func (s *EmailCodeStore) Delete(ctx context.Context, key string) error {
	return s.rdb.Del(ctx, emailCodeRedisKey(key)).Err()
}
