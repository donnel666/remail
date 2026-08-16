package kitesim

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	httpfingerprint "github.com/bogdanfinn/fhttp"
)

const (
	DefaultBaseURL  = "https://api.kitesim.co"
	maxLoginRetries = 10
)

var ErrLoginFailed = errors.New("kitesim: login failed")

type PhoneStatus int

const (
	PhoneExpired    PhoneStatus = 0
	PhoneActive     PhoneStatus = 1
	PhonePending    PhoneStatus = 2
	PhoneActivating PhoneStatus = 3
	PhoneRefunded   PhoneStatus = 4
)

var phoneStatuses = []PhoneStatus{
	PhoneActive,
	PhonePending,
	PhoneActivating,
	PhoneExpired,
	PhoneRefunded,
}

func (s PhoneStatus) Label() string {
	switch s {
	case PhoneActive:
		return "使用中"
	case PhonePending:
		return "待支付"
	case PhoneActivating:
		return "激活中"
	case PhoneExpired:
		return "已过期"
	case PhoneRefunded:
		return "已退款"
	default:
		return "未知"
	}
}

type PhoneOrder struct {
	ID                stringValue     `json:"id"`
	OrderNo           string          `json:"orderNo"`
	PhoneCode         stringValue     `json:"phoneCode"`
	PhoneNumber       string          `json:"phoneNumber"`
	CountryCode       stringValue     `json:"countryCode"`
	OrderStatus       int             `json:"orderStatus"`
	PackageID         stringValue     `json:"packageId"`
	DurationType      int             `json:"durationType"`
	DurationValue     int             `json:"durationValue"`
	AutoRenew         boolValue       `json:"autoRenew"`
	Currency          string          `json:"currency"`
	OriginalAmount    stringValue     `json:"originalAmount"`
	PaidAmount        stringValue     `json:"paidAmount"`
	AutoRenewPrice    stringValue     `json:"autoRenewPrice"`
	CreateTime        string          `json:"createTime"`
	PaymentTime       string          `json:"paymentTime"`
	ExpireTime        string          `json:"expireTime"`
	LatestRenewalTime string          `json:"latestRenewalTime"`
	NextRenewalDate   stringValue     `json:"nextRenewalDate"`
	RefundTime        stringValue     `json:"refundTime"`
	Status            PhoneStatus     `json:"-"`
	RawPayload        json.RawMessage `json:"-"`
}

func (o *PhoneOrder) UnmarshalJSON(data []byte) error {
	type wire PhoneOrder
	var value wire
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	*o = PhoneOrder(value)
	o.RawPayload = append(o.RawPayload[:0], data...)
	return nil
}

func (o PhoneOrder) FullPhoneNumber() string {
	code := strings.TrimPrefix(strings.TrimSpace(string(o.PhoneCode)), "+")
	number := strings.TrimSpace(o.PhoneNumber)
	if code == "" {
		return number
	}
	return "+" + code + " " + number
}

type Message struct {
	Caller     string `json:"caller"`
	Content    string `json:"content"`
	SendTime   string `json:"sendTime"`
	CreateTime string `json:"createTime"`
}

func (m Message) Time() string {
	if strings.TrimSpace(m.SendTime) != "" {
		return m.SendTime
	}
	return m.CreateTime
}

type Client struct {
	BaseURL    string
	HTTP       requestDoer
	headers    map[string]string
	userAgent  string
	customHTTP bool
	usesProxy  bool
	initErr    error
}

type requestDoer interface {
	Do(*http.Request) (*http.Response, error)
}

func NewClient(httpClient *http.Client) *Client {
	fingerprint := newBrowserFingerprint()
	client := &Client{
		BaseURL: DefaultBaseURL, headers: fingerprint.Headers,
		userAgent: fingerprint.UserAgent,
	}
	if httpClient != nil {
		client.HTTP = withKitesimRedirectPolicy(httpClient)
		client.customHTTP = true
		return client
	}
	client.HTTP, client.initErr = newFingerprintHTTPDoer("", fingerprint)
	return client
}

func (c *Client) withProxy(proxyURL string) (*Client, error) {
	if c == nil {
		return nil, errors.New("kitesim: client unavailable")
	}
	fingerprint := newBrowserFingerprint()
	client := &Client{
		BaseURL: c.BaseURL, headers: fingerprint.Headers,
		userAgent: fingerprint.UserAgent, usesProxy: strings.TrimSpace(proxyURL) != "",
	}
	if client.BaseURL == "" {
		client.BaseURL = DefaultBaseURL
	}
	if c.customHTTP {
		client.HTTP = c.HTTP
		client.customHTTP = true
		return client, nil
	}
	var err error
	client.HTTP, err = newFingerprintHTTPDoer(proxyURL, fingerprint)
	if err != nil {
		return nil, err
	}
	return client, nil
}

func (c *Client) SolveCaptcha(ctx context.Context) (string, error) {
	challenge, err := c.fetchCaptcha(ctx)
	if err != nil {
		return "", err
	}
	return solveCaptcha(challenge.Image)
}

func (c *Client) Login(ctx context.Context, account, password string) (string, error) {
	account = strings.TrimSpace(account)
	if account == "" || strings.TrimSpace(password) == "" {
		return "", ErrLoginFailed
	}
	var lastMessage string
	for range maxLoginRetries {
		challenge, err := c.fetchCaptcha(ctx)
		if err != nil {
			return "", err
		}
		code, err := solveCaptcha(challenge.Image)
		if err != nil {
			return "", err
		}
		var response apiResponse[string]
		err = c.doJSON(ctx, http.MethodPost, "/index/sign-in", nil, "", map[string]string{
			"email": account, "pass": password,
			"captchaCode": code, "captchaKey": challenge.Key,
		}, &response)
		if err != nil {
			return "", err
		}
		if response.Code == http.StatusOK && strings.TrimSpace(response.Data) != "" {
			return response.Data, nil
		}
		lastMessage = strings.TrimSpace(response.Message)
	}
	if lastMessage == "" {
		return "", ErrLoginFailed
	}
	return "", fmt.Errorf("%w: %s", ErrLoginFailed, lastMessage)
}

func (c *Client) PhoneOrders(ctx context.Context, token string) ([]PhoneOrder, error) {
	orders := make([]PhoneOrder, 0)
	for _, status := range phoneStatuses {
		for page := 1; ; page++ {
			query := url.Values{
				"page": {strconv.Itoa(page)}, "size": {"50"},
				"status": {strconv.Itoa(int(status))}, "phone": {""},
			}
			var response apiResponse[orderPage]
			if err := c.doJSON(ctx, http.MethodGet, "/userPhonePurchase/getOrderPage", query, token, nil, &response); err != nil {
				return nil, err
			}
			if response.Code != http.StatusOK {
				return nil, remoteError(response.Code, response.Message)
			}
			for i := range response.Data.Records {
				response.Data.Records[i].Status = status
			}
			orders = append(orders, response.Data.Records...)
			if page >= max(1, response.Data.TotalPage) {
				break
			}
		}
	}
	return orders, nil
}

func (c *Client) Messages(ctx context.Context, token, orderID, phoneNumber string) ([]Message, error) {
	query := url.Values{
		"orderId":     {strings.TrimSpace(orderID)},
		"phoneNumber": {strings.TrimSpace(phoneNumber)},
	}
	var response apiResponse[messageData]
	if err := c.doJSON(ctx, http.MethodGet, "/userPhonePurchase/seePhoneNubmerSms", query, token, nil, &response); err != nil {
		return nil, err
	}
	if response.Code != http.StatusOK {
		return nil, remoteError(response.Code, response.Message)
	}
	return response.Data.NoteList, nil
}

type captchaChallenge struct {
	Key   string
	Image []byte
}

func (c *Client) fetchCaptcha(ctx context.Context) (*captchaChallenge, error) {
	var response struct {
		Image string `json:"captchaImageBase64"`
		Key   string `json:"captchaKey"`
	}
	if err := c.doJSON(ctx, http.MethodGet, "/index/captcha-image-base64", nil, "", nil, &response); err != nil {
		return nil, err
	}
	image, err := base64.StdEncoding.DecodeString(response.Image)
	if err != nil || strings.TrimSpace(response.Key) == "" {
		return nil, errors.New("kitesim: invalid captcha response")
	}
	return &captchaChallenge{Key: response.Key, Image: image}, nil
}

func (c *Client) doJSON(ctx context.Context, method, requestPath string, query url.Values, token string, body, target any) error {
	if c == nil || c.HTTP == nil {
		return errors.New("kitesim: client unavailable")
	}
	if c.initErr != nil {
		return c.initErr
	}
	baseURL := strings.TrimRight(c.BaseURL, "/")
	requestURL := baseURL + requestPath
	if len(query) > 0 {
		requestURL += "?" + query.Encode()
	}
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(
		context.WithValue(ctx, redirectScopeContextKey{}, redirectScopeAPI),
		method,
		requestURL,
		reader,
	)
	if err != nil {
		return err
	}
	for key, value := range c.headers {
		request.Header.Set(key, value)
	}
	request.Header.Set("User-Agent", c.userAgent)
	request.Header.Set("Content-Type", "application/json")
	if strings.TrimSpace(token) != "" {
		request.Header.Set("token", token)
	}
	if !c.customHTTP {
		request.Header[httpfingerprint.HeaderOrderKey] = append([]string(nil), browserHeaderOrder...)
	}
	response, err := c.HTTP.Do(request)
	if err != nil {
		return &upstreamTransportError{err: err, proxy: c.usesProxy}
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return &upstreamStatusError{status: response.StatusCode, proxy: c.usesProxy}
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 4<<20))
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("kitesim: invalid JSON response: %w", err)
	}
	return nil
}

type apiResponse[T any] struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    T      `json:"data"`
}

type orderPage struct {
	Records   []PhoneOrder `json:"records"`
	TotalPage int          `json:"totalPage"`
}

type messageData struct {
	NoteList []Message `json:"noteList"`
}

type stringValue string

func (s *stringValue) UnmarshalJSON(data []byte) error {
	if bytes.Equal(data, []byte("null")) {
		*s = ""
		return nil
	}
	var value string
	if err := json.Unmarshal(data, &value); err == nil {
		*s = stringValue(value)
		return nil
	}
	var number json.Number
	if err := json.Unmarshal(data, &number); err != nil {
		return err
	}
	*s = stringValue(number.String())
	return nil
}

type boolValue bool

func (b *boolValue) UnmarshalJSON(data []byte) error {
	var value bool
	if err := json.Unmarshal(data, &value); err == nil {
		*b = boolValue(value)
		return nil
	}
	var number int
	if err := json.Unmarshal(data, &number); err != nil || number < 0 || number > 1 {
		return errors.New("kitesim: invalid boolean value")
	}
	*b = boolValue(number == 1)
	return nil
}

type remoteAPIError struct {
	code    int
	message string
}

func (e *remoteAPIError) Error() string {
	return fmt.Sprintf("kitesim: %s (code %d)", e.message, e.code)
}

func remoteError(code int, message string) error {
	message = strings.TrimSpace(message)
	if message == "" {
		message = "remote request failed"
	}
	return &remoteAPIError{code: code, message: message}
}
