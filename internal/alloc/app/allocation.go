package app

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"math/rand/v2"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/donnel666/remail/internal/alloc/domain"
	"github.com/donnel666/remail/internal/platform"
)

const inventoryStatsCacheTTL = 30 * time.Second

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
	EmailSuffix      string
	// FulfillExistingOrder is set only by Trade after an order is persisted.
	// A delisted product cannot receive new orders, but it must remain
	// allocatable for an already accepted order.
	FulfillExistingOrder bool
}

type UseCase struct {
	repo                      Repository
	queue                     CandidateRefreshQueue
	adminAllocationEnrichment AdminAllocationEnrichmentPort
	inventoryStatsCache       *platform.TTLCache[string, InventoryStats]
	productInventoryCache     *platform.TTLCache[string, ProjectProductInventoryTotals]
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
		repo:                  repo,
		queue:                 queue,
		inventoryStatsCache:   platform.NewTTLCache[string, InventoryStats](),
		productInventoryCache: platform.NewTTLCache[string, ProjectProductInventoryTotals](),
	}
}

func (uc *UseCase) Allocate(ctx context.Context, cmd AllocateCommand) (*domain.UnifiedAllocation, error) {
	cmd.OrderNo = strings.TrimSpace(cmd.OrderNo)
	cmd.SupplyScope = domain.NormalizeSupplyScope(cmd.SupplyScope)
	cmd.EmailSuffix = normalizeEmailSuffix(cmd.EmailSuffix)
	if cmd.OrderNo == "" || cmd.BuyerUserID == 0 || cmd.ProjectProductID == 0 {
		return nil, domain.ErrInvalidAllocationRequest
	}

	var result *domain.UnifiedAllocation
	var err error
	for attempt := 0; attempt < candidateRetryCount; attempt++ {
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
			if err := uc.repo.CreateOrderGuard(txCtx, cmd.OrderNo, config.ProductType); err != nil {
				if errors.Is(err, domain.ErrAllocationConflict) {
					existing, findErr := uc.repo.FindExistingAllocation(txCtx, cmd.OrderNo)
					if findErr != nil {
						return findErr
					}
					if existing != nil {
						result = existing
						return nil
					}
				}
				return err
			}

			switch config.ProductType {
			case domain.AllocationTypeMicrosoft:
				result, err = uc.allocateMicrosoft(txCtx, cmd, *config)
			case domain.AllocationTypeDomain:
				result, err = uc.allocateDomain(txCtx, cmd, *config)
			default:
				err = domain.ErrProjectNotAllocatable
			}
			return err
		})
		if err == nil || (!errors.Is(err, domain.ErrInsufficientInventory) && !errors.Is(err, domain.ErrAllocationConflict)) {
			break
		}
		if attempt < candidateRetryCount-1 && !uc.repo.HasParentTx(ctx) {
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

func (uc *UseCase) GetInventoryStats(ctx context.Context, projectID uint, buyerUserID uint) (*InventoryStats, error) {
	if projectID == 0 {
		return nil, domain.ErrInvalidAllocationRequest
	}
	key := inventoryCacheKey(projectID, buyerUserID)
	if cached, ok := uc.inventoryStatsCache.Get(key); ok {
		return cloneInventoryStats(cached), nil
	}
	stats, err := uc.repo.GetInventoryStats(ctx, projectID, buyerUserID)
	if err != nil || stats == nil {
		return stats, err
	}
	uc.inventoryStatsCache.Set(key, *stats, inventoryStatsCacheTTL)
	return cloneInventoryStats(*stats), nil
}

func (uc *UseCase) GetProductInventoryTotals(ctx context.Context, projectID uint, buyerUserID uint) (*ProjectProductInventoryTotals, error) {
	if projectID == 0 || buyerUserID == 0 {
		return nil, domain.ErrInvalidAllocationRequest
	}
	key := inventoryCacheKey(projectID, buyerUserID)
	if cached, ok := uc.productInventoryCache.Get(key); ok {
		return cloneProductInventoryTotals(cached), nil
	}
	totals, err := uc.repo.GetProductInventoryTotals(ctx, projectID, buyerUserID)
	if err != nil || totals == nil {
		return totals, err
	}
	uc.productInventoryCache.Set(key, *totals, inventoryStatsCacheTTL)
	return cloneProductInventoryTotals(*totals), nil
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
	job := &domain.CandidateRefreshJob{
		ProjectID:      projectID,
		OperatorUserID: operatorUserID,
		Status:         domain.CandidateRefreshPending,
		MaxAttempts:    1,
		RequestID:      strings.TrimSpace(requestID),
		Path:           strings.TrimSpace(path),
	}
	created, err := uc.repo.CreateCandidateRefreshJobWithLog(ctx, job)
	if err != nil {
		return nil, err
	}
	if created {
		if err := uc.enqueueCandidateRefresh(ctx, job); err != nil {
			job.LastSafeError = "Candidate refresh queue is unavailable; dispatcher will retry."
			_ = uc.repo.MarkCandidateRefreshJobDispatchFailed(ctx, job.ID, job.LastSafeError)
		}
	} else {
		uc.ScheduleCandidateRefreshDispatcher(ctx, 0)
	}
	message := "Candidate refresh job accepted."
	if !created {
		message = "Candidate refresh job already exists."
	}
	return &CandidateRefreshSubmitResult{
		JobID:     job.ID,
		ProjectID: job.ProjectID,
		Status:    job.Status,
		Created:   created,
		Message:   message,
		CreatedAt: job.CreatedAt,
		UpdatedAt: job.UpdatedAt,
	}, nil
}

func inventoryCacheKey(projectID uint, buyerUserID uint) string {
	return strconv.FormatUint(uint64(projectID), 10) + "|" + strconv.FormatUint(uint64(buyerUserID), 10)
}

func cloneInventoryStats(stats InventoryStats) *InventoryStats {
	cloned := stats
	return &cloned
}

func cloneProductInventoryTotals(totals ProjectProductInventoryTotals) *ProjectProductInventoryTotals {
	cloned := totals
	cloned.Items = make([]ProductInventoryTotal, len(totals.Items))
	for i := range totals.Items {
		cloned.Items[i] = totals.Items[i]
		cloned.Items[i].Suffixes = append([]ProductInventorySuffixTotal(nil), totals.Items[i].Suffixes...)
	}
	return &cloned
}

func (uc *UseCase) ProcessCandidateRefresh(ctx context.Context, task CandidateRefreshTask) error {
	if task.JobID == 0 {
		return domain.ErrAllocationNotFound
	}
	job, err := uc.repo.FindCandidateRefreshJob(ctx, task.JobID)
	if err != nil {
		return err
	}
	if job == nil {
		return domain.ErrAllocationNotFound
	}
	if domain.IsTerminalCandidateRefreshStatus(job.Status) {
		return nil
	}
	claimed, err := uc.repo.MarkCandidateRefreshJobRunning(ctx, task.JobID)
	if err != nil {
		return err
	}
	if !claimed {
		return nil
	}
	affected, err := uc.repo.RefreshRoutingCandidates(ctx, job.ProjectID)
	if err != nil {
		_ = uc.repo.MarkCandidateRefreshJobFailed(ctx, task.JobID, "Candidate refresh failed.")
		return err
	}
	return uc.repo.MarkCandidateRefreshJobSucceeded(ctx, task.JobID, affected)
}

func (uc *UseCase) DispatchCandidateRefreshJobs(ctx context.Context, limit int) (*CandidateRefreshDispatchResult, error) {
	if limit <= 0 {
		limit = 100
	}
	staleBefore := time.Now().UTC().Add(-10 * time.Minute)
	expired, err := uc.repo.ExpireStaleCandidateRefreshJobs(ctx, staleBefore)
	if err != nil {
		return nil, err
	}
	jobs, err := uc.repo.ClaimDispatchableCandidateRefreshJobs(ctx, limit, staleBefore)
	if err != nil {
		return nil, err
	}
	result := &CandidateRefreshDispatchResult{Attempted: len(jobs), Expired: expired}
	for i := range jobs {
		if err := uc.enqueueCandidateRefresh(ctx, &jobs[i]); err != nil {
			result.Failed++
			_ = uc.repo.MarkCandidateRefreshJobDispatchFailed(ctx, jobs[i].ID, "Candidate refresh queue is unavailable; dispatcher will retry.")
			continue
		}
		result.Queued++
	}
	return result, nil
}

func (uc *UseCase) ScheduleCandidateRefreshDispatcher(ctx context.Context, delay time.Duration) {
	if uc == nil || uc.queue == nil {
		return
	}
	_ = uc.queue.EnqueueCandidateRefreshDispatcher(ctx, delay)
}

func (uc *UseCase) enqueueCandidateRefresh(ctx context.Context, job *domain.CandidateRefreshJob) error {
	if uc == nil || uc.queue == nil {
		return domain.ErrInvalidAllocationRequest
	}
	if job == nil || job.ID == 0 {
		return domain.ErrInvalidAllocationRequest
	}
	if err := uc.queue.EnqueueCandidateRefresh(ctx, CandidateRefreshTask{JobID: job.ID, RequestID: job.RequestID}); err != nil {
		return err
	}
	queued, err := uc.repo.MarkCandidateRefreshJobQueued(ctx, job.ID)
	if err != nil {
		return err
	}
	if queued {
		job.Status = domain.CandidateRefreshQueued
	}
	return nil
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
	for _, mailbox := range preferences {
		buckets := bucketProbeSequence(cmd.OrderNo, config.ProjectID, string(mailbox))
		for _, bucket := range buckets {
			result, _, err := uc.tryMicrosoftBucket(ctx, cmd, config, mailbox, &bucket, now)
			if err != nil {
				return nil, err
			}
			if result != nil {
				return result, nil
			}
		}
		result, _, err := uc.tryMicrosoftBucket(ctx, cmd, config, mailbox, nil, now)
		if err != nil {
			return nil, err
		}
		if result != nil {
			return result, nil
		}
	}
	return nil, domain.ErrInsufficientInventory
}

func (uc *UseCase) tryMicrosoftBucket(ctx context.Context, cmd AllocateCommand, config ProductAllocationConfig, mailbox domain.MicrosoftMailbox, bucket *uint8, now time.Time) (*domain.UnifiedAllocation, bool, error) {
	limit := candidateWindowSize
	if bucket == nil {
		limit = globalCandidateWindow
	}
	candidates, err := uc.repo.ListMicrosoftSourceCandidates(ctx, config.ProjectID, cmd.BuyerUserID, cmd.SupplyScope, bucket, limit, cmd.EmailSuffix)
	if err != nil {
		return nil, false, err
	}
	if len(candidates) == 0 {
		return nil, true, nil
	}
	for _, candidate := range candidates {
		result, err := uc.tryMicrosoftCandidate(ctx, cmd, config, mailbox, candidate, now)
		if err == nil && result != nil {
			return result, false, nil
		}
		if err == domain.ErrAllocationConflict || err == domain.ErrInsufficientInventory {
			continue
		}
		return nil, false, err
	}
	return nil, false, nil
}

func (uc *UseCase) tryMicrosoftCandidate(ctx context.Context, cmd AllocateCommand, config ProductAllocationConfig, mailbox domain.MicrosoftMailbox, candidate MicrosoftCandidate, now time.Time) (*domain.UnifiedAllocation, error) {
	lockedRoot, err := uc.repo.LockResourceRoot(ctx, candidate.ResourceID, domain.AllocationTypeMicrosoft)
	if err != nil {
		return nil, err
	}
	if !lockedRoot {
		return nil, domain.ErrAllocationConflict
	}

	lockedCandidate, err := uc.repo.LockMicrosoftCandidate(ctx, candidate.ResourceID, config.ProjectID, cmd.BuyerUserID, cmd.SupplyScope, cmd.EmailSuffix)
	if err != nil {
		return nil, err
	}
	if lockedCandidate == nil {
		return nil, domain.ErrAllocationConflict
	}
	candidate = *lockedCandidate

	switch mailbox {
	case domain.MicrosoftMailboxMain:
		result, err := uc.createMicrosoftAllocation(ctx, cmd.OrderNo, cmd.SupplyScope, config, candidate.ResourceID, domain.MicrosoftMailboxMain, nil, nil, nil, candidate.EmailAddress, now, nil)
		if err == nil {
			return result, nil
		}
		if err != domain.ErrAllocationConflict {
			return nil, err
		}
		alias, aliasErr := uc.repo.FindReusableExplicitAlias(ctx, candidate.ResourceID)
		if aliasErr != nil {
			return nil, aliasErr
		}
		if alias == nil {
			return nil, domain.ErrAllocationConflict
		}
		return uc.createMicrosoftAllocation(ctx, cmd.OrderNo, cmd.SupplyScope, config, candidate.ResourceID, domain.MicrosoftMailboxAlias, &alias.ID, nil, nil, alias.Email, now, nil)
	case domain.MicrosoftMailboxDot:
		alias, err := uc.repo.FindReusableDotAlias(ctx, config.ProjectID, candidate.ResourceID)
		if err != nil {
			return nil, err
		}
		if alias != nil {
			return uc.createMicrosoftAllocation(ctx, cmd.OrderNo, cmd.SupplyScope, config, candidate.ResourceID, domain.MicrosoftMailboxDot, nil, &alias.ID, nil, alias.Email, now, nil)
		}
		for _, email := range dotAliasVariants(candidate.EmailAddress) {
			alias, err = uc.repo.FindOrCreateDotAlias(ctx, candidate.ResourceID, email)
			if err != nil {
				return nil, err
			}
			result, err := uc.createMicrosoftAllocation(ctx, cmd.OrderNo, cmd.SupplyScope, config, candidate.ResourceID, domain.MicrosoftMailboxDot, nil, &alias.ID, nil, alias.Email, now, nil)
			if err == nil {
				return result, nil
			}
			if err != domain.ErrAllocationConflict {
				return nil, err
			}
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
			return uc.createMicrosoftAllocation(ctx, cmd.OrderNo, cmd.SupplyScope, config, candidate.ResourceID, domain.MicrosoftMailboxPlus, nil, nil, &alias.ID, alias.Email, now, &dailyUsage)
		}
		for _, email := range plusAliasVariants(candidate.EmailAddress, config.ProjectID, cmd.OrderNo) {
			alias, err = uc.repo.FindOrCreatePlusAlias(ctx, candidate.ResourceID, email)
			if err != nil {
				return nil, err
			}
			result, err := uc.createMicrosoftAllocation(ctx, cmd.OrderNo, cmd.SupplyScope, config, candidate.ResourceID, domain.MicrosoftMailboxPlus, nil, nil, &alias.ID, alias.Email, now, &dailyUsage)
			if err == nil {
				return result, nil
			}
			if err != domain.ErrAllocationConflict {
				return nil, err
			}
		}
		return nil, domain.ErrInsufficientInventory
	default:
		return nil, domain.ErrInvalidAllocationRequest
	}
}

func (uc *UseCase) createMicrosoftAllocation(ctx context.Context, orderNo string, supplyScope domain.SupplyScope, config ProductAllocationConfig, resourceID uint, mailbox domain.MicrosoftMailbox, explicitAliasID, dotAliasID, plusAliasID *uint, email string, now time.Time, dailyUsage *DailyUsageReservation) (*domain.UnifiedAllocation, error) {
	allocation := &domain.MicrosoftAllocation{
		OrderNo:         orderNo,
		ProjectID:       config.ProjectID,
		ProductID:       config.ProductID,
		ResourceID:      resourceID,
		SupplyScope:     supplyScope,
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
	if err := uc.repo.CreateMicrosoftAllocation(ctx, allocation); err != nil {
		return nil, err
	}
	if dailyUsage != nil {
		if err := uc.repo.ConsumeDailyUsage(ctx, dailyUsage.UsageDate, dailyUsage.AllocationType, dailyUsage.ResourceID, dailyUsage.Kind, dailyUsage.Limit); err != nil {
			return nil, err
		}
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
	buckets := bucketProbeSequence(cmd.OrderNo, config.ProjectID, "domain")
	for _, bucket := range buckets {
		result, _, err := uc.tryDomainBucket(ctx, cmd, config, &bucket, now)
		if err != nil {
			return nil, err
		}
		if result != nil {
			return result, nil
		}
	}
	result, _, err := uc.tryDomainBucket(ctx, cmd, config, nil, now)
	if err != nil {
		return nil, err
	}
	if result != nil {
		return result, nil
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
		return nil, true, nil
	}
	for _, candidate := range candidates {
		result, err := uc.tryDomainCandidate(ctx, cmd, config, candidate, now)
		if err == nil && result != nil {
			return result, false, nil
		}
		if err == domain.ErrAllocationConflict || err == domain.ErrInsufficientInventory {
			continue
		}
		return nil, false, err
	}
	return nil, false, nil
}

func (uc *UseCase) tryDomainCandidate(ctx context.Context, cmd AllocateCommand, config ProductAllocationConfig, candidate DomainCandidate, now time.Time) (*domain.UnifiedAllocation, error) {
	lockedRoot, err := uc.repo.LockResourceRoot(ctx, candidate.ResourceID, domain.AllocationTypeDomain)
	if err != nil {
		return nil, err
	}
	if !lockedRoot {
		return nil, domain.ErrAllocationConflict
	}

	lockedCandidate, err := uc.repo.LockDomainCandidate(ctx, candidate.ResourceID, cmd.BuyerUserID, cmd.SupplyScope, cmd.EmailSuffix)
	if err != nil {
		return nil, err
	}
	if lockedCandidate == nil {
		return nil, domain.ErrAllocationConflict
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
		return uc.createDomainAllocation(ctx, cmd.OrderNo, cmd.SupplyScope, config, candidate.ResourceID, mailbox.ID, mailbox.Email, now, &dailyUsage)
	}
	for _, email := range generatedMailboxVariants(candidate.Domain) {
		mailbox, err = uc.repo.FindOrCreateGeneratedMailbox(ctx, candidate.ResourceID, candidate.OwnerUserID, email)
		if err != nil {
			return nil, err
		}
		result, err := uc.createDomainAllocation(ctx, cmd.OrderNo, cmd.SupplyScope, config, candidate.ResourceID, mailbox.ID, mailbox.Email, now, &dailyUsage)
		if err == nil {
			return result, nil
		}
		if err != domain.ErrAllocationConflict {
			return nil, err
		}
	}
	return nil, domain.ErrInsufficientInventory
}

func (uc *UseCase) createDomainAllocation(ctx context.Context, orderNo string, supplyScope domain.SupplyScope, config ProductAllocationConfig, resourceID uint, mailboxID uint, email string, now time.Time, dailyUsage *DailyUsageReservation) (*domain.UnifiedAllocation, error) {
	allocation := &domain.GeneratedMailboxAllocation{
		OrderNo:     orderNo,
		ProjectID:   config.ProjectID,
		ProductID:   config.ProductID,
		ResourceID:  resourceID,
		SupplyScope: supplyScope,
		MailboxID:   mailboxID,
		Email:       strings.ToLower(strings.TrimSpace(email)),
		Status:      domain.AllocationStatusAllocated,
	}
	if allocation.Email == "" {
		return nil, domain.ErrInvalidAllocationRequest
	}
	if err := uc.repo.CreateDomainAllocation(ctx, allocation); err != nil {
		return nil, err
	}
	if dailyUsage != nil {
		if err := uc.repo.ConsumeDailyUsage(ctx, dailyUsage.UsageDate, dailyUsage.AllocationType, dailyUsage.ResourceID, dailyUsage.Kind, dailyUsage.Limit); err != nil {
			return nil, err
		}
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
