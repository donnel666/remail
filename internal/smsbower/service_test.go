package smsbower

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/donnel666/remail/api/middleware"
	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	iamdomain "github.com/donnel666/remail/internal/iam/domain"
	"github.com/donnel666/remail/internal/platform"
	settingsapp "github.com/donnel666/remail/internal/systemsettings/app"
	settingsdomain "github.com/donnel666/remail/internal/systemsettings/domain"
	settingsinfra "github.com/donnel666/remail/internal/systemsettings/infra"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"github.com/donnel666/remail/internal/upstream"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type testProjectModel struct {
	ID         uint   `gorm:"column:id;primaryKey"`
	Name       string `gorm:"column:name"`
	Status     string `gorm:"column:status"`
	AccessType string `gorm:"column:access_type"`
}

func (testProjectModel) TableName() string { return "projects" }

type testProductModel struct {
	ID            uint   `gorm:"column:id;primaryKey"`
	ProjectID     uint   `gorm:"column:project_id"`
	Type          string `gorm:"column:type"`
	Status        string `gorm:"column:status"`
	CodeEnabled   bool   `gorm:"column:code_enabled"`
	CodePrice     string `gorm:"column:code_price"`
	PurchasePrice string `gorm:"column:purchase_price"`
}

func (testProductModel) TableName() string { return "project_products" }

type testProjectAccessModel struct {
	ProjectID uint `gorm:"column:project_id"`
	UserID    uint `gorm:"column:user_id"`
}

func (testProjectAccessModel) TableName() string { return "project_accesses" }

type testUserGroupModel struct {
	ID                 uint   `gorm:"column:id;primaryKey"`
	Enabled            bool   `gorm:"column:enabled"`
	PriceDiscountRatio string `gorm:"column:price_discount_ratio"`
}

func (testUserGroupModel) TableName() string { return "user_groups" }

type tradePortSpy struct {
	activations   int
	activation    upstream.Activation
	activationErr error
	completions   int
	failures      int
	failure       func()
	receiveUntil  time.Time
	receiveErr    error
}

func (s *tradePortSpy) ActivateUpstreamOrder(_ context.Context, activation upstream.Activation) error {
	s.activations++
	s.activation = activation
	return s.activationErr
}

func (s *tradePortSpy) CompleteGmailOrder(context.Context, string, string) error {
	s.completions++
	return nil
}

func (s *tradePortSpy) FailGmailOrder(context.Context, string, string) error {
	s.failures++
	if s.failure != nil {
		s.failure()
	}
	return nil
}

func (s *tradePortSpy) GmailOrderReceiveUntil(context.Context, string) (time.Time, error) {
	if s.receiveErr != nil {
		return time.Time{}, s.receiveErr
	}
	if !s.receiveUntil.IsZero() {
		return s.receiveUntil, nil
	}
	return time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC), nil
}

type notifierSpy struct {
	alerts int
	err    error
}

func (s *notifierSpy) NotifySMSBower(context.Context, Alert) error {
	s.alerts++
	return s.err
}

type operationLogSpy struct {
	items []*governancedomain.OperationLog
	err   error
	inTx  bool
}

type failingSettingsRepository struct {
	*settingsinfra.Repository
	err error
}

func (r *failingSettingsRepository) BulkUpsert(context.Context, []settingsdomain.Setting) ([]settingsdomain.Setting, error) {
	return nil, r.err
}

func (s *operationLogSpy) Create(ctx context.Context, log *governancedomain.OperationLog) error {
	_, s.inTx = platform.GormTxFromContext(ctx)
	s.items = append(s.items, log)
	return s.err
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func newServiceHarness(t *testing.T) (*Service, *gorm.DB, time.Time) {
	t.Helper()
	databaseName := strings.NewReplacer("/", "-", " ", "-").Replace(t.Name())
	db, err := gorm.Open(sqlite.Open("file:"+databaseName+"?mode=memory&cache=shared"), &gorm.Config{
		DisableForeignKeyConstraintWhenMigrating: true,
	})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&testProjectModel{}, &testProductModel{}, &testProjectAccessModel{}, &testUserGroupModel{},
		&orderGuardModel{}, &configModel{}, &accountStateModel{}, &serviceModel{}, &routeModel{}, &orderModel{},
	))
	now := time.Date(2026, 8, 5, 8, 0, 0, 0, time.UTC)
	lastSuccess := now.Add(-time.Minute)
	require.NoError(t, db.Create(&testProjectModel{ID: 1, Name: "Test Gmail", Status: "listed", AccessType: "public"}).Error)
	require.NoError(t, db.Create(&testProductModel{
		ID: 2, ProjectID: 1, Type: "gmail", Status: "enabled", CodeEnabled: true, CodePrice: "10", PurchasePrice: "20",
	}).Error)
	require.NoError(t, db.Create(&configModel{
		ID: 1, Enabled: true, APIKey: "secret", Strategy: string(upstream.StrategyUpstreamFirst),
		SyncIntervalMinutes: 5, BalanceWarningThreshold: "1", PointsPerUnit: "1", MinMarginRate: "0.10",
	}).Error)
	require.NoError(t, db.Create(&accountStateModel{
		ID: 1, Balance: "100", HealthStatus: "healthy", LastSuccessAt: &lastSuccess,
	}).Error)
	require.NoError(t, db.Create(&serviceModel{
		Code: "svc", Name: "Gmail", GmailPrice: "2", GmailStock: 10, Active: true, LastSeenAt: lastSuccess,
	}).Error)
	require.NoError(t, db.Create(&routeModel{ProjectID: 1, ServiceCode: "svc", Enabled: true}).Error)
	require.NoError(t, db.Create(&testUserGroupModel{ID: 1, Enabled: true, PriceDiscountRatio: "1"}).Error)
	service := NewService(db, nil)
	service.now = func() time.Time { return now }
	t.Cleanup(func() {
		sqlDB, sqlErr := db.DB()
		if sqlErr == nil {
			require.NoError(t, sqlDB.Close())
		}
	})
	return service, db, now
}

func TestAccountStatusLoadsPersistedState(t *testing.T) {
	service, _, now := newServiceHarness(t)

	status, err := service.AccountStatus(context.Background())

	require.NoError(t, err)
	require.True(t, status.Enabled)
	require.True(t, status.Configured)
	require.Equal(t, "100", status.Balance)
	require.Equal(t, "healthy", status.HealthStatus)
	require.NotNil(t, status.LastSuccessAt)
	require.Equal(t, now.Add(-time.Minute), *status.LastSuccessAt)
}

func TestPutMappingPreservesMappingWhileTogglingAvailability(t *testing.T) {
	service, db, _ := newServiceHarness(t)
	logs := &operationLogSpy{}
	service.SetOperationLogs(logs)
	meta := MutationMeta{OperatorUserID: 1, RequestID: "mapping-toggle", Path: "/admin/upstreams/smsbower/mappings/1"}

	require.NoError(t, service.PutMapping(context.Background(), 1, "svc", false, meta))
	var route routeModel
	require.NoError(t, db.First(&route, "project_id = ?", 1).Error)
	require.False(t, route.Enabled)

	mappings, err := service.ListMappings(context.Background())
	require.NoError(t, err)
	require.Len(t, mappings, 1)
	require.False(t, mappings[0].Enabled)

	require.NoError(t, service.PutMapping(context.Background(), 1, "svc", true, meta))
	require.NoError(t, db.First(&route, "project_id = ?", 1).Error)
	require.True(t, route.Enabled)
	require.Len(t, logs.items, 2)
}

func paidOrder(orderNo string) upstream.PaidOrder {
	return upstream.PaidOrder{
		OrderNo: orderNo, ProjectID: 1, ProductID: 2, BuyerID: 3,
		EmailType: upstream.EmailTypeGmail, OrderType: upstream.OrderTypeCode,
		PayAmount: "10", Selected: true,
	}
}

func paidOrderTxContext(tx *gorm.DB) context.Context {
	ctx, _ := platform.WithGormRollback(context.Background())
	return platform.WithGormTx(ctx, tx)
}

func createPendingProviderOrder(t *testing.T, db *gorm.DB, orderNo string, now time.Time) orderModel {
	t.Helper()
	nextPoll := now
	model := orderModel{
		OrderNo: orderNo, ProjectID: 1, ProductID: 2, ServiceCode: "svc",
		Status: StatusPending, CodesJSON: "[]", UpstreamPriceSnapshot: "2",
		PointsPerUnitSnapshot: "1", CostPointsSnapshot: "2", MaxPriceSnapshot: "2",
		NextPollAt: &nextPoll, Version: 1,
	}
	require.NoError(t, db.Create(&model).Error)
	return model
}

func TestAcceptPaidOrderRequiresAndSharesTradeTransaction(t *testing.T) {
	service, db, now := newServiceHarness(t)
	trade := &tradePortSpy{}
	service.SetTrade(trade)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		require.Equal(t, "/api/mail/getActivation", request.URL.Path)
		require.Equal(t, "svc", request.URL.Query().Get("service"))
		require.Equal(t, "2", request.URL.Query().Get("maxPrice"))
		calls.Add(1)
		_, _ = writer.Write([]byte(`{"status":1,"mail":"buyer@gmail.com","mailId":41}`))
	}))
	t.Cleanup(server.Close)
	service.client = newTestClient(server.URL, server.Client())

	handled, err := service.AcceptPaidOrder(context.Background(), paidOrder("ORDER-TX"))
	require.False(t, handled)
	require.ErrorIs(t, err, errPaidOrderTx)
	require.Zero(t, calls.Load())

	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		handled, acceptErr := service.AcceptPaidOrder(paidOrderTxContext(tx), paidOrder("ORDER-TX"))
		require.True(t, handled)
		return acceptErr
	}))
	var stored orderModel
	require.NoError(t, db.Where("order_no = ?", "ORDER-TX").Take(&stored).Error)
	require.Equal(t, StatusActive, stored.Status)
	require.Equal(t, "buyer@gmail.com", stored.Email)
	require.NotNil(t, stored.RemoteMailID)
	require.Equal(t, uint64(41), *stored.RemoteMailID)
	require.NotNil(t, stored.StartedAt)
	require.NotNil(t, stored.ExpiresAt)
	require.Equal(t, now, stored.StartedAt.UTC())
	require.Equal(t, now.Add(lifetime), stored.ExpiresAt.UTC())
	require.Equal(t, "2.00", stored.UpstreamPriceSnapshot)
	require.NoError(t, db.Where("order_no = ? AND type = ?", "ORDER-TX", "gmail").Take(&orderGuardModel{}).Error)
	require.Equal(t, int32(1), calls.Load())
	require.Equal(t, 1, trade.activations)
	require.Equal(t, "ORDER-TX", trade.activation.OrderNo)
	require.Equal(t, "buyer@gmail.com", trade.activation.Email)
}

func TestAcceptPaidOrderRemoteFailureRollsBackReservation(t *testing.T) {
	service, db, _ := newServiceHarness(t)
	service.SetTrade(&tradePortSpy{})
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte("NO_MAIL"))
	}))
	t.Cleanup(server.Close)
	service.client = newTestClient(server.URL, server.Client())

	err := db.Transaction(func(tx *gorm.DB) error {
		_, acceptErr := service.AcceptPaidOrder(paidOrderTxContext(tx), paidOrder("ORDER-NO-MAIL"))
		return acceptErr
	})
	require.ErrorIs(t, err, upstream.ErrUnavailable)
	var orderCount, guardCount int64
	require.NoError(t, db.Model(&orderModel{}).Where("order_no = ?", "ORDER-NO-MAIL").Count(&orderCount).Error)
	require.NoError(t, db.Model(&orderGuardModel{}).Where("order_no = ?", "ORDER-NO-MAIL").Count(&guardCount).Error)
	require.Zero(t, orderCount)
	require.Zero(t, guardCount)
}

func TestAcceptPaidOrderCancelsRemoteWhenLocalActivationFails(t *testing.T) {
	service, db, _ := newServiceHarness(t)
	wantErr := errors.New("activate trade order failed")
	service.SetTrade(&tradePortSpy{activationErr: wantErr})
	var cancelled atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/mail/getActivation":
			_, _ = writer.Write([]byte(`{"status":1,"mail":"buyer@gmail.com","mailId":42}`))
		case "/api/mail/setStatus":
			require.Equal(t, "42", request.URL.Query().Get("id"))
			require.Equal(t, "2", request.URL.Query().Get("status"))
			cancelled.Store(true)
			_, _ = writer.Write([]byte(`{"status":1}`))
		default:
			http.NotFound(writer, request)
		}
	}))
	t.Cleanup(server.Close)
	service.client = newTestClient(server.URL, server.Client())

	err := db.Transaction(func(tx *gorm.DB) error {
		_, acceptErr := service.AcceptPaidOrder(paidOrderTxContext(tx), paidOrder("ORDER-CALLBACK-FAIL"))
		return acceptErr
	})
	require.ErrorIs(t, err, wantErr)
	require.True(t, cancelled.Load())
	var orderCount int64
	require.NoError(t, db.Model(&orderModel{}).Where("order_no = ?", "ORDER-CALLBACK-FAIL").Count(&orderCount).Error)
	require.Zero(t, orderCount)
}

func TestAcceptPaidOrderCancelsRemoteWhenTransactionRollsBack(t *testing.T) {
	service, db, _ := newServiceHarness(t)
	service.SetTrade(&tradePortSpy{})
	var activationCalls, cancellationCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/mail/getActivation":
			activationCalls.Add(1)
			_, _ = writer.Write([]byte(`{"status":1,"mail":"rollback@gmail.com","mailId":44}`))
		case "/api/mail/setStatus":
			require.Equal(t, "44", request.URL.Query().Get("id"))
			require.Equal(t, "2", request.URL.Query().Get("status"))
			cancellationCalls.Add(1)
			_, _ = writer.Write([]byte(`{"status":1}`))
		default:
			http.NotFound(writer, request)
		}
	}))
	t.Cleanup(server.Close)
	service.client = newTestClient(server.URL, server.Client())
	wantRollback := errors.New("commit failed")
	var rollback func(context.Context) error

	err := db.Transaction(func(tx *gorm.DB) error {
		txCtx, runRollback := platform.WithGormRollback(context.Background())
		rollback = runRollback
		_, acceptErr := service.AcceptPaidOrder(platform.WithGormTx(txCtx, tx), paidOrder("ORDER-ROLLBACK"))
		if acceptErr != nil {
			return acceptErr
		}
		return wantRollback
	})
	require.ErrorIs(t, err, wantRollback)
	require.NoError(t, rollback(context.Background()))
	require.Equal(t, int32(1), activationCalls.Load())
	require.Equal(t, int32(1), cancellationCalls.Load())
	var orderCount int64
	require.NoError(t, db.Model(&orderModel{}).Where("order_no = ?", "ORDER-ROLLBACK").Count(&orderCount).Error)
	require.Zero(t, orderCount)
}

func TestUpdateConfigRejectsOversizedAPIKey(t *testing.T) {
	service, _, _ := newServiceHarness(t)
	_, err := service.UpdateConfig(context.Background(), ConfigUpdate{
		Enabled: true, APIKey: strings.Repeat("x", 513), Strategy: upstream.StrategyLocalFirst,
		SyncIntervalMinutes: 5, BalanceWarningThreshold: "1", PointsPerUnit: "1", MinMarginRate: "0.10",
	}, MutationMeta{})
	require.ErrorIs(t, err, ErrInvalidConfig)
}

func newSMSBowerConfigSettings(t *testing.T, db *gorm.DB) *settingsapp.SystemSettingsUseCase {
	t.Helper()
	require.NoError(t, db.AutoMigrate(&settingsinfra.SettingModel{}))
	repo := settingsinfra.NewRepository(db)
	_, err := repo.Upsert(context.Background(), runtimeconfig.SMSBowerNoCodeRefundTimeoutMinutesKey, "10")
	require.NoError(t, err)
	return settingsapp.NewSystemSettingsUseCase(repo, nil)
}

func TestConfigUpdateRollsBackWhenNoCodeRefundTimeoutWriteFails(t *testing.T) {
	service, db, _ := newServiceHarness(t)
	newSMSBowerConfigSettings(t, db)
	repo := settingsinfra.NewRepository(db)
	settings := settingsapp.NewSystemSettingsUseCase(&failingSettingsRepository{Repository: repo, err: errors.New("settings write failed")}, nil)
	service.SetOperationLogs(&operationLogSpy{})
	runtimeconfig.Set(runtimeconfig.SMSBowerNoCodeRefundTimeoutMinutesKey, "10")
	t.Cleanup(func() { runtimeconfig.Delete(runtimeconfig.SMSBowerNoCodeRefundTimeoutMinutesKey) })
	timeout := uint(9)

	_, err := (&handler{service: service, settings: settings}).updateConfig(context.Background(), ConfigUpdate{
		Enabled: true, Strategy: upstream.StrategyLocalFirst, SyncIntervalMinutes: 30,
		NoCodeRefundTimeoutMinutes: &timeout,
		BalanceWarningThreshold:    "1", PointsPerUnit: "1", MinMarginRate: "0.10",
	}, MutationMeta{OperatorUserID: 7, RequestID: "config-timeout", Path: "/v1/admin/upstreams/smsbower/config"})

	require.Error(t, err)
	var stored configModel
	require.NoError(t, db.First(&stored, "id = 1").Error)
	require.Equal(t, string(upstream.StrategyUpstreamFirst), stored.Strategy)
	require.Equal(t, uint(5), stored.SyncIntervalMinutes)
	setting, getErr := settings.Get(context.Background(), runtimeconfig.SMSBowerNoCodeRefundTimeoutMinutesKey)
	require.NoError(t, getErr)
	require.Equal(t, "10", setting.Value)
	require.Equal(t, "10", runtimeconfig.String(runtimeconfig.SMSBowerNoCodeRefundTimeoutMinutesKey, ""))
}

func TestAcceptPaidOrderDoesNotStealLocalGmailOwner(t *testing.T) {
	service, db, now := newServiceHarness(t)
	require.NoError(t, db.Create(&orderGuardModel{OrderNo: "ORDER-LOCAL", Type: "gmail", CreatedAt: now}).Error)

	err := db.Transaction(func(tx *gorm.DB) error {
		handled, acceptErr := service.AcceptPaidOrder(paidOrderTxContext(tx), paidOrder("ORDER-LOCAL"))
		require.True(t, handled)
		return acceptErr
	})
	require.ErrorIs(t, err, upstream.ErrUnavailable)
	var count int64
	require.NoError(t, db.Model(&orderModel{}).Where("order_no = ?", "ORDER-LOCAL").Count(&count).Error)
	require.Zero(t, count)
}

func TestAvailableSupplyReservesServiceStockAndSharedBalance(t *testing.T) {
	service, db, now := newServiceHarness(t)
	lastSuccess := now.Add(-time.Minute)
	require.NoError(t, db.Create(&orderModel{
		OrderNo: "ORDER-B", ProjectID: 1, ProductID: 2, ServiceCode: "other", Status: StatusPending,
		CodesJSON: "[]", UpstreamPriceSnapshot: "3", PointsPerUnitSnapshot: "1",
		CostPointsSnapshot: "3", MaxPriceSnapshot: "3", Version: 1, CreatedAt: now,
	}).Error)
	row := supplyRow{
		ServiceCode: "svc", Price: "2", Stock: 10, Balance: "5", LastSuccessAt: &lastSuccess,
	}

	available, balance, err := service.availableSupply(context.Background(), row)
	require.NoError(t, err)
	require.Equal(t, uint(1), available)
	require.Equal(t, "2", balance.String())

	require.NoError(t, db.Create(&orderModel{
		OrderNo: "ORDER-A", ProjectID: 1, ProductID: 2, ServiceCode: "svc", Status: StatusPending,
		CodesJSON: "[]", UpstreamPriceSnapshot: "2", PointsPerUnitSnapshot: "1",
		CostPointsSnapshot: "2", MaxPriceSnapshot: "2", Version: 1, CreatedAt: now,
	}).Error)
	available, balance, err = service.availableSupply(context.Background(), row)
	require.NoError(t, err)
	require.Zero(t, available)
	require.True(t, balance.IsZero())
}

func TestProvisionExplicitFailureRefundsWithoutRetryingRemote(t *testing.T) {
	service, db, now := newServiceHarness(t)
	trade := &tradePortSpy{}
	service.SetTrade(trade)
	requestQuery := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requestQuery <- request.URL.Query().Get("maxPrice")
		_, _ = writer.Write([]byte("NO_MAIL"))
	}))
	t.Cleanup(server.Close)
	service.client = newTestClient(server.URL, server.Client())
	order := createPendingProviderOrder(t, db, "ORDER-NO-MAIL", now)

	require.NoError(t, service.Provision(context.Background(), order.ID))
	require.Equal(t, "2", <-requestQuery)
	require.Equal(t, 1, trade.failures)
	var stored orderModel
	require.NoError(t, db.First(&stored, order.ID).Error)
	require.Equal(t, StatusFailed, stored.Status)
	require.Nil(t, stored.NextPollAt)
}

func TestProvisionUncertainResultHoldsForReviewWithoutRefund(t *testing.T) {
	service, db, now := newServiceHarness(t)
	trade := &tradePortSpy{}
	notifier := &notifierSpy{}
	service.SetTrade(trade)
	service.SetNotifier(notifier)
	notifier.err = errors.New("alert queue unavailable")
	var calls atomic.Int32
	service.client = newTestClient("https://example.invalid", &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return nil, errors.New("connection reset")
	})})
	order := createPendingProviderOrder(t, db, "ORDER-UNCERTAIN", now)

	require.Error(t, service.Provision(context.Background(), order.ID))
	require.Equal(t, int32(2), calls.Load())
	require.Zero(t, trade.failures)
	require.Equal(t, 1, notifier.alerts)
	var stored orderModel
	require.NoError(t, db.First(&stored, order.ID).Error)
	require.Equal(t, StatusUnknown, stored.Status)
	require.NotNil(t, stored.NextPollAt)

	notifier.err = nil
	require.NoError(t, service.Provision(context.Background(), order.ID))
	stored = orderModel{}
	require.NoError(t, db.First(&stored, order.ID).Error)
	require.Nil(t, stored.NextPollAt)
	require.Zero(t, trade.failures)
	require.Equal(t, 2, notifier.alerts)
	require.Equal(t, int32(2), calls.Load())
}

func TestProvisioningLeaseExpiryBecomesUnknownWithoutRemoteCall(t *testing.T) {
	service, db, now := newServiceHarness(t)
	trade := &tradePortSpy{}
	notifier := &notifierSpy{}
	service.SetTrade(trade)
	service.SetNotifier(notifier)
	service.client = nil
	order := createPendingProviderOrder(t, db, "ORDER-LEASE", now)
	past := now.Add(-time.Minute)
	require.NoError(t, db.Model(&orderModel{}).Where("id = ?", order.ID).Updates(map[string]any{
		"status": StatusProvisioning, "next_poll_at": past,
	}).Error)

	require.NoError(t, service.Provision(context.Background(), order.ID))
	require.Zero(t, trade.failures)
	require.Equal(t, 1, notifier.alerts)
	require.NoError(t, db.First(&order, order.ID).Error)
	require.Equal(t, StatusUnknown, order.Status)
}

func TestCancelPendingOrderDoesNotCallRemote(t *testing.T) {
	service, db, now := newServiceHarness(t)
	service.client = nil
	order := createPendingProviderOrder(t, db, "ORDER-CANCEL", now)

	handled, err := service.CancelOrder(context.Background(), order.OrderNo)
	require.NoError(t, err)
	require.True(t, handled)
	var stored orderModel
	require.NoError(t, db.First(&stored, order.ID).Error)
	require.Equal(t, StatusCancelled, stored.Status)
	require.Nil(t, stored.NextPollAt)
}

func TestCancelActiveOrderConfirmsUpstreamBeforeReturning(t *testing.T) {
	service, db, now := newServiceHarness(t)
	var cancellationCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		require.Equal(t, "/api/mail/setStatus", request.URL.Path)
		require.Equal(t, "2", request.URL.Query().Get("status"))
		cancellationCalls.Add(1)
		_, _ = writer.Write([]byte(`{"status":1}`))
	}))
	t.Cleanup(server.Close)
	service.client = newTestClient(server.URL, server.Client())
	mailID := uint64(44)
	nextPoll := now
	order := orderModel{
		OrderNo: "ORDER-CANCEL-ACTIVE", ProjectID: 1, ProductID: 2, ServiceCode: "svc",
		RemoteMailID: &mailID, Email: "active@gmail.com", Status: StatusActive, CodesJSON: "[]",
		UpstreamPriceSnapshot: "2", PointsPerUnitSnapshot: "1", CostPointsSnapshot: "2", MaxPriceSnapshot: "2",
		NextPollAt: &nextPoll, Version: 1,
	}
	require.NoError(t, db.Create(&order).Error)

	handled, err := service.CancelOrder(context.Background(), order.OrderNo)

	require.NoError(t, err)
	require.True(t, handled)
	require.Equal(t, int32(1), cancellationCalls.Load())
	var stored orderModel
	require.NoError(t, db.First(&stored, order.ID).Error)
	require.Equal(t, StatusCancelled, stored.Status)
	require.Empty(t, stored.PendingRemoteAction)
	require.NotNil(t, stored.NextPollAt)
}

func TestReservationsKeepUnpurchasedOrdersAcrossSyncBoundary(t *testing.T) {
	service, db, now := newServiceHarness(t)
	lastSuccess := now.Add(-time.Minute)
	old := lastSuccess.Add(-time.Minute)
	for index, status := range []string{StatusPending, StatusProvisioning, StatusUnknown, StatusActive} {
		require.NoError(t, db.Create(&orderModel{
			OrderNo: fmt.Sprintf("ORDER-RESERVE-%d", index), ProjectID: 1, ProductID: 2,
			ServiceCode: "svc", Status: status, CodesJSON: "[]", UpstreamPriceSnapshot: "2",
			PointsPerUnitSnapshot: "1", CostPointsSnapshot: "2", MaxPriceSnapshot: "2",
			Version: 1, CreatedAt: old,
		}).Error)
	}

	available, balance, err := service.availableSupply(context.Background(), supplyRow{
		ServiceCode: "svc", Price: "2", Stock: 10, Balance: "10", LastSuccessAt: &lastSuccess,
	})
	require.NoError(t, err)
	require.Equal(t, uint(2), available)
	require.Equal(t, "4", balance.String())

	items, err := service.ListInventory(context.Background(), []uint{1})
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.EqualValues(t, 7, items[0].CodeAvailable)
}

func TestPollClaimUsesDurableOrderLease(t *testing.T) {
	service, db, now := newServiceHarness(t)
	mailID := uint64(41)
	nextPoll := now
	order := orderModel{
		OrderNo: "ORDER-POLL-LEASE", ProjectID: 1, ProductID: 2, ServiceCode: "svc",
		RemoteMailID: &mailID, Email: "buyer@gmail.com", Status: StatusActive, CodesJSON: "[]",
		UpstreamPriceSnapshot: "2", PointsPerUnitSnapshot: "1", CostPointsSnapshot: "2", MaxPriceSnapshot: "2",
		NextPollAt: &nextPoll, Version: 1,
	}
	require.NoError(t, db.Create(&order).Error)

	claimedOrder, claimed, err := service.claimPoll(context.Background(), order.ID)
	require.NoError(t, err)
	require.True(t, claimed)
	require.Equal(t, now.Add(pollLease), claimedOrder.NextPollAt.UTC())

	_, claimed, err = service.claimPoll(context.Background(), order.ID)
	require.NoError(t, err)
	require.False(t, claimed)
}

func TestPollUsesUpstreamTerminalStateAfterReceivingCode(t *testing.T) {
	service, db, now := newServiceHarness(t)
	trade := &tradePortSpy{}
	service.SetTrade(trade)
	var codeCalls, statusCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/mail/getCode":
			codeCalls.Add(1)
			_, _ = writer.Write([]byte("Activation is already canceled"))
		case "/api/mail/setStatus":
			statusCalls.Add(1)
			_, _ = writer.Write([]byte(`{"status":1}`))
		default:
			http.NotFound(writer, request)
		}
	}))
	t.Cleanup(server.Close)
	service.client = newTestClient(server.URL, server.Client())
	mailID := uint64(45)
	startedAt := now.Add(-25 * time.Hour)
	expiresAt := now.Add(-time.Hour)
	nextPoll := now
	order := orderModel{
		OrderNo: "ORDER-UPSTREAM-ENDED", ProjectID: 1, ProductID: 2, ServiceCode: "svc",
		RemoteMailID: &mailID, Email: "ended@gmail.com", Status: StatusActive, ReceivedCount: 1,
		CodesJSON:             `[{"seq":1,"code":"123456","receivedAt":"2026-08-05T08:00:00Z"}]`,
		UpstreamPriceSnapshot: "2", PointsPerUnitSnapshot: "1", CostPointsSnapshot: "2", MaxPriceSnapshot: "2",
		NextPollAt: &nextPoll, StartedAt: &startedAt, ExpiresAt: &expiresAt, Version: 1,
	}
	require.NoError(t, db.Create(&order).Error)

	require.NoError(t, service.Poll(context.Background(), order.ID))

	require.Equal(t, int32(1), codeCalls.Load())
	require.Zero(t, statusCalls.Load())
	require.Equal(t, 1, trade.completions)
	var stored orderModel
	require.NoError(t, db.First(&stored, order.ID).Error)
	require.Equal(t, StatusCompleted, stored.Status)
	require.Nil(t, stored.NextPollAt)
}

func TestPollHoldsUnconfirmedZeroCodeActivationForReview(t *testing.T) {
	for _, test := range []struct {
		name string
		body string
	}{
		{name: "already cancelled", body: "Activation is already canceled"},
		{name: "missing", body: "No activation found with such id"},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, db, now := newServiceHarness(t)
			trade := &tradePortSpy{}
			notifier := &notifierSpy{}
			service.SetTrade(trade)
			service.SetNotifier(notifier)
			var statusCalls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				switch request.URL.Path {
				case "/api/mail/getCode":
					_, _ = writer.Write([]byte(test.body))
				case "/api/mail/setStatus":
					statusCalls.Add(1)
					_, _ = writer.Write([]byte(`{"status":1}`))
				default:
					http.NotFound(writer, request)
				}
			}))
			t.Cleanup(server.Close)
			service.client = newTestClient(server.URL, server.Client())
			mailID := uint64(46)
			expiresAt := now.Add(time.Hour)
			nextPoll := now
			order := orderModel{
				OrderNo: "ORDER-UPSTREAM-" + strings.ReplaceAll(test.name, " ", "-"), ProjectID: 1, ProductID: 2, ServiceCode: "svc",
				RemoteMailID: &mailID, Email: "missing@gmail.com", Status: StatusActive, CodesJSON: "[]",
				UpstreamPriceSnapshot: "2", PointsPerUnitSnapshot: "1", CostPointsSnapshot: "2", MaxPriceSnapshot: "2",
				NextPollAt: &nextPoll, StartedAt: &now, ExpiresAt: &expiresAt, Version: 1,
			}
			require.NoError(t, db.Create(&order).Error)

			require.NoError(t, service.Poll(context.Background(), order.ID))

			require.Zero(t, statusCalls.Load())
			require.Zero(t, trade.failures)
			require.Equal(t, 1, notifier.alerts)
			var stored orderModel
			require.NoError(t, db.First(&stored, order.ID).Error)
			require.Equal(t, StatusCancelling, stored.Status)
			require.Equal(t, ActionCancel, stored.PendingRemoteAction)
			require.Contains(t, stored.LastSafeError, "尚未退款")
			require.Nil(t, stored.NextPollAt)
		})
	}
}

func TestPollCancelsUpstreamAndRefundsAfterNoCodeTimeout(t *testing.T) {
	service, db, now := newServiceHarness(t)
	var codeCalls, cancelCalls atomic.Int32
	trade := &tradePortSpy{
		receiveUntil: now.Add(-time.Second),
		failure:      func() { require.Equal(t, int32(1), cancelCalls.Load()) },
	}
	service.SetTrade(trade)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/mail/getCode":
			codeCalls.Add(1)
			_, _ = writer.Write([]byte("Code has not been received yet, please try again later"))
		case "/api/mail/setStatus":
			require.Equal(t, "2", request.URL.Query().Get("status"))
			cancelCalls.Add(1)
			_, _ = writer.Write([]byte(`{"status":1}`))
		default:
			http.NotFound(writer, request)
		}
	}))
	t.Cleanup(server.Close)
	service.client = newTestClient(server.URL, server.Client())
	mailID := uint64(47)
	startedAt := now.Add(-4 * time.Minute)
	expiresAt := now.Add(lifetime)
	nextPoll := now
	order := orderModel{
		OrderNo: "ORDER-NO-CODE-TIMEOUT", ProjectID: 1, ProductID: 2, ServiceCode: "svc",
		RemoteMailID: &mailID, Email: "timeout@gmail.com", Status: StatusActive, CodesJSON: "[]",
		UpstreamPriceSnapshot: "2", PointsPerUnitSnapshot: "1", CostPointsSnapshot: "2",
		MaxPriceSnapshot: "2", NextPollAt: &nextPoll, StartedAt: &startedAt, ExpiresAt: &expiresAt, Version: 1,
	}
	require.NoError(t, db.Create(&order).Error)

	require.NoError(t, service.Poll(context.Background(), order.ID))
	require.Zero(t, codeCalls.Load())
	require.Equal(t, int32(1), cancelCalls.Load())
	require.Equal(t, 1, trade.failures)
	var stored orderModel
	require.NoError(t, db.First(&stored, order.ID).Error)
	require.Equal(t, StatusCancelled, stored.Status)
	require.Empty(t, stored.PendingRemoteAction)
	require.Nil(t, stored.NextPollAt)
}

func TestPollHoldsUnconfirmedUpstreamCancellationForReview(t *testing.T) {
	for _, test := range []struct {
		name string
		body string
	}{
		{name: "already cancelled", body: "Activation is already canceled"},
		{name: "missing", body: "No activation found with such id"},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, db, now := newServiceHarness(t)
			trade := &tradePortSpy{receiveUntil: now.Add(-time.Second)}
			notifier := &notifierSpy{}
			service.SetTrade(trade)
			service.SetNotifier(notifier)
			var cancelCalls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				require.Equal(t, "/api/mail/setStatus", request.URL.Path)
				require.Equal(t, "2", request.URL.Query().Get("status"))
				cancelCalls.Add(1)
				_, _ = writer.Write([]byte(test.body))
			}))
			t.Cleanup(server.Close)
			service.client = newTestClient(server.URL, server.Client())
			mailID := uint64(47)
			startedAt := now.Add(-4 * time.Minute)
			expiresAt := now.Add(lifetime)
			nextPoll := now
			order := orderModel{
				OrderNo: "ORDER-CANCEL-" + strings.ReplaceAll(test.name, " ", "-"), ProjectID: 1, ProductID: 2, ServiceCode: "svc",
				RemoteMailID: &mailID, Email: "timeout@gmail.com", Status: StatusActive, CodesJSON: "[]",
				UpstreamPriceSnapshot: "2", PointsPerUnitSnapshot: "1", CostPointsSnapshot: "2",
				MaxPriceSnapshot: "2", NextPollAt: &nextPoll, StartedAt: &startedAt, ExpiresAt: &expiresAt, Version: 1,
			}
			require.NoError(t, db.Create(&order).Error)

			require.NoError(t, service.Poll(context.Background(), order.ID))

			require.Equal(t, int32(1), cancelCalls.Load())
			require.Zero(t, trade.failures)
			require.Equal(t, 1, notifier.alerts)
			var stored orderModel
			require.NoError(t, db.First(&stored, order.ID).Error)
			require.Equal(t, StatusCancelling, stored.Status)
			require.Equal(t, ActionCancel, stored.PendingRemoteAction)
			require.Contains(t, stored.LastSafeError, "尚未退款")
			require.Nil(t, stored.NextPollAt)
		})
	}
}

func TestPollRetriesCancellationReviewWhenNotificationFails(t *testing.T) {
	service, db, now := newServiceHarness(t)
	current := now
	service.now = func() time.Time { return current }
	trade := &tradePortSpy{receiveUntil: now.Add(-time.Second)}
	notifier := &notifierSpy{err: errors.New("alert queue unavailable")}
	service.SetTrade(trade)
	service.SetNotifier(notifier)
	var cancellationCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		require.Equal(t, "/api/mail/setStatus", request.URL.Path)
		cancellationCalls.Add(1)
		_, _ = writer.Write([]byte("Activation is already canceled"))
	}))
	t.Cleanup(server.Close)
	service.client = newTestClient(server.URL, server.Client())
	mailID := uint64(48)
	expiresAt := now.Add(lifetime)
	nextPoll := now
	order := orderModel{
		OrderNo: "ORDER-CANCEL-NOTIFY-RETRY", ProjectID: 1, ProductID: 2, ServiceCode: "svc",
		RemoteMailID: &mailID, Email: "retry@gmail.com", Status: StatusActive, CodesJSON: "[]",
		UpstreamPriceSnapshot: "2", PointsPerUnitSnapshot: "1", CostPointsSnapshot: "2", MaxPriceSnapshot: "2",
		NextPollAt: &nextPoll, StartedAt: &now, ExpiresAt: &expiresAt, Version: 1,
	}
	require.NoError(t, db.Create(&order).Error)

	require.Error(t, service.Poll(context.Background(), order.ID))
	var stored orderModel
	require.NoError(t, db.First(&stored, order.ID).Error)
	require.NotNil(t, stored.NextPollAt)

	notifier.err = nil
	current = current.Add(pollLease)
	require.NoError(t, service.Poll(context.Background(), order.ID))
	stored = orderModel{}
	require.NoError(t, db.First(&stored, order.ID).Error)
	require.Nil(t, stored.NextPollAt)
	require.Equal(t, 2, notifier.alerts)
	require.Equal(t, int32(2), cancellationCalls.Load())
	require.Zero(t, trade.failures)
}

func TestPollUsesPersistedReceiveDeadline(t *testing.T) {
	service, db, now := newServiceHarness(t)
	trade := &tradePortSpy{receiveUntil: now.Add(time.Minute)}
	service.SetTrade(trade)
	var codeCalls, cancellationCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/mail/getCode":
			codeCalls.Add(1)
			_, _ = writer.Write([]byte("Code has not been received yet, please try again later"))
		case "/api/mail/setStatus":
			cancellationCalls.Add(1)
			_, _ = writer.Write([]byte(`{"status":1}`))
		default:
			http.NotFound(writer, request)
		}
	}))
	t.Cleanup(server.Close)
	service.client = newTestClient(server.URL, server.Client())
	mailID := uint64(49)
	startedAt := now.Add(-24 * time.Hour)
	expiresAt := now.Add(lifetime)
	nextPoll := now
	order := orderModel{
		OrderNo: "ORDER-PERSISTED-DEADLINE", ProjectID: 1, ProductID: 2, ServiceCode: "svc",
		RemoteMailID: &mailID, Email: "deadline@gmail.com", Status: StatusActive, CodesJSON: "[]",
		UpstreamPriceSnapshot: "2", PointsPerUnitSnapshot: "1", CostPointsSnapshot: "2", MaxPriceSnapshot: "2",
		NextPollAt: &nextPoll, StartedAt: &startedAt, ExpiresAt: &expiresAt, Version: 1,
	}
	require.NoError(t, db.Create(&order).Error)

	require.NoError(t, service.Poll(context.Background(), order.ID))
	require.Equal(t, int32(1), codeCalls.Load())
	require.Zero(t, cancellationCalls.Load())
}

func TestProvisionKeepsRemoteActivationWhenCancellationWinsStateRace(t *testing.T) {
	service, db, now := newServiceHarness(t)
	service.SetTrade(&tradePortSpy{})
	service.SetNotifier(&notifierSpy{})
	requestStarted := make(chan struct{})
	releaseRequest := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/mail/getActivation" {
			http.NotFound(writer, request)
			return
		}
		close(requestStarted)
		<-releaseRequest
		_, _ = writer.Write([]byte(`{"status":1,"mail":"race@gmail.com","mailId":91}`))
	}))
	t.Cleanup(server.Close)
	service.client = newTestClient(server.URL, server.Client())
	order := createPendingProviderOrder(t, db, "ORDER-CANCEL-RACE", now)

	provisionErr := make(chan error, 1)
	go func() { provisionErr <- service.Provision(context.Background(), order.ID) }()
	<-requestStarted
	handled, err := service.CancelOrder(context.Background(), order.OrderNo)
	require.NoError(t, err)
	require.True(t, handled)
	close(releaseRequest)
	require.Error(t, <-provisionErr)

	var stored orderModel
	require.NoError(t, db.First(&stored, order.ID).Error)
	require.Equal(t, StatusCancelling, stored.Status)
	require.Equal(t, ActionCancel, stored.PendingRemoteAction)
	require.NotNil(t, stored.RemoteMailID)
	require.Equal(t, uint64(91), *stored.RemoteMailID)
	require.Equal(t, "race@gmail.com", stored.Email)
	require.NotNil(t, stored.NextPollAt)
}

func TestSyncIgnoresDisabledRoutesMissingFromRemotePrices(t *testing.T) {
	service, db, _ := newServiceHarness(t)
	require.NoError(t, db.Create(&routeModel{ProjectID: 99, ServiceCode: "missing", Enabled: false}).Error)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/stubs/handler_api.php":
			if request.URL.Query().Get("action") == "getBalance" {
				_, _ = writer.Write([]byte("ACCESS_BALANCE:100"))
				return
			}
			_, _ = writer.Write([]byte(`{"status":1,"services":[{"code":"svc","name":"Gmail"}]}`))
		case "/api/mail/getPriceRests":
			_, _ = writer.Write([]byte(`{"status":1,"data":{"svc":{"gmail.com":{"price":2,"count":10}}}}`))
		default:
			http.NotFound(writer, request)
		}
	}))
	t.Cleanup(server.Close)
	service.client = newTestClient(server.URL, server.Client())

	require.NoError(t, service.Sync(context.Background()))
}

func TestSMSBowerMutationsWriteSafeAuditInSameTransaction(t *testing.T) {
	service, db, _ := newServiceHarness(t)
	auditFailure := errors.New("audit unavailable")
	logs := &operationLogSpy{err: auditFailure}
	service.SetOperationLogs(logs)
	meta := MutationMeta{OperatorUserID: 7, RequestID: "request-1", Path: "/v1/admin/upstreams/smsbower/config"}

	_, err := service.UpdateConfig(context.Background(), ConfigUpdate{
		Enabled: true, APIKey: "new-secret", Strategy: upstream.StrategyLocalFirst,
		SyncIntervalMinutes: 5, BalanceWarningThreshold: "1", PointsPerUnit: "1", MinMarginRate: "0.10",
	}, meta)
	require.ErrorIs(t, err, auditFailure)
	require.True(t, logs.inTx)
	require.Len(t, logs.items, 1)
	require.NotContains(t, logs.items[0].SafeSummary, "new-secret")
	var config configModel
	require.NoError(t, db.First(&config, "id = 1").Error)
	require.Equal(t, "secret", config.APIKey)

	logs.err = nil
	_, err = service.UpdateConfig(context.Background(), ConfigUpdate{
		Enabled: true, APIKey: "new-secret", Strategy: upstream.StrategyLocalFirst,
		SyncIntervalMinutes: 5, BalanceWarningThreshold: "1", PointsPerUnit: "1", MinMarginRate: "0.10",
	}, meta)
	require.NoError(t, err)
	require.Equal(t, "smsbower.config.update", logs.items[1].OperationType)
	require.NotContains(t, logs.items[1].SafeSummary, "new-secret")
}

func TestPutConfigReturnsCommittedResultWhenSyncQueueIsUnavailable(t *testing.T) {
	service, db, _ := newServiceHarness(t)
	settings := newSMSBowerConfigSettings(t, db)
	logs := &operationLogSpy{}
	service.SetOperationLogs(logs)
	runtimeconfig.Set(runtimeconfig.SMSBowerNoCodeRefundTimeoutMinutesKey, "10")
	t.Cleanup(func() { runtimeconfig.Delete(runtimeconfig.SMSBowerNoCodeRefundTimeoutMinutesKey) })
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodPut, "/v1/admin/upstreams/smsbower/config", strings.NewReader(`{
		"enabled":true,"apiKey":"","strategy":"local_first","syncIntervalMinutes":5,
		"noCodeRefundTimeoutMinutes":9,"balanceWarningThreshold":"1","pointsPerUnit":"1","minMarginRate":"0.10"
	}`))
	context.Request.Header.Set("Content-Type", "application/json")
	context.Set("request_id", "request-2")
	middleware.SetCurrentUser(context, 7, iamdomain.RoleAdmin, "admin@example.com", "session")

	(&handler{service: service, settings: settings}).putConfig(context)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Len(t, logs.items, 1)
	require.Equal(t, uint(7), logs.items[0].OperatorUserID)
	require.Contains(t, recorder.Body.String(), `"noCodeRefundTimeoutMinutes":9`)
	setting, err := settings.Get(context.Request.Context(), runtimeconfig.SMSBowerNoCodeRefundTimeoutMinutesKey)
	require.NoError(t, err)
	require.Equal(t, "9", setting.Value)
}
