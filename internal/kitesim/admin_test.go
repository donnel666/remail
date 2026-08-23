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
	if err := db.AutoMigrate(&accountModel{}, &syncRunModel{}); err != nil {
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
	var run syncRunModel
	if err := db.Where("account_id = ?", account.ID).First(&run).Error; err != nil {
		t.Fatal(err)
	}
	if SyncTaskStatus(run.Status) != SyncTaskQueued || !run.QueuedAt.Equal(*account.SyncQueuedAt) {
		t.Fatalf("durable sync run = %+v", run)
	}
}

func TestImportAccountsClearsStoredToken(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&accountModel{}, &phoneModel{}, &syncRunModel{}); err != nil {
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
	if err := db.AutoMigrate(&accountModel{}, &phoneModel{}, &syncRunModel{}); err != nil {
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
	if err := db.AutoMigrate(&accountModel{}, &phoneModel{}, &syncRunModel{}); err != nil {
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
					"id": "order-id-77", "orderNo": "ORDER-77", "phoneCode": "1", "phoneNumber": "15488768536",
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
	if len(list.Items) != 1 || list.Items[0].PhoneID == nil || list.Items[0].PhoneNumber != "+1 5488768536" || list.Items[0].Status != AdminPhoneActive || !list.Items[0].AutoRenew || !list.Items[0].TokenAvailable || list.Items[0].SyncStatus != SyncTaskSucceeded {
		t.Fatalf("unexpected phone list: %+v", list.Items)
	}
	encodedList, err := json.Marshal(list)
	if err != nil || strings.Contains(string(encodedList), "password1") || strings.Contains(string(encodedList), "long-lived-token-value") {
		t.Fatalf("phone read API exposed a stored credential: %s, err=%v", encodedList, err)
	}
	createdFrom := time.Date(2026, time.August, 16, 0, 30, 0, 0, time.UTC)
	createdTo := time.Date(2026, time.August, 16, 1, 30, 0, 0, time.UTC)
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
	if err := db.AutoMigrate(&accountModel{}, &phoneModel{}, &syncRunModel{}); err != nil {
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

func TestListPhonesIncludesLinkedICloudAccountCount(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&accountModel{}, &phoneModel{}); err != nil {
		t.Fatal(err)
	}
	account := testAccount("linked@example.com", "password", "token")
	if err := db.Create(&account).Error; err != nil {
		t.Fatal(err)
	}
	phone := phoneModel{AccountID: account.ID, ProviderOrderID: "order-linked", PhoneCode: "1", PhoneNumber: "5813045473", CountryCode: "US", Status: 1}
	if err := db.Create(&phone).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("CREATE TABLE icloud_resources (id INTEGER PRIMARY KEY, kitesim_phone_id INTEGER, status TEXT)").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("INSERT INTO icloud_resources (id, kitesim_phone_id, status) VALUES (?, ?, ?), (?, ?, ?), (?, ?, ?)",
		1, phone.ID, "normal", 2, phone.ID, "pending", 3, phone.ID, "deleted").Error; err != nil {
		t.Fatal(err)
	}
	list, err := NewService(db, nil).ListPhones(context.Background(), PhoneListFilter{Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 1 || list.Items[0].LinkedAccountCount != 2 {
		t.Fatalf("linked account count = %+v", list.Items)
	}
}

func TestListPhonesDerivesExclusiveStatusFromICloudAliasCapacity(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&accountModel{}, &phoneModel{}); err != nil {
		t.Fatal(err)
	}
	accounts := []accountModel{
		testAccount("exclusive@example.com", "password", "token"),
		testAccount("released@example.com", "password", "token"),
	}
	if err := db.Create(&accounts).Error; err != nil {
		t.Fatal(err)
	}
	phones := []phoneModel{
		{AccountID: accounts[0].ID, ProviderOrderID: "order-exclusive", PhoneCode: "1", PhoneNumber: "4155550001", CountryCode: "US", Status: int(PhoneActive)},
		{AccountID: accounts[1].ID, ProviderOrderID: "order-released", PhoneCode: "1", PhoneNumber: "4155550002", CountryCode: "US", Status: int(PhoneActive)},
	}
	if err := db.Create(&phones).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("CREATE TABLE icloud_resources (id INTEGER PRIMARY KEY, primary_email TEXT, kitesim_phone_id INTEGER, bound_phone_number TEXT, status TEXT, alias_count INTEGER)").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("INSERT INTO icloud_resources (id, primary_email, kitesim_phone_id, bound_phone_number, status, alias_count) VALUES (?, ?, ?, ?, ?, ?), (?, ?, ?, ?, ?, ?), (?, ?, ?, ?, ?, ?)",
		1, "placeholder@example.com", nil, "4155550001", "normal", 749,
		2, "full@example.com", phones[0].ID, "", "pending", 750,
		3, "deleted@example.com", phones[1].ID, "", "deleted", 0,
	).Error; err != nil {
		t.Fatal(err)
	}
	service := NewService(db, nil)
	list, err := service.ListPhones(context.Background(), PhoneListFilter{Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if list.Facets.Exclusive != 1 || list.Facets.Active != 1 {
		t.Fatalf("status facets = %+v", list.Facets)
	}
	byPhone := make(map[uint]PhoneItem, len(list.Items))
	for _, item := range list.Items {
		if item.PhoneID != nil {
			byPhone[*item.PhoneID] = item
		}
	}
	if byPhone[phones[0].ID].Status != AdminPhoneExclusive || byPhone[phones[0].ID].LinkedAccountCount != 2 {
		t.Fatalf("exclusive phone = %+v", byPhone[phones[0].ID])
	}
	if byPhone[phones[1].ID].Status != AdminPhoneActive || byPhone[phones[1].ID].LinkedAccountCount != 0 {
		t.Fatalf("released phone = %+v", byPhone[phones[1].ID])
	}
	exclusive, err := service.ListPhones(context.Background(), PhoneListFilter{Limit: 20, Status: AdminPhoneExclusive})
	if err != nil || exclusive.Total != 1 || len(exclusive.Items) != 1 || exclusive.Items[0].Status != AdminPhoneExclusive {
		t.Fatalf("exclusive filter = %+v err=%v", exclusive, err)
	}
	active, err := service.ListPhones(context.Background(), PhoneListFilter{Limit: 20, Status: AdminPhoneActive})
	if err != nil || active.Total != 1 || len(active.Items) != 1 || active.Items[0].PhoneID == nil || *active.Items[0].PhoneID != phones[1].ID {
		t.Fatalf("active filter = %+v err=%v", active, err)
	}
}

func TestListPhonesDerivesAndFiltersBlacklistedStatus(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&accountModel{}, &phoneModel{}); err != nil {
		t.Fatal(err)
	}
	accounts := []accountModel{
		testAccount("blacklisted@example.com", "password", "token"),
		testAccount("available@example.com", "password", "token"),
	}
	if err := db.Create(&accounts).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.August, 16, 12, 0, 0, 0, time.UTC)
	until := now.Add(time.Hour)
	phones := []phoneModel{
		{AccountID: accounts[0].ID, ProviderOrderID: "order-blacklisted", PhoneCode: "1", PhoneNumber: "4155550001", CountryCode: "US", Status: int(PhoneActive), SMSBlacklistedUntil: &until},
		{AccountID: accounts[1].ID, ProviderOrderID: "order-available", PhoneCode: "1", PhoneNumber: "4155550002", CountryCode: "US", Status: int(PhoneActive)},
	}
	if err := db.Create(&phones).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("CREATE TABLE icloud_resources (id INTEGER PRIMARY KEY, primary_email TEXT, kitesim_phone_id INTEGER, bound_phone_number TEXT, status TEXT, alias_count INTEGER)").Error; err != nil {
		t.Fatal(err)
	}
	service := NewService(db, nil)
	service.now = func() time.Time { return now }
	list, err := service.ListPhones(context.Background(), PhoneListFilter{Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if list.Facets.Blacklisted != 1 || list.Facets.Active != 1 {
		t.Fatalf("blacklist facets = %+v", list.Facets)
	}
	byPhone := make(map[uint]PhoneItem, len(list.Items))
	for _, item := range list.Items {
		if item.PhoneID != nil {
			byPhone[*item.PhoneID] = item
		}
	}
	if byPhone[phones[0].ID].Status != AdminPhoneBlacklisted {
		t.Fatalf("blacklisted phone status = %+v", byPhone[phones[0].ID])
	}
	blacklisted, err := service.ListPhones(context.Background(), PhoneListFilter{Limit: 20, Status: AdminPhoneBlacklisted})
	if err != nil || blacklisted.Total != 1 || len(blacklisted.Items) != 1 || blacklisted.Items[0].Status != AdminPhoneBlacklisted {
		t.Fatalf("blacklisted filter = %+v err=%v", blacklisted, err)
	}
	active, err := service.ListPhones(context.Background(), PhoneListFilter{Limit: 20, Status: AdminPhoneActive})
	if err != nil || active.Total != 1 || len(active.Items) != 1 || active.Items[0].PhoneID == nil || *active.Items[0].PhoneID != phones[1].ID {
		t.Fatalf("active filter includes blacklisted phone: %+v err=%v", active, err)
	}
}

func TestMessagesDoesNotOverwriteNewerToken(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:kitesim_message_token_fence?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&accountModel{}, &phoneModel{}, &syncRunModel{}); err != nil {
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
	phone := phoneModel{
		AccountID: account.ID, ProviderOrderID: "order-1", PhoneNumber: "14165550001", Status: int(PhoneActive),
	}
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
	select {
	case <-messageStarted:
	case err := <-errCh:
		t.Fatalf("messages returned before the upstream request started: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the upstream message request")
	}
	updateErr := db.Model(&accountModel{}).Where("id = ?", account.ID).Update("token", "newer-token").Error
	close(releaseMessage)
	if updateErr != nil {
		t.Fatal(updateErr)
	}
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the message request to finish")
	}
	if err := db.First(&account, account.ID).Error; err != nil {
		t.Fatal(err)
	}
	if account.Token != "newer-token" {
		t.Fatalf("newer token was overwritten with %q", account.Token)
	}
}

func TestPhoneLifecycleSoftDeleteAndReimport(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&accountModel{}, &phoneModel{}, &upstreamSettingsModel{}, &operationModel{}, &syncRunModel{}); err != nil {
		t.Fatal(err)
	}
	account := testAccount("owner@example.com", "password", "token")
	if err := db.Create(&account).Error; err != nil {
		t.Fatal(err)
	}
	phones := []phoneModel{
		{AccountID: account.ID, ProviderOrderID: "order-1", PhoneNumber: "14165550001", Status: int(PhoneActive)},
		{AccountID: account.ID, ProviderOrderID: "order-2", PhoneNumber: "14165550002", Status: int(PhoneActive)},
	}
	if err := db.Create(&phones).Error; err != nil {
		t.Fatal(err)
	}
	queue := &testSyncQueue{}
	service := NewService(db, queue)
	service.logs = testOperationLogs{}
	meta := MutationMeta{OperatorUserID: 1, Path: "/test"}

	if _, err := service.SetPhonesDisabled(context.Background(), []uint{phones[0].ID}, true, meta); err != nil {
		t.Fatal(err)
	}
	list, err := service.ListPhones(context.Background(), PhoneListFilter{Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 2 || list.Facets.Disabled != 1 {
		t.Fatalf("disabled list = %+v facets=%+v", list.Items, list.Facets)
	}

	if _, err := service.DeletePhones(context.Background(), []uint{phones[0].ID}, nil, meta); err != nil {
		t.Fatal(err)
	}
	if err := db.First(&account, account.ID).Error; err != nil || account.DeletedAt != nil {
		t.Fatalf("account deleted before its final phone: %+v err=%v", account, err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/userPhonePurchase/getOrderPage" {
			http.NotFound(response, request)
			return
		}
		records := []map[string]any{}
		if request.URL.Query().Get("status") == "1" {
			records = append(records,
				map[string]any{"id": "order-1", "orderNo": "ORDER-1", "phoneNumber": "14165550001"},
				map[string]any{"id": "order-2", "orderNo": "ORDER-2", "phoneNumber": "14165550002"},
			)
		}
		_ = json.NewEncoder(response).Encode(map[string]any{
			"code": 200, "data": map[string]any{"records": records, "totalPage": 1},
		})
	}))
	defer server.Close()
	service.client = NewClient(server.Client())
	service.client.BaseURL = server.URL
	if err := db.Model(&accountModel{}).Where("id = ?", account.ID).Updates(map[string]any{
		"sync_status": SyncTaskQueued, "sync_queued_at": time.Now().UTC(),
	}).Error; err != nil {
		t.Fatal(err)
	}
	claim, err := service.markSyncRunning(context.Background(), account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.processAccountSync(context.Background(), claim); err != nil {
		t.Fatal(err)
	}
	if err := db.First(&phones[0], phones[0].ID).Error; err != nil || phones[0].DeletedAt == nil || phones[0].DisabledAt == nil {
		t.Fatalf("ordinary sync restored a deleted phone: %+v err=%v", phones[0], err)
	}
	if _, err := service.DeletePhones(context.Background(), []uint{phones[1].ID}, nil, meta); err != nil {
		t.Fatal(err)
	}
	if err := db.First(&account, account.ID).Error; err != nil || account.DeletedAt == nil {
		t.Fatalf("account not soft-deleted after final phone: %+v err=%v", account, err)
	}

	if _, err := service.ImportAccounts(context.Background(), "owner@example.com----new-password", meta); err != nil {
		t.Fatal(err)
	}
	var restoredAccount accountModel
	if err := db.First(&restoredAccount, account.ID).Error; err != nil {
		t.Fatal(err)
	}
	if restoredAccount.DeletedAt != nil || restoredAccount.Password != "new-password" || len(queue.accountIDs) != 1 {
		t.Fatalf("reimported account was not restored: %+v queued=%v", restoredAccount, queue.accountIDs)
	}
	var restored []phoneModel
	if err := db.Where("account_id = ?", restoredAccount.ID).Order("id").Find(&restored).Error; err != nil {
		t.Fatal(err)
	}
	if len(restored) != 2 || restored[0].DeletedAt != nil || restored[0].DisabledAt != nil ||
		restored[1].DeletedAt != nil || restored[1].DisabledAt != nil {
		t.Fatalf("reimported phones were not restored: %+v", restored)
	}
}

func TestReimportPreservesExplicitPhoneDisable(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&accountModel{}, &phoneModel{}, &syncRunModel{}); err != nil {
		t.Fatal(err)
	}
	account := testAccount("owner@example.com", "password", "token")
	if err := db.Create(&account).Error; err != nil {
		t.Fatal(err)
	}
	disabledAt := time.Now().UTC()
	phone := phoneModel{
		AccountID: account.ID, ProviderOrderID: "order-1", PhoneNumber: "14165550001",
		Status: int(PhoneActive), DisabledAt: &disabledAt,
	}
	if err := db.Create(&phone).Error; err != nil {
		t.Fatal(err)
	}
	service := NewService(db, &testSyncQueue{})
	service.logs = testOperationLogs{}
	if _, err := service.ImportAccounts(context.Background(), "owner@example.com----new-password", MutationMeta{OperatorUserID: 1, Path: "/test"}); err != nil {
		t.Fatal(err)
	}
	if err := db.First(&phone, phone.ID).Error; err != nil {
		t.Fatal(err)
	}
	if phone.DeletedAt != nil || phone.DisabledAt == nil {
		t.Fatalf("reimport changed explicit disabled state: %+v", phone)
	}
}

func TestListSyncRunsReturnsExactAccountHistory(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&accountModel{}, &syncRunModel{}); err != nil {
		t.Fatal(err)
	}
	accountA := testAccount("a@example.com", "password", "")
	accountB := testAccount("b@example.com", "password", "")
	if err := db.Create(&accountA).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&accountB).Error; err != nil {
		t.Fatal(err)
	}
	queuedAt := time.Date(2026, time.August, 16, 12, 0, 0, 0, time.UTC)
	finishedAt := queuedAt.Add(time.Minute)
	runs := []syncRunModel{
		{AccountID: accountA.ID, Status: string(SyncTaskSucceeded), Attempts: 1, QueuedAt: queuedAt, FinishedAt: &finishedAt, UpdatedAt: finishedAt},
		{AccountID: accountA.ID, Status: string(SyncTaskFailed), Attempts: 2, LastSafeError: "safe failure", QueuedAt: queuedAt.Add(time.Hour), FinishedAt: &finishedAt, UpdatedAt: finishedAt.Add(time.Hour)},
		{AccountID: accountB.ID, Status: string(SyncTaskSucceeded), Attempts: 1, QueuedAt: queuedAt, FinishedAt: &finishedAt, UpdatedAt: finishedAt.Add(2 * time.Hour)},
	}
	if err := db.Create(&runs).Error; err != nil {
		t.Fatal(err)
	}
	result, err := NewService(db, nil).ListSyncRuns(context.Background(), accountA.ID, 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 2 || result.Succeeded != 1 || len(result.Items) != 1 ||
		result.Items[0].TaskID != fmt.Sprintf("kitesim-sync:%d", runs[1].ID) || result.Items[0].LastSafeError != "safe failure" {
		t.Fatalf("sync history = %+v", result)
	}
}

func TestJSONTextBindsAsDatabaseText(t *testing.T) {
	value, err := jsonText(`{"ok":true}`).Value()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := value.(string); !ok {
		t.Fatalf("JSON database value type = %T, want string", value)
	}
}
