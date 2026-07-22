package api

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	dashboardapp "github.com/donnel666/remail/internal/dashboard/app"
	"github.com/redis/go-redis/v9"
)

const (
	adminDashboardCachePrefix = "dashboard:admin:v1:"
	adminDashboardActiveKey   = adminDashboardCachePrefix + "active"
	adminDashboardCacheTTL    = 24 * time.Hour
)

type adminDashboardCache struct {
	redis redis.UniversalClient
}

type adminDashboardCacheEntry struct {
	From *time.Time                   `json:"from,omitempty"`
	To   *time.Time                   `json:"to,omitempty"`
	Data *dashboardapp.AdminDashboard `json:"data"`
}

type adminDashboardLoader func(context.Context, *time.Time, *time.Time) (*dashboardapp.AdminDashboard, error)

func newAdminDashboardCache(client redis.UniversalClient) *adminDashboardCache {
	return &adminDashboardCache{redis: client}
}

func (m *Module) loadAdminDashboard(ctx context.Context, from, to *time.Time) (*dashboardapp.AdminDashboard, error) {
	if m.adminCache != nil {
		cached, ok, err := m.adminCache.get(ctx, from, to)
		if err != nil {
			slog.WarnContext(ctx, "read admin dashboard cache failed", "error", err)
		}
		if ok {
			return cached, nil
		}
	}
	result, err := m.AdminQuery.AdminDashboard(ctx, from, to)
	if err != nil {
		return nil, err
	}
	if m.adminCache != nil {
		if err := m.adminCache.set(ctx, from, to, result); err != nil {
			slog.WarnContext(ctx, "store admin dashboard cache failed", "error", err)
		}
	}
	return result, nil
}

func (c *adminDashboardCache) get(ctx context.Context, from, to *time.Time) (*dashboardapp.AdminDashboard, bool, error) {
	if c == nil || c.redis == nil {
		return nil, false, nil
	}
	key := adminDashboardCacheKey(from, to)
	payload, err := c.redis.Get(ctx, key).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("get %s: %w", key, err)
	}
	var entry adminDashboardCacheEntry
	if err := json.Unmarshal(payload, &entry); err != nil {
		return nil, false, fmt.Errorf("decode %s: %w", key, err)
	}
	if entry.Data == nil {
		return nil, false, fmt.Errorf("decode %s: empty dashboard", key)
	}
	err = c.redis.ZAdd(ctx, adminDashboardActiveKey, redis.Z{
		Score:  float64(time.Now().UnixMilli()),
		Member: key,
	}).Err()
	return entry.Data, true, err
}

func (c *adminDashboardCache) set(ctx context.Context, from, to *time.Time, data *dashboardapp.AdminDashboard) error {
	if c == nil || c.redis == nil {
		return nil
	}
	entry := adminDashboardCacheEntry{From: from, To: to, Data: data}
	payload, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("encode admin dashboard cache: %w", err)
	}
	key := adminDashboardCacheKey(from, to)
	_, err = c.redis.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
		pipe.Set(ctx, key, payload, adminDashboardCacheTTL)
		pipe.ZAdd(ctx, adminDashboardActiveKey, redis.Z{Score: float64(time.Now().UnixMilli()), Member: key})
		return nil
	})
	return err
}

func (c *adminDashboardCache) refresh(ctx context.Context, load adminDashboardLoader) error {
	if c == nil || c.redis == nil || load == nil {
		return nil
	}
	cutoff := fmt.Sprintf("%d", time.Now().Add(-adminDashboardCacheTTL).UnixMilli())
	if err := c.redis.ZRemRangeByScore(ctx, adminDashboardActiveKey, "-inf", "("+cutoff).Err(); err != nil {
		return err
	}
	keys, err := c.redis.ZRange(ctx, adminDashboardActiveKey, 0, -1).Result()
	if err != nil {
		return err
	}
	var refreshErrors []error
	for _, key := range keys {
		payload, err := c.redis.Get(ctx, key).Bytes()
		if errors.Is(err, redis.Nil) {
			c.redis.ZRem(ctx, adminDashboardActiveKey, key)
			continue
		}
		var entry adminDashboardCacheEntry
		if err == nil {
			err = json.Unmarshal(payload, &entry)
		}
		if err == nil {
			entry.Data, err = load(ctx, entry.From, entry.To)
		}
		if err == nil {
			payload, err = json.Marshal(entry)
		}
		if err == nil {
			err = c.redis.Set(ctx, key, payload, adminDashboardCacheTTL).Err()
		}
		if err != nil {
			refreshErrors = append(refreshErrors, fmt.Errorf("%s: %w", key, err))
		}
	}
	return errors.Join(refreshErrors...)
}

func adminDashboardCacheKey(from, to *time.Time) string {
	var fromValue, toValue string
	if from != nil {
		fromValue = from.UTC().Format(time.RFC3339Nano)
	}
	if to != nil {
		toValue = to.UTC().Format(time.RFC3339Nano)
	}
	return fmt.Sprintf("%s%x", adminDashboardCachePrefix, sha256.Sum256([]byte(fromValue+"\n"+toValue)))
}
