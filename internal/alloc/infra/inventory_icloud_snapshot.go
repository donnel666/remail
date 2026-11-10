package infra

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	allocapp "github.com/donnel666/remail/internal/alloc/app"
	"github.com/donnel666/remail/internal/platform"
	"github.com/redis/go-redis/v9"
)

const (
	iCloudInventorySnapshotKey = inventoryCacheKeyPrefix + "shared:icloud"
	iCloudInventoryPageSize    = 5000
)

type iCloudInventorySnapshot struct {
	ComputedAt        time.Time      `json:"computedAt"`
	Forwarding        string         `json:"forwarding"`
	EligibleResources int64          `json:"eligibleResources"`
	Aliases           int64          `json:"aliases"`
	Used              map[uint]int64 `json:"used"`
	Active            map[uint]int64 `json:"active"`
}

type iCloudInventoryBuild struct {
	iCloudInventorySnapshot
	UpdatedAt     time.Time
	MaxAliasID    uint
	MaxHistoryID  uint
	AliasCursor   uint
	HistoryCursor uint
	History       bool
}

// All projects share one bounded scan; history includes released allocations too.
func (r *Repo) sharedICloudInventory(ctx context.Context) (*iCloudInventorySnapshot, error) {
	condition, args := iCloudForwardingDomainCondition("ia.forward_to_email")
	config, _ := json.Marshal(args)
	forwarding := string(config)
	var cached iCloudInventorySnapshot
	payload, err := r.redis.Get(ctx, iCloudInventorySnapshotKey).Bytes()
	if err != nil && err != redis.Nil {
		return nil, err
	}
	usable := err == nil && json.Unmarshal(payload, &cached) == nil && cached.Forwarding == forwarding &&
		time.Since(cached.ComputedAt) >= 0 && time.Since(cached.ComputedAt) <= allocapp.InventorySnapshotMaxAge
	if usable && time.Since(cached.ComputedAt) < time.Hour {
		return &cached, nil
	}
	for step := 0; step < 8; step++ {
		updated, err := r.advanceICloudInventory(ctx, condition, args, forwarding)
		if err == nil {
			return updated, nil
		}
		if usable {
			// Serving the old value must not cancel the worker's rebuild continuation.
			return &cached, err
		}
		if !errors.Is(err, platform.ErrBackgroundExecutionDeferred) {
			return nil, err
		}
		if step < 7 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(250 * time.Millisecond):
			}
		}
	}
	return nil, platform.ErrBackgroundExecutionDeferred
}

func (r *Repo) advanceICloudInventory(ctx context.Context, forwardingCondition string, forwardingArgs []any, forwarding string) (*iCloudInventorySnapshot, error) {
	allowed, err := r.redis.SetNX(ctx, iCloudInventorySnapshotKey+":budget", "1", 250*time.Millisecond).Result()
	if err != nil {
		return nil, err
	}
	if !allowed {
		return nil, platform.ErrBackgroundExecutionDeferred
	}
	lock, stateKey := iCloudInventorySnapshotKey+":lock", iCloudInventorySnapshotKey+":build"
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
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	var completed iCloudInventorySnapshot
	data, err := r.redis.Get(ctx, iCloudInventorySnapshotKey).Bytes()
	if err != nil && err != redis.Nil {
		return nil, err
	}
	if err == nil && json.Unmarshal(data, &completed) == nil && completed.Forwarding == forwarding &&
		time.Since(completed.ComputedAt) >= 0 && time.Since(completed.ComputedAt) < time.Hour {
		return &completed, nil
	}
	var state iCloudInventoryBuild
	data, err = r.redis.Get(ctx, stateKey).Bytes()
	if err != nil && err != redis.Nil {
		return nil, err
	}
	if err == nil {
		if err := json.Unmarshal(data, &state); err != nil {
			return nil, fmt.Errorf("decode iCloud inventory build: %w", err)
		}
	}
	if state.ComputedAt.IsZero() || time.Since(state.ComputedAt) < 0 || state.Forwarding != forwarding ||
		(!state.UpdatedAt.IsZero() && time.Since(state.UpdatedAt) > 24*time.Hour) {
		state = iCloudInventoryBuild{iCloudInventorySnapshot: iCloudInventorySnapshot{
			ComputedAt: time.Now().UTC(), Forwarding: forwarding, Used: map[uint]int64{}, Active: map[uint]int64{},
		}}
		if err := r.dbFor(ctx).Raw(`SELECT COALESCE(MAX(id),0) FROM icloud_aliases`).Scan(&state.MaxAliasID).Error; err != nil {
			return nil, err
		}
		if err := r.dbFor(ctx).Raw(`SELECT COALESCE(MAX(id),0) FROM icloud_allocations`).Scan(&state.MaxHistoryID).Error; err != nil {
			return nil, err
		}
		if err := r.dbFor(ctx).Raw(`SELECT COUNT(*) FROM icloud_resources ir
JOIN email_resources er ON er.id=ir.id AND er.type='icloud'
WHERE ir.status='normal' AND ir.for_sale=TRUE`).Scan(&state.EligibleResources).Error; err != nil {
			return nil, err
		}
	}
	table, cursor, upper := "icloud_aliases", state.AliasCursor, state.MaxAliasID
	if state.History {
		table, cursor, upper = "icloud_allocations", state.HistoryCursor, state.MaxHistoryID
	}
	var ids []uint
	if err := r.dbFor(ctx).Table(table).Where("id > ? AND id <= ?", cursor, upper).
		Order("id").Limit(iCloudInventoryPageSize).Pluck("id", &ids).Error; err != nil {
		return nil, fmt.Errorf("page iCloud inventory: %w", err)
	}
	complete := state.History && len(ids) == 0
	if len(ids) > 0 {
		end := ids[len(ids)-1]
		aliasTable, historyTable, join := "icloud_aliases ia", "icloud_allocations history", "JOIN "
		if r.dbFor(ctx).Name() == "mysql" {
			aliasTable += " FORCE INDEX (PRIMARY)"
			historyTable += " FORCE INDEX (PRIMARY)"
			join = "STRAIGHT_JOIN "
		}
		if !state.History {
			var count int64
			query := r.dbFor(ctx).Table(aliasTable).
				Joins(join+"icloud_resources ir ON ir.id=ia.resource_id").
				Joins(join+"email_resources er ON er.id=ir.id AND er.type='icloud'").
				Where("ia.id > ? AND ia.id <= ? AND ia.status='normal' AND ir.status='normal' AND ir.for_sale=TRUE", cursor, end).
				Where(forwardingCondition, forwardingArgs...)
			if err := query.Count(&count).Error; err != nil {
				return nil, fmt.Errorf("count iCloud inventory page: %w", err)
			}
			state.Aliases += count
			state.AliasCursor = end
		} else {
			var counts []struct {
				ProjectID    uint
				Used, Active int64
			}
			// Active orders remain active when their resource is private, disabled, or removed.
			eligible := "ia.id <= ? AND ia.status='normal' AND ir.status='normal' AND ir.for_sale=TRUE AND er.id IS NOT NULL AND (" + forwardingCondition + ")"
			selectArgs := append([]any{state.MaxAliasID}, forwardingArgs...)
			query := r.dbFor(ctx).Table(historyTable).
				Joins("LEFT JOIN icloud_aliases ia ON ia.id=history.alias_id").
				Joins("LEFT JOIN icloud_resources ir ON ir.id=ia.resource_id").
				Joins("LEFT JOIN email_resources er ON er.id=ir.id AND er.type='icloud'").
				Where("history.id > ? AND history.id <= ?", cursor, end).Group("history.project_id").
				Select("history.project_id, SUM(CASE WHEN "+eligible+" THEN 1 ELSE 0 END) AS used, SUM(CASE WHEN history.status='allocated' THEN 1 ELSE 0 END) AS active", selectArgs...)
			if err := query.Scan(&counts).Error; err != nil {
				return nil, fmt.Errorf("count iCloud history page: %w", err)
			}
			for _, item := range counts {
				state.Used[item.ProjectID] += item.Used
				state.Active[item.ProjectID] += item.Active
			}
			state.HistoryCursor = end
		}
	} else if !state.History {
		state.History = true
	}
	state.UpdatedAt = time.Now().UTC()
	var value any = state
	if complete {
		state.ComputedAt = state.UpdatedAt
		value = state.iCloudInventorySnapshot
	}
	payload, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	written, err := storeICloudInventoryBuildScript.Run(ctx, r.redis, []string{lock, stateKey, iCloudInventorySnapshotKey},
		token, payload, int64(allocapp.InventorySnapshotMaxAge/time.Second), complete).Int()
	if err != nil {
		return nil, err
	}
	if written == 0 || !complete {
		return nil, platform.ErrBackgroundExecutionDeferred
	}
	return &state.iCloudInventorySnapshot, nil
}

var storeICloudInventoryBuildScript = redis.NewScript(`
if redis.call('GET', KEYS[1]) ~= ARGV[1] then return 0 end
if ARGV[4] == '1' then
  redis.call('SET', KEYS[3], ARGV[2], 'EX', ARGV[3])
  redis.call('DEL', KEYS[2])
else
  redis.call('SET', KEYS[2], ARGV[2], 'EX', 86400)
end
return 1
`)
