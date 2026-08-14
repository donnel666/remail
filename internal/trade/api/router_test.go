package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	iamdomain "github.com/donnel666/remail/internal/iam/domain"
	"github.com/gin-gonic/gin"
)

type noSessionFetcher struct{}

func (noSessionFetcher) FetchSession(context.Context, string) (uint, iamdomain.Role, string, bool, error) {
	return 0, "", "", false, nil
}

type noPermissionChecker struct{}

func (noPermissionChecker) Check(context.Context, uint, iamdomain.Role, string, string) (bool, error) {
	return false, nil
}

func TestConsoleOrderRoutesIgnoreAPIKey(t *testing.T) {
	gin.SetMode(gin.TestMode)

	r := gin.New()
	RegisterRoutes(r.Group("/v1"), nil, noSessionFetcher{}, noPermissionChecker{})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/orders", nil)
	req.Header.Set("Authorization", "Bearer rk-test")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected status 401, got %d", w.Code)
	}
}

func TestSingleOrderBodyRejectsBatchQuantityField(t *testing.T) {
	gin.SetMode(gin.TestMode)
	response := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(response)
	ctx.Request = httptest.NewRequest(
		http.MethodPost,
		"/v1/orders",
		strings.NewReader(`{"projectId":10,"productId":20,"quantity":2}`),
	)
	var request CreateOrderRequest

	err := bindOrderJSON(ctx, &request)

	if err == nil || !strings.Contains(err.Error(), `unknown field "quantity"`) {
		t.Fatalf("expected unknown quantity field error, got %v", err)
	}
}

func TestOrderBodyAcceptsLegacyProductID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	response := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(response)
	ctx.Request = httptest.NewRequest(
		http.MethodPost,
		"/v1/orders",
		strings.NewReader(`{"projectId":10,"productId":20}`),
	)
	var request CreateOrderRequest

	if err := bindOrderJSON(ctx, &request); err != nil {
		t.Fatalf("expected legacy product ID body to decode, got %v", err)
	}
	if request.ProjectID != 10 || request.ProductID != 20 || request.EmailSuffix != "" {
		t.Fatalf("unexpected decoded legacy request: %#v", request)
	}
}

func TestBatchOrderIdempotencyKeysNormalizeHeaderWhitespace(t *testing.T) {
	for index := 0; index < 3; index++ {
		plain := batchOrderIdempotencyKey("batch-key", index)
		spaced := batchOrderIdempotencyKey("  batch-key  ", index)
		if plain != spaced {
			t.Fatalf("expected normalized key at index %d, got %q and %q", index, plain, spaced)
		}
	}
}
