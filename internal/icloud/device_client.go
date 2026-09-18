package icloud

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
)

var (
	errDeviceUnavailable       = errors.New("device platform is temporarily unavailable")
	errDeviceUnauthorized      = errors.New("device platform API key was rejected")
	errDeviceResponse          = errors.New("device platform returned an invalid response")
	errDeviceNoCode            = errors.New("device verification code is not available yet")
	errDeviceImportRejected    = errors.New("device platform rejected account import; check its account diagnostics")
	errDeviceRechargeRejected  = errors.New("device platform rejected the recharge card")
	errDeviceRechargeUncertain = errors.New("device recharge result is uncertain; refresh the balance before retrying")
)

type deviceClient struct{ http *http.Client }

// DeviceBalance reports the vendor's balance without converting points to local currency.
type DeviceBalance struct {
	Configured bool       `json:"configured"`
	Balance    *string    `json:"balance"`
	CheckedAt  *time.Time `json:"checkedAt"`
}

func parseDeviceBalance(body []byte) (string, error) {
	var result struct {
		Code *int `json:"code"`
		Data struct {
			Balance *json.Number `json:"balance"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &result) != nil || result.Code == nil {
		return "", errDeviceResponse
	}
	if *result.Code != 0 {
		return "", errDeviceRechargeRejected
	}
	if result.Data.Balance == nil {
		return "", errDeviceResponse
	}
	value := result.Data.Balance.String()
	number, err := strconv.ParseFloat(value, 64)
	if err != nil || math.IsNaN(number) || math.IsInf(number, 0) {
		return "", errDeviceResponse
	}
	return value, nil
}

func (s *Service) deviceBalance(ctx context.Context) (*DeviceBalance, error) {
	if !DeviceCodeConfigured() {
		return &DeviceBalance{}, nil
	}
	body, err := s.device.request(ctx, http.MethodGet, deviceBaseURL()+"/api/auth/me", nil)
	if err != nil {
		return nil, err
	}
	balance, err := parseDeviceBalance(body)
	if err != nil {
		return nil, errDeviceResponse
	}
	now := s.now().UTC()
	return &DeviceBalance{Configured: true, Balance: &balance, CheckedAt: &now}, nil
}

func (s *Service) rechargeDeviceBalance(ctx context.Context, card string) (*DeviceBalance, error) {
	body, err := s.device.request(ctx, http.MethodPost, deviceBaseURL()+"/api/balance/recharge/card", map[string]string{"card_key": card})
	if errors.Is(err, errDeviceUnauthorized) {
		return nil, err
	}
	if err != nil {
		return nil, errDeviceRechargeUncertain
	}
	balance, err := parseDeviceBalance(body)
	if errors.Is(err, errDeviceRechargeRejected) {
		return nil, err
	}
	if err != nil {
		return nil, errDeviceRechargeUncertain
	}
	now := s.now().UTC()
	return &DeviceBalance{Configured: true, Balance: &balance, CheckedAt: &now}, nil
}

func newDeviceClient() *deviceClient {
	return &deviceClient{http: &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

// DeviceCodeConfigured enables device enrollment for newly initialized accounts.
// Accounts already enrolled continue using their saved device API without SMS fallback.
func DeviceCodeConfigured() bool {
	return strings.TrimSpace(runtimeconfig.String(runtimeconfig.ICloudDeviceAPIKey, "")) != ""
}

func deviceBaseURL() string {
	return strings.TrimRight(runtimeconfig.String(runtimeconfig.ICloudDeviceBaseURLKey, "https://devices.orangeid.top:56133"), "/")
}

func validDeviceCodeURL(raw string) bool {
	base, err := url.Parse(deviceBaseURL())
	if err != nil {
		return false
	}
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Host == base.Host && u.User == nil && u.Fragment == ""
}

func (c *deviceClient) request(ctx context.Context, method, path string, payload any) ([]byte, error) {
	if !validDeviceCodeURL(path) {
		return nil, errDeviceResponse
	}
	var body []byte
	var err error
	if payload != nil {
		body, err = json.Marshal(payload)
		if err != nil {
			return nil, err
		}
	}
	r, err := http.NewRequestWithContext(ctx, method, path, bytes.NewReader(body))
	if err != nil {
		return nil, errDeviceResponse
	}
	if key := runtimeconfig.String(runtimeconfig.ICloudDeviceAPIKey, ""); key != "" {
		r.Header.Set("Authorization", "Bearer "+key)
	}
	r.Header.Set("Accept", "application/json, text/plain")
	if payload != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	response, err := c.http.Do(r)
	if err != nil {
		return nil, errDeviceUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode == 401 || response.StatusCode == 403 {
		return nil, errDeviceUnauthorized
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("%w (HTTP %d)", errDeviceUnavailable, response.StatusCode)
	}
	body, err = io.ReadAll(io.LimitReader(response.Body, (2<<20)+1))
	if err != nil || len(body) > 2<<20 {
		return nil, errDeviceResponse
	}
	return body, nil
}

func (c *deviceClient) importAccount(ctx context.Context, binding deviceBindingModel, callback string) error {
	if !DeviceCodeConfigured() {
		return errDeviceUnauthorized
	}
	body, err := c.request(ctx, http.MethodPost, deviceBaseURL()+"/outapi/Account/importData", map[string]any{
		"uid": 919, "account": binding.Email, "password": binding.Password, "phone": binding.Phone,
		"captcha_api": callback, "tag": deviceBindingTag(binding),
	})
	if err != nil {
		return err
	}
	var result struct {
		Code *int `json:"code"`
	}
	if json.Unmarshal(body, &result) != nil || result.Code == nil {
		return errDeviceResponse
	}
	if *result.Code != 0 {
		return errDeviceImportRejected
	}
	return nil
}

type deviceRemoteAccount struct {
	ID        json.Number     `json:"id"`
	Account   string          `json:"account"`
	Tag       string          `json:"tag"`
	Status    string          `json:"result_status"`
	BindUUID  json.RawMessage `json:"bind_uuid"`
	BindADI   json.RawMessage `json:"bind_adi"`
	DeviceAPI string          `json:"device_api"`
}

func (a deviceRemoteAccount) codeURL() string {
	if a.DeviceAPI != "" {
		return a.DeviceAPI
	}
	if _, err := strconv.ParseUint(string(a.ID), 10, 64); err != nil {
		return ""
	}
	for _, value := range []json.RawMessage{a.BindUUID, a.BindADI} {
		if len(value) == 0 || string(value) == "null" || string(value) == `""` || string(value) == "false" {
			return ""
		}
	}
	return deviceBaseURL() + "/api/free/v4/getcode?id=" + url.QueryEscape(string(a.ID))
}

func (c *deviceClient) accounts(ctx context.Context) ([]deviceRemoteAccount, error) {
	if !DeviceCodeConfigured() {
		return nil, errDeviceUnauthorized
	}
	var accounts []deviceRemoteAccount
	cursor := ""
	seen := map[string]bool{}
	// TODO(device-platform): confirm nonempty live records and a multi-ID query
	// contract. Field names/status/cursor come from the public frontend assets.
	// ponytail: list pagination is capped at 100 pages; use bulk ID lookup when documented.
	for page := 0; page < 100; page++ {
		query := url.Values{"limit": {"20"}}
		if cursor != "" {
			query.Set("cursor", cursor)
		}
		body, err := c.request(ctx, http.MethodGet, deviceBaseURL()+"/api/accounts?"+query.Encode(), nil)
		if err != nil {
			return nil, err
		}
		var result struct {
			Code *int `json:"code"`
			Data struct {
				List       []deviceRemoteAccount `json:"list"`
				HasMore    bool                  `json:"has_more"`
				NextCursor string                `json:"next_cursor"`
			} `json:"data"`
		}
		if json.Unmarshal(body, &result) != nil || result.Code == nil || *result.Code != 0 || result.Data.List == nil {
			return nil, errDeviceResponse
		}
		accounts = append(accounts, result.Data.List...)
		if !result.Data.HasMore {
			return accounts, nil
		}
		cursor = result.Data.NextCursor
		if cursor == "" || seen[cursor] {
			return nil, errDeviceResponse
		}
		seen[cursor] = true
	}
	return nil, errDeviceResponse
}

// FetchDeviceCode reads one live Apple trusted-device code, preserving leading zeroes.
func (s *Service) FetchDeviceCode(ctx context.Context, codeAPI string) (string, error) {
	body, err := s.device.request(ctx, http.MethodGet, codeAPI, nil)
	if err != nil {
		return "", err
	}
	if match := appleSMSCodePattern.FindSubmatch(body); len(match) == 2 {
		return string(match[1]), nil
	}
	// TODO(device-platform): verify live offline/expired/no-code response formats.
	return "", errDeviceNoCode
}
