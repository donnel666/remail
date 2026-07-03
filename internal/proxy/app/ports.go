package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	governanceapp "github.com/donnel666/remail/internal/governance/app"
	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	"github.com/donnel666/remail/internal/proxy/domain"
)

type ProxyRepository interface {
	CreateWithLog(ctx context.Context, proxy *domain.Proxy, log *governancedomain.OperationLog) error
	CreateWithLogAndCheckJob(ctx context.Context, proxy *domain.Proxy, log *governancedomain.OperationLog, task ProxyCheckTask) (*ProxyCheckJob, error)
	CreateBatchWithLog(ctx context.Context, proxies []*domain.Proxy, log *governancedomain.OperationLog) ([]domain.Proxy, int, error)
	CreateBatchWithLogAndCheckJob(ctx context.Context, proxies []*domain.Proxy, log *governancedomain.OperationLog, task ProxyCheckBatchTask) ([]domain.Proxy, int, *ProxyCheckJob, error)
	FindByID(ctx context.Context, id uint) (*domain.Proxy, error)
	List(ctx context.Context, filter ProxyListFilter, offset, limit int) ([]domain.Proxy, error)
	Count(ctx context.Context, filter ProxyListFilter) (int64, error)
	CountDisableCandidates(ctx context.Context, filter ProxyListFilter) (int64, error)
	Stats(ctx context.Context, filter ProxyListFilter) (*ProxyStats, error)
	ListBindings(ctx context.Context, filter ProxyBindingListFilter, offset, limit int) ([]domain.Binding, error)
	CountBindings(ctx context.Context, filter ProxyBindingListFilter) (int64, error)
	UpdateWithLog(ctx context.Context, proxy *domain.Proxy, log *governancedomain.OperationLog) error
	UpdateWithLogAndCheckJob(ctx context.Context, proxy *domain.Proxy, log *governancedomain.OperationLog, task ProxyCheckTask) (*ProxyCheckJob, error)
	DeleteBatchWithLog(ctx context.Context, ids []uint, log *governancedomain.OperationLog) ([]uint, error)
	DeleteByFilterWithLog(ctx context.Context, filter ProxyListFilter, log *governancedomain.OperationLog) (int64, error)
	DisableByFilterWithLog(ctx context.Context, filter ProxyListFilter, log *governancedomain.OperationLog) (int64, error)
	ListIDs(ctx context.Context, filter ProxyListFilter, afterID uint, limit int) ([]uint, error)
	MarkExpiredBefore(ctx context.Context, now time.Time) (int64, error)
	UpdateCheckResultWithLog(ctx context.Context, id uint, result domain.CheckResult, success bool, log *governancedomain.OperationLog) (*domain.Proxy, error)
	CreateCheckBatchJobWithLog(ctx context.Context, task ProxyCheckBatchTask, log *governancedomain.OperationLog) (*ProxyCheckJob, error)
	ListPendingProxyCheckJobs(ctx context.Context, limit int) ([]ProxyCheckJob, error)
	ListProxyCheckJobItemIDs(ctx context.Context, jobID uint, afterProxyID uint, limit int) ([]uint, error)
	MarkProxyCheckJobQueued(ctx context.Context, jobID uint) error
	MarkProxyCheckJobDispatchFailed(ctx context.Context, jobID uint, safeError string) error
	MarkProxyCheckJobRunning(ctx context.Context, jobID uint) error
	MarkProxyCheckJobSucceeded(ctx context.Context, jobID uint) error
	MarkProxyCheckJobFailed(ctx context.Context, jobID uint, safeError string) error
	AcquireResourceProxy(ctx context.Context, key string, ipVersion domain.ProxyIPVersion, now time.Time, bindingTTL time.Duration) (*domain.Proxy, error)
	AcquireSystemProxy(ctx context.Context, ipVersion domain.ProxyIPVersion, now time.Time) (*domain.Proxy, error)
	ReportSuccess(ctx context.Context, proxyID uint, usedAt time.Time) error
	ReportFailure(ctx context.Context, proxyID uint, safeError string, retryable bool) (*domain.Proxy, error)
}

type ProxyChecker interface {
	Check(ctx context.Context, proxyURL string) (domain.CheckResult, error)
}

type ProxyCheckQueue interface {
	EnqueueProxyCheck(ctx context.Context, task ProxyCheckTask) error
	EnqueueProxyCheckBatch(ctx context.Context, task ProxyCheckBatchTask) error
	EnqueueProxyCheckDispatcher(ctx context.Context, delay time.Duration) error
}

type ProxyCheckTask struct {
	JobID          uint   `json:"jobId,omitempty"`
	ProxyID        uint   `json:"proxyId"`
	OperatorUserID uint   `json:"operatorUserId"`
	RequestID      string `json:"requestId"`
	Path           string `json:"path"`
}

type ProxyCheckBatchTask struct {
	JobID          uint                `json:"jobId,omitempty"`
	Mode           ProxyCheckBatchMode `json:"mode,omitempty"`
	ProxyIDs       []uint              `json:"proxyIds,omitempty"`
	Filter         ProxyListFilter     `json:"filter"`
	OperatorUserID uint                `json:"operatorUserId"`
	RequestID      string              `json:"requestId"`
	Path           string              `json:"path"`
}

type ProxyCheckJobKind string

const (
	ProxyCheckJobSingle ProxyCheckJobKind = "single"
	ProxyCheckJobBatch  ProxyCheckJobKind = "batch"
)

type ProxyCheckBatchMode string

const (
	ProxyCheckBatchModeIDs    ProxyCheckBatchMode = "ids"
	ProxyCheckBatchModeFilter ProxyCheckBatchMode = "filter"
)

type ProxyCheckJobStatus string

const (
	ProxyCheckJobPending   ProxyCheckJobStatus = "pending"
	ProxyCheckJobQueued    ProxyCheckJobStatus = "queued"
	ProxyCheckJobRunning   ProxyCheckJobStatus = "running"
	ProxyCheckJobSucceeded ProxyCheckJobStatus = "succeeded"
	ProxyCheckJobFailed    ProxyCheckJobStatus = "failed"
)

type ProxyCheckJob struct {
	ID             uint
	Kind           ProxyCheckJobKind
	Mode           ProxyCheckBatchMode
	Status         ProxyCheckJobStatus
	ProxyID        uint
	ProxyIDs       []uint
	Filter         ProxyListFilter
	OperatorUserID uint
	RequestID      string
	Path           string
	LastSafeError  string
	CreatedAt      time.Time
	UpdatedAt      time.Time
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
	ExpireAt *time.Time
}

type ImportProxiesRequest struct {
	Pool     domain.ProxyPool
	URLs     []string
	ExpireAt *time.Time
}

type UpdateProxyRequest struct {
	URL         *string
	Status      *domain.ProxyStatus
	ExpireAt    *time.Time
	ExpireAtSet bool
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

type DisableProxiesResult struct {
	Requested        int
	Disabled         int
	DisabledByFilter bool
}

type ImportProxiesResult struct {
	Requested  int
	Created    int
	Duplicated int
	Items      []domain.Proxy
}

type CheckProxiesResult struct {
	Requested int
	Queued    int
	Checked   int
	Failed    int
	Items     []domain.Proxy
}

type DispatchProxyCheckJobsResult struct {
	Attempted int
	Queued    int
	Failed    int
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
	queue   ProxyCheckQueue
	ops     governanceapp.OperationLogPort
	systems governanceapp.SystemLogPort
	now     func() time.Time
}

const (
	defaultProxyListLimit = 20
	maxProxyListLimit     = 10000
	resourceBindingTTL    = 7 * 24 * time.Hour
	maxProxyAttempts      = 3
	batchCheckIDPageSize  = 1000
	pendingCheckJobLimit  = 100
	proxyCheckAttempts    = 3
)

func NewProxyUseCase(
	proxies ProxyRepository,
	checker ProxyChecker,
	queue ProxyCheckQueue,
	ops governanceapp.OperationLogPort,
	systems governanceapp.SystemLogPort,
) *ProxyUseCase {
	return &ProxyUseCase{
		proxies: proxies,
		checker: checker,
		queue:   queue,
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
	if err := uc.markExpiredBefore(ctx, "", "list"); err != nil {
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
	if err := uc.markExpiredBefore(ctx, "", "stats"); err != nil {
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
	expireAt, err := normalizeOptionalProxyExpireAt(req.ExpireAt, uc.now())
	if err != nil {
		return nil, domain.ErrInvalidProxyExpireAt
	}
	proxy := &domain.Proxy{
		Pool:      req.Pool,
		URL:       normalizedURL,
		ExpireAt:  expireAt,
		Status:    domain.ProxyStatusChecking,
		Country:   "UNKNOWN",
		Errors:    0,
		LatencyMs: 0,
	}
	log := uc.operationLog(operatorUserID, requestID, path, "proxy.proxy.create", "", "success", "Proxy created.")
	job, err := uc.proxies.CreateWithLogAndCheckJob(ctx, proxy, log, ProxyCheckTask{
		OperatorUserID: operatorUserID,
		RequestID:      requestID,
		Path:           path,
	})
	if err != nil {
		_ = uc.writeOperationLog(ctx, operatorUserID, requestID, path, "proxy.proxy.create", "0", "failure", "Proxy create failed.")
		return nil, err
	}
	uc.dispatchProxyCheckJob(ctx, job)
	return proxy, nil
}

func (uc *ProxyUseCase) Import(ctx context.Context, operatorUserID uint, requestID, path string, req ImportProxiesRequest) (*ImportProxiesResult, error) {
	if !domain.IsValidProxyPool(string(req.Pool)) {
		return nil, domain.ErrInvalidProxyPool
	}
	expireAt, err := normalizeOptionalProxyExpireAt(req.ExpireAt, uc.now())
	if err != nil {
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
	proxies := make([]*domain.Proxy, 0, len(normalizedURLs))
	for _, normalizedURL := range normalizedURLs {
		proxies = append(proxies, &domain.Proxy{
			Pool:      req.Pool,
			URL:       normalizedURL,
			ExpireAt:  expireAt,
			Status:    domain.ProxyStatusChecking,
			Country:   "UNKNOWN",
			Errors:    0,
			LatencyMs: 0,
		})
	}
	log := uc.operationLog(operatorUserID, requestID, path, "proxy.proxy.import", "batch", "success", "Proxy imported.")
	created, existingDuplicates, job, err := uc.proxies.CreateBatchWithLogAndCheckJob(ctx, proxies, log, ProxyCheckBatchTask{
		Mode:           ProxyCheckBatchModeIDs,
		OperatorUserID: operatorUserID,
		RequestID:      requestID,
		Path:           path,
	})
	if err != nil {
		_ = uc.writeOperationLog(ctx, operatorUserID, requestID, path, "proxy.proxy.import", "batch", "failure", "Proxy import failed.")
		return nil, err
	}
	duplicates += existingDuplicates
	uc.dispatchProxyCheckBatchJob(ctx, job)
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

	needsCheck := false
	if req.ExpireAtSet {
		wasExpired := proxy.Status == domain.ProxyStatusExpired
		expireAt, err := normalizeOptionalProxyExpireAt(req.ExpireAt, uc.now())
		if err != nil {
			return nil, domain.ErrInvalidProxyExpireAt
		}
		proxy.ExpireAt = expireAt
		if wasExpired {
			if err := proxy.MarkChecking(); err != nil {
				return nil, err
			}
			needsCheck = true
		}
	}
	if req.URL != nil {
		normalizedURL, err := domain.NormalizeProxyURL(*req.URL)
		if err != nil {
			return nil, err
		}
		if normalizedURL != proxy.URL {
			proxy.URL = normalizedURL
			proxy.IPVersion = ""
			proxy.OutboundIP = ""
			proxy.Country = "UNKNOWN"
			proxy.LatencyMs = 0
			proxy.Errors = 0
			if err := proxy.MarkChecking(); err != nil {
				return nil, err
			}
			proxy.LastCheckedAt = nil
			needsCheck = true
		}
	}
	if req.Status != nil {
		switch *req.Status {
		case domain.ProxyStatusDisabled:
			if err := proxy.MarkDisabled("Proxy disabled by administrator."); err != nil {
				return nil, err
			}
			needsCheck = false
		case domain.ProxyStatusChecking:
			if err := proxy.MarkChecking(); err != nil {
				return nil, err
			}
			needsCheck = true
		default:
			return nil, domain.ErrInvalidProxyStatus
		}
	}
	log := uc.operationLog(operatorUserID, requestID, path, "proxy.proxy.update", fmt.Sprintf("%d", proxy.ID), "success", "Proxy updated.")
	var job *ProxyCheckJob
	if needsCheck {
		job, err = uc.proxies.UpdateWithLogAndCheckJob(ctx, proxy, log, ProxyCheckTask{
			ProxyID:        id,
			OperatorUserID: operatorUserID,
			RequestID:      requestID,
			Path:           path,
		})
	} else {
		err = uc.proxies.UpdateWithLog(ctx, proxy, log)
	}
	if err != nil {
		_ = uc.writeOperationLog(ctx, operatorUserID, requestID, path, "proxy.proxy.update", fmt.Sprintf("%d", id), "failure", "Proxy update failed.")
		return nil, err
	}
	uc.dispatchProxyCheckJob(ctx, job)
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
	requested, err := uc.proxies.Count(ctx, filter)
	if err != nil {
		return nil, err
	}
	if requested == 0 {
		return nil, domain.ErrInvalidProxyFilter
	}
	log := uc.operationLog(operatorUserID, requestID, path, "proxy.proxy.delete", "filter", "success", "Proxy deleted.")
	deleted, err := uc.proxies.DeleteByFilterWithLog(ctx, filter, log)
	if err != nil {
		_ = uc.writeOperationLog(ctx, operatorUserID, requestID, path, "proxy.proxy.delete", "filter", "failure", "Proxy delete failed.")
		return nil, err
	}
	return &DeleteProxiesResult{
		Requested:       int(requested),
		Deleted:         int(deleted),
		DeletedByFilter: true,
	}, nil
}

func (uc *ProxyUseCase) DisableByFilter(ctx context.Context, filter ProxyListFilter, operatorUserID uint, requestID, path string) (*DisableProxiesResult, error) {
	if err := validateProxyListFilter(filter); err != nil {
		return nil, err
	}
	requested, err := uc.proxies.CountDisableCandidates(ctx, filter)
	if err != nil {
		return nil, err
	}
	if requested == 0 {
		return nil, domain.ErrInvalidProxyFilter
	}
	log := uc.operationLog(operatorUserID, requestID, path, "proxy.proxy.disable", "filter", "success", "Proxy disabled.")
	disabled, err := uc.proxies.DisableByFilterWithLog(ctx, filter, log)
	if err != nil {
		_ = uc.writeOperationLog(ctx, operatorUserID, requestID, path, "proxy.proxy.disable", "filter", "failure", "Proxy disable failed.")
		return nil, err
	}
	return &DisableProxiesResult{
		Requested:        int(requested),
		Disabled:         int(disabled),
		DisabledByFilter: true,
	}, nil
}

func (uc *ProxyUseCase) Check(ctx context.Context, id uint, operatorUserID uint, requestID, path string) (*domain.Proxy, error) {
	proxy, err := uc.Get(ctx, id)
	if err != nil {
		return nil, err
	}

	if err := proxy.MarkChecking(); err != nil {
		return nil, err
	}
	log := uc.operationLog(operatorUserID, requestID, path, "proxy.proxy.check", fmt.Sprintf("%d", id), "success", "Proxy check queued.")
	job, err := uc.proxies.UpdateWithLogAndCheckJob(ctx, proxy, log, ProxyCheckTask{
		ProxyID:        id,
		OperatorUserID: operatorUserID,
		RequestID:      requestID,
		Path:           path,
	})
	if err != nil {
		_ = uc.writeOperationLog(ctx, operatorUserID, requestID, path, "proxy.proxy.check", fmt.Sprintf("%d", id), "failure", "Proxy check queue failed.")
		return nil, err
	}
	uc.dispatchProxyCheckJob(ctx, job)
	return proxy, nil
}

func (uc *ProxyUseCase) RunCheck(ctx context.Context, task ProxyCheckTask) (updatedProxy *domain.Proxy, err error) {
	if task.JobID != 0 {
		_ = uc.proxies.MarkProxyCheckJobRunning(ctx, task.JobID)
		defer func() {
			if err != nil {
				_ = uc.proxies.MarkProxyCheckJobFailed(ctx, task.JobID, err.Error())
				return
			}
			_ = uc.proxies.MarkProxyCheckJobSucceeded(ctx, task.JobID)
		}()
	}
	if task.ProxyID == 0 {
		return nil, domain.ErrProxyNotFound
	}
	proxy, err := uc.Get(ctx, task.ProxyID)
	if err != nil {
		return nil, err
	}

	if proxy.Status != domain.ProxyStatusChecking {
		_ = uc.writeSystemLog(
			ctx,
			"warning",
			"proxy.check_task_skipped",
			task.RequestID,
			"proxy",
			fmt.Sprintf("%d", task.ProxyID),
			"Proxy check task skipped because status changed.",
			fmt.Sprintf("current status is %s", proxy.Status),
		)
		return proxy, nil
	}

	result, checkErr := uc.runProxyCheckAttempts(ctx, proxy.URL)
	if result.CheckedAt.IsZero() {
		result.CheckedAt = uc.now()
	}
	if checkErr != nil {
		if result.LastSafeError == "" {
			result.LastSafeError = "Proxy check failed."
		}
		log := uc.operationLog(task.OperatorUserID, task.RequestID, task.Path, "proxy.proxy.check", fmt.Sprintf("%d", task.ProxyID), "failure", "Proxy check failed.")
		updated, updateErr := uc.proxies.UpdateCheckResultWithLog(ctx, task.ProxyID, result, false, log)
		if updateErr != nil {
			_ = uc.writeOperationLog(ctx, task.OperatorUserID, task.RequestID, task.Path, "proxy.proxy.check", fmt.Sprintf("%d", task.ProxyID), "failure", "Proxy check failed.")
			return nil, updateErr
		}
		_ = uc.writeSystemLog(ctx, "warning", "proxy.check_failed", task.RequestID, "proxy", fmt.Sprintf("%d", task.ProxyID), "Proxy check failed.", result.LastSafeError)
		if updated.Status == domain.ProxyStatusDisabled {
			_ = uc.writeSystemLog(ctx, "warning", "proxy.auto_disabled", task.RequestID, "proxy", fmt.Sprintf("%d", task.ProxyID), "Proxy disabled automatically.", updated.LastSafeError)
		}
		return updated, nil
	}

	log := uc.operationLog(task.OperatorUserID, task.RequestID, task.Path, "proxy.proxy.check", fmt.Sprintf("%d", task.ProxyID), "success", "Proxy check succeeded.")
	updated, err := uc.proxies.UpdateCheckResultWithLog(ctx, task.ProxyID, result, true, log)
	if err != nil {
		_ = uc.writeOperationLog(ctx, task.OperatorUserID, task.RequestID, task.Path, "proxy.proxy.check", fmt.Sprintf("%d", task.ProxyID), "failure", "Proxy check failed.")
		return nil, err
	}
	return updated, nil
}

func (uc *ProxyUseCase) CheckBatch(ctx context.Context, ids []uint, operatorUserID uint, requestID, path string) (*CheckProxiesResult, error) {
	uniqueIDs, err := normalizeProxyIDs(ids)
	if err != nil {
		return nil, err
	}
	log := uc.operationLog(operatorUserID, requestID, path, "proxy.proxy.check_batch", "batch", "success", "Proxy batch check queued.")
	job, err := uc.proxies.CreateCheckBatchJobWithLog(ctx, ProxyCheckBatchTask{
		Mode:           ProxyCheckBatchModeIDs,
		ProxyIDs:       uniqueIDs,
		OperatorUserID: operatorUserID,
		RequestID:      requestID,
		Path:           path,
	}, log)
	if err != nil {
		_ = uc.writeOperationLog(ctx, operatorUserID, requestID, path, "proxy.proxy.check_batch", "batch", "failure", "Proxy batch check queue failed.")
		return nil, err
	}
	uc.dispatchProxyCheckBatchJob(ctx, job)
	return &CheckProxiesResult{
		Requested: len(uniqueIDs),
		Queued:    len(uniqueIDs),
		Items:     []domain.Proxy{},
	}, nil
}

func (uc *ProxyUseCase) CheckByFilter(ctx context.Context, filter ProxyListFilter, operatorUserID uint, requestID, path string) (*CheckProxiesResult, error) {
	if err := validateProxyListFilter(filter); err != nil {
		return nil, err
	}
	requested, err := uc.proxies.Count(ctx, filter)
	if err != nil {
		return nil, err
	}
	if requested == 0 {
		return nil, domain.ErrInvalidProxyFilter
	}
	log := uc.operationLog(operatorUserID, requestID, path, "proxy.proxy.check_batch", "filter", "success", "Proxy batch check queued.")
	job, err := uc.proxies.CreateCheckBatchJobWithLog(ctx, ProxyCheckBatchTask{
		Mode:           ProxyCheckBatchModeFilter,
		Filter:         filter,
		OperatorUserID: operatorUserID,
		RequestID:      requestID,
		Path:           path,
	}, log)
	if err != nil {
		_ = uc.writeOperationLog(ctx, operatorUserID, requestID, path, "proxy.proxy.check_batch", "filter", "failure", "Proxy batch check queue failed.")
		return nil, err
	}
	uc.dispatchProxyCheckBatchJob(ctx, job)
	return &CheckProxiesResult{
		Requested: int(requested),
		Queued:    int(requested),
	}, nil
}

func (uc *ProxyUseCase) RunCheckBatch(ctx context.Context, task ProxyCheckBatchTask) (result *CheckProxiesResult, err error) {
	if task.JobID != 0 {
		_ = uc.proxies.MarkProxyCheckJobRunning(ctx, task.JobID)
		defer func() {
			if err != nil {
				_ = uc.proxies.MarkProxyCheckJobFailed(ctx, task.JobID, err.Error())
				return
			}
			_ = uc.proxies.MarkProxyCheckJobSucceeded(ctx, task.JobID)
		}()
	}
	mode := task.Mode
	if mode == "" {
		if len(task.ProxyIDs) > 0 {
			mode = ProxyCheckBatchModeIDs
		} else {
			mode = ProxyCheckBatchModeFilter
		}
	}
	if mode == ProxyCheckBatchModeIDs {
		if len(task.ProxyIDs) > 0 {
			ids, err := normalizeProxyIDs(task.ProxyIDs)
			if err != nil {
				return nil, err
			}
			result, err := uc.queueProxyCheckIDs(ctx, ids, task.OperatorUserID, task.RequestID, task.Path)
			if err != nil {
				_ = uc.writeOperationLog(ctx, task.OperatorUserID, task.RequestID, task.Path, "proxy.proxy.check_batch", "batch", "failure", "Proxy batch check queue failed.")
				return nil, err
			}
			_ = uc.writeOperationLog(ctx, task.OperatorUserID, task.RequestID, task.Path, "proxy.proxy.check_batch", "batch", "success", "Proxy batch check worker finished.")
			return result, nil
		}
		if task.JobID == 0 {
			return nil, domain.ErrInvalidProxyFilter
		}
		result, err := uc.queueProxyCheckJobItems(ctx, task.JobID, task.OperatorUserID, task.RequestID, task.Path)
		if err != nil {
			_ = uc.writeOperationLog(ctx, task.OperatorUserID, task.RequestID, task.Path, "proxy.proxy.check_batch", "batch", "failure", "Proxy batch check queue failed.")
			return nil, err
		}
		_ = uc.writeOperationLog(ctx, task.OperatorUserID, task.RequestID, task.Path, "proxy.proxy.check_batch", "batch", "success", "Proxy batch check worker finished.")
		return result, nil
	}
	if mode != ProxyCheckBatchModeFilter {
		return nil, domain.ErrInvalidProxyFilter
	}

	filter := task.Filter
	if err := validateProxyListFilter(filter); err != nil {
		return nil, err
	}
	result = &CheckProxiesResult{}
	var afterID uint
	for {
		ids, err := uc.proxies.ListIDs(ctx, filter, afterID, batchCheckIDPageSize)
		if err != nil {
			return nil, err
		}
		if len(ids) == 0 {
			break
		}
		batch, err := uc.queueProxyCheckIDs(ctx, ids, task.OperatorUserID, task.RequestID, task.Path)
		if err != nil {
			_ = uc.writeOperationLog(ctx, task.OperatorUserID, task.RequestID, task.Path, "proxy.proxy.check_batch", "filter", "failure", "Proxy batch check queue failed.")
			return nil, err
		}
		result.Requested += batch.Requested
		result.Queued += batch.Queued
		afterID = ids[len(ids)-1]
		if len(ids) < batchCheckIDPageSize {
			break
		}
	}
	if result.Requested == 0 {
		return nil, domain.ErrInvalidProxyFilter
	}
	_ = uc.writeOperationLog(ctx, task.OperatorUserID, task.RequestID, task.Path, "proxy.proxy.check_batch", "filter", "success", "Proxy batch check worker finished.")
	return result, nil
}

func (uc *ProxyUseCase) RunCheckByFilter(ctx context.Context, task ProxyCheckBatchTask) (*CheckProxiesResult, error) {
	return uc.RunCheckBatch(ctx, task)
}

func (uc *ProxyUseCase) queueProxyCheckIDs(ctx context.Context, ids []uint, operatorUserID uint, requestID, path string) (*CheckProxiesResult, error) {
	result := &CheckProxiesResult{
		Requested: len(ids),
		Items:     make([]domain.Proxy, 0, len(ids)),
	}
	for _, id := range ids {
		proxy, err := uc.Check(ctx, id, operatorUserID, requestID, path)
		if err != nil {
			if errors.Is(err, domain.ErrProxyUnavailable) || errors.Is(err, domain.ErrProxyNotFound) || errors.Is(err, domain.ErrInvalidProxyStatus) {
				result.Failed++
				if proxy != nil {
					result.Items = append(result.Items, *proxy)
				}
				_ = uc.writeSystemLog(ctx, "warning", "proxy.check_queue_skipped", requestID, "proxy", fmt.Sprintf("%d", id), "Proxy check was skipped.", err.Error())
				continue
			}
			return result, err
		}
		result.Queued++
		if proxy != nil {
			result.Items = append(result.Items, *proxy)
		}
	}
	return result, nil
}

func (uc *ProxyUseCase) queueProxyCheckJobItems(ctx context.Context, jobID uint, operatorUserID uint, requestID, path string) (*CheckProxiesResult, error) {
	if jobID == 0 {
		return nil, domain.ErrInvalidProxyFilter
	}
	result := &CheckProxiesResult{}
	var afterID uint
	for {
		ids, err := uc.proxies.ListProxyCheckJobItemIDs(ctx, jobID, afterID, batchCheckIDPageSize)
		if err != nil {
			return nil, err
		}
		if len(ids) == 0 {
			break
		}
		batch, err := uc.queueProxyCheckIDs(ctx, ids, operatorUserID, requestID, path)
		if err != nil {
			return nil, err
		}
		result.Requested += batch.Requested
		result.Queued += batch.Queued
		result.Failed += batch.Failed
		result.Items = append(result.Items, batch.Items...)
		afterID = ids[len(ids)-1]
		if len(ids) < batchCheckIDPageSize {
			break
		}
	}
	if result.Requested == 0 {
		return nil, domain.ErrInvalidProxyFilter
	}
	return result, nil
}

func (uc *ProxyUseCase) runProxyCheckAttempts(ctx context.Context, proxyURL string) (domain.CheckResult, error) {
	var lastResult domain.CheckResult
	var lastErr error
	for attempt := 0; attempt < proxyCheckAttempts; attempt++ {
		result, err := uc.checker.Check(ctx, proxyURL)
		if result.CheckedAt.IsZero() {
			result.CheckedAt = uc.now()
		}
		if err == nil {
			return result, nil
		}
		lastResult = result
		lastErr = err
		if result.NonRetryable {
			break
		}
	}
	if lastResult.LastSafeError == "" {
		lastResult.LastSafeError = "Proxy check failed."
	}
	if errors.Is(lastErr, domain.ErrProxyCheckFailed) {
		return lastResult, lastErr
	}
	return lastResult, fmt.Errorf("%w: %v", domain.ErrProxyCheckFailed, lastErr)
}

func (uc *ProxyUseCase) dispatchProxyCheckJob(ctx context.Context, job *ProxyCheckJob) bool {
	if job == nil {
		return false
	}
	task := ProxyCheckTask{
		JobID:          job.ID,
		ProxyID:        job.ProxyID,
		OperatorUserID: job.OperatorUserID,
		RequestID:      job.RequestID,
		Path:           job.Path,
	}
	if uc.queue == nil {
		err := errors.New("proxy check queue is unavailable")
		_ = uc.proxies.MarkProxyCheckJobDispatchFailed(ctx, job.ID, err.Error())
		uc.markProxyCheckQueueFailure(ctx, job.ProxyID, job.OperatorUserID, job.RequestID, job.Path, err)
		return false
	}
	if err := uc.queue.EnqueueProxyCheck(ctx, task); err != nil {
		_ = uc.proxies.MarkProxyCheckJobDispatchFailed(ctx, job.ID, err.Error())
		uc.markProxyCheckQueueFailure(ctx, job.ProxyID, job.OperatorUserID, job.RequestID, job.Path, err)
		return false
	}
	_ = uc.proxies.MarkProxyCheckJobQueued(ctx, job.ID)
	return true
}

func (uc *ProxyUseCase) dispatchProxyCheckBatchJob(ctx context.Context, job *ProxyCheckJob) bool {
	if job == nil {
		return false
	}
	task := ProxyCheckBatchTask{
		JobID:          job.ID,
		Mode:           job.Mode,
		Filter:         job.Filter,
		OperatorUserID: job.OperatorUserID,
		RequestID:      job.RequestID,
		Path:           job.Path,
	}
	if uc.queue == nil {
		err := errors.New("proxy check queue is unavailable")
		_ = uc.proxies.MarkProxyCheckJobDispatchFailed(ctx, job.ID, err.Error())
		uc.writeProxyCheckBatchQueueFailure(ctx, job.RequestID, proxyCheckBatchJobBizID(job), err)
		return false
	}
	if err := uc.queue.EnqueueProxyCheckBatch(ctx, task); err != nil {
		_ = uc.proxies.MarkProxyCheckJobDispatchFailed(ctx, job.ID, err.Error())
		uc.writeProxyCheckBatchQueueFailure(ctx, job.RequestID, proxyCheckBatchJobBizID(job), err)
		return false
	}
	_ = uc.proxies.MarkProxyCheckJobQueued(ctx, job.ID)
	return true
}

func (uc *ProxyUseCase) DispatchPendingProxyCheckJobs(ctx context.Context, limit int) (*DispatchProxyCheckJobsResult, error) {
	if limit <= 0 || limit > pendingCheckJobLimit {
		limit = pendingCheckJobLimit
	}
	jobs, err := uc.proxies.ListPendingProxyCheckJobs(ctx, limit)
	if err != nil {
		return nil, err
	}
	result := &DispatchProxyCheckJobsResult{Attempted: len(jobs)}
	for i := range jobs {
		var queued bool
		switch jobs[i].Kind {
		case ProxyCheckJobSingle:
			queued = uc.dispatchProxyCheckJob(ctx, &jobs[i])
		case ProxyCheckJobBatch:
			queued = uc.dispatchProxyCheckBatchJob(ctx, &jobs[i])
		default:
			_ = uc.proxies.MarkProxyCheckJobFailed(ctx, jobs[i].ID, "Invalid proxy check job kind.")
			_ = uc.writeSystemLog(ctx, "error", "proxy.check_job_invalid", jobs[i].RequestID, "proxy_check_job", fmt.Sprintf("%d", jobs[i].ID), "Proxy check job is invalid.", "Invalid proxy check job kind.")
		}
		if queued {
			result.Queued++
		} else {
			result.Failed++
		}
	}
	return result, nil
}

func (uc *ProxyUseCase) ScheduleProxyCheckDispatcher(ctx context.Context, delay time.Duration) {
	if uc.queue == nil {
		return
	}
	if err := uc.queue.EnqueueProxyCheckDispatcher(ctx, delay); err != nil {
		_ = uc.writeSystemLog(ctx, "error", "proxy.check_dispatcher_enqueue_failed", "", "proxy_check_job", "dispatcher", "Proxy check dispatcher could not be queued.", err.Error())
	}
}

func (uc *ProxyUseCase) markProxyCheckQueueFailure(ctx context.Context, id uint, operatorUserID uint, requestID, path string, queueErr error) {
	detail := ""
	if queueErr != nil {
		detail = queueErr.Error()
	}
	_ = uc.writeOperationLog(ctx, operatorUserID, requestID, path, "proxy.proxy.check", fmt.Sprintf("%d", id), "failure", "Proxy check queue failed.")
	_ = uc.writeSystemLog(ctx, "error", "proxy.check_queue_failed", requestID, "proxy", fmt.Sprintf("%d", id), "Proxy check task could not be queued. Durable check job is pending for dispatcher recovery.", detail)
}

func (uc *ProxyUseCase) writeProxyCheckBatchQueueFailure(ctx context.Context, requestID, bizID string, queueErr error) {
	detail := ""
	if queueErr != nil {
		detail = queueErr.Error()
	}
	_ = uc.writeSystemLog(
		ctx,
		"error",
		"proxy.check_batch_queue_failed",
		requestID,
		"proxy",
		bizID,
		"Proxy batch check task could not be queued.",
		detail,
	)
}

func proxyCheckBatchJobBizID(job *ProxyCheckJob) string {
	if job == nil {
		return "batch"
	}
	if job.Mode == ProxyCheckBatchModeIDs {
		return "batch"
	}
	return "filter"
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
	if err := uc.markExpiredBefore(ctx, req.RequestID, "acquire"); err != nil {
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

func (uc *ProxyUseCase) markExpiredBefore(ctx context.Context, requestID, source string) error {
	updated, err := uc.proxies.MarkExpiredBefore(ctx, uc.now())
	if err != nil {
		return err
	}
	if updated > 0 {
		_ = uc.writeSystemLog(
			ctx,
			"info",
			"proxy.expired_scan",
			requestID,
			"proxy",
			source,
			fmt.Sprintf("Proxy expiration scan marked %d proxies expired.", updated),
			fmt.Sprintf("source=%s count=%d", source, updated),
		)
	}
	return nil
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

func (uc *ProxyUseCase) LogTaskFailure(ctx context.Context, eventType, requestID, bizType, bizID, message string, err error) {
	detail := ""
	if err != nil {
		detail = err.Error()
	}
	_ = uc.writeSystemLog(ctx, "error", eventType, requestID, bizType, bizID, message, detail)
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

func proxyIDs(items []domain.Proxy) []uint {
	ids := make([]uint, 0, len(items))
	for _, item := range items {
		if item.ID == 0 {
			continue
		}
		ids = append(ids, item.ID)
	}
	return ids
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

func normalizeOptionalProxyExpireAt(expireAt *time.Time, now time.Time) (time.Time, error) {
	if expireAt == nil {
		return time.Time{}, nil
	}
	if !expireAt.After(now) {
		return time.Time{}, domain.ErrInvalidProxyExpireAt
	}
	return expireAt.UTC(), nil
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
