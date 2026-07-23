package infra

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	allocapp "github.com/donnel666/remail/internal/alloc/app"
	"github.com/donnel666/remail/internal/platform"
	"github.com/redis/go-redis/v9"
)

const (
	inventoryCacheKeyPrefix = "alloc:inventory:v2:"
	inventoryCacheActiveKey = "alloc:inventory:active"
)

type InventoryCache struct {
	redis redis.UniversalClient
}

func NewInventoryCache(client redis.UniversalClient) *InventoryCache {
	return &InventoryCache{redis: client}
}

func (c *InventoryCache) GetInventoryStats(ctx context.Context, projectID uint, buyerUserID uint) (*allocapp.InventoryStats, error) {
	return loadInventoryCache[allocapp.InventoryStats](ctx, c.redis, inventoryCacheKey(allocapp.InventoryCacheStats, projectID, buyerUserID))
}

func (c *InventoryCache) SetInventoryStats(ctx context.Context, projectID uint, buyerUserID uint, stats *allocapp.InventoryStats, ttl time.Duration) error {
	return storeInventoryCache(ctx, c.redis, inventoryCacheKey(allocapp.InventoryCacheStats, projectID, buyerUserID), stats, ttl)
}

func (c *InventoryCache) RefreshInventoryStats(ctx context.Context, projectID uint, buyerUserID uint, stats *allocapp.InventoryStats, ttl time.Duration) error {
	return refreshInventoryCache(ctx, c.redis, inventoryCacheKey(allocapp.InventoryCacheStats, projectID, buyerUserID), stats, ttl)
}

func (c *InventoryCache) GetProductInventoryTotals(ctx context.Context, projectID uint, buyerUserID uint) (*allocapp.ProjectProductInventoryTotals, error) {
	totals, err := loadInventoryCache[allocapp.ProjectProductInventoryTotals](ctx, c.redis, inventoryCacheKey(allocapp.InventoryCacheProducts, projectID, buyerUserID))
	if err != nil || totals == nil {
		return totals, err
	}
	if err := c.applyProductUnavailableMarkers(ctx, totals, buyerUserID); err != nil {
		return nil, err
	}
	return totals, nil
}

func (c *InventoryCache) applyProductUnavailableMarkers(ctx context.Context, totals *allocapp.ProjectProductInventoryTotals, buyerUserID uint) error {
	if totals == nil {
		return nil
	}
	requests := productUnavailableMarkerRequests(*totals, buyerUserID)
	if len(requests) == 0 {
		return nil
	}
	keys := make([]string, len(requests))
	for i := range requests {
		keys[i] = productUnavailableMarkerKey(requests[i])
	}
	markers, err := c.redis.MGet(ctx, keys...).Result()
	if err != nil {
		return fmt.Errorf("load product inventory corrections: %w", err)
	}
	for i, marker := range markers {
		if marker != nil {
			markProductUnavailable(totals, requests[i])
		}
	}
	return nil
}

func (c *InventoryCache) SetProductInventoryTotals(ctx context.Context, projectID uint, buyerUserID uint, totals *allocapp.ProjectProductInventoryTotals, ttl time.Duration) error {
	return storeInventoryCache(ctx, c.redis, inventoryCacheKey(allocapp.InventoryCacheProducts, projectID, buyerUserID), totals, ttl)
}

func (c *InventoryCache) RefreshProductInventoryTotals(ctx context.Context, projectID uint, buyerUserID uint, totals *allocapp.ProjectProductInventoryTotals, ttl time.Duration) error {
	return refreshInventoryCache(ctx, c.redis, inventoryCacheKey(allocapp.InventoryCacheProducts, projectID, buyerUserID), totals, ttl)
}

func (c *InventoryCache) IsProductUnavailable(ctx context.Context, req allocapp.ProductInventoryAvailabilityRequest) (bool, error) {
	keys := []string{productUnavailableMarkerKey(req)}
	if req.PublicOnly {
		total := req
		total.PublicOnly = false
		keys = append(keys, productUnavailableMarkerKey(total))
	}
	values, err := c.redis.MGet(ctx, keys...).Result()
	if err != nil {
		return false, fmt.Errorf("load product inventory correction: %w", err)
	}
	for _, value := range values {
		if value != nil {
			return true, nil
		}
	}
	return false, nil
}

// MarkProductUnavailable immediately corrects the cached read model after the
// authoritative allocator proves that a scope has no candidate. WATCH prevents
// this correction from overwriting a concurrent background refresh, and
// KEEPTTL preserves the 24-hour hard expiry.
func (c *InventoryCache) MarkProductUnavailable(ctx context.Context, req allocapp.ProductInventoryAvailabilityRequest) (bool, error) {
	key := inventoryCacheKey(allocapp.InventoryCacheProducts, req.ProjectID, req.BuyerUserID)
	markerKey := productUnavailableMarkerKey(req)
	for attempt := 0; attempt < 3; attempt++ {
		marked := false
		err := c.redis.Watch(ctx, func(tx *redis.Tx) error {
			payload, err := tx.Get(ctx, key).Bytes()
			if err == redis.Nil {
				return nil
			}
			if err != nil {
				return err
			}
			var totals allocapp.ProjectProductInventoryTotals
			if err := json.Unmarshal(payload, &totals); err != nil {
				return fmt.Errorf("decode %s: %w", key, err)
			}
			if !productInventoryTargetExists(totals, req) {
				return nil
			}
			marked = true
			changed := markProductUnavailable(&totals, req)
			updated, err := json.Marshal(&totals)
			if err != nil {
				return fmt.Errorf("encode %s: %w", key, err)
			}
			_, err = tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
				if changed {
					pipe.SetArgs(ctx, key, updated, redis.SetArgs{KeepTTL: true})
				}
				pipe.Set(ctx, markerKey, "1", allocapp.InventoryRefreshInterval)
				pipe.ZAdd(ctx, inventoryCacheActiveKey, redis.Z{Score: float64(time.Now().UnixMilli()), Member: key})
				return nil
			})
			return err
		}, key)
		if err == redis.TxFailedErr {
			continue
		}
		if err != nil {
			return false, fmt.Errorf("mark product inventory unavailable: %w", err)
		}
		return marked, nil
	}
	return false, fmt.Errorf("mark product inventory unavailable: concurrent cache refresh")
}

func productInventoryTargetExists(totals allocapp.ProjectProductInventoryTotals, req allocapp.ProductInventoryAvailabilityRequest) bool {
	suffix := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(req.EmailSuffix), "@"))
	for _, item := range totals.Items {
		if item.ProductID != req.ProductID {
			continue
		}
		if suffix == "" {
			return true
		}
		for _, entry := range item.Suffixes {
			entrySuffix := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(entry.Suffix), "@"))
			if entrySuffix == suffix {
				return true
			}
		}
		return false
	}
	return false
}

func productUnavailableMarkerRequests(totals allocapp.ProjectProductInventoryTotals, buyerUserID uint) []allocapp.ProductInventoryAvailabilityRequest {
	requests := make([]allocapp.ProductInventoryAvailabilityRequest, 0, len(totals.Items)*2)
	for _, item := range totals.Items {
		for _, publicOnly := range []bool{false, true} {
			requests = append(requests, allocapp.ProductInventoryAvailabilityRequest{
				ProjectID: totals.ProjectID, ProductID: item.ProductID, BuyerUserID: buyerUserID, PublicOnly: publicOnly,
			})
			for _, suffix := range item.Suffixes {
				requests = append(requests, allocapp.ProductInventoryAvailabilityRequest{
					ProjectID: totals.ProjectID, ProductID: item.ProductID, BuyerUserID: buyerUserID,
					EmailSuffix: suffix.Suffix, PublicOnly: publicOnly,
				})
			}
		}
	}
	return requests
}

func productUnavailableMarkerKey(req allocapp.ProductInventoryAvailabilityRequest) string {
	scope := "total"
	if req.PublicOnly {
		scope = "public"
	}
	suffix := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(req.EmailSuffix), "@"))
	if suffix == "" {
		suffix = "-"
	}
	return inventoryCacheKeyPrefix + "unavailable:" +
		strconv.FormatUint(uint64(req.ProjectID), 10) + ":" +
		strconv.FormatUint(uint64(req.BuyerUserID), 10) + ":" +
		strconv.FormatUint(uint64(req.ProductID), 10) + ":" + scope + ":" + suffix
}

func markProductUnavailable(totals *allocapp.ProjectProductInventoryTotals, req allocapp.ProductInventoryAvailabilityRequest) bool {
	if totals == nil {
		return false
	}
	suffix := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(req.EmailSuffix), "@"))
	changed := false
	for i := range totals.Items {
		item := &totals.Items[i]
		if item.ProductID != req.ProductID {
			continue
		}
		if suffix == "" {
			if req.PublicOnly {
				changed = item.PublicAvailable != 0
				item.PublicAvailable = 0
				for j := range item.Suffixes {
					item.Suffixes[j].PublicAvailable = 0
				}
			} else {
				changed = item.TotalAvailable != 0 || item.PublicAvailable != 0
				item.TotalAvailable = 0
				item.PublicAvailable = 0
				for j := range item.Suffixes {
					item.Suffixes[j].TotalAvailable = 0
					item.Suffixes[j].PublicAvailable = 0
				}
			}
		} else {
			found := false
			for j := range item.Suffixes {
				entry := &item.Suffixes[j]
				entrySuffix := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(entry.Suffix), "@"))
				if entrySuffix != suffix {
					continue
				}
				found = true
				if req.PublicOnly {
					changed = changed || entry.PublicAvailable != 0
					entry.PublicAvailable = 0
				} else {
					changed = changed || entry.TotalAvailable != 0 || entry.PublicAvailable != 0
					entry.TotalAvailable = 0
					entry.PublicAvailable = 0
				}
			}
			if !found {
				return false
			}
			item.TotalAvailable = 0
			item.PublicAvailable = 0
			for _, entry := range item.Suffixes {
				item.TotalAvailable += entry.TotalAvailable
				item.PublicAvailable += entry.PublicAvailable
			}
		}
		break
	}
	if !changed {
		return false
	}
	totals.TotalAvailable = 0
	for _, item := range totals.Items {
		totals.TotalAvailable += item.TotalAvailable
	}
	return true
}

func (c *InventoryCache) ClaimActiveInventory(ctx context.Context, since time.Time, limit int) ([]allocapp.InventoryCacheEntry, error) {
	if limit <= 0 {
		return nil, nil
	}
	minimum := strconv.FormatInt(since.UnixMilli(), 10)
	if err := c.redis.ZRemRangeByScore(ctx, inventoryCacheActiveKey, "-inf", "("+minimum).Err(); err != nil {
		return nil, fmt.Errorf("remove inactive inventory cache keys: %w", err)
	}
	items, err := c.redis.ZPopMin(ctx, inventoryCacheActiveKey, int64(limit)).Result()
	if err != nil {
		return nil, fmt.Errorf("claim active inventory cache keys: %w", err)
	}
	entries := make([]allocapp.InventoryCacheEntry, 0, len(items))
	for _, item := range items {
		entry, ok := parseInventoryCacheKey(fmt.Sprint(item.Member))
		if !ok {
			continue
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

func (c *InventoryCache) RequeueInventory(ctx context.Context, entries []allocapp.InventoryCacheEntry) error {
	if len(entries) == 0 {
		return nil
	}
	now := float64(time.Now().UnixMilli())
	_, err := c.redis.Pipelined(ctx, func(pipe redis.Pipeliner) error {
		for _, entry := range entries {
			pipe.ZAdd(ctx, inventoryCacheActiveKey, redis.Z{Score: now, Member: inventoryCacheKey(entry.Kind, entry.ProjectID, entry.BuyerUserID)})
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("requeue inventory cache: %w", err)
	}
	return nil
}

func (c *InventoryCache) DeleteInventory(ctx context.Context, entry allocapp.InventoryCacheEntry) error {
	key := inventoryCacheKey(entry.Kind, entry.ProjectID, entry.BuyerUserID)
	_, err := c.redis.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
		pipe.Del(ctx, key)
		pipe.ZRem(ctx, inventoryCacheActiveKey, key)
		return nil
	})
	if err != nil {
		return fmt.Errorf("delete inventory cache: %w", err)
	}
	return nil
}

func (c *InventoryCache) AcquireInventoryRefresh(ctx context.Context, entry allocapp.InventoryCacheEntry, ttl time.Duration) (string, bool, error) {
	token := platform.NewUUIDV7String()
	acquired, err := c.redis.SetNX(ctx, inventoryCacheLockKey(entry), token, ttl).Result()
	if err != nil {
		return "", false, fmt.Errorf("acquire inventory cache lock: %w", err)
	}
	if !acquired {
		return "", false, nil
	}
	return token, true, nil
}

func (c *InventoryCache) ReleaseInventoryRefresh(ctx context.Context, entry allocapp.InventoryCacheEntry, token string) error {
	if strings.TrimSpace(token) == "" {
		return nil
	}
	if err := inventoryCacheLockReleaseScript.Run(ctx, c.redis, []string{inventoryCacheLockKey(entry)}, token).Err(); err != nil {
		return fmt.Errorf("release inventory cache lock: %w", err)
	}
	return nil
}

func loadInventoryCache[T any](ctx context.Context, client redis.UniversalClient, key string) (*T, error) {
	payload, err := client.Get(ctx, key).Bytes()
	if err != nil {
		if err == redis.Nil {
			return nil, nil
		}
		return nil, fmt.Errorf("get %s: %w", key, err)
	}
	var value T
	if err := json.Unmarshal(payload, &value); err != nil {
		return nil, fmt.Errorf("decode %s: %w", key, err)
	}
	if err := client.ZAdd(ctx, inventoryCacheActiveKey, redis.Z{Score: float64(time.Now().UnixMilli()), Member: key}).Err(); err != nil {
		return nil, fmt.Errorf("touch %s: %w", key, err)
	}
	return &value, nil
}

func storeInventoryCache(ctx context.Context, client redis.UniversalClient, key string, value any, ttl time.Duration) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode %s: %w", key, err)
	}
	_, err = client.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
		pipe.Set(ctx, key, payload, ttl)
		pipe.ZAdd(ctx, inventoryCacheActiveKey, redis.Z{Score: float64(time.Now().UnixMilli()), Member: key})
		return nil
	})
	if err != nil {
		return fmt.Errorf("store %s: %w", key, err)
	}
	return nil
}

func refreshInventoryCache(ctx context.Context, client redis.UniversalClient, key string, value any, ttl time.Duration) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode %s: %w", key, err)
	}
	if err := client.Set(ctx, key, payload, ttl).Err(); err != nil {
		return fmt.Errorf("refresh %s: %w", key, err)
	}
	return nil
}

func inventoryCacheKey(kind allocapp.InventoryCacheKind, projectID uint, buyerUserID uint) string {
	return inventoryCacheKeyPrefix + string(kind) + ":" + strconv.FormatUint(uint64(projectID), 10) + ":" + strconv.FormatUint(uint64(buyerUserID), 10)
}

func inventoryCacheLockKey(entry allocapp.InventoryCacheEntry) string {
	return inventoryCacheKeyPrefix + "lock:" + string(entry.Kind) + ":" + strconv.FormatUint(uint64(entry.ProjectID), 10) + ":" + strconv.FormatUint(uint64(entry.BuyerUserID), 10)
}

func parseInventoryCacheKey(key string) (allocapp.InventoryCacheEntry, bool) {
	parts := strings.Split(strings.TrimPrefix(key, inventoryCacheKeyPrefix), ":")
	if !strings.HasPrefix(key, inventoryCacheKeyPrefix) || len(parts) != 3 {
		return allocapp.InventoryCacheEntry{}, false
	}
	kind := allocapp.InventoryCacheKind(parts[0])
	if kind != allocapp.InventoryCacheStats && kind != allocapp.InventoryCacheProducts {
		return allocapp.InventoryCacheEntry{}, false
	}
	projectID, err := strconv.ParseUint(parts[1], 10, 64)
	if err != nil || projectID == 0 {
		return allocapp.InventoryCacheEntry{}, false
	}
	buyerUserID, err := strconv.ParseUint(parts[2], 10, 64)
	if err != nil {
		return allocapp.InventoryCacheEntry{}, false
	}
	return allocapp.InventoryCacheEntry{Kind: kind, ProjectID: uint(projectID), BuyerUserID: uint(buyerUserID)}, true
}

var inventoryCacheLockReleaseScript = redis.NewScript(`
if redis.call("GET", KEYS[1]) ~= ARGV[1] then
  return 0
end
return redis.call("DEL", KEYS[1])
`)
