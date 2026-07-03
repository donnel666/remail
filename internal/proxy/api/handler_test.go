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
	listIDFilter  proxyapp.ProxyListFilter
	listIDAfter   uint
	listIDLimit   int
	deletedByIDs  []uint
	created       []domain.Proxy
	listIDs       []uint
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
	}
	return created, 0, nil
}

func (r *fakeProxyRepo) FindByID(_ context.Context, id uint) (*domain.Proxy, error) {
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
	return nil, nil
}

func (r *fakeProxyRepo) Count(_ context.Context, _ proxyapp.ProxyListFilter) (int64, error) {
	return 0, nil
}

func (r *fakeProxyRepo) Stats(_ context.Context, _ proxyapp.ProxyListFilter) (*proxyapp.ProxyStats, error) {
	return &proxyapp.ProxyStats{}, nil
}

func (r *fakeProxyRepo) ListIDs(_ context.Context, filter proxyapp.ProxyListFilter, afterID uint, limit int) ([]uint, error) {
	r.listIDFilter = filter
	r.listIDAfter = afterID
	r.listIDLimit = limit
	return r.listIDs, nil
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

func (r *fakeProxyRepo) DeleteBatch(_ context.Context, ids []uint) ([]uint, error) {
	r.deletedByIDs = ids
	return ids, nil
}

func (r *fakeProxyRepo) DeleteBatchWithLog(ctx context.Context, ids []uint, _ *governancedomain.OperationLog) ([]uint, error) {
	return r.DeleteBatch(ctx, ids)
}

func (r *fakeProxyRepo) DeleteByFilter(_ context.Context, filter proxyapp.ProxyListFilter) (int64, error) {
	r.deleteFilter = filter
	return 3, nil
}

func (r *fakeProxyRepo) DeleteByFilterWithLog(ctx context.Context, filter proxyapp.ProxyListFilter, _ *governancedomain.OperationLog) (int64, error) {
	return r.DeleteByFilter(ctx, filter)
}

func (r *fakeProxyRepo) MarkExpiredBefore(_ context.Context, _ time.Time) (int64, error) {
	return 0, nil
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

func (r *fakeProxyRepo) UpdateCheckResultWithLog(ctx context.Context, id uint, result domain.CheckResult, success bool, _ *governancedomain.OperationLog) (*domain.Proxy, error) {
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

type fakeOperationLogPort struct{}

func (fakeOperationLogPort) Create(_ context.Context, _ *governancedomain.OperationLog) error {
	return nil
}

type fakeSystemLogPort struct{}

func (fakeSystemLogPort) Create(_ context.Context, _ *governancedomain.SystemLog) error {
	return nil
}

func newTestProxyHandler(repo *fakeProxyRepo) *ProxyHandler {
	return NewProxyHandler(&ProxyModule{
		ProxyUseCase: proxyapp.NewProxyUseCase(repo, fakeProxyChecker{}, fakeOperationLogPort{}, fakeSystemLogPort{}),
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

func TestPostProxyDeleteBatchByFilterContract(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &fakeProxyRepo{}
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
	require.Equal(t, 3, response.Deleted)
	require.True(t, response.DeletedByFilter)
}

func TestPostProxyImportsContract(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &fakeProxyRepo{}
	handler := newTestProxyHandler(repo)
	body := []byte(`{"pool":"system","urls":["http://127.0.0.1:18080","http://127.0.0.1:18081"],"expireAt":"2026-07-10T00:00:00Z"}`)
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
}

func TestPostProxyCheckBatchByFilterContract(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &fakeProxyRepo{listIDs: []uint{1, 2}}
	handler := newTestProxyHandler(repo)
	body := []byte(`{"all":true,"filter":{"pool":"resource","country":"US","status":"checking"}}`)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/admin/proxies/check", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	middleware.SetCurrentUser(c, 1, iamdomain.RoleAdmin, "admin@example.com", "session-id")

	handler.PostProxyCheckBatch(c)

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, domain.ProxyPoolResource, repo.listIDFilter.Pool)
	require.Equal(t, "US", repo.listIDFilter.Country)
	require.Equal(t, domain.ProxyStatusChecking, repo.listIDFilter.Status)
	require.Equal(t, uint(0), repo.listIDAfter)
	require.Equal(t, 1000, repo.listIDLimit)

	var response CheckProxiesResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &response))
	require.Equal(t, 2, response.Requested)
	require.Equal(t, 2, response.Checked)
	require.Empty(t, response.Items)
}
