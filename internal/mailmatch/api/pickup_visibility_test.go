package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	mailmatchapp "github.com/donnel666/remail/internal/mailmatch/app"
	"github.com/donnel666/remail/internal/mailmatch/domain"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestPickupRequiresCodeDeliveryBeforeExposingMail(t *testing.T) {
	for _, test := range []struct {
		name        string
		serviceMode string
		looseMatch  bool
		delivered   bool
		completed   bool
		code        string
		lookupError bool
		visible     bool
	}{
		{name: "loose code without extraction", serviceMode: "code", looseMatch: true},
		{name: "loose code without delivery", serviceMode: "code", looseMatch: true, code: "123456"},
		{name: "strict code without delivery", serviceMode: "code", code: "123456"},
		{name: "loose delivery awaiting completion", serviceMode: "code", looseMatch: true, delivered: true, code: "123456"},
		{name: "strict delivery awaiting completion", serviceMode: "code", delivered: true, code: "123456"},
		{name: "completed code without delivery", serviceMode: "code", looseMatch: true, completed: true, code: "123456"},
		{name: "loose code delivered", serviceMode: "code", looseMatch: true, delivered: true, completed: true, code: "123456", visible: true},
		{name: "strict code delivered", serviceMode: "code", delivered: true, completed: true, code: "123456", visible: true},
		{name: "purchase without extraction", serviceMode: "purchase", looseMatch: true, visible: true},
		{name: "delivery lookup failed", serviceMode: "code", looseMatch: true, lookupError: true},
	} {
		for _, endpoint := range []string{"single", "batch", "batch fallback", "detail"} {
			t.Run(test.name+"/"+endpoint, func(t *testing.T) {
				now := time.Now().UTC()
				repo := &pickupVisibilityRepoStub{
					scope: mailmatchapp.OrderScope{
						OrderID: 1, OrderNo: "ORDER-VISIBILITY", EmailResourceID: 1,
						AllocationType: domain.ResourceTypeMicrosoft, AllocationID: 1,
						Recipient: "user@example.com", ServiceMode: test.serviceMode,
						OrderStatus: "active", LooseMatch: test.looseMatch,
					},
					message: domain.Message{
						ID: 7, Recipient: "user@example.com", ReceivedAt: now,
						Subject: "Private sign-in message", RawBody: "Private message body",
						BodyPreview: "Private message preview", VerificationCode: test.code,
						Status: domain.MessageStatusMatched,
					},
				}
				repo.message.MatchedOrderID = &repo.scope.OrderID
				if test.completed {
					repo.scope.OrderStatus = "completed"
				}
				if test.delivered {
					repo.delivery = &mailmatchapp.OrderDelivery{Message: &repo.message, ReceivedAt: now}
				}
				if test.lookupError {
					repo.deliveryErr = errors.New("delivery lookup failed")
				}
				var repository mailmatchapp.Repository = repo
				if endpoint == "batch fallback" {
					repository = struct{ mailmatchapp.Repository }{repo}
				}
				mod := &Module{UseCase: mailmatchapp.NewUseCase(repository, repo, nil, nil)}
				router := gin.New()
				RegisterRoutes(router.Group("/v1"), mod)
				var response *httptest.ResponseRecorder
				if endpoint == "batch" || endpoint == "batch fallback" {
					response = performPickupBatchRequest(router, "192.0.2.10:1234", PickupBatchRequest{
						Items: []PickupCredentialRequest{
							{Email: repo.scope.Recipient, Token: "token"},
							{Email: repo.scope.Recipient, Token: "token"},
						},
					})
				} else {
					path := "/v1/pickup"
					if endpoint == "detail" {
						path += "/messages/7"
					}
					response = httptest.NewRecorder()
					router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path+"?email=user@example.com&token=token", nil))
				}
				wantStatus := http.StatusOK
				if test.lookupError {
					wantStatus = http.StatusInternalServerError
					if endpoint == "batch" || endpoint == "batch fallback" {
						wantStatus = http.StatusMultiStatus
					}
				} else if endpoint == "detail" && !test.visible {
					wantStatus = http.StatusNotFound
				}
				require.Equal(t, wantStatus, response.Code, response.Body.String())
				if test.visible {
					require.Contains(t, response.Body.String(), repo.message.Subject)
					require.Contains(t, response.Body.String(), repo.message.BodyPreview)
					if endpoint == "detail" {
						require.Contains(t, response.Body.String(), repo.message.RawBody)
					}
				} else {
					require.NotContains(t, response.Body.String(), "Private")
					require.NotContains(t, response.Body.String(), "123456")
					if !test.lookupError && endpoint != "detail" {
						require.Contains(t, response.Body.String(), `"items":[]`)
						if test.delivered {
							require.Zero(t, repo.fetchRequests)
						} else {
							require.Equal(t, 1, repo.fetchRequests)
						}
					}
				}
			})
		}
	}
}

type pickupVisibilityRepoStub struct {
	mailmatchapp.Repository
	scope         mailmatchapp.OrderScope
	message       domain.Message
	delivery      *mailmatchapp.OrderDelivery
	deliveryErr   error
	fetchRequests int
}

func (r *pickupVisibilityRepoStub) LoadPickupScope(context.Context, string, string) (*mailmatchapp.OrderScope, error) {
	return &r.scope, nil
}

func (r *pickupVisibilityRepoStub) LoadOrderScopeForServiceToken(context.Context, string) (*mailmatchapp.OrderScope, error) {
	return &r.scope, nil
}

func (r *pickupVisibilityRepoStub) FindOrderDelivery(context.Context, uint) (*mailmatchapp.OrderDelivery, error) {
	return r.delivery, r.deliveryErr
}

func (r *pickupVisibilityRepoStub) ListOrderMessages(context.Context, mailmatchapp.OrderScope, int) ([]domain.Message, error) {
	return []domain.Message{r.message}, nil
}

func (r *pickupVisibilityRepoStub) FindOrderMessage(context.Context, uint, uint) (*domain.Message, error) {
	return &r.message, nil
}

func (r *pickupVisibilityRepoStub) ReadPickupBatch(_ context.Context, credentials []mailmatchapp.PickupCredential, _ time.Time, _, _ int) ([]mailmatchapp.PickupBatchRead, error) {
	reads := make([]mailmatchapp.PickupBatchRead, len(credentials))
	for i := range reads {
		reads[i] = mailmatchapp.PickupBatchRead{
			Scope: &r.scope, Delivery: r.delivery, Messages: []domain.Message{r.message}, Err: r.deliveryErr,
		}
	}
	return reads, nil
}

func (r *pickupVisibilityRepoStub) EnqueuePickupRequest(context.Context, mailmatchapp.PickupRequestFetchTask) (bool, error) {
	r.fetchRequests++
	return true, nil
}
