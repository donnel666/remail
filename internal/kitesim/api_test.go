package kitesim

import (
	"bytes"
	"context"
	"fmt"
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
	if err := db.AutoMigrate(&accountModel{}, &phoneModel{}, &upstreamSettingsModel{}, &operationModel{}, &syncRunModel{}); err != nil {
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
	var account accountModel
	if err := db.Where("account = ?", "owner@example.com").First(&account).Error; err != nil {
		t.Fatal(err)
	}
	tasksRecorder := httptest.NewRecorder()
	tasksContext, _ := gin.CreateTestContext(tasksRecorder)
	tasksContext.Request = httptest.NewRequest(http.MethodGet, "/v1/admin/kitesim/accounts/1/tasks?offset=0&limit=20", nil)
	tasksContext.Params = gin.Params{{Key: "accountId", Value: fmt.Sprint(account.ID)}}
	h.listAccountTasks(tasksContext)
	if tasksRecorder.Code != http.StatusOK || !bytes.Contains(tasksRecorder.Body.Bytes(), []byte(`"total":1`)) {
		t.Fatalf("task list status=%d body=%s", tasksRecorder.Code, tasksRecorder.Body.String())
	}
	phone := phoneModel{AccountID: account.ID, ProviderOrderID: "order-1", PhoneNumber: "14165550001"}
	if err := db.Create(&phone).Error; err != nil {
		t.Fatal(err)
	}

	disableRecorder := httptest.NewRecorder()
	disableContext, _ := gin.CreateTestContext(disableRecorder)
	disableContext.Request = httptest.NewRequest(
		http.MethodPost,
		"/v1/admin/kitesim/phones/disable",
		bytes.NewBufferString(fmt.Sprintf(`{"phoneIds":[%d]}`, phone.ID)),
	)
	disableContext.Request.Header.Set("Content-Type", "application/json")
	middleware.SetCurrentUser(disableContext, 1, "super_admin", "admin@example.com", "session")
	h.disablePhones(disableContext)
	if disableRecorder.Code != http.StatusOK {
		t.Fatalf("disable status = %d, want %d", disableRecorder.Code, http.StatusOK)
	}

	deleteRecorder := httptest.NewRecorder()
	deleteContext, _ := gin.CreateTestContext(deleteRecorder)
	deleteContext.Request = httptest.NewRequest(
		http.MethodPost,
		"/v1/admin/kitesim/phones/delete",
		bytes.NewBufferString(fmt.Sprintf(`{"phoneIds":[%d]}`, phone.ID)),
	)
	deleteContext.Request.Header.Set("Content-Type", "application/json")
	middleware.SetCurrentUser(deleteContext, 1, "super_admin", "admin@example.com", "session")
	h.deletePhones(deleteContext)
	if deleteRecorder.Code != http.StatusOK {
		t.Fatalf("delete status = %d, want %d", deleteRecorder.Code, http.StatusOK)
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
