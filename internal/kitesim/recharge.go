package kitesim

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	stdhtml "html"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	httpfingerprint "github.com/bogdanfinn/fhttp"
	"github.com/donnel666/remail/internal/platform"
	proxydomain "github.com/donnel666/remail/internal/proxy/domain"
	"github.com/shopspring/decimal"
	xhtml "golang.org/x/net/html"
	"gorm.io/gorm"
)

const (
	storePayBaseURL        = "https://international.storepay.cn"
	allinPayBaseURL        = "https://oats.allinpay.com"
	cardinalGeoBaseURL     = "https://geo.cardinalcommerce.com"
	cardinalAPIBaseURL     = "https://centinelapi.cardinalcommerce.com"
	arcotBaseURL           = "https://secure5.arcot.com"
	threatMetrixBaseURL    = "https://h.online-metrix.net"
	kitesimH5URL           = "https://h5.kitesim.co"
	threatMetrixOrgID      = "k8vif92e"
	cardinalFingerprint    = "77bac6b6fd899ab2fffaad369ea7e89e"
	arcotFingerprint       = "2950bba941b66f15e7662474be2c8a3d"
	arcotAudioFingerprint  = "124.04347527516074"
	paymentLanguage        = "zh-CN"
	rechargeQueryCount     = 6
	rechargeQueryInterval  = 5 * time.Second
	paymentScreenWidth     = 2560
	paymentScreenHeight    = 1440
	paymentAvailableHeight = 1392
	paymentTimeOffset      = -480
)

const paymentWebGLVendor = "Google Inc. (NVIDIA)~ANGLE (NVIDIA, NVIDIA GeForce RTX 4090 (0x00002684) Direct3D11 vs_5_0 ps_5_0, D3D11)"

var paymentPDFPlugins = []string{
	"PDF Viewer::Portable Document Format::application/pdf~pdf,text/pdf~pdf",
	"Chrome PDF Viewer::Portable Document Format::application/pdf~pdf,text/pdf~pdf",
	"Chromium PDF Viewer::Portable Document Format::application/pdf~pdf,text/pdf~pdf",
	"Microsoft Edge PDF Viewer::Portable Document Format::application/pdf~pdf,text/pdf~pdf",
	"WebKit built-in PDF::Portable Document Format::application/pdf~pdf,text/pdf~pdf",
}

var errPaymentRejected = errors.New("kitesim: card payment was rejected")
var rechargeQueryWait = waitRechargeQuery

type rechargePayment struct {
	CashierURL     string
	OriginalOrder  string
	PaymentOrderID string
}

type paymentPage struct {
	Body []byte
	URL  string
}

func (s *Service) executeRecharge(ctx context.Context, operation operationModel, cvc string) (string, error) {
	amount, err := decimal.NewFromString(strings.TrimSpace(operation.Amount))
	if err != nil || !amount.IsPositive() || amount.GreaterThan(decimal.NewFromInt(10000)) || !validCVC(cvc) {
		return "", ErrInvalidInput
	}
	settings, err := s.loadUpstreamSettings(ctx)
	if err != nil {
		return "", err
	}
	if settings.AccountID == nil || *settings.AccountID != operation.AccountID {
		return "", ErrUpstreamNotConfigured
	}
	if len(settings.CardData) == 0 {
		return "", ErrCardNotConfigured
	}
	if settings.CardRevision != operation.CardRevision {
		return "", ErrOperationState
	}
	var card CardProfile
	if err := json.Unmarshal([]byte(settings.CardData), &card); err != nil {
		return "", err
	}
	var account accountModel
	if err := s.db.WithContext(ctx).Where("id = ? AND deleted_at IS NULL", operation.AccountID).First(&account).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", ErrAccountMissing
		}
		return "", fmt.Errorf("load Kitesim recharge account: %w", err)
	}
	applyKitesimCardDefaults(&card, account.Account)
	if _, err := normalizeCard(&card, s.now()); err != nil {
		return "", err
	}

	orderNo := ""
	err = s.withSingleUpstreamClient(ctx, account.Account, proxydomain.ProxyPurposeAuth, func(client *Client) error {
		token, err := s.authenticateOperationClient(ctx, client, account)
		if err != nil {
			return err
		}
		orderNo, err = client.createRechargeOrder(ctx, token, amount.String())
		if err != nil {
			return err
		}
		refs := operationProviderRefs{OrderNos: []string{orderNo}}
		if err := s.recordOperationProgress(ctx, operation.ID, 0, refs); err != nil {
			return err
		}
		payment, err := client.createRechargePayment(ctx, token, orderNo, amount.String())
		if err != nil {
			return err
		}
		refs.OutTransNo, refs.PayOrderID = payment.OriginalOrder, payment.PaymentOrderID
		if err := s.recordOperationProgress(ctx, operation.ID, 0, refs); err != nil {
			return err
		}
		if err := client.payRecharge(ctx, token, payment, card, strings.TrimSpace(cvc)); err != nil {
			return err
		}
		if err := s.recordOperationProgress(ctx, operation.ID, 1, refs); err != nil {
			return errors.Join(ErrPaymentUncertain, err)
		}
		return nil
	})
	return orderNo, err
}

func (c *Client) createRechargeOrder(ctx context.Context, token, amount string) (string, error) {
	var response apiResponse[stringValue]
	if err := c.doJSON(ctx, http.MethodPost, "/rechargeCampaignRecord/createBalanceOrder", nil, token, map[string]any{
		"rechargeCampaignId": nil,
		"actualAmount":       json.Number(amount),
		"rechargeMethod":     8,
	}, &response); err != nil {
		return "", err
	}
	orderNo := strings.TrimSpace(string(response.Data))
	if response.Code != http.StatusOK {
		return "", remoteError(response.Code, response.Message)
	}
	if orderNo == "" {
		return "", errors.New("kitesim: recharge order number missing")
	}
	return orderNo, nil
}

func (c *Client) createRechargePayment(ctx context.Context, token, orderNo, amount string) (rechargePayment, error) {
	var response apiResponse[json.RawMessage]
	if err := c.doJSON(ctx, http.MethodPost, "/payment/create", nil, token, map[string]any{
		"transBusinessType":    3,
		"orderNo":              strings.TrimSpace(orderNo),
		"amount":               json.Number(amount),
		"currency":             "HKD",
		"transType":            "",
		"thirdPartyPayChannel": "onlypay",
		"returnUrl":            kitesimH5URL + "/#/pages/successful/successful?back=/pages/my/recharge",
	}, &response); err != nil {
		return rechargePayment{}, err
	}
	if response.Code != http.StatusOK {
		return rechargePayment{}, remoteError(response.Code, response.Message)
	}
	payment, err := parseRechargePayment(response.Data)
	if err != nil {
		return rechargePayment{}, err
	}
	return payment, nil
}

func parseRechargePayment(raw json.RawMessage) (rechargePayment, error) {
	var direct string
	if json.Unmarshal(raw, &direct) == nil && strings.HasPrefix(strings.TrimSpace(direct), "https://") {
		_, err := providerURL(direct, "international.storepay.cn")
		if err != nil {
			return rechargePayment{}, err
		}
		return rechargePayment{}, errors.New("kitesim: payment identifiers missing")
	}
	var value struct {
		CodeURL         stringValue `json:"codeUrl"`
		PayInfo         stringValue `json:"payInfo"`
		PayURL          stringValue `json:"payUrl"`
		OutTransNo      stringValue `json:"outTransNo"`
		OriginalOrderNo stringValue `json:"originalOrderNo"`
		ExtInfo         stringValue `json:"extInfo"`
		PayOrderID      stringValue `json:"payOrderId"`
	}
	if err := json.Unmarshal(raw, &value); err != nil {
		return rechargePayment{}, errors.New("kitesim: invalid payment creation response")
	}
	cashier := firstNonEmpty(string(value.CodeURL), string(value.PayInfo), string(value.PayURL))
	cashierURL, err := providerURL(cashier, "international.storepay.cn")
	if err != nil {
		return rechargePayment{}, err
	}
	payment := rechargePayment{
		CashierURL:     cashierURL,
		OriginalOrder:  firstNonEmpty(string(value.OutTransNo), string(value.OriginalOrderNo)),
		PaymentOrderID: firstNonEmpty(string(value.ExtInfo), string(value.PayOrderID)),
	}
	if payment.OriginalOrder == "" || payment.PaymentOrderID == "" {
		return rechargePayment{}, errors.New("kitesim: payment identifiers missing")
	}
	return payment, nil
}

func (c *Client) payRecharge(ctx context.Context, token string, payment rechargePayment, card CardProfile, cvc string) error {
	cashier, err := c.paymentRequest(ctx, http.MethodGet, payment.CashierURL, nil, "", kitesimH5URL+"/", false, true)
	if err != nil {
		return err
	}
	sign, err := pickCardSign(cashier.Body)
	if err != nil {
		return err
	}
	billing, err := c.paymentRequest(ctx, http.MethodPost, storePayBaseURL+"/api/pay/cashier/billing", url.Values{
		"sign":      {sign},
		"firstName": {card.FirstName},
		"lastName":  {card.LastName},
		"email":     {card.BillingEmail},
		"phone":     {card.Phone},
		"country":   {card.Country},
		"city":      {card.City},
		"address":   {card.Address},
	}, storePayBaseURL, cashier.URL, true, false)
	if err != nil {
		return err
	}
	var billed struct {
		Valid bool `json:"valid"`
	}
	if json.Unmarshal(billing.Body, &billed) != nil || !billed.Valid {
		return errors.New("kitesim: billing validation failed")
	}

	cardPage, providerErr := c.paymentRequest(ctx, http.MethodPost, storePayBaseURL+"/api/pay/cashier/payment", url.Values{
		"sign":       {sign},
		"cardNo":     {formatCardNumber(card.Number)},
		"expiryDate": {fmt.Sprintf("%02d / %02d", card.ExpiryMonth, card.ExpiryYear%100)},
		"secureCode": {cvc},
		"cardHolder": {card.Holder},
	}, storePayBaseURL, cashier.URL, false, true)
	if providerErr == nil {
		providerErr = c.submitAllinPay(ctx, cardPage, card)
	}
	paid, queryErr := c.pollRechargePayment(ctx, token, payment)
	if paid {
		return nil
	}
	if errors.Is(queryErr, errPaymentRejected) {
		return queryErr
	}
	if errors.Is(providerErr, ErrThreeDSRequired) {
		return ErrThreeDSRequired
	}
	return errors.Join(ErrPaymentUncertain, providerErr, queryErr)
}

func (c *Client) pollRechargePayment(ctx context.Context, token string, payment rechargePayment) (bool, error) {
	var lastErr error
	for attempt := range rechargeQueryCount {
		delay := time.Duration(0)
		if attempt > 0 {
			delay = rechargeQueryInterval
		}
		if err := rechargeQueryWait(ctx, delay); err != nil {
			return false, errors.Join(ErrPaymentUncertain, err)
		}
		paid, err := c.queryRechargePayment(ctx, token, payment)
		if paid || errors.Is(err, errPaymentRejected) {
			return paid, err
		}
		lastErr = err
	}
	return false, errors.Join(ErrPaymentUncertain, lastErr)
}

func waitRechargeQuery(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			return nil
		}
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (c *Client) submitAllinPay(ctx context.Context, source paymentPage, card CardProfile) error {
	cashierURL, err := findAllinPayURL(source.Body, source.URL)
	if err != nil {
		if looksLike3DS(source.Body) {
			return ErrThreeDSRequired
		}
		return err
	}
	page, err := c.paymentRequest(ctx, http.MethodGet, cashierURL, nil, allinPayBaseURL, cashierURL, false, true)
	if err != nil {
		return err
	}
	pageURL, _ := url.Parse(page.URL)
	query := url.Values{}
	if pageURL != nil {
		query = pageURL.Query()
	}
	accessOrderID := firstNonEmpty(query.Get("accessOrderId"), inputValue(page.Body, "accessOrderId"))
	merchantID := firstNonEmpty(query.Get("mchtId"), inputValue(page.Body, "mchtId"))
	accessCode := firstNonEmpty(query.Get("accessCode"), inputValue(page.Body, "accessCode"))
	if accessOrderID == "" {
		return errors.New("kitesim: Allinpay order identifier missing")
	}
	_ = c.reportThreatMetrix(ctx, merchantID, accessOrderID, page.URL)
	cardType := "VISA"
	if cardBrand(card.Number) == "Mastercard" {
		cardType = "MASTERCARD"
	}
	binPage, err := c.paymentRequest(ctx, http.MethodPost, allinPayBaseURL+"/pay-web-h5/cnp_pay/cardBinInJwt", url.Values{
		"accessOrderId":    {accessOrderID},
		"mchtId":           {merchantID},
		"language":         {"en"},
		"accessCode":       {accessCode},
		"cardType":         {cardType},
		"payPageStyle":     {"TINY"},
		"cFlag":            {"dCashier"},
		"realCardNo":       {card.Number},
		"CreditCardNumber": {strings.Repeat("*", len(card.Number)-4) + card.Number[len(card.Number)-4:]},
		"Cardholder":       {card.Holder},
		"ExpirationMonth":  {""},
		"ExpirationYear":   {""},
		"SecurityCode":     {""},
	}, allinPayBaseURL, page.URL, true, false)
	if err != nil {
		return err
	}
	var binResult struct {
		ResultCode stringValue `json:"resultCode"`
		ResultDesc stringValue `json:"resultDesc"`
		SessionID  stringValue `json:"sessionId"`
		JWT        stringValue `json:"jwt"`
		Action     stringValue `json:"action"`
	}
	if json.Unmarshal(binPage.Body, &binResult) != nil || string(binResult.ResultCode) != "0000" {
		return errors.New("kitesim: Allinpay card validation failed")
	}
	sessionID := strings.TrimSpace(string(binResult.SessionID))
	if sessionID == "" {
		return errors.New("kitesim: Allinpay session identifier missing")
	}
	referenceID := jwtReferenceID(string(binResult.JWT))
	if strings.TrimSpace(string(binResult.JWT)) != "" && strings.TrimSpace(string(binResult.Action)) != "" {
		if reportedReferenceID, reportErr := c.reportCardinal(ctx, string(binResult.Action), string(binResult.JWT), card.Number); reportErr == nil && reportedReferenceID != "" {
			referenceID = reportedReferenceID
		}
	}
	checkForm := url.Values{}
	if referenceID != "" {
		checkForm.Set("setUpReferenceID", referenceID)
	}
	checked, err := c.paymentRequest(ctx, http.MethodPost, allinPayBaseURL+"/pay-web-h5/cnp_pay/checkCardBinInJwt/"+url.PathEscape(sessionID), checkForm, allinPayBaseURL, binPage.URL, false, true)
	if err != nil {
		return err
	}
	if looksLike3DS(checked.Body) {
		return ErrThreeDSRequired
	}
	return c.followPaymentForms(ctx, checked, 4)
}

type arcotMethod struct {
	MethodURL                  string `json:"MethodURL"`
	Payload                    string `json:"Payload"`
	ThreeDSServerTransactionID string `json:"ThreeDSServerTransactionId"`
}

type cardinalJWTClaims struct {
	OrgUnitID   string `json:"OrgUnitId"`
	ReferenceID string `json:"ReferenceId"`
}

func (c *Client) reportThreatMetrix(ctx context.Context, merchantID, accessOrderID, referer string) error {
	sessionID := strings.TrimSpace(merchantID) + strings.TrimSpace(accessOrderID)
	if sessionID == "" {
		return errors.New("kitesim: ThreatMetrix session identifier missing")
	}
	nonce := strings.ReplaceAll(platform.NewUUIDV4String(), "-", "")[:16]
	requests := []struct {
		path string
		html bool
	}{
		{path: "/fp/tags.js"},
		{path: "/fp/tags", html: true},
		{path: "/fp/clear.png"},
	}
	for _, item := range requests {
		query := url.Values{"org_id": {threatMetrixOrgID}, "session_id": {sessionID}}
		if item.path == "/fp/clear.png" {
			query.Set("nonce", nonce)
		}
		if _, err := c.paymentRequest(ctx, http.MethodGet, threatMetrixBaseURL+item.path+"?"+query.Encode(), nil, allinPayBaseURL, referer, false, item.html); err != nil {
			return err
		}
	}
	return nil
}

func (c *Client) reportCardinal(ctx context.Context, action, jwt, cardNumber string) (string, error) {
	actionURL, err := providerURL(action, "centinelapi.cardinalcommerce.com")
	if err != nil {
		return "", err
	}
	claims := decodeCardinalJWT(jwt)
	collectPage, err := c.paymentRequest(ctx, http.MethodPost, actionURL, url.Values{
		"Bin": {cardNumber}, "JWT": {jwt},
	}, allinPayBaseURL, allinPayBaseURL+"/", false, true)
	if err != nil {
		return "", err
	}
	orgUnitID := firstNonEmpty(claims.OrgUnitID, inputValue(collectPage.Body, "orgUnitId"))
	referenceID := firstNonEmpty(claims.ReferenceID, inputValue(collectPage.Body, "referenceId"))
	renderURL := inputValue(collectPage.Body, "dfUrlFullValue")
	if renderURL == "" {
		query := url.Values{
			"referenceId": {referenceID}, "orgUnitId": {orgUnitID},
			"threatmetrix": {"true"}, "tmEventType": {"PAYMENT"}, "geolocation": {"false"},
			"alias": {"Default"}, "origin": {"CruiseAPI"},
		}
		renderURL = cardinalGeoBaseURL + "/DeviceFingerprintWeb/V2/Browser/Render?" + query.Encode()
	} else {
		parsed, parseErr := url.Parse(renderURL)
		if parseErr != nil {
			return "", parseErr
		}
		query := parsed.Query()
		if query.Get("origin") == "" {
			query.Set("origin", "CruiseAPI")
			parsed.RawQuery = query.Encode()
		}
		renderURL = parsed.String()
	}
	renderURL, err = providerURL(renderURL, "geo.cardinalcommerce.com")
	if err != nil {
		return "", err
	}
	nonce := platform.NewUUIDV4String()
	renderPage, err := c.paymentRequest(ctx, http.MethodPost, renderURL, url.Values{
		"bin": {cardNumber}, "nonce": {nonce},
	}, cardinalAPIBaseURL, cardinalAPIBaseURL+"/", false, true)
	if err != nil {
		return "", err
	}
	_ = c.saveCardinalBrowserData(ctx, orgUnitID, referenceID, nonce, renderURL)
	var profile struct {
		Features struct {
			Methods struct {
				Items []arcotMethod `json:"methodUrls"`
			} `json:"merchantMethodUrlCollection"`
		} `json:"features"`
	}
	if decodeJSONAfter(renderPage.Body, "profiler.start(", &profile) == nil {
		for _, method := range profile.Features.Methods.Items {
			_ = c.reportArcotMethod(ctx, method)
		}
	}
	return strings.TrimSpace(referenceID), nil
}

func (c *Client) saveCardinalBrowserData(ctx context.Context, orgUnitID, referenceID, nonce, renderURL string) error {
	body := map[string]any{
		"Cookies":       map[string]bool{"Legacy": true, "LocalStorage": true, "SessionStorage": true},
		"DeviceChannel": "Browser",
		"Extended": map[string]any{
			"Browser": map[string]any{"Adblock": true, "AvailableJsFonts": []string{}, "DoNotTrack": "unknown", "JavaEnabled": false},
			"Device": map[string]any{
				"ColorDepth": 24, "Cpu": "unknown", "Platform": "Win32",
				"TouchSupport": map[string]any{"MaxTouchPoints": 0, "OnTouchStartAvailable": false, "TouchEventCreationSuccessful": false},
			},
		},
		"Fingerprint": cardinalFingerprint, "FingerprintingTime": 57,
		"FingerprintDetails": map[string]string{"Version": "1.5.1"},
		"Language":           paymentLanguage, "Latitude": nil, "Longitude": nil,
		"OrgUnitId": orgUnitID, "Origin": "CruiseAPI", "Plugins": paymentPDFPlugins,
		"ReferenceId": referenceID, "Referrer": cardinalAPIBaseURL + "/",
		"Screen": map[string]any{
			"FakedResolution": false, "Ratio": float64(paymentScreenWidth) / paymentScreenHeight,
			"Resolution":       fmt.Sprintf("%dx%d", paymentScreenWidth, paymentScreenHeight),
			"UsableResolution": fmt.Sprintf("%dx%d", paymentScreenWidth, paymentAvailableHeight), "CCAScreenSize": "02",
		},
		"CallSignEnabled": nil, "ThreatMetrixEnabled": false, "ThreatMetrixEventType": "PAYMENT",
		"ThreatMetrixAlias": "Default", "TimeOffset": paymentTimeOffset, "UserAgent": c.userAgent,
		"UserAgentDetails":    map[string]bool{"FakedOS": false, "FakedBrowser": false},
		"VcdiClientRequestId": nil, "BinSessionId": nonce,
	}
	_, err := c.paymentJSONRequest(ctx, http.MethodPost, cardinalGeoBaseURL+"/DeviceFingerprintWeb/V2/Browser/SaveBrowserData", body, cardinalGeoBaseURL, renderURL, true)
	return err
}

func (c *Client) reportArcotMethod(ctx context.Context, method arcotMethod) error {
	methodURL, err := providerURL(method.MethodURL, "secure5.arcot.com")
	if err != nil || strings.TrimSpace(method.Payload) == "" {
		return errors.New("kitesim: invalid Arcot method")
	}
	methodPage, err := c.paymentRequest(ctx, http.MethodPost, methodURL, url.Values{
		"threeDSMethodData": {method.Payload},
	}, cardinalGeoBaseURL, cardinalGeoBaseURL+"/", false, true)
	if err != nil {
		return err
	}
	postURL := quotedJSValue(string(methodPage.Body), "form.action")
	if !strings.Contains(postURL, "tds-method-post-device-data") {
		postURL = ""
	}
	notifyURL := quotedJSValue(string(methodPage.Body), "notifURLValue")
	transactionID := firstNonEmpty(method.ThreeDSServerTransactionID, quotedJSValue(string(methodPage.Body), "txnId"))
	payloadClaims := decodeThreeDSMethodPayload(method.Payload)
	notifyURL = firstNonEmpty(notifyURL, payloadClaims.NotificationURL)
	transactionID = firstNonEmpty(transactionID, payloadClaims.TransactionID)
	deviceInfo, err := base64JSON(c.arcotDeviceInfo())
	if err != nil {
		return err
	}
	if _, err := c.paymentJSONRequest(ctx, http.MethodPost, arcotBaseURL+"/content-server/api/tds2-fingerprintjs", map[string]any{
		"fingerprintId": arcotFingerprint, "txnid": transactionID, "tm_deviceid": "",
		"tm_devicedata": c.userAgent, "b64DeviceInfo": deviceInfo,
	}, arcotBaseURL, methodURL, false); err != nil {
		return err
	}
	if postURL != "" {
		postURL, err = providerURL(postURL, "secure5.arcot.com")
		if err != nil {
			return err
		}
		deviceData, marshalErr := json.Marshal(c.arcotDeviceData())
		if marshalErr != nil {
			return marshalErr
		}
		started := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
		timings, marshalErr := json.Marshal(map[string]any{
			"tds_post_data_fingerprintjs2_start_time": started, "tds_post_data_arcotddna_start_time": started,
			"tds_post_data_arcotddna_end_time": started, "tds_post_data_arcotddna_elapsed_time": 58,
			"tds_post_data_tdsscript_start_time": started, "tds_post_data_tdsscript_end_time": started,
			"tds_post_data_tdsscript_elapsed_time": 270, "tds_post_data_fingerprintjs2_end_time": started,
			"tds_post_data_fingerprintjs2_elapsed_time": 267, "tds_post_data_form_submit": "graceful",
			"tds_post_data_fpapi_elapsed_time": 225, "tds_post_data_fpapi_error_msg": "",
		})
		if marshalErr != nil {
			return marshalErr
		}
		if _, err := c.paymentRequest(ctx, http.MethodPost, postURL, url.Values{
			"threeDSServerTransId": {transactionID}, "threeDSMethodNotificationURL": {notifyURL},
			"deviceData": {string(deviceData)}, "extender": {""}, "domain": {""}, "org": {""},
			"scriptTimings": {string(timings)},
		}, arcotBaseURL, methodURL, false, true); err != nil {
			return err
		}
	}
	if notifyURL != "" {
		notifyURL, err = providerURL(notifyURL, "secure5.arcot.com", "oats.allinpay.com", "centinelapi.cardinalcommerce.com", "geo.cardinalcommerce.com")
		if err != nil {
			return err
		}
		notification, encodeErr := base64JSON(map[string]string{"threeDSServerTransID": transactionID})
		if encodeErr != nil {
			return encodeErr
		}
		if _, err := c.paymentRequest(ctx, http.MethodPost, notifyURL, url.Values{"threeDSMethodData": {notification}}, arcotBaseURL, arcotBaseURL+"/", false, true); err != nil {
			return err
		}
	}
	return nil
}

func (c *Client) arcotDeviceInfo() map[string]any {
	plugins := make([]any, 0, len(paymentPDFPlugins))
	for _, plugin := range paymentPDFPlugins {
		name := strings.SplitN(plugin, "::", 2)[0]
		plugins = append(plugins, []any{name, "Portable Document Format", [][]string{{"application/pdf", "pdf"}, {"text/pdf", "pdf"}}})
	}
	webGL := md5.Sum([]byte(paymentWebGLVendor))
	return map[string]any{
		"audio": arcotAudioFingerprint, "colorGamut": "srgb", "dyanmicRange": false,
		"fingerprintHash": arcotFingerprint, "hardwareConcurrency": 32,
		"height": paymentScreenHeight, "language": paymentLanguage, "motionReduced": false,
		"openDatabase": false, "platform": "Win32", "plugins": plugins,
		"timezone": "Asia/Shanghai", "touchSupport": []any{0, false, false}, "userAgent": c.userAgent,
		"webgl":                  []string{"data:image/png;base64," + base64.StdEncoding.EncodeToString(webGL[:])},
		"webglVendorAndRenderer": paymentWebGLVendor, "width": paymentScreenWidth,
	}
}

func (c *Client) arcotDeviceData() map[string]any {
	return map[string]any{
		"VERSION": "2.0",
		"MFP": map[string]any{
			"Browser":   map[string]any{"UserAgent": c.userAgent, "Vendor": "Google Inc.", "VendorSubID": "", "BuildID": "20030107", "CookieEnabled": true},
			"IEPlugins": map[string]any{},
			"NetscapePlugins": map[string]string{
				"PDF Viewer": "", "Chrome PDF Viewer": "", "Chromium PDF Viewer": "",
				"Microsoft Edge PDF Viewer": "", "WebKit built-in PDF": "",
			},
			"Screen": map[string]int{
				"FullHeight": paymentScreenHeight, "AvlHeight": paymentAvailableHeight,
				"FullWidth": paymentScreenWidth, "AvlWidth": paymentScreenWidth, "ColorDepth": 24, "PixelDepth": 24,
			},
			"System": map[string]any{"Platform": "Win32", "systemLanguage": paymentLanguage, "Timezone": paymentTimeOffset},
		},
		"ExternalIP": "",
	}
}

type threeDSMethodPayload struct {
	NotificationURL string `json:"threeDSMethodNotificationURL"`
	TransactionID   string `json:"threeDSServerTransID"`
}

func decodeThreeDSMethodPayload(value string) threeDSMethodPayload {
	var result threeDSMethodPayload
	encoded := strings.TrimSpace(value)
	for len(encoded)%4 != 0 {
		encoded += "="
	}
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		decoded, err = base64.URLEncoding.DecodeString(encoded)
	}
	if err == nil {
		_ = json.Unmarshal(decoded, &result)
	}
	return result
}

func base64JSON(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(encoded), nil
}

func decodeJSONAfter(body []byte, marker string, value any) error {
	index := bytes.Index(body, []byte(marker))
	if index < 0 {
		return errors.New("kitesim: embedded JSON marker missing")
	}
	start := bytes.IndexByte(body[index+len(marker):], '{')
	if start < 0 {
		return errors.New("kitesim: embedded JSON missing")
	}
	return json.NewDecoder(bytes.NewReader(body[index+len(marker)+start:])).Decode(value)
}

func (c *Client) queryRechargePayment(ctx context.Context, token string, payment rechargePayment) (bool, error) {
	var response apiResponse[json.RawMessage]
	if err := c.doJSON(ctx, http.MethodPost, "/payment/query", nil, token, map[string]string{
		"originalOrderNo":      payment.OriginalOrder,
		"payOrderId":           payment.PaymentOrderID,
		"thirdPartyPayChannel": "onlypay",
	}, &response); err != nil {
		return false, err
	}
	if response.Code != http.StatusOK {
		return false, remoteError(response.Code, response.Message)
	}
	var result struct {
		ResultMsg  stringValue `json:"resultMsg"`
		ResultCode stringValue `json:"resultCode"`
	}
	if err := json.Unmarshal(response.Data, &result); err != nil {
		return false, errors.New("kitesim: invalid payment query response")
	}
	code := strings.TrimSpace(string(result.ResultCode))
	if strings.TrimSpace(string(result.ResultMsg)) == "支付成功" || code == "0000" {
		return true, nil
	}
	switch code {
	case "9996", "0029", "R000", "9998":
		return false, errPaymentRejected
	default:
		return false, ErrPaymentUncertain
	}
}

func (c *Client) paymentRequest(ctx context.Context, method, requestURL string, form url.Values, origin, referer string, ajax, htmlPage bool) (paymentPage, error) {
	var body io.Reader
	contentType := ""
	if form != nil {
		body = strings.NewReader(form.Encode())
		contentType = "application/x-www-form-urlencoded"
	}
	return c.paymentRequestBody(ctx, method, requestURL, body, contentType, origin, referer, ajax, htmlPage)
}

func (c *Client) paymentJSONRequest(ctx context.Context, method, requestURL string, value any, origin, referer string, ajax bool) (paymentPage, error) {
	payload, err := json.Marshal(value)
	if err != nil {
		return paymentPage{}, err
	}
	return c.paymentRequestBody(ctx, method, requestURL, bytes.NewReader(payload), "application/json", origin, referer, ajax, false)
}

func (c *Client) paymentRequestBody(ctx context.Context, method, requestURL string, body io.Reader, contentType, origin, referer string, ajax, htmlPage bool) (paymentPage, error) {
	if c == nil || c.HTTP == nil {
		return paymentPage{}, errors.New("kitesim: client unavailable")
	}
	request, err := http.NewRequestWithContext(
		context.WithValue(ctx, redirectScopeContextKey{}, redirectScopePayment),
		method,
		requestURL,
		body,
	)
	if err != nil {
		return paymentPage{}, err
	}
	request.Header.Set("User-Agent", c.userAgent)
	for _, name := range []string{"Accept-Language", "sec-ch-ua", "sec-ch-ua-mobile", "sec-ch-ua-platform", "sec-ch-ua-platform-version"} {
		if value := c.headers[name]; value != "" {
			request.Header.Set(name, value)
		}
	}
	if htmlPage {
		request.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8")
		request.Header.Set("Upgrade-Insecure-Requests", "1")
	} else {
		request.Header.Set("Accept", "*/*")
	}
	if origin != "" {
		request.Header.Set("Origin", origin)
	}
	if referer != "" {
		request.Header.Set("Referer", referer)
	}
	if ajax {
		request.Header.Set("X-Requested-With", "XMLHttpRequest")
	}
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	if !c.customHTTP {
		request.Header[httpfingerprint.HeaderOrderKey] = append([]string(nil), browserHeaderOrder...)
	}
	response, err := c.HTTP.Do(request)
	if err != nil {
		return paymentPage{}, &upstreamTransportError{err: err, proxy: c.usesProxy}
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return paymentPage{}, &upstreamStatusError{status: response.StatusCode, proxy: c.usesProxy}
	}
	payload, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return paymentPage{}, &upstreamTransportError{err: err, proxy: c.usesProxy}
	}
	finalURL := requestURL
	if response.Request != nil && response.Request.URL != nil {
		finalURL = response.Request.URL.String()
	}
	return paymentPage{Body: payload, URL: finalURL}, nil
}

func pickCardSign(body []byte) (string, error) {
	document, err := xhtml.Parse(strings.NewReader(string(body)))
	if err != nil {
		return "", errors.New("kitesim: invalid cashier HTML")
	}
	first, preferred := "", ""
	walkHTML(document, func(node *xhtml.Node) bool {
		if node.Type != xhtml.ElementNode || node.Data != "input" || htmlAttr(node, "name") != "paymentMethod" || truthy(htmlAttr(node, "data-redirect")) {
			return true
		}
		sign, err := url.PathUnescape(htmlAttr(node, "data-sign"))
		if err != nil || sign == "" {
			return true
		}
		if first == "" {
			first = sign
		}
		if htmlAttr(node, "value") == "bankCard" {
			preferred = sign
			return false
		}
		return true
	})
	if preferred != "" {
		return preferred, nil
	}
	if first == "" {
		return "", errors.New("kitesim: bank card payment method unavailable")
	}
	return first, nil
}

func findAllinPayURL(body []byte, currentURL string) (string, error) {
	if candidate, err := providerURL(currentURL, "oats.allinpay.com"); err == nil {
		return candidate, nil
	}
	document, err := xhtml.Parse(strings.NewReader(string(body)))
	if err != nil {
		return "", errors.New("kitesim: invalid OnlyPay HTML")
	}
	result := ""
	walkHTML(document, func(node *xhtml.Node) bool {
		if node.Type == xhtml.ElementNode {
			for _, name := range []string{"href", "action"} {
				if candidate, err := providerURL(normalizeJSURL(htmlAttr(node, name)), "oats.allinpay.com"); err == nil {
					result = candidate
					return false
				}
			}
		}
		if node.Type == xhtml.ElementNode && node.Data == "script" {
			if candidate, err := providerURL(quotedJSValue(htmlText(node), "jumpUrl"), "oats.allinpay.com"); err == nil {
				result = candidate
				return false
			}
		}
		return true
	})
	if result == "" {
		if candidate, err := embeddedProviderURL(body, "oats.allinpay.com"); err == nil {
			return candidate, nil
		}
		return "", errors.New("kitesim: Allinpay cashier URL missing")
	}
	return result, nil
}

func (c *Client) followPaymentForms(ctx context.Context, page paymentPage, hops int) error {
	for range hops {
		if looksLike3DS(page.Body) {
			return ErrThreeDSRequired
		}
		form, ok, err := firstHTMLForm(page.Body, page.URL)
		if err != nil || !ok {
			return err
		}
		if _, err := providerURL(form.Action, "international.storepay.cn", "oats.allinpay.com", "h5.kitesim.co", "api.kitesim.co"); err != nil {
			return err
		}
		requestURL := form.Action
		values := form.Values
		if form.Method == http.MethodGet {
			parsed, _ := url.Parse(requestURL)
			query := parsed.Query()
			for key, items := range values {
				for _, item := range items {
					query.Add(key, item)
				}
			}
			parsed.RawQuery = query.Encode()
			requestURL, values = parsed.String(), nil
		}
		page, err = c.paymentRequest(ctx, form.Method, requestURL, values, urlOrigin(form.Action), page.URL, false, true)
		if err != nil {
			return err
		}
	}
	if looksLike3DS(page.Body) {
		return ErrThreeDSRequired
	}
	return nil
}

type htmlForm struct {
	Action string
	Method string
	Values url.Values
}

func firstHTMLForm(body []byte, baseURL string) (htmlForm, bool, error) {
	document, err := xhtml.Parse(strings.NewReader(string(body)))
	if err != nil {
		return htmlForm{}, false, errors.New("kitesim: invalid payment form HTML")
	}
	var formNode *xhtml.Node
	walkHTML(document, func(node *xhtml.Node) bool {
		if node.Type == xhtml.ElementNode && node.Data == "form" {
			formNode = node
			return false
		}
		return true
	})
	if formNode == nil {
		return htmlForm{}, false, nil
	}
	base, err := url.Parse(baseURL)
	if err != nil {
		return htmlForm{}, false, errors.New("kitesim: invalid payment form base URL")
	}
	action, err := base.Parse(stdhtml.UnescapeString(htmlAttr(formNode, "action")))
	if err != nil {
		return htmlForm{}, false, errors.New("kitesim: invalid payment form action")
	}
	method := strings.ToUpper(strings.TrimSpace(htmlAttr(formNode, "method")))
	if method != http.MethodPost {
		method = http.MethodGet
	}
	values := url.Values{}
	walkHTML(formNode, func(node *xhtml.Node) bool {
		if node.Type == xhtml.ElementNode && node.Data == "input" {
			if name := htmlAttr(node, "name"); name != "" {
				values.Add(name, htmlAttr(node, "value"))
			}
		}
		return true
	})
	return htmlForm{Action: action.String(), Method: method, Values: values}, true, nil
}

func inputValue(body []byte, name string) string {
	document, err := xhtml.Parse(strings.NewReader(string(body)))
	if err != nil {
		return ""
	}
	value := ""
	walkHTML(document, func(node *xhtml.Node) bool {
		if node.Type == xhtml.ElementNode && node.Data == "input" && (htmlAttr(node, "name") == name || htmlAttr(node, "id") == name) {
			value = htmlAttr(node, "value")
			return false
		}
		return true
	})
	return value
}

func walkHTML(node *xhtml.Node, visit func(*xhtml.Node) bool) bool {
	if node == nil || !visit(node) {
		return false
	}
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if !walkHTML(child, visit) {
			return false
		}
	}
	return true
}

func htmlAttr(node *xhtml.Node, name string) string {
	for _, attr := range node.Attr {
		if strings.EqualFold(attr.Key, name) {
			return stdhtml.UnescapeString(attr.Val)
		}
	}
	return ""
}

func htmlText(node *xhtml.Node) string {
	var builder strings.Builder
	var appendText func(*xhtml.Node)
	appendText = func(current *xhtml.Node) {
		if current.Type == xhtml.TextNode {
			builder.WriteString(current.Data)
		}
		for child := current.FirstChild; child != nil; child = child.NextSibling {
			appendText(child)
		}
	}
	appendText(node)
	return builder.String()
}

func quotedJSValue(script, name string) string {
	index := strings.Index(strings.ToLower(script), strings.ToLower(name))
	if index < 0 {
		return ""
	}
	rest := script[index+len(name):]
	for i, char := range rest {
		if char != '\'' && char != '"' {
			continue
		}
		quote, start := byte(char), i+1
		for end := start; end < len(rest); end++ {
			if rest[end] == quote && (end == start || rest[end-1] != '\\') {
				return normalizeJSURL(rest[start:end])
			}
		}
		return ""
	}
	return ""
}

func normalizeJSURL(value string) string {
	value = stdhtml.UnescapeString(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, `\/`, "/")
	value = strings.ReplaceAll(value, `\u0026`, "&")
	return value
}

func embeddedProviderURL(body []byte, host string) (string, error) {
	text := normalizeJSURL(string(body))
	lower := strings.ToLower(text)
	needle := "https://" + strings.ToLower(strings.TrimSpace(host))
	for offset := 0; offset < len(text); {
		index := strings.Index(lower[offset:], needle)
		if index < 0 {
			break
		}
		start := offset + index
		end := start
		for end < len(text) && !strings.ContainsRune("\"'<> \t\r\n", rune(text[end])) {
			end++
		}
		candidate := strings.TrimRight(text[start:end], "),;")
		if result, err := providerURL(candidate, host); err == nil {
			return result, nil
		}
		offset = start + len(needle)
	}
	return "", errors.New("kitesim: embedded payment provider URL missing")
}

func providerURL(value string, hosts ...string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Scheme != "https" || parsed.User != nil {
		return "", errors.New("kitesim: invalid payment provider URL")
	}
	for _, host := range hosts {
		if strings.EqualFold(parsed.Hostname(), host) {
			return parsed.String(), nil
		}
	}
	return "", errors.New("kitesim: unexpected payment provider URL")
}

func urlOrigin(value string) string {
	parsed, err := url.Parse(value)
	if err != nil {
		return ""
	}
	return parsed.Scheme + "://" + parsed.Host
}

func looksLike3DS(body []byte) bool {
	text := strings.ToLower(string(body))
	if strings.Contains(text, "challenge") && strings.Contains(text, "3ds") {
		return true
	}
	for _, token := range []string{`name="creq"`, "name='creq'", "name=creq", "threedschallenge", "acs-challenge", "challengeurl"} {
		if strings.Contains(text, token) {
			return true
		}
	}
	return false
}

func jwtReferenceID(token string) string {
	return decodeCardinalJWT(token).ReferenceID
}

func decodeCardinalJWT(token string) cardinalJWTClaims {
	parts := strings.Split(strings.TrimSpace(token), ".")
	if len(parts) < 2 {
		return cardinalJWTClaims{}
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return cardinalJWTClaims{}
	}
	var claims cardinalJWTClaims
	if json.Unmarshal(payload, &claims) != nil {
		return cardinalJWTClaims{}
	}
	claims.OrgUnitID = strings.TrimSpace(claims.OrgUnitID)
	claims.ReferenceID = strings.TrimSpace(claims.ReferenceID)
	return claims
}

func formatCardNumber(number string) string {
	var parts []string
	for len(number) > 4 {
		parts = append(parts, number[:4])
		number = number[4:]
	}
	if number != "" {
		parts = append(parts, number)
	}
	return strings.Join(parts, " ")
}

func truthy(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes":
		return true
	default:
		return false
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
