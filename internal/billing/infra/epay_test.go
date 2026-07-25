package infra

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	billingapp "github.com/donnel666/remail/internal/billing/app"
	"github.com/donnel666/remail/internal/billing/domain"
	"github.com/stretchr/testify/require"
)

func TestEPayV1PaymentURLAndActiveQuery(t *testing.T) {
	config := billingapp.RechargeConfig{
		Version: "v1", GatewayURL: "https://pay.example.com/base", MerchantID: "1000", MerchantKey: "secret",
		NotifyURL: "https://app.example.com/v1/payments/webhooks/epay/v1", ReturnURL: "https://app.example.com/wallet",
	}
	recharge := domain.Recharge{RechargeNo: "RC1", PaymentAmount: "100.00"}
	gateway := NewEPay()
	paymentURL, err := gateway.PaymentURL(context.Background(), config, recharge, "")
	require.NoError(t, err)
	parsed, err := url.Parse(paymentURL)
	require.NoError(t, err)
	require.Equal(t, "/base/submit.php", parsed.Path)
	require.Equal(t, "MD5", parsed.Query().Get("sign_type"))
	require.Equal(t, epaySign(map[string]string{
		"pid": "1000", "type": "alipay", "out_trade_no": "RC1",
		"notify_url": config.NotifyURL, "return_url": config.ReturnURL,
		"name": "Account recharge", "money": "100.00",
	}, "secret"), parsed.Query().Get("sign"))

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		require.Equal(t, http.MethodPost, request.Method)
		require.Equal(t, "/api.php", request.URL.Path)
		require.Empty(t, request.URL.RawQuery)
		require.NoError(t, request.ParseForm())
		require.Equal(t, "order", request.PostForm.Get("act"))
		require.Equal(t, "secret", request.PostForm.Get("key"))
		body := `{"code":1,"status":"1","pid":1000,"trade_no":"GW1","out_trade_no":"RC1","type":"alipay","money":"100.00","endtime":"2026-07-25 18:00:00"}`
		require.NoError(t, json.NewEncoder(w).Encode(body))
	}))
	defer server.Close()
	config.GatewayURL = server.URL
	gateway.client = server.Client()
	result, err := gateway.Query(context.Background(), config, recharge)
	require.NoError(t, err)
	require.True(t, result.Paid)
	require.Equal(t, "GW1", result.GatewayTrade)
	require.NotNil(t, result.PaidAt)

	recharge.PaymentAmount = "99.00"
	_, err = gateway.Query(context.Background(), config, recharge)
	require.ErrorIs(t, err, domain.ErrRechargeQueryMismatch)
}

func TestEPayQueryErrorDoesNotLeakMerchantKey(t *testing.T) {
	gateway := &EPay{client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("network down")
	})}}
	_, err := gateway.Query(context.Background(), billingapp.RechargeConfig{
		Version: "v1", GatewayURL: "https://pay.example.com", MerchantID: "1000", MerchantKey: "do-not-log-me",
	}, domain.Recharge{RechargeNo: "RC1"})
	require.Error(t, err)
	require.False(t, strings.Contains(err.Error(), "do-not-log-me"))
}

func TestEPayV2PaymentURLAndSignedActiveQuery(t *testing.T) {
	merchantPrivate, merchantPublic := epayRSAKeyPair(t)
	platformPrivate, platformPublic := epayRSAKeyPair(t)
	config := billingapp.RechargeConfig{
		Version: "v2", MerchantID: "1000",
		PrivateKey: merchantPrivate, PlatformPublicKey: platformPublic,
		NotifyURL: "https://app.example.com/v1/payments/webhooks/epay/v2", ReturnURL: "https://app.example.com/wallet",
	}
	recharge := domain.Recharge{RechargeNo: "RC2", PaymentAmount: "100.00"}
	gateway := NewEPay()
	responseMode := "create"
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		require.Equal(t, http.MethodPost, request.Method)
		require.NoError(t, request.ParseForm())
		require.Equal(t, "RSA", request.PostForm.Get("sign_type"))
		require.NoError(t, epayVerifyRSA(queryValues(request.PostForm), request.PostForm.Get("sign"), merchantPublic))
		if request.URL.Path == "/api/pay/create" {
			require.Equal(t, "web", request.PostForm.Get("method"))
			require.Equal(t, "203.0.113.10", request.PostForm.Get("clientip"))
			require.NotEmpty(t, request.PostForm.Get("timestamp"))
			values := map[string]string{
				"code": "0", "trade_no": "GW2", "pay_type": "qrcode",
				"pay_info": "/pay/submit/GW2/", "timestamp": "1753437600",
			}
			signature, signErr := epaySignRSA(values, platformPrivate)
			require.NoError(t, signErr)
			values["sign_type"] = "RSA"
			values["sign"] = signature
			if responseMode == "create_tampered" {
				values["pay_info"] = "https://evil.example.com/"
			}
			require.NoError(t, json.NewEncoder(w).Encode(values))
			return
		}
		require.Equal(t, "/api/pay/query", request.URL.Path)
		if responseMode == "missing" {
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"code": -1, "msg": "order not found"}))
			return
		}
		values := map[string]string{
			"code": "0", "status": "1", "pid": "1000", "trade_no": "GW2", "out_trade_no": "RC2",
			"type": "alipay", "money": "100.00", "endtime": "2026-07-25 18:00:00", "timestamp": "1753437600",
		}
		signature, signErr := epaySignRSA(values, platformPrivate)
		require.NoError(t, signErr)
		values["sign_type"] = "RSA"
		values["sign"] = signature
		if responseMode == "tampered" {
			values["money"] = "99.00"
		}
		require.NoError(t, json.NewEncoder(w).Encode(values))
	}))
	defer server.Close()
	config.GatewayURL = server.URL
	gateway.client = server.Client()

	paymentURL, err := gateway.PaymentURL(context.Background(), config, recharge, "203.0.113.10")
	require.NoError(t, err)
	require.Equal(t, server.URL+"/pay/submit/GW2/", paymentURL)

	responseMode = "create_tampered"
	_, err = gateway.PaymentURL(context.Background(), config, recharge, "203.0.113.10")
	require.ErrorIs(t, err, domain.ErrRechargeQueryMismatch)

	responseMode = "paid"
	result, err := gateway.Query(context.Background(), config, recharge)
	require.NoError(t, err)
	require.True(t, result.Paid)
	require.Equal(t, "GW2", result.GatewayTrade)

	responseMode = "tampered"
	_, err = gateway.Query(context.Background(), config, recharge)
	require.ErrorIs(t, err, domain.ErrRechargeQueryMismatch)

	responseMode = "missing"
	result, err = gateway.Query(context.Background(), config, recharge)
	require.NoError(t, err)
	require.False(t, result.Paid)
}

func TestEPayV2RejectsWeakRSAKeys(t *testing.T) {
	weak, err := rsa.GenerateKey(rand.Reader, 1024)
	require.NoError(t, err)
	weakPrivate := base64.StdEncoding.EncodeToString(x509.MarshalPKCS1PrivateKey(weak))
	weakPublicDER, err := x509.MarshalPKIXPublicKey(&weak.PublicKey)
	require.NoError(t, err)
	weakPublic := base64.StdEncoding.EncodeToString(weakPublicDER)

	_, err = epaySignRSA(map[string]string{"pid": "1000"}, weakPrivate)
	require.Error(t, err)
	strongPrivate, _ := epayRSAKeyPair(t)
	_, err = NewEPay().PaymentURL(context.Background(), billingapp.RechargeConfig{
		Version: "v2", GatewayURL: "https://pay.example.com", MerchantID: "1000",
		PrivateKey: strongPrivate, PlatformPublicKey: weakPublic,
	}, domain.Recharge{RechargeNo: "RC-WEAK", PaymentAmount: "10.00"}, "127.0.0.1")
	require.ErrorIs(t, err, domain.ErrRechargeConfigUnavailable)
}

func epayRSAKeyPair(t *testing.T) (string, string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	publicDER, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	require.NoError(t, err)
	return base64.StdEncoding.EncodeToString(x509.MarshalPKCS1PrivateKey(key)), base64.StdEncoding.EncodeToString(publicDER)
}

func queryValues(values url.Values) map[string]string {
	result := make(map[string]string, len(values))
	for key := range values {
		result[key] = values.Get(key)
	}
	return result
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return fn(request) }
