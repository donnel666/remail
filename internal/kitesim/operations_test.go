package kitesim

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

type testOperationQueue struct {
	accountIDs        []uint
	operationIDs      []uint64
	reconcileIDs      []uint64
	refreshes         int
	accountEnqueueErr error
}

func TestMoneyOperationIdempotencyReplaysOnlyTheSameCommand(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&accountModel{}, &upstreamSettingsModel{}, &productModel{}, &operationModel{}, &syncRunModel{}); err != nil {
		t.Fatal(err)
	}
	queue := &testOperationQueue{}
	service := NewService(db, queue)
	service.logs = testOperationLogs{}
	account := testAccount("owner@example.com", "password", "")
	if err := db.Create(&account).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&upstreamSettingsModel{ID: upstreamSettingsID, AccountID: &account.ID, Balance: "0"}).Error; err != nil {
		t.Fatal(err)
	}
	product := productModel{CountryCode: "CA", PackageID: "quarter", Currency: "USD", BuyPrice: "1", Active: true}
	if err := db.Create(&product).Error; err != nil {
		t.Fatal(err)
	}
	meta := MutationMeta{OperatorUserID: 7, IdempotencyKey: "purchase-1", RequestID: "request-1", Path: "/test"}

	first, err := service.QueuePurchase(context.Background(), product.ID, 1, "1.00", meta)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := service.QueuePurchase(context.Background(), product.ID, 1, "1", meta)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != replayed.ID || len(queue.operationIDs) != 1 {
		t.Fatalf("first=%d replay=%d queued=%v", first.ID, replayed.ID, queue.operationIDs)
	}
	if _, err := service.QueuePurchase(context.Background(), product.ID, 2, "1", meta); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("conflicting replay error = %v", err)
	}
}

func TestDispatcherRecoversDurableQueuedAndExpiresRechargeSecrets(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&accountModel{}, &upstreamSettingsModel{}, &operationModel{}, &syncRunModel{}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.August, 16, 12, 0, 0, 0, time.UTC)
	started := now.Add(-operationTaskTimeout - operationSettlementGrace - time.Second)
	queued := operationModel{
		Kind: string(OperationPurchase), AccountID: 1, RequestedCount: 1, Amount: "1",
		Status: string(OperationQueued), OperatorUserID: 1, IdempotencyKey: "queued-1",
		RequestFingerprint: strings.Repeat("a", 64), QueuedAt: now.Add(-time.Minute),
	}
	stale := operationModel{
		Kind: string(OperationRecharge), AccountID: 2, RequestedCount: 1, Amount: "10",
		Status: string(OperationRunning), OperatorUserID: 1, IdempotencyKey: "running-1",
		RequestFingerprint: strings.Repeat("b", 64), QueuedAt: started, StartedAt: &started,
		SecretPayload: jsonText(`{"cvc":"123"}`),
	}
	expiredRecharge := operationModel{
		Kind: string(OperationRecharge), AccountID: 3, RequestedCount: 1, Amount: "10",
		Status: string(OperationQueued), OperatorUserID: 1, IdempotencyKey: "expired-recharge-1",
		RequestFingerprint: strings.Repeat("c", 64), QueuedAt: now.Add(-queuedRechargeSecretTTL - time.Second),
		SecretPayload: jsonText(`{"cvc":"456"}`),
	}
	account := accountModel{Account: "owner@example.com", SyncStatus: string(SyncTaskQueued), SyncQueuedAt: &now}
	if err := db.Create(&account).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&upstreamSettingsModel{
		ID: upstreamSettingsID, RefreshStatus: string(SyncTaskQueued), RefreshQueued: &now, RefreshAttempts: 4,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&queued).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&stale).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&expiredRecharge).Error; err != nil {
		t.Fatal(err)
	}
	queue := &testOperationQueue{}
	service := NewService(db, queue)
	service.now = func() time.Time { return now }

	if err := service.DispatchQueuedOperations(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(queue.accountIDs) != 1 || queue.accountIDs[0] != account.ID || queue.refreshes != 0 ||
		len(queue.operationIDs) != 1 || queue.operationIDs[0] != queued.ID ||
		len(queue.reconcileIDs) != 1 || queue.reconcileIDs[0] != stale.ID {
		t.Fatalf("sync queue=%v refreshes=%d operation queue=%v reconcile queue=%v", queue.accountIDs, queue.refreshes, queue.operationIDs, queue.reconcileIDs)
	}
	if err := db.First(&stale, stale.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stale.Status != string(OperationUncertain) || len(stale.SecretPayload) != 0 || stale.ReconcileRequestedAt == nil {
		t.Fatalf("stale operation was not recovered safely: %+v", stale)
	}
	if err := db.First(&expiredRecharge, expiredRecharge.ID).Error; err != nil {
		t.Fatal(err)
	}
	if expiredRecharge.Status != string(OperationFailed) || len(expiredRecharge.SecretPayload) != 0 ||
		expiredRecharge.FinishedAt == nil || !strings.Contains(expiredRecharge.LastSafeError, "CVC") {
		t.Fatalf("expired recharge secret was not cleared: %+v", expiredRecharge)
	}
	var settings upstreamSettingsModel
	if err := db.First(&settings, upstreamSettingsID).Error; err != nil {
		t.Fatal(err)
	}
	if SyncTaskStatus(settings.RefreshStatus) != SyncTaskIdle || settings.RefreshAttempts != 0 || !strings.Contains(settings.LastSafeError, "已停止") {
		t.Fatalf("orphaned refresh was not stopped: %+v", settings)
	}
}

func TestDispatcherSchedulesFollowUpAfterConcurrentReadRefresh(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&accountModel{}, &upstreamSettingsModel{}, &operationModel{}, &syncRunModel{}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.August, 16, 12, 0, 0, 0, time.UTC)
	readStarted := now.Add(-2 * time.Minute)
	operationFinished := now.Add(-time.Minute)
	account := accountModel{
		Account: "owner@example.com", SyncStatus: string(SyncTaskSucceeded),
		SyncStartedAt: &readStarted, SyncAttempts: 4, LastSyncedAt: &now,
	}
	if err := db.Create(&account).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&upstreamSettingsModel{
		ID: upstreamSettingsID, AccountID: &account.ID, RefreshStatus: string(SyncTaskSucceeded),
		RefreshStarted: &readStarted, RefreshFinished: &now, RefreshAttempts: 3,
	}).Error; err != nil {
		t.Fatal(err)
	}
	operation := operationModel{
		Kind: string(OperationPurchase), AccountID: account.ID, RequestedCount: 1, CompletedCount: 1,
		Amount: "1", Status: string(OperationSucceeded), OperatorUserID: 1,
		IdempotencyKey: "completed-1", RequestFingerprint: strings.Repeat("c", 64),
		QueuedAt: readStarted, FinishedAt: &operationFinished,
	}
	if err := db.Create(&operation).Error; err != nil {
		t.Fatal(err)
	}
	queue := &testOperationQueue{}
	service := NewService(db, queue)
	service.now = func() time.Time { return now }

	if err := service.DispatchQueuedOperations(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(queue.accountIDs) != 1 || queue.accountIDs[0] != account.ID || queue.refreshes != 1 {
		t.Fatalf("post-operation refresh queue = accounts %v, refreshes %d", queue.accountIDs, queue.refreshes)
	}
	if err := db.First(&account, account.ID).Error; err != nil {
		t.Fatal(err)
	}
	var settings upstreamSettingsModel
	if err := db.First(&settings, upstreamSettingsID).Error; err != nil {
		t.Fatal(err)
	}
	if account.SyncAttempts != 0 || settings.RefreshAttempts != 0 {
		t.Fatalf("new post-operation tasks kept old attempts: account=%d refresh=%d", account.SyncAttempts, settings.RefreshAttempts)
	}
}

func TestOperationCreationRejectsStaleLifecycleState(t *testing.T) {
	meta := MutationMeta{OperatorUserID: 7, IdempotencyKey: "stale-state", Path: "/test"}
	newService := func(t *testing.T) (*gorm.DB, *Service, accountModel) {
		t.Helper()
		db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
		if err != nil {
			t.Fatal(err)
		}
		if err := db.AutoMigrate(&accountModel{}, &phoneModel{}, &upstreamSettingsModel{}, &operationModel{}); err != nil {
			t.Fatal(err)
		}
		account := testAccount("owner@example.com", "password", "")
		if err := db.Create(&account).Error; err != nil {
			t.Fatal(err)
		}
		service := NewService(db, &testOperationQueue{})
		service.logs = testOperationLogs{}
		return db, service, account
	}

	t.Run("deleted account", func(t *testing.T) {
		db, service, account := newService(t)
		now := time.Now().UTC()
		if err := db.Model(&accountModel{}).Where("id = ?", account.ID).Update("deleted_at", now).Error; err != nil {
			t.Fatal(err)
		}
		operation := operationModel{
			Kind: string(OperationPurchase), AccountID: account.ID, RequestedCount: 1,
			RequestFingerprint: strings.Repeat("a", 64),
		}
		if _, err := service.createAndEnqueueOperation(context.Background(), operation, meta); !errors.Is(err, ErrAccountMissing) {
			t.Fatalf("deleted account operation error = %v", err)
		}
	})

	t.Run("disabled renewal phone", func(t *testing.T) {
		db, service, account := newService(t)
		now := time.Now().UTC()
		phone := phoneModel{
			AccountID: account.ID, ProviderOrderID: "order-1", PhoneNumber: "14165550001",
			Status: int(PhoneActive), DisabledAt: &now,
		}
		if err := db.Create(&phone).Error; err != nil {
			t.Fatal(err)
		}
		operation := operationModel{
			Kind: string(OperationRenew), AccountID: account.ID, PhoneID: &phone.ID, RequestedCount: 1,
			RequestFingerprint: strings.Repeat("b", 64),
		}
		if _, err := service.createAndEnqueueOperation(context.Background(), operation, meta); !errors.Is(err, ErrPhoneMissing) {
			t.Fatalf("disabled phone operation error = %v", err)
		}
	})

	t.Run("changed system account", func(t *testing.T) {
		db, service, account := newService(t)
		other := testAccount("other@example.com", "password", "")
		if err := db.Create(&other).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&upstreamSettingsModel{ID: upstreamSettingsID, AccountID: &other.ID}).Error; err != nil {
			t.Fatal(err)
		}
		operation := operationModel{
			Kind: string(OperationPurchase), AccountID: account.ID, RequestedCount: 1,
			RequestFingerprint: strings.Repeat("c", 64),
		}
		if _, err := service.createAndEnqueueOperation(context.Background(), operation, meta); !errors.Is(err, ErrUpstreamNotConfigured) {
			t.Fatalf("changed upstream operation error = %v", err)
		}
	})
}

func TestDispatcherClearsExpiredRechargeBeforeQueueFailure(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&accountModel{}, &upstreamSettingsModel{}, &operationModel{}, &syncRunModel{}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.August, 16, 12, 0, 0, 0, time.UTC)
	account := accountModel{Account: "owner@example.com", SyncStatus: string(SyncTaskQueued), SyncQueuedAt: &now}
	if err := db.Create(&account).Error; err != nil {
		t.Fatal(err)
	}
	operation := operationModel{
		Kind: string(OperationRecharge), AccountID: account.ID, RequestedCount: 1, Amount: "10",
		Status: string(OperationQueued), OperatorUserID: 1, IdempotencyKey: "expired-before-redis-error",
		RequestFingerprint: strings.Repeat("d", 64), QueuedAt: now.Add(-queuedRechargeSecretTTL - time.Second),
		SecretPayload: jsonText(`{"cvc":"123"}`),
	}
	if err := db.Create(&operation).Error; err != nil {
		t.Fatal(err)
	}
	queueErr := errors.New("redis unavailable")
	service := NewService(db, &testOperationQueue{accountEnqueueErr: queueErr})
	service.now = func() time.Time { return now }

	if err := service.DispatchQueuedOperations(context.Background()); !errors.Is(err, queueErr) {
		t.Fatalf("dispatcher error = %v", err)
	}
	if err := db.First(&operation, operation.ID).Error; err != nil {
		t.Fatal(err)
	}
	if operation.Status != string(OperationFailed) || len(operation.SecretPayload) != 0 || operation.FinishedAt == nil {
		t.Fatalf("expired recharge survived queue failure: %+v", operation)
	}
}

func TestRenewalPriceUsesBuyPrice(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&accountModel{}, &phoneModel{}, &productModel{}, &operationModel{}); err != nil {
		t.Fatal(err)
	}
	account := testAccount("owner@example.com", "password", "")
	if err := db.Create(&account).Error; err != nil {
		t.Fatal(err)
	}
	phone := phoneModel{AccountID: account.ID, CountryCode: "CA", Status: int(PhoneActive)}
	product := productModel{CountryCode: "CA", PackageID: "quarter", Currency: "USD", BuyPrice: "7.80", AutoRenewPrice: "1.25", Active: true}
	if err := db.Create(&phone).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&product).Error; err != nil {
		t.Fatal(err)
	}
	service := NewService(db, &testOperationQueue{})
	service.logs = testOperationLogs{}
	meta := MutationMeta{OperatorUserID: 7, IdempotencyKey: "renew-1", Path: "/test"}
	if _, err := service.QueueRenewal(context.Background(), phone.ID, product.ID, "1.25", meta); !errors.Is(err, ErrPriceChanged) {
		t.Fatalf("renewal accepted auto-renew price as manual price: %v", err)
	}
	meta.IdempotencyKey = "renew-2"
	if _, err := service.QueueRenewal(context.Background(), phone.ID, product.ID, "7.80", meta); err != nil {
		t.Fatalf("renewal rejected buy price: %v", err)
	}
}

func TestRecordOperationFailureRequestsReconciliation(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&operationModel{}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.August, 16, 12, 0, 0, 123000000, time.UTC)
	service := NewService(db, &testOperationQueue{})
	service.now = func() time.Time { return now }

	for _, status := range []OperationStatus{OperationUncertain, OperationRequiresAction} {
		t.Run(string(status), func(t *testing.T) {
			operation := operationModel{
				Kind: string(OperationRecharge), AccountID: 1, RequestedCount: 1, Amount: "10",
				Status: string(OperationRunning), OperatorUserID: 1, IdempotencyKey: string(status),
				RequestFingerprint: strings.Repeat("a", 64), QueuedAt: now,
			}
			if err := db.Create(&operation).Error; err != nil {
				t.Fatal(err)
			}
			if err := service.recordOperationFailure(context.Background(), operation.ID, status, "upstream result unknown", 0); err != nil {
				t.Fatal(err)
			}
			if err := db.First(&operation, operation.ID).Error; err != nil {
				t.Fatal(err)
			}
			if operation.ReconcileRequestedAt == nil || !operation.ReconcileRequestedAt.Equal(now) {
				t.Fatalf("reconcile requested at = %v, want %v", operation.ReconcileRequestedAt, now)
			}
		})
	}
}

func (q *testOperationQueue) Enqueue(_ context.Context, accountID uint) (bool, error) {
	q.accountIDs = append(q.accountIDs, accountID)
	return q.accountEnqueueErr == nil, q.accountEnqueueErr
}

func (q *testOperationQueue) EnqueueUpstreamRefresh(context.Context) (bool, error) {
	q.refreshes++
	return true, nil
}

func (q *testOperationQueue) EnqueueOperation(_ context.Context, operationID uint64) (bool, error) {
	q.operationIDs = append(q.operationIDs, operationID)
	return true, nil
}

func (q *testOperationQueue) EnqueueOperationReconcile(_ context.Context, operationID uint64) (bool, error) {
	q.reconcileIDs = append(q.reconcileIDs, operationID)
	return true, nil
}

func TestRechargeCVCIsStoredPlainThenClearedOnClaim(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&accountModel{}, &upstreamSettingsModel{}, &operationModel{}); err != nil {
		t.Fatal(err)
	}
	queue := &testOperationQueue{}
	service := NewService(db, queue)
	account := testAccount("owner@example.com", "password", "")
	if err := db.Create(&account).Error; err != nil {
		t.Fatal(err)
	}
	service.logs = testOperationLogs{}
	if err := db.Create(&upstreamSettingsModel{
		ID: upstreamSettingsID, AccountID: &account.ID, CardData: jsonText(`{"number":"4111111111111111"}`),
		Balance: "0", RefreshStatus: string(SyncTaskIdle),
	}).Error; err != nil {
		t.Fatal(err)
	}

	operation, err := service.QueueRecharge(context.Background(), "10", "123", MutationMeta{
		OperatorUserID: 1, IdempotencyKey: "recharge-1", RequestID: "request-1", Path: "/test",
	})
	if err != nil {
		t.Fatal(err)
	}
	var stored operationModel
	if err := db.First(&stored, operation.ID).Error; err != nil {
		t.Fatal(err)
	}
	if len(stored.SecretPayload) == 0 || !bytes.Contains([]byte(stored.SecretPayload), []byte("123")) {
		t.Fatal("CVC was not stored as plain JSON")
	}
	claimed, secret, err := service.claimOperation(context.Background(), operation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if claimed.Status != string(OperationRunning) || len(secret) == 0 {
		t.Fatalf("unexpected claimed operation: %+v", claimed)
	}
	if err := db.First(&stored, operation.ID).Error; err != nil {
		t.Fatal(err)
	}
	if len(stored.SecretPayload) != 0 {
		t.Fatal("CVC payload was not cleared when the worker claimed the task")
	}
}

func TestExpiredRechargeFailsAndClearsSecretOnClaim(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&operationModel{}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.August, 16, 12, 0, 0, 0, time.UTC)
	operation := operationModel{
		Kind: string(OperationRecharge), AccountID: 1, RequestedCount: 1, Amount: "10",
		Status: string(OperationQueued), OperatorUserID: 1, IdempotencyKey: "expired-on-claim",
		RequestFingerprint: strings.Repeat("e", 64), QueuedAt: now.Add(-queuedRechargeSecretTTL),
		SecretPayload: jsonText(`{"cvc":"123"}`),
	}
	if err := db.Create(&operation).Error; err != nil {
		t.Fatal(err)
	}
	service := NewService(db, nil)
	service.now = func() time.Time { return now }

	claimed, secret, err := service.claimOperation(context.Background(), operation.ID)
	if !errors.Is(err, errOperationExpired) || claimed.ID != 0 || len(secret) != 0 {
		t.Fatalf("claim = %+v, secret=%q, err=%v", claimed, secret, err)
	}
	if err := db.First(&operation, operation.ID).Error; err != nil {
		t.Fatal(err)
	}
	if operation.Status != string(OperationFailed) || len(operation.SecretPayload) != 0 ||
		operation.FinishedAt == nil || operation.LastSafeError != expiredRechargeSafeError {
		t.Fatalf("expired recharge was not failed safely: %+v", operation)
	}
}

func TestOperationViewUsesEmptyProviderOrderArray(t *testing.T) {
	item := operationView(operationModel{}, "", "")
	if item.ProviderOrderNos == nil {
		t.Fatal("providerOrderNos is nil")
	}
	encoded, err := json.Marshal(item)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(encoded, []byte(`"providerOrderNos":[]`)) {
		t.Fatalf("operation JSON = %s", encoded)
	}
}

func TestDuplicateOperationErrorRecognizesTranslatedGORMError(t *testing.T) {
	if !duplicateOperationError(gorm.ErrDuplicatedKey) {
		t.Fatal("gorm.ErrDuplicatedKey was not recognized")
	}
}
