package infra

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	allocapp "github.com/donnel666/remail/internal/alloc/app"
	"github.com/donnel666/remail/internal/alloc/domain"
	coredomain "github.com/donnel666/remail/internal/core/domain"
	"github.com/redis/go-redis/v9"
)

const (
	fastInventoryBaseKey          = inventoryCacheKeyPrefix + "estimate:base:public"
	fastInventoryBaseLockKey      = inventoryCacheKeyPrefix + "estimate:base:lock"
	fastInventoryBaseStateKey     = inventoryCacheKeyPrefix + "estimate:base:state"
	fastInventoryBaseAliasesKey   = inventoryCacheKeyPrefix + "estimate:base:aliases"
	fastInventoryHistoryPrefix    = inventoryCacheKeyPrefix + "estimate:history:"
	fastInventoryBaseTTL          = 7 * 24 * time.Hour
	fastInventoryHistoryTTL       = 14 * 24 * time.Hour
	fastInventoryMinRefresh       = 6 * time.Hour
	fastInventoryBuildTimeout     = 30 * time.Second
	fastInventoryHistoryLockTTL   = 10 * time.Minute
	fastInventoryHistoryBatchSize = 5000
	fastInventoryBaseSuffixBatch  = 8
	fastInventoryHLLRelativeError = 0.0082
	fastInventoryMaxErrorBound    = 100000
)

type fastInventoryBase struct {
	ComputedAt        time.Time        `json:"computedAt"`
	EligibleResources int64            `json:"eligibleResources"`
	DotCapacity       int64            `json:"dotCapacity"`
	PlusDailyLimit    int64            `json:"plusDailyLimit"`
	AliasesBySuffix   map[string]int64 `json:"aliasesBySuffix"`
}

type fastInventoryBaseState struct {
	Cursor            string `json:"cursor"`
	EligibleResources int64  `json:"eligibleResources"`
	DotCapacity       int64  `json:"dotCapacity"`
	PlusDailyLimit    int64  `json:"plusDailyLimit"`
}

type fastInventoryHistory struct {
	ComputedAt             time.Time        `json:"computedAt"`
	MaxAllocationCreatedAt time.Time        `json:"maxAllocationCreatedAt"`
	MaxAllocationID        uint             `json:"maxAllocationId"`
	MainUsed               int64            `json:"mainUsed"`
	DotUsed                int64            `json:"dotUsed"`
	PlusUsed               int64            `json:"plusUsed"`
	AliasBySuffix          map[string]int64 `json:"aliasBySuffix"`
	ErrorBound             int64            `json:"errorBound"`
}

var errFastInventoryUnavailable = errors.New("fast inventory estimate is temporarily unavailable")
var errFastInventoryRestrictedProject = errors.New("project has Microsoft inventory restrictions")

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
		if errors.Is(err, errFastInventoryRestrictedProject) {
			return r.GetInventoryStats(ctx, projectID)
		}
		return nil, err
	}
	hasMicrosoft := false
	for _, row := range rows {
		switch coredomain.ProductType(row.Type) {
		case coredomain.ProductTypeMicrosoft:
			hasMicrosoft = true
		}
	}
	if !hasMicrosoft {
		if previous != nil {
			return previous, nil
		}
		return &allocapp.InventoryStats{ProjectID: projectID, Cold: true}, nil
	}
	base, err := r.loadFastInventoryBase(ctx)
	if err != nil {
		return nil, errors.Join(errFastInventoryUnavailable, err)
	}
	history, err := r.loadFastInventoryHistory(ctx, projectID)
	if err != nil {
		return nil, errors.Join(errFastInventoryUnavailable, err)
	}
	if history.ErrorBound > fastInventoryMaxErrorBound {
		return nil, fmt.Errorf("%w: error bound %d exceeds %d", errFastInventoryUnavailable, history.ErrorBound, fastInventoryMaxErrorBound)
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
			continue
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
	plusUsed, err := r.fastPlusDailyUsed(ctx)
	if err != nil {
		return nil, errors.Join(errFastInventoryUnavailable, err)
	}
	stats.Microsoft.PlusDailyUsed = plusUsed
	stats.Microsoft.PlusDailyAvailable = max(0, base.PlusDailyLimit-plusUsed)
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
	stats.ActiveMicrosoftAllocations, err = r.fastActiveMicrosoftAllocations(ctx, projectID)
	if err != nil {
		return nil, errors.Join(errFastInventoryUnavailable, err)
	}
	return &stats, nil
}

func (r *Repo) fastPlusDailyUsed(ctx context.Context) (int64, error) {
	var used int64
	date := time.Now().UTC().Format("2006-01-02")
	if err := r.dbFor(ctx).Raw(`SELECT COALESCE(SUM(used_count), 0)
FROM allocation_daily_usages
WHERE usage_date = ? AND resource_type = 'microsoft' AND usage_kind = 'plus'`, date).Scan(&used).Error; err != nil {
		return 0, fmt.Errorf("load fast Microsoft plus usage: %w", err)
	}
	return used, nil
}

func (r *Repo) fastActiveMicrosoftAllocations(ctx context.Context, projectID uint) (int64, error) {
	var active int64
	if err := r.dbFor(ctx).Raw("SELECT COUNT(*) FROM microsoft_allocations WHERE project_id = ? AND status = 'allocated'", projectID).Scan(&active).Error; err != nil {
		return 0, fmt.Errorf("load fast Microsoft active allocations: %w", err)
	}
	return active, nil
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
	previous, err := r.loadCachedFastTotals(ctx, projectID)
	if err != nil {
		return nil, err
	}
	stats, err := r.GetFastInventoryStats(ctx, projectID)
	if err != nil {
		return nil, err
	}
	base, err := r.loadFastInventoryBase(ctx)
	if err != nil {
		return nil, errors.Join(errFastInventoryUnavailable, err)
	}
	history, err := r.loadFastInventoryHistory(ctx, projectID)
	if err != nil {
		return nil, errors.Join(errFastInventoryUnavailable, err)
	}
	if history.ErrorBound > fastInventoryMaxErrorBound {
		return nil, fmt.Errorf("%w: error bound %d exceeds %d", errFastInventoryUnavailable, history.ErrorBound, fastInventoryMaxErrorBound)
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
		item.Suffixes = fastInventorySuffixes(base, history)
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
		return errFastInventoryRestrictedProject
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
		return nil, errors.Join(errFastInventoryUnavailable, errors.New("fast inventory base is being rebuilt"))
	}
	defer r.redis.Del(context.WithoutCancel(ctx), fastInventoryBaseLockKey)
	buildCtx, cancel := context.WithTimeout(ctx, fastInventoryBuildTimeout)
	defer cancel()

	state := fastInventoryBaseState{}
	if payload, stateErr := r.redis.Get(buildCtx, fastInventoryBaseStateKey).Bytes(); stateErr == nil {
		_ = json.Unmarshal(payload, &state)
	} else if stateErr != redis.Nil {
		return nil, stateErr
	}
	var suffixes []string
	if err := r.dbFor(buildCtx).Raw(`SELECT email_domain
FROM microsoft_resources
WHERE email_domain IS NOT NULL AND email_domain <> '' AND email_domain > ?
GROUP BY email_domain ORDER BY email_domain LIMIT ?`, state.Cursor, fastInventoryBaseSuffixBatch).Scan(&suffixes).Error; err != nil {
		return nil, errors.Join(errFastInventoryUnavailable, fmt.Errorf("load fast inventory base suffix batch: %w", err))
	}
	if len(suffixes) == 0 {
		return r.finishFastInventoryBase(buildCtx, state)
	}
	public := "ms.for_sale = TRUE AND u.status = 'active' AND u.role IN ('supplier', 'admin', 'super_admin')"
	for _, suffix := range suffixes {
		var capacity struct {
			EligibleResources int64
			DotCapacity       int64
			PlusDailyLimit    int64
		}
		if err := r.dbFor(buildCtx).Raw(`SELECT COUNT(*) AS eligible_resources,
COALESCE(SUM(`+microsoftDotCapacityExpression("ms")+`),0) AS dot_capacity,
COALESCE(SUM(ms.plus_daily_limit),0) AS plus_daily_limit
FROM microsoft_resources ms
JOIN email_resources er ON er.id=ms.id AND er.type='microsoft'
JOIN users u ON u.id=er.owner_user_id
WHERE ms.email_domain = ? AND ms.status='normal' AND `+microsoftNotUnderBlockingMaintenanceCondition+`
  AND `+public+` AND ms.email_domain NOT LIKE '%.onmicrosoft.com'`, suffix).Scan(&capacity).Error; err != nil {
			return nil, errors.Join(errFastInventoryUnavailable, fmt.Errorf("build fast inventory resource suffix %s: %w", suffix, err))
		}
		var aliasCount int64
		if err := r.dbFor(buildCtx).Raw(`SELECT COUNT(*)
FROM explicit_aliases ea
JOIN microsoft_resources ms ON ms.id=ea.resource_id
JOIN email_resources er ON er.id=ms.id AND er.type='microsoft'
JOIN users u ON u.id=er.owner_user_id
WHERE ea.email_domain = ? AND ea.status='normal' AND ms.status='normal'
  AND `+microsoftNotUnderBlockingMaintenanceCondition+` AND `+public+``, suffix).Scan(&aliasCount).Error; err != nil {
			return nil, errors.Join(errFastInventoryUnavailable, fmt.Errorf("build fast inventory alias suffix %s: %w", suffix, err))
		}
		state.EligibleResources += capacity.EligibleResources
		state.DotCapacity += capacity.DotCapacity
		state.PlusDailyLimit += capacity.PlusDailyLimit
		if err := r.redis.HIncrBy(buildCtx, fastInventoryBaseAliasesKey, suffix, aliasCount).Err(); err != nil {
			return nil, err
		}
		state.Cursor = suffix
	}
	statePayload, _ := json.Marshal(&state)
	if err := r.redis.Set(buildCtx, fastInventoryBaseStateKey, statePayload, fastInventoryBaseTTL).Err(); err != nil {
		return nil, err
	}
	return nil, errors.Join(errFastInventoryUnavailable, errors.New("fast inventory base is still warming"))
}

func (r *Repo) finishFastInventoryBase(ctx context.Context, state fastInventoryBaseState) (*fastInventoryBase, error) {
	values, err := r.redis.HGetAll(ctx, fastInventoryBaseAliasesKey).Result()
	if err != nil {
		return nil, err
	}
	aliases := make(map[string]int64, len(values))
	for suffix, raw := range values {
		value, parseErr := strconv.ParseInt(raw, 10, 64)
		if parseErr != nil {
			return nil, parseErr
		}
		aliases[suffix] = value
	}
	base := &fastInventoryBase{ComputedAt: time.Now().UTC(), EligibleResources: state.EligibleResources, DotCapacity: state.DotCapacity, PlusDailyLimit: state.PlusDailyLimit, AliasesBySuffix: aliases}
	payload, err := json.Marshal(base)
	if err != nil {
		return nil, err
	}
	if err := r.redis.Set(ctx, fastInventoryBaseKey, payload, fastInventoryBaseTTL).Err(); err != nil {
		return nil, fmt.Errorf("store fast inventory base: %w", err)
	}
	_ = r.redis.Del(ctx, fastInventoryBaseStateKey, fastInventoryBaseAliasesKey).Err()
	return base, nil
}

func (r *Repo) WarmFastInventoryBase(ctx context.Context) error {
	_, err := r.loadFastInventoryBase(ctx)
	if errors.Is(err, errFastInventoryUnavailable) {
		return nil
	}
	return err
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
	lockKey := key + ":lock"
	locked, err := r.redis.SetNX(ctx, lockKey, time.Now().UnixMilli(), fastInventoryHistoryLockTTL).Result()
	if err != nil {
		return nil, fmt.Errorf("claim fast inventory history: %w", err)
	}
	if !locked {
		return nil, errors.New("fast inventory history is being rebuilt")
	}
	defer r.redis.Del(context.WithoutCancel(ctx), lockKey)
	buildCtx, cancel := context.WithTimeout(ctx, fastInventoryBuildTimeout)
	defer cancel()
	cursorKey := key + ":cursor"
	var cursor struct {
		CreatedAt time.Time `json:"createdAt"`
		ID        uint      `json:"id"`
	}
	if payload, err := r.redis.Get(buildCtx, cursorKey).Bytes(); err == nil {
		_ = json.Unmarshal(payload, &cursor)
	} else if err != redis.Nil {
		return nil, err
	}
	mainKey := key + ":hll:main"
	dotKey := key + ":hll:dot"
	plusKey := key + ":hll:plus"
	suffixKey := key + ":suffixes"
	for {
		var rows []struct {
			ID            uint           `gorm:"column:id"`
			CreatedAt     time.Time      `gorm:"column:created_at"`
			Mailbox       string         `gorm:"column:mailbox"`
			ResourceID    sql.NullInt64  `gorm:"column:resource_id"`
			ExplicitAlias sql.NullInt64  `gorm:"column:explicit_alias_id"`
			DotAlias      sql.NullInt64  `gorm:"column:dot_alias_id"`
			PlusAlias     sql.NullInt64  `gorm:"column:plus_alias_id"`
			EmailDomain   sql.NullString `gorm:"column:email_domain"`
		}
		query := `SELECT /*+ SET_VAR(max_execution_time=30000) */
ma.id, ma.created_at, ma.mailbox, ma.resource_id, ma.explicit_alias_id,
ma.dot_alias_id, ma.plus_alias_id, ea.email_domain
FROM microsoft_allocations ma
LEFT JOIN explicit_aliases ea ON ea.id = ma.explicit_alias_id
`

		var args []any
		if cursor.CreatedAt.IsZero() {
			query += "WHERE ma.project_id = ? ORDER BY ma.created_at ASC, ma.id ASC LIMIT ?"
			args = []any{projectID, fastInventoryHistoryBatchSize}
		} else {
			query += "WHERE ma.project_id = ? AND (ma.created_at > ? OR (ma.created_at = ? AND ma.id > ?)) ORDER BY ma.created_at ASC, ma.id ASC LIMIT ?"
			args = []any{projectID, cursor.CreatedAt, cursor.CreatedAt, cursor.ID, fastInventoryHistoryBatchSize}
		}
		if err := r.dbFor(buildCtx).Raw(query, args...).Scan(&rows).Error; err != nil {
			return nil, fmt.Errorf("scan fast inventory allocation history: %w", err)
		}
		if len(rows) == 0 {
			break
		}
		pipe := r.redis.Pipeline()
		for _, row := range rows {
			switch row.Mailbox {
			case "main":
				if row.ResourceID.Valid {
					pipe.PFAdd(buildCtx, mainKey, strconv.FormatInt(row.ResourceID.Int64, 10))
				}
			case "dot":
				if row.DotAlias.Valid {
					pipe.PFAdd(buildCtx, dotKey, strconv.FormatInt(row.DotAlias.Int64, 10))
				}
			case "plus":
				if row.PlusAlias.Valid {
					pipe.PFAdd(buildCtx, plusKey, strconv.FormatInt(row.PlusAlias.Int64, 10))
				}
			case "alias":
				if row.ExplicitAlias.Valid && row.EmailDomain.Valid {
					suffix := strings.ToLower(strings.TrimSpace(row.EmailDomain.String))
					if suffix != "" {
						aliasKey := fastInventoryAliasHLLKey(key, suffix)
						pipe.PFAdd(buildCtx, aliasKey, strconv.FormatInt(row.ExplicitAlias.Int64, 10))
						pipe.SAdd(buildCtx, suffixKey, suffix)
					}
				}
			}
		}
		if _, err := pipe.Exec(buildCtx); err != nil {
			return nil, fmt.Errorf("store fast inventory history batch: %w", err)
		}
		last := rows[len(rows)-1]
		cursor = struct {
			CreatedAt time.Time `json:"createdAt"`
			ID        uint      `json:"id"`
		}{CreatedAt: last.CreatedAt, ID: last.ID}
		cursorPayload, _ := json.Marshal(cursor)
		if err := r.redis.Set(buildCtx, cursorKey, cursorPayload, fastInventoryHistoryTTL).Err(); err != nil {
			return nil, fmt.Errorf("store fast inventory history cursor: %w", err)
		}
	}
	mainUsed, err := r.redis.PFCount(buildCtx, mainKey).Result()
	if err != nil {
		return nil, err
	}
	dotUsed, err := r.redis.PFCount(buildCtx, dotKey).Result()
	if err != nil {
		return nil, err
	}
	plusUsed, err := r.redis.PFCount(buildCtx, plusKey).Result()
	if err != nil {
		return nil, err
	}
	suffixes, err := r.redis.SMembers(buildCtx, suffixKey).Result()
	if err != nil {
		return nil, err
	}
	aliasBySuffix := make(map[string]int64, len(suffixes))
	var aliasTotal int64
	for _, suffix := range suffixes {
		count, countErr := r.redis.PFCount(buildCtx, fastInventoryAliasHLLKey(key, suffix)).Result()
		if countErr != nil {
			return nil, countErr
		}
		aliasBySuffix[suffix] = count
		aliasTotal += count
	}
	history = fastInventoryHistory{
		ComputedAt:             time.Now().UTC(),
		MaxAllocationCreatedAt: cursor.CreatedAt,
		MaxAllocationID:        cursor.ID,
		MainUsed:               mainUsed,
		DotUsed:                dotUsed,
		PlusUsed:               plusUsed,
		AliasBySuffix:          aliasBySuffix,
		ErrorBound:             int64(float64(mainUsed+dotUsed+plusUsed+aliasTotal) * fastInventoryHLLRelativeError),
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

func fastInventoryAliasHLLKey(historyKey, suffix string) string {
	return historyKey + ":hll:alias:" + strconv.FormatUint(uint64(len(suffix)), 10) + ":" + suffix
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

func fastInventorySuffixes(base *fastInventoryBase, history *fastInventoryHistory) []allocapp.ProductInventorySuffixTotal {
	if base == nil || history == nil {
		return nil
	}
	keys := make([]string, 0, len(base.AliasesBySuffix))
	for suffix := range base.AliasesBySuffix {
		keys = append(keys, suffix)
	}
	sort.Strings(keys)
	result := make([]allocapp.ProductInventorySuffixTotal, 0, len(keys))
	for _, suffix := range keys {
		available := max(0, base.AliasesBySuffix[suffix]-history.AliasBySuffix[suffix])
		result = append(result, allocapp.ProductInventorySuffixTotal{Suffix: suffix, TotalAvailable: available, PublicAvailable: available})
	}
	return result
}

func int64Ptr(value int64) *int64 { return &value }
