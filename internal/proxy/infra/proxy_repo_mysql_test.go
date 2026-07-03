package infra

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	"github.com/donnel666/remail/internal/platform"
	proxyapp "github.com/donnel666/remail/internal/proxy/app"
	"github.com/donnel666/remail/internal/proxy/domain"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

func newProxyMySQLTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	ctx := context.Background()
	req := testcontainers.ContainerRequest{
		Image:        "mysql:8.0",
		ExposedPorts: []string{"3306/tcp"},
		Env: map[string]string{
			"MYSQL_ROOT_PASSWORD": "root",
			"MYSQL_DATABASE":      "remail_test",
			"MYSQL_USER":          "remail",
			"MYSQL_PASSWORD":      "remail",
		},
		WaitingFor: wait.ForListeningPort("3306/tcp").WithStartupTimeout(2 * time.Minute),
	}

	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, container.Terminate(context.Background()))
	})

	host, err := container.Host(ctx)
	require.NoError(t, err)
	port, err := container.MappedPort(ctx, "3306/tcp")
	require.NoError(t, err)

	dsn := fmt.Sprintf("remail:remail@tcp(%s:%s)/remail_test?charset=utf8mb4&parseTime=True&loc=Local", host, port.Port())
	var db *gorm.DB
	var sqlDB *sql.DB
	var lastErr error
	require.Eventually(t, func() bool {
		if sqlDB != nil {
			_ = sqlDB.Close()
		}

		db, lastErr = gorm.Open(mysql.Open(dsn), &gorm.Config{TranslateError: true})
		if lastErr != nil {
			return false
		}

		sqlDB, lastErr = db.DB()
		if lastErr != nil {
			return false
		}
		lastErr = sqlDB.PingContext(ctx)
		return lastErr == nil
	}, 30*time.Second, 500*time.Millisecond, "mysql did not become ready: %v", lastErr)
	t.Cleanup(func() {
		require.NoError(t, sqlDB.Close())
	})

	require.NoError(t, platform.RunMigrations(sqlDB, proxyMigrationsDir(t)))
	return db
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
	requireIndexExists(t, db, "proxy_bindings", "idx_proxy_bindings_key_ip")
	requireIndexExists(t, db, "proxy_bindings", "idx_proxy_bindings_proxy_expire")
	requireIndexExists(t, db, "proxy_bindings", "idx_proxy_bindings_expire_proxy")
	requireIndexExists(t, db, "proxy_check_jobs", "idx_proxy_check_jobs_status_created")
	requireIndexExists(t, db, "proxy_check_jobs", "idx_proxy_check_jobs_proxy_created")
	requireIndexExists(t, db, "proxy_check_job_items", "idx_proxy_check_job_items_job_proxy")
	requireIndexExists(t, db, "proxy_check_job_items", "idx_proxy_check_job_items_proxy")

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

	require.NoError(t, updated.MarkChecking())
	require.NoError(t, repo.Update(ctx, updated))
	updated, err = repo.UpdateCheckResult(ctx, proxy.ID, domain.CheckResult{
		LastSafeError: "Proxy endpoint is unreachable.",
		CheckedAt:     time.Now().UTC(),
	}, false)
	require.NoError(t, err)
	require.Equal(t, domain.ProxyStatusAbnormal, updated.Status)
	require.Equal(t, 1, updated.Errors)

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

func TestProxyRepoStatsAndListIDsMySQL(t *testing.T) {
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

	ids, err := repo.ListIDs(ctx, proxyapp.ProxyListFilter{
		Pool: domain.ProxyPoolSystem,
		IPv6: ptrBool(true),
	}, 0, 1000)
	require.NoError(t, err)
	require.Equal(t, []uint{second.ID}, ids)

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
	requireIndexExists(t, db, "proxy_check_jobs", "idx_proxy_check_jobs_status_created")

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

func TestProxyRepoCreateWithLogAndCheckJobPersistsDurableJobMySQL(t *testing.T) {
	db := newProxyMySQLTestDB(t)
	repo := NewProxyRepo(db)
	ctx := context.Background()
	proxy := &domain.Proxy{
		Pool:     domain.ProxyPoolResource,
		URL:      "http://127.0.0.1:23181",
		ExpireAt: time.Now().UTC().Add(time.Hour),
		Status:   domain.ProxyStatusChecking,
		Country:  "UNKNOWN",
	}

	job, err := repo.CreateWithLogAndCheckJob(ctx, proxy, &governancedomain.OperationLog{
		OperatorUserID: 1,
		OperationType:  "proxy.proxy.create",
		ResourceType:   "proxy",
		Path:           "/v1/admin/proxies/resource",
		Result:         "success",
		SafeSummary:    "Proxy created.",
		RequestID:      "durable-job-test",
	}, proxyapp.ProxyCheckTask{
		OperatorUserID: 1,
		RequestID:      "durable-job-test",
		Path:           "/v1/admin/proxies/resource",
	})

	require.NoError(t, err)
	require.NotZero(t, proxy.ID)
	require.NotNil(t, job)
	require.NotZero(t, job.ID)
	require.Equal(t, proxy.ID, job.ProxyID)
	require.Equal(t, proxyapp.ProxyCheckJobPending, job.Status)

	require.NoError(t, repo.MarkProxyCheckJobDispatchFailed(ctx, job.ID, "redis password=secret is unavailable"))
	var stored ProxyCheckJobModel
	require.NoError(t, db.First(&stored, "id = ?", job.ID).Error)
	require.Equal(t, string(proxyapp.ProxyCheckJobPending), stored.Status)
	require.NotContains(t, stored.LastSafeError, "password=secret")
	require.Contains(t, stored.LastSafeError, "password=***")

	queued, err := repo.MarkProxyCheckJobQueued(ctx, job.ID)
	require.NoError(t, err)
	require.True(t, queued)
	require.NoError(t, db.First(&stored, "id = ?", job.ID).Error)
	require.Equal(t, string(proxyapp.ProxyCheckJobQueued), stored.Status)
	require.Empty(t, stored.LastSafeError)

	running, err := repo.MarkProxyCheckJobRunning(ctx, job.ID)
	require.NoError(t, err)
	require.True(t, running)
	require.NoError(t, repo.MarkProxyCheckJobSucceeded(ctx, job.ID))
	running, err = repo.MarkProxyCheckJobRunning(ctx, job.ID)
	require.NoError(t, err)
	require.False(t, running)
	require.NoError(t, db.First(&stored, "id = ?", job.ID).Error)
	require.Equal(t, string(proxyapp.ProxyCheckJobSucceeded), stored.Status)
}

func TestProxyRepoCreateCheckBatchJobPersistsItemIDsMySQL(t *testing.T) {
	db := newProxyMySQLTestDB(t)
	repo := NewProxyRepo(db)
	ctx := context.Background()
	expireAt := time.Now().UTC().Add(time.Hour)

	first := &domain.Proxy{
		Pool:     domain.ProxyPoolSystem,
		URL:      "http://127.0.0.1:23182",
		ExpireAt: expireAt,
		Status:   domain.ProxyStatusChecking,
		Country:  "UNKNOWN",
	}
	second := &domain.Proxy{
		Pool:     domain.ProxyPoolSystem,
		URL:      "http://127.0.0.1:23183",
		ExpireAt: expireAt,
		Status:   domain.ProxyStatusChecking,
		Country:  "UNKNOWN",
	}
	require.NoError(t, repo.Create(ctx, first))
	require.NoError(t, repo.Create(ctx, second))

	job, err := repo.CreateCheckBatchJobWithLog(ctx, proxyapp.ProxyCheckBatchJobRequest{
		Mode:           proxyapp.ProxyCheckBatchModeIDs,
		ProxyIDs:       []uint{first.ID, second.ID, first.ID},
		OperatorUserID: 1,
		RequestID:      "durable-batch-job-test",
		Path:           "/v1/admin/proxies/check",
	}, &governancedomain.OperationLog{
		OperatorUserID: 1,
		OperationType:  "proxy.proxy.check_batch",
		ResourceType:   "proxy",
		Path:           "/v1/admin/proxies/check",
		Result:         "success",
		SafeSummary:    "Proxy batch check queued.",
		RequestID:      "durable-batch-job-test",
	})

	require.NoError(t, err)
	require.NotNil(t, job)
	require.Equal(t, proxyapp.ProxyCheckJobBatch, job.Kind)
	require.Equal(t, proxyapp.ProxyCheckBatchModeIDs, job.Mode)
	require.Equal(t, []uint{first.ID, second.ID}, job.ProxyIDs)

	firstPage, err := repo.ListProxyCheckJobItemIDs(ctx, job.ID, 0, 1)
	require.NoError(t, err)
	require.Equal(t, []uint{first.ID}, firstPage)
	secondPage, err := repo.ListProxyCheckJobItemIDs(ctx, job.ID, first.ID, 10)
	require.NoError(t, err)
	require.Equal(t, []uint{second.ID}, secondPage)

	pending, err := repo.ClaimDispatchableProxyCheckJobs(ctx, 10, time.Now().UTC().Add(-15*time.Minute))
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.Equal(t, proxyapp.ProxyCheckBatchModeIDs, pending[0].Mode)
	require.Equal(t, proxyapp.ProxyCheckJobQueued, pending[0].Status)
	require.Empty(t, pending[0].ProxyIDs)

	staleBefore := time.Now().UTC().Add(-15 * time.Minute)
	require.NoError(t, db.Model(&ProxyCheckJobModel{}).
		Where("id = ?", job.ID).
		Updates(map[string]any{
			"status":     string(proxyapp.ProxyCheckJobRunning),
			"updated_at": staleBefore.Add(-time.Minute),
		}).Error)
	dispatchable, err := repo.ClaimDispatchableProxyCheckJobs(ctx, 10, staleBefore)
	require.NoError(t, err)
	require.Len(t, dispatchable, 1)
	require.Equal(t, job.ID, dispatchable[0].ID)
	require.Equal(t, proxyapp.ProxyCheckJobQueued, dispatchable[0].Status)
	require.Empty(t, dispatchable[0].ProxyIDs)

	require.NoError(t, db.Model(&ProxyCheckJobModel{}).
		Where("id = ?", job.ID).
		Updates(map[string]any{
			"status":     string(proxyapp.ProxyCheckJobSucceeded),
			"updated_at": staleBefore.Add(-time.Hour),
		}).Error)
	dispatchable, err = repo.ClaimDispatchableProxyCheckJobs(ctx, 10, staleBefore)
	require.NoError(t, err)
	require.Empty(t, dispatchable)
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
