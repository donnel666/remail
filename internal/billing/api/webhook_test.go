package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	billingapp "github.com/donnel666/remail/internal/billing/app"
	"github.com/donnel666/remail/internal/billing/domain"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"golang.org/x/time/rate"
)

const (
	validWebhookRechargeOne = "RC00000000000000000000000000000001"
	validWebhookRechargeTwo = "RC00000000000000000000000000000002"
)

func TestEPayWebhookOnlyAcknowledges(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	RegisterBillingRoutes(router.Group("/v1"), &BillingModule{}, nil, nil, nil)

	for _, version := range []string{"v1", "v2"} {
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, httptest.NewRequest(method, "/v1/payments/webhooks/epay/"+version+"?trade_status=TRADE_SUCCESS", nil))
			require.Equal(t, http.StatusOK, recorder.Code)
			require.Equal(t, "success", recorder.Body.String())
		}
	}
}

func TestEPayWebhookStartsUntrustedReconciliationSignal(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &webhookRechargeRepoStub{marked: true}
	queue := &webhookRechargeQueueStub{}
	module := &BillingModule{RechargeUseCase: billingapp.NewRechargeUseCase(repo, nil, nil, queue)}
	router := gin.New()
	RegisterBillingRoutes(router.Group("/v1"), module, nil, nil, nil)

	get := httptest.NewRecorder()
	router.ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/v1/payments/webhooks/epay/v1?out_trade_no="+validWebhookRechargeOne+"&trade_status=TRADE_SUCCESS", nil))
	require.Equal(t, http.StatusOK, get.Code)
	require.Equal(t, "success", get.Body.String())

	form := url.Values{"out_trade_no": {validWebhookRechargeTwo}, "trade_status": {"TRADE_SUCCESS"}}
	postRequest := httptest.NewRequest(http.MethodPost, "/v1/payments/webhooks/epay/v2", strings.NewReader(form.Encode()))
	postRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	post := httptest.NewRecorder()
	router.ServeHTTP(post, postRequest)
	require.Equal(t, http.StatusOK, post.Code)
	require.Equal(t, "success", post.Body.String())
	require.Equal(t, []string{validWebhookRechargeOne, validWebhookRechargeTwo}, repo.rechargeNos)
	require.Equal(t, 2, queue.calls)
}

func TestEPayWebhookIsOpaqueAndBodyLimited(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("unknown order", func(t *testing.T) {
		repo := &webhookRechargeRepoStub{}
		router := gin.New()
		RegisterBillingRoutes(router.Group("/v1"), &BillingModule{RechargeUseCase: billingapp.NewRechargeUseCase(repo, nil, nil, &webhookRechargeQueueStub{})}, nil, nil, nil)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/payments/webhooks/epay/v1?out_trade_no=UNKNOWN", nil))
		require.Equal(t, http.StatusOK, response.Code)
		require.Equal(t, "success", response.Body.String())
	})

	t.Run("database failure", func(t *testing.T) {
		repo := &webhookRechargeRepoStub{err: errors.New("database details must stay private")}
		router := gin.New()
		RegisterBillingRoutes(router.Group("/v1"), &BillingModule{RechargeUseCase: billingapp.NewRechargeUseCase(repo, nil, nil, &webhookRechargeQueueStub{})}, nil, nil, nil)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/payments/webhooks/epay/v1?out_trade_no="+validWebhookRechargeOne, nil))
		require.Equal(t, http.StatusInternalServerError, response.Code)
		require.Equal(t, "fail", response.Body.String())
	})

	t.Run("oversized form", func(t *testing.T) {
		repo := &webhookRechargeRepoStub{marked: true}
		router := gin.New()
		RegisterBillingRoutes(router.Group("/v1"), &BillingModule{RechargeUseCase: billingapp.NewRechargeUseCase(repo, nil, nil, &webhookRechargeQueueStub{})}, nil, nil, nil)
		request := httptest.NewRequest(http.MethodPost, "/v1/payments/webhooks/epay/v1", strings.NewReader("out_trade_no="+strings.Repeat("x", maxEPayWebhookBytes)))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		require.Equal(t, http.StatusOK, response.Code)
		require.Equal(t, "success", response.Body.String())
		require.Empty(t, repo.rechargeNos)
	})
}

func TestEPayWebhookRateLimitDoesNotReachDatabase(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &webhookRechargeRepoStub{marked: true}
	module := &BillingModule{RechargeUseCase: billingapp.NewRechargeUseCase(repo, nil, nil, &webhookRechargeQueueStub{})}
	handler := NewBillingHandler(module, nil)
	handler.webhook = rate.NewLimiter(0, 1)
	router := gin.New()
	router.GET("/webhook", handler.EPayWebhook)

	for _, rechargeNo := range []string{validWebhookRechargeOne, validWebhookRechargeTwo} {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/webhook?out_trade_no="+rechargeNo, nil))
		require.Equal(t, http.StatusOK, response.Code)
		require.Equal(t, "success", response.Body.String())
	}
	require.Equal(t, []string{validWebhookRechargeOne}, repo.rechargeNos)
}

type webhookRechargeRepoStub struct {
	marked      bool
	err         error
	rechargeNos []string
}

func (stub *webhookRechargeRepoStub) CreateRecharge(context.Context, billingapp.CreateRechargeCommand) (*domain.Recharge, error) {
	return nil, domain.ErrRechargeNotFound
}

func (stub *webhookRechargeRepoStub) GetRechargeByNo(context.Context, string) (*domain.Recharge, error) {
	return nil, domain.ErrRechargeNotFound
}

func (stub *webhookRechargeRepoStub) MarkRechargeCallback(_ context.Context, rechargeNo string, _ time.Time) (bool, error) {
	stub.rechargeNos = append(stub.rechargeNos, rechargeNo)
	return stub.marked, stub.err
}

func (*webhookRechargeRepoStub) ListDueRecharges(context.Context, time.Time, int) ([]domain.Recharge, error) {
	return nil, nil
}

func (*webhookRechargeRepoStub) ExpirePendingRecharges(context.Context, time.Time, time.Time) (int64, error) {
	return 0, nil
}

func (*webhookRechargeRepoStub) ClaimRechargeQuery(context.Context, string, time.Time, time.Time) (*domain.Recharge, billingapp.RechargeConfig, int, bool, error) {
	return nil, billingapp.RechargeConfig{}, 0, false, nil
}

func (*webhookRechargeRepoStub) RecordRechargeQuery(context.Context, string, int, time.Time) error {
	return nil
}

func (*webhookRechargeRepoStub) FailRecharge(context.Context, string, int, string, time.Time) error {
	return nil
}

func (*webhookRechargeRepoStub) CreditRecharge(context.Context, billingapp.CreditRechargeCommand) (*domain.Recharge, error) {
	return nil, nil
}

type webhookRechargeQueueStub struct{ calls int }

func (stub *webhookRechargeQueueStub) Enqueue(context.Context, billingapp.RechargeTask) error {
	stub.calls++
	return nil
}

func TestRechargeSecurityErrorsAreClientVisible(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, test := range []struct {
		err     error
		status  int
		message string
	}{
		{err: domain.ErrRechargePending, status: http.StatusConflict, message: "Too many recharge orders"},
		{err: domain.ErrInvalidIdempotencyKey, status: http.StatusBadRequest, message: "Invalid Idempotency-Key"},
	} {
		recorder := httptest.NewRecorder()
		context, _ := gin.CreateTestContext(recorder)
		writeBillingError(context, test.err)
		require.Equal(t, test.status, recorder.Code)
		require.Contains(t, recorder.Body.String(), test.message)
	}
}
