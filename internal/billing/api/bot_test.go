package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	billingapp "github.com/donnel666/remail/internal/billing/app"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type botRechargeConfigProvider struct{ config billingapp.RechargeConfig }

func (p botRechargeConfigProvider) Current() (billingapp.RechargeConfig, error) { return p.config, nil }

func TestBotRechargeConfigUsesPublicCurrentConfigWithoutSecrets(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const setting = "redemption_code_purchase_url"
	previous, existed := runtimeconfig.Snapshot()[setting]
	runtimeconfig.Set(setting, "https://shop.example.com/cards")
	t.Cleanup(func() {
		if existed {
			runtimeconfig.Set(setting, previous)
		} else {
			runtimeconfig.Delete(setting)
		}
	})
	config := billingapp.RechargeConfig{
		Enabled: true, Version: "v1", GatewayURL: "https://pay.example.com",
		MerchantID: "1000", MerchantKey: "merchant-secret",
		NotifyURL: "https://app.example.com/notify", ReturnURL: "https://app.example.com/wallet",
		PointsPerYuan: "1000", MinPoints: "100", FeeRate: "2", FeeCapPoints: "5",
		Tiers: []billingapp.RechargeTier{{Points: "100", BonusPoints: "10"}},
	}
	module := &BillingModule{RechargeUseCase: billingapp.NewRechargeUseCase(nil, botRechargeConfigProvider{config}, nil, nil)}
	router := gin.New()
	RegisterBotRoutes(router.Group("/v1/bot"), module)

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/bot/recharges/config", nil))

	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var payload RechargeConfigResponse
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &payload))
	require.True(t, payload.Enabled)
	require.Equal(t, []string{"alipay"}, payload.PaymentMethods)
	require.Equal(t, "https://shop.example.com/cards", payload.RedemptionCodePurchaseURL)
	require.Len(t, payload.Tiers, 1)
	for _, secret := range []string{"merchant-secret", "pay.example.com", "app.example.com", "merchant", "gateway", "notify", "return", "pointsPerYuan"} {
		require.NotContains(t, strings.ToLower(response.Body.String()), strings.ToLower(secret))
	}
}
