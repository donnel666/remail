package kitesim

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	proxyapp "github.com/donnel666/remail/internal/proxy/app"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

type rewriteTransport struct {
	target *url.URL
	base   http.RoundTripper
}

func (t rewriteTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	clone := request.Clone(request.Context())
	originalURL := *request.URL
	redirectedURL := *request.URL
	redirectedURL.Scheme, redirectedURL.Host = t.target.Scheme, t.target.Host
	clone.URL, clone.Host = &redirectedURL, request.URL.Host
	response, err := t.base.RoundTrip(clone)
	if response != nil {
		response.Request = request.Clone(request.Context())
		response.Request.URL = &originalURL
	}
	return response, err
}

func TestRechargeThreeDSFailsAndClearsCVC(t *testing.T) {
	originalWait := rechargeQueryWait
	waits := make([]time.Duration, 0, rechargeQueryCount)
	rechargeQueryWait = func(_ context.Context, delay time.Duration) error {
		waits = append(waits, delay)
		return nil
	}
	t.Cleanup(func() { rechargeQueryWait = originalWait })

	var mutex sync.Mutex
	writes := map[string]int{}
	calls := map[string]int{}
	userAgents := map[string]struct{}{}
	recordWrite := func(request *http.Request) {
		mutex.Lock()
		userAgents[request.UserAgent()] = struct{}{}
		calls[request.URL.Path]++
		if request.Method == http.MethodPost {
			writes[request.URL.Path]++
		}
		mutex.Unlock()
	}
	jwtPayload, _ := json.Marshal(map[string]string{"OrgUnitId": "ORG-1", "ReferenceId": "REF-1"})
	jwt := "header." + base64.RawURLEncoding.EncodeToString(jwtPayload) + ".signature"
	methodPayload, _ := base64JSON(map[string]string{
		"threeDSMethodNotificationURL": "https://oats.allinpay.com/notify",
		"threeDSServerTransID":         "TXN-1",
	})
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		recordWrite(request)
		switch request.URL.Path {
		case "/user/info":
			_ = json.NewEncoder(response).Encode(map[string]any{"code": 200, "data": map[string]any{"balance": "15"}})
		case "/rechargeCampaignRecord/createBalanceOrder":
			_ = json.NewEncoder(response).Encode(map[string]any{"code": 200, "data": "TOPUP-1"})
		case "/payment/create":
			_ = json.NewEncoder(response).Encode(map[string]any{"code": 200, "data": map[string]any{
				"codeUrl": "https://international.storepay.cn/cashier", "outTransNo": "OUT-1", "extInfo": "PAY-1",
			}})
		case "/cashier":
			http.SetCookie(response, &http.Cookie{Name: "payment_session", Value: "one-session", Path: "/"})
			_, _ = response.Write([]byte(`<input name="paymentMethod" value="wallet" data-sign="wrong"><input name="paymentMethod" value="bankCard" data-sign="signed%2Fvalue">`))
		case "/api/pay/cashier/billing":
			if cookie, err := request.Cookie("payment_session"); err != nil || cookie.Value != "one-session" {
				t.Error("OnlyPay cookie jar was not reused for billing")
			}
			_ = request.ParseForm()
			if request.Form.Get("sign") != "signed/value" {
				t.Errorf("billing sign = %q", request.Form.Get("sign"))
			}
			_ = json.NewEncoder(response).Encode(map[string]any{"valid": true})
		case "/api/pay/cashier/payment":
			if cookie, err := request.Cookie("payment_session"); err != nil || cookie.Value != "one-session" {
				t.Error("OnlyPay cookie jar was not reused for card payment")
			}
			_ = request.ParseForm()
			if request.Form.Get("cardNo") != "4111 1111 1111 1111" || request.Form.Get("secureCode") != "123" {
				t.Error("card form was not formatted as expected")
			}
			_, _ = response.Write([]byte(`<script>var jumpUrl = "https:\/\/oats.allinpay.com/payh5/cnp_pay/directCashier?accessOrderId=A1&amp;mchtId=M1&amp;accessCode=C1";</script>`))
		case "/payh5/cnp_pay/directCashier":
			_, _ = response.Write([]byte(`<html><body>cashier</body></html>`))
		case "/pay-web-h5/cnp_pay/cardBinInJwt":
			_ = request.ParseForm()
			if request.Form.Get("realCardNo") != "4111111111111111" {
				t.Error("Allinpay did not receive the configured card")
			}
			_ = json.NewEncoder(response).Encode(map[string]any{
				"resultCode": "0000", "sessionId": "SESSION-1", "jwt": jwt,
				"action": "https://centinelapi.cardinalcommerce.com/V1/Cruise/Collect",
			})
		case "/fp/tags.js", "/fp/tags", "/fp/clear.png":
			_, _ = response.Write([]byte("ok"))
		case "/V1/Cruise/Collect":
			_ = request.ParseForm()
			if request.Form.Get("Bin") != "4111111111111111" || request.Form.Get("JWT") != jwt {
				t.Error("Cardinal Collect did not receive card BIN and JWT")
			}
			_, _ = response.Write([]byte(`<input id="orgUnitId" value="ORG-1"><input id="referenceId" value="REF-1">`))
		case "/DeviceFingerprintWeb/V2/Browser/Render":
			_ = request.ParseForm()
			if request.Form.Get("bin") != "4111111111111111" || request.Form.Get("nonce") == "" {
				t.Error("Cardinal Render did not receive fingerprint inputs")
			}
			_, _ = response.Write([]byte(`<script>profiler.start({"features":{"merchantMethodUrlCollection":{"methodUrls":[{"MethodURL":"https://secure5.arcot.com/method","Payload":"` + methodPayload + `","ThreeDSServerTransactionId":"TXN-1"}]}}});</script>`))
		case "/DeviceFingerprintWeb/V2/Browser/SaveBrowserData":
			var body map[string]any
			_ = json.NewDecoder(request.Body).Decode(&body)
			if body["ReferenceId"] != "REF-1" || body["UserAgent"] == "" {
				t.Error("Cardinal browser data was incomplete")
			}
		case "/method":
			_ = request.ParseForm()
			if request.Form.Get("threeDSMethodData") != methodPayload {
				t.Error("Arcot method payload was not forwarded")
			}
			_, _ = response.Write([]byte(`<script>form.action = "https://secure5.arcot.com/tds-method-post-device-data"; var notifURLValue = "https://oats.allinpay.com/notify"; var txnId = "TXN-1";</script>`))
		case "/content-server/api/tds2-fingerprintjs":
			var body map[string]any
			_ = json.NewDecoder(request.Body).Decode(&body)
			if body["txnid"] != "TXN-1" || body["b64DeviceInfo"] == "" {
				t.Error("Arcot fingerprint payload was incomplete")
			}
		case "/tds-method-post-device-data":
			_ = request.ParseForm()
			if request.Form.Get("threeDSServerTransId") != "TXN-1" || request.Form.Get("deviceData") == "" {
				t.Error("Arcot device data was incomplete")
			}
		case "/notify":
			_ = request.ParseForm()
			if request.Form.Get("threeDSMethodData") == "" {
				t.Error("Arcot notification was empty")
			}
		case "/pay-web-h5/cnp_pay/checkCardBinInJwt/SESSION-1":
			_ = request.ParseForm()
			if request.Form.Get("setUpReferenceID") != "REF-1" {
				t.Errorf("setUpReferenceID = %q", request.Form.Get("setUpReferenceID"))
			}
			_, _ = response.Write([]byte(`<form method="POST"><input type="hidden" name="creq" value="challenge"></form>`))
		case "/payment/query":
			_ = json.NewEncoder(response).Encode(map[string]any{"code": 200, "data": map[string]any{"resultCode": "PENDING", "resultMsg": "处理中"}})
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	target, _ := url.Parse(server.URL)
	jar, _ := cookiejar.New(nil)
	httpClient := &http.Client{Transport: rewriteTransport{target: target, base: http.DefaultTransport}, Jar: jar}
	client := NewClient(httpClient)
	client.BaseURL = server.URL

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if raw, _ := db.DB(); raw != nil {
		raw.SetMaxOpenConns(1)
	}
	if err := db.AutoMigrate(&accountModel{}, &upstreamSettingsModel{}, &operationModel{}); err != nil {
		t.Fatal(err)
	}
	service := NewService(db, nil)
	account := testAccount("owner@example.com", "unused", "token")
	if err := db.Create(&account).Error; err != nil {
		t.Fatal(err)
	}
	service.client = client
	proxies := &proxyProviderStub{configs: []*proxyapp.ProxyConfig{{Direct: true}}}
	service.SetProxyProvider(proxies)
	card := CardProfile{
		Number: "4111111111111111", ExpiryMonth: 8, ExpiryYear: 2030, Holder: "Test User",
		BillingEmail: "owner@example.com", FirstName: "Test", LastName: "User", Phone: "6505438765",
		Country: "US", City: "Mountain View", Address: "1295 Charleston Rd",
	}
	encodedCard, err := json.Marshal(card)
	if err != nil {
		t.Fatal(err)
	}
	settings := upstreamSettingsModel{ID: upstreamSettingsID, AccountID: &account.ID, CardData: jsonText(encodedCard), Balance: "0"}
	if err := db.Create(&settings).Error; err != nil {
		t.Fatal(err)
	}
	service.now = func() time.Time { return time.Date(2026, time.August, 16, 0, 0, 0, 0, time.UTC) }
	cvc, err := json.Marshal(struct {
		CVC string `json:"cvc"`
	}{CVC: "123"})
	if err != nil {
		t.Fatal(err)
	}
	operation := operationModel{
		Kind: string(OperationRecharge), AccountID: account.ID, RequestedCount: 1, Amount: "10",
		Status: string(OperationQueued), SecretPayload: jsonText(cvc), QueuedAt: service.now(),
	}
	if err := db.Create(&operation).Error; err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains([]byte(operation.SecretPayload), []byte("123")) {
		t.Fatal("queued CVC was not stored as plain JSON")
	}
	if err := service.processOperation(context.Background(), operation.ID); err != nil {
		t.Fatal(err)
	}
	if err := service.processOperation(context.Background(), operation.ID); err != nil {
		t.Fatal(err)
	}
	var stored operationModel
	if err := db.First(&stored, operation.ID).Error; err != nil {
		t.Fatal(err)
	}
	references, err := decodeOperationRefs([]byte(stored.ProviderOrderNos))
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != string(OperationFailed) || !slices.Equal(references.OrderNos, []string{"TOPUP-1"}) ||
		references.OutTransNo != "OUT-1" || references.PayOrderID != "PAY-1" {
		t.Fatalf("stored status=%q references=%v", stored.Status, references)
	}
	if len(stored.SecretPayload) != 0 || stored.Attempts != 1 {
		t.Fatalf("secret bytes=%d attempts=%d", len(stored.SecretPayload), stored.Attempts)
	}
	if len(proxies.requests) != 1 {
		t.Fatalf("proxy sessions = %d, want 1", len(proxies.requests))
	}
	if len(userAgents) != 1 {
		t.Fatalf("fingerprints = %d, want 1", len(userAgents))
	}
	if _, empty := userAgents[""]; empty {
		t.Fatal("payment requests did not carry a browser fingerprint")
	}
	wantWaits := []time.Duration{0, 5 * time.Second, 5 * time.Second, 5 * time.Second, 5 * time.Second, 5 * time.Second}
	if !slices.Equal(waits, wantWaits) {
		t.Fatalf("query waits = %v", waits)
	}
	wantWrites := map[string]int{
		"/rechargeCampaignRecord/createBalanceOrder": 1,
		"/payment/create":                                 1,
		"/api/pay/cashier/billing":                        1,
		"/api/pay/cashier/payment":                        1,
		"/pay-web-h5/cnp_pay/cardBinInJwt":                1,
		"/pay-web-h5/cnp_pay/checkCardBinInJwt/SESSION-1": 1,
		"/payment/query":                                  rechargeQueryCount,
	}
	for path, want := range wantWrites {
		if writes[path] != want {
			t.Fatalf("%s writes = %d, want %d", path, writes[path], want)
		}
	}
	for _, path := range []string{
		"/fp/tags.js", "/fp/tags", "/fp/clear.png", "/V1/Cruise/Collect",
		"/DeviceFingerprintWeb/V2/Browser/Render", "/DeviceFingerprintWeb/V2/Browser/SaveBrowserData",
		"/method", "/content-server/api/tds2-fingerprintjs", "/tds-method-post-device-data", "/notify",
	} {
		if calls[path] != 1 {
			t.Fatalf("%s calls = %d, want 1", path, calls[path])
		}
	}
}

func TestRechargeQueryPollingStopsOnTerminalResult(t *testing.T) {
	for _, test := range []struct {
		name         string
		results      []map[string]any
		wantPaid     bool
		wantRejected bool
	}{
		{name: "success", results: []map[string]any{{"resultCode": "PENDING"}, {"resultCode": "0000"}}, wantPaid: true},
		{name: "rejected", results: []map[string]any{{"resultCode": "9996"}}, wantRejected: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				if request.URL.Path != "/payment/query" {
					http.NotFound(response, request)
					return
				}
				result := test.results[min(calls, len(test.results)-1)]
				calls++
				_ = json.NewEncoder(response).Encode(map[string]any{"code": 200, "data": result})
			}))
			defer server.Close()
			client := NewClient(server.Client())
			client.BaseURL = server.URL
			originalWait := rechargeQueryWait
			waits := make([]time.Duration, 0, len(test.results))
			rechargeQueryWait = func(_ context.Context, delay time.Duration) error {
				waits = append(waits, delay)
				return nil
			}
			t.Cleanup(func() { rechargeQueryWait = originalWait })

			paid, err := client.pollRechargePayment(context.Background(), "token", rechargePayment{
				OriginalOrder: "OUT-1", PaymentOrderID: "PAY-1",
			})
			if paid != test.wantPaid || errors.Is(err, errPaymentRejected) != test.wantRejected {
				t.Fatalf("paid=%v err=%v", paid, err)
			}
			if test.wantRejected && errors.Is(err, ErrPaymentUncertain) {
				t.Fatalf("explicit rejection was marked uncertain: %v", err)
			}
			if !test.wantRejected && err != nil {
				t.Fatal(err)
			}
			if calls != len(test.results) || len(waits) != calls {
				t.Fatalf("calls=%d waits=%v", calls, waits)
			}
		})
	}
}

func TestPaymentRedirectsRequireAllowlistedGET(t *testing.T) {
	storePay, _ := url.Parse("https://international.storepay.cn/cashier")
	allinPay, _ := url.Parse("https://oats.allinpay.com/checkout")
	cardinal, _ := url.Parse("https://geo.cardinalcommerce.com/DeviceFingerprintWeb/V2/Browser/Render")
	evil, _ := url.Parse("https://example.com/collect")
	via := []*url.URL{storePay}
	if err := validateKitesimRedirect(allinPay, via, http.MethodGet, redirectScopePayment); err != nil {
		t.Fatalf("allowlisted payment redirect was blocked: %v", err)
	}
	if err := validateKitesimRedirect(cardinal, []*url.URL{allinPay}, http.MethodGet, redirectScopePayment); err != nil {
		t.Fatalf("allowlisted fingerprint redirect was blocked: %v", err)
	}
	if err := validateKitesimRedirect(allinPay, via, http.MethodPost, redirectScopePayment); err == nil {
		t.Fatal("cross-origin payment form POST was allowed")
	}
	if err := validateKitesimRedirect(evil, via, http.MethodGet, redirectScopePayment); err == nil {
		t.Fatal("untrusted payment redirect was allowed")
	}
}

func TestRechargeDetectsThreeDSChallenge(t *testing.T) {
	if !looksLike3DS([]byte(`<form><input type="hidden" name='creq' value="challenge"></form>`)) {
		t.Fatal("3DS challenge was not detected")
	}
	cashierURL, err := findAllinPayURL([]byte(`<script>window.next = 'https:\/\/oats.allinpay.com/payh5/cnp_pay/directCashier?accessOrderId=A1&amp;mchtId=M1';</script>`), "")
	if err != nil || !strings.Contains(cashierURL, "directCashier?accessOrderId=A1&mchtId=M1") {
		t.Fatalf("embedded Allinpay URL = %q, err=%v", cashierURL, err)
	}
}
