package kitesim

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

type failingRequestDoer struct{}

func (failingRequestDoer) Do(*http.Request) (*http.Response, error) {
	return nil, errors.New("connection reset")
}

type persistedRefreshQueue struct {
	db        *gorm.DB
	sawQueued bool
}

func (*persistedRefreshQueue) Enqueue(context.Context, uint) (bool, error) {
	return true, nil
}

func (q *persistedRefreshQueue) EnqueueUpstreamRefresh(context.Context) (bool, error) {
	var settings upstreamSettingsModel
	if err := q.db.First(&settings, upstreamSettingsID).Error; err != nil {
		return false, err
	}
	q.sawQueued = SyncTaskStatus(settings.RefreshStatus) == SyncTaskQueued
	return false, errors.New("redis unavailable")
}

func TestPickBestNumberUsesRarestSegmentThenLowestPrice(t *testing.T) {
	number, err := pickBestNumber([]PhoneNumberOffer{
		{PhoneCode: "1", PhoneNumber: "14165550001", BuyPrice: "1.20"},
		{PhoneCode: "1", PhoneNumber: "14165550002", BuyPrice: "0.80"},
		{PhoneCode: "1", PhoneNumber: "16045550003", BuyPrice: "1.10"},
		{PhoneCode: "1", PhoneNumber: "16475550004", BuyPrice: "1.30"},
		{PhoneCode: "1", PhoneNumber: "16475550005", BuyPrice: "0.90"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if number.PhoneNumber != "16045550003" {
		t.Fatalf("picked %s, want the only 604 number", number.PhoneNumber)
	}
}

func TestPickBestNumberSubtractsLocalInventoryFromRemoteRank(t *testing.T) {
	number, err := pickBestNumber([]PhoneNumberOffer{
		{PhoneCode: "1", PhoneNumber: "16045550001", BuyPrice: "0.80"},
		{PhoneCode: "1", PhoneNumber: "14165550002", BuyPrice: "1.20"},
		{PhoneCode: "1", PhoneNumber: "14165550003", BuyPrice: "0.90"},
	}, map[string]int{"604": 2})
	if err != nil {
		t.Fatal(err)
	}
	if number.PhoneNumber != "14165550003" {
		t.Fatalf("picked %s, want the 416 segment after local inventory penalty", number.PhoneNumber)
	}
}

func TestQueueUpstreamRefreshPersistsBeforeEnqueueFailure(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&accountModel{}, &upstreamSettingsModel{}); err != nil {
		t.Fatal(err)
	}
	account := testAccount("owner@example.com", "password", "")
	if err := db.Create(&account).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&upstreamSettingsModel{ID: upstreamSettingsID, AccountID: &account.ID, Balance: "0"}).Error; err != nil {
		t.Fatal(err)
	}
	queue := &persistedRefreshQueue{db: db}
	service := NewService(db, queue)
	service.logs = testOperationLogs{}

	task, err := service.QueueUpstreamRefresh(context.Background(), MutationMeta{OperatorUserID: 1, Path: "/test"})
	if err != nil {
		t.Fatal(err)
	}
	if !queue.sawQueued || task.Status != SyncTaskQueued {
		t.Fatalf("queue observed queued=%v, task=%+v", queue.sawQueued, task)
	}
	var settings upstreamSettingsModel
	if err := db.First(&settings, upstreamSettingsID).Error; err != nil {
		t.Fatal(err)
	}
	if SyncTaskStatus(settings.RefreshStatus) != SyncTaskQueued || settings.RefreshQueued == nil {
		t.Fatalf("durable refresh state = %+v", settings)
	}
}

func TestUpstreamRefreshDoesNotOverwriteSwitchedAccount(t *testing.T) {
	balanceStarted := make(chan struct{})
	releaseBalance := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/user/info":
			close(balanceStarted)
			<-releaseBalance
			_ = json.NewEncoder(response).Encode(map[string]any{"code": 200, "data": map[string]any{"balance": "55"}})
		case "/countryCode/page":
			_ = json.NewEncoder(response).Encode(map[string]any{"code": 200, "data": map[string]any{
				"records": []map[string]any{{"twoWordsCode": "CA", "status": true}}, "totalPage": 1,
			}})
		case "/package/getNumberPackageList":
			_ = json.NewEncoder(response).Encode(map[string]any{"code": 200, "data": []map[string]any{{
				"id": "quarter", "countryCode": "CA", "durationType": 2, "durationValue": 1, "buyPrice": "1",
			}}})
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	db, err := gorm.Open(sqlite.Open("file:kitesim_refresh_account_switch?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&accountModel{}, &productModel{}, &upstreamSettingsModel{}); err != nil {
		t.Fatal(err)
	}
	accountA := testAccount("a@example.com", "password", "token-a")
	accountB := testAccount("b@example.com", "password", "token-b")
	if err := db.Create(&accountA).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&accountB).Error; err != nil {
		t.Fatal(err)
	}
	oldStarted := time.Date(2026, time.August, 16, 12, 0, 0, 0, time.UTC)
	newStarted := oldStarted.Add(time.Minute)
	if err := db.Create(&upstreamSettingsModel{
		ID: upstreamSettingsID, AccountID: &accountA.ID, Balance: "1", RefreshStatus: string(SyncTaskRunning),
		RefreshStarted: &oldStarted,
	}).Error; err != nil {
		t.Fatal(err)
	}
	service := NewService(db, nil)
	service.client = NewClient(server.Client())
	service.client.BaseURL = server.URL
	errCh := make(chan error, 1)
	go func() {
		errCh <- service.processUpstreamRefresh(context.Background(), upstreamRefreshClaim{
			AccountID: accountA.ID, StartedAt: oldStarted,
		})
	}()
	<-balanceStarted
	if err := db.Model(&upstreamSettingsModel{}).Where("id = ?", upstreamSettingsID).Updates(map[string]any{
		"account_id": accountB.ID, "balance": "0", "refresh_status": SyncTaskIdle,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&upstreamSettingsModel{}).Where("id = ?", upstreamSettingsID).Updates(map[string]any{
		"account_id": accountA.ID, "balance": "2", "refresh_status": SyncTaskRunning,
		"refresh_started_at": newStarted,
	}).Error; err != nil {
		t.Fatal(err)
	}
	close(releaseBalance)
	if err := <-errCh; !errors.Is(err, errRefreshSuperseded) {
		t.Fatalf("refresh error = %v", err)
	}
	var settings upstreamSettingsModel
	if err := db.First(&settings, upstreamSettingsID).Error; err != nil {
		t.Fatal(err)
	}
	if settings.AccountID == nil || *settings.AccountID != accountA.ID || settings.Balance != "2" ||
		SyncTaskStatus(settings.RefreshStatus) != SyncTaskRunning || settings.RefreshStarted == nil ||
		!settings.RefreshStarted.Equal(newStarted) {
		t.Fatalf("new A->B->A refresh claim was overwritten: %+v", settings)
	}
	var products int64
	if err := db.Model(&productModel{}).Count(&products).Error; err != nil || products != 0 {
		t.Fatalf("superseded refresh committed products=%d err=%v", products, err)
	}
}

func TestInvalidProductCatalogPreservesExistingProducts(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/user/info":
			_ = json.NewEncoder(response).Encode(map[string]any{"code": 200, "data": map[string]any{"balance": "10"}})
		case "/countryCode/page":
			_ = json.NewEncoder(response).Encode(map[string]any{"code": 200, "data": map[string]any{
				"records": []map[string]any{{"twoWordsCode": "CA", "status": true}}, "totalPage": 1,
			}})
		case "/package/getNumberPackageList":
			_ = json.NewEncoder(response).Encode(map[string]any{"code": 200, "data": []map[string]any{{"id": "", "buyPrice": "1"}}})
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&accountModel{}, &productModel{}, &upstreamSettingsModel{}); err != nil {
		t.Fatal(err)
	}
	account := testAccount("owner@example.com", "unused", "token")
	if err := db.Create(&account).Error; err != nil {
		t.Fatal(err)
	}
	product := productModel{CountryCode: "CA", PackageID: "quarter", BuyPrice: "1", Active: true}
	if err := db.Create(&product).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&upstreamSettingsModel{ID: upstreamSettingsID, AccountID: &account.ID, Balance: "0"}).Error; err != nil {
		t.Fatal(err)
	}
	service := NewService(db, nil)
	service.client = NewClient(server.Client())
	service.client.BaseURL = server.URL

	if err := service.processUpstreamRefresh(context.Background(), upstreamRefreshClaim{
		AccountID: account.ID, StartedAt: time.Now().UTC(),
	}); err == nil {
		t.Fatal("invalid product catalog was accepted")
	}
	if err := db.First(&product, product.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !product.Active {
		t.Fatal("existing product was retired by an invalid catalog")
	}
}

func TestSelectOperationPackageUsesSelectedOrSameDurationFallback(t *testing.T) {
	packages := []NumberPackage{
		{ID: "other", DurationType: 2, DurationValue: 1, BuyPrice: "2"},
		{ID: "selected", DurationType: 2, DurationValue: 1, BuyPrice: "3"},
	}
	selected, err := selectOperationPackage(packages, productModel{
		PackageID: "selected", DurationType: 2, DurationValue: 1,
	})
	if err != nil || selected.ID != "selected" {
		t.Fatalf("selected package = %q, err=%v", selected.ID, err)
	}

	selected, err = selectOperationPackage(packages, productModel{
		PackageID: "retired-id", DurationType: 2, DurationValue: 1,
	})
	if err != nil || selected.ID != "other" {
		t.Fatalf("fallback package = %q, err=%v", selected.ID, err)
	}
}

func TestCreateOrderTransportFailuresAreTerminal(t *testing.T) {
	client := &Client{
		BaseURL: "https://api.invalid", HTTP: failingRequestDoer{},
		headers: map[string]string{}, customHTTP: true,
	}
	tests := []struct {
		name string
		run  func() error
	}{
		{name: "phone purchase", run: func() error {
			_, err := client.CreatePhoneOrder(context.Background(), "token", "US", "15550000000", "package")
			return err
		}},
		{name: "renewal", run: func() error {
			_, err := client.CreateRenewalOrder(context.Background(), "token", PhoneOrder{PhoneNumber: "15550000000", CountryCode: "US"}, "package")
			return err
		}},
		{name: "recharge order", run: func() error {
			_, err := client.createRechargeOrder(context.Background(), "token", "10")
			return err
		}},
		{name: "recharge payment", run: func() error {
			_, err := client.createRechargePayment(context.Background(), "token", "order", "10")
			return err
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.run()
			if err == nil || errors.Is(err, ErrPaymentUncertain) || operationStatusForError(err) != OperationFailed {
				t.Fatalf("error = %v, status = %s", err, operationStatusForError(err))
			}
		})
	}
}

func TestAPIClientBlocksCrossOriginTokenRedirect(t *testing.T) {
	redirected := false
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		redirected = true
	}))
	defer target.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		http.Redirect(response, request, target.URL, http.StatusFound)
	}))
	defer origin.Close()

	client := NewClient(origin.Client())
	client.BaseURL = origin.URL
	if _, err := client.Balance(context.Background(), "long-lived-token"); err == nil {
		t.Fatal("cross-origin API redirect was accepted")
	}
	if redirected {
		t.Fatal("cross-origin redirect received the API token request")
	}
}

func TestConfirmBusinessFailureRemainsUncertain(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/userPhonePurchase/confirmPay":
			_ = json.NewEncoder(response).Encode(map[string]any{"code": 409, "message": "not confirmed"})
		case "/userPhonePurchase/getOrderDetail":
			_ = json.NewEncoder(response).Encode(map[string]any{"code": 200, "data": map[string]any{"id": 1}})
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	client := NewClient(server.Client())
	client.BaseURL = server.URL
	err := confirmPhoneOrderOnce(context.Background(), client, "token", "ORDER-1")
	if !errors.Is(err, ErrPaymentUncertain) || operationStatusForError(err) != OperationUncertain {
		t.Fatalf("confirm error = %v, status = %s", err, operationStatusForError(err))
	}
}

func TestDecimalLessKeepsExactPricePrecision(t *testing.T) {
	if !decimalLess("9007199254740992.00", "9007199254740992.01") {
		t.Fatal("decimal price comparison lost precision")
	}
}

func TestBalanceRejectsInvalidDecimal(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(response).Encode(map[string]any{
			"code": 200,
			"data": map[string]any{"balance": "not-money"},
		})
	}))
	defer server.Close()

	client := NewClient(server.Client())
	client.BaseURL = server.URL
	if _, err := client.Balance(context.Background(), "token"); err == nil {
		t.Fatal("invalid upstream balance was accepted")
	}
}

func TestExecutePurchaseRefreshesInventoryBeforeEachOrder(t *testing.T) {
	inventoryFetches := 0
	confirmations := 0
	purchased := make([]string, 0, 2)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/user/info":
			_ = json.NewEncoder(response).Encode(map[string]any{"code": 200, "data": map[string]any{"balance": "100"}})
		case "/countryCode/getPhoneNumber/CA":
			inventoryFetches++
			numbers := []map[string]any{
				{"phoneCode": "1", "phoneNumber": "14165550001", "buyPrice": "0.50"},
				{"phoneCode": "1", "phoneNumber": "14165550003", "buyPrice": "0.40"},
				{"phoneCode": "1", "phoneNumber": "16045550004", "buyPrice": "1.20"},
			}
			if inventoryFetches > 1 {
				numbers[1] = map[string]any{"phoneCode": "1", "phoneNumber": "14165550005", "buyPrice": "0.60"}
			}
			_ = json.NewEncoder(response).Encode(map[string]any{"code": 200, "data": numbers})
		case "/package/getNumberPackageList":
			_ = json.NewEncoder(response).Encode(map[string]any{"code": 200, "data": []map[string]any{{
				"id": "package", "countryCode": "CA", "durationType": 2, "durationValue": 1, "buyPrice": "1",
			}}})
		case "/userPhonePurchase/buyPhoneNumberOrder":
			var body struct {
				PhoneNumber string `json:"phoneNumber"`
			}
			_ = json.NewDecoder(request.Body).Decode(&body)
			purchased = append(purchased, body.PhoneNumber)
			_ = json.NewEncoder(response).Encode(map[string]any{"code": 200, "data": map[string]any{"orderNo": body.PhoneNumber}})
		case "/userPhonePurchase/confirmPay":
			confirmations++
			_ = json.NewEncoder(response).Encode(map[string]any{"code": 200, "data": map[string]any{}})
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&accountModel{}, &phoneModel{}, &productModel{}, &upstreamSettingsModel{}, &operationModel{}); err != nil {
		t.Fatal(err)
	}
	service := NewService(db, nil)
	account := testAccount("owner@example.com", "unused", "token")
	if err := db.Create(&account).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create([]phoneModel{
		{AccountID: account.ID, ProviderOrderID: "local-604-1", PhoneCode: "1", PhoneNumber: "16045550101", CountryCode: "CA", Status: int(PhoneActive)},
		{AccountID: account.ID, ProviderOrderID: "local-604-2", PhoneCode: "1", PhoneNumber: "16045550102", CountryCode: "CA", Status: int(PhoneActive)},
	}).Error; err != nil {
		t.Fatal(err)
	}
	product := productModel{CountryCode: "CA", PackageID: "package", DurationType: 2, DurationValue: 1, Active: true}
	if err := db.Create(&product).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&upstreamSettingsModel{ID: upstreamSettingsID, AccountID: &account.ID, Balance: "0"}).Error; err != nil {
		t.Fatal(err)
	}
	client := NewClient(server.Client())
	client.BaseURL = server.URL
	service.client = client

	operation := operationModel{
		AccountID: account.ID, CountryCode: "CA", PackageID: "package", RequestedCount: 2,
		Amount: "100", Status: string(OperationRunning), QueuedAt: time.Now(),
	}
	if err := db.Create(&operation).Error; err != nil {
		t.Fatal(err)
	}
	completed, orderNos, err := service.executePurchase(context.Background(), operation)
	if err != nil {
		t.Fatal(err)
	}
	if completed != 2 || len(orderNos) != 2 || inventoryFetches != 2 || confirmations != 2 {
		t.Fatalf("completed=%d orders=%v inventoryFetches=%d confirmations=%d", completed, orderNos, inventoryFetches, confirmations)
	}
	if len(purchased) != 2 || purchased[0] != "14165550003" || purchased[1] != "16045550004" {
		t.Fatalf("purchased numbers = %v", purchased)
	}
}
