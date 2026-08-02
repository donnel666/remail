package gmail

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	tradeapp "github.com/donnel666/remail/internal/trade/app"
	tradedomain "github.com/donnel666/remail/internal/trade/domain"
	"github.com/glebarez/sqlite"
	"github.com/hibiken/asynq"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type gmailTradeSpy struct {
	activations []tradeapp.ActivateGmailOrderRequest
	completed   []string
	failed      []string
}

func (s *gmailTradeSpy) ActivateGmailOrder(_ context.Context, req tradeapp.ActivateGmailOrderRequest) error {
	s.activations = append(s.activations, req)
	return nil
}

func (s *gmailTradeSpy) CompleteGmailOrder(_ context.Context, orderNo, _ string) error {
	s.completed = append(s.completed, orderNo)
	return nil
}

func (s *gmailTradeSpy) FailGmailOrder(_ context.Context, orderNo, _ string) error {
	s.failed = append(s.failed, orderNo)
	return nil
}

type gmailAlertNotifierSpy struct {
	alerts   []Alert
	failures int
}

func (s *gmailAlertNotifierSpy) NotifySMSBower(_ context.Context, alert Alert) error {
	s.alerts = append(s.alerts, alert)
	if len(s.alerts) <= s.failures {
		return errors.New("mail unavailable")
	}
	return nil
}

func setGmailRuntime(t *testing.T, values map[string]string) {
	t.Helper()
	previous := runtimeconfig.Snapshot()
	for key, value := range values {
		runtimeconfig.Set(key, value)
		key := key
		t.Cleanup(func() {
			if value, ok := previous[key]; ok {
				runtimeconfig.Set(key, value)
			} else {
				runtimeconfig.Delete(key)
			}
		})
	}
}

func TestSupplyMarginAndThreeCodeState(t *testing.T) {
	pay := decimal.RequireFromString("8")
	upstream := decimal.RequireFromString("5")
	points := decimal.RequireFromString("1.2")
	minimumMargin := decimal.RequireFromString("0.20")
	cost, allowed, margin, safe := calculateSupplyMargin(pay, upstream, points, minimumMargin)
	require.True(t, cost.Equal(decimal.RequireFromString("6")))
	require.True(t, allowed.Equal(decimal.RequireFromString("5.3333333333333333")))
	require.True(t, margin.Equal(decimal.RequireFromString("0.25")))
	require.True(t, safe)
	require.EqualValues(t, 3, affordableStock(10, decimal.RequireFromString("4.9"), decimal.RequireFromString("1.5")))
	require.EqualValues(t, 10, affordableStock(10, decimal.RequireFromString("100"), decimal.RequireFromString("1.5")))
	require.EqualValues(t, 10, affordableStock(10, decimal.Zero, decimal.Zero))

	for count := 1; count <= MaxCodes; count++ {
		action, status := nextCodeAction(count)
		if count < MaxCodes {
			require.Equal(t, ActionWaitNext, action)
			require.Equal(t, SessionActive, status)
		} else {
			require.Equal(t, ActionComplete, action)
			require.Equal(t, SessionCompleting, status)
		}
	}
}

func TestCreateSessionIsIdempotentAndBadKeyDoesNotRetry(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:gmail-session-idempotency?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&sessionModel{}))
	require.NoError(t, db.Exec("CREATE UNIQUE INDEX idx_test_gmail_order ON gmail_code_sessions(order_no)").Error)

	service := NewService(db, nil)
	cmd := tradeapp.GmailSessionCommand{OrderNo: "R-GMAIL-1", Quote: tradeapp.GmailSupplyQuote{
		Source: SourceSMSBower, ProviderServiceCode: "gm", UpstreamPrice: "1.2",
		PointsPerUnit: "1", CostPoints: "1.2", MaxPrice: "1.2",
	}}
	first, err := service.CreateSession(context.Background(), cmd)
	require.NoError(t, err)
	second, err := service.CreateSession(context.Background(), cmd)
	require.NoError(t, err)
	require.Equal(t, first, second)

	var count int64
	require.NoError(t, db.Model(&sessionModel{}).Count(&count).Error)
	require.EqualValues(t, 1, count)
	require.True(t, errors.Is(gmailSyncTaskError(ErrBadKey), asynq.SkipRetry))
	require.False(t, errors.Is(gmailSyncTaskError(ErrRemote), asynq.SkipRetry))
}

func TestPutMappingRoutesCodeAndPurchaseIndependently(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:gmail-route-modes?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec("CREATE TABLE project_products (id INTEGER PRIMARY KEY, project_id INTEGER NOT NULL, type TEXT NOT NULL)").Error)
	require.NoError(t, db.AutoMigrate(&serviceModel{}, &routeModel{}))
	require.NoError(t, db.Exec("CREATE UNIQUE INDEX idx_test_gmail_route ON gmail_supply_routes(project_id, source)").Error)
	require.NoError(t, db.Exec("INSERT INTO project_products(id, project_id, type) VALUES (71, 7, 'gmail')").Error)
	require.NoError(t, db.Create(&serviceModel{Code: "gm", Name: "Gmail"}).Error)

	service := NewService(db, nil)
	require.NoError(t, service.PutMapping(context.Background(), 7, SourceSMSBower, "gm", true, true, false))
	require.NoError(t, service.PutMapping(context.Background(), 7, SourceLocal, "", true, false, true))
	require.ErrorIs(t, service.PutMapping(context.Background(), 7, SourceSMSBower, "gm", true, true, true), ErrInvalidRoute)
	require.ErrorIs(t, service.PutMapping(context.Background(), 7, SourceLocal, "", true, true, false), ErrInvalidRoute)
	require.NoError(t, service.PutMapping(context.Background(), 7, SourceLocal, "", false, false, false))

	var routes []routeModel
	require.NoError(t, db.Order("source").Find(&routes).Error)
	require.Len(t, routes, 2)
	for _, route := range routes {
		if route.Source == SourceSMSBower {
			require.True(t, route.CodeEnabled)
			require.False(t, route.PurchaseEnabled)
		} else {
			require.False(t, route.CodeEnabled)
			require.False(t, route.PurchaseEnabled)
			require.False(t, route.Enabled)
		}
	}
	require.NoError(t, service.DeleteMapping(context.Background(), 7, SourceLocal))
	routes = nil
	require.NoError(t, db.Find(&routes).Error)
	require.Len(t, routes, 1)
	require.Equal(t, SourceSMSBower, routes[0].Source)
}

func TestLocalPurchaseSupplyUsesLocalCostProtectionOnly(t *testing.T) {
	setGmailRuntime(t, map[string]string{
		"smsbower_points_per_unit": "invalid",
		"smsbower_min_margin_rate": "0.10",
	})
	db, err := gorm.Open(sqlite.Open("file:gmail-local-supply?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`CREATE TABLE project_products (
		id INTEGER PRIMARY KEY, project_id INTEGER NOT NULL, type TEXT NOT NULL,
		purchase_supplier_price TEXT NOT NULL
	)`).Error)
	require.NoError(t, db.AutoMigrate(&accountStateModel{}, &serviceModel{}, &routeModel{}, &localResourceModel{}))
	require.NoError(t, db.Exec("INSERT INTO project_products(id, project_id, type, purchase_supplier_price) VALUES (71, 7, 'gmail', '1')").Error)
	require.NoError(t, db.Create(&accountStateModel{ID: 1, Generation: 1}).Error)
	require.NoError(t, db.Create(&routeModel{ProjectID: 7, Source: SourceLocal, Enabled: true, PurchaseEnabled: true}).Error)
	require.NoError(t, db.Create(&localResourceModel{
		Email: "local@gmail.com", Identity: "local@gmail.com", Password: "password",
		TwoFactorSecret: "JBSWY3DPEHPK3PXP", AppPassword: "abcdefghijklmnop", Status: LocalResourceAvailable,
	}).Error)

	service := NewService(db, nil)
	quote, err := service.CheckSupply(context.Background(), 7, tradedomain.ServiceModePurchase, "2")
	require.NoError(t, err)
	require.Equal(t, SourceLocal, quote.Source)
	require.Equal(t, "1.00", quote.CostPoints)
	_, err = service.CheckSupply(context.Background(), 7, tradedomain.ServiceModePurchase, "1.05")
	require.ErrorIs(t, err, tradedomain.ErrUpstreamPriceProtected)
}

func TestListInventorySeparatesModesAndKeepsUnroutedGmailAtZero(t *testing.T) {
	setGmailRuntime(t, map[string]string{
		"smsbower_enabled":               "true",
		"smsbower_api_key":               "secret",
		"smsbower_points_per_unit":       "1",
		"smsbower_min_margin_rate":       "0.10",
		"smsbower_sync_interval_minutes": "5",
	})
	db, err := gorm.Open(sqlite.Open("file:gmail-mode-inventory?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`CREATE TABLE project_products (
		id INTEGER PRIMARY KEY, project_id INTEGER NOT NULL, type TEXT NOT NULL,
		status TEXT NOT NULL, code_enabled BOOLEAN NOT NULL, purchase_enabled BOOLEAN NOT NULL,
		code_price TEXT NOT NULL, purchase_price TEXT NOT NULL, purchase_supplier_price TEXT NOT NULL
	)`).Error)
	require.NoError(t, db.Exec("CREATE TABLE user_groups (enabled BOOLEAN NOT NULL, price_discount_ratio TEXT NOT NULL)").Error)
	require.NoError(t, db.AutoMigrate(&accountStateModel{}, &serviceModel{}, &routeModel{}, &localResourceModel{}))
	now := time.Date(2026, 8, 1, 1, 0, 0, 0, time.UTC)
	require.NoError(t, db.Exec("INSERT INTO project_products(id, project_id, type, status, code_enabled, purchase_enabled, code_price, purchase_price, purchase_supplier_price) VALUES (71, 7, 'gmail', 'enabled', 1, 1, '2', '2', '1'), (81, 8, 'gmail', 'enabled', 1, 1, '2', '1.05', '1')").Error)
	require.NoError(t, db.Exec("INSERT INTO user_groups(enabled, price_discount_ratio) VALUES (1, '1')").Error)
	require.NoError(t, db.Create(&accountStateModel{ID: 1, Balance: "10", HealthStatus: "healthy", LastSuccessAt: &now, Generation: 1}).Error)
	require.NoError(t, db.Create(&serviceModel{Code: "gm", Name: "Gmail", GmailPrice: "1", GmailStock: 7, Active: true, LastSeenAt: now}).Error)
	require.NoError(t, db.Create(&routeModel{ProjectID: 7, Source: SourceSMSBower, ProviderServiceCode: "gm", Enabled: true, CodeEnabled: true}).Error)
	require.NoError(t, db.Create(&routeModel{ProjectID: 7, Source: SourceLocal, Enabled: true, PurchaseEnabled: true}).Error)
	require.NoError(t, db.Create(&routeModel{ProjectID: 8, Source: SourceLocal, Enabled: true, PurchaseEnabled: true}).Error)
	require.NoError(t, db.Create(&localResourceModel{
		Email: "local@gmail.com", Identity: "local@gmail.com", Password: "password",
		TwoFactorSecret: "JBSWY3DPEHPK3PXP", AppPassword: "abcdefghijklmnop", Status: LocalResourceAvailable,
	}).Error)
	service := NewService(db, nil)
	service.now = func() time.Time { return now }

	items, err := service.ListInventory(context.Background(), []uint{7, 8})
	require.NoError(t, err)
	require.ElementsMatch(t, []InventoryItem{
		{ProjectID: 7, ProductID: 71, CodeAvailable: 7, PurchaseAvailable: 1},
		{ProjectID: 8, ProductID: 81},
	}, items)
}

func TestProvisionClaimLeavesRecoveryLeaseAndStaleClaimRefunds(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:gmail-provision-recovery?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&sessionModel{}))
	now := time.Date(2026, 8, 1, 1, 2, 3, 0, time.UTC)
	session := sessionModel{OrderNo: "GMAIL-RECOVERY", Source: SourceSMSBower, Status: SessionPending, CodesJSON: []byte("[]"), Version: 1}
	require.NoError(t, db.Create(&session).Error)
	trade := &gmailTradeSpy{}
	service := NewService(db, nil)
	service.SetTrade(trade)
	service.now = func() time.Time { return now }

	claimed, ok, err := service.claimProvision(context.Background(), session.ID)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, SessionProvisioning, claimed.Status)
	require.NotNil(t, claimed.NextPollAt)
	require.Equal(t, now.Add(gmailProvisionLease), claimed.NextPollAt.UTC())

	now = now.Add(gmailProvisionLease)
	require.NoError(t, service.Provision(context.Background(), session.ID))
	require.NoError(t, db.First(&session, session.ID).Error)
	require.Equal(t, SessionUnknown, session.Status)
	require.Nil(t, session.NextPollAt)
	require.Equal(t, []string{"GMAIL-RECOVERY"}, trade.failed)
}

func TestRemoteTerminalStatusCompletesLocalAction(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("Bad actual activation status"))
	}))
	defer server.Close()
	setGmailRuntime(t, map[string]string{"smsbower_api_key": "secret"})

	db, err := gorm.Open(sqlite.Open("file:gmail-terminal-action?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&sessionModel{}))
	now := time.Date(2026, 8, 1, 2, 3, 4, 0, time.UTC)
	session := sessionModel{
		OrderNo: "GMAIL-COMPLETE", Source: SourceSMSBower, SourceRef: "41", Status: SessionCompleting,
		ReceivedCount: MaxCodes, CodesJSON: []byte("[]"), PendingRemoteAction: ActionComplete,
		NextPollAt: &now, Version: 1,
	}
	require.NoError(t, db.Create(&session).Error)
	trade := &gmailTradeSpy{}
	service := NewService(db, nil)
	service.client = newSMSBowerClient(server.URL, server.Client())
	service.SetTrade(trade)
	service.now = func() time.Time { return now }

	require.NoError(t, service.Poll(context.Background(), session.ID))
	sessionID := session.ID
	session = sessionModel{}
	require.NoError(t, db.First(&session, sessionID).Error)
	require.Equal(t, SessionCompleted, session.Status)
	require.Empty(t, session.PendingRemoteAction)
	require.Nil(t, session.NextPollAt)
	require.Equal(t, []string{"GMAIL-COMPLETE"}, trade.completed)
}

func TestCancelGmailOrderPersistsIntentBeforeScheduling(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":1}`))
	}))
	defer server.Close()
	setGmailRuntime(t, map[string]string{"smsbower_api_key": "secret"})

	db, err := gorm.Open(sqlite.Open("file:gmail-cancel-intent?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&sessionModel{}))
	now := time.Date(2026, 8, 1, 2, 30, 0, 0, time.UTC)
	session := sessionModel{
		OrderNo: "GMAIL-CANCEL", Source: SourceSMSBower, SourceRef: "43", Email: "demo@gmail.com",
		Status: SessionActive, CodesJSON: []byte("[]"), NextPollAt: &now, Version: 1,
	}
	require.NoError(t, db.Create(&session).Error)
	trade := &gmailTradeSpy{}
	service := NewService(db, nil)
	service.client = newSMSBowerClient(server.URL, server.Client())
	service.SetTrade(trade)
	service.now = func() time.Time { return now }

	require.Error(t, service.CancelGmailOrder(context.Background(), session.OrderNo))
	sessionID := session.ID
	session = sessionModel{}
	require.NoError(t, db.First(&session, sessionID).Error)
	require.Equal(t, SessionCancelling, session.Status)
	require.Equal(t, ActionCancel, session.PendingRemoteAction)
	require.Equal(t, now, session.NextPollAt.UTC())

	require.NoError(t, service.Poll(context.Background(), session.ID))
	require.Equal(t, []string{"GMAIL-CANCEL"}, trade.failed)
}

func TestMissingRemoteActivationSettlesByReceivedCodeCount(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("No activation found with such id"))
	}))
	defer server.Close()
	setGmailRuntime(t, map[string]string{"smsbower_api_key": "secret"})

	for _, test := range []struct {
		name          string
		receivedCount uint8
		wantStatus    string
		wantComplete  bool
	}{
		{name: "without code", wantStatus: SessionCancelled},
		{name: "with code", receivedCount: 1, wantStatus: SessionCompleted, wantComplete: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, err := gorm.Open(sqlite.Open("file:gmail-missing-"+test.name+"?mode=memory&cache=shared"), &gorm.Config{})
			require.NoError(t, err)
			require.NoError(t, db.AutoMigrate(&sessionModel{}))
			now := time.Date(2026, 8, 1, 3, 4, 5, 0, time.UTC)
			expiresAt := now.Add(time.Hour)
			session := sessionModel{
				OrderNo: "GMAIL-MISSING-" + test.name, Source: SourceSMSBower, SourceRef: "42",
				Email: "demo@gmail.com", Status: SessionActive, ReceivedCount: test.receivedCount,
				CodesJSON: []byte("[]"), StartedAt: &now, ExpiresAt: &expiresAt, NextPollAt: &now, Version: 1,
			}
			require.NoError(t, db.Create(&session).Error)
			trade := &gmailTradeSpy{}
			service := NewService(db, nil)
			service.client = newSMSBowerClient(server.URL, server.Client())
			service.SetTrade(trade)
			service.now = func() time.Time { return now }

			require.NoError(t, service.Poll(context.Background(), session.ID))
			require.NoError(t, db.First(&session, session.ID).Error)
			require.Equal(t, test.wantStatus, session.Status)
			if test.wantComplete {
				require.Equal(t, []string{session.OrderNo}, trade.completed)
				require.Empty(t, trade.failed)
			} else {
				require.Equal(t, []string{session.OrderNo}, trade.failed)
				require.Empty(t, trade.completed)
			}
		})
	}
}

func TestSyncRetriesUnnotifiedPriceChange(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/stubs/handler_api.php":
			if r.URL.Query().Get("action") == "getBalance" {
				_, _ = w.Write([]byte("ACCESS_BALANCE:100"))
				return
			}
			_, _ = w.Write([]byte(`{"status":1,"services":[{"code":"gm","name":"Gmail"}]}`))
		case "/api/mail/getPriceRests":
			_, _ = w.Write([]byte(`{"status":1,"data":{"gm":{"gmail.com":{"price":2,"count":10}}}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	setGmailRuntime(t, map[string]string{
		"smsbower_enabled":                   "true",
		"smsbower_api_key":                   "secret",
		"smsbower_balance_warning_threshold": "0",
	})

	db, err := gorm.Open(sqlite.Open("file:gmail-price-notify-retry?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&accountStateModel{}, &serviceModel{}, &routeModel{}))
	require.NoError(t, db.Create(&accountStateModel{ID: 1, Balance: "0", HealthStatus: "disabled", Generation: 1}).Error)
	require.NoError(t, db.Create(&serviceModel{Code: "gm", Name: "Gmail", GmailPrice: "1", GmailStock: 5, Active: true, LastSeenAt: time.Now().UTC()}).Error)
	require.NoError(t, db.Create(&routeModel{ProjectID: 7, Source: SourceSMSBower, ProviderServiceCode: "gm", Enabled: true, CodeEnabled: true}).Error)
	notifier := &gmailAlertNotifierSpy{failures: 1}
	service := NewService(db, nil)
	service.client = newSMSBowerClient(server.URL, server.Client())
	service.SetNotifier(notifier)

	require.Error(t, service.Sync(context.Background()))
	var stored serviceModel
	require.NoError(t, db.First(&stored, "code = ?", "gm").Error)
	require.True(t, parseDecimalOrZero(stored.GmailPrice).Equal(decimal.NewFromInt(2)))
	require.Nil(t, stored.LastNotifiedPrice)

	require.NoError(t, service.Sync(context.Background()))
	require.Len(t, notifier.alerts, 2)
	require.NoError(t, db.First(&stored, "code = ?", "gm").Error)
	require.NotNil(t, stored.LastNotifiedPrice)
	require.True(t, parseDecimalOrZero(*stored.LastNotifiedPrice).Equal(decimal.NewFromInt(2)))
}

func TestSyncPriceCycleCreatesDistinctNotificationEvents(t *testing.T) {
	price := "2"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/stubs/handler_api.php":
			if r.URL.Query().Get("action") == "getBalance" {
				_, _ = w.Write([]byte("ACCESS_BALANCE:100"))
				return
			}
			_, _ = w.Write([]byte(`{"status":1,"services":[{"code":"gm","name":"Gmail"}]}`))
		case "/api/mail/getPriceRests":
			_, _ = w.Write([]byte(`{"status":1,"data":{"gm":{"gmail.com":{"price":` + price + `,"count":10}}}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	setGmailRuntime(t, map[string]string{
		"smsbower_enabled": "true", "smsbower_api_key": "secret", "smsbower_balance_warning_threshold": "0",
	})

	db, err := gorm.Open(sqlite.Open("file:gmail-price-cycle?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&accountStateModel{}, &serviceModel{}, &routeModel{}))
	require.NoError(t, db.Create(&accountStateModel{ID: 1, Generation: 1}).Error)
	lastNotified := "1"
	require.NoError(t, db.Create(&serviceModel{
		Code: "gm", Name: "Gmail", GmailPrice: "1", GmailStock: 5, LastNotifiedPrice: &lastNotified,
		Active: true, LastSeenAt: time.Now().UTC(),
	}).Error)
	require.NoError(t, db.Create(&routeModel{ProjectID: 7, Source: SourceSMSBower, ProviderServiceCode: "gm", Enabled: true, CodeEnabled: true}).Error)
	notifier := &gmailAlertNotifierSpy{}
	now := time.Date(2026, 8, 1, 1, 0, 0, 0, time.UTC)
	service := NewService(db, nil)
	service.client = newSMSBowerClient(server.URL, server.Client())
	service.now = func() time.Time { return now }
	service.SetNotifier(notifier)

	for _, next := range []string{"2", "1", "2"} {
		price = next
		require.NoError(t, service.Sync(context.Background()))
		now = now.Add(time.Minute)
	}
	require.Len(t, notifier.alerts, 3)
	require.NotEqual(t, notifier.alerts[0].ID, notifier.alerts[2].ID)
}

func TestSyncRejectsMissingPriceForEnabledMapping(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/stubs/handler_api.php" && r.URL.Query().Get("action") == "getBalance" {
			_, _ = w.Write([]byte("ACCESS_BALANCE:100"))
			return
		}
		if r.URL.Path == "/stubs/handler_api.php" {
			_, _ = w.Write([]byte(`{"status":1,"services":[{"code":"gm","name":"Gmail"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":1,"data":{}}`))
	}))
	defer server.Close()
	setGmailRuntime(t, map[string]string{"smsbower_enabled": "true", "smsbower_api_key": "secret"})

	db, err := gorm.Open(sqlite.Open("file:gmail-price-partial?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&accountStateModel{}, &serviceModel{}, &routeModel{}))
	require.NoError(t, db.Create(&accountStateModel{ID: 1, Generation: 1}).Error)
	require.NoError(t, db.Create(&serviceModel{Code: "gm", Name: "Gmail", GmailPrice: "1", Active: true, LastSeenAt: time.Now().UTC()}).Error)
	require.NoError(t, db.Create(&routeModel{ProjectID: 7, Source: SourceSMSBower, ProviderServiceCode: "gm", Enabled: true, CodeEnabled: true}).Error)
	service := NewService(db, nil)
	service.client = newSMSBowerClient(server.URL, server.Client())

	require.Error(t, service.Sync(context.Background()))
	status, err := service.AccountStatus(context.Background())
	require.NoError(t, err)
	require.Equal(t, "degraded", status.HealthStatus)
	require.Nil(t, status.LastSuccessAt)
}

func TestFinanceUsesOrderRevenueAndConservativeSessionCost(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:gmail-finance?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&sessionModel{}, &serviceModel{}))
	require.NoError(t, db.Exec("CREATE TABLE projects (id INTEGER PRIMARY KEY, name TEXT NOT NULL)").Error)
	require.NoError(t, db.Exec(`CREATE TABLE orders (
		order_no TEXT PRIMARY KEY, project_id INTEGER NOT NULL, debit_tx_id INTEGER,
		product_type TEXT NOT NULL, pay_amount TEXT NOT NULL, refund_amount TEXT NOT NULL,
		gmail_resource_id INTEGER, gmail_cost_points_snapshot TEXT NOT NULL DEFAULT '0'
	)`).Error)
	require.NoError(t, db.Exec("INSERT INTO projects(id, name) VALUES (7, 'Gmail Project')").Error)
	require.NoError(t, db.Create(&serviceModel{Code: "gm", Name: "Gmail", GmailPrice: "1", Active: true, LastSeenAt: time.Now().UTC()}).Error)

	rows := []struct {
		orderNo string
		status  string
		pay     string
		refund  string
		cost    string
		count   uint8
	}{
		{orderNo: "FIN-1", status: SessionCompleted, pay: "10", refund: "0", cost: "4", count: 3},
		{orderNo: "FIN-2", status: SessionCancelled, pay: "8", refund: "8", cost: "3"},
		{orderNo: "FIN-3", status: SessionActive, pay: "6", refund: "0", cost: "2", count: 1},
		{orderNo: "FIN-4", status: SessionUnknown, pay: "5", refund: "5", cost: "1"},
	}
	for i, row := range rows {
		require.NoError(t, db.Exec(
			"INSERT INTO orders(order_no, project_id, debit_tx_id, product_type, pay_amount, refund_amount) VALUES (?, 7, ?, 'gmail', ?, ?)",
			row.orderNo, i+1, row.pay, row.refund,
		).Error)
		require.NoError(t, db.Create(&sessionModel{
			OrderNo: row.orderNo, Source: SourceSMSBower, ProviderServiceCode: "gm", Email: "demo@gmail.com",
			Status: row.status, ReceivedCount: row.count, CodesJSON: []byte("[]"), CostPointsSnapshot: row.cost, Version: 1,
		}).Error)
	}

	report, err := NewService(db, nil).Finance(context.Background())
	require.NoError(t, err)
	require.EqualValues(t, 4, report.Overview.OrderCount)
	require.Equal(t, "16.00", report.Overview.NetRevenue)
	require.Equal(t, "7.00", report.Overview.ConservativeCost)
	require.Equal(t, "9.00", report.Overview.ConservativeProfit)
	require.Equal(t, "0.5625", report.Overview.ConservativeMarginRate)
	require.Equal(t, []FinanceBreakdown{{
		Key: "7", Name: "Gmail Project", OrderCount: 4, NetRevenue: "16.00", Cost: "7.00", Profit: "9.00",
	}}, report.ByProject)
}
