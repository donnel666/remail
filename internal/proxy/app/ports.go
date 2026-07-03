package app

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	governanceapp "github.com/donnel666/remail/internal/governance/app"
	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	"github.com/donnel666/remail/internal/proxy/domain"
)

type ProxyRepository interface {
	CreateWithLog(ctx context.Context, proxy *domain.Proxy, log *governancedomain.OperationLog) error
	CreateBatchWithLog(ctx context.Context, proxies []*domain.Proxy, log *governancedomain.OperationLog) ([]domain.Proxy, int, error)
	FindByID(ctx context.Context, id uint) (*domain.Proxy, error)
	List(ctx context.Context, filter ProxyListFilter, offset, limit int) ([]domain.Proxy, error)
	Count(ctx context.Context, filter ProxyListFilter) (int64, error)
	Stats(ctx context.Context, filter ProxyListFilter) (*ProxyStats, error)
	ListBindings(ctx context.Context, filter ProxyBindingListFilter, offset, limit int) ([]domain.Binding, error)
	CountBindings(ctx context.Context, filter ProxyBindingListFilter) (int64, error)
	UpdateWithLog(ctx context.Context, proxy *domain.Proxy, log *governancedomain.OperationLog) error
	DeleteBatchWithLog(ctx context.Context, ids []uint, log *governancedomain.OperationLog) ([]uint, error)
	DeleteByFilterWithLog(ctx context.Context, filter ProxyListFilter, log *governancedomain.OperationLog) (int64, error)
	ListIDs(ctx context.Context, filter ProxyListFilter, afterID uint, limit int) ([]uint, error)
	MarkExpiredBefore(ctx context.Context, now time.Time) (int64, error)
	UpdateCheckResultWithLog(ctx context.Context, id uint, result domain.CheckResult, success bool, log *governancedomain.OperationLog) (*domain.Proxy, error)
	AcquireResourceProxy(ctx context.Context, key string, ipVersion domain.ProxyIPVersion, now time.Time, bindingTTL time.Duration) (*domain.Proxy, error)
	AcquireSystemProxy(ctx context.Context, ipVersion domain.ProxyIPVersion, now time.Time) (*domain.Proxy, error)
	ReportSuccess(ctx context.Context, proxyID uint, usedAt time.Time) error
	ReportFailure(ctx context.Context, proxyID uint, safeError string, retryable bool) (*domain.Proxy, error)
}

type ProxyChecker interface {
	Check(ctx context.Context, proxyURL string) (domain.CheckResult, error)
}

type ProxyListFilter struct {
	Pool        domain.ProxyPool
	IPVersion   domain.ProxyIPVersion
	IPv6        *bool
	Status      domain.ProxyStatus
	Country     string
	Search      string
	CreatedFrom *time.Time
	CreatedTo   *time.Time
}

type ProxyBindingListFilter struct {
	Key       string
	ProxyID   uint
	IPVersion domain.ProxyIPVersion
}

type CreateProxyRequest struct {
	Pool     domain.ProxyPool
	URL      string
	ExpireAt time.Time
}

type ImportProxiesRequest struct {
	Pool     domain.ProxyPool
	URLs     []string
	ExpireAt time.Time
}

type UpdateProxyRequest struct {
	Status   *domain.ProxyStatus
	ExpireAt *time.Time
}

type ProxyListResult struct {
	Items  []domain.Proxy
	Total  int64
	Offset int
	Limit  int
}

type ProxyCount struct {
	Key   string
	Count int64
}

type ProxyStats struct {
	Total      int64
	Countries  []ProxyCount
	Statuses   []ProxyCount
	Pools      []ProxyCount
	IPVersions []ProxyCount
}

type ProxyBindingListResult struct {
	Items  []domain.Binding
	Total  int64
	Offset int
	Limit  int
}

type DeleteProxiesResult struct {
	Requested       int
	Deleted         int
	DeletedProxyIDs []uint
	DeletedByFilter bool
}

type ImportProxiesResult struct {
	Requested  int
	Created    int
	Duplicated int
	Items      []domain.Proxy
}

type CheckProxiesResult struct {
	Requested int
	Checked   int
	Failed    int
	Items     []domain.Proxy
}

type AcquireProxyRequest struct {
	Key                 string
	IPVersion           domain.ProxyIPVersion
	Purpose             domain.ProxyPurpose
	AllowSystemFallback bool
	Attempt             int
	RequestID           string
}

type ProxyConfig struct {
	ID        uint
	Pool      domain.ProxyPool
	URL       string
	IPVersion domain.ProxyIPVersion
	Country   string
	LatencyMs int
	Direct    bool
}

type ProxyUseCase struct {
	proxies ProxyRepository
	checker ProxyChecker
	ops     governanceapp.OperationLogPort
	systems governanceapp.SystemLogPort
	now     func() time.Time
}

const (
	defaultProxyListLimit = 20
	maxProxyListLimit     = 10000
	resourceBindingTTL    = 7 * 24 * time.Hour
	maxProxyAttempts      = 3
	batchCheckConcurrency = 4
	batchCheckIDPageSize  = 1000
)

func NewProxyUseCase(
	proxies ProxyRepository,
	checker ProxyChecker,
	ops governanceapp.OperationLogPort,
	systems governanceapp.SystemLogPort,
) *ProxyUseCase {
	return &ProxyUseCase{
		proxies: proxies,
		checker: checker,
		ops:     ops,
		systems: systems,
		now:     func() time.Time { return time.Now().UTC() },
	}
}

func (uc *ProxyUseCase) List(ctx context.Context, filter ProxyListFilter, offset, limit int) (*ProxyListResult, error) {
	if err := validateProxyListFilter(filter); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = defaultProxyListLimit
	}
	if limit > maxProxyListLimit {
		limit = maxProxyListLimit
	}
	if offset < 0 {
		offset = 0
	}
	if _, err := uc.proxies.MarkExpiredBefore(ctx, uc.now()); err != nil {
		return nil, err
	}
	items, err := uc.proxies.List(ctx, filter, offset, limit)
	if err != nil {
		return nil, err
	}
	total, err := uc.proxies.Count(ctx, filter)
	if err != nil {
		return nil, err
	}
	return &ProxyListResult{Items: items, Total: total, Offset: offset, Limit: limit}, nil
}

func (uc *ProxyUseCase) Stats(ctx context.Context, filter ProxyListFilter) (*ProxyStats, error) {
	if err := validateProxyListFilter(filter); err != nil {
		return nil, err
	}
	if _, err := uc.proxies.MarkExpiredBefore(ctx, uc.now()); err != nil {
		return nil, err
	}
	return uc.proxies.Stats(ctx, filter)
}

func (uc *ProxyUseCase) ListBindings(ctx context.Context, filter ProxyBindingListFilter, offset, limit int) (*ProxyBindingListResult, error) {
	if filter.IPVersion != "" && filter.IPVersion != domain.ProxyIPAuto && !domain.IsValidProxyIPVersion(string(filter.IPVersion)) {
		return nil, domain.ErrInvalidProxyFilter
	}
	if limit <= 0 {
		limit = defaultProxyListLimit
	}
	if limit > maxProxyListLimit {
		limit = maxProxyListLimit
	}
	if offset < 0 {
		offset = 0
	}
	items, err := uc.proxies.ListBindings(ctx, filter, offset, limit)
	if err != nil {
		return nil, err
	}
	total, err := uc.proxies.CountBindings(ctx, filter)
	if err != nil {
		return nil, err
	}
	return &ProxyBindingListResult{Items: items, Total: total, Offset: offset, Limit: limit}, nil
}

func (uc *ProxyUseCase) Create(ctx context.Context, operatorUserID uint, requestID, path string, req CreateProxyRequest) (*domain.Proxy, error) {
	if !domain.IsValidProxyPool(string(req.Pool)) {
		return nil, domain.ErrInvalidProxyPool
	}
	normalizedURL, err := domain.NormalizeProxyURL(req.URL)
	if err != nil {
		return nil, err
	}
	if req.ExpireAt.IsZero() || !req.ExpireAt.After(uc.now()) {
		return nil, domain.ErrInvalidProxyExpireAt
	}
	proxy := &domain.Proxy{
		Pool:      req.Pool,
		URL:       normalizedURL,
		ExpireAt:  req.ExpireAt.UTC(),
		Status:    domain.ProxyStatusChecking,
		Country:   "UNKNOWN",
		Errors:    0,
		LatencyMs: 0,
	}
	log := uc.operationLog(operatorUserID, requestID, path, "proxy.proxy.create", "", "success", "Proxy created.")
	if err := uc.proxies.CreateWithLog(ctx, proxy, log); err != nil {
		_ = uc.writeOperationLog(ctx, operatorUserID, requestID, path, "proxy.proxy.create", "0", "failure", "Proxy create failed.")
		return nil, err
	}
	return proxy, nil
}

func (uc *ProxyUseCase) Import(ctx context.Context, operatorUserID uint, requestID, path string, req ImportProxiesRequest) (*ImportProxiesResult, error) {
	if !domain.IsValidProxyPool(string(req.Pool)) {
		return nil, domain.ErrInvalidProxyPool
	}
	if req.ExpireAt.IsZero() || !req.ExpireAt.After(uc.now()) {
		return nil, domain.ErrInvalidProxyExpireAt
	}
	if len(req.URLs) == 0 {
		return nil, domain.ErrInvalidProxyURL
	}

	normalizedURLs := make([]string, 0, len(req.URLs))
	seen := make(map[string]struct{}, len(req.URLs))
	duplicates := 0
	for _, rawURL := range req.URLs {
		normalizedURL, err := domain.NormalizeProxyURL(rawURL)
		if err != nil {
			return nil, err
		}
		if _, ok := seen[normalizedURL]; ok {
			duplicates++
			continue
		}
		seen[normalizedURL] = struct{}{}
		normalizedURLs = append(normalizedURLs, normalizedURL)
	}
	if len(normalizedURLs) == 0 {
		return nil, domain.ErrDuplicateProxy
	}

	proxies := make([]*domain.Proxy, 0, len(normalizedURLs))
	for _, normalizedURL := range normalizedURLs {
		proxies = append(proxies, &domain.Proxy{
			Pool:      req.Pool,
			URL:       normalizedURL,
			ExpireAt:  req.ExpireAt.UTC(),
			Status:    domain.ProxyStatusChecking,
			Country:   "UNKNOWN",
			Errors:    0,
			LatencyMs: 0,
		})
	}
	log := uc.operationLog(operatorUserID, requestID, path, "proxy.proxy.import", "batch", "success", "Proxy imported.")
	created, existingDuplicates, err := uc.proxies.CreateBatchWithLog(ctx, proxies, log)
	if err != nil {
		_ = uc.writeOperationLog(ctx, operatorUserID, requestID, path, "proxy.proxy.import", "batch", "failure", "Proxy import failed.")
		return nil, err
	}
	duplicates += existingDuplicates
	return &ImportProxiesResult{
		Requested:  len(req.URLs),
		Created:    len(created),
		Duplicated: duplicates,
		Items:      created,
	}, nil
}

func (uc *ProxyUseCase) Get(ctx context.Context, id uint) (*domain.Proxy, error) {
	if id == 0 {
		return nil, domain.ErrProxyNotFound
	}
	proxy, err := uc.proxies.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if proxy == nil {
		return nil, domain.ErrProxyNotFound
	}
	return proxy, nil
}

func (uc *ProxyUseCase) Update(ctx context.Context, id uint, operatorUserID uint, requestID, path string, req UpdateProxyRequest) (*domain.Proxy, error) {
	proxy, err := uc.Get(ctx, id)
	if err != nil {
		return nil, err
	}

	if req.ExpireAt != nil {
		if !req.ExpireAt.After(uc.now()) {
			return nil, domain.ErrInvalidProxyExpireAt
		}
		proxy.ExpireAt = req.ExpireAt.UTC()
		if proxy.Status == domain.ProxyStatusExpired {
			if err := proxy.MarkChecking(); err != nil {
				return nil, err
			}
		}
	}
	if req.Status != nil {
		switch *req.Status {
		case domain.ProxyStatusDisabled:
			if err := proxy.MarkDisabled("Proxy disabled by administrator."); err != nil {
				return nil, err
			}
		case domain.ProxyStatusChecking:
			if err := proxy.MarkChecking(); err != nil {
				return nil, err
			}
		default:
			return nil, domain.ErrInvalidProxyStatus
		}
	}
	if proxy.IsExpired(uc.now()) && proxy.Status != domain.ProxyStatusExpired {
		if err := proxy.MarkExpired(uc.now()); err != nil {
			return nil, err
		}
	}
	log := uc.operationLog(operatorUserID, requestID, path, "proxy.proxy.update", fmt.Sprintf("%d", proxy.ID), "success", "Proxy updated.")
	if err := uc.proxies.UpdateWithLog(ctx, proxy, log); err != nil {
		_ = uc.writeOperationLog(ctx, operatorUserID, requestID, path, "proxy.proxy.update", fmt.Sprintf("%d", id), "failure", "Proxy update failed.")
		return nil, err
	}
	return proxy, nil
}

func (uc *ProxyUseCase) DeleteBatch(ctx context.Context, ids []uint, operatorUserID uint, requestID, path string) (*DeleteProxiesResult, error) {
	if len(ids) == 0 {
		return nil, domain.ErrInvalidProxyFilter
	}
	seen := make(map[uint]struct{}, len(ids))
	uniqueIDs := make([]uint, 0, len(ids))
	for _, id := range ids {
		if id == 0 {
			return nil, domain.ErrInvalidProxyFilter
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		uniqueIDs = append(uniqueIDs, id)
	}

	log := uc.operationLog(operatorUserID, requestID, path, "proxy.proxy.delete", "batch", "success", "Proxy deleted.")
	deletedIDs, err := uc.proxies.DeleteBatchWithLog(ctx, uniqueIDs, log)
	if err != nil {
		_ = uc.writeOperationLog(ctx, operatorUserID, requestID, path, "proxy.proxy.delete", "batch", "failure", "Proxy delete failed.")
		return nil, err
	}
	return &DeleteProxiesResult{
		Requested:       len(uniqueIDs),
		Deleted:         len(deletedIDs),
		DeletedProxyIDs: deletedIDs,
	}, nil
}

func (uc *ProxyUseCase) DeleteByFilter(ctx context.Context, filter ProxyListFilter, operatorUserID uint, requestID, path string) (*DeleteProxiesResult, error) {
	if err := validateProxyListFilter(filter); err != nil {
		return nil, err
	}
	log := uc.operationLog(operatorUserID, requestID, path, "proxy.proxy.delete", "filter", "success", "Proxy deleted.")
	deleted, err := uc.proxies.DeleteByFilterWithLog(ctx, filter, log)
	if err != nil {
		_ = uc.writeOperationLog(ctx, operatorUserID, requestID, path, "proxy.proxy.delete", "filter", "failure", "Proxy delete failed.")
		return nil, err
	}
	return &DeleteProxiesResult{
		Deleted:         int(deleted),
		DeletedByFilter: true,
	}, nil
}

func (uc *ProxyUseCase) Check(ctx context.Context, id uint, operatorUserID uint, requestID, path string) (*domain.Proxy, error) {
	proxy, err := uc.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	now := uc.now()
	if proxy.IsExpired(now) {
		result := domain.CheckResult{LastSafeError: "Proxy has expired.", CheckedAt: now}
		log := uc.operationLog(operatorUserID, requestID, path, "proxy.proxy.check", fmt.Sprintf("%d", id), "failure", "Proxy check failed.")
		updated, updateErr := uc.proxies.UpdateCheckResultWithLog(ctx, id, result, false, log)
		if updateErr != nil {
			_ = uc.writeOperationLog(ctx, operatorUserID, requestID, path, "proxy.proxy.check", fmt.Sprintf("%d", id), "failure", "Proxy check failed.")
			return nil, updateErr
		}
		_ = uc.writeSystemLog(ctx, "warning", "proxy.expired", requestID, "proxy", fmt.Sprintf("%d", id), "Proxy has expired.", result.LastSafeError)
		return updated, domain.ErrProxyUnavailable
	}

	result, checkErr := uc.checker.Check(ctx, proxy.URL)
	if result.CheckedAt.IsZero() {
		result.CheckedAt = uc.now()
	}
	if checkErr != nil {
		if result.LastSafeError == "" {
			result.LastSafeError = "Proxy check failed."
		}
		log := uc.operationLog(operatorUserID, requestID, path, "proxy.proxy.check", fmt.Sprintf("%d", id), "failure", "Proxy check failed.")
		updated, updateErr := uc.proxies.UpdateCheckResultWithLog(ctx, id, result, false, log)
		if updateErr != nil {
			_ = uc.writeOperationLog(ctx, operatorUserID, requestID, path, "proxy.proxy.check", fmt.Sprintf("%d", id), "failure", "Proxy check failed.")
			return nil, updateErr
		}
		_ = uc.writeSystemLog(ctx, "warning", "proxy.check_failed", requestID, "proxy", fmt.Sprintf("%d", id), "Proxy check failed.", result.LastSafeError)
		if updated.Status == domain.ProxyStatusDisabled {
			_ = uc.writeSystemLog(ctx, "warning", "proxy.auto_disabled", requestID, "proxy", fmt.Sprintf("%d", id), "Proxy disabled automatically.", updated.LastSafeError)
		}
		if errors.Is(checkErr, domain.ErrProxyCheckFailed) {
			return updated, checkErr
		}
		return updated, fmt.Errorf("%w: %v", domain.ErrProxyCheckFailed, checkErr)
	}

	log := uc.operationLog(operatorUserID, requestID, path, "proxy.proxy.check", fmt.Sprintf("%d", id), "success", "Proxy check succeeded.")
	updated, err := uc.proxies.UpdateCheckResultWithLog(ctx, id, result, true, log)
	if err != nil {
		_ = uc.writeOperationLog(ctx, operatorUserID, requestID, path, "proxy.proxy.check", fmt.Sprintf("%d", id), "failure", "Proxy check failed.")
		return nil, err
	}
	return updated, nil
}

func (uc *ProxyUseCase) CheckBatch(ctx context.Context, ids []uint, operatorUserID uint, requestID, path string) (*CheckProxiesResult, error) {
	uniqueIDs, err := normalizeProxyIDs(ids)
	if err != nil {
		return nil, err
	}
	result, err := uc.checkBatchIDs(ctx, uniqueIDs, operatorUserID, requestID, path)
	if err != nil {
		_ = uc.writeOperationLog(ctx, operatorUserID, requestID, path, "proxy.proxy.check_batch", "batch", "failure", "Proxy batch check failed.")
		return nil, err
	}
	_ = uc.writeOperationLog(ctx, operatorUserID, requestID, path, "proxy.proxy.check_batch", "batch", "success", "Proxy batch check completed.")
	return result, nil
}

func (uc *ProxyUseCase) CheckByFilter(ctx context.Context, filter ProxyListFilter, operatorUserID uint, requestID, path string) (*CheckProxiesResult, error) {
	if err := validateProxyListFilter(filter); err != nil {
		return nil, err
	}
	result := &CheckProxiesResult{}
	var afterID uint
	for {
		ids, err := uc.proxies.ListIDs(ctx, filter, afterID, batchCheckIDPageSize)
		if err != nil {
			return nil, err
		}
		if len(ids) == 0 {
			break
		}
		batch, err := uc.checkBatchIDs(ctx, ids, operatorUserID, requestID, path)
		if err != nil {
			_ = uc.writeOperationLog(ctx, operatorUserID, requestID, path, "proxy.proxy.check_batch", "filter", "failure", "Proxy batch check failed.")
			return nil, err
		}
		result.Requested += batch.Requested
		result.Checked += batch.Checked
		result.Failed += batch.Failed
		afterID = ids[len(ids)-1]
		if len(ids) < batchCheckIDPageSize {
			break
		}
	}
	if result.Requested == 0 {
		return nil, domain.ErrInvalidProxyFilter
	}
	_ = uc.writeOperationLog(ctx, operatorUserID, requestID, path, "proxy.proxy.check_batch", "filter", "success", "Proxy batch check completed.")
	return result, nil
}

func (uc *ProxyUseCase) checkBatchIDs(ctx context.Context, ids []uint, operatorUserID uint, requestID, path string) (*CheckProxiesResult, error) {
	var mu sync.Mutex
	items := make([]domain.Proxy, 0, len(ids))
	failed := 0
	index := 0
	var firstUnexpected error

	workerCount := batchCheckConcurrency
	if len(ids) < workerCount {
		workerCount = len(ids)
	}
	var wg sync.WaitGroup
	for i := 0; i < workerCount; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				mu.Lock()
				if firstUnexpected != nil {
					mu.Unlock()
					return
				}
				if index >= len(ids) {
					mu.Unlock()
					return
				}
				proxyID := ids[index]
				index++
				mu.Unlock()

				proxy, err := uc.Check(ctx, proxyID, operatorUserID, requestID, path)
				mu.Lock()
				if proxy != nil {
					items = append(items, *proxy)
				}
				if err != nil {
					if errors.Is(err, domain.ErrProxyCheckFailed) || errors.Is(err, domain.ErrProxyUnavailable) || errors.Is(err, domain.ErrProxyNotFound) {
						failed++
					} else {
						firstUnexpected = err
					}
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if firstUnexpected != nil {
		return nil, firstUnexpected
	}
	return &CheckProxiesResult{
		Requested: len(ids),
		Checked:   len(items),
		Failed:    failed,
		Items:     items,
	}, nil
}

func (uc *ProxyUseCase) Acquire(ctx context.Context, req AcquireProxyRequest) (*ProxyConfig, error) {
	ipVersion := normalizeAcquireIP(req.IPVersion, req.Purpose)
	now := uc.now()
	if req.Attempt < 0 {
		req.Attempt = 0
	}
	if req.Attempt >= maxProxyAttempts {
		_ = uc.writeSystemLog(ctx, "warning", "proxy.direct_fallback", req.RequestID, "proxy_binding", req.Key, "Proxy attempt budget exhausted, falling back to direct connection.", "Proxy attempts exhausted.")
		return directProxyConfig(), nil
	}
	if _, err := uc.proxies.MarkExpiredBefore(ctx, now); err != nil {
		return nil, err
	}
	var proxy *domain.Proxy
	var err error
	if req.Key != "" && req.Attempt == 0 {
		proxy, err = uc.proxies.AcquireResourceProxy(ctx, req.Key, ipVersion, now, resourceBindingTTL)
		if err == nil {
			return proxyConfig(proxy), nil
		}
		if !errors.Is(err, domain.ErrProxyUnavailable) || !req.AllowSystemFallback {
			if errors.Is(err, domain.ErrProxyUnavailable) {
				_ = uc.writeSystemLog(ctx, "warning", "proxy.direct_fallback", req.RequestID, "proxy_binding", req.Key, "Resource proxy unavailable, falling back to direct connection.", err.Error())
				return directProxyConfig(), nil
			}
			_ = uc.writeSystemLog(ctx, "warning", "proxy.acquire_failed", req.RequestID, "proxy_binding", req.Key, "Proxy unavailable.", err.Error())
			return nil, err
		}
		_ = uc.writeSystemLog(ctx, "warning", "proxy.system_fallback", req.RequestID, "proxy_binding", req.Key, "Resource proxy unavailable, falling back to system proxy.", "Proxy unavailable.")
	}
	proxy, err = uc.proxies.AcquireSystemProxy(ctx, ipVersion, now)
	if err != nil {
		if errors.Is(err, domain.ErrProxyUnavailable) {
			_ = uc.writeSystemLog(ctx, "warning", "proxy.direct_fallback", req.RequestID, "proxy_binding", req.Key, "System proxy unavailable, falling back to direct connection.", err.Error())
			return directProxyConfig(), nil
		}
		_ = uc.writeSystemLog(ctx, "error", "proxy.system_unavailable", req.RequestID, "proxy_binding", req.Key, "System proxy unavailable.", err.Error())
		return nil, err
	}
	return proxyConfig(proxy), nil
}

func (uc *ProxyUseCase) ReportSuccess(ctx context.Context, proxyID uint) error {
	if proxyID == 0 {
		return nil
	}
	return uc.proxies.ReportSuccess(ctx, proxyID, uc.now())
}

func (uc *ProxyUseCase) ReportFailure(ctx context.Context, proxyID uint, safeError string) error {
	return uc.reportFailure(ctx, proxyID, safeError, true)
}

func (uc *ProxyUseCase) ReportNonRetryableFailure(ctx context.Context, proxyID uint, safeError string) error {
	return uc.reportFailure(ctx, proxyID, safeError, false)
}

func (uc *ProxyUseCase) reportFailure(ctx context.Context, proxyID uint, safeError string, retryable bool) error {
	if proxyID == 0 {
		return nil
	}
	updated, err := uc.proxies.ReportFailure(ctx, proxyID, safeError, retryable)
	if err != nil {
		return err
	}
	if updated.Status == domain.ProxyStatusDisabled {
		_ = uc.writeSystemLog(ctx, "warning", "proxy.auto_disabled", "", "proxy", fmt.Sprintf("%d", proxyID), "Proxy disabled automatically.", updated.LastSafeError)
	}
	return nil
}

func (uc *ProxyUseCase) writeOperationLog(ctx context.Context, operatorUserID uint, requestID, path, operationType, resourceID, result, summary string) error {
	log := uc.operationLog(operatorUserID, requestID, path, operationType, resourceID, result, summary)
	if log == nil {
		return nil
	}
	return uc.ops.Create(ctx, log)
}

func (uc *ProxyUseCase) operationLog(operatorUserID uint, requestID, path, operationType, resourceID, result, summary string) *governancedomain.OperationLog {
	if uc.ops == nil {
		return nil
	}
	return &governancedomain.OperationLog{
		OperatorUserID: operatorUserID,
		OperationType:  operationType,
		ResourceType:   "proxy",
		ResourceID:     resourceID,
		Path:           path,
		Result:         result,
		SafeSummary:    summary,
		RequestID:      requestID,
	}
}

func (uc *ProxyUseCase) writeSystemLog(ctx context.Context, level, eventType, requestID, bizType, bizID, message, detail string) error {
	if uc.systems == nil {
		return nil
	}
	return uc.systems.Create(ctx, &governancedomain.SystemLog{
		Level:     level,
		Module:    "proxy",
		EventType: eventType,
		RequestID: requestID,
		BizType:   bizType,
		BizID:     bizID,
		Message:   message,
		Detail:    domain.SafeProxyError(detail),
	})
}

func validateProxyListFilter(filter ProxyListFilter) error {
	if filter.Pool != "" && !domain.IsValidProxyPool(string(filter.Pool)) {
		return domain.ErrInvalidProxyFilter
	}
	if filter.IPVersion != "" && filter.IPVersion != domain.ProxyIPAuto && !domain.IsValidProxyIPVersion(string(filter.IPVersion)) {
		return domain.ErrInvalidProxyFilter
	}
	if filter.Status != "" && !domain.IsValidProxyStatus(string(filter.Status)) {
		return domain.ErrInvalidProxyFilter
	}
	if filter.CreatedFrom != nil && filter.CreatedTo != nil && filter.CreatedFrom.After(*filter.CreatedTo) {
		return domain.ErrInvalidProxyFilter
	}
	return nil
}

func normalizeProxyIDs(ids []uint) ([]uint, error) {
	if len(ids) == 0 {
		return nil, domain.ErrInvalidProxyFilter
	}
	seen := make(map[uint]struct{}, len(ids))
	uniqueIDs := make([]uint, 0, len(ids))
	for _, id := range ids {
		if id == 0 {
			return nil, domain.ErrInvalidProxyFilter
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		uniqueIDs = append(uniqueIDs, id)
	}
	if len(uniqueIDs) == 0 {
		return nil, domain.ErrInvalidProxyFilter
	}
	return uniqueIDs, nil
}

func normalizeAcquireIP(ipVersion domain.ProxyIPVersion, purpose domain.ProxyPurpose) domain.ProxyIPVersion {
	if purpose == domain.ProxyPurposeBinding {
		return domain.ProxyIPv4
	}
	if ipVersion == "" {
		return domain.ProxyIPAuto
	}
	return ipVersion
}

func proxyConfig(proxy *domain.Proxy) *ProxyConfig {
	if proxy == nil {
		return nil
	}
	return &ProxyConfig{
		ID:        proxy.ID,
		Pool:      proxy.Pool,
		URL:       proxy.URL,
		IPVersion: proxy.IPVersion,
		Country:   proxy.Country,
		LatencyMs: proxy.LatencyMs,
	}
}

func directProxyConfig() *ProxyConfig {
	return &ProxyConfig{Direct: true}
}
