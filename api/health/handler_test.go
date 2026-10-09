package health

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/donnel666/remail/api/middleware"
	"github.com/donnel666/remail/internal/iam/domain"
	"github.com/donnel666/remail/internal/platform"
	_ "github.com/glebarez/go-sqlite"
	"github.com/minio/minio-go/v7"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func TestHealthz(t *testing.T) {
	gin.SetMode(gin.TestMode)

	r := gin.New()
	h := &Handler{} // platform not needed for liveness check
	r.GET("/healthz", h.Healthz)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/healthz", nil)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.JSONEq(t, `{"status":"ok"}`, w.Body.String())
}

func TestMonitoringRoutesRequireSuperAdminSession(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, path := range []string{"/metrics", "/readyz"} {
		for _, tc := range []struct {
			name   string
			cookie string
			role   domain.Role
			valid  bool
			status int
		}{
			{"anonymous", "", "", false, http.StatusUnauthorized},
			{"expired", "expired", domain.RoleSuperAdmin, false, http.StatusUnauthorized},
			{"user", "valid", domain.RoleUser, true, http.StatusForbidden},
			{"supplier", "valid", domain.RoleSupplier, true, http.StatusForbidden},
			{"admin", "valid", domain.RoleAdmin, true, http.StatusForbidden},
			{"super admin", "valid", domain.RoleSuperAdmin, true, http.StatusOK},
		} {
			t.Run(path+"/"+tc.name, func(t *testing.T) {
				r := gin.New()
				fetcher := middleware.SessionFetcherFunc(func(_ context.Context, sid string) (uint, domain.Role, string, bool) {
					assert.Equal(t, tc.cookie, sid)
					return 1, tc.role, "user@example.test", tc.valid
				})
				var p *platform.Platform
				if path == "/readyz" && tc.status == http.StatusOK {
					p = healthyPlatform(t)
				}
				RegisterMonitoringRoutes(r, p, fetcher)
				req := httptest.NewRequest(http.MethodGet, path, nil)
				if tc.cookie != "" {
					req.AddCookie(&http.Cookie{Name: middleware.SessionCookieName, Value: tc.cookie})
				}
				req.Header.Set("X-Forwarded-For", "127.0.0.1")
				w := httptest.NewRecorder()
				r.ServeHTTP(w, req)
				assert.Equal(t, tc.status, w.Code)
				if tc.status != http.StatusOK {
					assert.NotContains(t, w.Body.String(), "dependencies")
					assert.NotContains(t, w.Body.String(), "go_info")
				}
			})
		}
	}
}

func healthyPlatform(t *testing.T) *platform.Platform {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	redisServer := miniredis.RunT(t)
	redisClient := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	t.Cleanup(func() { _ = redisClient.Close() })
	minioServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(`<ListAllMyBucketsResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Owner><ID>test</ID></Owner><Buckets></Buckets></ListAllMyBucketsResult>`))
	}))
	t.Cleanup(minioServer.Close)
	minioClient, err := minio.New(strings.TrimPrefix(minioServer.URL, "http://"), &minio.Options{Region: "us-east-1"})
	require.NoError(t, err)
	p := &platform.Platform{SQLDB: db, Redis: redisClient, MinIO: minioClient}
	p.MarkWorkersReady()
	return p
}

func TestReadinessProbePreservesChecksWithoutDisclosingDetails(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, failed := range []string{"none", "mysql", "redis", "minio", "workers"} {
		t.Run(failed, func(t *testing.T) {
			p := healthyPlatform(t)
			switch failed {
			case "mysql":
				require.NoError(t, p.SQLDB.Close())
			case "redis":
				require.NoError(t, p.Redis.Close())
			case "minio":
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusForbidden) }))
				defer server.Close()
				client, err := minio.New(strings.TrimPrefix(server.URL, "http://"), &minio.Options{Region: "us-east-1"})
				require.NoError(t, err)
				p.MinIO = client
			case "workers":
				p = &platform.Platform{SQLDB: p.SQLDB, Redis: p.Redis, MinIO: p.MinIO}
			}
			r := gin.New()
			r.GET("/healthz", NewHandler(p).Healthz)
			RegisterMonitoringRoutes(r, p, middleware.SessionFetcherFunc(func(context.Context, string) (uint, domain.Role, string, bool) {
				return 1, domain.RoleSuperAdmin, "admin@example.test", true
			}))
			status, body := http.StatusOK, `{"status":"ok"}`
			if failed != "none" {
				status, body = http.StatusServiceUnavailable, `{"status":"error"}`
			}
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/healthz?ready=1", nil))
			assert.Equal(t, status, w.Code)
			assert.JSONEq(t, body, w.Body.String())
			req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
			req.AddCookie(&http.Cookie{Name: middleware.SessionCookieName, Value: "valid"})
			w = httptest.NewRecorder()
			r.ServeHTTP(w, req)
			assert.Equal(t, status, w.Code)
			assert.Contains(t, w.Body.String(), `"dependencies"`)
			if failed != "none" {
				assert.Contains(t, w.Body.String(), `"`+failed+`":"unhealthy"`)
			}
			w = httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/healthz", nil))
			assert.Equal(t, http.StatusOK, w.Code)
			assert.JSONEq(t, `{"status":"ok"}`, w.Body.String())
		})
	}
}

// Authentication storage failures must not execute monitoring handlers or leak dependency details.
type unavailableSessionFetcher struct{}

func (unavailableSessionFetcher) FetchSession(context.Context, string) (uint, domain.Role, string, bool, error) {
	return 0, "", "", false, errors.New("session storage unavailable canary")
}

func TestMonitoringRoutesFailClosedWhenSessionStorageIsUnavailable(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(middleware.RequestID())
	RegisterMonitoringRoutes(r, nil, unavailableSessionFetcher{})
	for _, path := range []string{"/readyz", "/metrics"} {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			req.AddCookie(&http.Cookie{Name: middleware.SessionCookieName, Value: "valid"})
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			require.Equal(t, http.StatusServiceUnavailable, w.Code)
			require.Contains(t, w.Body.String(), `"message":"Service is temporarily unavailable."`)
			require.Contains(t, w.Body.String(), `"requestId":`)
			require.NotContains(t, w.Body.String(), "dependencies")
			require.NotContains(t, w.Body.String(), "canary")
			require.NotContains(t, w.Body.String(), "go_info")
		})
	}
}
