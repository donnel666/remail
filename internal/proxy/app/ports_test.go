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
	listIDs       []uint
	nextID        uint
	nextJobID     uint
	checkJobs     []ProxyCheckJob

	resourceAcquireCount int
	systemAcquireCount   int
	reportSuccessCount   int
	reportFailureCount   int
	checkResultUpdates   int
	listIDCalls          int
	expiredUpdated       int64
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
func (r *fakeProxyRepository) CreateWithLogAndCheckJob(ctx context.Context, proxy *domain.Proxy, log *governancedomain.OperationLog, task ProxyCheckTask) (*ProxyCheckJob, error) {
	if err := r.CreateWithLog(ctx, proxy, log); err != nil {
		return nil, err
	}
	task.ProxyID = proxy.ID
	return r.createCheckJob(ProxyCheckJobSingle, task, ProxyCheckBatchTask{}), nil
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
	}
	return created, 0, nil
}
func (r *fakeProxyRepository) CreateBatchWithLogAndCheckJob(ctx context.Context, proxies []*domain.Proxy, log *governancedomain.OperationLog, task ProxyCheckBatchTask) ([]domain.Proxy, int, *ProxyCheckJob, error) {
	created, duplicated, err := r.CreateBatchWithLog(ctx, proxies, log)
	if err != nil || len(created) == 0 {
		return created, duplicated, nil, err
	}
	task.Mode = ProxyCheckBatchModeIDs
	task.ProxyIDs = proxyIDs(created)
	job := r.createCheckJob(ProxyCheckJobBatch, ProxyCheckTask{}, task)
	return created, duplicated, job, nil
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
func (r *fakeProxyRepository) ListIDs(_ context.Context, _ ProxyListFilter, afterID uint, limit int) ([]uint, error) {
	r.listIDCalls++
	ids := make([]uint, 0, limit)
	for _, id := range r.listIDs {
		if id <= afterID {
			continue
		}
		ids = append(ids, id)
		if len(ids) == limit {
			break
		}
	}
	return ids, nil
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
func (r *fakeProxyRepository) UpdateWithLogAndCheckJob(ctx context.Context, proxy *domain.Proxy, log *governancedomain.OperationLog, task ProxyCheckTask) (*ProxyCheckJob, error) {
	if err := r.UpdateWithLog(ctx, proxy, log); err != nil {
		return nil, err
	}
	task.ProxyID = proxy.ID
	return r.createCheckJob(ProxyCheckJobSingle, task, ProxyCheckBatchTask{}), nil
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
func (r *fakeProxyRepository) MarkExpiredBefore(context.Context, time.Time) (int64, error) {
	return r.expiredUpdated, nil
}
func (r *fakeProxyRepository) UpdateCheckResult(_ context.Context, _ uint, result domain.CheckResult, success bool) (*domain.Proxy, error) {
	r.checkResultUpdates++
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
func (r *fakeProxyRepository) UpdateCheckResultWithLog(ctx context.Context, id uint, result domain.CheckResult, success bool, _ *governancedomain.OperationLog) (*domain.Proxy, error) {
	return r.UpdateCheckResult(ctx, id, result, success)
}
func (r *fakeProxyRepository) CreateCheckBatchJobWithLog(_ context.Context, task ProxyCheckBatchTask, _ *governancedomain.OperationLog) (*ProxyCheckJob, error) {
	return r.createCheckJob(ProxyCheckJobBatch, ProxyCheckTask{}, task), nil
}
func (r *fakeProxyRepository) ListPendingProxyCheckJobs(_ context.Context, limit int) ([]ProxyCheckJob, error) {
	if limit <= 0 {
		limit = len(r.checkJobs)
	}
	jobs := make([]ProxyCheckJob, 0, len(r.checkJobs))
	for _, job := range r.checkJobs {
		if job.Status != ProxyCheckJobPending {
			continue
		}
		jobs = append(jobs, job)
		if len(jobs) == limit {
			break
		}
	}
	return jobs, nil
}
func (r *fakeProxyRepository) ListProxyCheckJobItemIDs(_ context.Context, jobID uint, afterProxyID uint, limit int) ([]uint, error) {
	if limit <= 0 {
		limit = batchCheckIDPageSize
	}
	for _, job := range r.checkJobs {
		if job.ID != jobID {
			continue
		}
		ids := make([]uint, 0, limit)
		for _, proxyID := range job.ProxyIDs {
			if proxyID <= afterProxyID {
				continue
			}
			ids = append(ids, proxyID)
			if len(ids) == limit {
				break
			}
		}
		return ids, nil
	}
	return nil, nil
}
func (r *fakeProxyRepository) MarkProxyCheckJobQueued(_ context.Context, jobID uint) error {
	r.updateCheckJobStatus(jobID, ProxyCheckJobQueued, "")
	return nil
}
func (r *fakeProxyRepository) MarkProxyCheckJobDispatchFailed(_ context.Context, jobID uint, safeError string) error {
	r.updateCheckJobStatus(jobID, ProxyCheckJobPending, safeError)
	return nil
}
func (r *fakeProxyRepository) MarkProxyCheckJobRunning(_ context.Context, jobID uint) error {
	r.updateCheckJobStatus(jobID, ProxyCheckJobRunning, "")
	return nil
}
func (r *fakeProxyRepository) MarkProxyCheckJobSucceeded(_ context.Context, jobID uint) error {
	r.updateCheckJobStatus(jobID, ProxyCheckJobSucceeded, "")
	return nil
}
func (r *fakeProxyRepository) MarkProxyCheckJobFailed(_ context.Context, jobID uint, safeError string) error {
	r.updateCheckJobStatus(jobID, ProxyCheckJobFailed, safeError)
	return nil
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
func (r *fakeProxyRepository) ReportFailure(context.Context, uint, string, bool) (*domain.Proxy, error) {
	r.reportFailureCount++
	return &domain.Proxy{Status: domain.ProxyStatusNormal}, nil
}

func (r *fakeProxyRepository) createCheckJob(kind ProxyCheckJobKind, task ProxyCheckTask, batchTask ProxyCheckBatchTask) *ProxyCheckJob {
	if r.nextJobID == 0 {
		r.nextJobID = 1
	}
	job := ProxyCheckJob{
		ID:             r.nextJobID,
		Kind:           kind,
		Status:         ProxyCheckJobPending,
		ProxyID:        task.ProxyID,
		Mode:           batchTask.Mode,
		ProxyIDs:       batchTask.ProxyIDs,
		Filter:         batchTask.Filter,
		OperatorUserID: task.OperatorUserID,
		RequestID:      task.RequestID,
		Path:           task.Path,
	}
	if kind == ProxyCheckJobBatch {
		if job.Mode == "" {
			if len(batchTask.ProxyIDs) > 0 {
				job.Mode = ProxyCheckBatchModeIDs
			} else {
				job.Mode = ProxyCheckBatchModeFilter
			}
		}
		job.OperatorUserID = batchTask.OperatorUserID
		job.RequestID = batchTask.RequestID
		job.Path = batchTask.Path
	}
	r.nextJobID++
	r.checkJobs = append(r.checkJobs, job)
	return &r.checkJobs[len(r.checkJobs)-1]
}

func (r *fakeProxyRepository) updateCheckJobStatus(jobID uint, status ProxyCheckJobStatus, safeError string) {
	for i := range r.checkJobs {
		if r.checkJobs[i].ID == jobID {
			r.checkJobs[i].Status = status
			r.checkJobs[i].LastSafeError = safeError
			return
		}
	}
}

type fakeProxyCheckQueue struct {
	tasks       []ProxyCheckTask
	batchTasks  []ProxyCheckBatchTask
	dispatches  []time.Duration
	err         error
	batchErr    error
	dispatchErr error
}

func (q *fakeProxyCheckQueue) EnqueueProxyCheck(_ context.Context, task ProxyCheckTask) error {
	if q.err != nil {
		return q.err
	}
	q.tasks = append(q.tasks, task)
	return nil
}

func (q *fakeProxyCheckQueue) EnqueueProxyCheckBatch(_ context.Context, task ProxyCheckBatchTask) error {
	if q.batchErr != nil {
		return q.batchErr
	}
	q.batchTasks = append(q.batchTasks, task)
	return nil
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

func TestProxyCreateQueuesCheckTask(t *testing.T) {
	queue := &fakeProxyCheckQueue{}
	uc := NewProxyUseCase(&fakeProxyRepository{}, nil, queue, nil, nil)

	proxy, err := uc.Create(context.Background(), 1, "request-create", "/v1/admin/proxies/resource", CreateProxyRequest{
		Pool: domain.ProxyPoolResource,
		URL:  "http://127.0.0.1:18080",
	})

	require.NoError(t, err)
	require.Equal(t, uint(1), proxy.ID)
	require.Equal(t, domain.ProxyStatusChecking, proxy.Status)
	require.Len(t, queue.tasks, 1)
	require.Equal(t, uint(1), queue.tasks[0].ProxyID)
	require.Empty(t, queue.batchTasks)
}

func TestProxyImportQueuesBatchCheckTask(t *testing.T) {
	queue := &fakeProxyCheckQueue{}
	uc := NewProxyUseCase(&fakeProxyRepository{}, nil, queue, nil, nil)

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
	require.Len(t, queue.batchTasks, 1)
	require.Equal(t, ProxyCheckBatchModeIDs, queue.batchTasks[0].Mode)
	require.NotZero(t, queue.batchTasks[0].JobID)
	require.Empty(t, queue.batchTasks[0].ProxyIDs)
}

func TestProxyCheckQueuesTaskAndMarksChecking(t *testing.T) {
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
	require.Equal(t, domain.ProxyStatusChecking, proxy.Status)
	require.NotNil(t, repo.updatedProxy)
	require.Equal(t, domain.ProxyStatusChecking, repo.updatedProxy.Status)
	require.Len(t, queue.tasks, 1)
	require.Equal(t, uint(7), queue.tasks[0].ProxyID)
	require.Equal(t, uint(1), queue.tasks[0].OperatorUserID)
	require.Equal(t, "request-1", queue.tasks[0].RequestID)
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
	queue := &fakeProxyCheckQueue{err: queueErr}
	systems := &fakeSystemLogPort{}
	uc := NewProxyUseCase(repo, nil, queue, nil, systems)
	uc.now = func() time.Time { return now }

	proxy, err := uc.Check(context.Background(), 7, 1, "request-queue-failed", "/v1/admin/proxies/7/check")

	require.NoError(t, err)
	require.NotNil(t, proxy)
	require.Equal(t, domain.ProxyStatusChecking, proxy.Status)
	require.NotNil(t, repo.updatedProxy)
	require.Equal(t, domain.ProxyStatusChecking, repo.updatedProxy.Status)
	require.Equal(t, 1, repo.updatedProxy.Errors)
	require.Equal(t, 0, repo.checkResultUpdates)
	require.Len(t, systems.logs, 1)
	require.Equal(t, "proxy.check_queue_failed", systems.logs[0].EventType)
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
	require.Equal(t, domain.ProxyStatusChecking, proxy.Status)
	require.NotNil(t, repo.updatedProxy)
	require.Equal(t, domain.ProxyStatusChecking, repo.updatedProxy.Status)
	require.Len(t, queue.tasks, 1)
	require.Equal(t, uint(9), queue.tasks[0].ProxyID)
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
	require.Equal(t, domain.ProxyStatusChecking, proxy.Status)
	require.NotNil(t, repo.updatedProxy)
	require.Equal(t, domain.ProxyStatusChecking, repo.updatedProxy.Status)
	require.Len(t, queue.tasks, 1)
	require.Equal(t, uint(10), queue.tasks[0].ProxyID)
}

func TestProxyCheckByFilterQueuesSingleBatchTask(t *testing.T) {
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
	require.Equal(t, 0, repo.listIDCalls)
	require.Empty(t, queue.tasks)
	require.Len(t, queue.batchTasks, 1)
	require.Equal(t, ProxyCheckBatchModeFilter, queue.batchTasks[0].Mode)
	require.Equal(t, domain.ProxyPoolResource, queue.batchTasks[0].Filter.Pool)
	require.Equal(t, "US", queue.batchTasks[0].Filter.Country)
	require.Equal(t, domain.ProxyStatusDisabled, queue.batchTasks[0].Filter.Status)
}

func TestProxyCheckBatchQueuesSingleBatchTask(t *testing.T) {
	queue := &fakeProxyCheckQueue{}
	uc := NewProxyUseCase(&fakeProxyRepository{}, nil, queue, nil, nil)

	result, err := uc.CheckBatch(context.Background(), []uint{1, 2, 2, 3}, 1, "request-ids", "/v1/admin/proxies/check")

	require.NoError(t, err)
	require.Equal(t, 3, result.Requested)
	require.Equal(t, 3, result.Queued)
	require.Empty(t, result.Items)
	require.Empty(t, queue.tasks)
	require.Len(t, queue.batchTasks, 1)
	require.Equal(t, ProxyCheckBatchModeIDs, queue.batchTasks[0].Mode)
	require.NotZero(t, queue.batchTasks[0].JobID)
	require.Empty(t, queue.batchTasks[0].ProxyIDs)
}

func TestProxyCheckBatchQueueFailureWritesSystemLog(t *testing.T) {
	queueErr := errors.New("redis is unavailable")
	queue := &fakeProxyCheckQueue{batchErr: queueErr}
	systems := &fakeSystemLogPort{}
	repo := &fakeProxyRepository{}
	uc := NewProxyUseCase(repo, nil, queue, nil, systems)

	result, err := uc.CheckBatch(context.Background(), []uint{1, 2}, 1, "request-batch-failed", "/v1/admin/proxies/check")

	require.NoError(t, err)
	require.Equal(t, 2, result.Queued)
	require.Len(t, repo.checkJobs, 1)
	require.Equal(t, ProxyCheckJobPending, repo.checkJobs[0].Status)
	require.Contains(t, repo.checkJobs[0].LastSafeError, "redis is unavailable")
	require.Len(t, systems.logs, 1)
	require.Equal(t, "proxy.check_batch_queue_failed", systems.logs[0].EventType)
	require.Equal(t, "batch", systems.logs[0].BizID)
	require.Contains(t, systems.logs[0].Detail, "redis is unavailable")
}

func TestProxyCheckByFilterQueueFailureWritesSystemLog(t *testing.T) {
	queueErr := errors.New("redis is unavailable")
	repo := &fakeProxyRepository{count: 2}
	queue := &fakeProxyCheckQueue{batchErr: queueErr}
	systems := &fakeSystemLogPort{}
	uc := NewProxyUseCase(repo, nil, queue, nil, systems)

	result, err := uc.CheckByFilter(context.Background(), ProxyListFilter{Pool: domain.ProxyPoolResource}, 1, "request-filter-failed", "/v1/admin/proxies/check")

	require.NoError(t, err)
	require.Equal(t, 2, result.Queued)
	require.Len(t, repo.checkJobs, 1)
	require.Equal(t, ProxyCheckJobPending, repo.checkJobs[0].Status)
	require.Contains(t, repo.checkJobs[0].LastSafeError, "redis is unavailable")
	require.Len(t, systems.logs, 1)
	require.Equal(t, "proxy.check_batch_queue_failed", systems.logs[0].EventType)
	require.Equal(t, "filter", systems.logs[0].BizID)
	require.Contains(t, systems.logs[0].Detail, "redis is unavailable")
}

func TestProxyRunCheckByFilterPagesAndQueuesChecks(t *testing.T) {
	now := time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)
	listIDs := make([]uint, batchCheckIDPageSize+1)
	for i := range listIDs {
		listIDs[i] = uint(i + 1)
	}
	repo := &fakeProxyRepository{
		findProxy: &domain.Proxy{
			ID:       1,
			Pool:     domain.ProxyPoolResource,
			URL:      "http://127.0.0.1:18084",
			ExpireAt: now.Add(time.Hour),
			Status:   domain.ProxyStatusNormal,
			Country:  "US",
		},
		listIDs: listIDs,
	}
	queue := &fakeProxyCheckQueue{}
	uc := NewProxyUseCase(repo, nil, queue, nil, nil)
	uc.now = func() time.Time { return now }

	result, err := uc.RunCheckByFilter(context.Background(), ProxyCheckBatchTask{
		Mode:           ProxyCheckBatchModeFilter,
		Filter:         ProxyListFilter{Pool: domain.ProxyPoolResource, Country: "US"},
		OperatorUserID: 1,
		RequestID:      "request-5",
		Path:           "/v1/admin/proxies/check",
	})

	require.NoError(t, err)
	require.Equal(t, batchCheckIDPageSize+1, result.Requested)
	require.Equal(t, batchCheckIDPageSize+1, result.Queued)
	require.Equal(t, 2, repo.listIDCalls)
	require.Len(t, queue.tasks, batchCheckIDPageSize+1)
	require.Len(t, queue.batchTasks, 0)
	require.Equal(t, uint(1), queue.tasks[0].ProxyID)
	require.Equal(t, uint(batchCheckIDPageSize+1), queue.tasks[len(queue.tasks)-1].ProxyID)
}

func TestProxyRunCheckBatchIDsPagesDurableJobItems(t *testing.T) {
	now := time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)
	repo := &fakeProxyRepository{
		findProxy: &domain.Proxy{
			ID:       1,
			Pool:     domain.ProxyPoolSystem,
			URL:      "http://127.0.0.1:18086",
			ExpireAt: now.Add(time.Hour),
			Status:   domain.ProxyStatusNormal,
			Country:  "US",
		},
	}
	job := repo.createCheckJob(ProxyCheckJobBatch, ProxyCheckTask{}, ProxyCheckBatchTask{
		Mode:     ProxyCheckBatchModeIDs,
		ProxyIDs: []uint{1, 2, 3},
	})
	queue := &fakeProxyCheckQueue{}
	uc := NewProxyUseCase(repo, nil, queue, nil, nil)
	uc.now = func() time.Time { return now }

	result, err := uc.RunCheckBatch(context.Background(), ProxyCheckBatchTask{
		JobID:          job.ID,
		Mode:           ProxyCheckBatchModeIDs,
		OperatorUserID: 1,
		RequestID:      "request-job-items",
		Path:           "/v1/admin/proxies/check",
	})

	require.NoError(t, err)
	require.Equal(t, 3, result.Requested)
	require.Equal(t, 3, result.Queued)
	require.Len(t, queue.tasks, 3)
	require.Equal(t, uint(1), queue.tasks[0].ProxyID)
	require.Equal(t, uint(3), queue.tasks[2].ProxyID)
	require.Equal(t, ProxyCheckJobSucceeded, repo.checkJobs[0].Status)
}

func TestProxyDispatchPendingProxyCheckJobsQueuesDurableJobs(t *testing.T) {
	repo := &fakeProxyRepository{}
	singleJob := repo.createCheckJob(ProxyCheckJobSingle, ProxyCheckTask{
		ProxyID:        7,
		OperatorUserID: 1,
		RequestID:      "request-single",
		Path:           "/v1/admin/proxies/7/check",
	}, ProxyCheckBatchTask{})
	batchJob := repo.createCheckJob(ProxyCheckJobBatch, ProxyCheckTask{}, ProxyCheckBatchTask{
		Mode:           ProxyCheckBatchModeIDs,
		ProxyIDs:       []uint{8, 9},
		OperatorUserID: 1,
		RequestID:      "request-batch",
		Path:           "/v1/admin/proxies/check",
	})
	queue := &fakeProxyCheckQueue{}
	uc := NewProxyUseCase(repo, nil, queue, nil, nil)

	result, err := uc.DispatchPendingProxyCheckJobs(context.Background(), 10)

	require.NoError(t, err)
	require.Equal(t, 2, result.Attempted)
	require.Equal(t, 2, result.Queued)
	require.Len(t, queue.tasks, 1)
	require.Equal(t, singleJob.ID, queue.tasks[0].JobID)
	require.Len(t, queue.batchTasks, 1)
	require.Equal(t, batchJob.ID, queue.batchTasks[0].JobID)
	require.Equal(t, ProxyCheckBatchModeIDs, queue.batchTasks[0].Mode)
	require.Empty(t, queue.batchTasks[0].ProxyIDs)
	require.Equal(t, ProxyCheckJobQueued, repo.checkJobs[0].Status)
	require.Equal(t, ProxyCheckJobQueued, repo.checkJobs[1].Status)
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
			ID:       8,
			Pool:     domain.ProxyPoolResource,
			URL:      "http://127.0.0.1:18081",
			ExpireAt: now.Add(time.Hour),
			Status:   domain.ProxyStatusChecking,
			Country:  "UNKNOWN",
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

	updated, err := uc.RunCheck(context.Background(), ProxyCheckTask{ProxyID: 8})

	require.NoError(t, err)
	require.Equal(t, 3, checker.calls)
	require.Equal(t, domain.ProxyStatusAbnormal, updated.Status)
	require.Equal(t, 1, updated.Errors)
	require.Equal(t, "Proxy endpoint is unreachable.", updated.LastSafeError)
}

func TestProxyRunCheckSkipsWhenStatusChanged(t *testing.T) {
	now := time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)
	repo := &fakeProxyRepository{
		findProxy: &domain.Proxy{
			ID:       11,
			Pool:     domain.ProxyPoolResource,
			URL:      "http://127.0.0.1:18085",
			ExpireAt: now.Add(time.Hour),
			Status:   domain.ProxyStatusDisabled,
			Country:  "US",
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

	updated, err := uc.RunCheck(context.Background(), ProxyCheckTask{ProxyID: 11, RequestID: "request-stale"})

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
