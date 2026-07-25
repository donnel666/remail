package infra

import (
	"bytes"
	"context"
	"crypto"
	"crypto/md5"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	billingapp "github.com/donnel666/remail/internal/billing/app"
	"github.com/donnel666/remail/internal/billing/domain"
)

const (
	epayMaxResponseBytes = 1 << 20
	epayV2SubmitPath     = "api/pay/submit"
	epayV2QueryPath      = "api/pay/query"
)

type EPay struct {
	client *http.Client
}

func NewEPay() *EPay {
	return &EPay{client: &http.Client{CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}}}
}

func (gateway *EPay) PaymentURL(config billingapp.RechargeConfig, recharge domain.Recharge) (string, error) {
	switch strings.ToLower(strings.TrimSpace(config.Version)) {
	case "v1":
		return gateway.paymentURLV1(config, recharge)
	case "v2":
		return gateway.paymentURLV2(config, recharge)
	default:
		return "", domain.ErrRechargeConfigUnavailable
	}
}

func (gateway *EPay) paymentURLV1(config billingapp.RechargeConfig, recharge domain.Recharge) (string, error) {
	endpoint, err := epayEndpoint(config.GatewayURL, "submit.php")
	if err != nil {
		return "", domain.ErrRechargeConfigUnavailable
	}
	values := map[string]string{
		"pid":          config.MerchantID,
		"type":         "alipay",
		"out_trade_no": recharge.RechargeNo,
		"notify_url":   config.NotifyURL,
		"return_url":   config.ReturnURL,
		"name":         "Account recharge",
		"money":        recharge.PaymentAmount,
	}
	query := url.Values{}
	for key, value := range values {
		query.Set(key, value)
	}
	query.Set("sign", epaySign(values, config.MerchantKey))
	query.Set("sign_type", "MD5")
	endpoint.RawQuery = query.Encode()
	return endpoint.String(), nil
}

func (gateway *EPay) paymentURLV2(config billingapp.RechargeConfig, recharge domain.Recharge) (string, error) {
	endpoint, err := epayEndpoint(config.GatewayURL, epayV2SubmitPath)
	if err != nil {
		return "", domain.ErrRechargeConfigUnavailable
	}
	values := map[string]string{
		"pid":          config.MerchantID,
		"type":         "alipay",
		"out_trade_no": recharge.RechargeNo,
		"notify_url":   config.NotifyURL,
		"return_url":   config.ReturnURL,
		"name":         "Account recharge",
		"money":        recharge.PaymentAmount,
		"timestamp":    strconv.FormatInt(time.Now().Unix(), 10),
	}
	signature, err := epaySignRSA(values, config.PrivateKey)
	if err != nil {
		return "", domain.ErrRechargeConfigUnavailable
	}
	if _, err := parseEPayPublicKey(config.PlatformPublicKey); err != nil {
		return "", domain.ErrRechargeConfigUnavailable
	}
	query := url.Values{}
	for key, value := range values {
		query.Set(key, value)
	}
	query.Set("sign", signature)
	query.Set("sign_type", "RSA")
	endpoint.RawQuery = query.Encode()
	return endpoint.String(), nil
}

func (gateway *EPay) Query(ctx context.Context, config billingapp.RechargeConfig, recharge domain.Recharge) (billingapp.RechargeGatewayQuery, error) {
	switch strings.ToLower(strings.TrimSpace(config.Version)) {
	case "v1":
		return gateway.queryV1(ctx, config, recharge)
	case "v2":
		return gateway.queryV2(ctx, config, recharge)
	default:
		return billingapp.RechargeGatewayQuery{}, domain.ErrRechargeConfigUnavailable
	}
}

func (gateway *EPay) queryV1(ctx context.Context, config billingapp.RechargeConfig, recharge domain.Recharge) (billingapp.RechargeGatewayQuery, error) {
	if gateway == nil || gateway.client == nil {
		return billingapp.RechargeGatewayQuery{}, domain.ErrRechargeConfigUnavailable
	}
	endpoint, err := epayEndpoint(config.GatewayURL, "api.php")
	if err != nil {
		return billingapp.RechargeGatewayQuery{}, domain.ErrRechargeConfigUnavailable
	}
	body, err := gateway.request(ctx, http.MethodPost, endpoint, map[string]string{
		"act": "order", "pid": config.MerchantID, "key": config.MerchantKey, "out_trade_no": recharge.RechargeNo,
	})
	if err != nil {
		return billingapp.RechargeGatewayQuery{}, err
	}
	result, _, err := decodeEPayResponse(body)
	if err != nil {
		return billingapp.RechargeGatewayQuery{}, fmt.Errorf("decode epay response: %w", err)
	}
	if result.Code == "-1" {
		return billingapp.RechargeGatewayQuery{}, nil
	}
	if result.Code != "1" {
		return billingapp.RechargeGatewayQuery{}, billingapp.ErrRechargeGatewayRejected
	}
	return validateEPayQuery(result, config, recharge)
}

func (gateway *EPay) queryV2(ctx context.Context, config billingapp.RechargeConfig, recharge domain.Recharge) (billingapp.RechargeGatewayQuery, error) {
	if gateway == nil || gateway.client == nil {
		return billingapp.RechargeGatewayQuery{}, domain.ErrRechargeConfigUnavailable
	}
	endpoint, err := epayEndpoint(config.GatewayURL, epayV2QueryPath)
	if err != nil {
		return billingapp.RechargeGatewayQuery{}, domain.ErrRechargeConfigUnavailable
	}
	values := map[string]string{
		"pid": config.MerchantID, "out_trade_no": recharge.RechargeNo,
		"timestamp": strconv.FormatInt(time.Now().Unix(), 10),
	}
	signature, err := epaySignRSA(values, config.PrivateKey)
	if err != nil {
		return billingapp.RechargeGatewayQuery{}, domain.ErrRechargeConfigUnavailable
	}
	values["sign"] = signature
	values["sign_type"] = "RSA"
	body, err := gateway.request(ctx, http.MethodPost, endpoint, values)
	if err != nil {
		return billingapp.RechargeGatewayQuery{}, err
	}
	result, responseValues, err := decodeEPayResponse(body)
	if err != nil {
		return billingapp.RechargeGatewayQuery{}, fmt.Errorf("decode epay response: %w", err)
	}
	if result.Code == "-1" {
		return billingapp.RechargeGatewayQuery{}, nil
	}
	if result.Code != "0" {
		return billingapp.RechargeGatewayQuery{}, billingapp.ErrRechargeGatewayRejected
	}
	if !strings.EqualFold(responseValues["sign_type"], "RSA") || epayVerifyRSA(responseValues, responseValues["sign"], config.PlatformPublicKey) != nil {
		return billingapp.RechargeGatewayQuery{}, domain.ErrRechargeQueryMismatch
	}
	return validateEPayQuery(result, config, recharge)
}

func validateEPayQuery(result epayResponse, config billingapp.RechargeConfig, recharge domain.Recharge) (billingapp.RechargeGatewayQuery, error) {
	status, err := strconv.Atoi(result.Status)
	if err != nil {
		return billingapp.RechargeGatewayQuery{}, domain.ErrRechargeQueryMismatch
	}
	if status != 1 {
		return billingapp.RechargeGatewayQuery{Terminal: status > 1}, nil
	}
	if result.PID != strings.TrimSpace(config.MerchantID) ||
		result.OutTradeNo != recharge.RechargeNo ||
		!strings.EqualFold(result.Type, "alipay") ||
		strings.TrimSpace(result.TradeNo) == "" ||
		!sameMoney(result.Money, recharge.PaymentAmount) {
		return billingapp.RechargeGatewayQuery{}, domain.ErrRechargeQueryMismatch
	}
	return billingapp.RechargeGatewayQuery{
		Paid:         true,
		GatewayTrade: strings.TrimSpace(result.TradeNo),
		PaidAt:       parseEPayTime(result.EndTime),
	}, nil
}

func (gateway *EPay) request(ctx context.Context, method string, endpoint *url.URL, values map[string]string) ([]byte, error) {
	if gateway == nil || gateway.client == nil {
		return nil, domain.ErrRechargeConfigUnavailable
	}
	form := url.Values{}
	for key, value := range values {
		if value != "" {
			form.Set(key, value)
		}
	}
	var body io.Reader
	if method == http.MethodGet {
		endpoint.RawQuery = form.Encode()
	} else {
		body = strings.NewReader(form.Encode())
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint.String(), body)
	if err != nil {
		return nil, fmt.Errorf("create epay query: %w", err)
	}
	if method == http.MethodPost {
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	response, err := gateway.client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("query epay: %w", ctx.Err())
		}
		return nil, fmt.Errorf("query epay: request failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("query epay: unexpected HTTP status %d", response.StatusCode)
	}
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, epayMaxResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read epay response: %w", err)
	}
	if len(responseBody) > epayMaxResponseBytes {
		return nil, fmt.Errorf("read epay response: response too large")
	}
	return responseBody, nil
}

type epayResponse struct {
	Code       string `json:"code"`
	Status     string `json:"status"`
	PID        string `json:"pid"`
	TradeNo    string `json:"trade_no"`
	OutTradeNo string `json:"out_trade_no"`
	Type       string `json:"type"`
	Money      string `json:"money"`
	EndTime    string `json:"endtime"`
}

func decodeEPayResponse(body []byte) (epayResponse, map[string]string, error) {
	body = bytes.TrimSpace(body)
	if len(body) > 0 && body[0] == '"' {
		var encoded string
		if err := json.Unmarshal(body, &encoded); err != nil {
			return epayResponse{}, nil, err
		}
		body = []byte(encoded)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return epayResponse{}, nil, err
	}
	values := make(map[string]string, len(raw))
	for key, value := range raw {
		values[key] = jsonScalar(value)
	}
	return epayResponse{
		Code: values["code"], Status: values["status"], PID: values["pid"], TradeNo: values["trade_no"],
		OutTradeNo: values["out_trade_no"], Type: values["type"], Money: values["money"], EndTime: values["endtime"],
	}, values, nil
}

func jsonScalar(raw json.RawMessage) string {
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return ""
	}
	var value string
	if json.Unmarshal(raw, &value) == nil {
		return strings.TrimSpace(value)
	}
	return strings.TrimSpace(string(raw))
}

func epayEndpoint(raw, file string) (*url.URL, error) {
	endpoint, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || endpoint.Scheme != "https" || endpoint.Host == "" {
		return nil, domain.ErrRechargeConfigUnavailable
	}
	endpoint.Path = strings.TrimRight(endpoint.Path, "/") + "/" + strings.TrimLeft(file, "/")
	endpoint.RawQuery = ""
	endpoint.Fragment = ""
	return endpoint, nil
}

func epaySign(values map[string]string, merchantKey string) string {
	sum := md5.Sum([]byte(epaySignContent(values) + merchantKey))
	return hex.EncodeToString(sum[:])
}

func epaySignContent(values map[string]string) string {
	keys := make([]string, 0, len(values))
	for key, value := range values {
		if value != "" && key != "sign" && key != "sign_type" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+"="+values[key])
	}
	return strings.Join(parts, "&")
}

func epaySignRSA(values map[string]string, privateKey string) (string, error) {
	key, err := parseEPayPrivateKey(privateKey)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256([]byte(epaySignContent(values)))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, hash[:])
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(signature), nil
}

func epayVerifyRSA(values map[string]string, signature, publicKey string) error {
	key, err := parseEPayPublicKey(publicKey)
	if err != nil {
		return err
	}
	rawSignature, err := base64.StdEncoding.DecodeString(strings.TrimSpace(signature))
	if err != nil {
		return err
	}
	hash := sha256.Sum256([]byte(epaySignContent(values)))
	return rsa.VerifyPKCS1v15(key, crypto.SHA256, hash[:], rawSignature)
}

func parseEPayPrivateKey(raw string) (*rsa.PrivateKey, error) {
	der, err := epayKeyDER(raw)
	if err != nil {
		return nil, err
	}
	if key, err := x509.ParsePKCS8PrivateKey(der); err == nil {
		if rsaKey, ok := key.(*rsa.PrivateKey); ok {
			if err := requireEPayRSAKeySize(rsaKey.N.BitLen()); err != nil {
				return nil, err
			}
			return rsaKey, nil
		}
	}
	key, err := x509.ParsePKCS1PrivateKey(der)
	if err != nil {
		return nil, err
	}
	if err := requireEPayRSAKeySize(key.N.BitLen()); err != nil {
		return nil, err
	}
	return key, nil
}

func parseEPayPublicKey(raw string) (*rsa.PublicKey, error) {
	der, err := epayKeyDER(raw)
	if err != nil {
		return nil, err
	}
	if key, err := x509.ParsePKIXPublicKey(der); err == nil {
		if rsaKey, ok := key.(*rsa.PublicKey); ok {
			if err := requireEPayRSAKeySize(rsaKey.N.BitLen()); err != nil {
				return nil, err
			}
			return rsaKey, nil
		}
	}
	key, err := x509.ParsePKCS1PublicKey(der)
	if err != nil {
		return nil, err
	}
	if err := requireEPayRSAKeySize(key.N.BitLen()); err != nil {
		return nil, err
	}
	return key, nil
}

func requireEPayRSAKeySize(bits int) error {
	if bits < 2048 {
		return fmt.Errorf("epay RSA key must be at least 2048 bits")
	}
	return nil
}

func epayKeyDER(raw string) ([]byte, error) {
	normalized := strings.ReplaceAll(strings.TrimSpace(raw), `\n`, "\n")
	if block, _ := pem.Decode([]byte(normalized)); block != nil {
		return block.Bytes, nil
	}
	return base64.StdEncoding.DecodeString(strings.Join(strings.Fields(normalized), ""))
}

func sameMoney(left, right string) bool {
	a, err := domain.ParseMoney(left)
	if err != nil {
		return false
	}
	b, err := domain.ParseMoney(right)
	return err == nil && a.Equal(b)
}

func parseEPayTime(value string) *time.Time {
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		location = time.FixedZone("Asia/Shanghai", 8*60*60)
	}
	parsed, err := time.ParseInLocation("2006-01-02 15:04:05", strings.TrimSpace(value), location)
	if err != nil {
		return nil
	}
	utc := parsed.UTC()
	return &utc
}
