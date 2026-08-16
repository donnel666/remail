package kitesim

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/donnel666/remail/api/middleware"
	iamdomain "github.com/donnel666/remail/internal/iam/domain"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

type denyPermissionChecker struct{}

func (denyPermissionChecker) Check(context.Context, uint, iamdomain.Role, string, string) (bool, error) {
	return false, nil
}

func TestHandlerStatusesMatchOpenAPI(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&accountModel{}, &phoneModel{}); err != nil {
		t.Fatal(err)
	}
	queue := &testSyncQueue{}
	service := NewService(db, queue)
	service.logs = testOperationLogs{}
	h := &handler{service: service}

	listRecorder := httptest.NewRecorder()
	listContext, _ := gin.CreateTestContext(listRecorder)
	listContext.Request = httptest.NewRequest(http.MethodGet, "/v1/admin/kitesim/phones", nil)
	h.listPhones(listContext)
	if listRecorder.Code != http.StatusOK {
		t.Fatalf("list status = %d, want %d", listRecorder.Code, http.StatusOK)
	}

	importRecorder := httptest.NewRecorder()
	importContext, _ := gin.CreateTestContext(importRecorder)
	importContext.Request = httptest.NewRequest(
		http.MethodPost,
		"/v1/admin/kitesim/accounts/imports",
		bytes.NewBufferString(`{"content":"owner@example.com----password1"}`),
	)
	importContext.Request.Header.Set("Content-Type", "application/json")
	middleware.SetCurrentUser(importContext, 1, "super_admin", "admin@example.com", "session")
	h.importAccounts(importContext)
	if importRecorder.Code != http.StatusAccepted {
		t.Fatalf("import status = %d, want %d", importRecorder.Code, http.StatusAccepted)
	}
}

func TestCardSettingsRequireSensitivePermission(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPut, "/v1/admin/kitesim/upstream", bytes.NewBufferString(`{
		"accountId":1,
		"card":{"number":"4111111111111111","expiryMonth":8,"expiryYear":2030,"holder":"Test User","billingEmail":"owner@example.com","firstName":"Test","lastName":"User","phone":"6505438765","country":"US","city":"Mountain View","address":"1295 Charleston Rd"}
	}`))
	c.Request.Header.Set("Content-Type", "application/json")
	middleware.SetCurrentUser(c, 7, iamdomain.RoleAdmin, "admin@example.com", "session")

	(&handler{checker: denyPermissionChecker{}}).putUpstream(c)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusForbidden)
	}
}
