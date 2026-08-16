package kitesim

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

type testOperationLogs struct{}

func (testOperationLogs) Create(context.Context, *governancedomain.OperationLog) error { return nil }

type testSyncQueue struct{ accountIDs []uint }

func (q *testSyncQueue) Enqueue(_ context.Context, accountID uint) (bool, error) {
	q.accountIDs = append(q.accountIDs, accountID)
	return true, nil
}

type persistedSyncQueue struct {
	db        *gorm.DB
	sawQueued bool
}

func (q *persistedSyncQueue) Enqueue(_ context.Context, accountID uint) (bool, error) {
	var account accountModel
	if err := q.db.Select("sync_status").First(&account, accountID).Error; err != nil {
		return false, err
	}
	q.sawQueued = SyncTaskStatus(account.SyncStatus) == SyncTaskQueued
	return false, errors.New("redis unavailable")
}

func testAccount(account, password, token string) accountModel {
	return accountModel{Account: account, Password: password, Token: token}
}

func TestQueueAccountSyncPersistsBeforeEnqueueFailure(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&accountModel{}); err != nil {
		t.Fatal(err)
	}
	account := testAccount("owner@example.com", "password", "")
	if err := db.Create(&account).Error; err != nil {
		t.Fatal(err)
	}
	queue := &persistedSyncQueue{db: db}
	service := NewService(db, queue)
	service.logs = testOperationLogs{}

	task, err := service.SyncAccount(context.Background(), account.ID, MutationMeta{OperatorUserID: 1, Path: "/test"})
	if err != nil {
		t.Fatal(err)
	}
	if !queue.sawQueued || task.Status != SyncTaskQueued {
		t.Fatalf("queue observed queued=%v, task=%+v", queue.sawQueued, task)
	}
	if err := db.First(&account, account.ID).Error; err != nil {
		t.Fatal(err)
	}
	if SyncTaskStatus(account.SyncStatus) != SyncTaskQueued || account.SyncQueuedAt == nil {
		t.Fatalf("durable sync state = %+v", account)
	}
}

func TestImportAccountsClearsStoredToken(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&accountModel{}); err != nil {
		t.Fatal(err)
	}
	tokenUpdated := time.Now().UTC()
	startedAt := tokenUpdated.Add(-time.Minute)
	account := testAccount("owner@example.com", "old-password", "old-token")
	account.TokenUpdated = &tokenUpdated
	account.SyncStatus = string(SyncTaskRunning)
	account.SyncStartedAt = &startedAt
	account.SyncAttempts = 3
	if err := db.Create(&account).Error; err != nil {
		t.Fatal(err)
	}
	queue := &testSyncQueue{}
	service := NewService(db, queue)
	service.logs = testOperationLogs{}
	if _, err := service.ImportAccounts(context.Background(), "owner@example.com----new-password", MutationMeta{OperatorUserID: 1, Path: "/test"}); err != nil {
		t.Fatal(err)
	}
	var stored accountModel
	if err := db.First(&stored, account.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Password != "new-password" || stored.Token != "" || stored.TokenUpdated != nil ||
		SyncTaskStatus(stored.SyncStatus) != SyncTaskQueued || stored.SyncQueuedAt == nil ||
		stored.SyncStartedAt != nil || stored.SyncFinishedAt != nil || stored.SyncAttempts != 0 ||
		len(queue.accountIDs) != 1 || queue.accountIDs[0] != account.ID {
		t.Fatalf("reimported account retained stale credentials: %+v", stored)
	}
}

func TestReimportSupersedesRunningAccountSync(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:kitesim_sync_reimport?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&accountModel{}, &phoneModel{}); err != nil {
		t.Fatal(err)
	}
	captcha, err := os.ReadFile("testdata/NUY7.png")
	if err != nil {
		t.Fatal(err)
	}
	orderStarted := make(chan struct{})
	releaseOrder := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/index/captcha-image-base64":
			_ = json.NewEncoder(response).Encode(map[string]any{
				"captchaImageBase64": base64.StdEncoding.EncodeToString(captcha), "captchaKey": "key",
			})
		case "/index/sign-in":
			_ = json.NewEncoder(response).Encode(map[string]any{"code": 200, "data": "stale-worker-token"})
		case "/userPhonePurchase/getOrderPage":
			records := []map[string]any{}
			if request.URL.Query().Get("status") == "1" {
				close(orderStarted)
				<-releaseOrder
				records = append(records, map[string]any{
					"id": "stale-order", "orderNo": "STALE-1", "phoneCode": "1", "phoneNumber": "14165550001",
				})
			}
			_ = json.NewEncoder(response).Encode(map[string]any{
				"code": 200, "data": map[string]any{"records": records, "totalPage": 1},
			})
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	clock := time.Date(2026, time.August, 16, 12, 0, 0, 0, time.UTC)
	queuedAt := clock.Add(-time.Millisecond)
	account := accountModel{
		Account: "owner@example.com", Password: "old-password",
		SyncStatus: string(SyncTaskQueued), SyncQueuedAt: &queuedAt,
	}
	if err := db.Create(&account).Error; err != nil {
		t.Fatal(err)
	}
	queue := &testSyncQueue{}
	service := NewService(db, queue)
	service.client = NewClient(server.Client())
	service.client.BaseURL = server.URL
	service.logs = testOperationLogs{}
	service.now = func() time.Time { return clock }
	oldClaim, err := service.markSyncRunning(context.Background(), account.ID)
	if err != nil {
		t.Fatal(err)
	}
	errCh := make(chan error, 1)
	go func() { errCh <- service.processAccountSync(context.Background(), oldClaim) }()
	<-orderStarted

	if _, err := service.ImportAccounts(context.Background(), "owner@example.com----new-password", MutationMeta{OperatorUserID: 1, Path: "/test"}); err != nil {
		t.Fatal(err)
	}
	newClaim, err := service.markSyncRunning(context.Background(), account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !newClaim.StartedAt.After(oldClaim.StartedAt) {
		t.Fatalf("new claim %v did not supersede old claim %v", newClaim.StartedAt, oldClaim.StartedAt)
	}
	close(releaseOrder)
	if err := <-errCh; !errors.Is(err, errSyncSuperseded) {
		t.Fatalf("old sync error = %v", err)
	}

	if err := db.First(&account, account.ID).Error; err != nil {
		t.Fatal(err)
	}
	if account.Password != "new-password" || account.Token != "" ||
		SyncTaskStatus(account.SyncStatus) != SyncTaskRunning || account.SyncStartedAt == nil ||
		!account.SyncStartedAt.Equal(newClaim.StartedAt) || len(queue.accountIDs) != 1 {
		t.Fatalf("new sync claim was overwritten: %+v, queued=%v", account, queue.accountIDs)
	}
	var phoneCount int64
	if err := db.Model(&phoneModel{}).Count(&phoneCount).Error; err != nil || phoneCount != 0 {
		t.Fatalf("stale sync committed phones=%d err=%v", phoneCount, err)
	}
}

func TestImportSyncListAndMessages(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&accountModel{}, &phoneModel{}); err != nil {
		t.Fatal(err)
	}
	captcha, err := os.ReadFile("testdata/NUY7.png")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/index/captcha-image-base64":
			_ = json.NewEncoder(w).Encode(map[string]any{"captchaImageBase64": base64.StdEncoding.EncodeToString(captcha), "captchaKey": "key"})
		case "/index/sign-in":
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["captchaCode"] != "NUY7" {
				t.Fatalf("captchaCode = %q", body["captchaCode"])
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": "long-lived-token-value"})
		case "/userPhonePurchase/getOrderPage":
			records := []map[string]any{}
			if r.URL.Query().Get("status") == "1" {
				records = append(records, map[string]any{
					"id": "order-id-77", "orderNo": "ORDER-77", "phoneCode": "86", "phoneNumber": "13600000000",
					"autoRenew": 1, "createTime": "2026-08-16 09:00:00", "expireTime": "2026-09-16 09:00:00",
				})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"records": records, "totalPage": 1}})
		case "/userPhonePurchase/seePhoneNubmerSms":
			if got := r.URL.Query().Get("orderId"); got != "order-id-77" {
				t.Fatalf("orderId = %q", got)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"noteList": []map[string]any{{"caller": "Kite", "content": "code 1234", "sendTime": "2026-08-16 10:00:00"}}}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := NewClient(server.Client())
	client.BaseURL = server.URL
	queue := &testSyncQueue{}
	service := NewService(db, queue)
	service.client = client
	service.logs = testOperationLogs{}
	result, err := service.ImportAccounts(context.Background(), "owner@example.com----password1", MutationMeta{OperatorUserID: 1, Path: "/test"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Imported != 1 || result.Queued != 1 || result.Failed != 0 || len(queue.accountIDs) != 1 {
		t.Fatalf("unexpected import result: %+v", result)
	}
	var storedAccount accountModel
	if err := db.First(&storedAccount, queue.accountIDs[0]).Error; err != nil {
		t.Fatal(err)
	}
	if storedAccount.Password != "password1" {
		t.Fatalf("stored password = %q", storedAccount.Password)
	}
	if err := db.Model(&accountModel{}).Where("id = ?", queue.accountIDs[0]).Update("created_at", time.Date(2020, time.January, 1, 0, 0, 0, 0, time.UTC)).Error; err != nil {
		t.Fatal(err)
	}
	queued, err := service.ListPhones(context.Background(), PhoneListFilter{Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(queued.Items) != 1 || queued.Items[0].Status != AdminPhoneUnsynced || queued.Items[0].SyncStatus != SyncTaskQueued || queued.Items[0].TokenAvailable {
		t.Fatalf("unexpected queued list: %+v", queued.Items)
	}
	claim, err := service.markSyncRunning(context.Background(), queue.accountIDs[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := service.processAccountSync(context.Background(), claim); err != nil {
		t.Fatal(err)
	}
	if err := db.First(&storedAccount, queue.accountIDs[0]).Error; err != nil {
		t.Fatal(err)
	}
	if storedAccount.Token != "long-lived-token-value" {
		t.Fatalf("stored token = %q", storedAccount.Token)
	}
	list, err := service.ListPhones(context.Background(), PhoneListFilter{Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 1 || list.Items[0].PhoneID == nil || list.Items[0].PhoneNumber != "+86 13600000000" || list.Items[0].Status != AdminPhoneActive || !list.Items[0].AutoRenew || !list.Items[0].TokenAvailable || list.Items[0].SyncStatus != SyncTaskSucceeded {
		t.Fatalf("unexpected phone list: %+v", list.Items)
	}
	encodedList, err := json.Marshal(list)
	if err != nil || strings.Contains(string(encodedList), "password1") || strings.Contains(string(encodedList), "long-lived-token-value") {
		t.Fatalf("phone read API exposed a stored credential: %s, err=%v", encodedList, err)
	}
	createdFrom := time.Date(2026, time.August, 16, 8, 30, 0, 0, time.UTC)
	createdTo := time.Date(2026, time.August, 16, 9, 30, 0, 0, time.UTC)
	filtered, err := service.ListPhones(context.Background(), PhoneListFilter{Limit: 20, CreatedFrom: &createdFrom, CreatedTo: &createdTo})
	if err != nil {
		t.Fatal(err)
	}
	if len(filtered.Items) != 1 {
		t.Fatalf("provider create-time filter returned %+v", filtered.Items)
	}
	phoneID := *list.Items[0].PhoneID
	if _, err := service.SyncAccount(context.Background(), queue.accountIDs[0], MutationMeta{OperatorUserID: 1, Path: "/test"}); err != nil {
		t.Fatal(err)
	}
	claim, err = service.markSyncRunning(context.Background(), queue.accountIDs[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := service.processAccountSync(context.Background(), claim); err != nil {
		t.Fatal(err)
	}
	list, err = service.ListPhones(context.Background(), PhoneListFilter{Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 1 || list.Items[0].PhoneID == nil || *list.Items[0].PhoneID != phoneID {
		t.Fatalf("phone id changed after sync: before=%d after=%+v", phoneID, list.Items)
	}
	messages, err := service.Messages(context.Background(), *list.Items[0].PhoneID, MutationMeta{OperatorUserID: 1, Path: "/test"})
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("%s:%s", messages[0].Caller, messages[0].Content); got != "Kite:code 1234" {
		t.Fatalf("unexpected message: %s", got)
	}
}

func TestSyncEmptyUpstreamResponsePreservesHistoricalPhones(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&accountModel{}, &phoneModel{}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/userPhonePurchase/getOrderPage" {
			http.NotFound(response, request)
			return
		}
		_ = json.NewEncoder(response).Encode(map[string]any{
			"code": 200,
			"data": map[string]any{"records": []map[string]any{}, "totalPage": 1},
		})
	}))
	defer server.Close()

	service := NewService(db, &testSyncQueue{})
	service.client = NewClient(server.Client())
	service.client.BaseURL = server.URL
	queuedAt := time.Now().UTC().Truncate(time.Millisecond)
	account := testAccount("owner@example.com", "password", "long-lived-token")
	account.SyncStatus = string(SyncTaskQueued)
	account.SyncQueuedAt = &queuedAt
	if err := db.Create(&account).Error; err != nil {
		t.Fatal(err)
	}
	phone := phoneModel{
		AccountID: account.ID, ProviderOrderID: "order-id-77", OrderNo: "ORDER-77",
		PhoneCode: "1", PhoneNumber: "14165550001", Status: int(PhoneActive),
	}
	if err := db.Create(&phone).Error; err != nil {
		t.Fatal(err)
	}

	claim, err := service.markSyncRunning(context.Background(), account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.processAccountSync(context.Background(), claim); err != nil {
		t.Fatal(err)
	}
	var stored phoneModel
	if err := db.First(&stored, phone.ID).Error; err != nil {
		t.Fatalf("historical phone was removed after empty sync: %v", err)
	}
	if stored.PhoneNumber != phone.PhoneNumber || stored.ProviderOrderID != phone.ProviderOrderID {
		t.Fatalf("historical phone changed after empty sync: %+v", stored)
	}
}

func TestMessagesDoesNotOverwriteNewerToken(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:kitesim_message_token_fence?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&accountModel{}, &phoneModel{}); err != nil {
		t.Fatal(err)
	}
	captcha, err := os.ReadFile("testdata/NUY7.png")
	if err != nil {
		t.Fatal(err)
	}
	messageStarted := make(chan struct{})
	releaseMessage := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/index/captcha-image-base64":
			_ = json.NewEncoder(response).Encode(map[string]any{
				"captchaImageBase64": base64.StdEncoding.EncodeToString(captcha), "captchaKey": "key",
			})
		case "/index/sign-in":
			_ = json.NewEncoder(response).Encode(map[string]any{"code": 200, "data": "stale-message-token"})
		case "/userPhonePurchase/seePhoneNubmerSms":
			close(messageStarted)
			<-releaseMessage
			_ = json.NewEncoder(response).Encode(map[string]any{
				"code": 200, "data": map[string]any{"noteList": []map[string]any{{"content": "1234"}}},
			})
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	account := testAccount("owner@example.com", "password", "")
	if err := db.Create(&account).Error; err != nil {
		t.Fatal(err)
	}
	phone := phoneModel{AccountID: account.ID, ProviderOrderID: "order-1", PhoneNumber: "14165550001"}
	if err := db.Create(&phone).Error; err != nil {
		t.Fatal(err)
	}
	service := NewService(db, nil)
	service.client = NewClient(server.Client())
	service.client.BaseURL = server.URL
	service.logs = testOperationLogs{}
	errCh := make(chan error, 1)
	go func() {
		_, err := service.Messages(context.Background(), phone.ID, MutationMeta{OperatorUserID: 1, Path: "/test"})
		errCh <- err
	}()
	<-messageStarted
	if err := db.Model(&accountModel{}).Where("id = ?", account.ID).Update("token", "newer-token").Error; err != nil {
		t.Fatal(err)
	}
	close(releaseMessage)
	if err := <-errCh; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&account, account.ID).Error; err != nil {
		t.Fatal(err)
	}
	if account.Token != "newer-token" {
		t.Fatalf("newer token was overwritten with %q", account.Token)
	}
}
