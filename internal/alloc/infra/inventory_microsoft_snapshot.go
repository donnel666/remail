package infra

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"time"

	allocapp "github.com/donnel666/remail/internal/alloc/app"
	"github.com/donnel666/remail/internal/platform"
	mysqlDriver "github.com/go-sql-driver/mysql"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

const microsoftSnapshotKey = inventoryCacheKeyPrefix + "shared:microsoft"
const microsoftSnapshotPageSize = 5000

// shortcut: a single resource supports at most 20,000 project usages; add a history subcursor beyond that scale.
const microsoftSnapshotUsageLimit = 20000

var errInventoryPageTooLarge = errors.New("inventory page has too many project usages")

type microsoftSnapshotUsage struct {
	Main    map[string]int64 `json:"main"`
	Aliases map[string]int64 `json:"aliases"`
}

type microsoftInventorySnapshot struct {
	ResourceUpper         uint                             `json:"resourceUpper"`
	AliasUpper            uint                             `json:"aliasUpper"`
	ActiveAllocationUpper uint                             `json:"activeAllocationUpper"`
	RequestID             string                           `json:"requestId,omitempty"`
	Generation            string                           `json:"generation"`
	StartedAt             time.Time                        `json:"startedAt"`
	ComputedAt            time.Time                        `json:"computedAt"`
	Main                  map[string]int64                 `json:"main"`
	Aliases               map[string]int64                 `json:"aliases"`
	Used                  map[uint]*microsoftSnapshotUsage `json:"used"`
	Active                map[uint]int64                   `json:"active"`
}

type microsoftSnapshotBuild struct {
	microsoftInventorySnapshot
	Phase     int       `json:"phase"`
	Cursor    uint      `json:"cursor"`
	Upper     uint      `json:"upper"`
	PageSize  int       `json:"pageSize"`
	FastPages int       `json:"fastPages"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type microsoftSnapshotResource struct {
	ID     uint
	Suffix string
}
type microsoftSnapshotUse struct {
	ID        uint
	ProjectID uint
}

// Each bounded read transaction counts a pool and its history from exactly the same resource IDs.
func (r *Repo) readMicrosoftSnapshotPage(ctx context.Context, state *microsoftSnapshotBuild) (uint, int, []microsoftSnapshotResource, []microsoftSnapshotUse, map[uint]int64, error) {
	var end uint
	var size int
	var eligible []microsoftSnapshotResource
	var used []microsoftSnapshotUse
	active := make(map[uint]int64)
	table := []string{"microsoft_resources", "explicit_aliases", "microsoft_allocations"}[state.Phase]
	err := r.dbFor(ctx).Transaction(func(db *gorm.DB) error {
		var ids []uint
		if err := db.Table(table).Where("id > ? AND id <= ?", state.Cursor, state.Upper).Order("id").Limit(state.PageSize).Pluck("id", &ids).Error; err != nil {
			return err
		}
		size = len(ids)
		if size == 0 {
			return nil
		}
		end = ids[size-1]
		if state.Phase == 2 {
			var rows []struct {
				ProjectID uint
				Count     int64
			}
			activeTable := "microsoft_allocations"
			if db.Name() == "mysql" {
				activeTable += " FORCE INDEX(PRIMARY)"
			}
			if err := db.Table(activeTable).Select("/*+ MAX_EXECUTION_TIME(1500) */ project_id, COUNT(*) AS count").Where("id > ? AND id <= ? AND status = 'allocated'", state.Cursor, end).Group("project_id").Scan(&rows).Error; err != nil {
				return err
			}
			for _, row := range rows {
				active[row.ProjectID] = row.Count
			}
			return nil
		}
		public, _ := microsoftInventoryScopeSQL()
		driver, join := "microsoft_resources ms", "JOIN "
		if state.Phase == 1 {
			driver = "explicit_aliases ea"
		}
		if db.Name() == "mysql" {
			driver += " FORCE INDEX(PRIMARY)"
			join = "STRAIGHT_JOIN "
		}
		query := db.Table(driver)
		if state.Phase == 0 {
			query = query.Select("/*+ MAX_EXECUTION_TIME(1500) */ ms.id, ms.email_domain AS suffix").Where("ms.id > ? AND ms.id <= ? AND ms.email_domain NOT LIKE '%.onmicrosoft.com'", state.Cursor, end)
		} else {
			query = query.Select("/*+ MAX_EXECUTION_TIME(1500) */ ea.id, ea.email_domain AS suffix").Joins(join+"microsoft_resources ms ON ms.id=ea.resource_id").Where("ea.id > ? AND ea.id <= ? AND ea.status='normal' AND ms.email_domain NOT LIKE '%.onmicrosoft.com'", state.Cursor, end)
		}
		if err := query.Joins(join + "email_resources er ON er.id=ms.id AND er.type='microsoft'").Joins(join + "users u ON u.id=er.owner_user_id").Where("ms.status='normal'").Where(microsoftNotUnderBlockingMaintenanceCondition).Where(public).Scan(&eligible).Error; err != nil {
			return err
		}
		if len(eligible) == 0 {
			return nil
		}
		goodIDs := make([]uint, len(eligible))
		for i, row := range eligible {
			goodIDs[i] = row.ID
		}
		field, mailbox, index := "resource_id", "main", "idx_ms_alloc_resource_project_mailbox"
		if state.Phase == 1 {
			field, mailbox, index = "explicit_alias_id", "alias", "idx_ms_alloc_explicit_project_mailbox"
		}
		history := "microsoft_allocations"
		if db.Name() == "mysql" {
			history += " FORCE INDEX(" + index + ")"
		}
		// UNION removes repeat allocations and overlap with project restrictions without an approximate counter.
		sqlQuery := "SELECT /*+ MAX_EXECUTION_TIME(1500) */ " + field + " AS id, project_id FROM " + history + " WHERE " + field + " IN ? AND mailbox=? UNION "
		if state.Phase == 0 {
			sqlQuery += "SELECT resource_id AS id,project_id FROM microsoft_resource_project_matches WHERE resource_id IN ?"
		} else {
			aliasTable := "explicit_aliases ea"
			if db.Name() == "mysql" {
				aliasTable += " FORCE INDEX(PRIMARY)"
			}
			sqlQuery += "SELECT ea.id, m.project_id FROM " + aliasTable + " " + join + "microsoft_resource_project_matches m ON m.resource_id=ea.resource_id WHERE ea.id IN ?"
		}
		sqlQuery += " LIMIT ?"
		if err := db.Raw(sqlQuery, goodIDs, mailbox, goodIDs, microsoftSnapshotUsageLimit+1).Scan(&used).Error; err != nil {
			return err
		}
		if len(used) > microsoftSnapshotUsageLimit {
			return errInventoryPageTooLarge
		}
		return nil
	}, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	return end, size, eligible, used, active, err
}

func (r *Repo) sharedMicrosoftInventory(ctx context.Context) (*microsoftInventorySnapshot, error) {
	var cached microsoftInventorySnapshot
	data, err := r.redis.Get(ctx, microsoftSnapshotKey).Bytes()
	if err != nil && err != redis.Nil {
		return nil, err
	}
	request, err := r.inventorySnapshotRequest(ctx, microsoftSnapshotKey)
	if err != nil {
		return nil, err
	}
	usable := err == nil && json.Unmarshal(data, &cached) == nil && !cached.StartedAt.IsZero() && time.Since(cached.StartedAt) >= 0 && time.Since(cached.StartedAt) <= allocapp.InventorySnapshotMaxAge
	if usable && (request == "" || request == cached.RequestID) && time.Since(cached.ComputedAt) >= 0 && time.Since(cached.ComputedAt) < time.Hour {
		return &cached, nil
	}
	updated, err := r.advanceMicrosoftInventory(ctx)
	if updated != nil {
		return updated, err
	}
	if usable {
		return &cached, err
	}
	return nil, err
}

func (r *Repo) advanceMicrosoftInventory(ctx context.Context) (*microsoftInventorySnapshot, error) {
	admitted, err := r.redis.SetNX(ctx, microsoftSnapshotKey+":budget", "1", 250*time.Millisecond).Result()
	if err != nil {
		return nil, err
	}
	if !admitted {
		return nil, platform.ErrBackgroundExecutionDeferred
	}
	lock, key := microsoftSnapshotKey+":lock", microsoftSnapshotKey+":build"
	token := platform.NewUUIDV7String()
	acquired, err := r.redis.SetNX(ctx, lock, token, 10*time.Second).Result()
	if err != nil {
		return nil, err
	}
	if !acquired {
		return nil, platform.ErrBackgroundExecutionDeferred
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = inventoryCacheLockReleaseScript.Run(cleanup, r.redis, []string{lock}, token).Err()
	}()
	request, err := r.inventorySnapshotRequest(ctx, microsoftSnapshotKey)
	if err != nil {
		return nil, err
	}
	var completed microsoftInventorySnapshot
	payload, err := r.redis.Get(ctx, microsoftSnapshotKey).Bytes()
	if err != nil && err != redis.Nil {
		return nil, err
	}
	if err == nil && json.Unmarshal(payload, &completed) == nil && (request == "" || request == completed.RequestID) && !completed.StartedAt.IsZero() && time.Since(completed.StartedAt) >= 0 && time.Since(completed.StartedAt) <= allocapp.InventorySnapshotMaxAge && time.Since(completed.ComputedAt) >= 0 && time.Since(completed.ComputedAt) < time.Hour {
		return &completed, nil
	}
	var state microsoftSnapshotBuild
	payload, err = r.redis.Get(ctx, key).Bytes()
	if err != nil && err != redis.Nil {
		return nil, err
	}
	if err == nil {
		if err := json.Unmarshal(payload, &state); err != nil {
			return nil, fmt.Errorf("decode Microsoft inventory build: %w", err)
		}
	}
	if state.Generation == "" || time.Since(state.StartedAt) < 0 || time.Since(state.StartedAt) > allocapp.InventorySnapshotMaxAge || time.Since(state.UpdatedAt) > 24*time.Hour {
		state = microsoftSnapshotBuild{microsoftInventorySnapshot: microsoftInventorySnapshot{RequestID: request, Generation: platform.NewUUIDV7String(), StartedAt: time.Now().UTC(), Main: map[string]int64{}, Aliases: map[string]int64{}, Used: map[uint]*microsoftSnapshotUsage{}, Active: map[uint]int64{}}, PageSize: microsoftSnapshotPageSize}
		slog.InfoContext(ctx, "Microsoft inventory snapshot started", "generation", state.Generation)
	}
	if state.Phase < 0 || state.Phase > 2 || state.PageSize < 1 || state.PageSize > microsoftSnapshotPageSize {
		return nil, errors.New("invalid Microsoft inventory cursor")
	}
	pageCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if state.Upper == 0 {
		table := []string{"microsoft_resources", "explicit_aliases", "microsoft_allocations"}[state.Phase]
		if err := r.dbFor(pageCtx).Table(table).Select("COALESCE(MAX(id),0)").Scan(&state.Upper).Error; err != nil {
			return nil, err
		}
		switch state.Phase {
		case 0:
			state.ResourceUpper = state.Upper
		case 1:
			state.AliasUpper = state.Upper
		case 2:
			state.ActiveAllocationUpper = state.Upper
		}
	}
	readStarted := time.Now()
	end, size, eligible, used, active, err := r.readMicrosoftSnapshotPage(pageCtx, &state)
	if err != nil {
		var mysqlErr *mysqlDriver.MySQLError
		budgetExceeded := errors.Is(err, context.DeadlineExceeded) || errors.Is(err, errInventoryPageTooLarge) || (errors.As(err, &mysqlErr) && mysqlErr.Number == 3024)
		if budgetExceeded && (state.PageSize > 1 || !errors.Is(err, errInventoryPageTooLarge)) {
			state.FastPages = 0
			state.PageSize = max(1, state.PageSize/2)
			if _, saveErr := r.storeMicrosoftInventoryPage(ctx, lock, key, token, &state, false); saveErr != nil {
				return nil, saveErr
			}
			slog.WarnContext(ctx, "Microsoft inventory page budget exhausted", "generation", state.Generation, "phase", state.Phase, "cursor", state.Cursor, "page_size", state.PageSize, "error", err)
			return nil, platform.ErrBackgroundExecutionDeferred
		}
		return nil, fmt.Errorf("microsoft inventory phase %d cursor %d: %w", state.Phase, state.Cursor, err)
	}
	// Recover from transient overload without immediately regrowing a dense history page.
	if size > 0 && state.PageSize < microsoftSnapshotPageSize && time.Since(readStarted) < 250*time.Millisecond && len(used) < microsoftSnapshotUsageLimit/2 {
		state.FastPages++
		if state.FastPages >= 8 {
			state.PageSize = min(microsoftSnapshotPageSize, state.PageSize*2)
			state.FastPages = 0
		}
	} else {
		state.FastPages = 0
	}
	complete := size == 0 && state.Phase == 2
	if size == 0 && !complete {
		state.Phase++
		state.Cursor, state.Upper = 0, 0
		state.PageSize = microsoftSnapshotPageSize
		state.FastPages = 0
	} else {
		state.Cursor = end
	}
	if size > 0 && state.Phase < 2 {
		suffixes := make(map[uint]string, len(eligible))
		for _, row := range eligible {
			suffix := normalizeCandidateSuffix(row.Suffix)
			suffixes[row.ID] = suffix
			if state.Phase == 0 {
				state.Main[suffix]++
			} else {
				state.Aliases[suffix]++
			}
		}
		for _, row := range used {
			suffix, ok := suffixes[row.ID]
			if !ok {
				return nil, errors.New("inventory usage outside eligible resource page")
			}
			usage := state.Used[row.ProjectID]
			if usage == nil {
				usage = &microsoftSnapshotUsage{Main: map[string]int64{}, Aliases: map[string]int64{}}
				state.Used[row.ProjectID] = usage
			}
			if state.Phase == 0 {
				usage.Main[suffix]++
			} else {
				usage.Aliases[suffix]++
			}
		}
	}
	for project, count := range active {
		state.Active[project] += count
	}
	written, err := r.storeMicrosoftInventoryPage(ctx, lock, key, token, &state, complete)
	if err != nil {
		return nil, err
	}
	if !written || !complete {
		return nil, platform.ErrBackgroundExecutionDeferred
	}
	slog.InfoContext(ctx, "Microsoft inventory snapshot completed", "generation", state.Generation, "started_at", state.StartedAt, "main_pool", sumInventorySuffixes(state.Main), "alias_pool", sumInventorySuffixes(state.Aliases), "projects", len(state.Used), "duration", time.Since(state.StartedAt))
	return &state.microsoftInventorySnapshot, nil
}

func (r *Repo) storeMicrosoftInventoryPage(ctx context.Context, lock, key, token string, state *microsoftSnapshotBuild, complete bool) (bool, error) {
	state.UpdatedAt = time.Now().UTC()
	var value any = state
	if complete {
		state.ComputedAt = state.UpdatedAt
		value = state.microsoftInventorySnapshot
	}
	payload, err := json.Marshal(value)
	if err != nil {
		return false, err
	}
	n, err := storeICloudInventoryBuildScript.Run(ctx, r.redis, []string{lock, key, microsoftSnapshotKey}, token, payload, int64(allocapp.InventorySnapshotMaxAge/time.Second), complete).Int()
	return n == 1, err
}

func sumInventorySuffixes(values map[string]int64) int64 {
	var n int64
	for _, v := range values {
		n += v
	}
	return n
}

func (s *microsoftInventorySnapshot) available(project uint, blocked map[string]bool) ([]allocapp.ProductInventorySuffixTotal, int64, int64, error) {
	mainUsed, aliasUsed := map[string]int64{}, map[string]int64{}
	if use := s.Used[project]; use != nil {
		mainUsed, aliasUsed = use.Main, use.Aliases
	}
	for suffix, n := range mainUsed {
		if n < 0 || n > s.Main[suffix] {
			return nil, 0, 0, fmt.Errorf("invalid Microsoft snapshot %s project %d suffix %s: usage exceeds pool", s.Generation, project, suffix)
		}
	}
	for suffix, n := range aliasUsed {
		if n < 0 || n > s.Aliases[suffix] {
			return nil, 0, 0, fmt.Errorf("invalid Microsoft snapshot %s project %d suffix %s: usage exceeds pool", s.Generation, project, suffix)
		}
	}
	keys := map[string]bool{}
	for suffix := range s.Main {
		keys[suffix] = true
	}
	for suffix := range s.Aliases {
		keys[suffix] = true
	}
	var items []allocapp.ProductInventorySuffixTotal
	var main, aliases int64
	for suffix := range keys {
		if blocked[suffix] {
			continue
		}
		m, a := s.Main[suffix]-mainUsed[suffix], s.Aliases[suffix]-aliasUsed[suffix]
		if m < 0 || a < 0 {
			return nil, 0, 0, fmt.Errorf("invalid Microsoft snapshot %s project %d suffix %s: usage exceeds pool", s.Generation, project, suffix)
		}
		main += m
		aliases += a
		items = append(items, allocapp.ProductInventorySuffixTotal{Suffix: suffix, TotalAvailable: m + a, PublicAvailable: m + a})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Suffix < items[j].Suffix })
	return items, main, aliases, nil
}
