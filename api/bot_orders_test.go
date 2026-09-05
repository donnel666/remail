package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/donnel666/remail/api/middleware"
	mailmatchapi "github.com/donnel666/remail/internal/mailmatch/api"
	tradeapp "github.com/donnel666/remail/internal/trade/app"
	tradedomain "github.com/donnel666/remail/internal/trade/domain"
	"github.com/gin-gonic/gin"
)

func TestBotOrdersOwnerScopeAndPrivateProjection(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name, query, scene string
		bound, available   bool
		owner              uint
		status             int
		read               bool
	}{
		{"own", "", middleware.BotScenePrivate, true, true, 2, 200, true},
		{"foreign_result", "", middleware.BotScenePrivate, true, true, 9, 503, true},
		{"unbound", "", middleware.BotScenePrivate, false, false, 2, 200, false},
		{"disabled", "", middleware.BotScenePrivate, true, false, 2, 200, false},
		{"group", "", middleware.BotSceneGroup, true, true, 2, 403, false},
		{"identity_override", "?userId=9", middleware.BotScenePrivate, true, true, 2, 400, false},
		{"admin_scope", "?scope=all", middleware.BotScenePrivate, true, true, 2, 400, false},
		{"large_page", "?limit=101", middleware.BotScenePrivate, true, true, 2, 400, false},
		{"duplicate_limit", "?limit=1&limit=100", middleware.BotScenePrivate, true, true, 2, 400, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			list := func(_ context.Context, filter tradeapp.OrderListFilter, offset int, after uint, limit int) (*tradeapp.OrderListResult, error) {
				called = true
				if filter.UserID != 2 || filter.IsAdmin || filter.Scope != "mine" || offset != 0 || after != 0 || limit != 100 {
					t.Fatalf("unsafe query: %+v offset=%d after=%d limit=%d", filter, offset, after, limit)
				}
				return &tradeapp.OrderListResult{Total: 101, Items: []tradeapp.CheckoutResult{{
					Order:       tradedomain.Order{UserID: tc.owner, ProjectID: 2, OrderNo: "PRIVATE_ORDER", DeliveryEmail: "private@example.test", PayAmount: "99", ServiceMode: tradedomain.ServiceModePurchase, ProductType: tradedomain.ProductTypeICloud, Status: tradedomain.OrderStatusActive, CreatedAt: time.Now().UTC()},
					ProjectName: "OwnProject", ServiceToken: "PRIVATE_TOKEN", VerificationCode: "PRIVATE_CODE", GmailPassword: "PRIVATE_PASSWORD",
				}}}, nil
			}
			router := gin.New()
			router.GET("/orders", func(c *gin.Context) {
				c.Set("bot_identity", middleware.BotIdentity{
					BotIntegration: middleware.BotIntegration{SystemKeyID: 1, Platform: "qq", SubjectNamespace: "qq:main"},
					Subject:        "123456789", Scene: tc.scene,
				})
				getBotOrders(c, func(*gin.Context) (mailmatchapi.BotUserResolution, bool) {
					if tc.scene == middleware.BotSceneGroup {
						t.Fatal("group request must not resolve binding")
					}
					return mailmatchapi.BotUserResolution{UserID: 2, Bound: tc.bound, Available: tc.available}, true
				}, list)
			})
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/orders"+tc.query, nil))
			if response.Code != tc.status || called != tc.read {
				t.Fatalf("status=%d called=%v body=%s", response.Code, called, response.Body.String())
			}
			for _, secret := range []string{"PRIVATE_", "private@example.test", "orderNo", "deliveryEmail", "payAmount", "verificationCode", "serviceToken", "userId"} {
				if strings.Contains(response.Body.String(), secret) {
					t.Fatalf("private data leaked: %s", secret)
				}
			}
			if tc.name == "own" {
				var result struct {
					Items     []botOrderSummary `json:"items"`
					Truncated bool              `json:"truncated"`
				}
				if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || len(result.Items) != 1 || !result.Truncated {
					t.Fatalf("invalid summary: %s", response.Body.String())
				}
			}
		})
	}
}

func TestBotContextBindingProjection(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, user := range []mailmatchapi.BotUserResolution{
		{}, {UserID: 2, Bound: true}, {UserID: 2, Bound: true, Available: true}, {Bound: true, Available: true},
	} {
		response := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(response)
		getBotContext(c, func(*gin.Context) (mailmatchapi.BotUserResolution, bool) { return user, true })
		var payload map[string]bool
		if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil || len(payload) != 3 || response.Code != http.StatusOK {
			t.Fatalf("unexpected context: %s", response.Body.String())
		}
		if !payload["authorized"] || payload["bound"] != user.Bound || payload["accountAvailable"] != (user.Bound && user.Available && user.UserID > 0) {
			t.Fatalf("wrong access flags: %s", response.Body.String())
		}
	}
}
