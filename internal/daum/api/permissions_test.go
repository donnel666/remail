package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/donnel666/remail/api/middleware"
	"github.com/donnel666/remail/internal/daum/infra"
	iamdomain "github.com/donnel666/remail/internal/iam/domain"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type daumPermissionCheckerFunc func(context.Context, uint, iamdomain.Role, string, string) (bool, error)

func (f daumPermissionCheckerFunc) Check(ctx context.Context, userID uint, role iamdomain.Role, resource, action string) (bool, error) {
	return f(ctx, userID, role, resource, action)
}

func daumPermissionRouter(role iamdomain.Role, checker middleware.PermissionChecker) *gin.Engine {
	r := gin.New()
	module := NewModuleWithQueue(infra.NewService(nil), nil)
	fetcher := middleware.SessionFetcherFunc(func(context.Context, string) (uint, iamdomain.Role, string, bool) {
		return 7, role, "owner@example.test", role != ""
	})
	RegisterRoutes(r.Group("/v1"), module, fetcher, checker)
	return r
}

func daumPermissionRequest(route gin.RouteInfo) *http.Request {
	path := strings.NewReplacer(":resourceId", "1", ":importId", "1", ":taskId", "daum_bulk:1", ":command", "validate").Replace(route.Path)
	request := httptest.NewRequest(route.Method, path, strings.NewReader(`{}`))
	request.AddCookie(&http.Cookie{Name: middleware.SessionCookieName, Value: "session"})
	request.AddCookie(&http.Cookie{Name: middleware.CSRFCookieName, Value: "csrf"})
	request.Header.Set(middleware.CSRFHeaderName, "csrf")
	return request
}

func TestDaumManagementRoutesRejectNonAdministrators(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, role := range []iamdomain.Role{"", iamdomain.RoleUser, iamdomain.RoleSupplier} {
		t.Run(string(role), func(t *testing.T) {
			// Even a direct per-user permission grant cannot reopen administrator-only management.
			r := daumPermissionRouter(role, allowDaumPermission{})
			for _, route := range r.Routes() {
				if !strings.HasPrefix(route.Path, "/v1/admin/") {
					continue
				}
				t.Run(route.Method+route.Path, func(t *testing.T) {
					recorder := httptest.NewRecorder()
					r.ServeHTTP(recorder, daumPermissionRequest(route))
					want := http.StatusForbidden
					if role == "" {
						want = http.StatusUnauthorized
					}
					require.Equal(t, want, recorder.Code, recorder.Body.String())
				})
			}
		})
	}
}

func TestDaumManagementRoutesKeepActionPermissions(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, role := range []iamdomain.Role{iamdomain.RoleAdmin, iamdomain.RoleSuperAdmin} {
		t.Run(string(role), func(t *testing.T) {
			var checked []string
			r := daumPermissionRouter(role, daumPermissionCheckerFunc(func(_ context.Context, userID uint, actualRole iamdomain.Role, resource, action string) (bool, error) {
				require.Equal(t, uint(7), userID)
				require.Equal(t, role, actualRole)
				require.Equal(t, "core:resource", resource)
				checked = append(checked, action)
				return false, nil
			}))
			for _, route := range r.Routes() {
				if !strings.HasPrefix(route.Path, "/v1/admin/") {
					continue
				}
				t.Run(route.Method+route.Path, func(t *testing.T) {
					checked = nil
					recorder := httptest.NewRecorder()
					r.ServeHTTP(recorder, daumPermissionRequest(route))
					require.Equal(t, http.StatusForbidden, recorder.Code, recorder.Body.String())
					action := "operate"
					if route.Method == http.MethodGet {
						action = "read"
					} else if route.Method == http.MethodPatch || strings.HasSuffix(route.Path, "/imports") {
						action = "write"
					}
					require.Equal(t, []string{action}, checked)
				})
			}
		})
	}
}

func TestDaumManagementGuardsDoNotRestrictOtherRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	v1 := r.Group("/v1")
	v1.Use(func(c *gin.Context) {
		middleware.SetCurrentUser(c, 7, iamdomain.RoleUser, "buyer@example.test", "session")
		c.Next()
	})
	module := NewModuleWithQueue(infra.NewService(nil), nil)
	RegisterRoutes(v1, module, nil, allowDaumPermission{})
	open := v1.Group("/open")
	for _, group := range []*gin.RouterGroup{v1, open} {
		for _, path := range []string{"/projects", "/orders", "/wallet"} {
			group.GET(path, func(c *gin.Context) { c.Status(http.StatusNoContent) })
		}
		group.POST("/orders", func(c *gin.Context) { c.Status(http.StatusCreated) })
	}
	for _, route := range r.Routes() {
		if strings.Contains(route.Path, "/daum/") {
			continue
		}
		t.Run(route.Method+route.Path, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(route.Method, route.Path, strings.NewReader(`{"productType":"daum"}`))
			r.ServeHTTP(recorder, request)
			want := http.StatusNoContent
			if route.Method == http.MethodPost {
				want = http.StatusCreated
			}
			require.Equal(t, want, recorder.Code, recorder.Body.String())
		})
	}
}

type allowDaumPermission struct{}

func (allowDaumPermission) Check(context.Context, uint, iamdomain.Role, string, string) (bool, error) {
	return true, nil
}
