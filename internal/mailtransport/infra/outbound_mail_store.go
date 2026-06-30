package infra

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/donnel666/remail/internal/mailtransport/domain"
	"github.com/redis/go-redis/v9"
)

const outboundMailKeyPrefix = "outbound_mail:"

type OutboundMailStore struct {
	rdb redis.UniversalClient
}

func NewOutboundMailStore(rdb redis.UniversalClient) *OutboundMailStore {
	return &OutboundMailStore{rdb: rdb}
}

func (s *OutboundMailStore) Reserve(ctx context.Context, mail *domain.OutboundMail, ttl time.Duration) (*domain.OutboundMail, bool, error) {
	payload, err := json.Marshal(mail)
	if err != nil {
		return nil, false, fmt.Errorf("marshal outbound mail: %w", err)
	}

	key := outboundMailRedisKey(mail.IdempotencyKey)
	created, err := s.rdb.SetNX(ctx, key, payload, ttl).Result()
	if err != nil {
		return nil, false, fmt.Errorf("redis outbound mail reserve: %w", err)
	}
	if created {
		return mail, true, nil
	}

	existing, err := s.get(ctx, key)
	if err != nil {
		return nil, false, err
	}
	return existing, false, nil
}

func (s *OutboundMailStore) Update(ctx context.Context, mail *domain.OutboundMail, ttl time.Duration) error {
	payload, err := json.Marshal(mail)
	if err != nil {
		return fmt.Errorf("marshal outbound mail: %w", err)
	}
	if err := s.rdb.Set(ctx, outboundMailRedisKey(mail.IdempotencyKey), payload, ttl).Err(); err != nil {
		return fmt.Errorf("redis outbound mail update: %w", err)
	}
	return nil
}

func (s *OutboundMailStore) get(ctx context.Context, key string) (*domain.OutboundMail, error) {
	payload, err := s.rdb.Get(ctx, key).Bytes()
	if err != nil {
		return nil, fmt.Errorf("redis outbound mail get: %w", err)
	}
	var mail domain.OutboundMail
	if err := json.Unmarshal(payload, &mail); err != nil {
		return nil, fmt.Errorf("unmarshal outbound mail: %w", err)
	}
	return &mail, nil
}

func outboundMailRedisKey(idempotencyKey string) string {
	return outboundMailKeyPrefix + idempotencyKey
}
