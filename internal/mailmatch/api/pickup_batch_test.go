package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/donnel666/remail/api/middleware"
	mailmatchapp "github.com/donnel666/remail/internal/mailmatch/app"
	mailmatchdomain "github.com/donnel666/remail/internal/mailmatch/domain"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"golang.org/x/time/rate"
)

func TestPickupBatchAcceptsTwoHundredItems(t *testing.T) {
	repo, router := newPickupBatchTestRouter(t, rate.Inf, 1000)
	require.Equal(t, 1024, pickupMaxActive)
	require.Equal(t, 1024, pickupMaxTotal)
	items := make([]PickupCredentialRequest, maxPickupBatchSize)
	for i := range items {
		items[i] = PickupCredentialRequest{
			Email: fmt.Sprintf("user-%d@example.com", i),
			Token: fmt.Sprintf("token-%d", i),
		}
	}

	response := performPickupBatchRequest(router, "192.0.2.10:1234", PickupBatchRequest{Items: items})

	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var body PickupBatchResponse
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
	require.Len(t, body, maxPickupBatchSize)
	require.Equal(t, int64(1), repo.loadCalls.Load())
	for i := range body {
		require.Equal(t, i, body[i].Index)
		require.Equal(t, "succeeded", body[i].Status)
		require.NotNil(t, body[i].Data)
		require.Nil(t, body[i].Error)
	}
}

func TestPickupBatchReturnsOneHundredItemBodyWhenRequestBudgetExpires(t *testing.T) {
	repo, router := newPickupBatchTestRouter(t, rate.Inf, 1000)
	repo.blockRead = true
	items := make([]PickupCredentialRequest, 100)
	for i := range items {
		items[i] = PickupCredentialRequest{
			Email: fmt.Sprintf("user-%d@example.com", i),
			Token: fmt.Sprintf("token-%d", i),
		}
	}
	payload, err := json.Marshal(PickupBatchRequest{Items: items})
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	req := httptest.NewRequest(http.MethodPost, "/v1/pickup/batch", bytes.NewReader(payload)).WithContext(ctx)
	req.RemoteAddr = "192.0.2.20:1234"
	req.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	started := time.Now()
	router.ServeHTTP(response, req)

	require.Less(t, time.Since(started), time.Second)
	require.Equal(t, http.StatusMultiStatus, response.Code, response.Body.String())
	require.Equal(t, "1", response.Header().Get("Retry-After"))
	var body PickupBatchResponse
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
	require.Len(t, body, 100)
	require.Equal(t, int64(1), repo.loadCalls.Load())
	for i := range body {
		require.Equal(t, i, body[i].Index)
		require.Equal(t, "failed", body[i].Status)
		require.NotNil(t, body[i].Error)
		require.Equal(t, "service_unavailable", body[i].Error.Code)
	}
}

func TestPickupBatchRejectsMoreThanTwoHundredItems(t *testing.T) {
	repo, router := newPickupBatchTestRouter(t, rate.Inf, 1000)
	items := make([]PickupCredentialRequest, maxPickupBatchSize+1)
	for i := range items {
		items[i] = PickupCredentialRequest{Email: "user@example.com", Token: fmt.Sprintf("token-%d", i)}
	}

	response := performPickupBatchRequest(router, "192.0.2.11:1234", PickupBatchRequest{Items: items})

	require.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())
	require.Zero(t, repo.loadCalls.Load())
}

func TestPickupBatchRateLimitsByClientIPBeforeDatabaseWork(t *testing.T) {
	repo, router := newPickupBatchTestRouter(t, rate.Every(10*time.Second), 2)
	body := PickupBatchRequest{Items: []PickupCredentialRequest{
		{Email: "a@example.com", Token: "token-a"},
		{Email: "b@example.com", Token: "token-b"},
	}}

	first := performPickupBatchRequest(router, "192.0.2.12:1234", body)
	second := performPickupBatchRequest(router, "192.0.2.12:4321", body)
	third := performPickupBatchRequest(router, "192.0.2.12:9999", body)

	require.Equal(t, http.StatusOK, first.Code, first.Body.String())
	require.Equal(t, http.StatusOK, second.Code, second.Body.String())
	require.Equal(t, http.StatusTooManyRequests, third.Code, third.Body.String())
	require.Equal(t, "10", third.Header().Get("Retry-After"))
	require.Equal(t, int64(2), repo.loadCalls.Load())
}

func TestPickupBatchRejectsBeforeDatabaseWhenGlobalQueueIsFull(t *testing.T) {
	repo, router := newPickupBatchTestRouter(t, rate.Inf, 1000)
	globalPickupOutstanding = make(chan struct{}, 1)
	globalPickupExecution = make(chan struct{}, 1)
	globalPickupOutstanding <- struct{}{}

	response := performPickupBatchRequest(router, "192.0.2.21:1234", PickupBatchRequest{Items: []PickupCredentialRequest{
		{Email: "a@example.com", Token: "token-a"},
		{Email: "b@example.com", Token: "token-b"},
	}})

	require.Equal(t, http.StatusServiceUnavailable, response.Code, response.Body.String())
	require.Equal(t, "1", response.Header().Get("Retry-After"))
	require.Zero(t, repo.loadCalls.Load())
}

func TestPickupBatchQueueCancellationReleasesOutstandingPermit(t *testing.T) {
	oldOutstanding := globalPickupOutstanding
	oldExecution := globalPickupExecution
	globalPickupOutstanding = make(chan struct{}, 1)
	globalPickupExecution = make(chan struct{}, 1)
	globalPickupExecution <- struct{}{}
	t.Cleanup(func() {
		globalPickupOutstanding = oldOutstanding
		globalPickupExecution = oldExecution
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, admitted := acquirePickup(ctx)

	require.False(t, admitted)
	require.Empty(t, globalPickupOutstanding)
}

func TestPickupBatchRejectsOversizedBody(t *testing.T) {
	_, router := newPickupBatchTestRouter(t, rate.Inf, 1000)
	body := `{"items":[{"email":"a@example.com","token":"` + strings.Repeat("x", maxPickupBatchBytes) + `"},{"email":"b@example.com","token":"token-b"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/pickup/batch", strings.NewReader(body))
	req.RemoteAddr = "192.0.2.13:1234"
	req.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	router.ServeHTTP(response, req)

	require.Equal(t, http.StatusRequestEntityTooLarge, response.Code, response.Body.String())
}

func TestPickupBatchRejectsOversizedTokenPerItemWithoutDatabaseOrLimiterAmplification(t *testing.T) {
	repo, router := newPickupBatchTestRouter(t, rate.Inf, 1000)
	response := performPickupBatchRequest(router, "192.0.2.14:1234", PickupBatchRequest{Items: []PickupCredentialRequest{
		{Email: "too-long@example.com", Token: strings.Repeat("x", 256)},
		{Email: "valid@example.com", Token: "valid-token"},
	}})

	require.Equal(t, http.StatusMultiStatus, response.Code, response.Body.String())
	var body PickupBatchResponse
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
	require.Len(t, body, 2)
	require.Equal(t, "failed", body[0].Status)
	require.Equal(t, "invalid_request", body[0].Error.Code)
	require.Equal(t, "succeeded", body[1].Status)
	require.Equal(t, int64(1), repo.loadCalls.Load())
	require.Len(t, globalPickupListLimiter.items, 1)
}

func TestNormalizePickupClientIPCanonicalizesEquivalentAddresses(t *testing.T) {
	require.Equal(t, "2001:db8::1", normalizePickupClientIP("2001:0db8:0:0:0:0:0:1"))
	require.Equal(t, "192.0.2.1", normalizePickupClientIP("::ffff:192.0.2.1"))
	require.Equal(t, "not-an-ip", normalizePickupClientIP("  not-an-ip  "))
}

func TestPickupLimiterRejectsNewKeysAtCapacityWithoutGrowing(t *testing.T) {
	limiter := newPickupLimiter(rate.Inf, 1)
	limiter.maxKeys = 2
	require.True(t, limiter.allow("token-a"))
	require.True(t, limiter.allow("token-b"))
	require.False(t, limiter.allow("token-c"))
	require.Len(t, limiter.items, 2)
}

type pickupBatchRepoStub struct {
	mailmatchapp.Repository
	loadCalls atomic.Int64
	blockRead bool
}

func (r *pickupBatchRepoStub) ReadPickupBatch(ctx context.Context, credentials []mailmatchapp.PickupCredential, _ time.Time, _ int) ([]mailmatchapp.PickupBatchRead, error) {
	r.loadCalls.Add(1)
	if r.blockRead {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	cooldown := time.Now().Add(time.Minute)
	reads := make([]mailmatchapp.PickupBatchRead, len(credentials))
	for i, credential := range credentials {
		reads[i] = mailmatchapp.PickupBatchRead{
			Scope: &mailmatchapp.OrderScope{
				OrderID: 1, OrderNo: "ORDER-" + credential.Token, EmailResourceID: 1,
				Recipient: credential.Email, ServiceMode: "purchase", OrderStatus: "active",
			},
			Fetch: &mailmatchdomain.FetchState{CooldownUntil: &cooldown},
		}
	}
	return reads, nil
}

func (r *pickupBatchRepoStub) LoadPickupScope(_ context.Context, token string, email string) (*mailmatchapp.OrderScope, error) {
	r.loadCalls.Add(1)
	return &mailmatchapp.OrderScope{
		OrderID:         1,
		OrderNo:         "ORDER-" + token,
		EmailResourceID: 1,
		Recipient:       email,
		ServiceMode:     "purchase",
		OrderStatus:     "active",
	}, nil
}

func (*pickupBatchRepoStub) FindOrderDelivery(context.Context, uint) (*mailmatchapp.OrderDelivery, error) {
	return nil, nil
}

func (*pickupBatchRepoStub) FindFetchStateForUpdate(context.Context, uint) (*mailmatchdomain.FetchState, error) {
	cooldown := time.Now().Add(time.Minute)
	return &mailmatchdomain.FetchState{CooldownUntil: &cooldown}, nil
}

func (*pickupBatchRepoStub) ListOrderMessages(context.Context, mailmatchapp.OrderScope, int) ([]mailmatchdomain.Message, error) {
	return nil, nil
}

func newPickupBatchTestRouter(t *testing.T, ipLimit rate.Limit, ipBurst int) (*pickupBatchRepoStub, *gin.Engine) {
	t.Helper()
	oldListLimiter := globalPickupListLimiter
	oldIPLimiter := globalPickupBatchIPLimiter
	oldOutstanding := globalPickupOutstanding
	oldExecution := globalPickupExecution
	globalPickupListLimiter = newPickupLimiter(rate.Inf, 1000)
	globalPickupBatchIPLimiter = newPickupLimiter(ipLimit, ipBurst)
	globalPickupOutstanding = make(chan struct{}, pickupMaxTotal)
	globalPickupExecution = make(chan struct{}, pickupMaxActive)
	t.Cleanup(func() {
		globalPickupListLimiter = oldListLimiter
		globalPickupBatchIPLimiter = oldIPLimiter
		globalPickupOutstanding = oldOutstanding
		globalPickupExecution = oldExecution
	})

	repo := &pickupBatchRepoStub{}
	module := &Module{UseCase: mailmatchapp.NewUseCase(repo, nil, nil, nil)}
	router := gin.New()
	require.NoError(t, router.SetTrustedProxies(nil))
	router.Use(middleware.RequestID())
	RegisterRoutes(router.Group("/v1"), module)
	return repo, router
}

func performPickupBatchRequest(router *gin.Engine, remoteAddr string, body PickupBatchRequest) *httptest.ResponseRecorder {
	payload, err := json.Marshal(body)
	if err != nil {
		panic(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/pickup/batch", bytes.NewReader(payload))
	req.RemoteAddr = remoteAddr
	req.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, req)
	return response
}
