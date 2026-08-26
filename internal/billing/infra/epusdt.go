package infra

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	billingapp "github.com/donnel666/remail/internal/billing/app"
	"github.com/donnel666/remail/internal/billing/domain"
	// Register the pure-Go sqlite driver used for EPUSDT's read-only database.
	_ "github.com/glebarez/go-sqlite"
)

const (
	epusdtMaxResponseBytes = 1 << 20
	epusdtCreatePath       = "payments/gmpay/v1/order/create-transaction"
	epusdtQueryPath        = "payments/gmpay/v1/order/query"
	epusdtLegacyCurrency   = "CNY"
	epusdtDirectCurrency   = "USDT"
	epusdtSQLitePathEnv    = "EPUSDT_SQLITE_PATH"
)

// Epusdt is the GMPay client used by the reconciliation worker.  The create
// response is intentionally never used as proof of payment; only Query can
// return Paid=true.
type Epusdt struct {
	client         *http.Client
	configProvider billingapp.RechargeConfigProvider
}

// EpusdtGateway is an alias for callers that use the provider name in their
// dependency wiring.
type EpusdtGateway = Epusdt

func NewEpusdt() *Epusdt {
	return &Epusdt{client: &http.Client{CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}}}
}

// NewEpusdtWithConfigProvider enables a one-time current-credential retry for
// orders whose immutable snapshot uses a rotated EPUSDT secret. The provider
// is optional so tests and isolated callers retain the original behavior.
func NewEpusdtWithConfigProvider(provider billingapp.RechargeConfigProvider) *Epusdt {
	gateway := NewEpusdt()
	gateway.configProvider = provider
	return gateway
}

// NewEpusdtGateway is kept as an explicit constructor for callers that prefer
// naming gateways by their role. A provider is optional for backwards
// compatibility; production wiring passes one to enable secret-rotation retry.
func NewEpusdtGateway(providers ...billingapp.RechargeConfigProvider) *Epusdt {
	if len(providers) > 0 {
		return NewEpusdtWithConfigProvider(providers[0])
	}
	return NewEpusdt()
}

func (gateway *Epusdt) PaymentURL(ctx context.Context, config billingapp.RechargeConfig, recharge domain.Recharge, _ string) (string, error) {
	base, err := epusdtBaseURL(config, epusdtCreatePath)
	if err != nil {
		return "", err
	}
	amount, err := epusdtPaymentAmount(recharge.PaymentAmount)
	if err != nil {
		return "", err
	}
	values, err := epusdtCreateValues(config, recharge, amount)
	if err != nil {
		return "", err
	}
	if path := strings.TrimSpace(os.Getenv(epusdtSQLitePathEnv)); path != "" {
		if _, err := queryEpusdtSQLiteOrder(ctx, path, config, recharge); err != nil {
			return "", err
		}
	}

	body, status, requestErr := gateway.request(ctx, http.MethodPost, base, values)
	if requestErr != nil {
		if status == 0 || status >= http.StatusInternalServerError || status == http.StatusConflict || epusdtOrderExists(body) || epusdtCreateAlreadyExists(body) {
			return gateway.recoverPaymentURL(ctx, config, recharge, requestErr, status)
		}
		return "", requestErr
	}
	if statusCode, ok := epusdtStatusCode(body); !ok || statusCode != http.StatusOK {
		if epusdtOrderExists(body) || epusdtCreateAlreadyExists(body) {
			return gateway.recoverPaymentURL(ctx, config, recharge, billingapp.ErrRechargeGatewayRejected, status)
		}
		return "", billingapp.ErrRechargeGatewayRejected
	}
	order, decodeErr := decodeEpusdtOrder(body)
	if decodeErr != nil {
		return "", fmt.Errorf("decode epusdt create response: %w", decodeErr)
	}
	// GMPay's create response is intentionally a smaller, unsigned view than
	// the signed query response: it does not include pid, network, or
	// signature. Keep this validation limited to fields the create contract
	// actually returns; only Query is allowed to establish payment state.
	if err := validateEpusdtCreateOrder(order, config, recharge); err != nil {
		return "", err
	}
	if strings.TrimSpace(order.PaymentURL) == "" {
		return gateway.recoverPaymentURL(ctx, config, recharge, billingapp.ErrRechargeGatewayRejected, status)
	}
	return epusdtPaymentURL(config, order.PaymentURL)
}

func (gateway *Epusdt) Query(ctx context.Context, config billingapp.RechargeConfig, recharge domain.Recharge) (billingapp.RechargeGatewayQuery, error) {
	order, err := gateway.queryOrder(ctx, config, recharge)
	if err != nil {
		return billingapp.RechargeGatewayQuery{}, err
	}
	if order.Status == 0 {
		return billingapp.RechargeGatewayQuery{}, nil
	}
	switch order.Status {
	case 1, 4:
		return billingapp.RechargeGatewayQuery{}, nil
	case 2:
		if strings.TrimSpace(order.TradeID) == "" {
			return billingapp.RechargeGatewayQuery{}, domain.ErrRechargeQueryMismatch
		}
		return billingapp.RechargeGatewayQuery{Paid: true, GatewayTrade: strings.TrimSpace(order.TradeID)}, nil
	case 3:
		return billingapp.RechargeGatewayQuery{Terminal: true}, nil
	default:
		return billingapp.RechargeGatewayQuery{}, domain.ErrRechargeQueryMismatch
	}
}

func (gateway *Epusdt) queryOrder(ctx context.Context, config billingapp.RechargeConfig, recharge domain.Recharge) (epusdtOrder, error) {
	if path := strings.TrimSpace(os.Getenv(epusdtSQLitePathEnv)); path != "" {
		return queryEpusdtSQLiteOrder(ctx, path, config, recharge)
	}

	order, err := gateway.queryOrderWithConfig(ctx, config, recharge)
	if !errors.Is(err, domain.ErrRechargeGatewayAuthUnavailable) || gateway == nil || gateway.configProvider == nil {
		return order, err
	}

	current, currentErr := gateway.configProvider.Current()
	if currentErr != nil || !epusdtCredentialRotationCompatible(config, current) ||
		strings.TrimSpace(current.EpusdtAPISecret) == "" ||
		current.EpusdtAPISecret == config.EpusdtAPISecret {
		return order, err
	}
	// The retry still runs through the normal signed query and field checks. A
	// changed merchant scope is deliberately rejected instead of probing it
	// with a new credential. Preserve every protocol field from the immutable
	// snapshot and replace only the rotated secret.
	rotated := current
	rotated.EpusdtGatewayURL = config.EpusdtGatewayURL
	rotated.EpusdtPID = config.EpusdtPID
	rotated.EpusdtToken = config.EpusdtToken
	rotated.EpusdtNetwork = config.EpusdtNetwork
	rotated.EpusdtCurrency = config.EpusdtCurrency
	rotated.EpusdtPointsPerUSDT = config.EpusdtPointsPerUSDT
	rotated.EpusdtNotifyURL = config.EpusdtNotifyURL
	rotated.EpusdtReturnURL = config.EpusdtReturnURL
	rotated.EpusdtAllowedHosts = config.EpusdtAllowedHosts
	return gateway.queryOrderWithConfig(ctx, rotated, recharge)
}

func (gateway *Epusdt) queryOrderWithConfig(ctx context.Context, config billingapp.RechargeConfig, recharge domain.Recharge) (epusdtOrder, error) {
	if gateway == nil || gateway.client == nil {
		return epusdtOrder{}, domain.ErrRechargeConfigUnavailable
	}
	endpoint, err := epusdtBaseURL(config, epusdtQueryPath)
	if err != nil {
		return epusdtOrder{}, err
	}
	orderID, err := epusdtProviderOrderID(recharge.RechargeNo)
	if err != nil {
		return epusdtOrder{}, err
	}
	values := map[string]string{
		"pid":      strings.TrimSpace(config.EpusdtPID),
		"order_id": orderID,
	}
	secret := config.EpusdtAPISecret
	if values["pid"] == "" || values["order_id"] == "" || strings.TrimSpace(secret) == "" ||
		!strings.EqualFold(strings.TrimSpace(config.EpusdtToken), "USDT") ||
		!strings.EqualFold(strings.TrimSpace(config.EpusdtNetwork), "tron") {
		return epusdtOrder{}, domain.ErrRechargeConfigUnavailable
	}
	values["signature"] = epusdtSign(values, secret)
	body, status, requestErr := gateway.request(ctx, http.MethodPost, endpoint, values)
	if requestErr != nil {
		if status == http.StatusUnauthorized || status == http.StatusForbidden {
			return epusdtOrder{}, domain.ErrRechargeGatewayAuthUnavailable
		}
		if code, ok := epusdtStatusCode(body); ok {
			switch code {
			case 10008:
				return epusdtOrder{}, nil
			case http.StatusUnauthorized, http.StatusForbidden:
				return epusdtOrder{}, domain.ErrRechargeGatewayAuthUnavailable
			}
		}
		if status == http.StatusNotFound {
			return epusdtOrder{}, nil
		}
		return epusdtOrder{}, requestErr
	}
	statusCode, ok := epusdtStatusCode(body)
	if !ok {
		return epusdtOrder{}, domain.ErrRechargeQueryMismatch
	}
	if statusCode == http.StatusNotFound || statusCode == 10008 {
		return epusdtOrder{}, nil
	}
	if statusCode == http.StatusUnauthorized || statusCode == http.StatusForbidden {
		return epusdtOrder{}, domain.ErrRechargeGatewayAuthUnavailable
	}
	if statusCode != http.StatusOK {
		return epusdtOrder{}, billingapp.ErrRechargeGatewayRejected
	}
	order, err := decodeEpusdtOrder(body)
	if err != nil {
		return epusdtOrder{}, fmt.Errorf("decode epusdt query response: %w", err)
	}
	if err := verifyEpusdtOrderSignature(order, secret); err != nil {
		return epusdtOrder{}, err
	}
	if err := validateEpusdtOrder(order, config, recharge); err != nil {
		return epusdtOrder{}, err
	}
	return order, nil
}

func epusdtCredentialRotationCompatible(snapshot, current billingapp.RechargeConfig) bool {
	snapshotEndpoint, snapshotErr := epusdtBaseURL(snapshot, epusdtQueryPath)
	currentEndpoint, currentErr := epusdtBaseURL(current, epusdtQueryPath)
	if snapshotErr != nil || currentErr != nil ||
		!sameHTTPSOrigin(snapshotEndpoint, currentEndpoint) ||
		snapshotEndpoint.EscapedPath() != currentEndpoint.EscapedPath() {
		return false
	}
	return strings.TrimSpace(snapshot.EpusdtPID) == strings.TrimSpace(current.EpusdtPID) &&
		strings.EqualFold(strings.TrimSpace(snapshot.EpusdtToken), strings.TrimSpace(current.EpusdtToken)) &&
		strings.EqualFold(strings.TrimSpace(snapshot.EpusdtNetwork), strings.TrimSpace(current.EpusdtNetwork))
}

type epusdtOrder struct {
	PID                string
	OrderID            string
	TradeID            string
	Status             int
	Amount             string
	Currency           string
	ActualAmount       string
	Token              string
	Network            string
	ReceiveAddress     string
	BlockTransactionID string
	PaymentURL         string
	Signature          string
	PayProvider        string
	ParentTradeID      string
	PayBySubID         uint64
	values             map[string]string
	outerValues        map[string]string
}

func queryEpusdtSQLiteOrder(ctx context.Context, path string, config billingapp.RechargeConfig, recharge domain.Recharge) (epusdtOrder, error) {
	orderID, err := epusdtProviderOrderID(recharge.RechargeNo)
	if err != nil {
		return epusdtOrder{}, err
	}
	pid := strings.TrimSpace(config.EpusdtPID)
	if pid == "" || !strings.EqualFold(strings.TrimSpace(config.EpusdtToken), "USDT") ||
		!strings.EqualFold(strings.TrimSpace(config.EpusdtNetwork), "tron") {
		return epusdtOrder{}, domain.ErrRechargeConfigUnavailable
	}
	dsn, err := epusdtSQLiteDSN(path)
	if err != nil {
		return epusdtOrder{}, err
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return epusdtOrder{}, fmt.Errorf("open epusdt sqlite: %w", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	var order epusdtOrder
	err = db.QueryRowContext(ctx, `
		SELECT k.pid,
		       o.order_id,
		       COALESCE(o.trade_id, ''),
		       o.status,
		       COALESCE(CAST(o.amount AS TEXT), ''),
		       COALESCE(o.currency, ''),
		       COALESCE(CAST(o.actual_amount AS TEXT), ''),
		       COALESCE(o.token, ''),
		       COALESCE(o.network, ''),
		       COALESCE(o.receive_address, ''),
		       COALESCE(o.block_transaction_id, ''),
		       COALESCE(o.pay_provider, ''),
		       COALESCE(o.parent_trade_id, ''),
		       COALESCE(o.pay_by_sub_id, 0)
		FROM orders AS o
		JOIN api_keys AS k
		  ON k.id = o.api_key_id
		 AND k.pid = ?
		WHERE o.order_id = ?
		  AND o.deleted_at IS NULL
		LIMIT 1`, pid, orderID).Scan(
		&order.PID,
		&order.OrderID,
		&order.TradeID,
		&order.Status,
		&order.Amount,
		&order.Currency,
		&order.ActualAmount,
		&order.Token,
		&order.Network,
		&order.ReceiveAddress,
		&order.BlockTransactionID,
		&order.PayProvider,
		&order.ParentTradeID,
		&order.PayBySubID,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return epusdtOrder{}, nil
	}
	if err != nil {
		return epusdtOrder{}, fmt.Errorf("query epusdt sqlite: %w", err)
	}
	if err := validateEpusdtSQLiteOrder(order, config, recharge); err != nil {
		return epusdtOrder{}, err
	}
	order.PaymentURL = "/pay/checkout-counter/" + strings.TrimSpace(order.TradeID)
	return order, nil
}

func epusdtSQLiteDSN(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", domain.ErrRechargeConfigUnavailable
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", domain.ErrRechargeConfigUnavailable
	}
	dsn := &url.URL{Scheme: "file", Path: absolute}
	query := url.Values{"mode": {"ro"}}
	query.Add("_pragma", "query_only(1)")
	query.Add("_pragma", "busy_timeout(5000)")
	dsn.RawQuery = query.Encode()
	return dsn.String(), nil
}

func epusdtCreateValues(config billingapp.RechargeConfig, recharge domain.Recharge, amount string) (map[string]string, error) {
	token := strings.ToUpper(strings.TrimSpace(config.EpusdtToken))
	network := strings.ToLower(strings.TrimSpace(config.EpusdtNetwork))
	pid := strings.TrimSpace(config.EpusdtPID)
	secret := config.EpusdtAPISecret
	if pid == "" || strings.TrimSpace(secret) == "" || token != "USDT" || network != "tron" {
		return nil, domain.ErrRechargeConfigUnavailable
	}
	localOrderID := strings.TrimSpace(recharge.RechargeNo)
	orderID, err := epusdtProviderOrderID(localOrderID)
	if err != nil {
		return nil, err
	}
	for _, raw := range []string{config.EpusdtNotifyURL, config.EpusdtReturnURL} {
		parsed, err := url.Parse(strings.TrimSpace(raw))
		if err != nil || !strings.EqualFold(parsed.Scheme, "https") || parsed.Host == "" || parsed.User != nil {
			return nil, domain.ErrRechargeConfigUnavailable
		}
	}
	redirectURL, err := epusdtRedirectURL(config.EpusdtReturnURL, localOrderID)
	if err != nil {
		return nil, err
	}
	values := map[string]string{
		"pid":          pid,
		"order_id":     orderID,
		"currency":     epusdtOrderCurrency(config),
		"token":        token,
		"network":      network,
		"amount":       amount,
		"notify_url":   strings.TrimSpace(config.EpusdtNotifyURL),
		"redirect_url": redirectURL,
		"name":         "Account recharge",
	}
	values["signature"] = epusdtSign(values, secret)
	return values, nil
}

// epusdtRedirectURL binds the local order to the provider's browser return.
// The return page is only a UX hint; reconciliation still relies exclusively
// on the signed asynchronous query. Existing query parameters and fragments
// are retained while any caller-supplied out_trade_no is replaced.
func epusdtRedirectURL(raw, orderID string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || !strings.EqualFold(parsed.Scheme, "https") || parsed.Host == "" || parsed.User != nil {
		return "", domain.ErrRechargeConfigUnavailable
	}
	query, err := url.ParseQuery(parsed.RawQuery)
	if err != nil {
		return "", domain.ErrRechargeConfigUnavailable
	}
	query.Set("out_trade_no", orderID)
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

func epusdtPaymentAmount(raw string) (string, error) {
	amount, err := domain.ParseMoney(raw)
	minimum, _ := domain.ParseMoney("0.01")
	if err != nil || !amount.GreaterThan(minimum) || !amount.Equal(amount.Round(2)) {
		return "", domain.ErrInvalidAmount
	}
	return amount.StringFixed(2), nil
}

// New orders use the remail-configured points/USDT rate and therefore send a
// direct USDT amount. Snapshots created before that setting existed retain the
// documented CNY contract so their immutable payment amount can still settle.
func epusdtOrderCurrency(config billingapp.RechargeConfig) string {
	switch strings.ToUpper(strings.TrimSpace(config.EpusdtCurrency)) {
	case epusdtDirectCurrency:
		return epusdtDirectCurrency
	case epusdtLegacyCurrency:
		return epusdtLegacyCurrency
	}
	return epusdtLegacyCurrency
}

func epusdtProviderOrderID(rechargeNo string) (string, error) {
	rechargeNo = strings.TrimSpace(rechargeNo)
	if len(rechargeNo) <= 32 {
		if rechargeNo == "" {
			return "", domain.ErrInvalidRecharge
		}
		return rechargeNo, nil
	}
	if domain.IsValidRechargeNo(rechargeNo) {
		return rechargeNo[2:], nil
	}
	return "", domain.ErrInvalidRecharge
}

func validateEpusdtOrder(order epusdtOrder, config billingapp.RechargeConfig, recharge domain.Recharge) error {
	orderID, err := epusdtProviderOrderID(recharge.RechargeNo)
	if err != nil {
		return err
	}
	if strings.TrimSpace(order.PID) != strings.TrimSpace(config.EpusdtPID) ||
		strings.TrimSpace(order.OrderID) != orderID ||
		!sameMoney(order.Amount, recharge.PaymentAmount) ||
		!strings.EqualFold(strings.TrimSpace(order.Currency), epusdtOrderCurrency(config)) ||
		!strings.EqualFold(strings.TrimSpace(order.Token), strings.TrimSpace(config.EpusdtToken)) ||
		!strings.EqualFold(strings.TrimSpace(order.Network), strings.TrimSpace(config.EpusdtNetwork)) {
		return domain.ErrRechargeQueryMismatch
	}
	if order.Status < 1 || order.Status > 4 {
		return domain.ErrRechargeQueryMismatch
	}
	if order.Status == 2 && strings.TrimSpace(order.TradeID) == "" {
		return domain.ErrRechargeQueryMismatch
	}
	if (order.Status == 1 || order.Status == 2) && epusdtOrderCurrency(config) == epusdtDirectCurrency {
		expected, expectedErr := domain.ParseMoney(recharge.PaymentAmount)
		actual, actualErr := domain.ParseMoney(order.ActualAmount)
		if expectedErr != nil || actualErr != nil || actual.LessThan(expected) {
			return domain.ErrRechargeQueryMismatch
		}
	}
	return nil
}

func validateEpusdtSQLiteOrder(order epusdtOrder, config billingapp.RechargeConfig, recharge domain.Recharge) error {
	if err := validateEpusdtOrder(order, config, recharge); err != nil {
		return err
	}
	actual, actualErr := domain.ParseMoney(order.ActualAmount)
	if strings.TrimSpace(order.TradeID) == "" ||
		strings.TrimSpace(order.PayProvider) != "on_chain" ||
		strings.TrimSpace(order.ParentTradeID) != "" ||
		order.PayBySubID != 0 ||
		order.Status == 2 && (strings.TrimSpace(order.ReceiveAddress) == "" ||
			strings.TrimSpace(order.BlockTransactionID) == "" || actualErr != nil || !actual.IsPositive()) {
		return domain.ErrRechargeQueryMismatch
	}
	return nil
}

func validateEpusdtCreateOrder(order epusdtOrder, config billingapp.RechargeConfig, recharge domain.Recharge) error {
	orderID, err := epusdtProviderOrderID(recharge.RechargeNo)
	if err != nil {
		return err
	}
	if strings.TrimSpace(order.OrderID) != orderID ||
		!sameMoney(order.Amount, recharge.PaymentAmount) ||
		!strings.EqualFold(strings.TrimSpace(order.Currency), epusdtOrderCurrency(config)) ||
		!strings.EqualFold(strings.TrimSpace(order.Token), strings.TrimSpace(config.EpusdtToken)) {
		return domain.ErrRechargeQueryMismatch
	}
	// These fields are present on some provider versions but are not part of
	// the GMPay CreateTransactionResponse contract. Validate them when present
	// without requiring them, so a provider upgrade cannot make every create
	// request fail solely because the optional fields are omitted.
	if pid := strings.TrimSpace(order.PID); pid != "" && pid != strings.TrimSpace(config.EpusdtPID) {
		return domain.ErrRechargeQueryMismatch
	}
	if network := strings.TrimSpace(order.Network); network != "" && !strings.EqualFold(network, strings.TrimSpace(config.EpusdtNetwork)) {
		return domain.ErrRechargeQueryMismatch
	}
	if order.Status < 1 || order.Status > 4 || strings.TrimSpace(order.TradeID) == "" {
		return domain.ErrRechargeQueryMismatch
	}
	// In direct-USDT mode the provider should preserve the quoted amount. A
	// provider-side round-up is acceptable, but a smaller amount would create
	// a payment that the signed reconciliation path must later reject.
	if epusdtOrderCurrency(config) == epusdtDirectCurrency && strings.TrimSpace(order.ActualAmount) != "" {
		expected, expectedErr := domain.ParseMoney(recharge.PaymentAmount)
		actual, actualErr := domain.ParseMoney(order.ActualAmount)
		if expectedErr != nil || actualErr != nil || actual.LessThan(expected) {
			return domain.ErrRechargeQueryMismatch
		}
	}
	return nil
}

func (gateway *Epusdt) recoverPaymentURL(ctx context.Context, config billingapp.RechargeConfig, recharge domain.Recharge, fallback error, status int) (string, error) {
	// A timed-out create may have committed remotely. Give the idempotent query
	// a short independent budget when the caller's create deadline is exhausted.
	queryCtx := ctx
	var cancel context.CancelFunc
	if ctx == nil || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		queryCtx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
	} else if ctx.Err() != nil {
		return "", fallback
	}
	if cancel != nil {
		defer cancel()
	}
	order, err := gateway.queryOrder(queryCtx, config, recharge)
	if err == nil && strings.TrimSpace(order.PaymentURL) != "" {
		paymentURL, urlErr := epusdtPaymentURL(config, order.PaymentURL)
		if urlErr == nil {
			return paymentURL, nil
		}
	}
	if fallback != nil {
		return "", fallback
	}
	if status != 0 {
		return "", fmt.Errorf("epusdt create failed with HTTP status %d", status)
	}
	if err == nil {
		return "", billingapp.ErrRechargeGatewayRejected
	}
	return "", err
}

func (gateway *Epusdt) request(ctx context.Context, method string, endpoint *url.URL, values map[string]string) ([]byte, int, error) {
	if gateway == nil || gateway.client == nil {
		return nil, 0, domain.ErrRechargeConfigUnavailable
	}
	form := url.Values{}
	for key, value := range values {
		if value != "" {
			form.Set(key, value)
		}
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint.String(), strings.NewReader(form.Encode()))
	if err != nil {
		return nil, 0, fmt.Errorf("create epusdt request: %w", err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := gateway.client.Do(request)
	if err != nil {
		if ctx != nil && ctx.Err() != nil {
			return nil, 0, fmt.Errorf("epusdt request: %w", ctx.Err())
		}
		return nil, 0, errors.New("epusdt request failed")
	}
	defer response.Body.Close()
	body, readErr := io.ReadAll(io.LimitReader(response.Body, epusdtMaxResponseBytes+1))
	if readErr != nil {
		return nil, response.StatusCode, fmt.Errorf("read epusdt response: %w", readErr)
	}
	if len(body) > epusdtMaxResponseBytes {
		return nil, response.StatusCode, errors.New("epusdt response too large")
	}
	if response.StatusCode != http.StatusOK {
		return body, response.StatusCode, fmt.Errorf("epusdt unexpected HTTP status %d", response.StatusCode)
	}
	return body, response.StatusCode, nil
}

func epusdtBaseURL(config billingapp.RechargeConfig, path string) (*url.URL, error) {
	base, err := url.Parse(strings.TrimSpace(config.EpusdtGatewayURL))
	if err != nil || !strings.EqualFold(base.Scheme, "https") || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return nil, domain.ErrRechargeConfigUnavailable
	}
	base.Path = strings.TrimRight(base.Path, "/") + "/" + strings.TrimLeft(path, "/")
	return base, nil
}

func epusdtPaymentURL(config billingapp.RechargeConfig, raw string) (string, error) {
	base, err := url.Parse(strings.TrimSpace(config.EpusdtGatewayURL))
	if err != nil || !strings.EqualFold(base.Scheme, "https") || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return "", billingapp.ErrRechargeGatewayRejected
	}
	redirect, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || redirect.User != nil {
		return "", billingapp.ErrRechargeGatewayRejected
	}
	redirect = base.ResolveReference(redirect)
	if !strings.EqualFold(redirect.Scheme, "https") || redirect.Host == "" || !epusdtHostAllowed(base, redirect, config.EpusdtAllowedHosts) {
		return "", billingapp.ErrRechargeGatewayRejected
	}
	return redirect.String(), nil
}

func epusdtHostAllowed(base, candidate *url.URL, rawAllowlist string) bool {
	// The configured API origin is always trusted for provider requests and
	// relative cashier links. The allowlist only constrains cross-origin links.
	if sameHTTPSOrigin(base, candidate) {
		return true
	}
	allowlist := strings.TrimSpace(rawAllowlist)
	if allowlist == "" {
		return sameHTTPSOrigin(base, candidate)
	}
	candidateHost, candidatePort, ok := epusdtHostPort(candidate)
	if !ok {
		return false
	}
	for _, raw := range strings.Split(allowlist, ",") {
		entry := strings.TrimSpace(raw)
		if entry == "" {
			continue
		}
		entryURL, err := url.Parse(entry)
		if err == nil && entryURL.Host != "" {
			entry = entryURL.Host
		}
		entryHost, entryPort, ok := epusdtHostPort(&url.URL{Host: entry})
		if ok && strings.EqualFold(entryHost, candidateHost) && entryPort == candidatePort {
			return true
		}
	}
	return false
}

func epusdtHostPort(value *url.URL) (string, string, bool) {
	if value == nil || value.User != nil || value.Host == "" {
		return "", "", false
	}
	host := strings.ToLower(strings.TrimSpace(value.Hostname()))
	if host == "" {
		return "", "", false
	}
	port := value.Port()
	if port == "" {
		port = "443"
	}
	return host, port, true
}

func sameHTTPSOrigin(left, right *url.URL) bool {
	if !strings.EqualFold(left.Scheme, "https") || !strings.EqualFold(right.Scheme, "https") {
		return false
	}
	leftHost, leftPort, leftOK := epusdtHostPort(left)
	rightHost, rightPort, rightOK := epusdtHostPort(right)
	return leftOK && rightOK && strings.EqualFold(leftHost, rightHost) && leftPort == rightPort
}

func epusdtSign(values map[string]string, secret string) string {
	keys := make([]string, 0, len(values))
	for key, value := range values {
		if key != "signature" && value != "" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+"="+values[key])
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(strings.Join(parts, "&")))
	return hex.EncodeToString(mac.Sum(nil))
}

func decodeEpusdtOrder(body []byte) (epusdtOrder, error) {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(body, &envelope); err != nil {
		return epusdtOrder{}, err
	}
	values := rawStringMap(envelope)
	outerValues := make(map[string]string, len(values))
	for key, value := range values {
		if key != "data" {
			outerValues[key] = value
		}
	}
	if raw, ok := envelope["data"]; ok && len(raw) > 0 && string(raw) != "null" {
		var data map[string]json.RawMessage
		if err := json.Unmarshal(raw, &data); err != nil {
			return epusdtOrder{}, err
		}
		values = rawStringMap(data)
	}
	status, err := strconv.Atoi(strings.TrimSpace(values["status"]))
	if err != nil {
		return epusdtOrder{}, domain.ErrRechargeQueryMismatch
	}
	return epusdtOrder{
		PID: values["pid"], OrderID: values["order_id"], TradeID: values["trade_id"], Status: status,
		Amount: values["amount"], Currency: values["currency"], ActualAmount: values["actual_amount"],
		Token: values["token"], Network: values["network"], ReceiveAddress: values["receive_address"],
		BlockTransactionID: values["block_transaction_id"], PaymentURL: values["payment_url"], Signature: values["signature"],
		values: values, outerValues: outerValues,
	}, nil
}

func rawStringMap(raw map[string]json.RawMessage) map[string]string {
	values := make(map[string]string, len(raw))
	for key, value := range raw {
		if key == "data" {
			continue
		}
		values[key] = jsonScalar(value)
	}
	return values
}

func verifyEpusdtOrderSignature(order epusdtOrder, secret string) error {
	if strings.TrimSpace(order.Signature) == "" {
		return domain.ErrRechargeQueryMismatch
	}
	if hmac.Equal([]byte(strings.ToLower(strings.TrimSpace(order.Signature))), []byte(epusdtSign(order.values, secret))) {
		return nil
	}
	// Keep compatibility with envelopes that include status_code in the
	// signed set while the provider rolls out the query endpoint.
	merged := make(map[string]string, len(order.values)+len(order.outerValues))
	for key, value := range order.values {
		merged[key] = value
	}
	for key, value := range order.outerValues {
		if key != "message" {
			merged[key] = value
		}
	}
	if hmac.Equal([]byte(strings.ToLower(strings.TrimSpace(order.Signature))), []byte(epusdtSign(merged, secret))) {
		return nil
	}
	return domain.ErrRechargeQueryMismatch
}

func epusdtStatusCode(body []byte) (int, bool) {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(body, &envelope); err != nil {
		return 0, false
	}
	value, ok := envelope["status_code"]
	if !ok {
		return 0, false
	}
	code, err := strconv.Atoi(strings.TrimSpace(jsonScalar(value)))
	return code, err == nil
}

func epusdtOrderExists(body []byte) bool {
	message := strings.ToLower(string(body))
	for _, marker := range []string{"already exists", "order exists", "duplicate", "已存在", "重复"} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

func epusdtCreateAlreadyExists(body []byte) bool {
	code, ok := epusdtStatusCode(body)
	return ok && code == 10002
}
