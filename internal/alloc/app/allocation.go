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
	coredomain "github.com/donnel666/remail/internal/core/domain"
	"github.com/donnel666/remail/internal/mailbox"
	moneyfmt "github.com/donnel666/remail/internal/money"
	"github.com/donnel666/remail/internal/platform"
)

const (
	inventoryCacheHardTTL   = 24 * time.Hour
	inventoryRefreshLockTTL = 10 * time.Minute
	// ponytail: five cold keys bound each aggregate burst; raise only when the
	// refresh p99 stays below one interval without affecting checkout/pickup.
	inventoryRefreshBatchSize = 5
)

func InventoryRefreshParametersValue() InventoryRefreshParameters {
	return InventoryRefreshParameters{
		RefreshInterval: InventoryRefreshIntervalValue(),
		CacheHardTTL:    inventoryCacheHardTTLValue(),
		BatchSize:       inventoryRefreshBatchSize,
	}
}

var (
	errCandidateUnavailable = errors.New("allocation candidate unavailable")
	errResourceRootBusy     = errors.New("allocation resource root busy")
	errResourceTypeBusy     = errors.New("allocation resource type busy")
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
	ServiceMode      domain.GmailServiceMode
	// RequiredUntil is the latest instant for which the allocated resource must
	// remain usable. Trade supplies the immutable order service-window bound.
	RequiredUntil time.Time
	// FulfillExistingOrder is set only by Trade after an order is persisted.
	// A delisted product cannot receive new orders, but it must remain
	// allocatable for an already accepted order.
	FulfillExistingOrder bool
	ensureOrderGuard     func(context.Context, domain.AllocationType) error
	lockResourceRoot     func(context.Context, uint, domain.AllocationType) (bool, error)
}

type UseCase struct {
	repo                       Repository
	queue                      InventoryRefreshQueue
	adminAllocationEnrichment  AdminAllocationEnrichmentPort
	historicalMicrosoftAliases HistoricalMicrosoftAliasPort
	gmailVariantCooldown       GmailVariantCooldownPort
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

func (uc *UseCase) SetGmailVariantCooldownPort(port GmailVariantCooldownPort) {
	if uc != nil {
		uc.gmailVariantCooldown = port
	}
}

func (uc *UseCase) SetAdminAllocationEnrichmentPort(port AdminAllocationEnrichmentPort) {
	if uc != nil {
		uc.adminAllocationEnrichment = port
	}
}

func NewUseCase(repo Repository, queues ...InventoryRefreshQueue) *UseCase {
	var queue InventoryRefreshQueue
	if len(queues) > 0 {
		queue = queues[0]
	}
	return &UseCase{
		repo:  repo,
		queue: queue,
	}
}

func (uc *UseCase) Allocate(ctx context.Context, cmd AllocateCommand) (result *domain.UnifiedAllocation, runErr error) {
	startedAt := time.Now()
	metricType := "unknown"
	existingHit := false
	defer func() {
		recovered := recover()
		if result != nil {
			metricType = string(result.Type)
		}
		metricResult := allocationMetricResult(runErr, existingHit)
		if recovered != nil {
			metricResult = "system_failed"
		}
		platform.ObserveAllocationDuration(metricType, metricResult, startedAt)
		platform.RecordAllocationResult(metricType, metricResult)
		if recovered != nil {
			panic(recovered)
		}
	}()

	cmd.OrderNo = strings.TrimSpace(cmd.OrderNo)
	scopes := normalizedSupplyScopes(cmd)
	cmd.EmailSuffix = normalizeEmailSuffix(cmd.EmailSuffix)
	requestedSuffix := cmd.EmailSuffix
	domainSelection := requestedSuffix
	if cmd.OrderNo == "" || cmd.BuyerUserID == 0 || cmd.ProjectProductID == 0 {
		return nil, domain.ErrInvalidAllocationRequest
	}

	var err error
	if isRandomSuffixSelector(requestedSuffix) {
		existing, findErr := uc.repo.FindExistingAllocation(ctx, cmd.OrderNo)
		if findErr != nil {
			return nil, findErr
		}
		if existing != nil {
			result = existing
			existingHit = true
			return result, nil
		}
		requestedSuffix, err = uc.SelectRandomInventorySuffix(ctx, ProductSuffixSelectionRequest{
			ProductID:            cmd.ProjectProductID,
			BuyerUserID:          cmd.BuyerUserID,
			SupplyScopes:         scopes,
			Selector:             cmd.EmailSuffix,
			FulfillExistingOrder: cmd.FulfillExistingOrder,
		})
		if err != nil {
			existing, findErr = uc.repo.FindExistingAllocation(ctx, cmd.OrderNo)
			if findErr != nil {
				return nil, findErr
			}
			if existing != nil {
				result = existing
				existingHit = true
				return result, nil
			}
			return nil, err
		}
		cmd.EmailSuffix = requestedSuffix
		domainSelection = requestedSuffix
	}
	attempts := candidateRetryCountValue()
	if uc.repo.HasParentTx(ctx) {
		// A nested retry would keep the parent wallet/resource locks and sleep in
		// the same transaction. Let the complete order transaction roll back first.
		attempts = 1
	}
	for attempt := 0; attempt < attempts; attempt++ {
		result = nil
		existingHit = false
		err = uc.repo.WithTx(ctx, func(txCtx context.Context) error {
			cmd.EmailSuffix = requestedSuffix
			existing, err := uc.repo.FindExistingAllocation(txCtx, cmd.OrderNo)
			if err != nil {
				return err
			}
			if existing != nil {
				result = existing
				existingHit = true
				return nil
			}

			config, err := uc.repo.LoadProductConfig(txCtx, cmd.ProjectProductID, cmd.BuyerUserID, cmd.FulfillExistingOrder)
			if err != nil {
				return err
			}
			if config == nil {
				return domain.ErrProjectNotAllocatable
			}
			metricType = string(config.ProductType)
			if config.ProductType == coredomain.ProductTypeDomain && domainSelection != "" {
				var privateSelection bool
				cmd.EmailSuffix, privateSelection, err = normalizeDomainSelection(domainSelection)
				if err != nil {
					return domain.ErrInvalidAllocationRequest
				}
				if privateSelection {
					if !containsSupplyScope(scopes, domain.SupplyScopeOwned) {
						return domain.ErrInvalidAllocationRequest
					}
					scopes = []domain.SupplyScope{domain.SupplyScopeOwned}
				}
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
				// Gmail rotation must skip a concurrently selected resource even when
				// it is the first candidate. Later roots for every type also skip so the
				// shared wallet -> resource lock order stays acyclic.
				if allocationType == domain.AllocationTypeGmail || len(lockedRoots) > 0 {
					locked, err := uc.repo.TryLockResourceRoot(lockCtx, resourceID, allocationType)
					if locked {
						lockedRoots[key] = struct{}{}
					} else if err == nil {
						platform.RecordAllocationResourceLockSkip(string(allocationType))
					}
					return locked, err
				}
				locked, err := uc.repo.LockResourceRoot(lockCtx, resourceID, allocationType)
				if locked {
					lockedRoots[key] = struct{}{}
				}
				return locked, err
			}
			productTypes := []coredomain.ProductType{config.ProductType}
			allRoutesDefinitive := config.ProductType == coredomain.ProductTypeMicrosoft
			for _, scope := range scopes {
				attemptCmd := cmd
				attemptCmd.SupplyScope = scope
				for _, productType := range productTypes {
					switch productType {
					case coredomain.ProductTypeMicrosoft:
						result, err = uc.allocateMicrosoft(txCtx, attemptCmd, *config)
					case coredomain.ProductTypeDomain:
						result, err = uc.allocateDomain(txCtx, attemptCmd, *config)
					case coredomain.ProductTypeGmail, coredomain.ProductTypeGmailVariant:
						result, err = uc.allocateGmail(txCtx, attemptCmd, *config)
					case coredomain.ProductTypeICloud:
						result, err = uc.allocateICloud(txCtx, attemptCmd, *config)
					default:
						return domain.ErrProjectNotAllocatable
					}
					if err == nil {
						return nil
					}
					if errors.Is(err, errResourceTypeBusy) {
						return domain.ErrAllocationConflict
					}
					if !errors.Is(err, domain.ErrInsufficientInventory) {
						return err
					}
					allRoutesDefinitive = allRoutesDefinitive && errors.Is(err, domain.ErrDefinitiveInventoryExhausted)
				}
			}
			if allRoutesDefinitive {
				return domain.ErrDefinitiveInventoryExhausted
			}
			return domain.ErrInsufficientInventory
		})
		if err == nil || errors.Is(err, domain.ErrDefinitiveInventoryExhausted) ||
			(!errors.Is(err, domain.ErrInsufficientInventory) && !errors.Is(err, domain.ErrAllocationConflict) && !errors.Is(err, errResourceTypeBusy)) {
			break
		}
		if attempt < attempts-1 {
			time.Sleep(candidateRetryDelay)
		}
	}
	if errors.Is(err, errResourceTypeBusy) {
		err = domain.ErrAllocationConflict
	}
	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, domain.ErrInsufficientInventory
	}
	return result, nil
}

func allocationMetricResult(err error, existing bool) string {
	switch {
	case err == nil && existing:
		return "existing"
	case err == nil:
		return "succeeded"
	case errors.Is(err, domain.ErrInsufficientInventory):
		return "insufficient_inventory"
	case errors.Is(err, domain.ErrAllocationConflict):
		return "conflict"
	case errors.Is(err, domain.ErrInvalidAllocationRequest), errors.Is(err, domain.ErrProjectNotAllocatable):
		return "invalid_request"
	default:
		return "system_failed"
	}
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

func containsSupplyScope(scopes []domain.SupplyScope, want domain.SupplyScope) bool {
	for _, scope := range scopes {
		if scope == want {
			return true
		}
	}
	return false
}

func isRandomSuffixSelector(value string) bool {
	return value == coredomain.RandomMicrosoftSuffixSelector || value == coredomain.RandomDomainSuffixSelector
}

func randomSuffixMatchesProduct(selector string, productType coredomain.ProductType, suffix string) bool {
	switch selector {
	case coredomain.RandomMicrosoftSuffixSelector:
		return productType == coredomain.ProductTypeMicrosoft && coredomain.IsMicrosoftEmailDomain("selector@"+suffix)
	case coredomain.RandomDomainSuffixSelector:
		return productType == coredomain.ProductTypeDomain && suffix != ""
	default:
		return false
	}
}

func chooseWeightedInventorySuffix(inventory map[string]int64, allowed func(string) bool, randomInt64N func(int64) int64) (string, bool) {
	weights := make(map[string]int64, len(inventory))
	total := int64(0)
	for suffix, available := range inventory {
		suffix = normalizeEmailSuffix(suffix)
		if available <= 0 || !allowed(suffix) {
			continue
		}
		weights[suffix] += available
		total += available
	}
	if total <= 0 {
		return "", false
	}
	suffixes := make([]string, 0, len(weights))
	for suffix := range weights {
		suffixes = append(suffixes, suffix)
	}
	sort.Strings(suffixes)
	ticket := randomInt64N(total)
	for _, suffix := range suffixes {
		ticket -= weights[suffix]
		if ticket < 0 {
			return suffix, true
		}
	}
	return "", false
}

func (uc *UseCase) SelectRandomInventorySuffix(ctx context.Context, req ProductSuffixSelectionRequest) (string, error) {
	req.Selector = normalizeEmailSuffix(req.Selector)
	if req.ProductID == 0 || req.BuyerUserID == 0 || !isRandomSuffixSelector(req.Selector) {
		return "", domain.ErrInvalidAllocationRequest
	}
	config, err := uc.repo.LoadProductConfig(ctx, req.ProductID, req.BuyerUserID, req.FulfillExistingOrder)
	if err != nil {
		return "", err
	}
	if config == nil {
		return "", domain.ErrProjectNotAllocatable
	}
	if req.ProjectID != 0 && req.ProjectID != config.ProjectID {
		return "", domain.ErrInvalidAllocationRequest
	}
	scopes := req.SupplyScopes
	if len(scopes) == 0 {
		scopes = []domain.SupplyScope{domain.SupplyScopePublic}
	}
	for i := range scopes {
		scopes[i] = domain.NormalizeSupplyScope(scopes[i])
	}
	return uc.selectRandomInventorySuffix(ctx, *config, req.BuyerUserID, scopes, req.Selector)
}

func (uc *UseCase) selectRandomInventorySuffix(ctx context.Context, config ProductAllocationConfig, buyerUserID uint, scopes []domain.SupplyScope, selector string) (string, error) {
	wantType := coredomain.ProductTypeMicrosoft
	if selector == coredomain.RandomDomainSuffixSelector {
		wantType = coredomain.ProductTypeDomain
	} else if selector != coredomain.RandomMicrosoftSuffixSelector {
		return "", domain.ErrInvalidAllocationRequest
	}
	if config.ProductType != wantType {
		return "", domain.ErrInvalidAllocationRequest
	}
	for _, scope := range scopes {
		inventory, err := uc.productSuffixInventoryForSelection(ctx, config, buyerUserID, scope)
		if err != nil {
			return "", err
		}
		if suffix, ok := chooseWeightedInventorySuffix(inventory, func(suffix string) bool {
			return randomSuffixMatchesProduct(selector, config.ProductType, suffix)
		}, rand.Int64N); ok {
			return suffix, nil
		}
	}
	return "", domain.ErrInsufficientInventory
}

func (uc *UseCase) productSuffixInventoryForSelection(ctx context.Context, config ProductAllocationConfig, buyerUserID uint, scope domain.SupplyScope) (map[string]int64, error) {
	if scope != domain.SupplyScopePublic || uc.inventoryCache == nil {
		return uc.repo.ListProductSuffixInventory(ctx, config, buyerUserID, scope)
	}
	totals, err := uc.GetProductInventorySnapshot(ctx, config.ProjectID)
	if err != nil {
		return nil, err
	}
	if totals.Cold {
		return map[string]int64{}, nil
	}
	for _, item := range totals.Items {
		if item.ProductID != config.ProductID {
			continue
		}
		if item.ProductType != config.ProductType {
			return nil, domain.ErrInvalidAllocationRequest
		}
		inventory := make(map[string]int64, len(item.Suffixes))
		for _, suffix := range item.Suffixes {
			inventory[suffix.Suffix] += suffix.PublicAvailable
		}
		return inventory, nil
	}
	// A newly enabled or previously disabled product may not be present in the
	// current shared snapshot yet. Keep that rare path authoritative.
	return uc.repo.ListProductSuffixInventory(ctx, config, buyerUserID, scope)
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
		if cmd.Mailbox == domain.MicrosoftMailboxAlias {
			if mailboxType, explicitBase := historicalAliasMailbox(cmd.Email); mailboxType != domain.MicrosoftMailboxAlias {
				if err := uc.historicalMicrosoftAliases.BackfillExistingAliases(txCtx, cmd.ResourceID, []string{explicitBase}); err != nil {
					return err
				}
				cmd.Mailbox = mailboxType
			}
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

func historicalAliasMailbox(email string) (domain.MicrosoftMailbox, string) {
	exact, plusBase, dotBase, ok := mailbox.AliasForms(email)
	if !ok {
		return domain.MicrosoftMailboxAlias, ""
	}
	if exact != plusBase {
		return domain.MicrosoftMailboxPlus, dotBase
	}
	if plusBase != dotBase {
		return domain.MicrosoftMailboxDot, dotBase
	}
	return domain.MicrosoftMailboxAlias, exact
}

func sameHistoricalMicrosoftAllocation(existing domain.UnifiedAllocation, orderNo string, cmd HistoricalMicrosoftAllocationCommand) bool {
	emailMatches := cmd.Mailbox == domain.MicrosoftMailboxMain || strings.EqualFold(existing.Email, cmd.Email)
	return existing.Type == domain.AllocationTypeMicrosoft && existing.OrderNo == orderNo &&
		existing.ProjectID == cmd.ProjectID && existing.ProductID == cmd.ProductID && existing.ResourceID == cmd.ResourceID &&
		existing.Mailbox == string(cmd.Mailbox) && emailMatches &&
		existing.Status == domain.AllocationStatusReleased
}

func (uc *UseCase) ImportHistoricalGmailAllocation(ctx context.Context, cmd HistoricalGmailAllocationCommand) (*domain.UnifiedAllocation, error) {
	cmd.Email = strings.ToLower(strings.TrimSpace(cmd.Email))
	cmd.CreatedAt = cmd.CreatedAt.UTC()
	cmd.ReleasedAt = cmd.ReleasedAt.UTC()
	if uc == nil || uc.repo == nil || cmd.ProjectID == 0 || cmd.ProductID == 0 || cmd.ResourceID == 0 ||
		cmd.Email == "" || !domain.IsValidGmailMailbox(cmd.Mailbox) || cmd.CreatedAt.IsZero() ||
		cmd.ReleasedAt.IsZero() || cmd.ReleasedAt.Before(cmd.CreatedAt) {
		return nil, domain.ErrInvalidAllocationRequest
	}

	var result *domain.UnifiedAllocation
	err := uc.repo.WithTx(ctx, func(txCtx context.Context) error {
		lockedRoot, err := uc.repo.LockResourceRoot(txCtx, cmd.ResourceID, domain.AllocationTypeGmail)
		if err != nil {
			return err
		}
		if !lockedRoot {
			return domain.ErrInvalidAllocationRequest
		}
		orderNo := historicalGmailAllocationOrderNo(cmd)
		existing, err := uc.repo.FindExistingAllocation(txCtx, orderNo)
		if err != nil {
			return err
		}
		if existing != nil {
			if !sameHistoricalGmailAllocationIdentity(*existing, orderNo, cmd) {
				return domain.ErrAllocationConflict
			}
			result = existing
			return nil
		}
		available, err := uc.repo.IsGmailMailboxAvailable(txCtx, cmd.ResourceID, cmd.ProjectID, cmd.Mailbox, cmd.Email)
		if err != nil {
			return err
		}
		if !available {
			return nil
		}
		if err := uc.repo.CreateOrderGuard(txCtx, orderNo, domain.AllocationTypeGmail); err != nil {
			return err
		}
		releasedAt := cmd.ReleasedAt
		allocation := &domain.GmailAllocation{
			OrderNo: orderNo, ProjectID: cmd.ProjectID, ProductID: cmd.ProductID, ResourceID: cmd.ResourceID,
			SupplyScope: domain.SupplyScopePublic, Mailbox: cmd.Mailbox, ServiceMode: domain.GmailServiceModePurchase,
			Email: cmd.Email, Status: domain.AllocationStatusReleased, CostPointsSnapshot: "0.00",
			CreatedAt: cmd.CreatedAt, ReleasedAt: &releasedAt,
		}
		if err := uc.repo.CreateGmailAllocation(txCtx, allocation); err != nil {
			return err
		}
		unified := domain.UnifiedAllocation{
			Type: domain.AllocationTypeGmail, ID: allocation.ID, OrderNo: allocation.OrderNo,
			ProjectID: allocation.ProjectID, ProductID: allocation.ProductID, ResourceID: allocation.ResourceID,
			SupplyScope: allocation.SupplyScope, Mailbox: string(allocation.Mailbox), Email: allocation.Email,
			Status: allocation.Status, CreatedAt: allocation.CreatedAt, ReleasedAt: allocation.ReleasedAt, Created: true,
		}
		result = &unified
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func historicalGmailAllocationOrderNo(cmd HistoricalGmailAllocationCommand) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d:%d:%s:%s", cmd.ResourceID, cmd.ProjectID, cmd.Mailbox, cmd.Email)))
	return "HIST-GMAIL-" + hex.EncodeToString(sum[:20])
}

func sameHistoricalGmailAllocationIdentity(existing domain.UnifiedAllocation, orderNo string, cmd HistoricalGmailAllocationCommand) bool {
	emailMatches := cmd.Mailbox == domain.GmailMailboxMain || strings.EqualFold(existing.Email, cmd.Email)
	productMatches := existing.ProductID == cmd.ProductID || cmd.Mailbox == domain.GmailMailboxDot || cmd.Mailbox == domain.GmailMailboxPlus
	return existing.Type == domain.AllocationTypeGmail && existing.OrderNo == orderNo &&
		existing.ProjectID == cmd.ProjectID && productMatches && existing.ResourceID == cmd.ResourceID &&
		existing.Mailbox == string(cmd.Mailbox) && emailMatches && existing.Status == domain.AllocationStatusReleased
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

func (uc *UseCase) FindAllocationsByOrders(ctx context.Context, orderNos []string) (map[string]domain.UnifiedAllocation, error) {
	normalized := make([]string, 0, len(orderNos))
	seen := make(map[string]struct{}, len(orderNos))
	for _, orderNo := range orderNos {
		orderNo = strings.TrimSpace(orderNo)
		if orderNo == "" {
			continue
		}
		if _, exists := seen[orderNo]; exists {
			continue
		}
		seen[orderNo] = struct{}{}
		normalized = append(normalized, orderNo)
	}
	if len(normalized) == 0 {
		return map[string]domain.UnifiedAllocation{}, nil
	}
	if reader, ok := uc.repo.(interface {
		FindAllocationsByOrders(context.Context, []string) (map[string]domain.UnifiedAllocation, error)
	}); ok {
		return reader.FindAllocationsByOrders(ctx, normalized)
	}
	result := make(map[string]domain.UnifiedAllocation, len(normalized))
	for _, orderNo := range normalized {
		allocation, err := uc.repo.FindAllocationByOrder(ctx, orderNo)
		if errors.Is(err, domain.ErrAllocationNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		result[orderNo] = *allocation
	}
	return result, nil
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
	stats, err := loadCachedInventory(
		ctx,
		uc.inventoryCache,
		entry,
		func(ctx context.Context) (*InventoryStats, error) {
			return uc.inventoryCache.GetInventoryStats(ctx, projectID)
		},
		func() *InventoryStats { return &InventoryStats{ProjectID: projectID, Cold: true} },
		uc.ScheduleInventoryRefresh,
	)
	if err != nil {
		return nil, err
	}
	if stats != nil {
		if stats.Cold {
			return nil, domain.ErrInventoryRefreshInProgress
		}
		// Legacy placeholders predate Cold and have neither source enabled;
		// authoritative stats always enable at least one configured product type.
		if !stats.Microsoft.Enabled && !stats.Domain.Enabled && !stats.Gmail.Enabled && !stats.ICloud.Enabled {
			_ = uc.ScheduleInventoryRefresh(ctx)
			return nil, domain.ErrInventoryRefreshInProgress
		}
	}
	return stats, nil
}

func (uc *UseCase) GetProductInventoryTotals(ctx context.Context, projectID uint, viewerUserID uint) (*ProjectProductInventoryTotals, error) {
	if projectID == 0 || viewerUserID == 0 {
		return nil, domain.ErrInvalidAllocationRequest
	}
	if err := uc.repo.AssertProjectInventoryAccess(ctx, projectID, viewerUserID); err != nil {
		return nil, err
	}
	snapshot, err := uc.GetProductInventorySnapshot(ctx, projectID)
	if err != nil {
		return nil, err
	}
	privateMicrosoft, err := uc.repo.ListPrivateMicrosoftInventoryTotals(ctx, projectID, viewerUserID)
	if err != nil {
		return nil, err
	}
	privateDomains, err := uc.repo.ListPrivateDomainInventoryTotals(ctx, projectID, viewerUserID)
	if err != nil {
		return nil, err
	}
	privateGmail, err := uc.repo.ListPrivateGmailInventoryTotals(ctx, projectID, viewerUserID)
	if err != nil {
		return nil, err
	}
	privateICloud, err := uc.repo.ListPrivateICloudInventoryTotals(ctx, projectID, viewerUserID)
	if err != nil {
		return nil, err
	}
	if len(privateMicrosoft) == 0 && len(privateDomains) == 0 && len(privateGmail) == 0 && len(privateICloud) == 0 {
		return snapshot, nil
	}
	result := cloneProductInventoryTotals(snapshot)
	mergePrivateProductInventory(result, privateMicrosoft, coredomain.ProductTypeMicrosoft)
	mergePrivateProductInventory(result, privateDomains, coredomain.ProductTypeDomain)
	mergePrivateSingletonInventory(result, privateGmail, coredomain.ProductTypeGmail)
	mergePrivateSingletonInventory(result, privateICloud, coredomain.ProductTypeICloud)
	for i := range result.Items {
		sort.Slice(result.Items[i].Suffixes, func(left, right int) bool {
			return result.Items[i].Suffixes[left].Suffix < result.Items[i].Suffixes[right].Suffix
		})
	}
	return result, nil
}

func mergePrivateSingletonInventory(result *ProjectProductInventoryTotals, inventory []PrivateSingletonInventoryTotal, productType coredomain.ProductType) {
	for _, private := range inventory {
		if private.Available <= 0 {
			continue
		}
		itemIndex := -1
		for i := range result.Items {
			if result.Items[i].ProductID == private.ProductID {
				itemIndex = i
				break
			}
		}
		if itemIndex < 0 {
			itemProductType := productType
			if private.ProductType != "" {
				itemProductType = private.ProductType
			}
			result.Items = append(result.Items, ProductInventoryTotal{ProductID: private.ProductID, ProductType: itemProductType})
			itemIndex = len(result.Items) - 1
		}
		item := &result.Items[itemIndex]
		fixedInventory := item.ProductType == coredomain.ProductTypeGmailVariant || private.ProductType == coredomain.ProductTypeGmailVariant
		mergeAvailable := func(current *int64) int64 {
			if current == nil {
				return private.Available
			}
			if fixedInventory {
				return max(*current, private.Available)
			}
			return *current + private.Available
		}
		codeAvailable := mergeAvailable(item.CodeAvailable)
		item.CodeAvailable = &codeAvailable
		purchaseAvailable := mergeAvailable(item.PurchaseAvailable)
		item.PurchaseAvailable = &purchaseAvailable
		if item.CodePublicAvailable == nil {
			available := int64(0)
			item.CodePublicAvailable = &available
		}
		if item.PurchasePublicAvailable == nil {
			available := int64(0)
			item.PurchasePublicAvailable = &available
		}
		previous := item.TotalAvailable
		if fixedInventory {
			item.TotalAvailable = max(item.TotalAvailable, private.Available)
		} else {
			item.TotalAvailable += private.Available
		}
		result.TotalAvailable += item.TotalAvailable - previous
	}
}

func mergePrivateProductInventory(result *ProjectProductInventoryTotals, inventory []PrivateProductInventoryTotal, productType coredomain.ProductType) {
	for _, private := range inventory {
		if private.Available <= 0 || private.Suffix == "" {
			continue
		}
		itemIndex := -1
		for i := range result.Items {
			if result.Items[i].ProductID == private.ProductID {
				itemIndex = i
				break
			}
		}
		if itemIndex < 0 {
			result.Items = append(result.Items, ProductInventoryTotal{ProductID: private.ProductID, ProductType: productType})
			itemIndex = len(result.Items) - 1
		}
		item := &result.Items[itemIndex]
		suffixIndex := -1
		for i := range item.Suffixes {
			if item.Suffixes[i].Suffix == private.Suffix {
				suffixIndex = i
				break
			}
		}
		if suffixIndex < 0 {
			item.Suffixes = append(item.Suffixes, ProductInventorySuffixTotal{Suffix: private.Suffix})
			suffixIndex = len(item.Suffixes) - 1
		}
		item.Suffixes[suffixIndex].TotalAvailable += private.Available
		item.TotalAvailable += private.Available
		if item.CodeAvailable != nil {
			codeAvailable := *item.CodeAvailable + private.Available
			item.CodeAvailable = &codeAvailable
		}
		if item.PurchaseAvailable != nil {
			purchaseAvailable := *item.PurchaseAvailable + private.Available
			item.PurchaseAvailable = &purchaseAvailable
		}
		result.TotalAvailable += private.Available
	}
}

func cloneProductInventoryTotals(source *ProjectProductInventoryTotals) *ProjectProductInventoryTotals {
	result := *source
	result.Items = append([]ProductInventoryTotal(nil), source.Items...)
	for i := range result.Items {
		result.Items[i].Suffixes = append([]ProductInventorySuffixTotal(nil), source.Items[i].Suffixes...)
		if source.Items[i].CodeAvailable != nil {
			value := *source.Items[i].CodeAvailable
			result.Items[i].CodeAvailable = &value
		}
		if source.Items[i].CodePublicAvailable != nil {
			value := *source.Items[i].CodePublicAvailable
			result.Items[i].CodePublicAvailable = &value
		}
		if source.Items[i].PurchaseAvailable != nil {
			value := *source.Items[i].PurchaseAvailable
			result.Items[i].PurchaseAvailable = &value
		}
		if source.Items[i].PurchasePublicAvailable != nil {
			value := *source.Items[i].PurchasePublicAvailable
			result.Items[i].PurchasePublicAvailable = &value
		}
	}
	return &result
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
		observedAt := time.Now().UTC()
		for _, projectID := range projectIDs {
			totals, err := uc.repo.GetProductInventoryTotals(ctx, projectID)
			if errors.Is(err, domain.ErrProjectNotAllocatable) {
				continue
			}
			if err != nil {
				return nil, err
			}
			if totals.RefreshedAt == nil {
				totals.RefreshedAt = &observedAt
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
	if err := uc.inventoryCache.InitializeInventory(ctx, missing, inventoryCacheHardTTLValue()); err != nil {
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
		if isRandomSuffixSelector(req.EmailSuffix) {
			for _, suffix := range item.Suffixes {
				if !randomSuffixMatchesProduct(req.EmailSuffix, item.ProductType, normalizeEmailSuffix(suffix.Suffix)) {
					continue
				}
				if req.PublicOnly && suffix.PublicAvailable > 0 || !req.PublicOnly && suffix.TotalAvailable > 0 {
					return true, true
				}
			}
			return false, true
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
		if item.ProductType == coredomain.ProductTypeMicrosoft {
			return false, false
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
	// Honor project-level markers written by older instances during a rolling
	// deployment. New allocator misses only schedule the background read model.
	req.PublicOnly = false
	alreadyUnavailable, err := uc.inventoryCache.IsProductUnavailable(ctx, req)
	if err != nil {
		return false, err
	}
	if alreadyUnavailable {
		return true, nil
	}
	// An allocator miss must never synchronously rebuild the project's full
	// suffix inventory. The scheduled read model can correct the snapshot later;
	// the indexed allocator remains the authoritative request-time check.
	if err := uc.inventoryCache.RequeueInventory(ctx, []InventoryCacheEntry{{
		Kind: InventoryCacheProducts, ProjectID: req.ProjectID,
	}}); err != nil {
		return false, err
	}
	return false, uc.ScheduleInventoryRefresh(ctx)
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
	if err := cache.InitializeInventory(ctx, []InventoryCacheEntry{entry}, inventoryCacheHardTTLValue()); err != nil {
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

func (uc *UseCase) RefreshInventoryCache(ctx context.Context) (*InventoryRefreshResult, error) {
	if err := uc.EnsureInventoryRefreshSchedule(ctx); err != nil {
		return nil, err
	}
	return uc.RefreshInventoryCacheBefore(ctx, time.Now())
}

// EnsureInventoryRefreshSchedule lets the backend discover new projects and
// restore schedule entries lost by an interrupted refresh task.
func (uc *UseCase) EnsureInventoryRefreshSchedule(ctx context.Context) error {
	if uc == nil || uc.repo == nil || uc.inventoryCache == nil {
		return nil
	}
	projectIDs, err := uc.repo.ListInventoryProjectIDs(ctx)
	if err != nil {
		return fmt.Errorf("list inventory projects: %w", err)
	}
	projectIDs = uniqueInventoryProjectIDs(projectIDs)
	entries := make([]InventoryCacheEntry, 0, len(projectIDs)*2)
	for _, projectID := range projectIDs {
		entries = append(entries,
			InventoryCacheEntry{Kind: InventoryCacheStats, ProjectID: projectID},
			InventoryCacheEntry{Kind: InventoryCacheProducts, ProjectID: projectID},
		)
	}
	if len(entries) == 0 {
		return nil
	}
	if err := uc.inventoryCache.InitializeInventory(ctx, entries, inventoryCacheHardTTLValue()); err != nil {
		return fmt.Errorf("initialize inventory refresh schedule: %w", err)
	}
	return nil
}

func (uc *UseCase) ListInventoryRefreshes(ctx context.Context) ([]InventoryRefreshItem, error) {
	if uc == nil || uc.repo == nil || uc.inventoryCache == nil {
		return nil, errors.New("inventory refresh is unavailable")
	}
	projects, err := uc.repo.ListInventoryProjects(ctx)
	if err != nil {
		return nil, err
	}
	projectIDs := make([]uint, len(projects))
	for i := range projects {
		projectIDs[i] = projects[i].ID
	}
	states, err := uc.inventoryCache.ListInventoryRefreshStates(ctx, projectIDs)
	if err != nil {
		return nil, err
	}
	items := make([]InventoryRefreshItem, len(projects))
	for i, project := range projects {
		state := states[project.ID]
		state.ProjectID = project.ID
		items[i] = InventoryRefreshItem{InventoryRefreshState: state, ProjectName: project.Name}
	}
	return items, nil
}

func (uc *UseCase) TriggerInventoryRefresh(ctx context.Context, projectID uint) ([]uint, error) {
	if uc == nil || uc.repo == nil || uc.inventoryCache == nil {
		return nil, errors.New("inventory refresh is unavailable")
	}
	projects, err := uc.repo.ListInventoryProjects(ctx)
	if err != nil {
		return nil, err
	}
	projectIDs := make([]uint, 0, len(projects))
	for _, project := range projects {
		if projectID == 0 || project.ID == projectID {
			projectIDs = append(projectIDs, project.ID)
		}
	}
	if projectID > 0 && len(projectIDs) == 0 {
		return nil, domain.ErrProjectNotAllocatable
	}
	entries := make([]InventoryCacheEntry, 0, len(projectIDs)*2)
	for _, id := range projectIDs {
		entries = append(entries,
			InventoryCacheEntry{Kind: InventoryCacheStats, ProjectID: id},
			InventoryCacheEntry{Kind: InventoryCacheProducts, ProjectID: id},
		)
	}
	if err := uc.inventoryCache.ClearInventoryRefreshFailures(ctx, entries); err != nil {
		return nil, err
	}
	if err := uc.inventoryCache.RequeueInventory(ctx, entries); err != nil {
		return nil, err
	}
	if err := uc.ScheduleInventoryRefreshContinuation(ctx); err != nil {
		return nil, err
	}
	return projectIDs, nil
}

// RefreshInventoryCacheBefore refreshes entries whose backend schedule is due
// before one task's fixed cutoff.
func (uc *UseCase) RefreshInventoryCacheBefore(ctx context.Context, before time.Time) (*InventoryRefreshResult, error) {
	if uc == nil || uc.inventoryCache == nil {
		return &InventoryRefreshResult{}, nil
	}
	if before.IsZero() {
		before = time.Now()
	}
	entries, err := uc.inventoryCache.ClaimDueInventory(ctx, before, inventoryRefreshBatchSize)
	if err != nil {
		return nil, fmt.Errorf("claim due inventory cache entries: %w", err)
	}
	result := &InventoryRefreshResult{Attempted: len(entries)}
	for i, entry := range entries {
		if err := ctx.Err(); err != nil {
			_ = requeueInventory(uc.inventoryCache, entries[i:])
			return result, err
		}
		token, acquired, err := uc.inventoryCache.AcquireInventoryRefresh(ctx, entry, inventoryRefreshLockTTL)
		if err != nil {
			result.Failed++
			recordErr := recordInventoryRefreshFailure(uc.inventoryCache, entry, err)
			requeueErr := requeueInventory(uc.inventoryCache, []InventoryCacheEntry{entry})
			result.LastError = errors.Join(err, recordErr, requeueErr)
			if requeueErr != nil {
				return result, result.LastError
			}
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
				err = uc.inventoryCache.RefreshInventoryStats(ctx, entry.ProjectID, stats, inventoryCacheHardTTLValue())
			}
		case InventoryCacheProducts:
			totals, refreshErr := uc.repo.GetProductInventoryTotals(ctx, entry.ProjectID)
			err = refreshErr
			if errors.Is(err, domain.ErrProjectNotAllocatable) || (err == nil && totals == nil) {
				err = uc.inventoryCache.DeleteInventory(ctx, entry)
				removed = err == nil
			} else if err == nil {
				err = uc.inventoryCache.RefreshProductInventoryTotals(ctx, entry.ProjectID, totals, inventoryCacheHardTTLValue())
			}
		default:
			err = uc.inventoryCache.DeleteInventory(ctx, entry)
			removed = err == nil
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		releaseErr := uc.inventoryCache.ReleaseInventoryRefresh(cleanupCtx, entry, token)
		var clearErr error
		if err == nil && releaseErr == nil {
			clearErr = uc.inventoryCache.ClearInventoryRefreshFailure(cleanupCtx, entry)
		}
		cancel()
		if err == nil {
			err = errors.Join(releaseErr, clearErr)
		}
		if err != nil {
			result.Failed++
			recordErr := recordInventoryRefreshFailure(uc.inventoryCache, entry, err)
			requeueErr := requeueInventory(uc.inventoryCache, []InventoryCacheEntry{entry})
			result.LastError = errors.Join(err, recordErr, requeueErr)
			if requeueErr != nil {
				return result, result.LastError
			}
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

func recordInventoryRefreshFailure(cache InventoryCache, entry InventoryCacheEntry, refreshErr error) error {
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return cache.RecordInventoryRefreshFailure(cleanupCtx, entry, refreshErr)
}

func (uc *UseCase) ScheduleInventoryRefresh(ctx context.Context) error {
	if uc == nil || uc.queue == nil {
		return nil
	}
	return uc.queue.EnqueueInventoryRefresh(ctx)
}

func (uc *UseCase) ScheduleInventoryRefreshContinuation(ctx context.Context) error {
	if uc == nil || uc.queue == nil {
		return nil
	}
	return uc.queue.EnqueueInventoryRefreshContinuation(ctx)
}

func (uc *UseCase) allocateMicrosoft(ctx context.Context, cmd AllocateCommand, config ProductAllocationConfig) (*domain.UnifiedAllocation, error) {
	return uc.allocateMicrosoftOnce(ctx, cmd, config)
}

func (uc *UseCase) allocateGmail(ctx context.Context, cmd AllocateCommand, config ProductAllocationConfig) (*domain.UnifiedAllocation, error) {
	if cmd.EmailSuffix != "" || !domain.IsValidGmailServiceMode(cmd.ServiceMode) {
		return nil, domain.ErrInvalidAllocationRequest
	}
	cost, err := gmailAllocationCost(config, cmd.ServiceMode, cmd.SupplyScope)
	if err != nil {
		return nil, err
	}
	preferences := gmailMailboxPreferences(cmd.OrderNo, config)
	if len(preferences) == 0 {
		return nil, domain.ErrProjectNotAllocatable
	}
	now := time.Now().UTC()
	resourceBusy := false
	for _, mailbox := range preferences {
		result, busy, err := uc.tryGmailCandidates(ctx, cmd, config, mailbox, cost, now)
		if err != nil {
			return nil, err
		}
		resourceBusy = resourceBusy || busy
		if result != nil {
			return result, nil
		}
	}
	if resourceBusy {
		return nil, errResourceTypeBusy
	}
	return nil, domain.ErrInsufficientInventory
}

func gmailAllocationCost(config ProductAllocationConfig, mode domain.GmailServiceMode, scope domain.SupplyScope) (string, error) {
	enabled, value := config.CodeEnabled, config.CodeSupplierPrice
	if mode == domain.GmailServiceModePurchase {
		enabled, value = config.PurchaseEnabled, config.PurchaseSupplierPrice
	}
	if !enabled {
		return "", domain.ErrProjectNotAllocatable
	}
	cost, err := moneyfmt.Parse(value)
	if err != nil || cost.IsNegative() {
		return "", domain.ErrProjectNotAllocatable
	}
	if scope == domain.SupplyScopeOwned {
		return "0.00", nil
	}
	return moneyfmt.Format(cost), nil
}

func (uc *UseCase) tryGmailCandidates(
	ctx context.Context,
	cmd AllocateCommand,
	config ProductAllocationConfig,
	mailbox domain.GmailMailbox,
	cost string,
	now time.Time,
) (*domain.UnifiedAllocation, bool, error) {
	limit := globalCandidateWindowValue()
	resourceBusy := false
	var after *GmailCandidate
	for {
		candidates, err := uc.repo.ListGmailSourceCandidates(
			ctx, config.ProjectID, cmd.BuyerUserID, cmd.SupplyScope, mailbox, after, limit,
		)
		if err != nil {
			return nil, false, err
		}
		cooling := make(map[uint]bool)
		if mailbox != domain.GmailMailboxMain && uc.gmailVariantCooldown != nil && len(candidates) > 0 {
			resourceIDs := make([]uint, len(candidates))
			for i, candidate := range candidates {
				resourceIDs[i] = candidate.ResourceID
			}
			coolingIDs, err := uc.gmailVariantCooldown.CoolingResourceIDs(ctx, config.ProjectID, resourceIDs)
			if err != nil {
				return nil, false, err
			}
			for _, resourceID := range coolingIDs {
				cooling[resourceID] = true
			}
		}
		for _, candidate := range candidates {
			if cooling[candidate.ResourceID] {
				continue
			}
			platform.AddAllocationCandidateAttempts(string(domain.AllocationTypeGmail), 1)
			result, err := uc.tryGmailCandidate(ctx, cmd, config, mailbox, candidate, cost, now)
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
			return nil, false, err
		}
		// Keep the unfiltered page as the cursor: a cooling page is not exhaustion.
		if len(candidates) < limit {
			return nil, resourceBusy, nil
		}
		cursor := candidates[len(candidates)-1]
		after = &cursor
	}
}

func (uc *UseCase) tryGmailCandidate(
	ctx context.Context,
	cmd AllocateCommand,
	config ProductAllocationConfig,
	mailbox domain.GmailMailbox,
	candidate GmailCandidate,
	cost string,
	now time.Time,
) (*domain.UnifiedAllocation, error) {
	lockRoot := uc.repo.LockResourceRoot
	if cmd.lockResourceRoot != nil {
		lockRoot = cmd.lockResourceRoot
	}
	lockedRoot, err := lockRoot(ctx, candidate.ResourceID, domain.AllocationTypeGmail)
	if err != nil {
		return nil, err
	}
	if !lockedRoot {
		return nil, errResourceRootBusy
	}
	locked, err := uc.repo.LockGmailCandidate(
		ctx, candidate.ResourceID, config.ProjectID, cmd.BuyerUserID, cmd.SupplyScope, mailbox,
	)
	if err != nil {
		return nil, err
	}
	if locked == nil {
		platform.RecordAllocationCandidateRecheckMiss(string(domain.AllocationTypeGmail))
		return nil, errCandidateUnavailable
	}
	candidate = *locked

	var emails []string
	switch mailbox {
	case domain.GmailMailboxMain:
		emails = []string{gmailPrimaryAddress(candidate.Email)}
	case domain.GmailMailboxDot:
		historyCount, err := uc.repo.CountGmailDotHistory(ctx, candidate.ResourceID, config.ProjectID)
		if err != nil {
			return nil, err
		}
		capacity := gmailDotAliasCapacity(candidate.Email)
		if capacity == 0 || historyCount >= capacity {
			return nil, errCandidateUnavailable
		}
		// Any historyCount+1 distinct candidates contain a free alias: there are
		// only historyCount historical aliases. Scan in bounded query-sized pages
		// so fragmented legacy history cannot pin generation to one full window.
		// ponytail: O(history/window) DB batches; persist a per-project cursor or
		// used-mask set only if a single resource's measured history makes this slow.
		scanLimit := min(historyCount+1, capacity)
		window := uint64(aliasGenerationWindowValue())
		for scanned := uint64(0); scanned < scanLimit; {
			batchSize := min(window, scanLimit-scanned)
			batch := gmailDotAliasVariantBatch(candidate.Email, historyCount+scanned, int(batchSize))
			if len(batch) == 0 {
				break
			}
			unavailable, err := uc.repo.ListUnavailableGmailMailboxEmails(ctx, config.ProjectID, mailbox, batch)
			if err != nil {
				return nil, err
			}
			for _, email := range batch {
				email = strings.ToLower(strings.TrimSpace(email))
				if _, exists := unavailable[email]; !exists {
					return uc.createGmailAllocation(ctx, cmd, config, candidate.ResourceID, mailbox, email, cost, now)
				}
			}
			scanned += uint64(len(batch))
		}
		return nil, errCandidateUnavailable
	case domain.GmailMailboxPlus:
		emails = gmailPlusAliasVariants(candidate.Email, cmd.OrderNo)
	default:
		return nil, domain.ErrInvalidAllocationRequest
	}
	for _, email := range emails {
		email = strings.ToLower(strings.TrimSpace(email))
		if email == "" || len(email) > 320 {
			continue
		}
		available, err := uc.repo.IsGmailMailboxAvailable(ctx, candidate.ResourceID, config.ProjectID, mailbox, email)
		if err != nil {
			return nil, err
		}
		if available {
			return uc.createGmailAllocation(ctx, cmd, config, candidate.ResourceID, mailbox, email, cost, now)
		}
	}
	return nil, errCandidateUnavailable
}

func (uc *UseCase) createGmailAllocation(
	ctx context.Context,
	cmd AllocateCommand,
	config ProductAllocationConfig,
	resourceID uint,
	mailbox domain.GmailMailbox,
	email, cost string,
	now time.Time,
) (*domain.UnifiedAllocation, error) {
	if cmd.ensureOrderGuard == nil {
		return nil, domain.ErrAllocationTxRequired
	}
	allocation := &domain.GmailAllocation{
		OrderNo: cmd.OrderNo, ProjectID: config.ProjectID, ProductID: config.ProductID,
		ResourceID: resourceID, SupplyScope: cmd.SupplyScope, Mailbox: mailbox,
		ServiceMode: cmd.ServiceMode, Email: strings.ToLower(strings.TrimSpace(email)),
		Status: domain.AllocationStatusAllocated, CostPointsSnapshot: cost, CreatedAt: now,
	}
	if allocation.Email == "" || !domain.IsValidGmailMailbox(allocation.Mailbox) {
		return nil, domain.ErrInvalidAllocationRequest
	}
	if mailbox != domain.GmailMailboxMain && uc.gmailVariantCooldown != nil {
		started, err := uc.gmailVariantCooldown.StartVariantCooldown(ctx, resourceID, config.ProjectID)
		if err != nil {
			return nil, err
		}
		if !started {
			return nil, errCandidateUnavailable
		}
	}
	if err := cmd.ensureOrderGuard(ctx, domain.AllocationTypeGmail); err != nil {
		return nil, err
	}
	if err := uc.repo.CreateGmailAllocation(ctx, allocation); err != nil {
		return nil, err
	}
	if err := uc.repo.TouchGmailAllocated(ctx, resourceID, now); err != nil {
		return nil, err
	}
	return &domain.UnifiedAllocation{
		Type: domain.AllocationTypeGmail, ID: allocation.ID, OrderNo: allocation.OrderNo,
		ProjectID: allocation.ProjectID, ProductID: allocation.ProductID, ResourceID: allocation.ResourceID,
		SupplyScope: allocation.SupplyScope, Mailbox: string(allocation.Mailbox), Email: allocation.Email,
		Status: allocation.Status, CreatedAt: allocation.CreatedAt,
	}, nil
}

func (uc *UseCase) allocateICloud(ctx context.Context, cmd AllocateCommand, config ProductAllocationConfig) (*domain.UnifiedAllocation, error) {
	now := time.Now().UTC()
	requiredUntil := cmd.RequiredUntil.UTC()
	if requiredUntil.Before(now) {
		requiredUntil = now
	}
	candidates, err := uc.repo.ListICloudSourceCandidates(
		ctx, config.ProjectID, cmd.BuyerUserID, cmd.SupplyScope, requiredUntil, globalCandidateWindowValue(),
	)
	if err != nil {
		return nil, err
	}
	resourceBusy := false
	for _, candidate := range candidates {
		platform.AddAllocationCandidateAttempts(string(domain.AllocationTypeICloud), 1)
		result, err := uc.tryICloudCandidate(ctx, cmd, config, candidate, requiredUntil, now)
		if err == nil && result != nil {
			return result, nil
		}
		if errors.Is(err, errResourceRootBusy) {
			resourceBusy = true
			continue
		}
		if errors.Is(err, errCandidateUnavailable) || errors.Is(err, domain.ErrInsufficientInventory) {
			continue
		}
		return nil, err
	}
	if resourceBusy {
		return nil, errResourceTypeBusy
	}
	return nil, domain.ErrInsufficientInventory
}

func (uc *UseCase) tryICloudCandidate(ctx context.Context, cmd AllocateCommand, config ProductAllocationConfig, candidate ICloudCandidate, requiredUntil, now time.Time) (*domain.UnifiedAllocation, error) {
	lockRoot := uc.repo.LockResourceRoot
	if cmd.lockResourceRoot != nil {
		lockRoot = cmd.lockResourceRoot
	}
	lockedRoot, err := lockRoot(ctx, candidate.ResourceID, domain.AllocationTypeICloud)
	if err != nil {
		return nil, err
	}
	if !lockedRoot {
		return nil, errResourceRootBusy
	}
	locked, err := uc.repo.LockICloudCandidate(
		ctx, candidate.ResourceID, candidate.AliasID, config.ProjectID, cmd.BuyerUserID, cmd.SupplyScope, requiredUntil,
	)
	if err != nil {
		return nil, err
	}
	if locked == nil {
		platform.RecordAllocationCandidateRecheckMiss(string(domain.AllocationTypeICloud))
		return nil, errCandidateUnavailable
	}
	if cmd.ensureOrderGuard == nil {
		return nil, domain.ErrAllocationTxRequired
	}
	allocation := &domain.ICloudAllocation{
		OrderNo: cmd.OrderNo, ProjectID: config.ProjectID, ProductID: config.ProductID,
		ResourceID: locked.ResourceID, AliasID: locked.AliasID, SupplyScope: cmd.SupplyScope,
		Email: strings.ToLower(strings.TrimSpace(locked.Email)), Status: domain.AllocationStatusAllocated,
	}
	if allocation.Email == "" {
		return nil, domain.ErrInvalidAllocationRequest
	}
	if err := cmd.ensureOrderGuard(ctx, domain.AllocationTypeICloud); err != nil {
		return nil, err
	}
	if err := uc.repo.CreateICloudAllocation(ctx, allocation); err != nil {
		return nil, err
	}
	if err := uc.repo.TouchICloudAllocated(ctx, allocation.ResourceID, allocation.AliasID, now); err != nil {
		return nil, err
	}
	return &domain.UnifiedAllocation{
		Type: domain.AllocationTypeICloud, ID: allocation.ID, OrderNo: allocation.OrderNo,
		ProjectID: allocation.ProjectID, ProductID: allocation.ProductID, ResourceID: allocation.ResourceID,
		SupplyScope: allocation.SupplyScope, Mailbox: "alias", Email: allocation.Email,
		Status: allocation.Status, CreatedAt: allocation.CreatedAt,
	}, nil
}

func (uc *UseCase) allocateMicrosoftOnce(ctx context.Context, cmd AllocateCommand, config ProductAllocationConfig) (*domain.UnifiedAllocation, error) {
	preferences := microsoftMailboxPreferences(cmd.OrderNo, config)
	now := time.Now().UTC()
	resourceBusy := false
	definitiveExhausted := len(preferences) > 0
	for _, mailbox := range preferences {
		buckets := bucketProbeSequence(cmd.OrderNo, config.ProjectID, string(mailbox), MicrosoftBucketCount)
		for _, bucket := range buckets {
			result, busy, _, err := uc.tryMicrosoftBucket(ctx, cmd, config, mailbox, &bucket, now)
			if err != nil {
				return nil, err
			}
			resourceBusy = resourceBusy || busy
			if result != nil {
				return result, nil
			}
		}
		// ponytail: bucket=nil preserves correctness but may scan a dense exhausted
		// suffix; materialize project/scope/suffix availability only if that ceiling matters.
		platform.RecordAllocationBucketFallback(string(domain.AllocationTypeMicrosoft), "probes_exhausted")
		result, busy, empty, err := uc.tryMicrosoftBucket(ctx, cmd, config, mailbox, nil, now)
		if err != nil {
			return nil, err
		}
		definitiveExhausted = definitiveExhausted && empty
		resourceBusy = resourceBusy || busy
		if result != nil {
			return result, nil
		}
	}
	if resourceBusy {
		return nil, errResourceTypeBusy
	}
	if definitiveExhausted {
		return nil, domain.ErrDefinitiveInventoryExhausted
	}
	return nil, domain.ErrInsufficientInventory
}

func (uc *UseCase) tryMicrosoftBucket(ctx context.Context, cmd AllocateCommand, config ProductAllocationConfig, mailbox domain.MicrosoftMailbox, bucket *uint16, now time.Time) (*domain.UnifiedAllocation, bool, bool, error) {
	limit := candidateWindowSizeValue()
	if bucket == nil {
		limit = globalCandidateWindowValue()
	}
	candidates, err := uc.repo.ListMicrosoftSourceCandidates(ctx, config.ProjectID, cmd.BuyerUserID, cmd.SupplyScope, mailbox, bucket, limit, cmd.EmailSuffix)
	if err != nil {
		return nil, false, false, err
	}
	if len(candidates) == 0 {
		return nil, false, true, nil
	}
	resourceBusy := false
	for _, candidate := range candidates {
		platform.AddAllocationCandidateAttempts(string(domain.AllocationTypeMicrosoft), 1)
		result, err := uc.tryMicrosoftCandidate(ctx, cmd, config, mailbox, candidate, now)
		if err == nil && result != nil {
			return result, false, false, nil
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
		return nil, false, false, err
	}
	return nil, resourceBusy, false, nil
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
		platform.RecordAllocationCandidateRecheckMiss(string(domain.AllocationTypeMicrosoft))
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
	resourceBusy := false
	privateMailbox := isPrivateDomainMailboxSelection(cmd.EmailSuffix)
	if cmd.EmailSuffix == "" || isPrivateDomainSelection(cmd.EmailSuffix) {
		result, busy, err := uc.tryReusableDomainMailboxes(ctx, cmd, config)
		if err != nil || result != nil {
			return result, err
		}
		resourceBusy = busy
		if privateMailbox {
			if resourceBusy {
				return nil, errResourceTypeBusy
			}
			return nil, domain.ErrInsufficientInventory
		}
	}
	result, err := uc.generateDomainMailboxOnce(ctx, cmd, config)
	if errors.Is(err, domain.ErrInsufficientInventory) && resourceBusy {
		return nil, errResourceTypeBusy
	}
	return result, err
}

func (uc *UseCase) tryReusableDomainMailboxes(ctx context.Context, cmd AllocateCommand, config ProductAllocationConfig) (*domain.UnifiedAllocation, bool, error) {
	now := time.Now().UTC()
	if isPrivateDomainMailboxSelection(cmd.EmailSuffix) {
		bucket := coredomain.GeneratedMailboxBucket(cmd.EmailSuffix)
		result, busy, _, err := uc.tryGeneratedMailboxBucket(ctx, cmd, config, &bucket, now)
		return result, busy, err
	}
	resourceBusy := false
	buckets := bucketProbeSequence(cmd.OrderNo, config.ProjectID, "generated-domain", GeneratedMailboxBucketCount)
	for _, bucket := range buckets {
		result, busy, _, err := uc.tryGeneratedMailboxBucket(ctx, cmd, config, &bucket, now)
		if err != nil {
			return nil, false, err
		}
		resourceBusy = resourceBusy || busy
		if result != nil {
			return result, resourceBusy, nil
		}
	}
	return nil, resourceBusy, nil
}

func (uc *UseCase) tryGeneratedMailboxBucket(ctx context.Context, cmd AllocateCommand, config ProductAllocationConfig, bucket *uint16, now time.Time) (*domain.UnifiedAllocation, bool, bool, error) {
	candidates, err := uc.repo.ListGeneratedMailboxCandidates(ctx, config.ProjectID, cmd.BuyerUserID, cmd.SupplyScope, bucket, candidateWindowSizeValue(), cmd.EmailSuffix)
	if err != nil {
		return nil, false, false, err
	}
	if len(candidates) == 0 {
		return nil, false, false, nil
	}
	resourceBusy := false
	for _, candidate := range candidates {
		platform.AddAllocationCandidateAttempts(string(domain.AllocationTypeDomain), 1)
		result, err := uc.tryGeneratedMailboxCandidate(ctx, cmd, config, candidate, now)
		if err == nil && result != nil {
			return result, false, true, nil
		}
		if errors.Is(err, errResourceRootBusy) {
			resourceBusy = true
			continue
		}
		if errors.Is(err, domain.ErrInsufficientInventory) || errors.Is(err, errCandidateUnavailable) {
			continue
		}
		return nil, false, true, err
	}
	return nil, resourceBusy, true, nil
}

func (uc *UseCase) tryGeneratedMailboxCandidate(ctx context.Context, cmd AllocateCommand, config ProductAllocationConfig, candidate GeneratedMailboxCandidate, now time.Time) (*domain.UnifiedAllocation, error) {
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

	domainCandidate, err := uc.repo.LockDomainCandidate(ctx, candidate.ResourceID, cmd.BuyerUserID, cmd.SupplyScope, cmd.EmailSuffix)
	if err != nil {
		return nil, err
	}
	if domainCandidate == nil {
		platform.RecordAllocationCandidateRecheckMiss(string(domain.AllocationTypeDomain))
		return nil, errCandidateUnavailable
	}
	mailbox, err := uc.repo.LockGeneratedMailboxCandidate(ctx, candidate.ID, candidate.ResourceID, config.ProjectID)
	if err != nil {
		return nil, err
	}
	if mailbox == nil {
		platform.RecordAllocationCandidateRecheckMiss(string(domain.AllocationTypeDomain))
		return nil, errCandidateUnavailable
	}

	dailyUsage := DailyUsageReservation{
		UsageDate:      allocationUsageDate(now),
		AllocationType: domain.AllocationTypeDomain,
		ResourceID:     candidate.ResourceID,
		Kind:           domain.DailyUsageKindDomainMailbox,
		Limit:          domainCandidate.MailboxDailyLimit,
	}
	if err := uc.repo.EnsureDailyUsageAvailable(ctx, dailyUsage.UsageDate, dailyUsage.AllocationType, dailyUsage.ResourceID, dailyUsage.Kind, dailyUsage.Limit); err != nil {
		return nil, err
	}
	return uc.createDomainAllocation(ctx, cmd, config, candidate.ResourceID, mailbox.ID, mailbox.Email, now, &dailyUsage)
}

func (uc *UseCase) generateDomainMailboxOnce(ctx context.Context, cmd AllocateCommand, config ProductAllocationConfig) (*domain.UnifiedAllocation, error) {
	now := time.Now().UTC()
	resourceBusy := false
	buckets := bucketProbeSequence(cmd.OrderNo, config.ProjectID, "domain", DomainBucketCount)
	for _, bucket := range buckets {
		result, busy, _, err := uc.tryDomainBucket(ctx, cmd, config, &bucket, now)
		if err != nil {
			return nil, err
		}
		resourceBusy = resourceBusy || busy
		if result != nil {
			return result, nil
		}
	}
	platform.RecordAllocationBucketFallback(string(domain.AllocationTypeDomain), "probes_exhausted")
	result, busy, _, err := uc.tryDomainBucket(ctx, cmd, config, nil, now)
	if err != nil {
		return nil, err
	}
	resourceBusy = resourceBusy || busy
	if result != nil {
		return result, nil
	}
	if resourceBusy {
		return nil, errResourceTypeBusy
	}
	return nil, domain.ErrInsufficientInventory
}

func (uc *UseCase) tryDomainBucket(ctx context.Context, cmd AllocateCommand, config ProductAllocationConfig, bucket *uint16, now time.Time) (*domain.UnifiedAllocation, bool, bool, error) {
	limit := candidateWindowSizeValue()
	if bucket == nil {
		limit = globalCandidateWindowValue()
	}
	candidates, err := uc.repo.ListDomainSourceCandidates(ctx, cmd.BuyerUserID, cmd.SupplyScope, bucket, limit, cmd.EmailSuffix)
	if err != nil {
		return nil, false, false, err
	}
	if len(candidates) == 0 {
		return nil, false, false, nil
	}
	resourceBusy := false
	for _, candidate := range candidates {
		platform.AddAllocationCandidateAttempts(string(domain.AllocationTypeDomain), 1)
		result, err := uc.tryDomainCandidate(ctx, cmd, config, candidate, now)
		if err == nil && result != nil {
			return result, false, true, nil
		}
		if errors.Is(err, errResourceRootBusy) {
			resourceBusy = true
			continue
		}
		if errors.Is(err, domain.ErrInsufficientInventory) || errors.Is(err, errCandidateUnavailable) {
			continue
		}
		// Domain allocation conflicts have the same failed-INSERT lock lifetime.
		return nil, false, true, err
	}
	return nil, resourceBusy, true, nil
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
		platform.RecordAllocationCandidateRecheckMiss(string(domain.AllocationTypeDomain))
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
		historicallyAllocated, err := uc.repo.IsDomainEmailHistoricallyAllocated(ctx, config.ProjectID, mailbox.Email)
		if err != nil {
			return nil, err
		}
		if historicallyAllocated {
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
	if cmd.EmailSuffix != "" {
		selection := strings.TrimSpace(cmd.EmailSuffix)
		matches := strings.EqualFold(strings.TrimSpace(email), selection)
		if isConcreteDomainSelection(selection) {
			_, host, valid := splitEmail(email)
			matches = valid && strings.EqualFold(host, selection)
		} else if !isPrivateDomainMailboxSelection(selection) {
			_, suffix, valid := splitEmail(email)
			matches = valid && normalizeEmailSuffix(coredomain.TLD(suffix)) == normalizeEmailSuffix(selection)
		}
		if !matches {
			return nil, errCandidateUnavailable
		}
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

func gmailMailboxPreferences(orderNo string, config ProductAllocationConfig) []domain.GmailMailbox {
	switch config.ProductType {
	case coredomain.ProductTypeGmail:
		return []domain.GmailMailbox{domain.GmailMailboxMain}
	case coredomain.ProductTypeGmailVariant:
		if hash64(orderNo+"|gmail-special")%2 == 0 {
			return []domain.GmailMailbox{domain.GmailMailboxDot, domain.GmailMailboxPlus}
		}
		return []domain.GmailMailbox{domain.GmailMailboxPlus, domain.GmailMailboxDot}
	default:
		return nil
	}
}

func bucketProbeSequence(orderNo string, projectID uint, kind string, bucketCount uint16) []uint16 {
	start := uint16(hash64(orderNo+"|"+strconv.Itoa(int(projectID))+"|"+kind) % uint64(bucketCount))
	probeCount := min(bucketProbeCountValue(), int(bucketCount))
	result := make([]uint16, 0, probeCount)
	for i := 0; i < probeCount; i++ {
		result = append(result, uint16((int(start)+i)%int(bucketCount)))
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
	if capacity := DotAliasCapacityPerResourceValue(); limit > capacity {
		limit = capacity
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

func gmailDotAliasParts(email string) ([]rune, string, uint64, uint64, bool) {
	local, domainPart, ok := splitEmail(email)
	_, _, domainsOK := gmailAliasDomains(domainPart)
	characters := make([]rune, 0, len(local))
	originalMask := uint64(0)
	for _, character := range local {
		if character == '.' {
			if len(characters) > 0 {
				originalMask |= uint64(1) << (len(characters) - 1)
			}
			continue
		}
		characters = append(characters, character)
	}
	if !ok || !domainsOK || len(characters) < 2 || len(characters) > GmailDotMaxLocalCharacters {
		return nil, "", 0, 0, false
	}
	// Every dot mask exists on both equivalent domains. Exclude only the
	// resource's original address, which belongs to the primary Gmail product.
	aliasCount := (uint64(1) << len(characters)) - 1
	return characters, domainPart, originalMask, aliasCount, true
}

func gmailDotAliasCapacity(email string) uint64 {
	_, _, _, capacity, ok := gmailDotAliasParts(email)
	if !ok {
		return 0
	}
	return capacity
}

func gmailDotAliasVariants(email string, offset uint64) []string {
	return gmailDotAliasVariantBatch(email, offset, aliasGenerationWindowValue())
}

func gmailDotAliasVariantBatch(email string, offset uint64, limit int) []string {
	characters, domainPart, originalMask, aliasCount, ok := gmailDotAliasParts(email)
	if !ok || limit <= 0 {
		return nil
	}
	primaryDomain, alternateDomain, _ := gmailAliasDomains(domainPart)
	masksPerDomain := uint64(1) << (len(characters) - 1)
	if uint64(limit) > aliasCount {
		limit = int(aliasCount)
	}
	result := make([]string, 0, limit)
	for i := uint64(0); i < aliasCount && len(result) < limit; i++ {
		position := (offset + i) % aliasCount
		mask, aliasDomain := originalMask, alternateDomain
		if position > 0 {
			maskPosition := (position - 1) % (masksPerDomain - 1)
			if maskPosition >= originalMask {
				maskPosition++
			}
			mask = maskPosition
			if position < masksPerDomain {
				aliasDomain = primaryDomain
			}
		}
		var alias strings.Builder
		for i, character := range characters {
			alias.WriteRune(character)
			if i < len(characters)-1 && mask&(uint64(1)<<i) != 0 {
				alias.WriteByte('.')
			}
		}
		alias.WriteByte('@')
		alias.WriteString(aliasDomain)
		result = append(result, alias.String())
	}
	return result
}

func gmailAliasDomains(domainPart string) (string, string, bool) {
	switch strings.ToLower(strings.TrimSpace(domainPart)) {
	case "gmail.com", "googlemail.com":
		return "gmail.com", "googlemail.com", true
	default:
		return "", "", false
	}
}

func gmailPrimaryAddress(email string) string {
	local, domainPart, ok := splitEmail(email)
	_, _, domainsOK := gmailAliasDomains(domainPart)
	if !ok || !domainsOK || local == "" {
		return ""
	}
	return local + "@gmail.com"
}

func allocationUsageDate(value time.Time) string {
	return value.UTC().Format("2006-01-02")
}

func normalizeEmailSuffix(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.TrimPrefix(value, "@")
	return strings.TrimSuffix(strings.TrimPrefix(value, "."), ".")
}

func normalizeDomainSelection(value string) (string, bool, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if strings.Contains(value, "@") && !strings.HasPrefix(value, "@") {
		email, err := coredomain.NormalizeDomainMailbox(value)
		return email, true, err
	}
	if suffix, err := coredomain.NormalizeDomainTLD(value); err == nil {
		return suffix, false, nil
	}
	host, err := coredomain.NormalizeDomainName(value)
	return host, true, err
}

func isPrivateDomainMailboxSelection(value string) bool {
	return strings.Contains(value, "@") && !strings.HasPrefix(value, "@")
}

func isConcreteDomainSelection(value string) bool {
	if value == "" || isPrivateDomainMailboxSelection(value) {
		return false
	}
	_, err := coredomain.NormalizeDomainTLD(value)
	return err != nil
}

func isPrivateDomainSelection(value string) bool {
	return isPrivateDomainMailboxSelection(value) || isConcreteDomainSelection(value)
}

func plusAliasVariants(email string, projectID uint, orderNo string) []string {
	local, domainPart, ok := splitEmail(email)
	if !ok || local == "" {
		return nil
	}
	base := strconv.FormatUint(uint64(projectID), 36) + strconv.FormatUint(hash64(orderNo), 36)
	window := aliasGenerationWindowValue()
	result := make([]string, 0, window)
	for i := 0; i < window; i++ {
		result = append(result, local+"+p"+base+strconv.FormatInt(int64(i), 36)+"@"+domainPart)
	}
	return result
}

const (
	gmailPlusAliasLetters = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"
	gmailPlusAliasDigits  = "0123456789"
	gmailPlusAliasChars   = gmailPlusAliasLetters + gmailPlusAliasDigits
)

func gmailPlusAliasVariants(email, orderNo string) []string {
	local, domainPart, ok := splitEmail(email)
	primaryDomain, alternateDomain, domainsOK := gmailAliasDomains(domainPart)
	if !ok || !domainsOK || local == "" {
		return nil
	}
	domains := [...]string{primaryDomain, alternateDomain}
	domainOffset := int(hash64(orderNo+"|gmail-plus-domain") % uint64(len(domains)))
	window := aliasGenerationWindowValue()
	result := make([]string, 0, window)
	seen := make(map[string]struct{}, window)
	for len(result) < window {
		suffix := make([]byte, rand.IntN(9)+4)
		suffix[0] = gmailPlusAliasLetters[rand.IntN(len(gmailPlusAliasLetters))]
		suffix[1] = gmailPlusAliasDigits[rand.IntN(len(gmailPlusAliasDigits))]
		for i := 2; i < len(suffix); i++ {
			suffix[i] = gmailPlusAliasChars[rand.IntN(len(gmailPlusAliasChars))]
		}
		rand.Shuffle(len(suffix), func(i, j int) { suffix[i], suffix[j] = suffix[j], suffix[i] })
		aliasDomain := domains[(domainOffset+len(result))%len(domains)]
		alias := local + "+" + string(suffix) + "@" + aliasDomain
		if _, exists := seen[alias]; exists {
			continue
		}
		seen[alias] = struct{}{}
		result = append(result, alias)
	}
	return result
}

func generatedMailboxVariants(domainPart string) []string {
	domainPart = strings.ToLower(strings.TrimSpace(domainPart))
	if domainPart == "" {
		return nil
	}
	window := aliasGenerationWindowValue()
	result := make([]string, 0, window)
	seen := make(map[string]struct{}, window)
	for len(result) < window {
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
	return email[:index], strings.TrimSuffix(email[index+1:], "."), true
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
