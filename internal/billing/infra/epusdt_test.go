package infra

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
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
	providerOrderID := recharge.RechargeNo[2:]

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		require.Equal(t, http.MethodPost, request.Method)
		require.NoError(t, request.ParseForm())
		form := request.PostForm
		require.Equal(t, "1000", form.Get("pid"))
		require.Equal(t, providerOrderID, form.Get("order_id"))
		require.Equal(t, epusdtSign(formToMap(form, "signature"), secret), form.Get("signature"))

		if request.URL.Path == "/payments/gmpay/v1/order/create-transaction" {
			require.Equal(t, "10.00", form.Get("amount"))
			redirectURL, redirectErr := url.Parse(form.Get("redirect_url"))
			require.NoError(t, redirectErr)
			require.Equal(t, recharge.RechargeNo, redirectURL.Query().Get("out_trade_no"))
			// GMPay CreateTransactionResponse does not include pid, network, or
			// signature. Those fields are required only on the signed query view.
			writeEpusdtResponse(w, map[string]string{
				"order_id": providerOrderID, "trade_id": "GW-1", "status": "1",
				"amount": "10.00", "currency": "CNY", "actual_amount": "0.150000", "token": "USDT",
				"receive_address": "TAddress", "payment_url": "/pay/checkout-counter/GW-1",
			}, secret, false)
			return
		}
		require.Equal(t, "/payments/gmpay/v1/order/query", request.URL.Path)
		writeEpusdtResponse(w, map[string]string{
			"pid": "1000", "order_id": providerOrderID, "trade_id": "GW-1", "status": "2",
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

func TestEpusdtProviderOrderID(t *testing.T) {
	const localOrderID = "RC01234567890123456789012345678901"
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{name: "remail id", input: localOrderID, want: localOrderID[2:]},
		{name: "legacy short id", input: "RC-LEGACY", want: "RC-LEGACY"},
		{name: "legacy max length", input: strings.Repeat("L", 32), want: strings.Repeat("L", 32)},
		{name: "empty", wantErr: true},
		{name: "33 characters", input: strings.Repeat("A", 33), wantErr: true},
		{name: "invalid long id", input: "XX" + strings.Repeat("A", 32), wantErr: true},
		{name: "overlong", input: strings.Repeat("A", 35), wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := epusdtProviderOrderID(test.input)
			if test.wantErr {
				require.ErrorIs(t, err, domain.ErrInvalidRecharge)
				return
			}
			require.NoError(t, err)
			require.Equal(t, test.want, got)
		})
	}
}

func TestEpusdtSQLiteQueryPendingThenPaidWithoutHTTP(t *testing.T) {
	db, path := newEpusdtSQLiteTestDB(t)
	config, recharge, order := epusdtSQLiteTestValues(t)
	order.Status = 1
	// A hash may be observed before EPUSDT atomically finalizes the order. It
	// remains pending until status=2 instead of becoming a permanent mismatch.
	order.BlockTransactionID = "tx-observed-before-final-status"
	insertEpusdtSQLiteAPIKey(t, db, 1, config.EpusdtPID, 1, nil)
	insertEpusdtSQLiteOrder(t, db, 1, order, nil)
	t.Setenv(epusdtSQLitePathEnv, path)

	var httpQueries int
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		httpQueries++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	config.EpusdtGatewayURL = server.URL
	config.EpusdtAPISecret = "must-not-be-used"
	gateway := NewEpusdt()
	gateway.client = server.Client()

	result, err := gateway.Query(context.Background(), config, recharge)
	require.NoError(t, err)
	require.False(t, result.Paid)
	require.False(t, result.Terminal)
	require.Zero(t, httpQueries)

	_, err = db.Exec(`UPDATE orders SET status = 2, block_transaction_id = ? WHERE order_id = ?`, "tx-final", order.OrderID)
	require.NoError(t, err)
	result, err = gateway.Query(context.Background(), config, recharge)
	require.NoError(t, err)
	require.True(t, result.Paid)
	require.Equal(t, order.TradeID, result.GatewayTrade)
	require.Zero(t, httpQueries)
}

func TestEpusdtSQLiteQueryRejectsMismatchedPaidOrder(t *testing.T) {
	db, path := newEpusdtSQLiteTestDB(t)
	config, recharge, order := epusdtSQLiteTestValues(t)
	order.Amount = "3.00"
	insertEpusdtSQLiteAPIKey(t, db, 1, config.EpusdtPID, 1, nil)
	insertEpusdtSQLiteOrder(t, db, 1, order, nil)
	t.Setenv(epusdtSQLitePathEnv, path)

	_, err := NewEpusdt().Query(context.Background(), config, recharge)
	require.ErrorIs(t, err, domain.ErrRechargeQueryMismatch)
}

func TestEpusdtSQLiteQueryBindsMerchantHistory(t *testing.T) {
	tests := []struct {
		name         string
		pid          string
		keyStatus    int
		keyDeleted   any
		orderDeleted any
		wantPaid     bool
	}{
		{name: "foreign pid", pid: "2000", keyStatus: 1},
		{name: "disabled key", pid: "1000", keyStatus: 2, wantPaid: true},
		{name: "deleted key", pid: "1000", keyStatus: 1, keyDeleted: "2026-01-01", wantPaid: true},
		{name: "deleted order", pid: "1000", keyStatus: 1, orderDeleted: "2026-01-01"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db, path := newEpusdtSQLiteTestDB(t)
			config, recharge, order := epusdtSQLiteTestValues(t)
			insertEpusdtSQLiteAPIKey(t, db, 1, test.pid, test.keyStatus, test.keyDeleted)
			insertEpusdtSQLiteOrder(t, db, 1, order, test.orderDeleted)
			t.Setenv(epusdtSQLitePathEnv, path)

			result, err := NewEpusdt().Query(context.Background(), config, recharge)
			require.NoError(t, err)
			require.Equal(t, test.wantPaid, result.Paid)
		})
	}
}

func TestEpusdtPaymentURLRequiresReadableSQLite(t *testing.T) {
	config, recharge, _ := epusdtSQLiteTestValues(t)
	config.EpusdtGatewayURL = "https://pay.example.invalid"
	config.EpusdtAPISecret = "secret"
	config.EpusdtNotifyURL = "https://app.example.invalid/notify"
	config.EpusdtReturnURL = "https://app.example.invalid/wallet"
	t.Setenv(epusdtSQLitePathEnv, filepath.Join(t.TempDir(), "missing.db"))

	_, err := NewEpusdt().PaymentURL(context.Background(), config, recharge, "")
	require.ErrorContains(t, err, "query epusdt sqlite")
}

func TestEpusdtSQLiteDSNIsReadOnlyAndQueryOnly(t *testing.T) {
	db, path := newEpusdtSQLiteTestDB(t)
	config, _, order := epusdtSQLiteTestValues(t)
	insertEpusdtSQLiteAPIKey(t, db, 1, config.EpusdtPID, 1, nil)
	insertEpusdtSQLiteOrder(t, db, 1, order, nil)

	dsn, err := epusdtSQLiteDSN(path)
	require.NoError(t, err)
	parsed, err := url.Parse(dsn)
	require.NoError(t, err)
	require.Equal(t, "ro", parsed.Query().Get("mode"))
	require.ElementsMatch(t, []string{"query_only(1)", "busy_timeout(5000)"}, parsed.Query()["_pragma"])
	require.NotContains(t, dsn, "immutable")

	readOnly, err := sql.Open("sqlite", dsn)
	require.NoError(t, err)
	defer readOnly.Close()
	var queryOnly int
	require.NoError(t, readOnly.QueryRow(`PRAGMA query_only`).Scan(&queryOnly))
	require.Equal(t, 1, queryOnly)
	_, err = readOnly.Exec(`UPDATE orders SET status = 3`)
	require.Error(t, err)
}

func TestValidateEpusdtSQLiteOrderBindsSettlementFields(t *testing.T) {
	config, recharge, base := epusdtSQLiteTestValues(t)
	tests := []struct {
		name   string
		mutate func(*epusdtOrder)
	}{
		{name: "order id", mutate: func(order *epusdtOrder) { order.OrderID = "foreign" }},
		{name: "amount", mutate: func(order *epusdtOrder) { order.Amount = "1.99" }},
		{name: "currency", mutate: func(order *epusdtOrder) { order.Currency = "CNY" }},
		{name: "token", mutate: func(order *epusdtOrder) { order.Token = "TRX" }},
		{name: "network", mutate: func(order *epusdtOrder) { order.Network = "ethereum" }},
		{name: "status", mutate: func(order *epusdtOrder) { order.Status = 5 }},
		{name: "trade id", mutate: func(order *epusdtOrder) { order.TradeID = "" }},
		{name: "receive address", mutate: func(order *epusdtOrder) { order.ReceiveAddress = "" }},
		{name: "block transaction id", mutate: func(order *epusdtOrder) { order.BlockTransactionID = "" }},
		{name: "provider", mutate: func(order *epusdtOrder) { order.PayProvider = "okpay" }},
		{name: "parent trade", mutate: func(order *epusdtOrder) { order.ParentTradeID = "parent" }},
		{name: "paid by sub order", mutate: func(order *epusdtOrder) { order.PayBySubID = 9 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			order := base
			test.mutate(&order)
			require.ErrorIs(t, validateEpusdtSQLiteOrder(order, config, recharge), domain.ErrRechargeQueryMismatch)
		})
	}
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
	order.Status = 1
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

func newEpusdtSQLiteTestDB(t *testing.T) (*sql.DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "epusdt.db")
	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	var journalMode string
	require.NoError(t, db.QueryRow(`PRAGMA journal_mode = WAL`).Scan(&journalMode))
	require.Equal(t, "wal", strings.ToLower(journalMode))
	_, err = db.Exec(`PRAGMA wal_autocheckpoint = 0`)
	require.NoError(t, err)
	_, err = db.Exec(`
		CREATE TABLE api_keys (
			id INTEGER PRIMARY KEY,
			pid TEXT NOT NULL,
			status INTEGER NOT NULL,
			deleted_at TEXT
		);
		CREATE TABLE orders (
			id INTEGER PRIMARY KEY,
			api_key_id INTEGER NOT NULL,
			order_id TEXT NOT NULL,
			trade_id TEXT,
			status INTEGER NOT NULL,
			amount NUMERIC,
			currency TEXT,
			actual_amount NUMERIC,
			token TEXT,
			network TEXT,
			receive_address TEXT,
			block_transaction_id TEXT,
			pay_provider TEXT,
			parent_trade_id TEXT,
			pay_by_sub_id INTEGER,
			deleted_at TEXT
		);`)
	require.NoError(t, err)
	require.FileExists(t, path+"-wal")
	return db, path
}

func epusdtSQLiteTestValues(t *testing.T) (billingapp.RechargeConfig, domain.Recharge, epusdtOrder) {
	t.Helper()
	config := billingapp.RechargeConfig{
		EpusdtPID:      "1000",
		EpusdtToken:    "USDT",
		EpusdtNetwork:  "tron",
		EpusdtCurrency: "USDT",
	}
	recharge := domain.Recharge{
		RechargeNo:    "RC01234567890123456789012345678901",
		PaymentAmount: "2.00",
	}
	orderID, err := epusdtProviderOrderID(recharge.RechargeNo)
	require.NoError(t, err)
	order := epusdtOrder{
		PID:                config.EpusdtPID,
		OrderID:            orderID,
		TradeID:            "GW-SQLITE-1",
		Status:             2,
		Amount:             recharge.PaymentAmount,
		Currency:           config.EpusdtCurrency,
		ActualAmount:       recharge.PaymentAmount,
		Token:              config.EpusdtToken,
		Network:            config.EpusdtNetwork,
		ReceiveAddress:     "TAddress",
		BlockTransactionID: "tx-paid",
		PayProvider:        "on_chain",
	}
	return config, recharge, order
}

func insertEpusdtSQLiteAPIKey(t *testing.T, db *sql.DB, id int, pid string, status int, deletedAt any) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO api_keys (id, pid, status, deleted_at) VALUES (?, ?, ?, ?)`, id, pid, status, deletedAt)
	require.NoError(t, err)
}

func insertEpusdtSQLiteOrder(t *testing.T, db *sql.DB, apiKeyID int, order epusdtOrder, deletedAt any) {
	t.Helper()
	_, err := db.Exec(`
		INSERT INTO orders (
			api_key_id, order_id, trade_id, status, amount, currency, actual_amount,
			token, network, receive_address, block_transaction_id, pay_provider,
			parent_trade_id, pay_by_sub_id, deleted_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		apiKeyID,
		order.OrderID,
		order.TradeID,
		order.Status,
		order.Amount,
		order.Currency,
		order.ActualAmount,
		order.Token,
		order.Network,
		order.ReceiveAddress,
		order.BlockTransactionID,
		order.PayProvider,
		order.ParentTradeID,
		order.PayBySubID,
		deletedAt,
	)
	require.NoError(t, err)
}
