package icloud

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/donnel666/remail/api/middleware"
	coreinfra "github.com/donnel666/remail/internal/core/infra"
	governanceinfra "github.com/donnel666/remail/internal/governance/infra"
	iamdomain "github.com/donnel666/remail/internal/iam/domain"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

type iCloudPermissionCheckerFunc func(context.Context, uint, iamdomain.Role, string, string) (bool, error)

func (f iCloudPermissionCheckerFunc) Check(ctx context.Context, userID uint, role iamdomain.Role, resource, action string) (bool, error) {
	return f(ctx, userID, role, resource, action)
}

type recordingICloudPermissionChecker struct {
	allowed map[string]bool
	calls   []string
}

func (c *recordingICloudPermissionChecker) Check(_ context.Context, _ uint, _ iamdomain.Role, resource, action string) (bool, error) {
	key := resource + "/" + action
	c.calls = append(c.calls, key)
	return c.allowed[key], nil
}

func TestRequireICloudIdempotencyKey(t *testing.T) {
	for _, test := range []struct {
		name   string
		header string
		valid  bool
	}{
		{name: "missing"},
		{name: "blank", header: "   "},
		{name: "too long", header: strings.Repeat("x", 129)},
		{name: "valid", header: "icloud-command-1", valid: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/admin/icloud/resources/1/validation", nil)
			ctx.Request.Header.Set("Idempotency-Key", test.header)

			if got := requireICloudIdempotencyKey(ctx); got != test.valid {
				t.Fatalf("valid = %v, want %v", got, test.valid)
			}
			if !test.valid && recorder.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadRequest)
			}
		})
	}
}

func TestAdminICloudOnboardingRouteRequiresOperateAndTaskRead(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, test := range []struct {
		name    string
		allowed map[string]bool
		calls   []string
	}{
		{
			name:    "write is insufficient",
			allowed: map[string]bool{"core:resource/write": true},
			calls:   []string{"core:resource/operate"},
		},
		{
			name:    "task recovery permission is required",
			allowed: map[string]bool{"core:resource/operate": true},
			calls:   []string{"core:resource/operate", "governance:task/read"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			checker := &recordingICloudPermissionChecker{allowed: test.allowed}
			router := gin.New()
			RegisterRoutes(
				router.Group("/v1"),
				&Module{Service: NewService(nil, nil, nil)},
				middleware.SessionFetcherFunc(func(context.Context, string) (uint, iamdomain.Role, string, bool) {
					return 9, iamdomain.RoleAdmin, "admin@test.local", true
				}),
				checker,
			)

			request := httptest.NewRequest(http.MethodPost, "/v1/admin/icloud/resources/onboarding-imports", nil)
			request.Header.Set("Idempotency-Key", "onboarding-permission")
			request.AddCookie(&http.Cookie{Name: middleware.SessionCookieName, Value: "session"})
			request.AddCookie(&http.Cookie{Name: middleware.CSRFCookieName, Value: "csrf"})
			request.Header.Set(middleware.CSRFHeaderName, "csrf")
			response := httptest.NewRecorder()

			router.ServeHTTP(response, request)

			if response.Code != http.StatusForbidden {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			if strings.Join(checker.calls, ",") != strings.Join(test.calls, ",") {
				t.Fatalf("permission calls=%v want=%v", checker.calls, test.calls)
			}
		})
	}
}

func TestAdminICloudOnboardingReadAndActionsRequireTaskRead(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, test := range []struct {
		name       string
		method     string
		path       string
		coreAction string
		idempotent bool
	}{
		{name: "read import", method: http.MethodGet, path: "/v1/admin/icloud/resources/onboarding-imports/1", coreAction: "read"},
		{name: "submit sms", method: http.MethodPost, path: "/v1/admin/icloud/resources/onboarding-tasks/1/sms-code", coreAction: "operate"},
		{name: "confirm family reset", method: http.MethodPost, path: "/v1/admin/icloud/resources/onboarding-tasks/1/family-reset", coreAction: "operate", idempotent: true},
		{name: "confirm icloud activation", method: http.MethodPost, path: "/v1/admin/icloud/resources/onboarding-tasks/1/icloud-activation", coreAction: "operate", idempotent: true},
		{name: "retry post family", method: http.MethodPost, path: "/v1/admin/icloud/resources/onboarding-tasks/1/retry", coreAction: "operate", idempotent: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			checker := &recordingICloudPermissionChecker{allowed: map[string]bool{"core:resource/" + test.coreAction: true}}
			router := gin.New()
			RegisterRoutes(
				router.Group("/v1"), &Module{Service: NewService(nil, nil, nil)},
				middleware.SessionFetcherFunc(func(context.Context, string) (uint, iamdomain.Role, string, bool) {
					return 9, iamdomain.RoleAdmin, "admin@example.com", true
				}),
				checker,
			)
			request := httptest.NewRequest(test.method, test.path, nil)
			request.AddCookie(&http.Cookie{Name: middleware.SessionCookieName, Value: "session"})
			if test.method == http.MethodPost {
				request.AddCookie(&http.Cookie{Name: middleware.CSRFCookieName, Value: "csrf"})
				request.Header.Set(middleware.CSRFHeaderName, "csrf")
			}
			if test.idempotent {
				request.Header.Set("Idempotency-Key", "onboarding-task-read")
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)

			if response.Code != http.StatusForbidden {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			wantCalls := []string{"core:resource/" + test.coreAction, "governance:task/read"}
			if strings.Join(checker.calls, ",") != strings.Join(wantCalls, ",") {
				t.Fatalf("permission calls=%v want=%v", checker.calls, wantCalls)
			}
		})
	}
}

func TestRetryAdminICloudOnboardingPostFamily(t *testing.T) {
	service, db, task, _ := newOnboardingStateTest(t)
	primaryID := uint(88)
	if err := db.Model(task).Updates(map[string]any{
		"stage": "family_join_apply", "onboarding_status": iCloudOnboardingWaiting, "dispatch_status": "waiting",
		"family_primary_resource_id": primaryID, "attempts": 5, "max_attempts": 5,
		"session_payload":     []byte(`{"private":"api-secret-session"}`),
		"last_error_category": "provider_unavailable", "last_safe_error": "Apple family state is uncertain.",
	}).Error; err != nil {
		t.Fatal(err)
	}

	checker := &recordingICloudPermissionChecker{allowed: map[string]bool{
		"core:resource/operate": true,
		"governance:task/read":  true,
	}}
	router := gin.New()
	RegisterRoutes(
		router.Group("/v1"), &Module{Service: service},
		middleware.SessionFetcherFunc(func(context.Context, string) (uint, iamdomain.Role, string, bool) {
			return 9, iamdomain.RoleAdmin, "admin@example.com", true
		}),
		checker,
	)
	request := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/v1/admin/icloud/resources/onboarding-tasks/%d/retry", task.ID), nil)
	request.Header.Set("Idempotency-Key", "retry-post-family")
	request.AddCookie(&http.Cookie{Name: middleware.SessionCookieName, Value: "session"})
	request.AddCookie(&http.Cookie{Name: middleware.CSRFCookieName, Value: "csrf"})
	request.Header.Set(middleware.CSRFHeaderName, "csrf")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"stage":"family_join_apply"`) ||
		!strings.Contains(recorder.Body.String(), `"needsPostFamilyRecovery":false`) {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	for _, forbidden := range []string{"Secret1!", "api-secret-session", "secretPayload", "sessionPayload"} {
		if strings.Contains(recorder.Body.String(), forbidden) {
			t.Fatalf("response leaked %q: %s", forbidden, recorder.Body.String())
		}
	}
	if strings.Join(checker.calls, ",") != "core:resource/operate,governance:task/read" {
		t.Fatalf("permission calls=%v", checker.calls)
	}
}

func TestAdminICloudOnboardingRetryRouteGuards(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, test := range []struct {
		name           string
		allowed        bool
		csrf           bool
		idempotencyKey string
		wantCalls      int
		wantStatus     int
	}{
		{name: "csrf required", allowed: true, idempotencyKey: "retry", wantStatus: http.StatusForbidden},
		{name: "operate required", csrf: true, idempotencyKey: "retry", wantCalls: 1, wantStatus: http.StatusForbidden},
		{name: "idempotency key required", allowed: true, csrf: true, wantCalls: 1, wantStatus: http.StatusBadRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			checker := &recordingICloudPermissionChecker{allowed: map[string]bool{"core:resource/operate": test.allowed}}
			router := gin.New()
			RegisterRoutes(
				router.Group("/v1"), &Module{Service: NewService(nil, nil, nil)},
				middleware.SessionFetcherFunc(func(context.Context, string) (uint, iamdomain.Role, string, bool) {
					return 9, iamdomain.RoleAdmin, "admin@example.com", true
				}),
				checker,
			)
			request := httptest.NewRequest(http.MethodPost, "/v1/admin/icloud/resources/onboarding-tasks/1/retry", nil)
			request.AddCookie(&http.Cookie{Name: middleware.SessionCookieName, Value: "session"})
			request.AddCookie(&http.Cookie{Name: middleware.CSRFCookieName, Value: "csrf"})
			if test.csrf {
				request.Header.Set(middleware.CSRFHeaderName, "csrf")
			}
			if test.idempotencyKey != "" {
				request.Header.Set("Idempotency-Key", test.idempotencyKey)
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != test.wantStatus || len(checker.calls) != test.wantCalls {
				t.Fatalf("status=%d calls=%v body=%s", response.Code, checker.calls, response.Body.String())
			}
		})
	}
}

func TestCreateAdminICloudAliasesReturnsOKWhenTargetAlreadyReached(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:icloud-api-alias-full?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := db.AutoMigrate(
		&iCloudRootModel{}, &iCloudResourceModel{},
		&iCloudMaintenanceRunModel{},
		&governanceinfra.OperationLogModel{}, &coreinfra.AdminResourceCommandReceiptModel{},
	); err != nil {
		t.Fatalf("migrate database: %v", err)
	}
	now := time.Date(2026, 8, 8, 11, 0, 0, 0, time.UTC)
	if err := db.Create(&iCloudRootModel{ID: 1, Type: "icloud", OwnerUserID: 7, Version: 1, CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatalf("create root: %v", err)
	}
	if err := db.Create(&iCloudResourceModel{
		ID: 1, ResourceType: "icloud", PrimaryEmail: "full@icloud.com", Status: iCloudResourceNormal,
		AliasCount: iCloudMaxAliases, CredentialRevision: 1,
		ValidationGeneration: 1, ExpireAt: now.Add(time.Hour), CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("create resource: %v", err)
	}

	request := httptest.NewRequest(http.MethodPost, "/v1/admin/icloud/resources/1/aliases?version=1", nil)
	request.Header.Set("Idempotency-Key", "alias-full")
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = request
	c.Params = gin.Params{{Key: "resourceId", Value: "1"}}
	middleware.SetCurrentUser(c, 99, iamdomain.RoleAdmin, "admin@example.com", "session")
	h := &handler{service: NewService(db, nil, nil)}
	h.resourceCommand(AdminICloudAlias)(c)

	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"changed":false`) {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestPatchAdminICloudResourceRequiresOperateOnlyForSensitiveFields(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:icloud-api-edit-permission?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := db.AutoMigrate(
		&iCloudRootModel{}, &iCloudResourceModel{}, &iCloudAdminTestUser{},
		&iCloudMaintenanceRunModel{},
		&governanceinfra.OperationLogModel{}, &coreinfra.AdminResourceCommandReceiptModel{},
	); err != nil {
		t.Fatalf("migrate database: %v", err)
	}
	now := time.Date(2026, 8, 8, 11, 0, 0, 0, time.UTC)
	if err := db.Create(&iCloudAdminTestUser{ID: 7, Email: "owner@example.com", Status: "active", Role: "supplier"}).Error; err != nil {
		t.Fatalf("create owner: %v", err)
	}
	if err := db.Create(&iCloudRootModel{ID: 1, Type: "icloud", OwnerUserID: 7, Version: 1, CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatalf("create root: %v", err)
	}
	if err := db.Create(&iCloudResourceModel{
		ID: 1, ResourceType: "icloud", PrimaryEmail: "main@icloud.com", Status: iCloudResourceNormal,
		CredentialRevision: 1, ValidationGeneration: 1, ExpireAt: now.Add(time.Hour), CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("create resource: %v", err)
	}

	operateChecks := 0
	checker := iCloudPermissionCheckerFunc(func(_ context.Context, _ uint, _ iamdomain.Role, resource, action string) (bool, error) {
		if resource == "core:resource" && action == "operate" {
			operateChecks++
		}
		return false, nil
	})
	service := NewService(db, nil, nil)
	service.SetImportOwnerValidator(func(context.Context, uint) (bool, error) { return true, nil })
	h := &handler{service: service, checker: checker}

	request := httptest.NewRequest(http.MethodPatch, "/v1/admin/icloud/resources/1", bytes.NewBufferString(`{"version":1,"ownerId":7}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "safe-edit")
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = request
	c.Params = gin.Params{{Key: "resourceId", Value: "1"}}
	middleware.SetCurrentUser(c, 99, iamdomain.RoleAdmin, "admin@example.com", "session")
	h.patchResource(c)
	if recorder.Code != http.StatusOK || operateChecks != 0 {
		t.Fatalf("safe edit status=%d operate checks=%d body=%s", recorder.Code, operateChecks, recorder.Body.String())
	}

	for index, body := range []string{
		`{"version":1,"forSale":false}`,
		`{"version":1,"expireAt":"2026-09-08T11:00:00Z"}`,
		`{"version":1,"importLine":"main@icloud.com----curl 'https://appleid.apple.com/account/manage/' -H 'Cookie: secret' -H 'scnt: value'"}`,
		`{"version":1,"familyInviteUrl":"replacement-token"}`,
		`{"version":1,"phoneId":8,"phoneNumber":"+1 416 555 0002"}`,
	} {
		request = httptest.NewRequest(http.MethodPatch, "/v1/admin/icloud/resources/1", bytes.NewBufferString(body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Idempotency-Key", "sensitive-edit-"+string(rune('1'+index)))
		recorder = httptest.NewRecorder()
		c, _ = gin.CreateTestContext(recorder)
		c.Request = request
		c.Params = gin.Params{{Key: "resourceId", Value: "1"}}
		middleware.SetCurrentUser(c, 99, iamdomain.RoleAdmin, "admin@example.com", "session")
		h.patchResource(c)
		if recorder.Code != http.StatusForbidden {
			t.Fatalf("sensitive edit %d status=%d body=%s", index, recorder.Code, recorder.Body.String())
		}
	}
	if operateChecks != 5 {
		t.Fatalf("operate checks = %d, want 5", operateChecks)
	}
}
