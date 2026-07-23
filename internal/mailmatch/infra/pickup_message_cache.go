package infra

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	mailmatchapp "github.com/donnel666/remail/internal/mailmatch/app"
	"github.com/redis/go-redis/v9"
)

const pickupMessageCacheKeyPrefix = "remail:mailmatch:pickup-messages:"

type PickupMessageCache struct {
	redis redis.UniversalClient
}

func NewPickupMessageCache(client redis.UniversalClient) *PickupMessageCache {
	return &PickupMessageCache{redis: client}
}

func (c *PickupMessageCache) Load(ctx context.Context, emailResourceID uint) ([]mailmatchapp.FetchedMessage, bool, error) {
	if c == nil || c.redis == nil || emailResourceID == 0 {
		return nil, false, nil
	}
	payload, err := c.redis.Get(ctx, pickupMessageCacheKey(emailResourceID)).Bytes()
	if err == redis.Nil {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("load pickup message cache: %w", err)
	}
	var messages []mailmatchapp.FetchedMessage
	if err := json.Unmarshal(payload, &messages); err != nil {
		_ = c.redis.Del(context.WithoutCancel(ctx), pickupMessageCacheKey(emailResourceID)).Err()
		return nil, false, fmt.Errorf("decode pickup message cache: %w", err)
	}
	return messages, true, nil
}

func (c *PickupMessageCache) LoadMany(ctx context.Context, emailResourceIDs []uint) (map[uint][]mailmatchapp.FetchedMessage, error) {
	result := make(map[uint][]mailmatchapp.FetchedMessage, len(emailResourceIDs))
	if c == nil || c.redis == nil || len(emailResourceIDs) == 0 {
		return result, nil
	}
	ids := make([]uint, 0, len(emailResourceIDs))
	keys := make([]string, 0, len(emailResourceIDs))
	seen := make(map[uint]struct{}, len(emailResourceIDs))
	for _, resourceID := range emailResourceIDs {
		if resourceID == 0 {
			continue
		}
		if _, exists := seen[resourceID]; exists {
			continue
		}
		seen[resourceID] = struct{}{}
		ids = append(ids, resourceID)
		keys = append(keys, pickupMessageCacheKey(resourceID))
	}
	if len(keys) == 0 {
		return result, nil
	}
	values, err := c.redis.MGet(ctx, keys...).Result()
	if err != nil {
		return nil, fmt.Errorf("load pickup message caches: %w", err)
	}
	for index, value := range values {
		if value == nil {
			continue
		}
		payload, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("decode pickup message cache %d: invalid Redis value", ids[index])
		}
		var messages []mailmatchapp.FetchedMessage
		if err := json.Unmarshal([]byte(payload), &messages); err != nil {
			_ = c.redis.Del(context.WithoutCancel(ctx), keys[index]).Err()
			return nil, fmt.Errorf("decode pickup message cache %d: %w", ids[index], err)
		}
		result[ids[index]] = messages
	}
	return result, nil
}

func (c *PickupMessageCache) Store(ctx context.Context, emailResourceID uint, messages []mailmatchapp.FetchedMessage, ttl time.Duration) error {
	if c == nil || c.redis == nil || emailResourceID == 0 || ttl <= 0 {
		return nil
	}
	payload, err := json.Marshal(messages)
	if err != nil {
		return fmt.Errorf("encode pickup message cache: %w", err)
	}
	if err := c.redis.Set(ctx, pickupMessageCacheKey(emailResourceID), payload, ttl).Err(); err != nil {
		return fmt.Errorf("store pickup message cache: %w", err)
	}
	return nil
}

func pickupMessageCacheKey(emailResourceID uint) string {
	return fmt.Sprintf("%s%d", pickupMessageCacheKeyPrefix, emailResourceID)
}
