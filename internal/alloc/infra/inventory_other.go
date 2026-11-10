package infra

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	allocapp "github.com/donnel666/remail/internal/alloc/app"
	coredomain "github.com/donnel666/remail/internal/core/domain"
	"github.com/donnel666/remail/internal/platform"
	"github.com/redis/go-redis/v9"
)

type otherInventorySnapshot struct {
	Stats allocapp.InventoryStats
	Items []allocapp.ProductInventoryTotal
}

type otherInventoryGroup struct {
	Config      string
	ComputedAt  time.Time
	AttemptedAt time.Time
	Stats       *allocapp.InventoryStats
	Items       []allocapp.ProductInventoryTotal
	Error       string
}

func (r *Repo) otherInventory(ctx context.Context, projectID uint, products []productInventoryRow) (*otherInventorySnapshot, error) {
	groups := make(map[coredomain.ProductType][]productInventoryRow)
	var kinds []coredomain.ProductType
	for _, row := range products {
		kind := coredomain.ProductType(row.Type)
		if kind == coredomain.ProductTypeMicrosoft {
			continue
		}
		// Gmail and its variants share counters and must be measured together.
		if kind == coredomain.ProductTypeGmailVariant {
			kind = coredomain.ProductTypeGmail
		}
		if _, exists := groups[kind]; !exists {
			kinds = append(kinds, kind)
		}
		groups[kind] = append(groups[kind], row)
	}
	result := &otherInventorySnapshot{Stats: allocapp.InventoryStats{ProjectID: projectID,
		SourceRefreshedAt: make(map[coredomain.ProductType]time.Time)}}
	if len(groups) == 0 {
		return result, nil
	}

	// At most one worker runs exact provider queries; each provider has its own deadline.
	var lockErr error
	lockAttempted, locked := false, false
	lock, token := inventoryCacheKeyPrefix+"other:lock", platform.NewUUIDV7String()
	defer func() {
		if locked {
			cleanup, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_ = inventoryCacheLockReleaseScript.Run(cleanup, r.redis, []string{lock}, token).Err()
		}
	}()
	var refreshErr error
	deferred := false
	for _, kind := range kinds {
		rows := groups[kind]
		config, _ := json.Marshal(rows)
		key := fmt.Sprintf("%sother:%d:%s", inventoryCacheKeyPrefix, projectID, kind)
		var old otherInventoryGroup
		payload, readErr := r.redis.Get(ctx, key).Bytes()
		if readErr != nil && readErr != redis.Nil {
			return nil, readErr
		}
		if readErr == nil {
			_ = json.Unmarshal(payload, &old)
		}
		if old.Config != string(config) {
			old = otherInventoryGroup{Config: string(config)}
		}
		var groupErr error
		if kind == coredomain.ProductTypeICloud {
			snapshot, err := r.sharedICloudInventory(ctx)
			groupErr = err
			if snapshot != nil {
				available := max(0, snapshot.Aliases-snapshot.Used[projectID])
				stats := &allocapp.InventoryStats{ProjectID: projectID, TotalAvailable: available,
					ICloud: allocapp.ICloudInventoryStats{Enabled: true, EligibleResources: snapshot.EligibleResources,
						AliasAvailable: available, TotalAvailable: available}, ActiveICloudAllocations: snapshot.Active[projectID]}
				old.Stats, old.ComputedAt, old.Items = stats, snapshot.ComputedAt, nil
				for _, row := range rows {
					item, err := r.productInventoryItem(ctx, projectID, row, stats)
					if err != nil {
						return nil, err
					}
					old.Items = append(old.Items, item)
				}
			}
		} else if time.Since(old.AttemptedAt) >= 0 && time.Since(old.AttemptedAt) < time.Minute {
			if old.Error != "" {
				groupErr = errors.New(old.Error)
			}
		} else {
			if !lockAttempted {
				lockAttempted = true
				var admitted bool
				admitted, lockErr = r.redis.SetNX(ctx, inventoryCacheKeyPrefix+"other:budget", "1", 500*time.Millisecond).Result()
				if lockErr == nil && admitted {
					locked, lockErr = r.redis.SetNX(ctx, lock, token, 30*time.Second).Result()
				}
				if lockErr == nil && !locked {
					lockErr = platform.ErrBackgroundExecutionDeferred
				}
			}
			groupErr = lockErr
			if locked {
				countCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
				stats, err := r.inventoryStatsForProducts(countCtx, projectID, rows)
				var items []allocapp.ProductInventoryTotal
				if err == nil {
					for _, row := range rows {
						var item allocapp.ProductInventoryTotal
						item, err = r.productInventoryItem(countCtx, projectID, row, stats)
						if err != nil {
							break
						}
						items = append(items, item)
					}
				}
				cancel()
				groupErr = err
				old.AttemptedAt, old.Error = time.Now().UTC(), ""
				if err == nil {
					old.Stats, old.Items, old.ComputedAt = stats, items, old.AttemptedAt
				} else {
					old.Error = err.Error()
				}
				payload, marshalErr := json.Marshal(old)
				if marshalErr != nil {
					return nil, marshalErr
				}
				if err := storeOtherInventoryGroupScript.Run(ctx, r.redis, []string{lock, key}, token, payload,
					int64(allocapp.InventorySnapshotMaxAge/time.Second)).Err(); err != nil {
					return nil, err
				}
			}
		}
		if errors.Is(groupErr, platform.ErrBackgroundExecutionDeferred) {
			deferred = true
		} else if groupErr != nil {
			refreshErr = errors.Join(refreshErr, fmt.Errorf("%s inventory: %w", kind, groupErr))
		}
		if old.Stats == nil || time.Since(old.ComputedAt) < 0 || time.Since(old.ComputedAt) > allocapp.InventorySnapshotMaxAge {
			continue
		}
		mergeOtherInventoryStats(&result.Stats, old.Stats, kind)
		result.Items = append(result.Items, old.Items...)
		for _, row := range rows {
			result.Stats.SourceRefreshedAt[coredomain.ProductType(row.Type)] = old.ComputedAt
		}
	}
	if refreshErr == nil && deferred {
		refreshErr = platform.ErrBackgroundExecutionDeferred
	}
	result.Stats.RefreshPending = refreshErr != nil
	return result, refreshErr
}

func mergeOtherInventoryStats(result, source *allocapp.InventoryStats, kind coredomain.ProductType) {
	switch kind {
	case coredomain.ProductTypeMicrosoftEnterprise:
		result.MicrosoftEnterprise = source.MicrosoftEnterprise
	case coredomain.ProductTypeDomain:
		result.Domain, result.ActiveDomainAllocations = source.Domain, source.ActiveDomainAllocations
	case coredomain.ProductTypeGmail:
		result.Gmail, result.ActiveGmailAllocations = source.Gmail, source.ActiveGmailAllocations
	case coredomain.ProductTypeICloud:
		result.ICloud, result.ActiveICloudAllocations = source.ICloud, source.ActiveICloudAllocations
	case coredomain.ProductTypeProto:
		result.Proto, result.ActiveProtoAllocations = source.Proto, source.ActiveProtoAllocations
	case coredomain.ProductTypeDaum:
		result.Daum, result.ActiveDaumAllocations = source.Daum, source.ActiveDaumAllocations
	}
	result.TotalAvailable += source.TotalAvailable
}

var storeOtherInventoryGroupScript = redis.NewScript(`
if redis.call('GET', KEYS[1]) ~= ARGV[1] then return 0 end
redis.call('SET', KEYS[2], ARGV[2], 'EX', ARGV[3])
return 1
`)
