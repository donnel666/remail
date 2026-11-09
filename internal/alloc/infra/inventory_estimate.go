package infra

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	allocapp "github.com/donnel666/remail/internal/alloc/app"
	"github.com/donnel666/remail/internal/alloc/domain"
	coredomain "github.com/donnel666/remail/internal/core/domain"
	"github.com/redis/go-redis/v9"
)

const (
	fastInventoryBaseKey       = inventoryCacheKeyPrefix + "estimate:base:public"
	fastInventoryBaseLockKey   = inventoryCacheKeyPrefix + "estimate:base:lock"
	fastInventoryHistoryPrefix = inventoryCacheKeyPrefix + "estimate:history:"
	fastInventoryBaseTTL       = 7 * 24 * time.Hour
	fastInventoryHistoryTTL    = 14 * 24 * time.Hour
	fastInventoryMinRefresh    = 6 * time.Hour
)

type fastInventoryBase struct {
	ComputedAt        time.Time        `json:"computedAt"`
	EligibleResources int64            `json:"eligibleResources"`
	DotCapacity       int64            `json:"dotCapacity"`
	PlusDailyLimit    int64            `json:"plusDailyLimit"`
	AliasesBySuffix   map[string]int64 `json:"aliasesBySuffix"`
}

type fastInventoryHistory struct {
	ComputedAt      time.Time        `json:"computedAt"`
	MaxAllocationID uint             `json:"maxAllocationId"`
	MainUsed        int64            `json:"mainUsed"`
	DotUsed         int64            `json:"dotUsed"`
	PlusUsed        int64            `json:"plusUsed"`
	AliasBySuffix   map[string]int64 `json:"aliasBySuffix"`
}

func (r *Repo) GetFastInventoryStats(ctx context.Context, projectID uint) (*allocapp.InventoryStats, error) {
	if r.redis == nil {
		return r.GetInventoryStats(ctx, projectID)
	}
	rows, err := r.fastInventoryProductRows(ctx, projectID)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, domain.ErrProjectNotAllocatable
	}
	previous, err := r.loadCachedFastStats(ctx, projectID)
	if err != nil {
		return nil, err
	}
	if err := r.assertFastProjectScope(ctx, projectID); err != nil {
		if previous != nil {
			return previous, nil
		}
		return r.GetInventoryStats(ctx, projectID)
	}
	unsupported := false
	for _, row := range rows {
		switch coredomain.ProductType(row.Type) {
		case coredomain.ProductTypeMicrosoft:
		default:
			unsupported = true
		}
	}
	if unsupported && previous == nil {
		return r.GetInventoryStats(ctx, projectID)
	}
	base, err := r.loadFastInventoryBase(ctx)
	if err != nil {
		if previous != nil {
			return previous, nil
		}
		return nil, err
	}
	history, err := r.loadFastInventoryHistory(ctx, projectID)
	if err != nil {
		if previous != nil {
			return previous, nil
		}
		return nil, err
	}
	if previous == nil {
		previous = &allocapp.InventoryStats{ProjectID: projectID}
	}
	stats := *previous
	stats.ProjectID = projectID
	stats.Cold = false
	for _, row := range rows {
		switch coredomain.ProductType(row.Type) {
		case coredomain.ProductTypeMicrosoft:
			stats.Microsoft.Enabled = true
			stats.Microsoft.MainEnabled = stats.Microsoft.MainEnabled || row.MainWeight > 0
			stats.Microsoft.DotEnabled = stats.Microsoft.DotEnabled || row.DotWeight > 0
			stats.Microsoft.PlusEnabled = stats.Microsoft.PlusEnabled || row.PlusWeight > 0
		case coredomain.ProductTypeMicrosoftEnterprise:
			// The public fast baseline intentionally excludes enterprise resources.
			if previous != nil {
				return previous, nil
			}
			return r.GetInventoryStats(ctx, projectID)
		}
	}
	oldTotal := stats.Microsoft.TotalAvailable
	stats.Microsoft.EligibleResources = max(0, base.EligibleResources-history.MainUsed)
	stats.Microsoft.MainAvailable = stats.Microsoft.EligibleResources
	stats.Microsoft.ExplicitAliasAvailable = max(0, sumFastInventoryAliases(base.AliasesBySuffix)-sumFastInventoryAliases(history.AliasBySuffix))
	stats.Microsoft.DotCapacity = base.DotCapacity
	stats.Microsoft.ActiveDotAllocations = history.DotUsed
	stats.Microsoft.DotAvailable = max(0, base.DotCapacity-history.DotUsed)
	stats.Microsoft.PlusDailyLimit = base.PlusDailyLimit
	stats.Microsoft.PlusDailyUsed = history.PlusUsed
	stats.Microsoft.PlusDailyAvailable = max(0, base.PlusDailyLimit-history.PlusUsed)
	stats.Microsoft.TotalAvailable = 0
	if stats.Microsoft.MainEnabled {
		stats.Microsoft.TotalAvailable += stats.Microsoft.MainAvailable + stats.Microsoft.ExplicitAliasAvailable
	}
	if stats.Microsoft.DotEnabled {
		stats.Microsoft.TotalAvailable += stats.Microsoft.DotAvailable
	}
	if stats.Microsoft.PlusEnabled {
		stats.Microsoft.TotalAvailable += stats.Microsoft.PlusDailyAvailable
	}
	stats.TotalAvailable = max(0, stats.TotalAvailable-oldTotal+stats.Microsoft.TotalAvailable)
	stats.ActiveMicrosoftAllocations = history.MainUsed + history.DotUsed + history.PlusUsed
	return &stats, nil
}

func (r *Repo) GetFastProductInventoryTotals(ctx context.Context, projectID uint) (*allocapp.ProjectProductInventoryTotals, error) {
	if r.redis == nil {
		return r.GetProductInventoryTotals(ctx, projectID)
	}
	rows, err := r.fastInventoryProductRows(ctx, projectID)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, domain.ErrProjectNotAllocatable
	}
	if previous, err := r.loadCachedFastTotals(ctx, projectID); err == nil && previous == nil {
		for _, row := range rows {
			if coredomain.ProductType(row.Type) != coredomain.ProductTypeMicrosoft {
				return r.GetProductInventoryTotals(ctx, projectID)
			}
		}
	} else if err != nil {
		return nil, err
	}
	stats, err := r.GetFastInventoryStats(ctx, projectID)
	if err != nil {
		return nil, err
	}
	previous, err := r.loadCachedFastTotals(ctx, projectID)
	if err != nil {
		return nil, err
	}
	if previous == nil {
		previous = &allocapp.ProjectProductInventoryTotals{ProjectID: projectID}
	}
	result := *previous
	result.ProjectID = projectID
	result.Cold = false
	result.TotalAvailable = stats.TotalAvailable
	for _, row := range rows {
		item := fastProductItem(&result, row.ProductID)
		if item == nil {
			result.Items = append(result.Items, allocapp.ProductInventoryTotal{ProductID: row.ProductID, ProductType: coredomain.ProductType(row.Type)})
			item = &result.Items[len(result.Items)-1]
		}
		item.ProductType = coredomain.ProductType(row.Type)
		if item.ProductType != coredomain.ProductTypeMicrosoft {
			continue
		}
		item.TotalAvailable = productInventoryTotalFromStats(row, stats)
		item.PublicAvailable = item.TotalAvailable
		item.CodeAvailable = int64Ptr(item.TotalAvailable)
		item.CodePublicAvailable = int64Ptr(item.PublicAvailable)
		item.PurchaseAvailable = int64Ptr(item.TotalAvailable)
		item.PurchasePublicAvailable = int64Ptr(item.PublicAvailable)
	}
	return &result, nil
}

func (r *Repo) fastInventoryProductRows(ctx context.Context, projectID uint) ([]productInventoryRow, error) {
	var rows []productInventoryRow
	err := r.dbFor(ctx).Raw(`
SELECT pp.id AS product_id, pp.type AS type, pp.code_enabled AS code_enabled,
       pp.purchase_enabled AS purchase_enabled, pp.main_weight AS main_weight,
       pp.dot_weight AS dot_weight, pp.plus_weight AS plus_weight
FROM projects p
JOIN project_products pp ON pp.project_id = p.id
WHERE p.id = ? AND p.status = 'listed' AND pp.status = 'enabled'
  AND pp.type IN ('microsoft', 'microsoft_enterprise', 'domain', 'gmail', 'gmail_variant', 'icloud', 'proto', 'daum')
ORDER BY pp.id ASC`, projectID).Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("load fast inventory product rows: %w", err)
	}
	return rows, nil
}

func (r *Repo) assertFastProjectScope(ctx context.Context, projectID uint) error {
	var restricted int64
	if err := r.dbFor(ctx).Raw(`
SELECT (
  EXISTS (SELECT 1 FROM project_microsoft_suffix_blacklists WHERE project_id = ?)
  OR EXISTS (SELECT 1 FROM microsoft_resource_project_matches WHERE project_id = ?)
)`, projectID, projectID).Scan(&restricted).Error; err != nil {
		return fmt.Errorf("check fast inventory project scope: %w", err)
	}
	if restricted != 0 {
		return errors.New("project has Microsoft inventory restrictions")
	}
	return nil
}

func (r *Repo) loadFastInventoryBase(ctx context.Context) (*fastInventoryBase, error) {
	if payload, err := r.redis.Get(ctx, fastInventoryBaseKey).Bytes(); err == nil {
		var base fastInventoryBase
		if json.Unmarshal(payload, &base) == nil && time.Since(base.ComputedAt) < fastInventoryBaseTTL {
			return &base, nil
		}
	} else if err != redis.Nil {
		return nil, err
	}
	locked, err := r.redis.SetNX(ctx, fastInventoryBaseLockKey, time.Now().UnixMilli(), 10*time.Minute).Result()
	if err != nil {
		return nil, fmt.Errorf("claim fast inventory base: %w", err)
	}
	if !locked {
		return nil, errors.New("fast inventory base is being rebuilt")
	}
	defer r.redis.Del(context.WithoutCancel(ctx), fastInventoryBaseLockKey)
	var capacity struct {
		EligibleResources int64
		DotCapacity       int64
		PlusDailyLimit    int64
	}
	public := "ms.for_sale = TRUE AND u.status = 'active' AND u.role IN ('supplier', 'admin', 'super_admin')"
	if err := r.dbFor(ctx).Raw(`SELECT COUNT(*) AS eligible_resources,
COALESCE(SUM(` + microsoftDotCapacityExpression("ms") + `),0) AS dot_capacity,
COALESCE(SUM(ms.plus_daily_limit),0) AS plus_daily_limit
FROM microsoft_resources ms
JOIN email_resources er ON er.id = ms.id AND er.type = 'microsoft'
JOIN users u ON u.id = er.owner_user_id
WHERE ms.status = 'normal' AND ` + microsoftNotUnderBlockingMaintenanceCondition + `
  AND ` + public + ` AND ms.email_domain NOT LIKE '%.onmicrosoft.com'`).Scan(&capacity).Error; err != nil {
		return nil, fmt.Errorf("build fast inventory resource base: %w", err)
	}
	var aliases []struct {
		Suffix string
		Count  int64
	}
	if err := r.dbFor(ctx).Raw(`SELECT ea.email_domain AS suffix, COUNT(*) AS count
FROM explicit_aliases ea
JOIN microsoft_resources ms ON ms.id = ea.resource_id
JOIN email_resources er ON er.id = ms.id AND er.type = 'microsoft'
JOIN users u ON u.id = er.owner_user_id
WHERE ea.status = 'normal' AND ms.status = 'normal'
  AND ` + microsoftNotUnderBlockingMaintenanceCondition + ` AND ` + public + `
  AND ms.email_domain NOT LIKE '%.onmicrosoft.com'
GROUP BY ea.email_domain`).Scan(&aliases).Error; err != nil {
		return nil, fmt.Errorf("build fast inventory alias base: %w", err)
	}
	base := &fastInventoryBase{ComputedAt: time.Now().UTC(), EligibleResources: capacity.EligibleResources, DotCapacity: capacity.DotCapacity, PlusDailyLimit: capacity.PlusDailyLimit, AliasesBySuffix: make(map[string]int64, len(aliases))}
	for _, row := range aliases {
		base.AliasesBySuffix[strings.ToLower(strings.TrimSpace(row.Suffix))] = row.Count
	}
	payload, err := json.Marshal(base)
	if err != nil {
		return nil, err
	}
	if err := r.redis.Set(ctx, fastInventoryBaseKey, payload, fastInventoryBaseTTL).Err(); err != nil {
		return nil, fmt.Errorf("store fast inventory base: %w", err)
	}
	return base, nil
}

func (r *Repo) loadFastInventoryHistory(ctx context.Context, projectID uint) (*fastInventoryHistory, error) {
	key := fastInventoryHistoryPrefix + strconv.FormatUint(uint64(projectID), 10)
	var history fastInventoryHistory
	if payload, err := r.redis.Get(ctx, key).Bytes(); err == nil {
		if json.Unmarshal(payload, &history) == nil && time.Since(history.ComputedAt) < fastInventoryMinRefresh {
			return &history, nil
		}
	} else if err != redis.Nil {
		return nil, err
	}
	var maxID uint
	if err := r.dbFor(ctx).Raw("SELECT COALESCE(MAX(id),0) FROM microsoft_allocations WHERE project_id = ?", projectID).Scan(&maxID).Error; err != nil {
		return nil, fmt.Errorf("load fast inventory allocation watermark: %w", err)
	}
	if history.MaxAllocationID == maxID && !history.ComputedAt.IsZero() {
		return &history, nil
	}
	var counts struct {
		MainUsed int64
		DotUsed  int64
		PlusUsed int64
	}
	if err := r.dbFor(ctx).Raw(`SELECT
COUNT(DISTINCT CASE WHEN mailbox = 'main' THEN resource_id END) AS main_used,
COUNT(DISTINCT CASE WHEN mailbox = 'dot' THEN dot_alias_id END) AS dot_used,
COUNT(DISTINCT CASE WHEN mailbox = 'plus' THEN plus_alias_id END) AS plus_used
FROM microsoft_allocations WHERE project_id = ?`, projectID).Scan(&counts).Error; err != nil {
		return nil, fmt.Errorf("build fast inventory allocation history: %w", err)
	}
	var aliases []struct {
		Suffix string
		Count  int64
	}
	if err := r.dbFor(ctx).Raw(`SELECT ea.email_domain AS suffix, COUNT(DISTINCT ma.explicit_alias_id) AS count
FROM microsoft_allocations ma
JOIN explicit_aliases ea ON ea.id = ma.explicit_alias_id
WHERE ma.project_id = ? AND ma.mailbox = 'alias' AND ma.explicit_alias_id IS NOT NULL
GROUP BY ea.email_domain`, projectID).Scan(&aliases).Error; err != nil {
		return nil, fmt.Errorf("build fast inventory alias history: %w", err)
	}
	history = fastInventoryHistory{ComputedAt: time.Now().UTC(), MaxAllocationID: maxID, MainUsed: counts.MainUsed, DotUsed: counts.DotUsed, PlusUsed: counts.PlusUsed, AliasBySuffix: make(map[string]int64, len(aliases))}
	for _, row := range aliases {
		history.AliasBySuffix[strings.ToLower(strings.TrimSpace(row.Suffix))] = row.Count
	}
	payload, err := json.Marshal(&history)
	if err != nil {
		return nil, err
	}
	if err := r.redis.Set(ctx, key, payload, fastInventoryHistoryTTL).Err(); err != nil {
		return nil, fmt.Errorf("store fast inventory history: %w", err)
	}
	return &history, nil
}

func (r *Repo) loadCachedFastStats(ctx context.Context, projectID uint) (*allocapp.InventoryStats, error) {
	payload, err := r.redis.Get(ctx, inventoryCacheKey(allocapp.InventoryCacheStats, projectID)).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var stats allocapp.InventoryStats
	if err := json.Unmarshal(payload, &stats); err != nil {
		return nil, fmt.Errorf("decode cached fast inventory stats: %w", err)
	}
	return &stats, nil
}

func (r *Repo) loadCachedFastTotals(ctx context.Context, projectID uint) (*allocapp.ProjectProductInventoryTotals, error) {
	payload, err := r.redis.Get(ctx, inventoryCacheKey(allocapp.InventoryCacheProducts, projectID)).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var totals allocapp.ProjectProductInventoryTotals
	if err := json.Unmarshal(payload, &totals); err != nil {
		return nil, fmt.Errorf("decode cached fast inventory totals: %w", err)
	}
	return &totals, nil
}

func fastProductItem(totals *allocapp.ProjectProductInventoryTotals, productID uint) *allocapp.ProductInventoryTotal {
	for i := range totals.Items {
		if totals.Items[i].ProductID == productID {
			return &totals.Items[i]
		}
	}
	return nil
}

func sumFastInventoryAliases(values map[string]int64) int64 {
	var total int64
	for _, value := range values {
		total += value
	}
	return total
}

func int64Ptr(value int64) *int64 { return &value }
