package infra

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	billingapp "github.com/donnel666/remail/internal/billing/app"
	"github.com/donnel666/remail/internal/billing/domain"
	"github.com/stretchr/testify/require"
)

func TestEpusdtCreateAndSignedQuery(t *testing.T) {
	const secret = "epusdt-secret"
	config := billingapp.RechargeConfig{
		EpusdtGatewayURL: "https://pay.example.invalid",
		EpusdtPID:        "1000",
		EpusdtAPISecret:  secret,
		EpusdtToken:      "USDT",
		EpusdtNetwork:    "tron",
		EpusdtNotifyURL:  "https://app.example.invalid/payments/epusdt/notify",
		EpusdtReturnURL:  "https://app.example.invalid/wallet",
	}
	recharge := domain.Recharge{RechargeNo: "RC01234567890123456789012345678901", PaymentAmount: "10.00"}

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		require.Equal(t, http.MethodPost, request.Method)
		require.NoError(t, request.ParseForm())
		form := request.PostForm
		require.Equal(t, "1000", form.Get("pid"))
		require.Equal(t, recharge.RechargeNo, form.Get("order_id"))
		require.Equal(t, epusdtSign(formToMap(form, "signature"), secret), form.Get("signature"))

		if request.URL.Path == "/payments/gmpay/v1/order/create-transaction" {
			require.Equal(t, "10.00", form.Get("amount"))
			redirectURL, redirectErr := url.Parse(form.Get("redirect_url"))
			require.NoError(t, redirectErr)
			require.Equal(t, recharge.RechargeNo, redirectURL.Query().Get("out_trade_no"))
			// GMPay CreateTransactionResponse does not include pid, network, or
			// signature. Those fields are required only on the signed query view.
			writeEpusdtResponse(w, map[string]string{
				"order_id": recharge.RechargeNo, "trade_id": "GW-1", "status": "1",
				"amount": "10.00", "currency": "CNY", "actual_amount": "0.150000", "token": "USDT",
				"receive_address": "TAddress", "payment_url": "/pay/checkout-counter/GW-1",
			}, secret, false)
			return
		}
		require.Equal(t, "/payments/gmpay/v1/order/query", request.URL.Path)
		writeEpusdtResponse(w, map[string]string{
			"pid": "1000", "order_id": recharge.RechargeNo, "trade_id": "GW-1", "status": "2",
			"amount": "10.0000", "currency": "CNY", "actual_amount": "0.150000", "token": "USDT",
			"network": "tron", "receive_address": "TAddress", "block_transaction_id": "tx-1",
			"payment_url": "https://pay.example.invalid/pay/checkout-counter/GW-1",
		}, secret, true)
	}))
	defer server.Close()

	config.EpusdtGatewayURL = server.URL
	gateway := NewEpusdt()
	gateway.client = server.Client()
	paymentURL, err := gateway.PaymentURL(context.Background(), config, recharge, "")
	require.NoError(t, err)
	require.Equal(t, server.URL+"/pay/checkout-counter/GW-1", paymentURL)

	result, err := gateway.Query(context.Background(), config, recharge)
	require.NoError(t, err)
	require.True(t, result.Paid)
	require.Equal(t, "GW-1", result.GatewayTrade)
}

func TestEpusdtDirectUSDTModeBindsCurrencyToRateSnapshot(t *testing.T) {
	config := billingapp.RechargeConfig{
		EpusdtGatewayURL:    "https://pay.example.invalid",
		EpusdtPID:           "1000",
		EpusdtAPISecret:     "epusdt-secret",
		EpusdtToken:         "USDT",
		EpusdtNetwork:       "tron",
		EpusdtCurrency:      "USDT",
		EpusdtPointsPerUSDT: "6800",
		EpusdtNotifyURL:     "https://app.example.invalid/notify",
		EpusdtReturnURL:     "https://app.example.invalid/return",
	}
	recharge := domain.Recharge{RechargeNo: "RC-DIRECT-USDT", PaymentAmount: "1.48"}

	values, err := epusdtCreateValues(config, recharge, "1.48")
	require.NoError(t, err)
	require.Equal(t, "USDT", values["currency"])
	require.Equal(t, epusdtSign(values, config.EpusdtAPISecret), values["signature"])

	order := epusdtOrder{PID: "1000", OrderID: recharge.RechargeNo, Amount: "1.4800", Currency: "USDT", ActualAmount: "1.480000", Token: "USDT", Network: "tron", Status: 2, TradeID: "GW-DIRECT"}
	require.NoError(t, validateEpusdtOrder(order, config, recharge))
	order.ActualAmount = "1.470000"
	require.ErrorIs(t, validateEpusdtOrder(order, config, recharge), domain.ErrRechargeQueryMismatch)
	order.Currency = "CNY"
	require.ErrorIs(t, validateEpusdtOrder(order, config, recharge), domain.ErrRechargeQueryMismatch)
}

func TestEpusdtRedirectURLPreservesQueryAndFragment(t *testing.T) {
	redirect, err := epusdtRedirectURL(
		"https://app.example.invalid/payment/return?source=wallet&out_trade_no=stale#complete",
		"RC01234567890123456789012345678901",
	)
	require.NoError(t, err)
	parsed, err := url.Parse(redirect)
	require.NoError(t, err)
	require.Equal(t, "wallet", parsed.Query().Get("source"))
	require.Equal(t, "RC01234567890123456789012345678901", parsed.Query().Get("out_trade_no"))
	require.Equal(t, "complete", parsed.Fragment)
}

func TestEpusdtPaymentAmountRequiresStrictProviderMinimum(t *testing.T) {
	for _, raw := range []string{"0.00", "0.01", "0.0100"} {
		_, err := epusdtPaymentAmount(raw)
		require.ErrorIs(t, err, domain.ErrInvalidAmount, raw)
	}
	amount, err := epusdtPaymentAmount("0.02")
	require.NoError(t, err)
	require.Equal(t, "0.02", amount)
}

func TestEpusdtQueryRejectsTamperedResponseAndCreateURL(t *testing.T) {
	const secret = "epusdt-secret"
	config := billingapp.RechargeConfig{
		EpusdtPID:       "1000",
		EpusdtAPISecret: secret,
		EpusdtToken:     "USDT",
		EpusdtNetwork:   "tron",
		EpusdtNotifyURL: "https://app.example.invalid/notify",
		EpusdtReturnURL: "https://app.example.invalid/return",
	}
	recharge := domain.Recharge{RechargeNo: "RC-TAMPER", PaymentAmount: "10.00"}
	mode := "query"
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		require.NoError(t, request.ParseForm())
		if request.URL.Path == "/payments/gmpay/v1/order/create-transaction" {
			writeEpusdtResponse(w, map[string]string{
				"pid": "1000", "order_id": recharge.RechargeNo, "trade_id": "GW-2", "status": "1",
				"amount": "10.00", "currency": "CNY", "actual_amount": "0.15", "token": "USDT", "network": "tron",
				"payment_url": "https://evil.example.invalid/pay/GW-2",
			}, secret, false)
			return
		}
		values := map[string]string{
			"pid": "1000", "order_id": recharge.RechargeNo, "trade_id": "GW-2", "status": "2",
			"amount": "99.00", "currency": "CNY", "actual_amount": "0.15", "token": "USDT", "network": "tron",
			"payment_url": "https://pay.example.invalid/pay/GW-2",
		}
		if mode == "bad-signature" {
			writeEpusdtResponse(w, values, "wrong", true)
			return
		}
		writeEpusdtResponse(w, values, secret, true)
	}))
	defer server.Close()
	config.EpusdtGatewayURL = server.URL
	gateway := NewEpusdt()
	gateway.client = server.Client()

	_, err := gateway.PaymentURL(context.Background(), config, recharge, "")
	require.ErrorIs(t, err, billingapp.ErrRechargeGatewayRejected)

	_, err = gateway.Query(context.Background(), config, recharge)
	require.ErrorIs(t, err, domain.ErrRechargeQueryMismatch)

	mode = "bad-signature"
	_, err = gateway.Query(context.Background(), config, recharge)
	require.ErrorIs(t, err, domain.ErrRechargeQueryMismatch)
}

func TestEpusdtQueryClassifiesAuthenticationUnavailable(t *testing.T) {
	config := billingapp.RechargeConfig{
		EpusdtPID:       "1000",
		EpusdtAPISecret: "old-secret",
		EpusdtToken:     "USDT",
		EpusdtNetwork:   "tron",
	}
	recharge := domain.Recharge{RechargeNo: "RC-AUTH-RETRY", PaymentAmount: "10.00"}

	tests := []struct {
		name       string
		httpStatus int
		body       string
	}{
		{name: "http 401", httpStatus: http.StatusUnauthorized, body: `{"status_code":401}`},
		{name: "http 401 with unrelated body code", httpStatus: http.StatusUnauthorized, body: `{"status_code":10008}`},
		{name: "http 403", httpStatus: http.StatusForbidden, body: `{"status_code":403}`},
		{name: "wrapped 401", httpStatus: http.StatusOK, body: `{"status_code":401,"message":"signature error"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				require.Equal(t, "/payments/gmpay/v1/order/query", request.URL.Path)
				w.WriteHeader(test.httpStatus)
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()

			config.EpusdtGatewayURL = server.URL
			gateway := NewEpusdt()
			gateway.client = server.Client()
			_, err := gateway.Query(context.Background(), config, recharge)
			require.ErrorIs(t, err, domain.ErrRechargeGatewayAuthUnavailable)
			require.NotErrorIs(t, err, domain.ErrRechargeQueryMismatch)
		})
	}
}

func TestEpusdtQueryNeverFallsBackToUnsignedCheckStatus(t *testing.T) {
	config := billingapp.RechargeConfig{
		EpusdtPID:       "1000",
		EpusdtAPISecret: "epusdt-secret",
		EpusdtToken:     "USDT",
		EpusdtNetwork:   "tron",
	}
	recharge := domain.Recharge{RechargeNo: "RC-MISSING-QUERY", PaymentAmount: "10.00"}
	var legacyChecks int
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/payments/gmpay/v1/order/query":
			// A stock EPUSDT deployment without the signed query route returns
			// HTTP 404. Treat it as pending; never substitute an unsigned route.
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"Not Found"}`))
		case "/pay/check-status/GW-UNSIGNED":
			legacyChecks++
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status_code":200,"data":{"status":2,"trade_id":"GW-UNSIGNED"}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	config.EpusdtGatewayURL = server.URL
	gateway := NewEpusdt()
	gateway.client = server.Client()

	result, err := gateway.Query(context.Background(), config, recharge)
	require.NoError(t, err)
	require.False(t, result.Paid)
	require.Zero(t, legacyChecks)
}

func TestEpusdtQueryRetriesWithCurrentSecretAfterRotation(t *testing.T) {
	const (
		oldSecret = "old-secret"
		newSecret = "new-secret"
	)
	recharge := domain.Recharge{RechargeNo: "RC-AUTH-FALLBACK", PaymentAmount: "10.00"}
	var attempts int
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		require.NoError(t, request.ParseForm())
		attempts++
		values := formToMap(request.PostForm, "signature")
		switch request.PostForm.Get("signature") {
		case epusdtSign(values, oldSecret):
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"status_code":401,"message":"signature error"}`))
		case epusdtSign(values, newSecret):
			writeEpusdtResponse(w, map[string]string{
				"pid": rechargePID, "order_id": recharge.RechargeNo, "trade_id": "GW-ROTATED", "status": "2",
				"amount": "10.00", "currency": "CNY", "actual_amount": "0.15", "token": "USDT", "network": "tron",
				"block_transaction_id": "tx-rotated",
			}, newSecret, true)
		default:
			t.Fatalf("unexpected query signature: %q", request.PostForm.Get("signature"))
		}
	}))
	defer server.Close()

	snapshot := billingapp.RechargeConfig{
		EpusdtGatewayURL: server.URL,
		EpusdtPID:        rechargePID,
		EpusdtAPISecret:  oldSecret,
		EpusdtToken:      "USDT",
		EpusdtNetwork:    "tron",
	}
	current := snapshot
	current.EpusdtAPISecret = newSecret
	current.EpusdtCurrency = "USDT"
	gateway := NewEpusdtWithConfigProvider(epusdtConfigProviderStub{config: current})
	gateway.client = server.Client()

	result, err := gateway.Query(context.Background(), snapshot, recharge)
	require.NoError(t, err)
	require.True(t, result.Paid)
	require.Equal(t, "GW-ROTATED", result.GatewayTrade)
	require.Equal(t, 2, attempts)
}

func TestEpusdtQueryRetriesWhenSecretRotationOnlyChangesWhitespace(t *testing.T) {
	const (
		oldSecret = "rotated-secret "
		newSecret = "rotated-secret"
	)
	recharge := domain.Recharge{RechargeNo: "RC-AUTH-WHITESPACE", PaymentAmount: "10.00"}
	var attempts int
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		require.NoError(t, request.ParseForm())
		attempts++
		values := formToMap(request.PostForm, "signature")
		switch request.PostForm.Get("signature") {
		case epusdtSign(values, oldSecret):
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"status_code":401,"message":"signature error"}`))
		case epusdtSign(values, newSecret):
			writeEpusdtResponse(w, map[string]string{
				"pid": rechargePID, "order_id": recharge.RechargeNo, "trade_id": "GW-WHITESPACE", "status": "2",
				"amount": "10.00", "currency": "CNY", "actual_amount": "0.15", "token": "USDT", "network": "tron",
			}, newSecret, true)
		default:
			t.Fatalf("unexpected query signature: %q", request.PostForm.Get("signature"))
		}
	}))
	defer server.Close()

	snapshot := billingapp.RechargeConfig{
		EpusdtGatewayURL: server.URL, EpusdtPID: rechargePID, EpusdtAPISecret: oldSecret,
		EpusdtToken: "USDT", EpusdtNetwork: "tron",
	}
	current := snapshot
	current.EpusdtAPISecret = newSecret
	gateway := NewEpusdtWithConfigProvider(epusdtConfigProviderStub{config: current})
	gateway.client = server.Client()

	result, err := gateway.Query(context.Background(), snapshot, recharge)
	require.NoError(t, err)
	require.True(t, result.Paid)
	require.Equal(t, "GW-WHITESPACE", result.GatewayTrade)
	require.Equal(t, 2, attempts)
}

func TestEpusdtQueryDoesNotFallbackAcrossMerchantScope(t *testing.T) {
	var attempts int
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts++
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"status_code":401}`))
	}))
	defer server.Close()

	snapshot := billingapp.RechargeConfig{
		EpusdtGatewayURL: server.URL,
		EpusdtPID:        "1000",
		EpusdtAPISecret:  "old-secret",
		EpusdtToken:      "USDT",
		EpusdtNetwork:    "tron",
	}
	current := snapshot
	current.EpusdtPID = "2000"
	current.EpusdtAPISecret = "new-secret"
	gateway := NewEpusdtWithConfigProvider(epusdtConfigProviderStub{config: current})
	gateway.client = server.Client()

	_, err := gateway.Query(context.Background(), snapshot, domain.Recharge{RechargeNo: "RC-SCOPE", PaymentAmount: "10.00"})
	require.ErrorIs(t, err, domain.ErrRechargeGatewayAuthUnavailable)
	require.Equal(t, 1, attempts)
}

func TestEpusdtQueryDoesNotRetryAcrossCurrencyMode(t *testing.T) {
	var attempts int
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		attempts++
		if attempts == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"status_code":401}`))
			return
		}
		require.NoError(t, request.ParseForm())
		writeEpusdtResponse(w, map[string]string{
			"pid": "1000", "order_id": "RC-CURRENCY", "trade_id": "GW-CURRENCY", "status": "2",
			"amount": "1.00", "currency": "CNY", "actual_amount": "0.15", "token": "USDT", "network": "tron",
		}, "new-secret", true)
	}))
	defer server.Close()

	snapshot := billingapp.RechargeConfig{
		EpusdtGatewayURL: server.URL, EpusdtPID: "1000", EpusdtAPISecret: "old-secret",
		EpusdtToken: "USDT", EpusdtNetwork: "tron", EpusdtCurrency: "USDT",
	}
	current := snapshot
	current.EpusdtAPISecret = "new-secret"
	current.EpusdtCurrency = "CNY"
	gateway := NewEpusdtWithConfigProvider(epusdtConfigProviderStub{config: current})
	gateway.client = server.Client()

	_, err := gateway.Query(context.Background(), snapshot, domain.Recharge{RechargeNo: "RC-CURRENCY", PaymentAmount: "1.00"})
	require.ErrorIs(t, err, domain.ErrRechargeQueryMismatch)
	require.Equal(t, 2, attempts)
}

const rechargePID = "1000"

type epusdtConfigProviderStub struct {
	config billingapp.RechargeConfig
	err    error
}

func (stub epusdtConfigProviderStub) Current() (billingapp.RechargeConfig, error) {
	return stub.config, stub.err
}

func TestEpusdtPaymentURLKeepsGatewayOriginWithAllowlist(t *testing.T) {
	config := billingapp.RechargeConfig{
		EpusdtGatewayURL:   "https://api.example.invalid",
		EpusdtAllowedHosts: "checkout.example.invalid",
	}
	url, err := epusdtPaymentURL(config, "/pay/checkout-counter/GW-1")
	require.NoError(t, err)
	require.Equal(t, "https://api.example.invalid/pay/checkout-counter/GW-1", url)
	_, err = epusdtPaymentURL(config, "https://evil.example.invalid/pay/GW-1")
	require.ErrorIs(t, err, billingapp.ErrRechargeGatewayRejected)
}

func TestEpusdtCreateFailureRecoversPaymentURLByQuery(t *testing.T) {
	const secret = "epusdt-secret"
	config := billingapp.RechargeConfig{
		EpusdtPID:       "1000",
		EpusdtAPISecret: secret,
		EpusdtToken:     "USDT",
		EpusdtNetwork:   "tron",
		EpusdtNotifyURL: "https://app.example.invalid/notify",
		EpusdtReturnURL: "https://app.example.invalid/return",
	}
	recharge := domain.Recharge{RechargeNo: "RC-RECOVER", PaymentAmount: "10.00"}
	var creates, queries int
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		require.NoError(t, request.ParseForm())
		if request.URL.Path == "/payments/gmpay/v1/order/create-transaction" {
			creates++
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(`{"status_code":10010,"message":"order already exists"}`))
			return
		}
		queries++
		writeEpusdtResponse(w, map[string]string{
			"pid": "1000", "order_id": recharge.RechargeNo, "trade_id": "GW-RECOVER", "status": "1",
			"amount": "10.00", "currency": "CNY", "actual_amount": "0.15", "token": "USDT", "network": "tron",
			"payment_url": "/pay/checkout-counter/GW-RECOVER",
		}, secret, true)
	}))
	defer server.Close()
	config.EpusdtGatewayURL = server.URL
	gateway := NewEpusdt()
	gateway.client = server.Client()

	paymentURL, err := gateway.PaymentURL(context.Background(), config, recharge, "")
	require.NoError(t, err)
	require.Equal(t, server.URL+"/pay/checkout-counter/GW-RECOVER", paymentURL)
	require.Equal(t, 1, creates)
	require.Equal(t, 1, queries)
}

func formToMap(values url.Values, skip string) map[string]string {
	result := make(map[string]string, len(values))
	for key := range values {
		if key != skip {
			result[key] = values.Get(key)
		}
	}
	return result
}

func writeEpusdtResponse(w http.ResponseWriter, values map[string]string, secret string, signed bool) {
	if signed {
		values["signature"] = epusdtSign(values, secret)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"status_code": 200, "data": values})
}
