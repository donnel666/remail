package infra

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"sort"
	"time"

	allocapp "github.com/donnel666/remail/internal/alloc/app"
	"github.com/donnel666/remail/internal/alloc/domain"
	coredomain "github.com/donnel666/remail/internal/core/domain"
	"github.com/donnel666/remail/internal/platform"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

func (r *Repo) GetFastInventoryStats(ctx context.Context, projectID uint) (*allocapp.InventoryStats, error) {
	if r.redis == nil {
		return r.GetInventoryStats(ctx, projectID)
	}
	stats, _, err := r.fastProjectInventory(ctx, projectID)
	return stats, err
}

func (r *Repo) GetFastProductInventoryTotals(ctx context.Context, projectID uint) (*allocapp.ProjectProductInventoryTotals, error) {
	if r.redis == nil {
		return r.GetProductInventoryTotals(ctx, projectID)
	}
	_, totals, err := r.fastProjectInventory(ctx, projectID)
	return totals, err
}

func (r *Repo) fastProjectInventory(ctx context.Context, projectID uint) (*allocapp.InventoryStats, *allocapp.ProjectProductInventoryTotals, error) {
	rows, err := r.fastInventoryProductRows(ctx, projectID)
	if err != nil {
		return nil, nil, err
	}
	if len(rows) == 0 {
		return nil, nil, domain.ErrProjectNotAllocatable
	}
	other, otherErr := r.otherInventory(ctx, projectID, rows)
	if other == nil {
		return nil, nil, otherErr
	}
	stats := other.Stats
	totals := &allocapp.ProjectProductInventoryTotals{ProjectID: projectID, Items: other.Items, SourceRefreshedAt: maps.Clone(stats.SourceRefreshedAt)}
	var microsoftRows []productInventoryRow
	for _, row := range rows {
		if coredomain.ProductType(row.Type) == coredomain.ProductTypeMicrosoft {
			microsoftRows = append(microsoftRows, row)
		}
	}
	var msErr error
	if len(microsoftRows) > 0 {
		ms, items, observed, err := r.projectMicrosoftInventory(ctx, projectID, microsoftRows)
		statsObserved, productsObserved := observed, observed
		msErr = err
		if ms == nil {
			var previousProducts *allocapp.ProjectProductInventoryTotals
			var fallbackErr error
			ms, previousProducts, fallbackErr = r.previousMicrosoftInventory(ctx, projectID, microsoftRows)
			msErr = inventoryRefreshErrors(msErr, fallbackErr)
			if ms != nil {
				statsObserved = ms.SourceRefreshedAt[coredomain.ProductTypeMicrosoft]
			}
			if previousProducts != nil {
				items = previousProducts.Items
				productsObserved = previousProducts.SourceRefreshedAt[coredomain.ProductTypeMicrosoft]
			}
		}
		if ms != nil {
			stats.Microsoft, stats.ActiveMicrosoftAllocations = ms.Microsoft, ms.ActiveMicrosoftAllocations
			stats.TotalAvailable += ms.Microsoft.TotalAvailable
			stats.SourceRefreshedAt[coredomain.ProductTypeMicrosoft] = statsObserved
		}
		if len(items) > 0 {
			totals.Items = append(totals.Items, items...)
			totals.SourceRefreshedAt[coredomain.ProductTypeMicrosoft] = productsObserved
		}
	}
	err = inventoryRefreshErrors(otherErr, msErr)
	stats.RefreshPending, totals.RefreshPending = err != nil, err != nil
	sort.Slice(totals.Items, func(i, j int) bool { return totals.Items[i].ProductID < totals.Items[j].ProductID })
	for _, item := range totals.Items {
		totals.TotalAvailable += item.TotalAvailable
	}
	return &stats, totals, err
}

func (r *Repo) projectMicrosoftInventory(ctx context.Context, projectID uint, rows []productInventoryRow) (*allocapp.InventoryStats, []allocapp.ProductInventoryTotal, time.Time, error) {
	// Optional generated mailbox modes retain their existing authoritative formulas and a bounded deadline.
	for _, row := range rows {
		if row.DotWeight > 0 || row.PlusWeight > 0 {
			exactCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			defer cancel()
			var stats *allocapp.InventoryStats
			var items []allocapp.ProductInventoryTotal
			err := r.dbFor(exactCtx).Transaction(func(tx *gorm.DB) error {
				readCtx := platform.WithGormTx(exactCtx, tx)
				var err error
				stats, err = r.inventoryStatsForProducts(readCtx, projectID, rows)
				if err != nil {
					return err
				}
				for _, row := range rows {
					item, err := r.productInventoryItem(readCtx, projectID, row, stats)
					if err != nil {
						return err
					}
					items = append(items, item)
				}
				return nil
			}, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
			if err != nil {
				return nil, nil, time.Time{}, err
			}
			return stats, items, time.Now().UTC(), nil
		}
	}
	snapshot, refreshErr := r.sharedMicrosoftInventory(ctx)
	if snapshot == nil {
		return nil, nil, time.Time{}, refreshErr
	}
	var suffixBlacklist []string
	if err := r.dbFor(ctx).Table("project_microsoft_suffix_blacklists").Where("project_id=?", projectID).Pluck("suffix", &suffixBlacklist).Error; err != nil {
		return nil, nil, time.Time{}, err
	}
	blocked := make(map[string]bool, len(suffixBlacklist))
	for _, suffix := range suffixBlacklist {
		blocked[normalizeCandidateSuffix(suffix)] = true
	}
	suffixes, main, aliases, err := snapshot.available(projectID, blocked)
	if err != nil {
		return nil, nil, time.Time{}, err
	}
	var eligible, aliasPool int64
	for suffix, n := range snapshot.Main {
		if !blocked[suffix] {
			eligible += n
		}
	}
	for suffix, n := range snapshot.Aliases {
		if !blocked[suffix] {
			aliasPool += n
		}
	}
	stats := &allocapp.InventoryStats{ProjectID: projectID, Microsoft: allocapp.MicrosoftInventoryStats{Enabled: true, MainEnabled: true, EligibleResources: eligible, MainAvailable: main, ExplicitAliasAvailable: aliases, TotalAvailable: main + aliases}, ActiveMicrosoftAllocations: snapshot.Active[projectID], TotalAvailable: main + aliases}
	items := make([]allocapp.ProductInventoryTotal, 0, len(rows))
	for _, row := range rows {
		item := allocapp.ProductInventoryTotal{ProductID: row.ProductID, ProductType: coredomain.ProductTypeMicrosoft, TotalAvailable: main + aliases, PublicAvailable: main + aliases, Suffixes: suffixes}
		item.CodeAvailable, item.CodePublicAvailable = int64Ptr(item.TotalAvailable), int64Ptr(item.TotalAvailable)
		item.PurchaseAvailable, item.PurchasePublicAvailable = int64Ptr(item.TotalAvailable), int64Ptr(item.TotalAvailable)
		items = append(items, item)
	}
	slog.InfoContext(ctx, "Microsoft inventory projected", "project_id", projectID, "generation", snapshot.Generation, "source_started_at", snapshot.StartedAt, "source_completed_at", snapshot.ComputedAt, "main_pool", eligible, "main_used", eligible-main, "alias_pool", aliasPool, "alias_used", aliasPool-aliases, "main_available", main, "alias_available", aliases, "refresh_pending", refreshErr != nil)
	return stats, items, snapshot.StartedAt, refreshErr
}

func (r *Repo) previousMicrosoftInventory(ctx context.Context, projectID uint, rows []productInventoryRow) (*allocapp.InventoryStats, *allocapp.ProjectProductInventoryTotals, error) {
	data, err := r.redis.MGet(ctx, inventoryCacheKey(allocapp.InventoryCacheStats, projectID), inventoryCacheKey(allocapp.InventoryCacheProducts, projectID)).Result()
	if err != nil {
		return nil, nil, err
	}
	var stats *allocapp.InventoryStats
	var products *allocapp.ProjectProductInventoryTotals
	var decodeErr error
	if data[0] != nil {
		var old allocapp.InventoryStats
		if err := json.Unmarshal([]byte(fmt.Sprint(data[0])), &old); err != nil {
			decodeErr = err
		} else if !old.Cold && old.Microsoft.Enabled && usableInventoryObservation(old.SourceRefreshedAt[coredomain.ProductTypeMicrosoft]) {
			stats = &old
		}
	}
	if data[1] != nil {
		var old allocapp.ProjectProductInventoryTotals
		if err := json.Unmarshal([]byte(fmt.Sprint(data[1])), &old); err != nil {
			decodeErr = errors.Join(decodeErr, err)
		} else {
			observed := old.SourceRefreshedAt[coredomain.ProductTypeMicrosoft]
			if observed.IsZero() && old.RefreshedAt != nil {
				observed = *old.RefreshedAt
			}
			if !old.Cold && usableInventoryObservation(observed) {
				var items []allocapp.ProductInventoryTotal
				for _, row := range rows {
					for _, item := range old.Items {
						if item.ProductID == row.ProductID && item.ProductType == coredomain.ProductTypeMicrosoft {
							items = append(items, item)
						}
					}
				}
				if len(items) == len(rows) {
					old.Items = items
					old.SourceRefreshedAt = map[coredomain.ProductType]time.Time{coredomain.ProductTypeMicrosoft: observed}
					products = &old
				}
			}
		}
	}
	return stats, products, decodeErr
}

func usableInventoryObservation(observed time.Time) bool {
	age := time.Since(observed)
	return !observed.IsZero() && age >= 0 && age <= allocapp.InventorySnapshotMaxAge
}

func inventoryRefreshErrors(values ...error) error {
	var result error
	deferred := false
	for _, err := range values {
		if errors.Is(err, platform.ErrBackgroundExecutionDeferred) {
			deferred = true
		} else {
			result = errors.Join(result, err)
		}
	}
	if result == nil && deferred {
		return platform.ErrBackgroundExecutionDeferred
	}
	return result
}

// WarmFastInventoryBase advances a fixed number of pages; all work runs behind the background CPU gate.
func (r *Repo) WarmFastInventoryBase(ctx context.Context) error {
	if r.redis == nil {
		return nil
	}
	// A quiet project schedule does not require rebuilding an otherwise usable shared pool.
	building, err := r.redis.Exists(ctx, microsoftSnapshotKey+":build").Result()
	if err != nil {
		return err
	}
	if building == 0 {
		probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		var needed bool
		err := r.dbFor(probeCtx).Raw(`SELECT EXISTS(SELECT 1 FROM projects p JOIN project_products pp ON pp.project_id=p.id WHERE p.status='listed' AND pp.status='enabled' AND pp.type='microsoft' AND pp.main_weight>0 AND pp.dot_weight=0 AND pp.plus_weight=0)`).Scan(&needed).Error
		cancel()
		if err != nil {
			return err
		}
		if !needed {
			return nil
		}
		cached, err := r.redis.Exists(ctx, microsoftSnapshotKey).Result()
		if err != nil {
			return err
		}
		if cached > 0 {
			due, err := r.redis.ZCount(ctx, inventoryCacheScheduleKey, "-inf", fmt.Sprint(time.Now().UnixMilli())).Result()
			if err != nil {
				return err
			}
			if due == 0 {
				request, err := r.inventorySnapshotRequest(ctx, microsoftSnapshotKey)
				if err != nil {
					return err
				}
				if request == "" {
					return nil
				}
				data, err := r.redis.Get(ctx, microsoftSnapshotKey).Bytes()
				if err != nil {
					return err
				}
				var snapshot microsoftInventorySnapshot
				if json.Unmarshal(data, &snapshot) == nil && snapshot.RequestID == request {
					return nil
				}
			}
		}
	}
	started := time.Now()
	for step := 0; step < 16; step++ {
		_, err := r.sharedMicrosoftInventory(ctx)
		if !errors.Is(err, platform.ErrBackgroundExecutionDeferred) {
			return err
		}
		if time.Since(started) >= 4*time.Second {
			return platform.ErrBackgroundExecutionDeferred
		}
		if step < 15 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(250 * time.Millisecond):
			}
		}
	}
	return platform.ErrBackgroundExecutionDeferred
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

func int64Ptr(value int64) *int64 { return &value }

// Manual refreshes request a new generation without deleting the last usable value or restarting live pages.
func (r *Repo) RequestInventorySnapshotRebuild(ctx context.Context, projectIDs []uint) error {
	if r.redis == nil {
		return nil
	}
	token := platform.NewUUIDV7String()
	_, err := r.redis.Pipelined(ctx, func(pipe redis.Pipeliner) error {
		pipe.Set(ctx, microsoftSnapshotKey+":request", token, allocapp.InventorySnapshotMaxAge)
		pipe.Set(ctx, iCloudInventorySnapshotKey+":request", token, allocapp.InventorySnapshotMaxAge)
		for _, id := range projectIDs {
			if id > 0 {
				pipe.Set(ctx, fmt.Sprintf("%sother:%d:request", inventoryCacheKeyPrefix, id), token, allocapp.InventorySnapshotMaxAge)
			}
		}
		return nil
	})
	return err
}
func (r *Repo) inventorySnapshotRequest(ctx context.Context, key string) (string, error) {
	token, err := r.redis.Get(ctx, key+":request").Result()
	if err == redis.Nil {
		return "", nil
	}
	return token, err
}
