package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/fnv"
	"math/rand/v2"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/donnel666/remail/internal/alloc/domain"
)

const (
	inventoryCacheActivityTTL = 2 * InventoryRefreshInterval
	inventoryCacheHardTTL     = 24 * time.Hour
	inventoryRefreshLockTTL   = 10 * time.Minute
	// ponytail: five cold keys bound each aggregate burst; raise only when the
	// refresh p99 stays below one interval without affecting checkout/pickup.
	inventoryRefreshBatchSize = 5
)

var (
	errCandidateUnavailable = errors.New("allocation candidate unavailable")
	errResourceRootBusy     = errors.New("allocation resource root busy")
)

var pinyinMailboxNameParts = [...]string{
	"an", "ao", "bai", "bao", "bei", "bo", "cai", "chang", "chao", "chen",
	"cheng", "chun", "da", "dan", "de", "dong", "fan", "fang", "fei", "feng",
	"gang", "gao", "guang", "gui", "guo", "hai", "han", "hao", "he", "heng",
	"hong", "hua", "huan", "hui", "ji", "jia", "jian", "jiang", "jie", "jin",
	"jing", "jun", "kai", "kang", "ke", "lan", "lei", "li", "lian", "liang",
	"lin", "ling", "long", "lu", "man", "mei", "meng", "min", "ming", "nan",
	"ning", "peng", "ping", "qi", "qian", "qiang", "qiao", "qin", "qing", "quan",
	"ren", "rong", "rui", "shan", "sheng", "shi", "shu", "shuang", "song", "tao",
	"tian", "tong", "wan", "wei", "wen", "xi", "xia", "xian", "xiang", "xiao",
	"xin", "xing", "xiu", "xuan", "ya", "yan", "yang", "yao", "yi", "ying",
	"yong", "you", "yu", "yuan", "yun", "zhen", "zhi", "zhong", "zhou", "zhu",
}

type AllocateCommand struct {
	OrderNo          string
	BuyerUserID      uint
	ProjectProductID uint
	SupplyScope      domain.SupplyScope
	SupplyScopes     []domain.SupplyScope
	EmailSuffix      string
	// FulfillExistingOrder is set only by Trade after an order is persisted.
	// A delisted product cannot receive new orders, but it must remain
	// allocatable for an already accepted order.
	FulfillExistingOrder bool
	ensureOrderGuard     func(context.Context, domain.AllocationType) error
	lockResourceRoot     func(context.Context, uint, domain.AllocationType) (bool, error)
}

type UseCase struct {
	repo                       Repository
	queue                      CandidateRefreshQueue
	adminAllocationEnrichment  AdminAllocationEnrichmentPort
	historicalMicrosoftAliases HistoricalMicrosoftAliasPort
	inventoryCache             InventoryCache
}

func (uc *UseCase) SetInventoryCache(cache InventoryCache) {
	if uc != nil {
		uc.inventoryCache = cache
	}
}

func (uc *UseCase) SetHistoricalMicrosoftAliasPort(port HistoricalMicrosoftAliasPort) {
	if uc != nil {
		uc.historicalMicrosoftAliases = port
	}
}

func (uc *UseCase) SetAdminAllocationEnrichmentPort(port AdminAllocationEnrichmentPort) {
	if uc != nil {
		uc.adminAllocationEnrichment = port
	}
}

func NewUseCase(repo Repository, queues ...CandidateRefreshQueue) *UseCase {
	var queue CandidateRefreshQueue
	if len(queues) > 0 {
		queue = queues[0]
	}
	return &UseCase{
		repo:  repo,
		queue: queue,
	}
}

func (uc *UseCase) Allocate(ctx context.Context, cmd AllocateCommand) (*domain.UnifiedAllocation, error) {
	cmd.OrderNo = strings.TrimSpace(cmd.OrderNo)
	scopes := normalizedSupplyScopes(cmd)
	cmd.EmailSuffix = normalizeEmailSuffix(cmd.EmailSuffix)
	if cmd.OrderNo == "" || cmd.BuyerUserID == 0 || cmd.ProjectProductID == 0 {
		return nil, domain.ErrInvalidAllocationRequest
	}

	var result *domain.UnifiedAllocation
	var err error
	attempts := candidateRetryCount
	if uc.repo.HasParentTx(ctx) {
		// A nested retry would keep the parent wallet/resource locks and sleep in
		// the same transaction. Let the complete order transaction roll back first.
		attempts = 1
	}
	for attempt := 0; attempt < attempts; attempt++ {
		result = nil
		err = uc.repo.WithTx(ctx, func(txCtx context.Context) error {
			existing, err := uc.repo.FindExistingAllocation(txCtx, cmd.OrderNo)
			if err != nil {
				return err
			}
			if existing != nil {
				result = existing
				return nil
			}

			config, err := uc.repo.LoadProductConfig(txCtx, cmd.ProjectProductID, cmd.BuyerUserID, cmd.FulfillExistingOrder)
			if err != nil {
				return err
			}
			if config == nil {
				return domain.ErrProjectNotAllocatable
			}
			// Create the guard only after a candidate is locked. Rolling back an
			// empty owned-scope guard retained the right-edge supremum lock in MySQL.
			guardCreated := false
			cmd.ensureOrderGuard = func(guardCtx context.Context, allocationType domain.AllocationType) error {
				if guardCreated {
					return nil
				}
				if err := uc.repo.CreateOrderGuard(guardCtx, cmd.OrderNo, allocationType); err != nil {
					return err
				}
				guardCreated = true
				return nil
			}
			type resourceRootKey struct {
				id             uint
				allocationType domain.AllocationType
			}
			lockedRoots := make(map[resourceRootKey]struct{})
			cmd.lockResourceRoot = func(lockCtx context.Context, resourceID uint, allocationType domain.AllocationType) (bool, error) {
				key := resourceRootKey{id: resourceID, allocationType: allocationType}
				if _, locked := lockedRoots[key]; locked {
					return true, nil
				}
				// A request-level batch may already hold earlier resource roots. Never
				// wait for another root in that state; SKIP LOCKED keeps the shared
				// wallet -> resource lock order acyclic.
				if len(lockedRoots) > 0 {
					locked, err := uc.repo.TryLockResourceRoot(lockCtx, resourceID, allocationType)
					if locked {
						lockedRoots[key] = struct{}{}
					}
					return locked, err
				}
				locked, err := uc.repo.LockResourceRoot(lockCtx, resourceID, allocationType)
				if locked {
					lockedRoots[key] = struct{}{}
				}
				return locked, err
			}
			for _, scope := range scopes {
				attemptCmd := cmd
				attemptCmd.SupplyScope = scope
				switch config.ProductType {
				case domain.AllocationTypeMicrosoft:
					result, err = uc.allocateMicrosoft(txCtx, attemptCmd, *config)
				case domain.AllocationTypeDomain:
					result, err = uc.allocateDomain(txCtx, attemptCmd, *config)
				default:
					return domain.ErrProjectNotAllocatable
				}
				if err == nil {
					return nil
				}
				if !errors.Is(err, domain.ErrInsufficientInventory) {
					return err
				}
			}
			return domain.ErrInsufficientInventory
		})
		if err == nil || (!errors.Is(err, domain.ErrInsufficientInventory) && !errors.Is(err, domain.ErrAllocationConflict)) {
			break
		}
		if attempt < attempts-1 {
			time.Sleep(candidateRetryDelay)
		}
	}
	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, domain.ErrInsufficientInventory
	}
	return result, nil
}

func normalizedSupplyScopes(cmd AllocateCommand) []domain.SupplyScope {
	if len(cmd.SupplyScopes) == 0 {
		return []domain.SupplyScope{domain.NormalizeSupplyScope(cmd.SupplyScope)}
	}
	scopes := make([]domain.SupplyScope, len(cmd.SupplyScopes))
	for i, scope := range cmd.SupplyScopes {
		scopes[i] = domain.NormalizeSupplyScope(scope)
	}
	return scopes
}

func (uc *UseCase) ImportHistoricalMicrosoftAllocation(ctx context.Context, cmd HistoricalMicrosoftAllocationCommand) (*domain.UnifiedAllocation, error) {
	cmd.Email = strings.ToLower(strings.TrimSpace(cmd.Email))
	cmd.CreatedAt = cmd.CreatedAt.UTC()
	cmd.ReleasedAt = cmd.ReleasedAt.UTC()
	if uc == nil || uc.repo == nil || cmd.ProjectID == 0 || cmd.ProductID == 0 ||
		cmd.ResourceID == 0 || cmd.Email == "" || !domain.IsValidMicrosoftMailbox(cmd.Mailbox) ||
		cmd.CreatedAt.IsZero() || cmd.ReleasedAt.IsZero() || cmd.ReleasedAt.Before(cmd.CreatedAt) ||
		(cmd.Mailbox == domain.MicrosoftMailboxAlias && uc.historicalMicrosoftAliases == nil) {
		return nil, domain.ErrInvalidAllocationRequest
	}
	var result *domain.UnifiedAllocation
	err := uc.repo.WithTx(ctx, func(txCtx context.Context) error {
		lockedRoot, err := uc.repo.LockResourceRoot(txCtx, cmd.ResourceID, domain.AllocationTypeMicrosoft)
		if err != nil {
			return err
		}
		if !lockedRoot {
			return domain.ErrInvalidAllocationRequest
		}
		var explicitAliasID, dotAliasID, plusAliasID *uint
		mailboxID := cmd.ResourceID
		switch cmd.Mailbox {
		case domain.MicrosoftMailboxMain:
		case domain.MicrosoftMailboxAlias:
			alias, err := uc.repo.FindExplicitAlias(txCtx, cmd.ResourceID, cmd.Email)
			if err != nil {
				return err
			}
			if alias == nil {
				if cmd.AliasOwnerID == 0 {
					return domain.ErrHistoricalAllocationOwnerRequired
				}
				if err := uc.historicalMicrosoftAliases.BackfillExistingAliases(txCtx, cmd.ResourceID, []string{cmd.Email}); err != nil {
					return err
				}
				alias, err = uc.repo.FindExplicitAlias(txCtx, cmd.ResourceID, cmd.Email)
				if err != nil {
					return err
				}
			}
			if alias == nil || alias.ID == 0 {
				return domain.ErrInvalidAllocationRequest
			}
			explicitAliasID = &alias.ID
			mailboxID = alias.ID
		case domain.MicrosoftMailboxDot:
			alias, err := uc.repo.FindOrCreateDotAlias(txCtx, cmd.ResourceID, cmd.Email)
			if err != nil {
				return err
			}
			if alias == nil || alias.ID == 0 {
				return domain.ErrInvalidAllocationRequest
			}
			dotAliasID = &alias.ID
			mailboxID = alias.ID
		case domain.MicrosoftMailboxPlus:
			alias, err := uc.repo.FindOrCreatePlusAlias(txCtx, cmd.ResourceID, cmd.Email)
			if err != nil {
				return err
			}
			if alias == nil || alias.ID == 0 {
				return domain.ErrInvalidAllocationRequest
			}
			plusAliasID = &alias.ID
			mailboxID = alias.ID
		}
		matched, err := uc.repo.IsMicrosoftMailboxHistoricallyMatched(txCtx, cmd.ProjectID, cmd.Mailbox, mailboxID)
		if err != nil {
			return err
		}
		if matched {
			return nil
		}
		if cmd.Mailbox == domain.MicrosoftMailboxAlias && cmd.AliasOwnerID == 0 {
			return domain.ErrHistoricalAllocationOwnerRequired
		}
		orderNo := historicalMicrosoftAllocationOrderNo(cmd, mailboxID)
		existing, err := uc.repo.FindExistingAllocation(txCtx, orderNo)
		if err != nil {
			return err
		}
		if existing != nil {
			if !sameHistoricalMicrosoftAllocation(*existing, orderNo, cmd) {
				return domain.ErrAllocationConflict
			}
			result = existing
			return nil
		}
		if err := uc.repo.CreateOrderGuard(txCtx, orderNo, domain.AllocationTypeMicrosoft); err != nil {
			return err
		}
		releasedAt := cmd.ReleasedAt
		allocation := &domain.MicrosoftAllocation{
			OrderNo: orderNo, ProjectID: cmd.ProjectID, ProductID: cmd.ProductID,
			ResourceID: cmd.ResourceID, SupplyScope: domain.SupplyScopePublic,
			Mailbox: cmd.Mailbox, ExplicitAliasID: explicitAliasID, DotAliasID: dotAliasID, PlusAliasID: plusAliasID,
			Email: cmd.Email, Status: domain.AllocationStatusReleased,
			CreatedAt: cmd.CreatedAt, ReleasedAt: &releasedAt,
		}
		if err := uc.repo.CreateMicrosoftAllocation(txCtx, allocation); err != nil {
			return err
		}
		unified := domain.UnifiedAllocation{
			Type: domain.AllocationTypeMicrosoft, ID: allocation.ID, OrderNo: allocation.OrderNo,
			ProjectID: allocation.ProjectID, ProductID: allocation.ProductID, ResourceID: allocation.ResourceID,
			SupplyScope: allocation.SupplyScope, Mailbox: string(allocation.Mailbox), Email: allocation.Email,
			Status: allocation.Status, CreatedAt: allocation.CreatedAt, ReleasedAt: allocation.ReleasedAt,
		}
		result = &unified
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func historicalMicrosoftAllocationOrderNo(cmd HistoricalMicrosoftAllocationCommand, mailboxID uint) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d:%d:%s:%d", cmd.ResourceID, cmd.ProjectID, cmd.Mailbox, mailboxID)))
	return "HIST-" + hex.EncodeToString(sum[:20])
}

func sameHistoricalMicrosoftAllocation(existing domain.UnifiedAllocation, orderNo string, cmd HistoricalMicrosoftAllocationCommand) bool {
	emailMatches := cmd.Mailbox == domain.MicrosoftMailboxMain || strings.EqualFold(existing.Email, cmd.Email)
	return existing.Type == domain.AllocationTypeMicrosoft && existing.OrderNo == orderNo &&
		existing.ProjectID == cmd.ProjectID && existing.ProductID == cmd.ProductID && existing.ResourceID == cmd.ResourceID &&
		existing.Mailbox == string(cmd.Mailbox) && emailMatches &&
		existing.Status == domain.AllocationStatusReleased
}

func (uc *UseCase) ReleaseByOrder(ctx context.Context, orderNo string) (*domain.UnifiedAllocation, error) {
	orderNo = strings.TrimSpace(orderNo)
	if orderNo == "" {
		return nil, domain.ErrInvalidAllocationRequest
	}
	var result *domain.UnifiedAllocation
	err := uc.repo.WithTx(ctx, func(txCtx context.Context) error {
		var err error
		result, err = uc.repo.ReleaseByOrder(txCtx, orderNo, time.Now().UTC())
		return err
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (uc *UseCase) ListAllocations(ctx context.Context, filter AllocationFilter) (*AllocationListResult, error) {
	if filter.Type != "" && !domain.IsValidAllocationType(filter.Type) {
		return nil, domain.ErrInvalidAllocationRequest
	}
	if filter.Status != "" && !domain.IsValidAllocationStatus(filter.Status) {
		return nil, domain.ErrInvalidAllocationRequest
	}
	if filter.Mailbox != "" && !isValidMailboxFilter(filter.Mailbox) {
		return nil, domain.ErrInvalidAllocationRequest
	}
	if filter.Limit <= 0 || filter.Limit > 100 {
		filter.Limit = 20
	}
	if filter.Offset < 0 {
		filter.Offset = 0
	}
	return uc.repo.ListAllocations(ctx, filter)
}

// ListAdminAllocations returns the OpenAPI administrator read composition. The
// page boundary is established by Alloc first; cross-context display facts are
// then loaded in one bounded batch and never written back into the allocation
// fact.
func (uc *UseCase) ListAdminAllocations(ctx context.Context, filter AllocationFilter) (*AdminAllocationListResult, error) {
	if uc == nil || uc.repo == nil || uc.adminAllocationEnrichment == nil {
		return nil, fmt.Errorf("administrator allocation query is unavailable")
	}
	if filter.Type != "" && !domain.IsValidAllocationType(filter.Type) {
		return nil, domain.ErrInvalidAllocationRequest
	}
	if filter.Status != "" && !domain.IsValidAllocationStatus(filter.Status) {
		return nil, domain.ErrInvalidAllocationRequest
	}
	if filter.Mailbox != "" && !isValidMailboxFilter(filter.Mailbox) {
		return nil, domain.ErrInvalidAllocationRequest
	}
	if filter.Offset < 0 || filter.Limit < 1 || filter.Limit > 100 {
		return nil, domain.ErrInvalidAllocationRequest
	}

	page, err := uc.repo.ListAllocations(ctx, filter)
	if err != nil {
		return nil, err
	}
	result := &AdminAllocationListResult{
		Items: make([]AdminAllocationItem, 0, len(page.Items)),
		Total: page.Total, Offset: page.Offset, Limit: page.Limit,
	}
	if len(page.Items) == 0 {
		return result, nil
	}
	orderNos := uniqueAllocationOrderNos(page.Items)
	enrichments, err := uc.adminAllocationEnrichment.GetAdminAllocationEnrichments(ctx, orderNos)
	if err != nil {
		return nil, fmt.Errorf("load administrator allocation enrichments: %w", err)
	}
	for _, item := range page.Items {
		enrichment, ok := enrichments[item.OrderNo]
		if !ok {
			return nil, fmt.Errorf("administrator allocation enrichment missing for order")
		}
		result.Items = append(result.Items, AdminAllocationItem{
			Type: item.Type, ID: item.ID, OrderNo: item.OrderNo,
			ProjectID: item.ProjectID, ProjectName: enrichment.ProjectName, ProjectLogoURL: enrichment.ProjectLogoURL,
			ResourceID: item.ResourceID, Mailbox: item.Mailbox, SupplyScope: item.SupplyScope,
			DeliveryEmail: enrichment.DeliveryEmail, ServiceMode: enrichment.ServiceMode, OrderStatus: enrichment.OrderStatus,
			Status: item.Status, PayAmount: enrichment.PayAmount, BuyerEmail: enrichment.BuyerEmail,
			VerificationCode: enrichment.VerificationCode, CreatedAt: item.CreatedAt, ReceiveUntil: enrichment.ReceiveUntil,
		})
	}
	return result, nil
}

func uniqueAllocationOrderNos(items []domain.UnifiedAllocation) []string {
	seen := make(map[string]struct{}, len(items))
	result := make([]string, 0, len(items))
	for _, item := range items {
		orderNo := strings.TrimSpace(item.OrderNo)
		if orderNo == "" {
			continue
		}
		if _, exists := seen[orderNo]; exists {
			continue
		}
		seen[orderNo] = struct{}{}
		result = append(result, orderNo)
	}
	return result
}

func (uc *UseCase) FindAllocationDetail(ctx context.Context, allocationType domain.AllocationType, allocationID uint) (*domain.UnifiedAllocation, error) {
	if allocationID == 0 || !domain.IsValidAllocationType(allocationType) {
		return nil, domain.ErrInvalidAllocationRequest
	}
	return uc.repo.FindAllocationDetail(ctx, allocationType, allocationID)
}

func (uc *UseCase) FindAllocationByOrder(ctx context.Context, orderNo string) (*domain.UnifiedAllocation, error) {
	orderNo = strings.TrimSpace(orderNo)
	if orderNo == "" {
		return nil, domain.ErrInvalidAllocationRequest
	}
	return uc.repo.FindAllocationByOrder(ctx, orderNo)
}

func (uc *UseCase) ListActiveByRecipient(ctx context.Context, recipient string) ([]domain.UnifiedAllocation, error) {
	recipient = strings.ToLower(strings.TrimSpace(recipient))
	if recipient == "" {
		return nil, domain.ErrInvalidAllocationRequest
	}
	return uc.repo.ListActiveByRecipient(ctx, recipient)
}

// AssertNoActiveAllocations is the Alloc-owned guard used by resource-state
// owners before changing a delivered identity, transferring ownership, or
// deleting a resource. The caller must already hold the corresponding
// email_resources roots in ascending ID order in the tx-bound context. New
// allocations acquire the same roots before any subtype/candidate lock.
func (uc *UseCase) AssertNoActiveAllocations(ctx context.Context, resourceIDs []uint) error {
	if uc == nil || uc.repo == nil {
		return domain.ErrAllocationTxRequired
	}
	resourceIDs = normalizeResourceIDs(resourceIDs)
	if len(resourceIDs) == 0 {
		return nil
	}
	if !uc.repo.HasParentTx(ctx) {
		return domain.ErrAllocationTxRequired
	}
	return uc.repo.AssertNoActiveAllocations(ctx, resourceIDs)
}

func (uc *UseCase) GetInventoryStats(ctx context.Context, projectID uint) (*InventoryStats, error) {
	if projectID == 0 {
		return nil, domain.ErrInvalidAllocationRequest
	}
	if uc.inventoryCache == nil {
		return uc.repo.GetInventoryStats(ctx, projectID)
	}
	entry := InventoryCacheEntry{Kind: InventoryCacheStats, ProjectID: projectID}
	return loadCachedInventory(
		ctx,
		uc.inventoryCache,
		entry,
		func(ctx context.Context) (*InventoryStats, error) {
			return uc.inventoryCache.GetInventoryStats(ctx, projectID)
		},
		func() *InventoryStats { return &InventoryStats{ProjectID: projectID} },
		uc.ScheduleInventoryRefresh,
	)
}

func (uc *UseCase) GetProductInventoryTotals(ctx context.Context, projectID uint, viewerUserID uint) (*ProjectProductInventoryTotals, error) {
	if projectID == 0 || viewerUserID == 0 {
		return nil, domain.ErrInvalidAllocationRequest
	}
	if err := uc.repo.AssertProjectInventoryAccess(ctx, projectID, viewerUserID); err != nil {
		return nil, err
	}
	return uc.GetProductInventorySnapshot(ctx, projectID)
}

// GetProductInventorySnapshot reads the shared project snapshot after the
// caller has already authorized project visibility.
func (uc *UseCase) GetProductInventorySnapshot(ctx context.Context, projectID uint) (*ProjectProductInventoryTotals, error) {
	if projectID == 0 {
		return nil, domain.ErrInvalidAllocationRequest
	}
	snapshots, err := uc.GetProductInventorySnapshots(ctx, []uint{projectID})
	if err != nil {
		return nil, err
	}
	snapshot := snapshots[projectID]
	if snapshot == nil {
		return nil, domain.ErrProjectNotAllocatable
	}
	return snapshot, nil
}

// GetProductInventorySnapshots reads shared snapshots for project IDs that the
// caller has already authorized. Cold keys are seeded as known-zero snapshots
// and queued for asynchronous refresh.
func (uc *UseCase) GetProductInventorySnapshots(ctx context.Context, projectIDs []uint) (map[uint]*ProjectProductInventoryTotals, error) {
	projectIDs = uniqueInventoryProjectIDs(projectIDs)
	if len(projectIDs) == 0 {
		return map[uint]*ProjectProductInventoryTotals{}, nil
	}
	if uc.inventoryCache == nil {
		result := make(map[uint]*ProjectProductInventoryTotals, len(projectIDs))
		for _, projectID := range projectIDs {
			totals, err := uc.repo.GetProductInventoryTotals(ctx, projectID)
			if errors.Is(err, domain.ErrProjectNotAllocatable) {
				continue
			}
			if err != nil {
				return nil, err
			}
			result[projectID] = totals
		}
		return result, nil
	}
	snapshots, err := uc.inventoryCache.GetProductInventorySnapshots(ctx, projectIDs)
	if err != nil {
		return nil, err
	}
	missing := make([]InventoryCacheEntry, 0, len(projectIDs)-len(snapshots))
	for _, projectID := range projectIDs {
		if snapshots[projectID] == nil {
			missing = append(missing, InventoryCacheEntry{Kind: InventoryCacheProducts, ProjectID: projectID})
		}
	}
	if len(missing) == 0 {
		return snapshots, nil
	}
	if err := uc.inventoryCache.InitializeInventory(ctx, missing, inventoryCacheHardTTL); err != nil {
		return nil, fmt.Errorf("initialize inventory cache: %w", err)
	}
	missingProjectIDs := make([]uint, len(missing))
	for i := range missing {
		missingProjectIDs[i] = missing[i].ProjectID
	}
	initialized, err := uc.inventoryCache.GetProductInventorySnapshots(ctx, missingProjectIDs)
	if err != nil {
		return nil, err
	}
	_ = uc.ScheduleInventoryRefresh(ctx)
	for _, entry := range missing {
		if totals := initialized[entry.ProjectID]; totals != nil {
			snapshots[entry.ProjectID] = totals
		} else {
			snapshots[entry.ProjectID] = &ProjectProductInventoryTotals{ProjectID: entry.ProjectID, Cold: true}
		}
	}
	return snapshots, nil
}

func uniqueInventoryProjectIDs(projectIDs []uint) []uint {
	result := make([]uint, 0, len(projectIDs))
	seen := make(map[uint]struct{}, len(projectIDs))
	for _, projectID := range projectIDs {
		if projectID == 0 {
			continue
		}
		if _, ok := seen[projectID]; ok {
			continue
		}
		seen[projectID] = struct{}{}
		result = append(result, projectID)
	}
	return result
}

func (uc *UseCase) HasProductInventory(ctx context.Context, req ProductInventoryAvailabilityRequest) (bool, error) {
	if req.ProjectID == 0 || req.ProductID == 0 {
		return false, domain.ErrInvalidAllocationRequest
	}
	req.EmailSuffix = normalizeEmailSuffix(req.EmailSuffix)
	if uc.inventoryCache == nil {
		return true, nil
	}
	unavailable, err := uc.inventoryCache.IsProductUnavailable(ctx, req)
	if err != nil {
		return true, err
	}
	if unavailable && req.PublicOnly {
		return false, nil
	}
	totals, err := loadCachedInventory(
		ctx,
		uc.inventoryCache,
		InventoryCacheEntry{Kind: InventoryCacheProducts, ProjectID: req.ProjectID},
		func(ctx context.Context) (*ProjectProductInventoryTotals, error) {
			return uc.inventoryCache.GetProductInventoryTotals(ctx, req.ProjectID)
		},
		func() *ProjectProductInventoryTotals {
			return &ProjectProductInventoryTotals{ProjectID: req.ProjectID, Cold: true}
		},
		uc.ScheduleInventoryRefresh,
	)
	if err != nil || totals == nil {
		// Cache outages must not reject an order. The allocator is still the
		// authoritative check-and-reserve operation.
		return true, err
	}
	if totals.Cold {
		return false, nil
	}
	// The shared snapshot contains public supply only. A zero cannot prove that
	// a private-first buyer has no owned resource, so warm the shared cache but
	// leave the final decision to the authoritative allocator.
	if !req.PublicOnly {
		return true, nil
	}
	available, known := productInventoryAvailable(totals, req)
	if !known {
		// A newly enabled product can predate its next inventory refresh. Fail open
		// so a stale read model never overrides the allocator.
		return true, nil
	}
	return available, nil
}

func productInventoryAvailable(totals *ProjectProductInventoryTotals, req ProductInventoryAvailabilityRequest) (bool, bool) {
	if totals == nil {
		return false, false
	}
	if totals.Cold {
		return false, true
	}
	for _, item := range totals.Items {
		if item.ProductID != req.ProductID {
			continue
		}
		if req.EmailSuffix == "" {
			if req.PublicOnly {
				return item.PublicAvailable > 0, true
			}
			return item.TotalAvailable > 0, true
		}
		for _, suffix := range item.Suffixes {
			if normalizeEmailSuffix(suffix.Suffix) != req.EmailSuffix {
				continue
			}
			if req.PublicOnly {
				return suffix.PublicAvailable > 0, true
			}
			return suffix.TotalAvailable > 0, true
		}
		return false, true
	}
	return false, false
}

func (uc *UseCase) MarkProductInventoryUnavailable(ctx context.Context, req ProductInventoryAvailabilityRequest) (bool, error) {
	if req.ProjectID == 0 || req.ProductID == 0 {
		return false, domain.ErrInvalidAllocationRequest
	}
	if uc.inventoryCache == nil {
		return false, nil
	}
	req.EmailSuffix = normalizeEmailSuffix(req.EmailSuffix)
	// The shared snapshot contains public supply only, so one project-level
	// correction covers every buyer and both supply policies. Avoid repeating
	// the expensive aggregate confirmation while that correction is live.
	req.PublicOnly = false
	alreadyUnavailable, err := uc.inventoryCache.IsProductUnavailable(ctx, req)
	if err != nil {
		return false, err
	}
	if alreadyUnavailable {
		return true, nil
	}
	// Bounded allocator probes can be exhausted by stale/concurrently claimed
	// candidates while later resources remain usable. Only publish a zero after
	// the exact read model independently confirms that this scope is exhausted.
	fresh, err := uc.repo.GetProductInventoryTotals(ctx, req.ProjectID)
	if err != nil {
		return false, err
	}
	// The shared snapshot is entirely public. Correct both the total and public
	// views together, regardless of which supply policy observed the miss.
	available, known := productInventoryAvailable(fresh, req)
	if !known || available {
		return false, nil
	}
	return uc.inventoryCache.MarkProductUnavailable(ctx, req)
}

func loadCachedInventory[T any](
	ctx context.Context,
	cache InventoryCache,
	entry InventoryCacheEntry,
	load func(context.Context) (*T, error),
	cold func() *T,
	schedule func(context.Context) error,
) (*T, error) {
	if cached, err := load(ctx); err != nil || cached != nil {
		return cached, err
	}
	if err := cache.InitializeInventory(ctx, []InventoryCacheEntry{entry}, inventoryCacheHardTTL); err != nil {
		return nil, fmt.Errorf("initialize inventory cache: %w", err)
	}
	cached, err := load(ctx)
	if err != nil {
		return nil, err
	}
	if schedule != nil {
		_ = schedule(ctx)
	}
	if cached != nil {
		return cached, nil
	}
	return cold(), nil
}

func (uc *UseCase) RefreshRoutingCandidates(ctx context.Context, projectID uint) (int, error) {
	if projectID == 0 {
		return 0, domain.ErrInvalidAllocationRequest
	}
	return uc.repo.RefreshRoutingCandidates(ctx, projectID)
}

func (uc *UseCase) QueueRoutingCandidateRefresh(ctx context.Context, projectID uint, operatorUserID uint, requestID string, path string) (*CandidateRefreshSubmitResult, error) {
	if projectID == 0 || operatorUserID == 0 {
		return nil, domain.ErrInvalidAllocationRequest
	}
	state, err := uc.repo.RequestCandidateRefresh(
		ctx,
		projectID,
		operatorUserID,
		strings.TrimSpace(requestID),
		strings.TrimSpace(path),
	)
	if err != nil {
		return nil, err
	}
	uc.ScheduleCandidateRefreshDispatcher(ctx, 0)
	requestedAt := state.UpdatedAt
	if state.RequestedAt != nil {
		requestedAt = *state.RequestedAt
	}
	return &CandidateRefreshSubmitResult{
		JobID:     state.ProjectID,
		ProjectID: state.ProjectID,
		Status:    state.Status,
		Created:   true,
		Message:   "Candidate refresh accepted.",
		CreatedAt: requestedAt,
		UpdatedAt: state.UpdatedAt,
	}, nil
}

func (uc *UseCase) RefreshInventoryCache(ctx context.Context) (*InventoryRefreshResult, error) {
	return uc.RefreshInventoryCacheBefore(ctx, time.Now())
}

// RefreshInventoryCacheBefore refreshes only entries active before one task's
// cutoff, so reads during aggregation are left for the next task.
func (uc *UseCase) RefreshInventoryCacheBefore(ctx context.Context, before time.Time) (*InventoryRefreshResult, error) {
	if uc == nil || uc.inventoryCache == nil {
		return &InventoryRefreshResult{}, nil
	}
	if before.IsZero() {
		before = time.Now()
	}
	entries, err := uc.inventoryCache.ClaimActiveInventory(ctx, before.Add(-inventoryCacheActivityTTL), before, inventoryRefreshBatchSize)
	if err != nil {
		return nil, fmt.Errorf("claim active inventory cache entries: %w", err)
	}
	result := &InventoryRefreshResult{Attempted: len(entries)}
	for i, entry := range entries {
		if err := ctx.Err(); err != nil {
			_ = requeueInventory(uc.inventoryCache, entries[i:])
			return result, err
		}
		token, acquired, err := uc.inventoryCache.AcquireInventoryRefresh(ctx, entry, inventoryRefreshLockTTL)
		if err != nil {
			if requeueErr := requeueInventory(uc.inventoryCache, []InventoryCacheEntry{entry}); requeueErr != nil {
				return result, errors.Join(err, requeueErr)
			}
			result.Failed++
			result.LastError = err
			continue
		}
		if !acquired {
			if err := requeueInventory(uc.inventoryCache, []InventoryCacheEntry{entry}); err != nil {
				return result, err
			}
			result.Skipped++
			continue
		}
		removed := false
		switch entry.Kind {
		case InventoryCacheStats:
			stats, refreshErr := uc.repo.GetInventoryStats(ctx, entry.ProjectID)
			err = refreshErr
			if errors.Is(err, domain.ErrProjectNotAllocatable) || (err == nil && stats == nil) {
				err = uc.inventoryCache.DeleteInventory(ctx, entry)
				removed = err == nil
			} else if err == nil {
				err = uc.inventoryCache.RefreshInventoryStats(ctx, entry.ProjectID, stats, inventoryCacheHardTTL)
			}
		case InventoryCacheProducts:
			totals, refreshErr := uc.repo.GetProductInventoryTotals(ctx, entry.ProjectID)
			err = refreshErr
			if errors.Is(err, domain.ErrProjectNotAllocatable) || (err == nil && totals == nil) {
				err = uc.inventoryCache.DeleteInventory(ctx, entry)
				removed = err == nil
			} else if err == nil {
				err = uc.inventoryCache.RefreshProductInventoryTotals(ctx, entry.ProjectID, totals, inventoryCacheHardTTL)
			}
		default:
			err = uc.inventoryCache.DeleteInventory(ctx, entry)
			removed = err == nil
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		releaseErr := uc.inventoryCache.ReleaseInventoryRefresh(cleanupCtx, entry, token)
		cancel()
		if err == nil {
			err = releaseErr
		}
		if err != nil {
			if requeueErr := requeueInventory(uc.inventoryCache, []InventoryCacheEntry{entry}); requeueErr != nil {
				return result, errors.Join(err, requeueErr)
			}
			result.Failed++
			result.LastError = err
			continue
		}
		if removed {
			result.Removed++
		} else {
			result.Updated++
		}
	}
	return result, nil
}

func requeueInventory(cache InventoryCache, entries []InventoryCacheEntry) error {
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return cache.RequeueInventory(cleanupCtx, entries)
}

func (uc *UseCase) ProcessCandidateRefresh(ctx context.Context, task CandidateRefreshTask) error {
	if task.ProjectID == 0 || task.Generation == 0 {
		return domain.ErrAllocationNotFound
	}
	if _, err := uc.repo.MarkCandidateRefreshProcessing(ctx, task.ProjectID, task.Generation); err != nil {
		return err
	}
	_, current, err := uc.repo.RunCandidateRefresh(ctx, task.ProjectID, task.Generation)
	if err == nil || !current {
		return nil
	}
	if errors.Is(err, domain.ErrCandidateRefreshInfrastructure) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if _, releaseErr := uc.repo.ReleaseCandidateRefreshInfrastructureFailure(
			cleanupCtx,
			task.ProjectID,
			task.Generation,
			"Candidate refresh infrastructure failed; dispatcher will retry.",
		); releaseErr != nil {
			return errors.Join(err, releaseErr)
		}
		return err
	}
	recorded, abnormal, recordErr := uc.repo.RecordCandidateRefreshFailure(
		ctx,
		task.ProjectID,
		task.Generation,
		"Candidate refresh failed.",
	)
	if recordErr != nil {
		return errors.Join(err, recordErr)
	}
	if recorded && !abnormal {
		uc.ScheduleCandidateRefreshDispatcher(ctx, time.Second)
	}
	return err
}

func (uc *UseCase) DispatchCandidateRefreshes(ctx context.Context, limit int) (*CandidateRefreshDispatchResult, error) {
	if limit <= 0 {
		limit = 100
	}
	states, err := uc.repo.ListPendingCandidateRefreshes(ctx, limit)
	if err != nil {
		return nil, err
	}
	result := &CandidateRefreshDispatchResult{Attempted: len(states)}
	for i := range states {
		queued, err := uc.enqueueCandidateRefresh(ctx, states[i])
		if err != nil {
			result.Failed++
			continue
		}
		if queued {
			result.Queued++
		}
	}
	return result, nil
}

func (uc *UseCase) ScheduleCandidateRefreshDispatcher(ctx context.Context, delay time.Duration) {
	if uc == nil || uc.queue == nil {
		return
	}
	_ = uc.queue.EnqueueCandidateRefreshDispatcher(ctx, delay)
}

func (uc *UseCase) ScheduleInventoryRefresh(ctx context.Context) error {
	if uc == nil || uc.queue == nil {
		return nil
	}
	return uc.queue.EnqueueInventoryRefresh(ctx)
}

func (uc *UseCase) enqueueCandidateRefresh(ctx context.Context, state domain.CandidateRefresh) (bool, error) {
	if uc == nil || uc.queue == nil {
		return false, domain.ErrInvalidAllocationRequest
	}
	if state.ProjectID == 0 || state.Generation == 0 {
		return false, domain.ErrInvalidAllocationRequest
	}
	accepted, err := uc.queue.EnqueueCandidateRefresh(ctx, CandidateRefreshTask{
		ProjectID:  state.ProjectID,
		Generation: state.Generation,
		RequestID:  state.RequestID,
	})
	if err != nil || !accepted {
		return false, err
	}
	processing, err := uc.repo.MarkCandidateRefreshProcessing(ctx, state.ProjectID, state.Generation)
	if err != nil {
		return false, err
	}
	return processing, nil
}

func (uc *UseCase) ListRoutingCandidates(ctx context.Context, filter CandidateFilter) (*CandidateListResult, error) {
	if filter.ProjectID == 0 {
		return nil, domain.ErrInvalidAllocationRequest
	}
	if filter.Type != "" && !domain.IsValidAllocationType(filter.Type) {
		return nil, domain.ErrInvalidAllocationRequest
	}
	if filter.Limit <= 0 || filter.Limit > 100 {
		filter.Limit = 20
	}
	if filter.Offset < 0 {
		filter.Offset = 0
	}
	return uc.repo.ListRoutingCandidates(ctx, filter)
}

func (uc *UseCase) allocateMicrosoft(ctx context.Context, cmd AllocateCommand, config ProductAllocationConfig) (*domain.UnifiedAllocation, error) {
	return uc.allocateMicrosoftOnce(ctx, cmd, config)
}

func (uc *UseCase) allocateMicrosoftOnce(ctx context.Context, cmd AllocateCommand, config ProductAllocationConfig) (*domain.UnifiedAllocation, error) {
	preferences := microsoftMailboxPreferences(cmd.OrderNo, config)
	now := time.Now().UTC()
	resourceBusy := false
	for _, mailbox := range preferences {
		buckets := bucketProbeSequence(cmd.OrderNo, config.ProjectID, string(mailbox))
		for _, bucket := range buckets {
			result, busy, err := uc.tryMicrosoftBucket(ctx, cmd, config, mailbox, &bucket, now)
			if err != nil {
				return nil, err
			}
			resourceBusy = resourceBusy || busy
			if result != nil {
				return result, nil
			}
		}
		result, busy, err := uc.tryMicrosoftBucket(ctx, cmd, config, mailbox, nil, now)
		if err != nil {
			return nil, err
		}
		resourceBusy = resourceBusy || busy
		if result != nil {
			return result, nil
		}
	}
	if resourceBusy {
		return nil, domain.ErrAllocationConflict
	}
	return nil, domain.ErrInsufficientInventory
}

func (uc *UseCase) tryMicrosoftBucket(ctx context.Context, cmd AllocateCommand, config ProductAllocationConfig, mailbox domain.MicrosoftMailbox, bucket *uint8, now time.Time) (*domain.UnifiedAllocation, bool, error) {
	limit := candidateWindowSize
	if bucket == nil {
		limit = globalCandidateWindow
	}
	candidates, err := uc.repo.ListMicrosoftSourceCandidates(ctx, config.ProjectID, cmd.BuyerUserID, cmd.SupplyScope, mailbox, bucket, limit, cmd.EmailSuffix)
	if err != nil {
		return nil, false, err
	}
	if len(candidates) == 0 {
		return nil, false, nil
	}
	resourceBusy := false
	for _, candidate := range candidates {
		result, err := uc.tryMicrosoftCandidate(ctx, cmd, config, mailbox, candidate, now)
		if err == nil && result != nil {
			return result, false, nil
		}
		if errors.Is(err, errResourceRootBusy) {
			resourceBusy = true
			continue
		}
		if errors.Is(err, domain.ErrInsufficientInventory) || errors.Is(err, errCandidateUnavailable) {
			continue
		}
		// A failed allocation INSERT retains index locks until this transaction
		// rolls back, so conflicts must never advance to another candidate.
		return nil, false, err
	}
	return nil, resourceBusy, nil
}

func (uc *UseCase) tryMicrosoftCandidate(ctx context.Context, cmd AllocateCommand, config ProductAllocationConfig, mailbox domain.MicrosoftMailbox, candidate MicrosoftCandidate, now time.Time) (*domain.UnifiedAllocation, error) {
	lockRoot := uc.repo.LockResourceRoot
	if cmd.lockResourceRoot != nil {
		lockRoot = cmd.lockResourceRoot
	}
	lockedRoot, err := lockRoot(ctx, candidate.ResourceID, domain.AllocationTypeMicrosoft)
	if err != nil {
		return nil, err
	}
	if !lockedRoot {
		return nil, errResourceRootBusy
	}

	lockedCandidate, err := uc.repo.LockMicrosoftCandidate(ctx, candidate.ResourceID, config.ProjectID, cmd.BuyerUserID, cmd.SupplyScope, mailbox, cmd.EmailSuffix)
	if err != nil {
		return nil, err
	}
	if lockedCandidate == nil {
		return nil, errCandidateUnavailable
	}
	candidate = *lockedCandidate

	switch mailbox {
	case domain.MicrosoftMailboxMain:
		matched, err := uc.repo.IsMicrosoftMailboxHistoricallyMatched(ctx, config.ProjectID, domain.MicrosoftMailboxMain, candidate.ResourceID)
		if err != nil {
			return nil, err
		}
		_, candidateSuffix, validEmail := splitEmail(candidate.EmailAddress)
		if (cmd.EmailSuffix == "" || validEmail && candidateSuffix == cmd.EmailSuffix) && !matched && !candidate.MainAllocated {
			return uc.createMicrosoftAllocation(ctx, cmd, config, candidate.ResourceID, domain.MicrosoftMailboxMain, nil, nil, nil, candidate.EmailAddress, now, nil)
		}
		alias, aliasErr := uc.repo.FindReusableExplicitAlias(ctx, config.ProjectID, candidate.ResourceID, cmd.EmailSuffix)
		if aliasErr != nil {
			return nil, aliasErr
		}
		if alias == nil {
			return nil, errCandidateUnavailable
		}
		return uc.createMicrosoftAllocation(ctx, cmd, config, candidate.ResourceID, domain.MicrosoftMailboxAlias, &alias.ID, nil, nil, alias.Email, now, nil)
	case domain.MicrosoftMailboxDot:
		alias, err := uc.repo.FindReusableDotAlias(ctx, config.ProjectID, candidate.ResourceID)
		if err != nil {
			return nil, err
		}
		if alias != nil {
			return uc.createMicrosoftAllocation(ctx, cmd, config, candidate.ResourceID, domain.MicrosoftMailboxDot, nil, &alias.ID, nil, alias.Email, now, nil)
		}
		for _, email := range dotAliasVariants(candidate.EmailAddress) {
			alias, err = uc.repo.FindOrCreateDotAlias(ctx, candidate.ResourceID, email)
			if err != nil {
				return nil, err
			}
			if alias == nil {
				continue
			}
			matched, err := uc.repo.IsMicrosoftMailboxHistoricallyMatched(ctx, config.ProjectID, domain.MicrosoftMailboxDot, alias.ID)
			if err != nil {
				return nil, err
			}
			if matched {
				continue
			}
			return uc.createMicrosoftAllocation(ctx, cmd, config, candidate.ResourceID, domain.MicrosoftMailboxDot, nil, &alias.ID, nil, alias.Email, now, nil)
		}
		return nil, domain.ErrInsufficientInventory
	case domain.MicrosoftMailboxPlus:
		dailyUsage := DailyUsageReservation{
			UsageDate:      allocationUsageDate(now),
			AllocationType: domain.AllocationTypeMicrosoft,
			ResourceID:     candidate.ResourceID,
			Kind:           domain.DailyUsageKindPlus,
			Limit:          candidate.PlusDailyLimit,
		}
		if err := uc.repo.EnsureDailyUsageAvailable(ctx, dailyUsage.UsageDate, dailyUsage.AllocationType, dailyUsage.ResourceID, dailyUsage.Kind, dailyUsage.Limit); err != nil {
			return nil, err
		}
		alias, err := uc.repo.FindReusablePlusAlias(ctx, config.ProjectID, candidate.ResourceID)
		if err != nil {
			return nil, err
		}
		if alias != nil {
			return uc.createMicrosoftAllocation(ctx, cmd, config, candidate.ResourceID, domain.MicrosoftMailboxPlus, nil, nil, &alias.ID, alias.Email, now, &dailyUsage)
		}
		for _, email := range plusAliasVariants(candidate.EmailAddress, config.ProjectID, cmd.OrderNo) {
			alias, err = uc.repo.FindOrCreatePlusAlias(ctx, candidate.ResourceID, email)
			if err != nil {
				return nil, err
			}
			if alias == nil {
				continue
			}
			matched, err := uc.repo.IsMicrosoftMailboxHistoricallyMatched(ctx, config.ProjectID, domain.MicrosoftMailboxPlus, alias.ID)
			if err != nil {
				return nil, err
			}
			if matched {
				continue
			}
			return uc.createMicrosoftAllocation(ctx, cmd, config, candidate.ResourceID, domain.MicrosoftMailboxPlus, nil, nil, &alias.ID, alias.Email, now, &dailyUsage)
		}
		return nil, domain.ErrInsufficientInventory
	default:
		return nil, domain.ErrInvalidAllocationRequest
	}
}

func (uc *UseCase) createMicrosoftAllocation(ctx context.Context, cmd AllocateCommand, config ProductAllocationConfig, resourceID uint, mailbox domain.MicrosoftMailbox, explicitAliasID, dotAliasID, plusAliasID *uint, email string, now time.Time, dailyUsage *DailyUsageReservation) (*domain.UnifiedAllocation, error) {
	if cmd.ensureOrderGuard == nil {
		return nil, domain.ErrAllocationTxRequired
	}
	if _, suffix, valid := splitEmail(email); cmd.EmailSuffix != "" && (!valid || suffix != cmd.EmailSuffix) {
		return nil, errCandidateUnavailable
	}
	allocation := &domain.MicrosoftAllocation{
		OrderNo:         cmd.OrderNo,
		ProjectID:       config.ProjectID,
		ProductID:       config.ProductID,
		ResourceID:      resourceID,
		SupplyScope:     cmd.SupplyScope,
		Mailbox:         mailbox,
		ExplicitAliasID: explicitAliasID,
		DotAliasID:      dotAliasID,
		PlusAliasID:     plusAliasID,
		Email:           strings.ToLower(strings.TrimSpace(email)),
		Status:          domain.AllocationStatusAllocated,
	}
	if allocation.Email == "" {
		return nil, domain.ErrInvalidAllocationRequest
	}
	if dailyUsage != nil {
		if err := uc.repo.ConsumeDailyUsage(ctx, dailyUsage.UsageDate, dailyUsage.AllocationType, dailyUsage.ResourceID, dailyUsage.Kind, dailyUsage.Limit); err != nil {
			return nil, err
		}
	}
	if err := cmd.ensureOrderGuard(ctx, domain.AllocationTypeMicrosoft); err != nil {
		return nil, err
	}
	if err := uc.repo.CreateMicrosoftAllocation(ctx, allocation); err != nil {
		return nil, err
	}
	if err := uc.repo.TouchMicrosoftAllocated(ctx, resourceID, now); err != nil {
		return nil, err
	}
	return &domain.UnifiedAllocation{
		Type:        domain.AllocationTypeMicrosoft,
		ID:          allocation.ID,
		OrderNo:     allocation.OrderNo,
		ProjectID:   allocation.ProjectID,
		ProductID:   allocation.ProductID,
		ResourceID:  allocation.ResourceID,
		SupplyScope: allocation.SupplyScope,
		Mailbox:     string(allocation.Mailbox),
		Email:       allocation.Email,
		Status:      allocation.Status,
		CreatedAt:   allocation.CreatedAt,
	}, nil
}

func (uc *UseCase) allocateDomain(ctx context.Context, cmd AllocateCommand, config ProductAllocationConfig) (*domain.UnifiedAllocation, error) {
	return uc.allocateDomainOnce(ctx, cmd, config)
}

func (uc *UseCase) allocateDomainOnce(ctx context.Context, cmd AllocateCommand, config ProductAllocationConfig) (*domain.UnifiedAllocation, error) {
	now := time.Now().UTC()
	resourceBusy := false
	buckets := bucketProbeSequence(cmd.OrderNo, config.ProjectID, "domain")
	for _, bucket := range buckets {
		result, busy, err := uc.tryDomainBucket(ctx, cmd, config, &bucket, now)
		if err != nil {
			return nil, err
		}
		resourceBusy = resourceBusy || busy
		if result != nil {
			return result, nil
		}
	}
	result, busy, err := uc.tryDomainBucket(ctx, cmd, config, nil, now)
	if err != nil {
		return nil, err
	}
	resourceBusy = resourceBusy || busy
	if result != nil {
		return result, nil
	}
	if resourceBusy {
		return nil, domain.ErrAllocationConflict
	}
	return nil, domain.ErrInsufficientInventory
}

func (uc *UseCase) tryDomainBucket(ctx context.Context, cmd AllocateCommand, config ProductAllocationConfig, bucket *uint8, now time.Time) (*domain.UnifiedAllocation, bool, error) {
	limit := candidateWindowSize
	if bucket == nil {
		limit = globalCandidateWindow
	}
	candidates, err := uc.repo.ListDomainSourceCandidates(ctx, cmd.BuyerUserID, cmd.SupplyScope, bucket, limit, cmd.EmailSuffix)
	if err != nil {
		return nil, false, err
	}
	if len(candidates) == 0 {
		return nil, false, nil
	}
	resourceBusy := false
	for _, candidate := range candidates {
		result, err := uc.tryDomainCandidate(ctx, cmd, config, candidate, now)
		if err == nil && result != nil {
			return result, false, nil
		}
		if errors.Is(err, errResourceRootBusy) {
			resourceBusy = true
			continue
		}
		if errors.Is(err, domain.ErrInsufficientInventory) || errors.Is(err, errCandidateUnavailable) {
			continue
		}
		// Domain allocation conflicts have the same failed-INSERT lock lifetime.
		return nil, false, err
	}
	return nil, resourceBusy, nil
}

func (uc *UseCase) tryDomainCandidate(ctx context.Context, cmd AllocateCommand, config ProductAllocationConfig, candidate DomainCandidate, now time.Time) (*domain.UnifiedAllocation, error) {
	lockRoot := uc.repo.LockResourceRoot
	if cmd.lockResourceRoot != nil {
		lockRoot = cmd.lockResourceRoot
	}
	lockedRoot, err := lockRoot(ctx, candidate.ResourceID, domain.AllocationTypeDomain)
	if err != nil {
		return nil, err
	}
	if !lockedRoot {
		return nil, errResourceRootBusy
	}

	lockedCandidate, err := uc.repo.LockDomainCandidate(ctx, candidate.ResourceID, cmd.BuyerUserID, cmd.SupplyScope, cmd.EmailSuffix)
	if err != nil {
		return nil, err
	}
	if lockedCandidate == nil {
		return nil, errCandidateUnavailable
	}
	candidate = *lockedCandidate

	dailyUsage := DailyUsageReservation{
		UsageDate:      allocationUsageDate(now),
		AllocationType: domain.AllocationTypeDomain,
		ResourceID:     candidate.ResourceID,
		Kind:           domain.DailyUsageKindDomainMailbox,
		Limit:          candidate.MailboxDailyLimit,
	}
	if err := uc.repo.EnsureDailyUsageAvailable(ctx, dailyUsage.UsageDate, dailyUsage.AllocationType, dailyUsage.ResourceID, dailyUsage.Kind, dailyUsage.Limit); err != nil {
		return nil, err
	}

	mailbox, err := uc.repo.FindReusableGeneratedMailbox(ctx, config.ProjectID, candidate.ResourceID)
	if err != nil {
		return nil, err
	}
	if mailbox != nil {
		return uc.createDomainAllocation(ctx, cmd, config, candidate.ResourceID, mailbox.ID, mailbox.Email, now, &dailyUsage)
	}
	for _, email := range generatedMailboxVariants(candidate.Domain) {
		mailbox, err = uc.repo.FindOrCreateGeneratedMailbox(ctx, candidate.ResourceID, candidate.OwnerUserID, email)
		if err != nil {
			return nil, err
		}
		if mailbox == nil {
			continue
		}
		allocated, err := uc.repo.IsDomainMailboxAllocated(ctx, config.ProjectID, mailbox.ID)
		if err != nil {
			return nil, err
		}
		if allocated {
			continue
		}
		return uc.createDomainAllocation(ctx, cmd, config, candidate.ResourceID, mailbox.ID, mailbox.Email, now, &dailyUsage)
	}
	return nil, domain.ErrInsufficientInventory
}

func (uc *UseCase) createDomainAllocation(ctx context.Context, cmd AllocateCommand, config ProductAllocationConfig, resourceID uint, mailboxID uint, email string, now time.Time, dailyUsage *DailyUsageReservation) (*domain.UnifiedAllocation, error) {
	if cmd.ensureOrderGuard == nil {
		return nil, domain.ErrAllocationTxRequired
	}
	allocation := &domain.GeneratedMailboxAllocation{
		OrderNo:     cmd.OrderNo,
		ProjectID:   config.ProjectID,
		ProductID:   config.ProductID,
		ResourceID:  resourceID,
		SupplyScope: cmd.SupplyScope,
		MailboxID:   mailboxID,
		Email:       strings.ToLower(strings.TrimSpace(email)),
		Status:      domain.AllocationStatusAllocated,
	}
	if allocation.Email == "" {
		return nil, domain.ErrInvalidAllocationRequest
	}
	if dailyUsage != nil {
		if err := uc.repo.ConsumeDailyUsage(ctx, dailyUsage.UsageDate, dailyUsage.AllocationType, dailyUsage.ResourceID, dailyUsage.Kind, dailyUsage.Limit); err != nil {
			return nil, err
		}
	}
	if err := cmd.ensureOrderGuard(ctx, domain.AllocationTypeDomain); err != nil {
		return nil, err
	}
	if err := uc.repo.CreateDomainAllocation(ctx, allocation); err != nil {
		return nil, err
	}
	if err := uc.repo.TouchDomainAllocated(ctx, resourceID, mailboxID, now); err != nil {
		return nil, err
	}
	return &domain.UnifiedAllocation{
		Type:        domain.AllocationTypeDomain,
		ID:          allocation.ID,
		OrderNo:     allocation.OrderNo,
		ProjectID:   allocation.ProjectID,
		ProductID:   allocation.ProductID,
		ResourceID:  allocation.ResourceID,
		SupplyScope: allocation.SupplyScope,
		Mailbox:     "domain",
		Email:       allocation.Email,
		Status:      allocation.Status,
		CreatedAt:   allocation.CreatedAt,
	}, nil
}

func microsoftMailboxPreferences(orderNo string, config ProductAllocationConfig) []domain.MicrosoftMailbox {
	type weightedMailbox struct {
		mailbox domain.MicrosoftMailbox
		weight  int
	}
	weights := []weightedMailbox{
		{mailbox: domain.MicrosoftMailboxMain, weight: config.MainWeight},
		{mailbox: domain.MicrosoftMailboxDot, weight: config.DotWeight},
		{mailbox: domain.MicrosoftMailboxPlus, weight: config.PlusWeight},
	}
	total := 0
	for _, item := range weights {
		if item.weight > 0 {
			total += item.weight
		}
	}
	if total <= 0 {
		return nil
	}
	pick := int(hash64(orderNo+"|"+strconv.Itoa(int(config.ProductID))) % uint64(total))
	selected := domain.MicrosoftMailboxMain
	running := 0
	for _, item := range weights {
		if item.weight <= 0 {
			continue
		}
		running += item.weight
		if pick < running {
			selected = item.mailbox
			break
		}
	}
	result := []domain.MicrosoftMailbox{selected}
	for _, item := range weights {
		if item.weight <= 0 || item.mailbox == selected {
			continue
		}
		result = append(result, item.mailbox)
	}
	return result
}

func bucketProbeSequence(orderNo string, projectID uint, kind string) []uint8 {
	start := uint8(hash64(orderNo+"|"+strconv.Itoa(int(projectID))+"|"+kind) % BucketCount)
	result := make([]uint8, 0, bucketProbeCount)
	for i := 0; i < bucketProbeCount; i++ {
		result = append(result, uint8((int(start)+i)%BucketCount))
	}
	return result
}

func hash64(value string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(value))
	return h.Sum64()
}

func dotAliasVariants(email string) []string {
	local, domainPart, ok := splitEmail(email)
	if !ok || len(local) < 2 {
		return nil
	}
	limit := len(local) - 1
	if limit > DotAliasCapacityPerResource {
		limit = DotAliasCapacityPerResource
	}
	result := make([]string, 0, limit)
	for i := 1; i <= limit; i++ {
		if local[i-1] == '.' || local[i] == '.' {
			continue
		}
		result = append(result, local[:i]+"."+local[i:]+"@"+domainPart)
	}
	return result
}

func allocationUsageDate(value time.Time) string {
	return value.UTC().Format("2006-01-02")
}

func normalizeEmailSuffix(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	return strings.TrimPrefix(value, "@")
}

func plusAliasVariants(email string, projectID uint, orderNo string) []string {
	local, domainPart, ok := splitEmail(email)
	if !ok || local == "" {
		return nil
	}
	base := strconv.FormatUint(uint64(projectID), 36) + strconv.FormatUint(hash64(orderNo)%46656, 36)
	result := make([]string, 0, aliasGenerationWindow)
	for i := 0; i < aliasGenerationWindow; i++ {
		result = append(result, local+"+p"+base+strconv.FormatInt(int64(i), 36)+"@"+domainPart)
	}
	return result
}

func generatedMailboxVariants(domainPart string) []string {
	domainPart = strings.ToLower(strings.TrimSpace(domainPart))
	if domainPart == "" {
		return nil
	}
	result := make([]string, 0, aliasGenerationWindow)
	seen := make(map[string]struct{}, aliasGenerationWindow)
	for len(result) < aliasGenerationWindow {
		name := generatedMailboxName(rand.IntN(generatedMailboxNameCount()))
		var suffix strings.Builder
		for range rand.IntN(7) {
			suffix.WriteByte(byte('0' + rand.IntN(10)))
		}
		email := name + suffix.String() + "@" + domainPart
		if _, exists := seen[email]; exists {
			continue
		}
		seen[email] = struct{}{}
		result = append(result, email)
	}
	return result
}

func generatedMailboxNameCount() int {
	return len(biblicalMailboxNames) + len(pinyinMailboxNameParts)*len(pinyinMailboxNameParts)
}

func generatedMailboxName(index int) string {
	if index < len(biblicalMailboxNames) {
		return biblicalMailboxNames[index]
	}
	index -= len(biblicalMailboxNames)
	return pinyinMailboxNameParts[index/len(pinyinMailboxNameParts)] + pinyinMailboxNameParts[index%len(pinyinMailboxNameParts)]
}

func splitEmail(email string) (string, string, bool) {
	email = strings.ToLower(strings.TrimSpace(email))
	index := strings.LastIndex(email, "@")
	if index <= 0 || index == len(email)-1 {
		return "", "", false
	}
	return email[:index], email[index+1:], true
}

func isValidMailboxFilter(value string) bool {
	switch domain.MicrosoftMailbox(value) {
	case domain.MicrosoftMailboxMain, domain.MicrosoftMailboxAlias, domain.MicrosoftMailboxDot, domain.MicrosoftMailboxPlus:
		return true
	default:
		return value == "domain"
	}
}

func normalizeResourceIDs(ids []uint) []uint {
	if len(ids) == 0 {
		return nil
	}
	seen := make(map[uint]struct{}, len(ids))
	result := make([]uint, 0, len(ids))
	for _, id := range ids {
		if id == 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		result = append(result, id)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}
