package infra

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	"github.com/donnel666/remail/internal/platform/testmysql"
	proxyapp "github.com/donnel666/remail/internal/proxy/app"
	"github.com/donnel666/remail/internal/proxy/domain"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

var proxyMySQLTestServer = testmysql.New("remail_proxy_test")

func TestMain(m *testing.M) {
	code := m.Run()
	_ = proxyMySQLTestServer.Close(context.Background())
	os.Exit(code)
}

func newProxyMySQLTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	return proxyMySQLTestServer.Database(t, proxyMigrationsDir(t))
}

func proxyMigrationsDir(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	return filepath.Clean(filepath.Join(filepath.Dir(file), "../../..", "migrations"))
}

func TestProxySchemaConstraintsMySQL(t *testing.T) {
	db := newProxyMySQLTestDB(t)

	requireIndexExists(t, db, "proxies", "idx_proxies_pool_url_hash")
	requireIndexExists(t, db, "proxies", "idx_proxies_url_host")
	requireIndexExists(t, db, "proxies", "idx_proxies_select_health")
	requireIndexExists(t, db, "proxies", "idx_proxies_created")
	requireIndexExists(t, db, "proxies", "idx_proxies_check_dispatch")
	requireIndexExists(t, db, "proxy_bindings", "idx_proxy_bindings_key_ip")
	requireIndexExists(t, db, "proxy_bindings", "idx_proxy_bindings_proxy_expire")
	requireIndexExists(t, db, "proxy_bindings", "idx_proxy_bindings_expire_proxy")
	require.False(t, db.Migrator().HasTable("proxy_check_jobs"))
	require.False(t, db.Migrator().HasTable("proxy_check_job_items"))

	expireAt := time.Now().UTC().Add(time.Hour)
	require.NoError(t, db.Create(&ProxyModel{
		Pool:     "resource",
		URL:      "socks5://user:pass@127.0.0.1:1080",
		URLHash:  proxyURLHash("socks5://user:pass@127.0.0.1:1080"),
		ExpireAt: ptrTime(expireAt),
		Country:  "UNKNOWN",
		Status:   "checking",
	}).Error)
	require.Error(t, db.Create(&ProxyModel{
		Pool:     "resource",
		URL:      "socks5://user:pass@127.0.0.1:1080",
		URLHash:  proxyURLHash("socks5://user:pass@127.0.0.1:1080"),
		ExpireAt: ptrTime(expireAt),
		Country:  "UNKNOWN",
		Status:   "checking",
	}).Error)
	require.NoError(t, db.Create(&ProxyModel{
		Pool:     "system",
		URL:      "socks5://user:pass@127.0.0.1:1080",
		URLHash:  proxyURLHash("socks5://user:pass@127.0.0.1:1080"),
		ExpireAt: ptrTime(expireAt),
		Country:  "UNKNOWN",
		Status:   "checking",
	}).Error)
	require.Error(t, db.Create(&ProxyModel{
		Pool:     "resource",
		URL:      "http://127.0.0.1:18080",
		URLHash:  proxyURLHash("http://127.0.0.1:18080"),
		ExpireAt: ptrTime(expireAt),
		Country:  "UNKNOWN",
		Status:   "invalid",
	}).Error)
	require.NoError(t, db.Create(&ProxyModel{
		Pool:            "resource",
		URL:             "http://127.0.0.1:18081",
		URLHash:         proxyURLHash("http://127.0.0.1:18081"),
		ExpireAt:        ptrTime(expireAt),
		Country:         "UNKNOWN",
		Status:          "pending",
		CheckGeneration: 1,
	}).Error)

	require.NoError(t, db.Create(&ProxyBindingModel{
		BindKey:   "user@example.com",
		ProxyID:   1,
		IPVersion: "ipv4",
		ExpireAt:  expireAt,
	}).Error)
	require.Error(t, db.Create(&ProxyBindingModel{
		BindKey:   "user@example.com",
		ProxyID:   1,
		IPVersion: "ipv4",
		ExpireAt:  expireAt,
	}).Error)
}

func TestProxyRepoPendingDispatchGenerationFencesStaleWorkMySQL(t *testing.T) {
	db := newProxyMySQLTestDB(t)
	repo := NewProxyRepo(db)
	ctx := context.Background()
	proxy := &domain.Proxy{
		Pool:            domain.ProxyPoolResource,
		URL:             "http://127.0.0.1:28080",
		ExpireAt:        time.Now().UTC().Add(time.Hour),
		Status:          domain.ProxyStatusPending,
		Country:         "UNKNOWN",
		CheckGeneration: 5,
	}
	require.NoError(t, repo.Create(ctx, proxy))

	tasks, err := repo.ListPendingProxyChecks(ctx, 10)
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	require.Equal(t, proxyapp.ProxyCheckTask{ProxyID: proxy.ID, CheckGeneration: 5}, tasks[0])

	activated, err := repo.ActivateProxyCheck(ctx, proxy.ID, 4)
	require.NoError(t, err)
	require.False(t, activated)
	activated, err = repo.ActivateProxyCheck(ctx, proxy.ID, 5)
	require.NoError(t, err)
	require.True(t, activated)

	matched, retriggered, err := repo.MarkPendingBatchWithLog(ctx, []uint{proxy.ID}, nil)
	require.NoError(t, err)
	require.Equal(t, 1, matched)
	require.Equal(t, 1, retriggered)

	var stored ProxyModel
	require.NoError(t, db.First(&stored, proxy.ID).Error)
	require.Equal(t, string(domain.ProxyStatusPending), stored.Status)
	require.Equal(t, uint64(6), stored.CheckGeneration)

	activated, err = repo.ActivateProxyCheck(ctx, proxy.ID, 5)
	require.NoError(t, err)
	require.False(t, activated)
	activated, err = repo.ActivateProxyCheck(ctx, proxy.ID, 6)
	require.NoError(t, err)
	require.True(t, activated)

	_, err = repo.UpdateCheckResultForGenerationWithLog(ctx, proxy.ID, 5, domain.CheckResult{
		IPVersion:  domain.ProxyIPv4,
		OutboundIP: "198.51.100.5",
		Country:    "US",
		CheckedAt:  time.Now().UTC(),
	}, true, nil)
	require.ErrorIs(t, err, domain.ErrInvalidProxyStatus)

	updated, err := repo.UpdateCheckResultForGenerationWithLog(ctx, proxy.ID, 6, domain.CheckResult{
		IPVersion:  domain.ProxyIPv4,
		OutboundIP: "198.51.100.6",
		Country:    "US",
		CheckedAt:  time.Now().UTC(),
	}, true, nil)
	require.NoError(t, err)
	require.Equal(t, domain.ProxyStatusNormal, updated.Status)
	require.Equal(t, "198.51.100.6", updated.OutboundIP)
}

func TestProxyRepoInfrastructureReleaseReturnsPendingWithoutBusinessFailureMySQL(t *testing.T) {
	db := newProxyMySQLTestDB(t)
	repo := NewProxyRepo(db)
	ctx := context.Background()
	proxy := &domain.Proxy{
		Pool:            domain.ProxyPoolResource,
		URL:             "http://127.0.0.1:28082",
		ExpireAt:        time.Now().UTC().Add(time.Hour),
		Status:          domain.ProxyStatusChecking,
		Country:         "UNKNOWN",
		Errors:          2,
		CheckGeneration: 7,
	}
	require.NoError(t, repo.Create(ctx, proxy))

	released, err := repo.ReleaseProxyCheckInfrastructureFailure(ctx, proxy.ID, 6, "database unavailable")
	require.NoError(t, err)
	require.False(t, released)

	released, err = repo.ReleaseProxyCheckInfrastructureFailure(ctx, proxy.ID, 7, "database unavailable")
	require.NoError(t, err)
	require.True(t, released)

	stored, err := repo.FindByID(ctx, proxy.ID)
	require.NoError(t, err)
	require.Equal(t, domain.ProxyStatusPending, stored.Status)
	require.Equal(t, uint64(8), stored.CheckGeneration)
	require.Equal(t, 2, stored.Errors, "infrastructure failure must not consume a business attempt")
	require.Equal(t, "database unavailable", stored.LastSafeError)

	released, err = repo.ReleaseProxyCheckInfrastructureFailure(ctx, proxy.ID, 7, "stale worker")
	require.NoError(t, err)
	require.False(t, released)
}

func TestProxyRepoBatchRetriggerReleasesCheckingRowsToPendingMySQL(t *testing.T) {
	db := newProxyMySQLTestDB(t)
	repo := NewProxyRepo(db)
	ctx := context.Background()
	proxy := &domain.Proxy{
		Pool:            domain.ProxyPoolSystem,
		URL:             "http://127.0.0.1:28081",
		ExpireAt:        time.Now().UTC().Add(time.Hour),
		Status:          domain.ProxyStatusChecking,
		Country:         "UNKNOWN",
		CheckGeneration: 2,
	}
	require.NoError(t, repo.Create(ctx, proxy))

	matched, updated, err := repo.MarkPendingBatchWithLog(ctx, []uint{proxy.ID}, nil)
	require.NoError(t, err)
	require.Equal(t, 1, matched)
	require.Equal(t, 1, updated)

	var stored ProxyModel
	require.NoError(t, db.First(&stored, proxy.ID).Error)
	require.Equal(t, string(domain.ProxyStatusPending), stored.Status)
	require.Equal(t, uint64(3), stored.CheckGeneration)
}

func TestProxyRepoCheckResultMySQL(t *testing.T) {
	db := newProxyMySQLTestDB(t)
	repo := NewProxyRepo(db)
	ctx := context.Background()

	proxy := &domain.Proxy{
		Pool:     domain.ProxyPoolResource,
		URL:      "http://127.0.0.1:18080",
		ExpireAt: time.Now().UTC().Add(time.Hour),
		Status:   domain.ProxyStatusChecking,
		Country:  "UNKNOWN",
	}
	require.NoError(t, repo.Create(ctx, proxy))

	updated, err := repo.UpdateCheckResult(ctx, proxy.ID, domain.CheckResult{
		IPVersion:  domain.ProxyIPv4,
		OutboundIP: "198.51.100.9",
		Country:    "sg",
		LatencyMs:  88,
		CheckedAt:  time.Now().UTC(),
	}, true)
	require.NoError(t, err)
	require.Equal(t, domain.ProxyStatusNormal, updated.Status)
	require.Equal(t, domain.ProxyIPv4, updated.IPVersion)
	require.Equal(t, "SG", updated.Country)
	require.Equal(t, 88, updated.LatencyMs)

	items, err := repo.List(ctx, proxyapp.ProxyListFilter{
		Pool:      domain.ProxyPoolResource,
		IPVersion: domain.ProxyIPv4,
		Status:    domain.ProxyStatusNormal,
		Country:   "SG",
	}, 0, 20)
	require.NoError(t, err)
	require.Len(t, items, 1)

	require.NoError(t, updated.MarkPending())
	require.NoError(t, updated.MarkChecking())
	require.NoError(t, repo.Update(ctx, updated))
	updated, err = repo.UpdateCheckResult(ctx, proxy.ID, domain.CheckResult{
		LastSafeError: "Proxy endpoint is unreachable.",
		CheckedAt:     time.Now().UTC(),
	}, false)
	require.NoError(t, err)
	require.Equal(t, domain.ProxyStatusAbnormal, updated.Status)
	require.Equal(t, 1, updated.Errors)
	require.Equal(t, "Proxy endpoint is unreachable.", updated.LastSafeError)

	items, err = repo.List(ctx, proxyapp.ProxyListFilter{
		Status: domain.ProxyStatusAbnormal,
	}, 0, 20)
	require.NoError(t, err)
	require.Len(t, items, 1)

	require.NoError(t, updated.MarkPending())
	require.NoError(t, updated.MarkChecking())
	require.NoError(t, repo.Update(ctx, updated))
	updated, err = repo.UpdateCheckResult(ctx, proxy.ID, domain.CheckResult{
		LastSafeError: "Proxy endpoint is unreachable.",
		CheckedAt:     time.Now().UTC(),
	}, false)
	require.NoError(t, err)
	require.Equal(t, domain.ProxyStatusAbnormal, updated.Status)
	require.Equal(t, 1, updated.Errors)

	require.NoError(t, updated.MarkPending())
	require.NoError(t, updated.MarkChecking())
	require.NoError(t, repo.Update(ctx, updated))
	updated, err = repo.UpdateCheckResult(ctx, proxy.ID, domain.CheckResult{
		NonRetryable:  true,
		LastSafeError: "Invalid proxy URL.",
		CheckedAt:     time.Now().UTC(),
	}, false)
	require.NoError(t, err)
	require.Equal(t, domain.ProxyStatusAbnormal, updated.Status)
	require.Equal(t, 0, updated.Errors)
	require.Equal(t, "Invalid proxy URL.", updated.LastSafeError)

	stillAbnormal, err := repo.FindByID(ctx, proxy.ID)
	require.NoError(t, err)
	require.Equal(t, domain.ProxyStatusAbnormal, stillAbnormal.Status)
	require.Equal(t, 0, stillAbnormal.Errors)

	invalidProxy := &domain.Proxy{
		Pool:     domain.ProxyPoolResource,
		URL:      "http://127.0.0.1:18081",
		ExpireAt: time.Now().UTC().Add(time.Hour),
		Status:   domain.ProxyStatusChecking,
		Country:  "UNKNOWN",
	}
	require.NoError(t, repo.Create(ctx, invalidProxy))
	updated, err = repo.UpdateCheckResult(ctx, invalidProxy.ID, domain.CheckResult{
		NonRetryable:  true,
		LastSafeError: "Invalid proxy URL.",
		CheckedAt:     time.Now().UTC(),
	}, false)
	require.NoError(t, err)
	require.Equal(t, domain.ProxyStatusAbnormal, updated.Status)
	require.Equal(t, 0, updated.Errors)

	disabledProxy := &domain.Proxy{
		Pool:     domain.ProxyPoolResource,
		URL:      "http://127.0.0.1:18082",
		ExpireAt: time.Now().UTC().Add(time.Hour),
		Status:   domain.ProxyStatusDisabled,
		Country:  "US",
	}
	require.NoError(t, repo.Create(ctx, disabledProxy))

	updated, err = repo.UpdateCheckResult(ctx, disabledProxy.ID, domain.CheckResult{
		IPVersion:  domain.ProxyIPv4,
		OutboundIP: "198.51.100.10",
		Country:    "sg",
		LatencyMs:  66,
		CheckedAt:  time.Now().UTC(),
	}, true)
	require.ErrorIs(t, err, domain.ErrInvalidProxyStatus)
	require.Nil(t, updated)

	stillDisabled, err := repo.FindByID(ctx, disabledProxy.ID)
	require.NoError(t, err)
	require.Equal(t, domain.ProxyStatusDisabled, stillDisabled.Status)
	require.Equal(t, "US", stillDisabled.Country)
}

func TestProxyRepoRuntimeReportsDoNotMutateDisabledProxyMySQL(t *testing.T) {
	db := newProxyMySQLTestDB(t)
	repo := NewProxyRepo(db)
	ctx := context.Background()
	disabled := &domain.Proxy{
		Pool:          domain.ProxyPoolResource,
		URL:           "http://127.0.0.1:18182",
		ExpireAt:      time.Now().UTC().Add(time.Hour),
		Status:        domain.ProxyStatusDisabled,
		Country:       "US",
		Errors:        2,
		LastSafeError: "Proxy disabled by administrator.",
	}
	require.NoError(t, repo.Create(ctx, disabled))

	updated, err := repo.ReportFailure(ctx, disabled.ID, "network timeout", true)
	require.NoError(t, err)
	require.Equal(t, domain.ProxyStatusDisabled, updated.Status)
	require.Equal(t, 2, updated.Errors)
	require.Equal(t, "Proxy disabled by administrator.", updated.LastSafeError)

	require.NoError(t, repo.ReportSuccess(ctx, disabled.ID, time.Now().UTC()))

	stored, err := repo.FindByID(ctx, disabled.ID)
	require.NoError(t, err)
	require.Equal(t, domain.ProxyStatusDisabled, stored.Status)
	require.Equal(t, 2, stored.Errors)
	require.Equal(t, "Proxy disabled by administrator.", stored.LastSafeError)
	require.Nil(t, stored.LastUsedAt)
}

func TestProxyRepoRetryableFailureThresholdCreatesPendingGenerationMySQL(t *testing.T) {
	db := newProxyMySQLTestDB(t)
	repo := NewProxyRepo(db)
	ctx := context.Background()
	proxy := &domain.Proxy{
		Pool:     domain.ProxyPoolResource,
		URL:      "http://127.0.0.1:18183",
		ExpireAt: time.Now().UTC().Add(time.Hour),
		Status:   domain.ProxyStatusNormal,
		Country:  "US",
	}
	require.NoError(t, repo.Create(ctx, proxy))

	for attempt := 1; attempt <= 2; attempt++ {
		updated, err := repo.ReportFailure(ctx, proxy.ID, "network timeout", true)
		require.NoError(t, err)
		require.Equal(t, domain.ProxyStatusNormal, updated.Status)
		require.Equal(t, attempt, updated.Errors)
	}

	updated, err := repo.ReportFailure(ctx, proxy.ID, "network timeout", true)
	require.NoError(t, err)
	require.Equal(t, domain.ProxyStatusPending, updated.Status)
	require.Zero(t, updated.Errors)
	require.Equal(t, uint64(2), updated.CheckGeneration)
}

func TestProxyRepoAcquireResourceBindingMySQL(t *testing.T) {
	db := newProxyMySQLTestDB(t)
	repo := NewProxyRepo(db)
	ctx := context.Background()
	now := time.Now().UTC()

	first := &domain.Proxy{
		Pool:       domain.ProxyPoolResource,
		URL:        "http://127.0.0.1:18081",
		ExpireAt:   now.Add(24 * time.Hour),
		IPVersion:  domain.ProxyIPv4,
		OutboundIP: "198.51.100.10",
		Country:    "US",
		LatencyMs:  20,
		Status:     domain.ProxyStatusNormal,
	}
	second := &domain.Proxy{
		Pool:       domain.ProxyPoolResource,
		URL:        "http://127.0.0.1:18082",
		ExpireAt:   now.Add(24 * time.Hour),
		IPVersion:  domain.ProxyIPv4,
		OutboundIP: "198.51.100.11",
		Country:    "US",
		LatencyMs:  30,
		Status:     domain.ProxyStatusNormal,
	}
	require.NoError(t, repo.Create(ctx, first))
	require.NoError(t, repo.Create(ctx, second))

	selected, err := repo.AcquireResourceProxy(ctx, "a@example.com", domain.ProxyIPv4, now, 7*24*time.Hour)
	require.NoError(t, err)
	require.Equal(t, first.ID, selected.ID)

	selected, err = repo.AcquireResourceProxy(ctx, "a@example.com", domain.ProxyIPv4, now.Add(time.Minute), 7*24*time.Hour)
	require.NoError(t, err)
	require.Equal(t, first.ID, selected.ID)

	selected, err = repo.AcquireResourceProxy(ctx, "b@example.com", domain.ProxyIPv4, now.Add(2*time.Minute), 7*24*time.Hour)
	require.NoError(t, err)
	require.Equal(t, second.ID, selected.ID)

	var bindingCount int64
	require.NoError(t, db.Model(&ProxyBindingModel{}).Count(&bindingCount).Error)
	require.Equal(t, int64(2), bindingCount)

	bindings, err := repo.ListBindings(ctx, proxyapp.ProxyBindingListFilter{
		Key:       "a@example.com",
		IPVersion: domain.ProxyIPv4,
	}, 0, 20)
	require.NoError(t, err)
	require.Len(t, bindings, 1)
	require.Equal(t, first.ID, bindings[0].ProxyID)

	total, err := repo.CountBindings(ctx, proxyapp.ProxyBindingListFilter{
		ProxyID: first.ID,
	})
	require.NoError(t, err)
	require.Equal(t, int64(1), total)

	var wg sync.WaitGroup
	errs := make(chan error, 100)
	selectedIDs := make(chan uint, 100)
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			selected, acquireErr := repo.AcquireResourceProxy(ctx, "c@example.com", domain.ProxyIPv4, now.Add(3*time.Minute), 7*24*time.Hour)
			if acquireErr != nil {
				errs <- acquireErr
				return
			}
			selectedIDs <- selected.ID
		}()
	}
	wg.Wait()
	close(errs)
	close(selectedIDs)
	for err := range errs {
		require.NoError(t, err)
	}
	var expectedID uint
	for id := range selectedIDs {
		if expectedID == 0 {
			expectedID = id
		}
		require.Equal(t, expectedID, id)
	}
	bindings, err = repo.ListBindings(ctx, proxyapp.ProxyBindingListFilter{
		Key: "c@example.com",
	}, 0, 20)
	require.NoError(t, err)
	require.Len(t, bindings, 1)
}

func TestProxyRepoListsSafeAdminResourceBindingMetadataMySQL(t *testing.T) {
	db := newProxyMySQLTestDB(t)
	repo := NewProxyRepo(db)
	now := time.Now().UTC().Truncate(time.Second)
	proxy := &domain.Proxy{
		Pool:       domain.ProxyPoolResource,
		URL:        "socks5://proxy-user:proxy-password@proxy.example.net:1080",
		ExpireAt:   now.Add(24 * time.Hour),
		IPVersion:  domain.ProxyIPv4,
		OutboundIP: "198.51.100.20",
		Country:    "US",
		Status:     domain.ProxyStatusNormal,
	}
	require.NoError(t, repo.Create(context.Background(), proxy))
	_, err := repo.AcquireResourceProxy(context.Background(), "safe-binding@example.com", domain.ProxyIPv4, now, 7*24*time.Hour)
	require.NoError(t, err)

	items, err := repo.ListAdminResourceProxyBindings(context.Background(), []string{"SAFE-BINDING@example.com"})
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.Equal(t, "safe-binding@example.com", items[0].BindKey)
	require.Equal(t, proxy.ID, items[0].ProxyID)
	require.Equal(t, "proxy.example.net", items[0].Host)
	require.Equal(t, "198.51.100.20", items[0].OutboundIP)
	require.Equal(t, "ipv4", items[0].IPVersion)
	require.Equal(t, "normal", items[0].Status)
	require.NotContains(t, items[0].Host, "proxy-user")
	require.NotContains(t, items[0].Host, "proxy-password")
}

func TestProxyRepoAcquireResourceCoversExpiredBindingMySQL(t *testing.T) {
	db := newProxyMySQLTestDB(t)
	repo := NewProxyRepo(db)
	ctx := context.Background()
	now := time.Now().UTC()

	expiredProxy := &domain.Proxy{
		Pool:       domain.ProxyPoolResource,
		URL:        "http://127.0.0.1:18181",
		ExpireAt:   now.Add(24 * time.Hour),
		IPVersion:  domain.ProxyIPv4,
		OutboundIP: "198.51.100.101",
		Country:    "US",
		LatencyMs:  200,
		Status:     domain.ProxyStatusNormal,
	}
	replacementProxy := &domain.Proxy{
		Pool:       domain.ProxyPoolResource,
		URL:        "http://127.0.0.1:18182",
		ExpireAt:   now.Add(24 * time.Hour),
		IPVersion:  domain.ProxyIPv4,
		OutboundIP: "198.51.100.102",
		Country:    "US",
		LatencyMs:  10,
		Status:     domain.ProxyStatusNormal,
	}
	require.NoError(t, repo.Create(ctx, expiredProxy))
	require.NoError(t, repo.Create(ctx, replacementProxy))
	require.NoError(t, db.Create(&ProxyBindingModel{
		BindKey:    "expired@example.com",
		ProxyID:    expiredProxy.ID,
		IPVersion:  string(domain.ProxyIPv4),
		ExpireAt:   now.Add(-time.Minute),
		CreatedAt:  now.Add(-2 * time.Hour),
		LastUsedAt: ptrTime(now.Add(-2 * time.Hour)),
	}).Error)

	selected, err := repo.AcquireResourceProxy(ctx, "expired@example.com", domain.ProxyIPv4, now, 7*24*time.Hour)
	require.NoError(t, err)
	require.Equal(t, replacementProxy.ID, selected.ID)

	var bindings []ProxyBindingModel
	require.NoError(t, db.Find(&bindings, "bind_key = ?", "expired@example.com").Error)
	require.Len(t, bindings, 1)
	require.Equal(t, replacementProxy.ID, bindings[0].ProxyID)
	require.True(t, bindings[0].ExpireAt.After(now))
	require.NotNil(t, bindings[0].LastUsedAt)
}

func TestProxyRepoAcquireSystemDoesNotBindMySQL(t *testing.T) {
	db := newProxyMySQLTestDB(t)
	repo := NewProxyRepo(db)
	ctx := context.Background()
	now := time.Now().UTC()

	system := &domain.Proxy{
		Pool:       domain.ProxyPoolSystem,
		URL:        "http://127.0.0.1:19081",
		ExpireAt:   now.Add(24 * time.Hour),
		IPVersion:  domain.ProxyIPv6,
		OutboundIP: "2001:db8::2",
		Country:    "JP",
		LatencyMs:  40,
		Status:     domain.ProxyStatusNormal,
	}
	require.NoError(t, repo.Create(ctx, system))

	selected, err := repo.AcquireSystemProxy(ctx, domain.ProxyIPAuto, now)
	require.NoError(t, err)
	require.Equal(t, system.ID, selected.ID)

	var bindingCount int64
	require.NoError(t, db.Model(&ProxyBindingModel{}).Count(&bindingCount).Error)
	require.Equal(t, int64(0), bindingCount)
}

func TestProxyRepoAcquirePrefersLowerErrorCountMySQL(t *testing.T) {
	db := newProxyMySQLTestDB(t)
	repo := NewProxyRepo(db)
	ctx := context.Background()
	now := time.Now().UTC()

	resourceWithError := &domain.Proxy{
		Pool:       domain.ProxyPoolResource,
		URL:        "http://127.0.0.1:19581",
		ExpireAt:   now.Add(24 * time.Hour),
		IPVersion:  domain.ProxyIPv4,
		OutboundIP: "198.51.100.30",
		Country:    "US",
		LatencyMs:  1,
		Status:     domain.ProxyStatusNormal,
		Errors:     1,
	}
	resourceHealthy := &domain.Proxy{
		Pool:       domain.ProxyPoolResource,
		URL:        "http://127.0.0.1:19582",
		ExpireAt:   now.Add(24 * time.Hour),
		IPVersion:  domain.ProxyIPv4,
		OutboundIP: "198.51.100.31",
		Country:    "US",
		LatencyMs:  999,
		Status:     domain.ProxyStatusNormal,
	}
	systemWithError := &domain.Proxy{
		Pool:       domain.ProxyPoolSystem,
		URL:        "http://127.0.0.1:19583",
		ExpireAt:   now.Add(24 * time.Hour),
		IPVersion:  domain.ProxyIPv4,
		OutboundIP: "198.51.100.32",
		Country:    "US",
		LatencyMs:  1,
		Status:     domain.ProxyStatusNormal,
		Errors:     1,
	}
	systemHealthy := &domain.Proxy{
		Pool:       domain.ProxyPoolSystem,
		URL:        "http://127.0.0.1:19584",
		ExpireAt:   now.Add(24 * time.Hour),
		IPVersion:  domain.ProxyIPv4,
		OutboundIP: "198.51.100.33",
		Country:    "US",
		LatencyMs:  999,
		Status:     domain.ProxyStatusNormal,
	}
	require.NoError(t, repo.Create(ctx, resourceWithError))
	require.NoError(t, repo.Create(ctx, resourceHealthy))
	require.NoError(t, repo.Create(ctx, systemWithError))
	require.NoError(t, repo.Create(ctx, systemHealthy))

	selected, err := repo.AcquireResourceProxy(ctx, "healthy@example.com", domain.ProxyIPv4, now, 7*24*time.Hour)
	require.NoError(t, err)
	require.Equal(t, resourceHealthy.ID, selected.ID)

	selected, err = repo.AcquireSystemProxy(ctx, domain.ProxyIPv4, now)
	require.NoError(t, err)
	require.Equal(t, systemHealthy.ID, selected.ID)
}

func TestProxyRepoMaintenanceMySQL(t *testing.T) {
	db := newProxyMySQLTestDB(t)
	repo := NewProxyRepo(db)
	ctx := context.Background()
	now := time.Now().UTC()

	expired := &domain.Proxy{
		Pool:       domain.ProxyPoolResource,
		URL:        "http://127.0.0.1:20081",
		ExpireAt:   now.Add(-time.Minute),
		IPVersion:  domain.ProxyIPv4,
		OutboundIP: "198.51.100.20",
		Country:    "US",
		Status:     domain.ProxyStatusNormal,
	}
	checkingExpired := &domain.Proxy{
		Pool:       domain.ProxyPoolResource,
		URL:        "http://127.0.0.1:20083",
		ExpireAt:   now.Add(-time.Minute),
		IPVersion:  domain.ProxyIPv4,
		OutboundIP: "198.51.100.21",
		Country:    "US",
		Status:     domain.ProxyStatusChecking,
	}
	normal := &domain.Proxy{
		Pool:       domain.ProxyPoolSystem,
		URL:        "http://127.0.0.1:20082",
		ExpireAt:   now.Add(time.Hour),
		IPVersion:  domain.ProxyIPv6,
		OutboundIP: "2001:db8::3",
		Country:    "JP",
		Status:     domain.ProxyStatusNormal,
	}
	require.NoError(t, repo.Create(ctx, expired))
	require.NoError(t, repo.Create(ctx, checkingExpired))
	require.NoError(t, repo.Create(ctx, normal))

	updated, err := repo.MarkExpiredBefore(ctx, now)
	require.NoError(t, err)
	require.Equal(t, int64(1), updated)
	stored, err := repo.FindByID(ctx, expired.ID)
	require.NoError(t, err)
	require.Equal(t, domain.ProxyStatusExpired, stored.Status)
	stored, err = repo.FindByID(ctx, checkingExpired.ID)
	require.NoError(t, err)
	require.Equal(t, domain.ProxyStatusChecking, stored.Status)

	deleted, err := repo.DeleteByFilter(ctx, proxyapp.ProxyListFilter{
		Pool: domain.ProxyPoolSystem,
		IPv6: ptrBool(true),
	})
	require.NoError(t, err)
	require.Equal(t, int64(1), deleted)
	stored, err = repo.FindByID(ctx, normal.ID)
	require.NoError(t, err)
	require.Nil(t, stored)
}

func TestProxyRepoStatsAndSearchMySQL(t *testing.T) {
	db := newProxyMySQLTestDB(t)
	repo := NewProxyRepo(db)
	ctx := context.Background()
	now := time.Now().UTC()

	first := &domain.Proxy{
		Pool:       domain.ProxyPoolResource,
		URL:        "http://127.0.0.1:21081",
		ExpireAt:   now.Add(time.Hour),
		IPVersion:  domain.ProxyIPv4,
		OutboundIP: "198.51.100.40",
		Country:    "US",
		Status:     domain.ProxyStatusNormal,
	}
	second := &domain.Proxy{
		Pool:       domain.ProxyPoolSystem,
		URL:        "http://127.0.0.1:21082",
		ExpireAt:   now.Add(time.Hour),
		IPVersion:  domain.ProxyIPv6,
		OutboundIP: "2001:db8::40",
		Country:    "JP",
		Status:     domain.ProxyStatusChecking,
	}
	require.NoError(t, repo.Create(ctx, first))
	require.NoError(t, repo.Create(ctx, second))

	stats, err := repo.Stats(ctx, proxyapp.ProxyListFilter{})
	require.NoError(t, err)
	require.Equal(t, int64(2), stats.Total)
	require.Contains(t, stats.Countries, proxyapp.ProxyCount{Key: "US", Count: 1})
	require.Contains(t, stats.Countries, proxyapp.ProxyCount{Key: "JP", Count: 1})

	searched, err := repo.List(ctx, proxyapp.ProxyListFilter{
		Search: "127.0.0",
	}, 0, 20)
	require.NoError(t, err)
	require.Len(t, searched, 2)

	exactURL, err := repo.List(ctx, proxyapp.ProxyListFilter{
		Search: first.URL,
	}, 0, 20)
	require.NoError(t, err)
	require.Len(t, exactURL, 1)
	require.Equal(t, first.ID, exactURL[0].ID)
}

func TestProxyRepoExplainEvidenceMySQL(t *testing.T) {
	db := newProxyMySQLTestDB(t)
	requireIndexExists(t, db, "proxies", "idx_proxies_select_health")
	requireIndexExists(t, db, "proxies", "idx_proxies_created")
	requireIndexExists(t, db, "proxy_bindings", "idx_proxy_bindings_expire_proxy")

	now := time.Now().UTC()
	require.NoError(t, db.Create(&ProxyModel{
		Pool:       "resource",
		URL:        "http://127.0.0.1:22081",
		URLHash:    proxyURLHash("http://127.0.0.1:22081"),
		ExpireAt:   ptrTime(now.Add(time.Hour)),
		IPVersion:  "ipv4",
		OutboundIP: "198.51.100.50",
		Country:    "US",
		Status:     "normal",
	}).Error)
	require.NoError(t, db.Create(&ProxyModel{
		Pool:       "system",
		URL:        "http://127.0.0.1:22082",
		URLHash:    proxyURLHash("http://127.0.0.1:22082"),
		ExpireAt:   ptrTime(now.Add(time.Hour)),
		IPVersion:  "ipv6",
		OutboundIP: "2001:db8::50",
		Country:    "JP",
		Status:     "normal",
	}).Error)

	resourceSQL, resourceArgs := buildSelectResourceProxySQL(domain.ProxyIPv4, now)
	requireExplainUsesIndex(t, db, "idx_proxies_select_health", "EXPLAIN "+resourceSQL, resourceArgs...)

	systemSQL, systemArgs := buildSelectSystemProxySQL(domain.ProxyIPv6, now)
	requireExplainUsesIndex(t, db, "idx_proxies_select_health", "EXPLAIN "+systemSQL, systemArgs...)

	requireExplainUsesIndexedAccess(t, db,
		"EXPLAIN SELECT id FROM proxies WHERE pool = 'system' AND status = 'normal' AND ip_version = 'ipv6' ORDER BY created_at DESC, id DESC LIMIT 100",
	)
}

func TestProxyRepoCreateWithLogRollsBackWhenOperationLogFailsMySQL(t *testing.T) {
	db := newProxyMySQLTestDB(t)
	repo := NewProxyRepo(db)
	ctx := context.Background()

	require.NoError(t, db.Migrator().DropTable("operation_logs"))
	proxy := &domain.Proxy{
		Pool:     domain.ProxyPoolResource,
		URL:      "http://127.0.0.1:23081",
		ExpireAt: time.Now().UTC().Add(time.Hour),
		Status:   domain.ProxyStatusChecking,
		Country:  "UNKNOWN",
	}
	err := repo.CreateWithLog(ctx, proxy, &governancedomain.OperationLog{
		OperatorUserID: 1,
		OperationType:  "proxy.proxy.create",
		ResourceType:   "proxy",
		Path:           "/v1/admin/proxies/resource",
		Result:         "success",
		SafeSummary:    "Proxy created.",
		RequestID:      "rollback-test",
	})
	require.Error(t, err)

	var count int64
	require.NoError(t, db.Model(&ProxyModel{}).Where("url_hash = ?", proxyURLHash("http://127.0.0.1:23081")).Count(&count).Error)
	require.Equal(t, int64(0), count)
}

func TestProxyRepoDeleteBatchWithLogWritesNoopAuditMySQL(t *testing.T) {
	db := newProxyMySQLTestDB(t)
	repo := NewProxyRepo(db)
	ctx := context.Background()

	deletedIDs, err := repo.DeleteBatchWithLog(ctx, []uint{99999}, &governancedomain.OperationLog{
		OperatorUserID: 1,
		OperationType:  "proxy.proxy.delete",
		ResourceType:   "proxy",
		Path:           "/v1/admin/proxies/delete",
		Result:         "success",
		SafeSummary:    "Proxy deleted.",
		RequestID:      "noop-delete-test",
	})
	require.NoError(t, err)
	require.Empty(t, deletedIDs)

	var row struct {
		Count       int64  `gorm:"column:count"`
		ResourceID  string `gorm:"column:resource_id"`
		SafeSummary string `gorm:"column:safe_summary"`
	}
	require.NoError(t, db.Raw(
		"SELECT COUNT(*) AS count, COALESCE(MAX(resource_id), '') AS resource_id, COALESCE(MAX(safe_summary), '') AS safe_summary FROM operation_logs WHERE request_id = ?",
		"noop-delete-test",
	).Scan(&row).Error)
	require.Equal(t, int64(1), row.Count)
	require.Equal(t, "batch", row.ResourceID)
	require.Equal(t, "Proxy deleted. Count: 0.", row.SafeSummary)
}

func ptrBool(value bool) *bool {
	return &value
}

func ptrTime(value time.Time) *time.Time {
	return &value
}

func requireIndexExists(t *testing.T, db *gorm.DB, tableName string, indexName string) {
	t.Helper()

	var count int64
	require.NoError(t, db.Raw(
		"SELECT COUNT(*) FROM information_schema.statistics WHERE table_schema = DATABASE() AND table_name = ? AND index_name = ?",
		tableName,
		indexName,
	).Scan(&count).Error)
	require.Positive(t, count, "expected index %s on %s", indexName, tableName)
}

func requireExplainUsesIndex(t *testing.T, db *gorm.DB, expectedKey string, query string, args ...any) {
	t.Helper()

	rows := explainRows(t, db, query, args...)
	usedExpectedKey := false
	seenKeys := make([]string, 0, len(rows))
	for _, row := range rows {
		assertExplainIndexedRow(t, row, query)
		seenKeys = append(seenKeys, row.Key.String)
		if row.Key.String == expectedKey {
			usedExpectedKey = true
		}
	}
	require.True(t, usedExpectedKey, "expected query to use index %s, saw %v: %s", expectedKey, seenKeys, query)
}

func requireExplainUsesIndexedAccess(t *testing.T, db *gorm.DB, query string, args ...any) {
	t.Helper()

	rows := explainRows(t, db, query, args...)
	for _, row := range rows {
		assertExplainIndexedRow(t, row, query)
	}
}

type explainRow struct {
	Key        sql.NullString `gorm:"column:key"`
	Rows       sql.NullInt64  `gorm:"column:rows"`
	AccessType sql.NullString `gorm:"column:type"`
}

func explainRows(t *testing.T, db *gorm.DB, query string, args ...any) []explainRow {
	t.Helper()

	var rows []struct {
		Key        sql.NullString `gorm:"column:key"`
		Rows       sql.NullInt64  `gorm:"column:rows"`
		AccessType sql.NullString `gorm:"column:type"`
	}
	require.NoError(t, db.Raw(query, args...).Scan(&rows).Error)
	require.NotEmpty(t, rows, "expected EXPLAIN rows for %s", query)
	items := make([]explainRow, len(rows))
	for i, row := range rows {
		items[i] = explainRow(row)
	}
	return items
}

func assertExplainIndexedRow(t *testing.T, row explainRow, query string) {
	t.Helper()

	require.True(t, row.Key.Valid, "expected query to use an index: %s", query)
	require.True(t, row.Rows.Valid, "expected query to expose row estimate: %s", query)
	require.LessOrEqual(t, row.Rows.Int64, int64(10), "unexpected row estimate for %s using %s", query, row.Key.String)
	require.NotEqual(t, "ALL", row.AccessType.String, "unexpected full table scan for %s", query)
}
