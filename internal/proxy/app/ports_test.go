package app

import (
	"context"
	"errors"
	"testing"
	"time"

	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	"github.com/donnel666/remail/internal/proxy/domain"
	"github.com/stretchr/testify/require"
)

type fakeProxyRepository struct {
	resourceProxy *domain.Proxy
	systemProxy   *domain.Proxy
	resourceErr   error
	systemErr     error
	findProxy     *domain.Proxy
	updatedProxy  *domain.Proxy
	count         int64
	nextID        uint
	pendingChecks []uint
	pendingTasks  map[uint]ProxyCheckTask
	failureProxy  *domain.Proxy

	resourceAcquireCount    int
	systemAcquireCount      int
	reportSuccessCount      int
	reportFailureCount      int
	checkResultUpdates      int
	checkResultErr          error
	expiredUpdated          int64
	activateCalls           int
	releaseCalls            int
	releaseErr              error
	activationBeforeEnqueue bool
	enqueueObserved         func() bool
}

func (r *fakeProxyRepository) Create(_ context.Context, proxy *domain.Proxy) error {
	if r.nextID == 0 {
		r.nextID = 1
	}
	proxy.ID = r.nextID
	r.nextID++
	return nil
}
func (r *fakeProxyRepository) CreateWithLog(ctx context.Context, proxy *domain.Proxy, _ *governancedomain.OperationLog) error {
	return r.Create(ctx, proxy)
}
func (r *fakeProxyRepository) CreateBatchWithLog(ctx context.Context, proxies []*domain.Proxy, _ *governancedomain.OperationLog) ([]domain.Proxy, int, error) {
	created := make([]domain.Proxy, 0, len(proxies))
	for _, proxy := range proxies {
		if proxy == nil {
			continue
		}
		if err := r.Create(ctx, proxy); err != nil {
			return nil, 0, err
		}
		created = append(created, *proxy)
		r.pendingChecks = append(r.pendingChecks, proxy.ID)
		r.rememberPendingTask(proxy.ID, proxy.CheckGeneration)
	}
	return created, 0, nil
}
func (r *fakeProxyRepository) FindByID(_ context.Context, id uint) (*domain.Proxy, error) {
	if r.findProxy == nil {
		return nil, nil
	}
	proxy := *r.findProxy
	proxy.ID = id
	return &proxy, nil
}
func (r *fakeProxyRepository) List(context.Context, ProxyListFilter, int, int) ([]domain.Proxy, error) {
	return nil, nil
}
func (r *fakeProxyRepository) Count(context.Context, ProxyListFilter) (int64, error) {
	return r.count, nil
}
func (r *fakeProxyRepository) CountDisableCandidates(context.Context, ProxyListFilter) (int64, error) {
	return r.count, nil
}
func (r *fakeProxyRepository) Stats(context.Context, ProxyListFilter) (*ProxyStats, error) {
	return &ProxyStats{}, nil
}
func (r *fakeProxyRepository) ListBindings(context.Context, ProxyBindingListFilter, int, int) ([]domain.Binding, error) {
	return nil, nil
}
func (r *fakeProxyRepository) CountBindings(context.Context, ProxyBindingListFilter) (int64, error) {
	return 0, nil
}
func (r *fakeProxyRepository) Update(context.Context, *domain.Proxy) error { return nil }
func (r *fakeProxyRepository) UpdateWithLog(_ context.Context, proxy *domain.Proxy, _ *governancedomain.OperationLog) error {
	if proxy == nil {
		r.updatedProxy = nil
		return nil
	}
	stored := *proxy
	r.updatedProxy = &stored
	return nil
}
func (r *fakeProxyRepository) UpdateWithLogAndBumpCheckGeneration(ctx context.Context, proxy *domain.Proxy, log *governancedomain.OperationLog) error {
	proxy.CheckGeneration++
	return r.UpdateWithLog(ctx, proxy, log)
}
func (r *fakeProxyRepository) DeleteBatch(context.Context, []uint) ([]uint, error) {
	return nil, nil
}
func (r *fakeProxyRepository) DeleteBatchWithLog(context.Context, []uint, *governancedomain.OperationLog) ([]uint, error) {
	return nil, nil
}
func (r *fakeProxyRepository) DeleteByFilter(context.Context, ProxyListFilter) (int64, error) {
	return r.count, nil
}
func (r *fakeProxyRepository) DeleteByFilterWithLog(context.Context, ProxyListFilter, *governancedomain.OperationLog) (int64, error) {
	return r.count, nil
}
func (r *fakeProxyRepository) DisableByFilterWithLog(context.Context, ProxyListFilter, *governancedomain.OperationLog) (int64, error) {
	return 0, nil
}
func (r *fakeProxyRepository) MarkPendingBatchWithLog(_ context.Context, ids []uint, _ *governancedomain.OperationLog) (int, int, error) {
	r.pendingChecks = append(r.pendingChecks, ids...)
	for _, id := range ids {
		r.rememberPendingTask(id, 1)
	}
	return len(ids), len(ids), nil
}
func (r *fakeProxyRepository) MarkPendingByFilterWithLog(_ context.Context, _ ProxyListFilter, _ *governancedomain.OperationLog) (int64, int64, error) {
	for id := uint(1); id <= uint(r.count); id++ {
		r.pendingChecks = append(r.pendingChecks, id)
		r.rememberPendingTask(id, 1)
	}
	return r.count, r.count, nil
}
func (r *fakeProxyRepository) MarkExpiredBefore(context.Context, time.Time) (int64, error) {
	return r.expiredUpdated, nil
}
func (r *fakeProxyRepository) ListPendingProxyChecks(_ context.Context, limit int) ([]ProxyCheckTask, error) {
	if limit <= 0 {
		limit = len(r.pendingChecks)
	}
	tasks := make([]ProxyCheckTask, 0, limit)
	for _, id := range r.pendingChecks {
		task := r.pendingTasks[id]
		task.ProxyID = id
		if task.CheckGeneration == 0 {
			task.CheckGeneration = 1
		}
		tasks = append(tasks, task)
		if len(tasks) == limit {
			break
		}
	}
	return tasks, nil
}
func (r *fakeProxyRepository) ActivateProxyCheck(_ context.Context, id uint, generation uint64) (bool, error) {
	r.activateCalls++
	if r.enqueueObserved != nil && !r.enqueueObserved() {
		r.activationBeforeEnqueue = true
	}
	if r.findProxy != nil {
		if r.findProxy.CheckGeneration == 0 {
			r.findProxy.CheckGeneration = generation
		}
		if r.findProxy.CheckGeneration != generation {
			return false, nil
		}
		r.findProxy.Status = domain.ProxyStatusChecking
	}
	return id != 0 && generation != 0, nil
}
func (r *fakeProxyRepository) ReleaseProxyCheckInfrastructureFailure(_ context.Context, id uint, generation uint64, safeError string) (bool, error) {
	r.releaseCalls++
	if r.releaseErr != nil {
		return false, r.releaseErr
	}
	if r.findProxy == nil || r.findProxy.ID != id || r.findProxy.Status != domain.ProxyStatusChecking || r.findProxy.CheckGeneration != generation {
		return false, nil
	}
	r.findProxy.Status = domain.ProxyStatusPending
	r.findProxy.CheckGeneration++
	r.findProxy.LastSafeError = safeError
	return true, nil
}
func (r *fakeProxyRepository) UpdateCheckResult(_ context.Context, _ uint, result domain.CheckResult, success bool) (*domain.Proxy, error) {
	r.checkResultUpdates++
	if r.checkResultErr != nil {
		return nil, r.checkResultErr
	}
	if r.findProxy == nil {
		return nil, domain.ErrProxyNotFound
	}
	proxy := *r.findProxy
	var err error
	if success {
		err = proxy.ApplyCheckSuccess(result)
	} else {
		err = proxy.ApplyCheckFailure(result)
	}
	if err != nil {
		return nil, err
	}
	r.updatedProxy = &proxy
	return &proxy, nil
}
func (r *fakeProxyRepository) UpdateCheckResultForGenerationWithLog(ctx context.Context, id uint, generation uint64, result domain.CheckResult, success bool, _ *governancedomain.OperationLog) (*domain.Proxy, error) {
	if r.findProxy == nil || r.findProxy.CheckGeneration != generation {
		return nil, domain.ErrInvalidProxyStatus
	}
	return r.UpdateCheckResult(ctx, id, result, success)
}

func (r *fakeProxyRepository) rememberPendingTask(proxyID uint, generation uint64) {
	if r.pendingTasks == nil {
		r.pendingTasks = make(map[uint]ProxyCheckTask)
	}
	if generation == 0 {
		generation = 1
	}
	r.pendingTasks[proxyID] = ProxyCheckTask{ProxyID: proxyID, CheckGeneration: generation}
}
func (r *fakeProxyRepository) AcquireResourceProxy(context.Context, string, domain.ProxyIPVersion, time.Time, time.Duration) (*domain.Proxy, error) {
	r.resourceAcquireCount++
	return r.resourceProxy, r.resourceErr
}
func (r *fakeProxyRepository) AcquireSystemProxy(context.Context, domain.ProxyIPVersion, time.Time) (*domain.Proxy, error) {
	r.systemAcquireCount++
	return r.systemProxy, r.systemErr
}
func (r *fakeProxyRepository) ReportSuccess(context.Context, uint, time.Time) error {
	r.reportSuccessCount++
	return nil
}
func (r *fakeProxyRepository) ReportFailure(_ context.Context, proxyID uint, safeError string, retryable bool) (*domain.Proxy, error) {
	r.reportFailureCount++
	if r.failureProxy == nil {
		return &domain.Proxy{Status: domain.ProxyStatusNormal}, nil
	}
	previous := r.failureProxy.Status
	if err := r.failureProxy.ReportFailure(safeError, retryable); err != nil {
		return nil, err
	}
	if previous != domain.ProxyStatusPending && r.failureProxy.Status == domain.ProxyStatusPending {
		r.failureProxy.CheckGeneration++
		r.pendingChecks = append(r.pendingChecks, proxyID)
		r.rememberPendingTask(proxyID, r.failureProxy.CheckGeneration)
	}
	return r.failureProxy, nil
}

type fakeProxyCheckQueue struct {
	tasks       []ProxyCheckTask
	dispatches  []time.Duration
	err         error
	dispatchErr error
	duplicate   bool
}

func (q *fakeProxyCheckQueue) EnqueueProxyCheck(_ context.Context, task ProxyCheckTask) (bool, error) {
	if q.err != nil {
		return false, q.err
	}
	if q.duplicate {
		return false, nil
	}
	q.tasks = append(q.tasks, task)
	return true, nil
}

func (q *fakeProxyCheckQueue) EnqueueProxyCheckDispatcher(_ context.Context, delay time.Duration) error {
	if q.dispatchErr != nil {
		return q.dispatchErr
	}
	q.dispatches = append(q.dispatches, delay)
	return nil
}

type fakeSystemLogPort struct {
	logs []*governancedomain.SystemLog
	err  error
}

type fakeOperationLogPort struct{}

func (fakeOperationLogPort) Create(context.Context, *governancedomain.OperationLog) error { return nil }

func (p *fakeSystemLogPort) Create(_ context.Context, log *governancedomain.SystemLog) error {
	if log != nil {
		copied := *log
		p.logs = append(p.logs, &copied)
	}
	return p.err
}

type fakeProxyChecker struct {
	calls  int
	result domain.CheckResult
	err    error
}

func (c *fakeProxyChecker) Check(context.Context, string) (domain.CheckResult, error) {
	c.calls++
	return c.result, c.err
}

func TestProxyCreatePersistsPendingAndWakesDispatcher(t *testing.T) {
	queue := &fakeProxyCheckQueue{}
	uc := NewProxyUseCase(&fakeProxyRepository{}, nil, queue, nil, nil)

	proxy, err := uc.Create(context.Background(), 1, "request-create", "/v1/admin/proxies/resource", CreateProxyRequest{
		Pool: domain.ProxyPoolResource,
		URL:  "http://127.0.0.1:18080",
	})

	require.NoError(t, err)
	require.Equal(t, uint(1), proxy.ID)
	require.Equal(t, domain.ProxyStatusPending, proxy.Status)
	require.Empty(t, queue.tasks)
	require.Len(t, queue.dispatches, 1)
}

func TestProxyImportPersistsPendingBeforeDispatcherEnqueuesChecks(t *testing.T) {
	repo := &fakeProxyRepository{}
	queue := &fakeProxyCheckQueue{}
	uc := NewProxyUseCase(repo, nil, queue, fakeOperationLogPort{}, nil)

	result, err := uc.Import(context.Background(), 1, "request-import", "/v1/admin/proxies/imports", ImportProxiesRequest{
		Pool: domain.ProxyPoolSystem,
		URLs: []string{
			"http://127.0.0.1:18080",
			"http://127.0.0.1:18081",
		},
	})

	require.NoError(t, err)
	require.Equal(t, 2, result.Created)
	require.Empty(t, queue.tasks)
	require.Len(t, queue.dispatches, 1)
	require.Len(t, repo.pendingChecks, 2)

	dispatched, err := uc.DispatchPendingProxyChecks(context.Background(), 10)
	require.NoError(t, err)
	require.Equal(t, 2, dispatched.Attempted)
	require.Equal(t, 2, dispatched.Queued)
	require.Len(t, queue.tasks, 2)
	require.Equal(t, uint64(1), queue.tasks[0].CheckGeneration)
}

func TestProxyCheckPersistsPendingAndWakesDispatcher(t *testing.T) {
	now := time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)
	repo := &fakeProxyRepository{
		findProxy: &domain.Proxy{
			ID:       7,
			Pool:     domain.ProxyPoolResource,
			URL:      "http://127.0.0.1:18080",
			ExpireAt: now.Add(time.Hour),
			Status:   domain.ProxyStatusNormal,
			Country:  "US",
		},
	}
	queue := &fakeProxyCheckQueue{}
	uc := NewProxyUseCase(repo, nil, queue, nil, nil)
	uc.now = func() time.Time { return now }

	proxy, err := uc.Check(context.Background(), 7, 1, "request-1", "/v1/admin/proxies/7/check")

	require.NoError(t, err)
	require.Equal(t, domain.ProxyStatusPending, proxy.Status)
	require.NotNil(t, repo.updatedProxy)
	require.Equal(t, domain.ProxyStatusPending, repo.updatedProxy.Status)
	require.Empty(t, queue.tasks)
	require.Len(t, queue.dispatches, 1)
}

func TestProxyCheckQueueFailureDoesNotApplyHealthFailure(t *testing.T) {
	now := time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)
	queueErr := errors.New("redis is unavailable")
	repo := &fakeProxyRepository{
		findProxy: &domain.Proxy{
			ID:       7,
			Pool:     domain.ProxyPoolResource,
			URL:      "http://127.0.0.1:18080",
			ExpireAt: now.Add(time.Hour),
			Status:   domain.ProxyStatusNormal,
			Country:  "US",
			Errors:   1,
		},
	}
	queue := &fakeProxyCheckQueue{dispatchErr: queueErr}
	systems := &fakeSystemLogPort{}
	uc := NewProxyUseCase(repo, nil, queue, nil, systems)
	uc.now = func() time.Time { return now }

	proxy, err := uc.Check(context.Background(), 7, 1, "request-queue-failed", "/v1/admin/proxies/7/check")

	require.NoError(t, err)
	require.NotNil(t, proxy)
	require.Equal(t, domain.ProxyStatusPending, proxy.Status)
	require.NotNil(t, repo.updatedProxy)
	require.Equal(t, domain.ProxyStatusPending, repo.updatedProxy.Status)
	require.Equal(t, 0, repo.updatedProxy.Errors)
	require.Equal(t, 0, repo.checkResultUpdates)
	require.Len(t, systems.logs, 1)
	require.Equal(t, "proxy.check_dispatcher_enqueue_failed", systems.logs[0].EventType)
	require.Contains(t, systems.logs[0].Detail, "redis is unavailable")
}

func TestProxyUpdateToCheckingQueuesTask(t *testing.T) {
	now := time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)
	repo := &fakeProxyRepository{
		findProxy: &domain.Proxy{
			ID:       9,
			Pool:     domain.ProxyPoolSystem,
			URL:      "http://127.0.0.1:18082",
			ExpireAt: now.Add(time.Hour),
			Status:   domain.ProxyStatusDisabled,
			Country:  "UNKNOWN",
		},
	}
	queue := &fakeProxyCheckQueue{}
	uc := NewProxyUseCase(repo, nil, queue, nil, nil)
	uc.now = func() time.Time { return now }
	status := domain.ProxyStatusChecking

	proxy, err := uc.Update(context.Background(), 9, 1, "request-2", "/v1/admin/proxies/9", UpdateProxyRequest{
		Status: &status,
	})

	require.NoError(t, err)
	require.Equal(t, domain.ProxyStatusPending, proxy.Status)
	require.NotNil(t, repo.updatedProxy)
	require.Equal(t, domain.ProxyStatusPending, repo.updatedProxy.Status)
	require.Empty(t, queue.tasks)
	require.Len(t, queue.dispatches, 1)
}

func TestProxyDeleteByFilterReportsRequestedCandidates(t *testing.T) {
	repo := &fakeProxyRepository{count: 5}
	uc := NewProxyUseCase(repo, nil, nil, nil, nil)

	result, err := uc.DeleteByFilter(context.Background(), ProxyListFilter{
		Pool:    domain.ProxyPoolSystem,
		Country: "US",
	}, 1, "request-delete-filter", "/v1/admin/proxies/delete")

	require.NoError(t, err)
	require.Equal(t, 5, result.Requested)
	require.Equal(t, 5, result.Deleted)
	require.True(t, result.DeletedByFilter)
}

func TestProxyUpdateExpiredExpireAtQueuesTask(t *testing.T) {
	now := time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)
	futureExpireAt := now.Add(time.Hour)
	repo := &fakeProxyRepository{
		findProxy: &domain.Proxy{
			ID:       10,
			Pool:     domain.ProxyPoolResource,
			URL:      "http://127.0.0.1:18083",
			ExpireAt: now.Add(-time.Hour),
			Status:   domain.ProxyStatusExpired,
			Country:  "US",
		},
	}
	queue := &fakeProxyCheckQueue{}
	uc := NewProxyUseCase(repo, nil, queue, nil, nil)
	uc.now = func() time.Time { return now }

	proxy, err := uc.Update(context.Background(), 10, 1, "request-3", "/v1/admin/proxies/10", UpdateProxyRequest{
		ExpireAt:    &futureExpireAt,
		ExpireAtSet: true,
	})

	require.NoError(t, err)
	require.Equal(t, domain.ProxyStatusPending, proxy.Status)
	require.NotNil(t, repo.updatedProxy)
	require.Equal(t, domain.ProxyStatusPending, repo.updatedProxy.Status)
	require.Empty(t, queue.tasks)
	require.Len(t, queue.dispatches, 1)
}

func TestProxyCheckByFilterPersistsCheckingAndWakesDispatcher(t *testing.T) {
	repo := &fakeProxyRepository{count: 2}
	queue := &fakeProxyCheckQueue{}
	uc := NewProxyUseCase(repo, nil, queue, nil, nil)

	result, err := uc.CheckByFilter(context.Background(), ProxyListFilter{
		Pool:    domain.ProxyPoolResource,
		Country: "US",
		Status:  domain.ProxyStatusDisabled,
	}, 1, "request-4", "/v1/admin/proxies/check")

	require.NoError(t, err)
	require.Equal(t, 2, result.Requested)
	require.Equal(t, 2, result.Queued)
	require.Empty(t, queue.tasks)
	require.Len(t, queue.dispatches, 1)
	require.Len(t, repo.pendingChecks, 2)
}

func TestProxyCheckBatchPersistsCheckingAndWakesDispatcher(t *testing.T) {
	repo := &fakeProxyRepository{}
	queue := &fakeProxyCheckQueue{}
	uc := NewProxyUseCase(repo, nil, queue, nil, nil)

	result, err := uc.CheckBatch(context.Background(), []uint{1, 2, 2, 3}, 1, "request-ids", "/v1/admin/proxies/check")

	require.NoError(t, err)
	require.Equal(t, 3, result.Requested)
	require.Equal(t, 3, result.Queued)
	require.Empty(t, result.Items)
	require.Empty(t, queue.tasks)
	require.Len(t, queue.dispatches, 1)
	require.Equal(t, []uint{1, 2, 3}, repo.pendingChecks)
}

func TestProxyCheckBatchDispatcherWakeFailureKeepsPersistedState(t *testing.T) {
	queueErr := errors.New("redis is unavailable")
	queue := &fakeProxyCheckQueue{dispatchErr: queueErr}
	systems := &fakeSystemLogPort{}
	repo := &fakeProxyRepository{}
	uc := NewProxyUseCase(repo, nil, queue, nil, systems)

	result, err := uc.CheckBatch(context.Background(), []uint{1, 2}, 1, "request-batch-failed", "/v1/admin/proxies/check")

	require.NoError(t, err)
	require.Equal(t, 2, result.Queued)
	require.Equal(t, []uint{1, 2}, repo.pendingChecks)
	require.Len(t, systems.logs, 1)
	require.Equal(t, "proxy.check_dispatcher_enqueue_failed", systems.logs[0].EventType)
	require.Equal(t, "dispatcher", systems.logs[0].BizID)
	require.Contains(t, systems.logs[0].Detail, "redis is unavailable")
}

func TestProxyCheckByFilterDispatcherWakeFailureKeepsPersistedState(t *testing.T) {
	queueErr := errors.New("redis is unavailable")
	repo := &fakeProxyRepository{count: 2}
	queue := &fakeProxyCheckQueue{dispatchErr: queueErr}
	systems := &fakeSystemLogPort{}
	uc := NewProxyUseCase(repo, nil, queue, nil, systems)

	result, err := uc.CheckByFilter(context.Background(), ProxyListFilter{Pool: domain.ProxyPoolResource}, 1, "request-filter-failed", "/v1/admin/proxies/check")

	require.NoError(t, err)
	require.Equal(t, 2, result.Queued)
	require.Len(t, repo.pendingChecks, 2)
	require.Len(t, systems.logs, 1)
	require.Equal(t, "proxy.check_dispatcher_enqueue_failed", systems.logs[0].EventType)
	require.Equal(t, "dispatcher", systems.logs[0].BizID)
	require.Contains(t, systems.logs[0].Detail, "redis is unavailable")
}

func TestProxyDispatcherActivatesOnlyAfterAcceptedEnqueue(t *testing.T) {
	queue := &fakeProxyCheckQueue{}
	repo := &fakeProxyRepository{
		findProxy:     &domain.Proxy{ID: 7, Status: domain.ProxyStatusPending, CheckGeneration: 4},
		pendingChecks: []uint{7},
		pendingTasks:  map[uint]ProxyCheckTask{7: {ProxyID: 7, CheckGeneration: 4}},
	}
	repo.enqueueObserved = func() bool { return len(queue.tasks) == 1 }
	uc := NewProxyUseCase(repo, nil, queue, nil, nil)

	result, err := uc.DispatchPendingProxyChecks(context.Background(), 10)

	require.NoError(t, err)
	require.Equal(t, 1, result.Attempted)
	require.Equal(t, 1, result.Queued)
	require.Equal(t, 0, result.Failed)
	require.Len(t, queue.tasks, 1)
	require.Equal(t, 1, repo.activateCalls)
	require.False(t, repo.activationBeforeEnqueue)
	require.Equal(t, domain.ProxyStatusChecking, repo.findProxy.Status)
}

func TestProxyDispatcherDuplicateAndFailureLeavePending(t *testing.T) {
	for _, tc := range []struct {
		name      string
		duplicate bool
		err       error
		failed    int
	}{
		{name: "duplicate", duplicate: true},
		{name: "redis failure", err: errors.New("redis unavailable"), failed: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			queue := &fakeProxyCheckQueue{duplicate: tc.duplicate, err: tc.err}
			repo := &fakeProxyRepository{
				findProxy:     &domain.Proxy{ID: 7, Status: domain.ProxyStatusPending, CheckGeneration: 4},
				pendingChecks: []uint{7},
				pendingTasks:  map[uint]ProxyCheckTask{7: {ProxyID: 7, CheckGeneration: 4}},
			}
			uc := NewProxyUseCase(repo, nil, queue, nil, nil)

			result, err := uc.DispatchPendingProxyChecks(context.Background(), 10)

			require.NoError(t, err)
			require.Equal(t, tc.failed, result.Failed)
			require.Zero(t, result.Queued)
			require.Zero(t, repo.activateCalls)
			require.Equal(t, domain.ProxyStatusPending, repo.findProxy.Status)
		})
	}
}

func TestProxyListWritesExpiredScanSystemLog(t *testing.T) {
	repo := &fakeProxyRepository{expiredUpdated: 2}
	systems := &fakeSystemLogPort{}
	uc := NewProxyUseCase(repo, nil, nil, nil, systems)

	result, err := uc.List(context.Background(), ProxyListFilter{}, 0, 20)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Len(t, systems.logs, 1)
	require.Equal(t, "proxy.expired_scan", systems.logs[0].EventType)
	require.Contains(t, systems.logs[0].Detail, "count=2")
}

func TestProxyRunCheckRetriesInternallyThenMarksAbnormal(t *testing.T) {
	now := time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)
	repo := &fakeProxyRepository{
		findProxy: &domain.Proxy{
			ID:              8,
			Pool:            domain.ProxyPoolResource,
			URL:             "http://127.0.0.1:18081",
			ExpireAt:        now.Add(time.Hour),
			Status:          domain.ProxyStatusChecking,
			Country:         "UNKNOWN",
			CheckGeneration: 1,
		},
	}
	checker := &fakeProxyChecker{
		result: domain.CheckResult{
			LastSafeError: "Proxy endpoint is unreachable.",
			CheckedAt:     now,
		},
		err: domain.ErrProxyCheckFailed,
	}
	uc := NewProxyUseCase(repo, checker, nil, nil, nil)
	uc.now = func() time.Time { return now }

	updated, err := uc.RunCheck(context.Background(), ProxyCheckTask{ProxyID: 8, CheckGeneration: 1}, false)

	require.NoError(t, err)
	require.Equal(t, 3, checker.calls)
	require.Equal(t, domain.ProxyStatusAbnormal, updated.Status)
	require.Equal(t, 3, updated.Errors)
	require.Equal(t, "Proxy endpoint is unreachable.", updated.LastSafeError)
}

func TestProxyRunCheckInfrastructureFailureDoesNotWriteBusinessFailure(t *testing.T) {
	now := time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)
	repo := &fakeProxyRepository{findProxy: &domain.Proxy{
		ID: 18, URL: "http://127.0.0.1:18088", Status: domain.ProxyStatusChecking, CheckGeneration: 3,
	}}
	checker := &fakeProxyChecker{err: context.Canceled}
	uc := NewProxyUseCase(repo, checker, nil, nil, nil)
	uc.now = func() time.Time { return now }

	updated, err := uc.RunCheck(context.Background(), ProxyCheckTask{ProxyID: 18, CheckGeneration: 3}, false)

	require.ErrorIs(t, err, context.Canceled)
	require.Nil(t, updated)
	require.Equal(t, 1, checker.calls)
	require.Zero(t, repo.checkResultUpdates)
	require.Equal(t, domain.ProxyStatusChecking, repo.findProxy.Status)
	require.Zero(t, repo.releaseCalls)
}

func TestProxyRunCheckFinalInfrastructureFailureReleasesPendingGeneration(t *testing.T) {
	now := time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name string
		err  error
	}{
		{name: "context canceled", err: context.Canceled},
		{name: "checker unavailable", err: errors.New("dialer unavailable")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &fakeProxyRepository{findProxy: &domain.Proxy{
				ID: 18, URL: "http://127.0.0.1:18088", Status: domain.ProxyStatusChecking, CheckGeneration: 3, Errors: 2,
			}}
			checker := &fakeProxyChecker{err: tc.err}
			queue := &fakeProxyCheckQueue{}
			uc := NewProxyUseCase(repo, checker, queue, nil, nil)
			uc.now = func() time.Time { return now }

			updated, err := uc.RunCheck(context.Background(), ProxyCheckTask{ProxyID: 18, CheckGeneration: 3}, true)

			require.NoError(t, err)
			require.Nil(t, updated)
			require.Equal(t, 1, checker.calls)
			require.Zero(t, repo.checkResultUpdates)
			require.Equal(t, 1, repo.releaseCalls)
			require.Equal(t, domain.ProxyStatusPending, repo.findProxy.Status)
			require.Equal(t, uint64(4), repo.findProxy.CheckGeneration)
			require.Equal(t, 2, repo.findProxy.Errors, "infrastructure failure must not consume a business attempt")
			require.Len(t, queue.dispatches, 1)
		})
	}
}

func TestProxyRunCheckFinalPersistenceFailureReleasesPendingGeneration(t *testing.T) {
	now := time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)
	repo := &fakeProxyRepository{
		findProxy: &domain.Proxy{
			ID: 21, URL: "http://127.0.0.1:18090", Status: domain.ProxyStatusChecking, CheckGeneration: 5, Errors: 1,
		},
		checkResultErr: errors.New("database unavailable"),
	}
	checker := &fakeProxyChecker{result: domain.CheckResult{CheckedAt: now}}
	queue := &fakeProxyCheckQueue{}
	uc := NewProxyUseCase(repo, checker, queue, nil, nil)
	uc.now = func() time.Time { return now }

	updated, err := uc.RunCheck(context.Background(), ProxyCheckTask{ProxyID: 21, CheckGeneration: 5}, true)

	require.NoError(t, err)
	require.Nil(t, updated)
	require.Equal(t, 1, repo.checkResultUpdates)
	require.Equal(t, 1, repo.releaseCalls)
	require.Equal(t, domain.ProxyStatusPending, repo.findProxy.Status)
	require.Equal(t, uint64(6), repo.findProxy.CheckGeneration)
	require.Equal(t, 1, repo.findProxy.Errors, "result persistence failure must not consume a business attempt")
	require.Len(t, queue.dispatches, 1)
}

func TestProxyRunCheckNonRetryableFailureIsImmediatelyAbnormal(t *testing.T) {
	now := time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)
	repo := &fakeProxyRepository{findProxy: &domain.Proxy{
		ID: 19, URL: "invalid", Status: domain.ProxyStatusChecking, CheckGeneration: 2,
	}}
	checker := &fakeProxyChecker{
		result: domain.CheckResult{NonRetryable: true, LastSafeError: "Invalid proxy URL.", CheckedAt: now},
		err:    domain.ErrInvalidProxyURL,
	}
	uc := NewProxyUseCase(repo, checker, nil, nil, nil)
	uc.now = func() time.Time { return now }

	updated, err := uc.RunCheck(context.Background(), ProxyCheckTask{ProxyID: 19, CheckGeneration: 2}, false)

	require.NoError(t, err)
	require.Equal(t, 1, checker.calls)
	require.Equal(t, domain.ProxyStatusAbnormal, updated.Status)
	require.Zero(t, updated.Errors)
}

func TestProxyRunCheckSkipsStaleGeneration(t *testing.T) {
	repo := &fakeProxyRepository{findProxy: &domain.Proxy{
		ID: 20, URL: "http://127.0.0.1:18089", Status: domain.ProxyStatusChecking, CheckGeneration: 9,
	}}
	checker := &fakeProxyChecker{}
	uc := NewProxyUseCase(repo, checker, nil, nil, nil)

	updated, err := uc.RunCheck(context.Background(), ProxyCheckTask{ProxyID: 20, CheckGeneration: 8}, false)

	require.NoError(t, err)
	require.Equal(t, uint64(9), updated.CheckGeneration)
	require.Zero(t, checker.calls)
	require.Zero(t, repo.checkResultUpdates)
}

func TestProxyRunCheckSkipsWhenStatusChanged(t *testing.T) {
	now := time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)
	repo := &fakeProxyRepository{
		findProxy: &domain.Proxy{
			ID:              11,
			Pool:            domain.ProxyPoolResource,
			URL:             "http://127.0.0.1:18085",
			ExpireAt:        now.Add(time.Hour),
			Status:          domain.ProxyStatusDisabled,
			Country:         "US",
			CheckGeneration: 1,
		},
	}
	checker := &fakeProxyChecker{
		result: domain.CheckResult{
			IPVersion:  domain.ProxyIPv4,
			OutboundIP: "198.51.100.9",
			Country:    "SG",
			LatencyMs:  88,
			CheckedAt:  now,
		},
	}
	systems := &fakeSystemLogPort{}
	uc := NewProxyUseCase(repo, checker, nil, nil, systems)
	uc.now = func() time.Time { return now }

	updated, err := uc.RunCheck(context.Background(), ProxyCheckTask{ProxyID: 11, CheckGeneration: 1}, false)

	require.NoError(t, err)
	require.Equal(t, domain.ProxyStatusDisabled, updated.Status)
	require.Equal(t, 0, checker.calls)
	require.Nil(t, repo.updatedProxy)
	require.Len(t, systems.logs, 1)
	require.Equal(t, "proxy.check_task_skipped", systems.logs[0].EventType)
}

func TestProxyAcquireUsesDirectFallbackAfterAttemptBudget(t *testing.T) {
	repo := &fakeProxyRepository{
		resourceProxy: &domain.Proxy{ID: 1, Pool: domain.ProxyPoolResource, URL: "http://resource.example:8080"},
		systemProxy:   &domain.Proxy{ID: 2, Pool: domain.ProxyPoolSystem, URL: "http://system.example:8080"},
	}
	uc := NewProxyUseCase(repo, nil, nil, nil, nil)

	config, err := uc.Acquire(context.Background(), AcquireProxyRequest{
		Key:                 "user@example.com",
		AllowSystemFallback: true,
		Attempt:             3,
	})

	require.NoError(t, err)
	require.True(t, config.Direct)
	require.Equal(t, 0, repo.resourceAcquireCount)
	require.Equal(t, 0, repo.systemAcquireCount)
}

func TestProxyAcquireRetriesSystemBeforeDirect(t *testing.T) {
	repo := &fakeProxyRepository{
		resourceProxy: &domain.Proxy{ID: 1, Pool: domain.ProxyPoolResource, URL: "http://resource.example:8080"},
		systemProxy:   &domain.Proxy{ID: 2, Pool: domain.ProxyPoolSystem, URL: "http://system.example:8080"},
	}
	uc := NewProxyUseCase(repo, nil, nil, nil, nil)

	config, err := uc.Acquire(context.Background(), AcquireProxyRequest{
		Key:                 "user@example.com",
		AllowSystemFallback: true,
		Attempt:             1,
	})

	require.NoError(t, err)
	require.False(t, config.Direct)
	require.Equal(t, uint(2), config.ID)
	require.Equal(t, 0, repo.resourceAcquireCount)
	require.Equal(t, 1, repo.systemAcquireCount)
}

func TestProxyAcquireFallsBackToDirectWhenPoolsUnavailable(t *testing.T) {
	repo := &fakeProxyRepository{
		resourceErr: domain.ErrProxyUnavailable,
		systemErr:   domain.ErrProxyUnavailable,
	}
	uc := NewProxyUseCase(repo, nil, nil, nil, nil)

	config, err := uc.Acquire(context.Background(), AcquireProxyRequest{
		Key:                 "user@example.com",
		AllowSystemFallback: true,
	})

	require.NoError(t, err)
	require.True(t, config.Direct)
	require.Equal(t, 1, repo.resourceAcquireCount)
	require.Equal(t, 1, repo.systemAcquireCount)
}

func TestProxyReportsIgnoreDirectRoute(t *testing.T) {
	repo := &fakeProxyRepository{}
	uc := NewProxyUseCase(repo, nil, nil, nil, nil)

	require.NoError(t, uc.ReportSuccess(context.Background(), 0))
	require.NoError(t, uc.ReportFailure(context.Background(), 0, "direct request failed"))
	require.NoError(t, uc.ReportNonRetryableFailure(context.Background(), 0, "direct request failed"))
	require.Equal(t, 0, repo.reportSuccessCount)
	require.Equal(t, 0, repo.reportFailureCount)
}

func TestProxyRetryableFailuresQueueOneCheckAtThreshold(t *testing.T) {
	repo := &fakeProxyRepository{
		failureProxy: &domain.Proxy{ID: 17, Status: domain.ProxyStatusNormal},
	}
	queue := &fakeProxyCheckQueue{}
	uc := NewProxyUseCase(repo, nil, queue, nil, nil)

	for attempt := 1; attempt <= 2; attempt++ {
		require.NoError(t, uc.ReportFailure(context.Background(), 17, "network timeout"))
		require.Equal(t, domain.ProxyStatusNormal, repo.failureProxy.Status)
		require.Equal(t, attempt, repo.failureProxy.Errors)
		require.Empty(t, queue.tasks)
	}

	require.NoError(t, uc.ReportFailure(context.Background(), 17, "network timeout"))
	require.Equal(t, domain.ProxyStatusPending, repo.failureProxy.Status)
	require.Equal(t, 0, repo.failureProxy.Errors)
	require.Equal(t, uint64(1), repo.failureProxy.CheckGeneration)
	require.Empty(t, queue.tasks)
	require.Len(t, queue.dispatches, 1)
}
