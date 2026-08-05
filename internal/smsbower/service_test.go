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
	"github.com/donnel666/remail/internal/upstream"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type testProjectModel struct {
	ID         uint   `gorm:"column:id;primaryKey"`
	Status     string `gorm:"column:status"`
	AccessType string `gorm:"column:access_type"`
}

func (testProjectModel) TableName() string { return "projects" }

type testProductModel struct {
	ID          uint   `gorm:"column:id;primaryKey"`
	ProjectID   uint   `gorm:"column:project_id"`
	Type        string `gorm:"column:type"`
	Status      string `gorm:"column:status"`
	CodeEnabled bool   `gorm:"column:code_enabled"`
	CodePrice   string `gorm:"column:code_price"`
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
	activations int
	completions int
	failures    int
}

func (s *tradePortSpy) ActivateUpstreamOrder(context.Context, upstream.Activation) error {
	s.activations++
	return nil
}

func (s *tradePortSpy) CompleteGmailOrder(context.Context, string, string) error {
	s.completions++
	return nil
}

func (s *tradePortSpy) FailGmailOrder(context.Context, string, string) error {
	s.failures++
	return nil
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
	require.NoError(t, db.Create(&testProjectModel{ID: 1, Status: "listed", AccessType: "public"}).Error)
	require.NoError(t, db.Create(&testProductModel{
		ID: 2, ProjectID: 1, Type: "gmail", Status: "enabled", CodeEnabled: true, CodePrice: "10",
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

func paidOrder(orderNo string) upstream.PaidOrder {
	return upstream.PaidOrder{
		OrderNo: orderNo, ProjectID: 1, ProductID: 2, BuyerID: 3,
		EmailType: upstream.EmailTypeGmail, OrderType: upstream.OrderTypeCode,
		PayAmount: "10", Selected: true,
	}
}

func createPendingProviderOrder(t *testing.T, db *gorm.DB, orderNo string, now time.Time) orderModel {
	t.Helper()
	nextPoll := now
	model := orderModel{
		OrderNo: orderNo, ProjectID: 1, ProductID: 2, ServiceCode: "svc",
		Status: StatusPending, CodesJSON: []byte("[]"), UpstreamPriceSnapshot: "2",
		PointsPerUnitSnapshot: "1", CostPointsSnapshot: "2", MaxPriceSnapshot: "2",
		NextPollAt: &nextPoll, Version: 1,
	}
	require.NoError(t, db.Create(&model).Error)
	return model
}

func TestAcceptPaidOrderRequiresAndSharesTradeTransaction(t *testing.T) {
	service, db, _ := newServiceHarness(t)
	service.client = nil

	handled, err := service.AcceptPaidOrder(context.Background(), paidOrder("ORDER-TX"))
	require.False(t, handled)
	require.ErrorIs(t, err, errPaidOrderTx)

	rollback := errors.New("rollback test")
	err = db.Transaction(func(tx *gorm.DB) error {
		handled, acceptErr := service.AcceptPaidOrder(platform.WithGormTx(context.Background(), tx), paidOrder("ORDER-TX"))
		if acceptErr != nil {
			return acceptErr
		}
		if !handled {
			return errors.New("provider did not handle selected order")
		}
		return rollback
	})
	require.ErrorIs(t, err, rollback)
	var orderCount, guardCount int64
	require.NoError(t, db.Model(&orderModel{}).Where("order_no = ?", "ORDER-TX").Count(&orderCount).Error)
	require.NoError(t, db.Model(&orderGuardModel{}).Where("order_no = ?", "ORDER-TX").Count(&guardCount).Error)
	require.Zero(t, orderCount)
	require.Zero(t, guardCount)

	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		handled, acceptErr := service.AcceptPaidOrder(platform.WithGormTx(context.Background(), tx), paidOrder("ORDER-TX"))
		require.True(t, handled)
		return acceptErr
	}))
	var stored orderModel
	require.NoError(t, db.Where("order_no = ?", "ORDER-TX").Take(&stored).Error)
	require.Equal(t, StatusPending, stored.Status)
	require.Equal(t, "2.00", stored.UpstreamPriceSnapshot)
	require.NoError(t, db.Where("order_no = ? AND type = ?", "ORDER-TX", "gmail").Take(&orderGuardModel{}).Error)
}

func TestUpdateConfigRejectsOversizedAPIKey(t *testing.T) {
	service, _, _ := newServiceHarness(t)
	_, err := service.UpdateConfig(context.Background(), ConfigUpdate{
		Enabled: true, APIKey: strings.Repeat("x", 513), Strategy: upstream.StrategyLocalFirst,
		SyncIntervalMinutes: 5, BalanceWarningThreshold: "1", PointsPerUnit: "1", MinMarginRate: "0.10",
	}, MutationMeta{})
	require.ErrorIs(t, err, ErrInvalidConfig)
}

func TestAcceptPaidOrderDoesNotStealLocalGmailOwner(t *testing.T) {
	service, db, now := newServiceHarness(t)
	require.NoError(t, db.Create(&orderGuardModel{OrderNo: "ORDER-LOCAL", Type: "gmail", CreatedAt: now}).Error)

	err := db.Transaction(func(tx *gorm.DB) error {
		handled, acceptErr := service.AcceptPaidOrder(platform.WithGormTx(context.Background(), tx), paidOrder("ORDER-LOCAL"))
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
		CodesJSON: []byte("[]"), UpstreamPriceSnapshot: "3", PointsPerUnitSnapshot: "1",
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
		CodesJSON: []byte("[]"), UpstreamPriceSnapshot: "2", PointsPerUnitSnapshot: "1",
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

func TestProvisionUncertainResultStopsAutomaticRepurchase(t *testing.T) {
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
	require.Equal(t, int32(1), calls.Load())
	require.Equal(t, 1, trade.failures)
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
	require.Equal(t, 2, trade.failures)
	require.Equal(t, 2, notifier.alerts)
	require.Equal(t, int32(1), calls.Load())
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
	require.Equal(t, 1, trade.failures)
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

func TestReservationsKeepUnpurchasedOrdersAcrossSyncBoundary(t *testing.T) {
	service, db, now := newServiceHarness(t)
	lastSuccess := now.Add(-time.Minute)
	old := lastSuccess.Add(-time.Minute)
	for index, status := range []string{StatusPending, StatusProvisioning, StatusUnknown, StatusActive} {
		require.NoError(t, db.Create(&orderModel{
			OrderNo: fmt.Sprintf("ORDER-RESERVE-%d", index), ProjectID: 1, ProductID: 2,
			ServiceCode: "svc", Status: status, CodesJSON: []byte("[]"), UpstreamPriceSnapshot: "2",
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
		RemoteMailID: &mailID, Email: "buyer@gmail.com", Status: StatusActive, CodesJSON: []byte("[]"),
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
	service, _, _ := newServiceHarness(t)
	logs := &operationLogSpy{}
	service.SetOperationLogs(logs)
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodPut, "/v1/admin/upstreams/smsbower/config", strings.NewReader(`{
		"enabled":true,"apiKey":"","strategy":"local_first","syncIntervalMinutes":5,
		"balanceWarningThreshold":"1","pointsPerUnit":"1","minMarginRate":"0.10"
	}`))
	context.Request.Header.Set("Content-Type", "application/json")
	context.Set("request_id", "request-2")
	middleware.SetCurrentUser(context, 7, iamdomain.RoleAdmin, "admin@example.com", "session")

	(&handler{service: service}).putConfig(context)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Len(t, logs.items, 1)
	require.Equal(t, uint(7), logs.items[0].OperatorUserID)
}
