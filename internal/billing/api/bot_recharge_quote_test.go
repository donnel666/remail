package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	billingapp "github.com/donnel666/remail/internal/billing/app"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// Any repository, gateway or queue access panics: quotations must only use config.
type quoteForbiddenSideEffects struct {
	billingapp.RechargeRepository
	billingapp.RechargeGateway
	billingapp.RechargeQueue
}

func botQuoteConfig() billingapp.RechargeConfig {
	return billingapp.RechargeConfig{
		Enabled: true, Version: "v1", GatewayURL: "https://pay.example.test",
		MerchantID: "PRIVATE_MERCHANT", MerchantKey: "PRIVATE_KEY",
		NotifyURL: "https://app.example.test/notify", ReturnURL: "https://app.example.test/wallet",
		PointsPerYuan: "1000", MinPoints: "100", FeeRate: "2", FeeCapPoints: "5",
		Tiers:         []billingapp.RechargeTier{{Points: "1000", BonusPoints: "100"}},
		EpusdtEnabled: true, EpusdtCurrency: "USDT", EpusdtPointsPerUSDT: "100",
		EpusdtMinimumPaymentAmount: "10.00", EpusdtGatewayURL: "https://usdt.example.test",
		EpusdtPID: "PRIVATE_PID", EpusdtAPISecret: "PRIVATE_SECRET", EpusdtToken: "USDT", EpusdtNetwork: "tron",
		EpusdtNotifyURL: "https://app.example.test/usdt-notify", EpusdtReturnURL: "https://app.example.test/wallet",
	}
}

func botQuoteRouter(config billingapp.RechargeConfig) *gin.Engine {
	gin.SetMode(gin.TestMode)
	forbidden := &quoteForbiddenSideEffects{}
	module := &BillingModule{RechargeUseCase: billingapp.NewRechargeUseCase(forbidden, botRechargeConfigProvider{config}, forbidden, forbidden)}
	router := gin.New()
	RegisterBotRoutes(router.Group("/v1/bot"), module)
	return router
}

func TestBotRechargeQuoteUsesRealCalculationWithoutAnySideEffects(t *testing.T) {
	router := botQuoteRouter(botQuoteConfig())
	for _, tc := range []struct{ method, amount, currency, fee string }{
		{"alipay", "1.01", "CNY", "5.00"},
		{"epusdt_usdt_tron", "10.00", "USDT", "0.00"},
		{"", "1.01", "CNY", "5.00"},
	} {
		body := `{"points":"1000","paymentMethod":"` + tc.method + `"}`
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/bot/recharges/quote", strings.NewReader(body)))
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
		var payload RechargeQuoteResponse
		require.NoError(t, json.Unmarshal(response.Body.Bytes(), &payload))
		require.Equal(t, "1000.00", payload.Points)
		require.Equal(t, "100.00", payload.BonusPoints)
		require.Equal(t, "1100.00", payload.CreditedPoints)
		require.Equal(t, tc.fee, payload.FeePoints)
		require.Equal(t, tc.amount, payload.PaymentAmount)
		require.Equal(t, tc.currency, payload.PaymentCurrency)
		var fields map[string]any
		require.NoError(t, json.Unmarshal(response.Body.Bytes(), &fields))
		require.Len(t, fields, 6)
		require.NotContains(t, response.Body.String(), "PRIVATE_")
	}
}

func TestBotRechargeQuoteRejectsIdentityOverridesMalformedAndOversizedBodies(t *testing.T) {
	router := botQuoteRouter(botQuoteConfig())
	for _, tc := range []struct {
		query, body string
		status      int
	}{
		{"?userId=9", `{"points":"1000"}`, 400},
		{"", `{"points":"1000","userId":9}`, 400},
		{"", `{"points":"1000","scope":"all"}`, 400},
		{"", `{"points":"1000","subject":"PRIVATE_SUBJECT"}`, 400},
		{"", `{"points":"1000","PRIVATE_FIELD":"PRIVATE_VALUE"}`, 400},
		{"", `{"points":"1000"} {"points":"2000"}`, 400},
		{"", `{"points":"` + strings.Repeat("9", maxRechargeRequestBytes) + `"}`, 400},
		{"", `{"points":1000}`, 400},
		{"", `{}`, 400},
		{"", `{"points":"-1"}`, 422},
		{"", `{"points":"1.5"}`, 422},
		{"", `{"points":"1000","paymentMethod":"usd"}`, 422},
		{"", `{"points":"100","paymentMethod":"epusdt_usdt_tron"}`, 422},
	} {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/bot/recharges/quote"+tc.query, strings.NewReader(tc.body)))
		require.Equal(t, tc.status, response.Code, response.Body.String())
		require.NotContains(t, response.Body.String(), "PRIVATE_")
	}
	missing := gin.New()
	RegisterBotRoutes(missing.Group("/v1/bot"), nil)
	response := httptest.NewRecorder()
	missing.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/bot/recharges/quote", strings.NewReader(`{"points":"1000"}`)))
	require.Equal(t, http.StatusServiceUnavailable, response.Code)
}

func TestBotRechargeConfigCurrenciesOnlyDescribeEnabledMethods(t *testing.T) {
	for _, tc := range []struct {
		alipay, usdt bool
		want         map[string]string
	}{
		{true, true, map[string]string{"alipay": "CNY", "epusdt_usdt_tron": "USDT"}},
		{true, false, map[string]string{"alipay": "CNY"}},
		{false, true, map[string]string{"epusdt_usdt_tron": "USDT"}},
		{false, false, nil},
	} {
		config := botQuoteConfig()
		config.Enabled, config.EpusdtEnabled = tc.alipay, tc.usdt
		response := httptest.NewRecorder()
		botQuoteRouter(config).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/bot/recharges/config", nil))
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
		var payload RechargeConfigResponse
		require.NoError(t, json.Unmarshal(response.Body.Bytes(), &payload))
		require.Equal(t, tc.want, payload.PaymentCurrencies)
		require.Len(t, payload.PaymentMethods, len(tc.want))
		for _, secret := range []string{"PRIVATE_", "gateway", "pointsPerYuan", "pointsPerUSDT", "apiSecret", "notify", "returnURL"} {
			require.NotContains(t, response.Body.String(), secret)
		}
	}
}
