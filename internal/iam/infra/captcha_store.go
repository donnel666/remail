package infra

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	captchaKeyPrefix = "captcha:"
)

// CaptchaStore implements app.CaptchaStore using Redis.
type CaptchaStore struct {
	rdb redis.UniversalClient
}

// NewCaptchaStore creates a new Redis-backed captcha store.
func NewCaptchaStore(rdb redis.UniversalClient) *CaptchaStore {
	return &CaptchaStore{rdb: rdb}
}

func captchaKey(id string) string {
	return captchaKeyPrefix + id
}

func (s *CaptchaStore) Create(ctx context.Context, captchaID, answer string, ttlSeconds int) error {
	err := s.rdb.Set(ctx, captchaKey(captchaID), answer, time.Duration(ttlSeconds)*time.Second).Err()
	if err != nil {
		return fmt.Errorf("redis captcha create: %w", err)
	}
	return nil
}

func (s *CaptchaStore) Get(ctx context.Context, captchaID string) (string, error) {
	val, err := s.rdb.Get(ctx, captchaKey(captchaID)).Result()
	if err != nil {
		if err == redis.Nil {
			return "", nil
		}
		return "", fmt.Errorf("redis captcha get: %w", err)
	}
	return val, nil
}

func (s *CaptchaStore) GetDel(ctx context.Context, captchaID string) (string, error) {
	val, err := s.rdb.GetDel(ctx, captchaKey(captchaID)).Result()
	if err != nil {
		if err == redis.Nil {
			return "", nil
		}
		return "", fmt.Errorf("redis captcha getdel: %w", err)
	}
	return val, nil
}

func (s *CaptchaStore) Delete(ctx context.Context, captchaID string) error {
	return s.rdb.Del(ctx, captchaKey(captchaID)).Err()
}
