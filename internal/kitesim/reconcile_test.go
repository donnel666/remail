package kitesim

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestManualResolutionIsTerminalAndIdempotent(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&accountModel{}, &phoneModel{}, &upstreamSettingsModel{}, &operationModel{}); err != nil {
		t.Fatal(err)
	}
	service := NewService(db, &testOperationQueue{})
	service.logs = testOperationLogs{}
	account := testAccount("owner@example.com", "password", "")
	if err := db.Create(&account).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.August, 16, 12, 0, 0, 0, time.UTC)
	operation := operationModel{
		Kind: string(OperationRecharge), AccountID: account.ID, RequestedCount: 1, Amount: "10",
		Status: string(OperationUncertain), OperatorUserID: 7, IdempotencyKey: "recharge-1",
		RequestFingerprint: strings.Repeat("a", 64), ReconcileAttempts: 1, QueuedAt: now, FinishedAt: &now,
	}
	if err := db.Create(&operation).Error; err != nil {
		t.Fatal(err)
	}
	meta := MutationMeta{OperatorUserID: 9, RequestID: "resolve-1", Path: "/test"}

	resolved, err := service.ResolveOperation(context.Background(), operation.ID, OperationFailed, "已核对银行卡账单", meta)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Status != OperationFailed || resolved.ResolutionSource != "manual" {
		t.Fatalf("resolved operation = %+v", resolved)
	}
	listed, err := service.listOperationViews(context.Background(), 20)
	if err != nil || len(listed) != 1 || listed[0].ID != operation.ID {
		t.Fatalf("listed operations = %+v err=%v", listed, err)
	}
	if _, err := service.ResolveOperation(context.Background(), operation.ID, OperationFailed, "重复提交不会改写终态", meta); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ResolveOperation(context.Background(), operation.ID, OperationSucceeded, "冲突结果", meta); !errors.Is(err, ErrOperationState) {
		t.Fatalf("terminal conflict error = %v", err)
	}
}

func TestUnresolvedReconcileSchedulesAnotherReadOnlyAttempt(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&operationModel{}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.August, 16, 12, 0, 0, 0, time.UTC)
	service := NewService(db, &testOperationQueue{})
	service.now = func() time.Time { return now }
	operation := operationModel{
		Kind: string(OperationRecharge), AccountID: 1, RequestedCount: 1, Amount: "10",
		Status: string(OperationUncertain), OperatorUserID: 7, IdempotencyKey: "recharge-retry",
		RequestFingerprint: strings.Repeat("b", 64), QueuedAt: now,
	}
	if err := db.Create(&operation).Error; err != nil {
		t.Fatal(err)
	}

	if err := service.finishReconcileAttempt(context.Background(), operation, "", 0, ErrPaymentUncertain); err != nil {
		t.Fatal(err)
	}
	if err := db.First(&operation, operation.ID).Error; err != nil {
		t.Fatal(err)
	}
	if operation.LastReconciledAt == nil || !operation.LastReconciledAt.Equal(now) ||
		operation.ReconcileRequestedAt == nil || !operation.ReconcileRequestedAt.Equal(now.Add(reconcileRetryInterval)) {
		t.Fatalf("reconcile retry was not scheduled: %+v", operation)
	}
}

func TestPurchaseReconcileClosesConfirmedPartialCompletion(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&accountModel{}, &phoneModel{}, &upstreamSettingsModel{}, &operationModel{}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/user/info":
			_ = json.NewEncoder(response).Encode(map[string]any{"code": 200, "data": map[string]any{"balance": "100"}})
		case "/userPhonePurchase/getOrderDetail":
			_ = json.NewEncoder(response).Encode(map[string]any{
				"code": 200,
				"data": map[string]any{"id": 1, "orderNo": request.URL.Query().Get("orderNo"), "paymentTime": "2026-08-16 12:00:00"},
			})
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	service := NewService(db, &testOperationQueue{})
	service.logs = testOperationLogs{}
	service.client = NewClient(server.Client())
	service.client.BaseURL = server.URL
	account := testAccount("owner@example.com", "password", "token")
	if err := db.Create(&account).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&upstreamSettingsModel{ID: upstreamSettingsID, AccountID: &account.ID, Balance: "0"}).Error; err != nil {
		t.Fatal(err)
	}
	refs, err := json.Marshal(operationProviderRefs{OrderNos: []string{"ORDER-1", "ORDER-2"}})
	if err != nil {
		t.Fatal(err)
	}
	operation := operationModel{
		Kind: string(OperationPurchase), AccountID: account.ID, RequestedCount: 3, CompletedCount: 1,
		Amount: "1", Status: string(OperationUncertain), ProviderOrderNos: refs,
		OperatorUserID: 7, IdempotencyKey: "purchase-partial", RequestFingerprint: strings.Repeat("a", 64),
		QueuedAt: time.Now(),
	}
	if err := db.Create(&operation).Error; err != nil {
		t.Fatal(err)
	}

	if err := service.processOperationReconcile(context.Background(), operation.ID); err != nil {
		t.Fatal(err)
	}
	if err := db.First(&operation, operation.ID).Error; err != nil {
		t.Fatal(err)
	}
	if operation.Status != string(OperationFailed) || operation.CompletedCount != 2 || operation.ResolutionSource != "query" {
		t.Fatalf("partial purchase reconciliation = %+v", operation)
	}
	if !strings.Contains(operation.LastSafeError, "2/3") {
		t.Fatalf("partial purchase reconciliation message = %q", operation.LastSafeError)
	}
}
