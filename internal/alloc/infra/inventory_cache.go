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
	inventoryCacheKeyPrefix        = "alloc:inventory:v6:"
	inventoryCacheFailureKeyPrefix = "alloc:inventory:v6:failure:"
	// The Redis name is retained for compatibility; scores are backend-owned
	// next-refresh times, not client activity times.
	inventoryCacheScheduleKey = "alloc:inventory:v6:active"
)

type InventoryCache struct {
	redis redis.UniversalClient
}

func NewInventoryCache(client redis.UniversalClient) *InventoryCache {
	return &InventoryCache{redis: client}
}

func (c *InventoryCache) GetInventoryStats(ctx context.Context, projectID uint) (*allocapp.InventoryStats, error) {
	return loadInventoryCache[allocapp.InventoryStats](ctx, c.redis, inventoryCacheKey(allocapp.InventoryCacheStats, projectID))
}

func (c *InventoryCache) SetInventoryStats(ctx context.Context, projectID uint, stats *allocapp.InventoryStats, ttl time.Duration) error {
	return storeInventoryCache(ctx, c.redis, inventoryCacheKey(allocapp.InventoryCacheStats, projectID), stats, ttl)
}

func (c *InventoryCache) RefreshInventoryStats(ctx context.Context, projectID uint, stats *allocapp.InventoryStats, ttl time.Duration) error {
	return refreshInventoryCache(ctx, c.redis, inventoryCacheKey(allocapp.InventoryCacheStats, projectID), stats, ttl)
}

func (c *InventoryCache) GetProductInventoryTotals(ctx context.Context, projectID uint) (*allocapp.ProjectProductInventoryTotals, error) {
	snapshots, err := c.GetProductInventorySnapshots(ctx, []uint{projectID})
	if err != nil {
		return nil, err
	}
	return snapshots[projectID], nil
}

func (c *InventoryCache) GetProductInventorySnapshots(ctx context.Context, projectIDs []uint) (map[uint]*allocapp.ProjectProductInventoryTotals, error) {
	result := make(map[uint]*allocapp.ProjectProductInventoryTotals, len(projectIDs))
	if len(projectIDs) == 0 {
		return result, nil
	}
	keys := make([]string, len(projectIDs))
	for i, projectID := range projectIDs {
		keys[i] = inventoryCacheKey(allocapp.InventoryCacheProducts, projectID)
	}
	payloads, err := c.redis.MGet(ctx, keys...).Result()
	if err != nil {
		return nil, fmt.Errorf("load product inventory snapshots: %w", err)
	}
	loadedKeys := make([]redis.Z, 0, len(keys))
	requests := make([]allocapp.ProductInventoryAvailabilityRequest, 0)
	requestTotals := make([]*allocapp.ProjectProductInventoryTotals, 0)
	for i, payload := range payloads {
		if payload == nil {
			continue
		}
		var totals allocapp.ProjectProductInventoryTotals
		if err := json.Unmarshal([]byte(fmt.Sprint(payload)), &totals); err != nil {
			return nil, fmt.Errorf("decode %s: %w", keys[i], err)
		}
		result[projectIDs[i]] = &totals
		loadedKeys = append(loadedKeys, redis.Z{Score: float64(time.Now().UnixMilli()), Member: keys[i]})
		for _, request := range productUnavailableMarkerRequests(totals) {
			requests = append(requests, request)
			requestTotals = append(requestTotals, &totals)
		}
	}
	if len(requests) > 0 {
		markerKeys := make([]string, len(requests))
		for i := range requests {
			markerKeys[i] = productUnavailableMarkerKey(requests[i])
		}
		markers, err := c.redis.MGet(ctx, markerKeys...).Result()
		if err != nil {
			return nil, fmt.Errorf("load product inventory corrections: %w", err)
		}
		for i, marker := range markers {
			if marker != nil {
				markProductUnavailable(requestTotals[i], requests[i])
			}
		}
	}
	if len(loadedKeys) > 0 {
		if err := c.redis.ZAddArgs(ctx, inventoryCacheScheduleKey, redis.ZAddArgs{NX: true, Members: loadedKeys}).Err(); err != nil {
			return nil, fmt.Errorf("schedule product inventory snapshots: %w", err)
		}
	}
	return result, nil
}

// InitializeInventory seeds cold placeholders without overwriting a concurrent
// background refresh.
func (c *InventoryCache) InitializeInventory(ctx context.Context, entries []allocapp.InventoryCacheEntry, ttl time.Duration) error {
	if len(entries) == 0 {
		return nil
	}
	now := float64(time.Now().UnixMilli())
	_, err := c.redis.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
		for _, entry := range entries {
			var value any
			switch entry.Kind {
			case allocapp.InventoryCacheStats:
				value = &allocapp.InventoryStats{ProjectID: entry.ProjectID, Cold: true}
			case allocapp.InventoryCacheProducts:
				value = &allocapp.ProjectProductInventoryTotals{ProjectID: entry.ProjectID, Cold: true}
			default:
				continue
			}
			payload, marshalErr := json.Marshal(value)
			if marshalErr != nil {
				return marshalErr
			}
			key := inventoryCacheKey(entry.Kind, entry.ProjectID)
			pipe.SetNX(ctx, key, payload, ttl)
			pipe.ZAddArgs(ctx, inventoryCacheScheduleKey, redis.ZAddArgs{NX: true, Members: []redis.Z{{Score: now, Member: key}}})
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("initialize inventory cache: %w", err)
	}
	return nil
}

func (c *InventoryCache) SetProductInventoryTotals(ctx context.Context, projectID uint, totals *allocapp.ProjectProductInventoryTotals, ttl time.Duration) error {
	return storeInventoryCache(ctx, c.redis, inventoryCacheKey(allocapp.InventoryCacheProducts, projectID), cloneProductInventoryTotalsForCache(totals), ttl)
}

func (c *InventoryCache) RefreshProductInventoryTotals(ctx context.Context, projectID uint, totals *allocapp.ProjectProductInventoryTotals, ttl time.Duration) error {
	return refreshInventoryCache(ctx, c.redis, inventoryCacheKey(allocapp.InventoryCacheProducts, projectID), cloneProductInventoryTotalsForCache(totals), ttl)
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
	key := inventoryCacheKey(allocapp.InventoryCacheProducts, req.ProjectID)
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
				pipe.Set(ctx, markerKey, "1", allocapp.InventoryRefreshIntervalValue())
				pipe.ZAdd(ctx, inventoryCacheScheduleKey, redis.Z{Score: float64(time.Now().UnixMilli()), Member: key})
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
	suffix := normalizeCandidateSuffix(req.EmailSuffix)
	for _, item := range totals.Items {
		if item.ProductID != req.ProductID {
			continue
		}
		if suffix == "" {
			return true
		}
		for _, entry := range item.Suffixes {
			entrySuffix := normalizeCandidateSuffix(entry.Suffix)
			if entrySuffix == suffix {
				return true
			}
		}
		return false
	}
	return false
}

func productUnavailableMarkerRequests(totals allocapp.ProjectProductInventoryTotals) []allocapp.ProductInventoryAvailabilityRequest {
	requests := make([]allocapp.ProductInventoryAvailabilityRequest, 0, len(totals.Items)*2)
	for _, item := range totals.Items {
		for _, publicOnly := range []bool{false, true} {
			requests = append(requests, allocapp.ProductInventoryAvailabilityRequest{
				ProjectID: totals.ProjectID, ProductID: item.ProductID, PublicOnly: publicOnly,
			})
			for _, suffix := range item.Suffixes {
				requests = append(requests, allocapp.ProductInventoryAvailabilityRequest{
					ProjectID: totals.ProjectID, ProductID: item.ProductID,
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
	suffix := normalizeCandidateSuffix(req.EmailSuffix)
	if suffix == "" {
		suffix = "-"
	}
	return inventoryCacheKeyPrefix + "unavailable:" +
		strconv.FormatUint(uint64(req.ProjectID), 10) + ":" +
		strconv.FormatUint(uint64(req.ProductID), 10) + ":" + scope + ":" + suffix
}

func markProductUnavailable(totals *allocapp.ProjectProductInventoryTotals, req allocapp.ProductInventoryAvailabilityRequest) bool {
	if totals == nil {
		return false
	}
	suffix := normalizeCandidateSuffix(req.EmailSuffix)
	changed := false
	removedTotal := int64(0)
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
				removedTotal = item.TotalAvailable
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
				entrySuffix := normalizeCandidateSuffix(entry.Suffix)
				if entrySuffix != suffix {
					continue
				}
				found = true
				if req.PublicOnly {
					changed = changed || entry.PublicAvailable != 0
					entry.PublicAvailable = 0
				} else {
					changed = changed || entry.TotalAvailable != 0 || entry.PublicAvailable != 0
					removedTotal += entry.TotalAvailable
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
	if !req.PublicOnly {
		totals.TotalAvailable = max(0, totals.TotalAvailable-removedTotal)
	}
	return true
}

func (c *InventoryCache) ClaimDueInventory(ctx context.Context, before time.Time, limit int) ([]allocapp.InventoryCacheEntry, error) {
	if limit <= 0 {
		return nil, nil
	}
	if before.IsZero() {
		before = time.Now()
	}
	items, err := claimDueInventoryScript.Run(ctx, c.redis, []string{inventoryCacheScheduleKey},
		strconv.FormatInt(before.UnixMilli(), 10),
		limit,
	).StringSlice()
	if err != nil {
		return nil, fmt.Errorf("claim due inventory cache keys: %w", err)
	}
	entries := make([]allocapp.InventoryCacheEntry, 0, len(items))
	for _, item := range items {
		entry, ok := parseInventoryCacheKey(item)
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
			pipe.ZAdd(ctx, inventoryCacheScheduleKey, redis.Z{Score: now, Member: inventoryCacheKey(entry.Kind, entry.ProjectID)})
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("requeue inventory cache: %w", err)
	}
	return nil
}

type inventoryRefreshFailure struct {
	LastAttemptAt time.Time `json:"lastAttemptAt"`
	LastError     string    `json:"lastError"`
}

func (c *InventoryCache) RecordInventoryRefreshFailure(ctx context.Context, entry allocapp.InventoryCacheEntry, refreshErr error) error {
	if refreshErr == nil {
		return nil
	}
	failure := inventoryRefreshFailure{LastAttemptAt: time.Now().UTC(), LastError: refreshErr.Error()}
	payload, err := json.Marshal(failure)
	if err != nil {
		return fmt.Errorf("encode inventory refresh failure: %w", err)
	}
	if err := c.redis.Set(ctx, inventoryRefreshFailureKey(entry), payload, allocapp.InventoryRefreshParametersValue().CacheHardTTL).Err(); err != nil {
		return fmt.Errorf("record inventory refresh failure: %w", err)
	}
	return nil
}

func (c *InventoryCache) ClearInventoryRefreshFailure(ctx context.Context, entry allocapp.InventoryCacheEntry) error {
	if err := c.redis.Del(ctx, inventoryRefreshFailureKey(entry)).Err(); err != nil {
		return fmt.Errorf("clear inventory refresh failure: %w", err)
	}
	return nil
}

func (c *InventoryCache) ClearInventoryRefreshFailures(ctx context.Context, entries []allocapp.InventoryCacheEntry) error {
	if len(entries) == 0 {
		return nil
	}
	keys := make([]string, len(entries))
	for i, entry := range entries {
		keys[i] = inventoryRefreshFailureKey(entry)
	}
	if err := c.redis.Del(ctx, keys...).Err(); err != nil {
		return fmt.Errorf("clear inventory refresh failures: %w", err)
	}
	return nil
}

func (c *InventoryCache) ListInventoryRefreshStates(ctx context.Context, projectIDs []uint) (map[uint]allocapp.InventoryRefreshState, error) {
	states := make(map[uint]allocapp.InventoryRefreshState, len(projectIDs))
	if len(projectIDs) == 0 {
		return states, nil
	}
	now := time.Now().UTC()
	pipe := c.redis.Pipeline()
	type commands struct {
		statsScore, productsScore     *redis.FloatCmd
		statsLock, productsLock       *redis.IntCmd
		products                      *redis.StringCmd
		statsFailure, productsFailure *redis.StringCmd
	}
	commandsByProject := make(map[uint]commands, len(projectIDs))
	for _, projectID := range projectIDs {
		stats := allocapp.InventoryCacheEntry{Kind: allocapp.InventoryCacheStats, ProjectID: projectID}
		products := allocapp.InventoryCacheEntry{Kind: allocapp.InventoryCacheProducts, ProjectID: projectID}
		productsKey := inventoryCacheKey(products.Kind, projectID)
		commandsByProject[projectID] = commands{
			statsScore:      pipe.ZScore(ctx, inventoryCacheScheduleKey, inventoryCacheKey(stats.Kind, projectID)),
			productsScore:   pipe.ZScore(ctx, inventoryCacheScheduleKey, productsKey),
			statsLock:       pipe.Exists(ctx, inventoryCacheLockKey(stats)),
			productsLock:    pipe.Exists(ctx, inventoryCacheLockKey(products)),
			products:        pipe.Get(ctx, productsKey),
			statsFailure:    pipe.Get(ctx, inventoryRefreshFailureKey(stats)),
			productsFailure: pipe.Get(ctx, inventoryRefreshFailureKey(products)),
		}
	}
	if _, err := pipe.Exec(ctx); err != nil && err != redis.Nil {
		return nil, fmt.Errorf("load inventory refresh states: %w", err)
	}
	for _, projectID := range projectIDs {
		cmds := commandsByProject[projectID]
		state := allocapp.InventoryRefreshState{ProjectID: projectID, Status: allocapp.InventoryRefreshScheduled}
		payload, payloadErr := cmds.products.Bytes()
		var totals allocapp.ProjectProductInventoryTotals
		if payloadErr == nil {
			if err := json.Unmarshal(payload, &totals); err != nil {
				return nil, fmt.Errorf("decode inventory refresh state for project %d: %w", projectID, err)
			}
			state.TotalAvailable = totals.TotalAvailable
			if !totals.Cold && totals.RefreshedAt != nil {
				refreshedAt := totals.RefreshedAt.UTC()
				state.LastRefreshedAt = &refreshedAt
			}
		} else if payloadErr != redis.Nil {
			return nil, fmt.Errorf("load inventory refresh state for project %d: %w", projectID, payloadErr)
		}

		scores := make([]time.Time, 0, 2)
		due := payloadErr == redis.Nil || totals.Cold
		for _, scoreCmd := range []*redis.FloatCmd{cmds.statsScore, cmds.productsScore} {
			score, err := scoreCmd.Result()
			if err == redis.Nil {
				due = true
				continue
			}
			if err != nil {
				return nil, fmt.Errorf("load inventory refresh schedule for project %d: %w", projectID, err)
			}
			scheduledAt := time.UnixMilli(int64(score))
			if !scheduledAt.After(now) {
				due = true
			} else {
				scores = append(scores, scheduledAt)
			}
		}
		var latestFailure *inventoryRefreshFailure
		for _, failureCmd := range []*redis.StringCmd{cmds.statsFailure, cmds.productsFailure} {
			failurePayload, failureErr := failureCmd.Bytes()
			if failureErr == redis.Nil {
				continue
			}
			if failureErr != nil {
				return nil, fmt.Errorf("load inventory refresh failure for project %d: %w", projectID, failureErr)
			}
			var failure inventoryRefreshFailure
			if err := json.Unmarshal(failurePayload, &failure); err != nil {
				return nil, fmt.Errorf("decode inventory refresh failure for project %d: %w", projectID, err)
			}
			if latestFailure == nil || failure.LastAttemptAt.After(latestFailure.LastAttemptAt) {
				latest := failure
				latestFailure = &latest
			}
		}
		if latestFailure != nil {
			state.LastAttemptAt = &latestFailure.LastAttemptAt
			state.LastError = latestFailure.LastError
		}
		switch {
		case cmds.statsLock.Val() > 0 || cmds.productsLock.Val() > 0:
			state.Status = allocapp.InventoryRefreshRunning
		case latestFailure != nil:
			state.Status = allocapp.InventoryRefreshFailed
		case due:
			state.Status = allocapp.InventoryRefreshQueued
		default:
			if len(scores) > 0 {
				next := scores[0]
				for _, score := range scores[1:] {
					if score.Before(next) {
						next = score
					}
				}
				state.NextRefreshAt = &next
			}
		}
		states[projectID] = state
	}
	return states, nil
}

func (c *InventoryCache) DeleteInventory(ctx context.Context, entry allocapp.InventoryCacheEntry) error {
	key := inventoryCacheKey(entry.Kind, entry.ProjectID)
	_, err := c.redis.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
		pipe.Del(ctx, key)
		pipe.ZRem(ctx, inventoryCacheScheduleKey, key)
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
	member := redis.Z{Score: float64(time.Now().UnixMilli()), Member: key}
	if err := client.ZAddArgs(ctx, inventoryCacheScheduleKey, redis.ZAddArgs{NX: true, Members: []redis.Z{member}}).Err(); err != nil {
		return nil, fmt.Errorf("schedule %s: %w", key, err)
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
		pipe.ZAdd(ctx, inventoryCacheScheduleKey, redis.Z{Score: float64(time.Now().UnixMilli()), Member: key})
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
	nextRefresh := float64(time.Now().Add(allocapp.InventoryRefreshIntervalValue()).UnixMilli())
	_, err = client.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
		pipe.Set(ctx, key, payload, ttl)
		pipe.ZAdd(ctx, inventoryCacheScheduleKey, redis.Z{Score: nextRefresh, Member: key})
		return nil
	})
	if err != nil {
		return fmt.Errorf("refresh %s: %w", key, err)
	}
	return nil
}

func cloneProductInventoryTotalsForCache(source *allocapp.ProjectProductInventoryTotals) *allocapp.ProjectProductInventoryTotals {
	if source == nil {
		return nil
	}
	value := *source
	now := time.Now().UTC()
	value.RefreshedAt = &now
	return &value
}

func inventoryCacheKey(kind allocapp.InventoryCacheKind, projectID uint) string {
	return inventoryCacheKeyPrefix + string(kind) + ":" + strconv.FormatUint(uint64(projectID), 10)
}

func inventoryCacheLockKey(entry allocapp.InventoryCacheEntry) string {
	return inventoryCacheKeyPrefix + "lock:" + string(entry.Kind) + ":" + strconv.FormatUint(uint64(entry.ProjectID), 10)
}

func inventoryRefreshFailureKey(entry allocapp.InventoryCacheEntry) string {
	return inventoryCacheFailureKeyPrefix + string(entry.Kind) + ":" + strconv.FormatUint(uint64(entry.ProjectID), 10)
}

func parseInventoryCacheKey(key string) (allocapp.InventoryCacheEntry, bool) {
	parts := strings.Split(strings.TrimPrefix(key, inventoryCacheKeyPrefix), ":")
	if !strings.HasPrefix(key, inventoryCacheKeyPrefix) || len(parts) != 2 {
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
	return allocapp.InventoryCacheEntry{Kind: kind, ProjectID: uint(projectID)}, true
}

var inventoryCacheLockReleaseScript = redis.NewScript(`
if redis.call("GET", KEYS[1]) ~= ARGV[1] then
  return 0
end
return redis.call("DEL", KEYS[1])
`)

var claimDueInventoryScript = redis.NewScript(`
local entries = redis.call("ZRANGEBYSCORE", KEYS[1], "-inf", ARGV[1], "LIMIT", 0, ARGV[2])
if #entries > 0 then
  redis.call("ZREM", KEYS[1], unpack(entries))
end
return entries
`)
