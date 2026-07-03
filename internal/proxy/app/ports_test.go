package app

import (
	"context"
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

	resourceAcquireCount int
	systemAcquireCount   int
	reportSuccessCount   int
	reportFailureCount   int
}

func (r *fakeProxyRepository) Create(context.Context, *domain.Proxy) error { return nil }
func (r *fakeProxyRepository) CreateWithLog(context.Context, *domain.Proxy, *governancedomain.OperationLog) error {
	return nil
}
func (r *fakeProxyRepository) CreateBatchWithLog(context.Context, []*domain.Proxy, *governancedomain.OperationLog) ([]domain.Proxy, int, error) {
	return nil, 0, nil
}
func (r *fakeProxyRepository) FindByID(context.Context, uint) (*domain.Proxy, error) {
	return nil, nil
}
func (r *fakeProxyRepository) List(context.Context, ProxyListFilter, int, int) ([]domain.Proxy, error) {
	return nil, nil
}
func (r *fakeProxyRepository) Count(context.Context, ProxyListFilter) (int64, error) { return 0, nil }
func (r *fakeProxyRepository) Stats(context.Context, ProxyListFilter) (*ProxyStats, error) {
	return &ProxyStats{}, nil
}
func (r *fakeProxyRepository) ListIDs(context.Context, ProxyListFilter, uint, int) ([]uint, error) {
	return nil, nil
}
func (r *fakeProxyRepository) ListBindings(context.Context, ProxyBindingListFilter, int, int) ([]domain.Binding, error) {
	return nil, nil
}
func (r *fakeProxyRepository) CountBindings(context.Context, ProxyBindingListFilter) (int64, error) {
	return 0, nil
}
func (r *fakeProxyRepository) Update(context.Context, *domain.Proxy) error { return nil }
func (r *fakeProxyRepository) UpdateWithLog(context.Context, *domain.Proxy, *governancedomain.OperationLog) error {
	return nil
}
func (r *fakeProxyRepository) DeleteBatch(context.Context, []uint) ([]uint, error) {
	return nil, nil
}
func (r *fakeProxyRepository) DeleteBatchWithLog(context.Context, []uint, *governancedomain.OperationLog) ([]uint, error) {
	return nil, nil
}
func (r *fakeProxyRepository) DeleteByFilter(context.Context, ProxyListFilter) (int64, error) {
	return 0, nil
}
func (r *fakeProxyRepository) DeleteByFilterWithLog(context.Context, ProxyListFilter, *governancedomain.OperationLog) (int64, error) {
	return 0, nil
}
func (r *fakeProxyRepository) MarkExpiredBefore(context.Context, time.Time) (int64, error) {
	return 0, nil
}
func (r *fakeProxyRepository) UpdateCheckResult(context.Context, uint, domain.CheckResult, bool) (*domain.Proxy, error) {
	return nil, nil
}
func (r *fakeProxyRepository) UpdateCheckResultWithLog(context.Context, uint, domain.CheckResult, bool, *governancedomain.OperationLog) (*domain.Proxy, error) {
	return nil, nil
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

func TestProxyAcquireUsesDirectFallbackAfterAttemptBudget(t *testing.T) {
	repo := &fakeProxyRepository{
		resourceProxy: &domain.Proxy{ID: 1, Pool: domain.ProxyPoolResource, URL: "http://resource.example:8080"},
		systemProxy:   &domain.Proxy{ID: 2, Pool: domain.ProxyPoolSystem, URL: "http://system.example:8080"},
	}
	uc := NewProxyUseCase(repo, nil, nil, nil)

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
	uc := NewProxyUseCase(repo, nil, nil, nil)

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
	uc := NewProxyUseCase(repo, nil, nil, nil)

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
	uc := NewProxyUseCase(repo, nil, nil, nil)

	require.NoError(t, uc.ReportSuccess(context.Background(), 0))
	require.NoError(t, uc.ReportFailure(context.Background(), 0, "direct request failed"))
	require.NoError(t, uc.ReportNonRetryableFailure(context.Background(), 0, "direct request failed"))
	require.Equal(t, 0, repo.reportSuccessCount)
	require.Equal(t, 0, repo.reportFailureCount)
}
