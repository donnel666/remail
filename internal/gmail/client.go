package gmail

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

const smsbowerBaseURL = "https://smsbower.page"

var (
	ErrBadKey              = errors.New("smsbower: bad api key")
	ErrNoMail              = errors.New("smsbower: no mail available")
	ErrInsufficientBalance = errors.New("smsbower: insufficient balance")
	ErrPriceChanged        = errors.New("smsbower: price exceeds maximum")
	ErrCodeWaiting         = errors.New("smsbower: code not received yet")
	ErrActivationMissing   = errors.New("smsbower: activation no longer exists")
	ErrActivationStatus    = errors.New("smsbower: activation status already changed")
	ErrRemote              = errors.New("smsbower: remote request failed")
)

type UncertainActivationError struct{ Err error }

func (e *UncertainActivationError) Error() string { return "smsbower: activation result is uncertain" }
func (e *UncertainActivationError) Unwrap() error { return e.Err }

type SMSBowerService struct {
	Code string
	Name string
}

type SMSBowerPriceRest struct {
	Price decimal.Decimal
	Count int
}

type SMSBowerActivation struct {
	Email  string
	MailID uint64
}

type SMSBowerClient struct {
	baseURL string
	http    *http.Client
}

func NewSMSBowerClient() *SMSBowerClient {
	return &SMSBowerClient{
		baseURL: smsbowerBaseURL,
		http:    &http.Client{Timeout: 15 * time.Second},
	}
}

func newSMSBowerClient(baseURL string, client *http.Client) *SMSBowerClient {
	return &SMSBowerClient{baseURL: strings.TrimRight(baseURL, "/"), http: client}
}

func (c *SMSBowerClient) Balance(ctx context.Context, apiKey string) (decimal.Decimal, error) {
	body, err := c.get(ctx, "/stubs/handler_api.php", url.Values{"action": {"getBalance"}, "api_key": {apiKey}}, false)
	if err != nil {
		return decimal.Zero, err
	}
	value := strings.TrimSpace(string(body))
	if strings.EqualFold(value, "BAD_KEY") {
		return decimal.Zero, ErrBadKey
	}
	prefix, balance, ok := strings.Cut(value, ":")
	if !ok || !strings.EqualFold(strings.TrimSpace(prefix), "ACCESS_BALANCE") {
		return decimal.Zero, classifyRemoteError(value)
	}
	parsed, err := decimal.NewFromString(strings.TrimSpace(balance))
	if err != nil || parsed.IsNegative() {
		return decimal.Zero, ErrRemote
	}
	return parsed, nil
}

func (c *SMSBowerClient) Services(ctx context.Context, apiKey string) ([]SMSBowerService, error) {
	body, err := c.get(ctx, "/stubs/handler_api.php", url.Values{"action": {"getMailServicesList"}, "api_key": {apiKey}}, false)
	if err != nil {
		return nil, err
	}
	if classified := classifyKnownRemoteError(string(body)); classified != nil {
		return nil, classified
	}
	var response struct {
		Status   json.RawMessage `json:"status"`
		Services []struct {
			Code string `json:"code"`
			Name string `json:"name"`
		} `json:"services"`
	}
	if err := json.Unmarshal(body, &response); err != nil || !remoteStatusOK(response.Status) {
		return nil, ErrRemote
	}
	services := make([]SMSBowerService, 0, len(response.Services))
	for _, item := range response.Services {
		code := strings.TrimSpace(item.Code)
		name := strings.TrimSpace(item.Name)
		if code == "" || len(code) > 64 || name == "" {
			continue
		}
		services = append(services, SMSBowerService{Code: code, Name: name})
	}
	return services, nil
}

func (c *SMSBowerClient) GmailPrices(ctx context.Context, apiKey string) (map[string]SMSBowerPriceRest, error) {
	body, err := c.get(ctx, "/api/mail/getPriceRests", url.Values{"api_key": {apiKey}, "domain": {"gmail.com"}}, false)
	if err != nil {
		return nil, err
	}
	if classified := classifyKnownRemoteError(string(body)); classified != nil {
		return nil, classified
	}
	var response struct {
		Status json.RawMessage `json:"status"`
		Data   map[string]map[string]struct {
			Price json.Number `json:"price"`
			Count int         `json:"count"`
		} `json:"data"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.UseNumber()
	if err := decoder.Decode(&response); err != nil || !remoteStatusOK(response.Status) {
		return nil, ErrRemote
	}
	result := make(map[string]SMSBowerPriceRest, len(response.Data))
	for serviceCode, domains := range response.Data {
		item, ok := domains["gmail.com"]
		if !ok {
			continue
		}
		price, err := decimal.NewFromString(string(item.Price))
		if err != nil || price.IsNegative() || item.Count < 0 {
			continue
		}
		result[strings.TrimSpace(serviceCode)] = SMSBowerPriceRest{Price: price, Count: item.Count}
	}
	return result, nil
}

func (c *SMSBowerClient) Activate(ctx context.Context, apiKey, service string, maxPrice decimal.Decimal) (*SMSBowerActivation, error) {
	body, err := c.get(ctx, "/api/mail/getActivation", url.Values{
		"api_key": {apiKey}, "service": {strings.TrimSpace(service)}, "domain": {"gmail.com"},
		"maxPrice": {maxPrice.String()}, "alias": {"0"},
	}, true)
	if err != nil {
		return nil, err
	}
	if classified := classifyKnownRemoteError(string(body)); classified != nil {
		return nil, classified
	}
	var response struct {
		Status json.RawMessage `json:"status"`
		Mail   string          `json:"mail"`
		MailID json.Number     `json:"mailId"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.UseNumber()
	if err := decoder.Decode(&response); err != nil || !remoteStatusOK(response.Status) {
		return nil, ErrRemote
	}
	mailID, err := strconv.ParseUint(string(response.MailID), 10, 64)
	email := strings.ToLower(strings.TrimSpace(response.Mail))
	if err != nil || mailID == 0 || !strings.HasSuffix(email, "@gmail.com") {
		return nil, ErrRemote
	}
	return &SMSBowerActivation{Email: email, MailID: mailID}, nil
}

func (c *SMSBowerClient) Code(ctx context.Context, apiKey string, mailID uint64) (string, error) {
	body, err := c.get(ctx, "/api/mail/getCode", url.Values{"api_key": {apiKey}, "mailId": {strconv.FormatUint(mailID, 10)}}, false)
	if err != nil {
		return "", err
	}
	if classified := classifyKnownRemoteError(string(body)); classified != nil {
		return "", classified
	}
	var response struct {
		Status json.RawMessage `json:"status"`
		Code   string          `json:"code"`
	}
	if err := json.Unmarshal(body, &response); err != nil || !remoteStatusOK(response.Status) {
		return "", ErrRemote
	}
	code := strings.TrimSpace(response.Code)
	if code == "" || len(code) > 128 || strings.ContainsAny(code, "\r\n\x00") {
		return "", ErrRemote
	}
	return code, nil
}

func (c *SMSBowerClient) SetStatus(ctx context.Context, apiKey string, mailID uint64, status int) error {
	if status != 2 && status != 3 && status != 5 {
		return fmt.Errorf("invalid SMSBower status: %d", status)
	}
	body, err := c.get(ctx, "/api/mail/setStatus", url.Values{
		"api_key": {apiKey}, "id": {strconv.FormatUint(mailID, 10)}, "status": {strconv.Itoa(status)},
	}, false)
	if err != nil {
		return err
	}
	if classified := classifyKnownRemoteError(string(body)); classified != nil {
		return classified
	}
	var response struct {
		Status json.RawMessage `json:"status"`
	}
	if json.Unmarshal(body, &response) == nil && remoteStatusOK(response.Status) {
		return nil
	}
	value := strings.ToLower(strings.TrimSpace(string(body)))
	if strings.Contains(value, "success") || strings.HasPrefix(value, "access_") {
		return nil
	}
	return ErrRemote
}

func (c *SMSBowerClient) get(ctx context.Context, path string, query url.Values, uncertain bool) ([]byte, error) {
	endpoint, err := url.Parse(c.baseURL + path)
	if err != nil {
		return nil, ErrRemote
	}
	endpoint.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, ErrRemote
	}
	response, err := c.http.Do(request)
	if err != nil {
		if uncertain {
			return nil, &UncertainActivationError{Err: err}
		}
		return nil, ErrRemote
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		if uncertain {
			return nil, &UncertainActivationError{Err: err}
		}
		return nil, ErrRemote
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		if uncertain {
			return nil, &UncertainActivationError{Err: ErrRemote}
		}
		return nil, classifyRemoteError(string(body))
	}
	return body, nil
}

func remoteStatusOK(raw json.RawMessage) bool {
	value := strings.Trim(strings.ToLower(strings.TrimSpace(string(raw))), `"`)
	return value == "1" || value == "success" || value == "true"
}

func classifyKnownRemoteError(value string) error {
	lower := strings.ToLower(strings.TrimSpace(value))
	switch {
	case strings.Contains(lower, "bad_key") || strings.Contains(lower, "bad key") || strings.Contains(lower, "no access"):
		return ErrBadKey
	case strings.Contains(lower, "no mails yet") || strings.Contains(lower, "no mail"):
		return ErrNoMail
	case strings.Contains(lower, "insufficient balance"):
		return ErrInsufficientBalance
	case strings.Contains(lower, "has not been received yet") || strings.Contains(lower, "try again later"):
		return ErrCodeWaiting
	case strings.Contains(lower, "no activation found with such id") || strings.Contains(lower, "pass mail id"):
		return ErrActivationMissing
	case strings.Contains(lower, "bad actual activation status") || strings.Contains(lower, "activation is already cancel"):
		return ErrActivationStatus
	case strings.Contains(lower, "maxprice") || strings.Contains(lower, "max price") || strings.Contains(lower, "price exceeds"):
		return ErrPriceChanged
	default:
		return nil
	}
}

func classifyRemoteError(value string) error {
	if err := classifyKnownRemoteError(value); err != nil {
		return err
	}
	return ErrRemote
}
