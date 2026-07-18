package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/donnel666/remail/api/middleware"
	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	iamdomain "github.com/donnel666/remail/internal/iam/domain"
	proxyapp "github.com/donnel666/remail/internal/proxy/app"
	"github.com/donnel666/remail/internal/proxy/domain"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type fakeProxyRepo struct {
	bindings      []domain.Binding
	bindingFilter proxyapp.ProxyBindingListFilter
	deleteFilter  proxyapp.ProxyListFilter
	disableFilter proxyapp.ProxyListFilter
	listIDFilter  proxyapp.ProxyListFilter
	listItems     []domain.Proxy
	deletedByIDs  []uint
	created       []domain.Proxy
	findProxy     *domain.Proxy
	count         int64
	pendingChecks []uint
}

func (r *fakeProxyRepo) Create(_ context.Context, proxy *domain.Proxy) error {
	proxy.ID = uint(len(r.created) + 1)
	r.created = append(r.created, *proxy)
	return nil
}

func (r *fakeProxyRepo) CreateWithLog(ctx context.Context, proxy *domain.Proxy, _ *governancedomain.OperationLog) error {
	return r.Create(ctx, proxy)
}

func (r *fakeProxyRepo) CreateBatchWithLog(_ context.Context, proxies []*domain.Proxy, _ *governancedomain.OperationLog) ([]domain.Proxy, int, error) {
	created := make([]domain.Proxy, 0, len(proxies))
	for _, proxy := range proxies {
		if proxy == nil {
			continue
		}
		proxy.ID = uint(len(r.created) + 1)
		r.created = append(r.created, *proxy)
		created = append(created, *proxy)
		r.pendingChecks = append(r.pendingChecks, proxy.ID)
	}
	return created, 0, nil
}

func (r *fakeProxyRepo) FindByID(_ context.Context, id uint) (*domain.Proxy, error) {
	if r.findProxy != nil {
		proxy := *r.findProxy
		return &proxy, nil
	}
	now := time.Now().UTC()
	return &domain.Proxy{
		ID:        id,
		Pool:      domain.ProxyPoolResource,
		URL:       "http://127.0.0.1:18080",
		ExpireAt:  now.Add(time.Hour),
		Status:    domain.ProxyStatusChecking,
		Country:   "UNKNOWN",
		CreatedAt: now,
		UpdatedAt: now,
	}, nil
}

func (r *fakeProxyRepo) List(_ context.Context, _ proxyapp.ProxyListFilter, _, _ int) ([]domain.Proxy, error) {
	return r.listItems, nil
}

func (r *fakeProxyRepo) Count(_ context.Context, _ proxyapp.ProxyListFilter) (int64, error) {
	return r.count, nil
}

func (r *fakeProxyRepo) CountDisableCandidates(_ context.Context, _ proxyapp.ProxyListFilter) (int64, error) {
	return r.count, nil
}

func (r *fakeProxyRepo) Stats(_ context.Context, _ proxyapp.ProxyListFilter) (*proxyapp.ProxyStats, error) {
	return &proxyapp.ProxyStats{}, nil
}

func (r *fakeProxyRepo) ListBindings(_ context.Context, filter proxyapp.ProxyBindingListFilter, _, _ int) ([]domain.Binding, error) {
	r.bindingFilter = filter
	return r.bindings, nil
}

func (r *fakeProxyRepo) CountBindings(_ context.Context, _ proxyapp.ProxyBindingListFilter) (int64, error) {
	return int64(len(r.bindings)), nil
}

func (r *fakeProxyRepo) Update(_ context.Context, _ *domain.Proxy) error {
	return nil
}

func (r *fakeProxyRepo) UpdateWithLog(ctx context.Context, proxy *domain.Proxy, _ *governancedomain.OperationLog) error {
	return r.Update(ctx, proxy)
}

func (r *fakeProxyRepo) UpdateWithLogAndBumpCheckGeneration(ctx context.Context, proxy *domain.Proxy, log *governancedomain.OperationLog) error {
	proxy.CheckGeneration++
	return r.UpdateWithLog(ctx, proxy, log)
}

func (r *fakeProxyRepo) DeleteBatch(_ context.Context, ids []uint) ([]uint, error) {
	r.deletedByIDs = ids
	return ids, nil
}

func (r *fakeProxyRepo) DeleteBatchWithLog(ctx context.Context, ids []uint, _ *governancedomain.OperationLog) ([]uint, error) {
	return r.DeleteBatch(ctx, ids)
}

func (r *fakeProxyRepo) DeleteByFilter(_ context.Context, filter proxyapp.ProxyListFilter) (int64, error) {
	r.deleteFilter = filter
	return r.count, nil
}

func (r *fakeProxyRepo) DeleteByFilterWithLog(ctx context.Context, filter proxyapp.ProxyListFilter, _ *governancedomain.OperationLog) (int64, error) {
	return r.DeleteByFilter(ctx, filter)
}

func (r *fakeProxyRepo) DisableByFilterWithLog(_ context.Context, filter proxyapp.ProxyListFilter, _ *governancedomain.OperationLog) (int64, error) {
	r.disableFilter = filter
	return r.count, nil
}

func (r *fakeProxyRepo) MarkPendingBatchWithLog(_ context.Context, ids []uint, _ *governancedomain.OperationLog) (int, int, error) {
	r.pendingChecks = append(r.pendingChecks, ids...)
	return len(ids), len(ids), nil
}

func (r *fakeProxyRepo) MarkPendingByFilterWithLog(_ context.Context, filter proxyapp.ProxyListFilter, _ *governancedomain.OperationLog) (int64, int64, error) {
	r.listIDFilter = filter
	for id := uint(1); id <= uint(r.count); id++ {
		r.pendingChecks = append(r.pendingChecks, id)
	}
	return r.count, r.count, nil
}

func (r *fakeProxyRepo) MarkExpiredBefore(_ context.Context, _ time.Time) (int64, error) {
	return 0, nil
}
func (r *fakeProxyRepo) ListPendingProxyChecks(_ context.Context, limit int) ([]proxyapp.ProxyCheckTask, error) {
	if limit <= 0 {
		limit = len(r.pendingChecks)
	}
	tasks := make([]proxyapp.ProxyCheckTask, 0, limit)
	for _, id := range r.pendingChecks {
		tasks = append(tasks, proxyapp.ProxyCheckTask{ProxyID: id, CheckGeneration: 1})
		if len(tasks) == limit {
			break
		}
	}
	return tasks, nil
}
func (r *fakeProxyRepo) ActivateProxyCheck(_ context.Context, id uint, generation uint64) (bool, error) {
	return id != 0 && generation != 0, nil
}
func (r *fakeProxyRepo) ReleaseProxyCheckInfrastructureFailure(context.Context, uint, uint64, string) (bool, error) {
	return false, nil
}

func (r *fakeProxyRepo) UpdateCheckResult(_ context.Context, id uint, _ domain.CheckResult, success bool) (*domain.Proxy, error) {
	now := time.Now().UTC()
	status := domain.ProxyStatusDisabled
	if success {
		status = domain.ProxyStatusNormal
	}
	return &domain.Proxy{
		ID:        id,
		Pool:      domain.ProxyPoolResource,
		URL:       "http://127.0.0.1:18080",
		ExpireAt:  now.Add(time.Hour),
		Status:    status,
		Country:   "UNKNOWN",
		CreatedAt: now,
		UpdatedAt: now,
	}, nil
}

func (r *fakeProxyRepo) UpdateCheckResultForGenerationWithLog(ctx context.Context, id uint, _ uint64, result domain.CheckResult, success bool, _ *governancedomain.OperationLog) (*domain.Proxy, error) {
	return r.UpdateCheckResult(ctx, id, result, success)
}

func (r *fakeProxyRepo) AcquireResourceProxy(_ context.Context, _ string, _ domain.ProxyIPVersion, _ time.Time, _ time.Duration) (*domain.Proxy, error) {
	return nil, nil
}

func (r *fakeProxyRepo) AcquireSystemProxy(_ context.Context, _ domain.ProxyIPVersion, _ time.Time) (*domain.Proxy, error) {
	return nil, nil
}

func (r *fakeProxyRepo) ReportSuccess(_ context.Context, _ uint, _ time.Time) error {
	return nil
}

func (r *fakeProxyRepo) ReportFailure(_ context.Context, _ uint, _ string, _ bool) (*domain.Proxy, error) {
	return nil, nil
}

type fakeProxyChecker struct{}

func (fakeProxyChecker) Check(_ context.Context, _ string) (domain.CheckResult, error) {
	return domain.CheckResult{}, nil
}

type fakeProxyCheckQueue struct {
	tasks      []proxyapp.ProxyCheckTask
	dispatches []time.Duration
}

func (q *fakeProxyCheckQueue) EnqueueProxyCheck(_ context.Context, task proxyapp.ProxyCheckTask) (bool, error) {
	q.tasks = append(q.tasks, task)
	return true, nil
}

func (q *fakeProxyCheckQueue) EnqueueProxyCheckDispatcher(_ context.Context, delay time.Duration) error {
	q.dispatches = append(q.dispatches, delay)
	return nil
}

type fakeOperationLogPort struct{}

func (fakeOperationLogPort) Create(_ context.Context, _ *governancedomain.OperationLog) error {
	return nil
}

type fakeSystemLogPort struct{}

func (fakeSystemLogPort) Create(_ context.Context, _ *governancedomain.SystemLog) error {
	return nil
}

func newTestProxyHandler(repo *fakeProxyRepo) *ProxyHandler {
	return newTestProxyHandlerWithQueue(repo, &fakeProxyCheckQueue{})
}

func newTestProxyHandlerWithQueue(repo *fakeProxyRepo, queue *fakeProxyCheckQueue) *ProxyHandler {
	return NewProxyHandler(&ProxyModule{
		ProxyUseCase: proxyapp.NewProxyUseCase(repo, fakeProxyChecker{}, queue, fakeOperationLogPort{}, fakeSystemLogPort{}),
	})
}

func TestGetProxyBindingsContract(t *testing.T) {
	gin.SetMode(gin.TestMode)
	expiresAt := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	repo := &fakeProxyRepo{
		bindings: []domain.Binding{
			{
				ID:        11,
				Key:       "a@example.com",
				ProxyID:   7,
				IPVersion: domain.ProxyIPv4,
				ExpireAt:  expiresAt,
				CreatedAt: expiresAt.Add(-time.Hour),
			},
		},
	}
	handler := newTestProxyHandler(repo)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/admin/proxies/bindings?key=a@example.com&ip=ipv4", nil)

	handler.GetProxyBindings(c)

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, "a@example.com", repo.bindingFilter.Key)
	require.Equal(t, domain.ProxyIPv4, repo.bindingFilter.IPVersion)

	var body ProxyBindingListResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	require.Equal(t, int64(1), body.Total)
	require.Len(t, body.Items, 1)
	require.Equal(t, uint(7), body.Items[0].ProxyID)
}

func TestGetProxyStatsContract(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &fakeProxyRepo{}
	handler := newTestProxyHandler(repo)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/admin/proxies/stats?pool=system&status=normal", nil)

	handler.GetProxyStats(c)

	require.Equal(t, http.StatusOK, w.Code)
	var body ProxyStatsResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
}

func TestGetProxiesReturnsCompleteURLForAdmin(t *testing.T) {
	gin.SetMode(gin.TestMode)
	now := time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)
	repo := &fakeProxyRepo{
		count: 1,
		listItems: []domain.Proxy{
			{
				ID:        7,
				Pool:      domain.ProxyPoolSystem,
				URL:       "socks5://user:password@127.0.0.1:1080",
				Status:    domain.ProxyStatusNormal,
				Country:   "US",
				CreatedAt: now,
				UpdatedAt: now,
			},
		},
	}
	handler := newTestProxyHandler(repo)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/admin/proxies", nil)

	handler.GetProxies(c)

	require.Equal(t, http.StatusOK, w.Code)
	var body ProxyListResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	require.Len(t, body.Items, 1)
	require.Equal(t, "socks5://user:password@127.0.0.1:1080", body.Items[0].URL)
}

func TestPostProxyDeleteBatchByFilterContract(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &fakeProxyRepo{count: 3}
	handler := newTestProxyHandler(repo)
	body := []byte(`{"all":true,"filter":{"pool":"system","ipv6":true,"status":"normal","createdFrom":"2026-07-03T00:00:00Z","createdTo":"2026-07-04T00:00:00Z"}}`)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/admin/proxies/delete", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	middleware.SetCurrentUser(c, 1, iamdomain.RoleAdmin, "admin@example.com", "session-id")

	handler.PostProxyDeleteBatch(c)

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, domain.ProxyPoolSystem, repo.deleteFilter.Pool)
	require.NotNil(t, repo.deleteFilter.IPv6)
	require.True(t, *repo.deleteFilter.IPv6)
	require.Equal(t, domain.ProxyStatusNormal, repo.deleteFilter.Status)
	require.NotNil(t, repo.deleteFilter.CreatedFrom)
	require.NotNil(t, repo.deleteFilter.CreatedTo)

	var response DeleteProxiesResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &response))
	require.Equal(t, 3, response.Requested)
	require.Equal(t, 3, response.Deleted)
	require.True(t, response.DeletedByFilter)
}

func TestPostProxyDisableBatchByFilterContract(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &fakeProxyRepo{count: 4}
	handler := newTestProxyHandler(repo)
	body := []byte(`{"all":true,"filter":{"pool":"system","ipv6":false,"country":"US","status":"normal"}}`)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/admin/proxies/disable", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	middleware.SetCurrentUser(c, 1, iamdomain.RoleAdmin, "admin@example.com", "session-id")

	handler.PostProxyDisableBatch(c)

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, domain.ProxyPoolSystem, repo.disableFilter.Pool)
	require.NotNil(t, repo.disableFilter.IPv6)
	require.False(t, *repo.disableFilter.IPv6)
	require.Equal(t, "US", repo.disableFilter.Country)
	require.Equal(t, domain.ProxyStatusNormal, repo.disableFilter.Status)

	var response DisableProxiesResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &response))
	require.Equal(t, 4, response.Requested)
	require.Equal(t, 4, response.Disabled)
	require.True(t, response.DisabledByFilter)
}

func TestPostProxyImportsContract(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &fakeProxyRepo{}
	queue := &fakeProxyCheckQueue{}
	handler := newTestProxyHandlerWithQueue(repo, queue)
	body := []byte(`{"pool":"system","urls":["http://127.0.0.1:18080","http://127.0.0.1:18081"],"expireAt":"2099-07-10T00:00:00Z"}`)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/admin/proxies/imports", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	middleware.SetCurrentUser(c, 1, iamdomain.RoleAdmin, "admin@example.com", "session-id")

	handler.PostProxyImports(c)

	require.Equal(t, http.StatusCreated, w.Code)
	require.Len(t, repo.created, 2)
	require.Equal(t, domain.ProxyPoolSystem, repo.created[0].Pool)

	var response ImportProxiesResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &response))
	require.Equal(t, 2, response.Requested)
	require.Equal(t, 2, response.Created)
	require.Len(t, response.Items, 2)
	require.Empty(t, queue.tasks)
	require.Len(t, queue.dispatches, 1)
	require.Len(t, repo.pendingChecks, 2)
}

func TestPostProxyCheckBatchByFilterContract(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &fakeProxyRepo{count: 2}
	queue := &fakeProxyCheckQueue{}
	handler := newTestProxyHandlerWithQueue(repo, queue)
	body := []byte(`{"all":true,"filter":{"pool":"resource","country":"US","status":"checking"}}`)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/admin/proxies/check", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	middleware.SetCurrentUser(c, 1, iamdomain.RoleAdmin, "admin@example.com", "session-id")

	handler.PostProxyCheckBatch(c)

	require.Equal(t, http.StatusAccepted, w.Code)
	require.Equal(t, domain.ProxyPoolResource, repo.listIDFilter.Pool)
	require.Equal(t, "US", repo.listIDFilter.Country)
	require.Equal(t, domain.ProxyStatusChecking, repo.listIDFilter.Status)
	require.Len(t, queue.dispatches, 1)
	require.Len(t, repo.pendingChecks, 2)

	var response CheckProxiesResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &response))
	require.Equal(t, 2, response.Requested)
	require.Equal(t, 2, response.Queued)
	require.Empty(t, response.Items)
}

func TestPostProxyCheckBatchByIDsContract(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &fakeProxyRepo{}
	queue := &fakeProxyCheckQueue{}
	handler := newTestProxyHandlerWithQueue(repo, queue)
	body := []byte(`{"proxyIds":[7,8,7]}`)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/admin/proxies/check", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	middleware.SetCurrentUser(c, 1, iamdomain.RoleAdmin, "admin@example.com", "session-id")

	handler.PostProxyCheckBatch(c)

	require.Equal(t, http.StatusAccepted, w.Code)
	require.Empty(t, queue.tasks)
	require.Len(t, queue.dispatches, 1)
	require.Equal(t, []uint{7, 8}, repo.pendingChecks)

	var response CheckProxiesResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &response))
	require.Equal(t, 2, response.Requested)
	require.Equal(t, 2, response.Queued)
	require.Empty(t, response.Items)
}

func TestPostProxyCheckQueuesContract(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &fakeProxyRepo{}
	queue := &fakeProxyCheckQueue{}
	handler := newTestProxyHandlerWithQueue(repo, queue)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Params = gin.Params{{Key: "proxyId", Value: "7"}}
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/admin/proxies/7/check", nil)
	c.Request.Header.Set("Content-Type", "application/json")
	middleware.SetCurrentUser(c, 1, iamdomain.RoleAdmin, "admin@example.com", "session-id")

	handler.PostProxyCheck(c)

	require.Equal(t, http.StatusAccepted, w.Code)
	require.Empty(t, queue.tasks)
	require.Len(t, queue.dispatches, 1)
}

func TestPostProxyCheckIgnoresExpireAtAndQueues(t *testing.T) {
	gin.SetMode(gin.TestMode)
	now := time.Now().UTC()
	repo := &fakeProxyRepo{
		findProxy: &domain.Proxy{
			ID:       8,
			Pool:     domain.ProxyPoolResource,
			URL:      "http://127.0.0.1:18080",
			ExpireAt: now.Add(-time.Hour),
			Status:   domain.ProxyStatusExpired,
			Country:  "UNKNOWN",
		},
	}
	queue := &fakeProxyCheckQueue{}
	handler := newTestProxyHandlerWithQueue(repo, queue)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Params = gin.Params{{Key: "proxyId", Value: "8"}}
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/admin/proxies/8/check", nil)
	c.Request.Header.Set("Content-Type", "application/json")
	middleware.SetCurrentUser(c, 1, iamdomain.RoleAdmin, "admin@example.com", "session-id")

	handler.PostProxyCheck(c)

	require.Equal(t, http.StatusAccepted, w.Code)
	require.Empty(t, queue.tasks)
	require.Len(t, queue.dispatches, 1)
}
