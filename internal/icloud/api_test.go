package icloud

import (
	"bytes"
	"context"
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
		`{"version":1,"importLine":"main@icloud.com----app-password----curl 'https://appleid.apple.com/account/manage/' -H 'Cookie: secret' -H 'scnt: value'"}`,
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
	if operateChecks != 3 {
		t.Fatalf("operate checks = %d, want 3", operateChecks)
	}
}
