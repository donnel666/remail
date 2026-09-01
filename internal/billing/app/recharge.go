package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/donnel666/remail/internal/billing/domain"
	mailapp "github.com/donnel666/remail/internal/mailtransport/app"
	"github.com/donnel666/remail/internal/platform"
	"github.com/shopspring/decimal"
)

const (
	defaultRechargeDispatchLimit = 200
	rechargePaymentCreateTimeout = 10 * time.Second
	defaultRechargeQueryTimeout  = 5 * time.Second
	maxRechargeQueryTimeout      = 30 * time.Second
)

var ErrRechargeGatewayRejected = errors.New("billing: recharge gateway rejected query")

type RechargeTier struct {
	Points      string
	BonusPoints string
}

type RechargeConfig struct {
	Enabled bool
	// PaymentMethod/Provider are set on the immutable snapshot stored with an
	// order. Empty values are kept compatible with the legacy EPay snapshot.
	PaymentMethod     string
	Provider          string
	Version           string
	GatewayURL        string
	MerchantID        string
	MerchantKey       string
	PrivateKey        string
	PlatformPublicKey string
	NotifyURL         string
	ReturnURL         string
	PointsPerYuan     string
	MinPoints         string
	FeeRate           string
	FeeCapPoints      string
	Tiers             []RechargeTier
	MaxPendingOrders  int
	RequestTimeout    time.Duration
	EpusdtEnabled     bool
	EpusdtGatewayURL  string
	EpusdtPID         string
	EpusdtCurrency    string
	// EpusdtPointsPerUSDT is the remail-side exchange rate for new EPUSDT
	// orders. A missing value is retained for legacy CNY snapshots.
	EpusdtPointsPerUSDT        string
	EpusdtMinimumPaymentAmount string
	// EpusdtAPIKey is retained for deployments that keep a separate label;
	// GMPay authenticates requests with PID + APISecret and does not send it.
	EpusdtAPIKey       string
	EpusdtAPISecret    string
	EpusdtToken        string
	EpusdtNetwork      string
	EpusdtNotifyURL    string
	EpusdtReturnURL    string
	EpusdtAllowedHosts string
}

type RechargeConfigProvider interface {
	Current() (RechargeConfig, error)
}

type RechargeGatewayQuery struct {
	Paid         bool
	Terminal     bool
	GatewayTrade string
	PaidAt       *time.Time
}

type RechargeGateway interface {
	PaymentURL(ctx context.Context, config RechargeConfig, recharge domain.Recharge, clientIP string) (string, error)
	Query(ctx context.Context, config RechargeConfig, recharge domain.Recharge) (RechargeGatewayQuery, error)
}

type RechargeTask struct {
	RechargeNo string `json:"rechargeNo"`
}

type RechargeQueue interface {
	Enqueue(ctx context.Context, task RechargeTask) error
}

type CreateRechargeCommand struct {
	Recharge                  domain.Recharge
	GatewayConfig             RechargeConfig
	MaxPendingOrders          int
	IdempotencyKey            string
	RequestFingerprint        string
	LegacyRequestFingerprints []string
	RequireIdempotencyReplay  bool
}

type CreditRechargeCommand struct {
	RechargeNo     string
	GatewayTradeNo string
	PaidAt         *time.Time
	QueriedAt      time.Time
}

type RechargeRepository interface {
	CreateRecharge(ctx context.Context, command CreateRechargeCommand) (*domain.Recharge, error)
	GetRechargeByNo(ctx context.Context, rechargeNo string) (*domain.Recharge, error)
	MarkRechargeCallback(ctx context.Context, rechargeNo string, callbackAt time.Time) (bool, error)
	ListDueRecharges(ctx context.Context, now time.Time, limit int) ([]domain.Recharge, error)
	ExpirePendingRecharges(ctx context.Context, createdBefore, now time.Time) (int64, error)
	ClaimRechargeQuery(ctx context.Context, rechargeNo string, claimedAt, leaseUntil time.Time) (*domain.Recharge, RechargeConfig, int, bool, error)
	RecordRechargeQuery(ctx context.Context, rechargeNo string, generation int, queriedAt time.Time) error
	FailRecharge(ctx context.Context, rechargeNo string, generation int, reason string, failedAt time.Time) error
	CreditRecharge(ctx context.Context, command CreditRechargeCommand) (*domain.Recharge, error)
}

type RechargeUseCase struct {
	repo     RechargeRepository
	config   RechargeConfigProvider
	gateway  RechargeGateway
	queue    RechargeQueue
	delivery mailapp.DeliveryPort
	users    UserDirectory
	wallets  *WalletUseCase
	now      func() time.Time
}

type RechargeConfigResult struct {
	Enabled        bool
	PaymentMethods []string
	MinPoints      string
	FeeRate        string
	FeeCapPoints   string
	Tiers          []RechargeTierResult
}

func (uc *RechargeUseCase) SetNotifications(delivery mailapp.DeliveryPort, users UserDirectory, wallets *WalletUseCase) {
	uc.delivery, uc.users, uc.wallets = delivery, users, wallets
}

type RechargeTierResult struct {
	Points         string
	BonusPoints    string
	FeePoints      string
	CreditedPoints string
}

type CreateRechargeRequest struct {
	UserID         uint
	Points         string
	PaymentMethod  string
	IdempotencyKey string
	ClientIP       string
}

type RechargeQuoteResult struct {
	Points          string
	BonusPoints     string
	FeePoints       string
	CreditedPoints  string
	PaymentAmount   string
	PaymentCurrency string
}

type CreateRechargeResult struct {
	Recharge  domain.Recharge
	PayURL    string
	ExpiresAt time.Time
}

func NewRechargeUseCase(repo RechargeRepository, config RechargeConfigProvider, gateway RechargeGateway, queue RechargeQueue) *RechargeUseCase {
	return &RechargeUseCase{
		repo: repo, config: config, gateway: gateway, queue: queue,
		now: func() time.Time { return time.Now().UTC() },
	}
}

func (uc *RechargeUseCase) Config() (*RechargeConfigResult, error) {
	config, err := uc.currentConfig()
	if err != nil {
		return nil, err
	}
	result := &RechargeConfigResult{
		PaymentMethods: availableRechargePaymentMethods(config),
		MinPoints:      config.MinPoints,
		FeeRate:        config.FeeRate,
		FeeCapPoints:   config.FeeCapPoints,
		Tiers:          make([]RechargeTierResult, 0, len(config.Tiers)),
	}
	result.Enabled = len(result.PaymentMethods) > 0
	if !result.Enabled {
		return result, nil
	}
	// Tier fee semantics follow the first available method. The quote endpoint
	// remains method-aware for custom and preset selections.
	config.PaymentMethod = result.PaymentMethods[0]
	for _, tier := range config.Tiers {
		tierPoints, err := domain.ParseMoney(tier.Points)
		if err != nil || !tierPoints.IsPositive() {
			return nil, domain.ErrRechargeConfigUnavailable
		}
		if !tierPoints.Equal(tierPoints.Truncate(0)) {
			continue
		}
		quote, _, err := rechargeAmounts(config, tier.Points)
		if errors.Is(err, domain.ErrRechargePaymentBelowMinimum) {
			continue
		}
		if err != nil {
			return nil, domain.ErrRechargeConfigUnavailable
		}
		result.Tiers = append(result.Tiers, RechargeTierResult{
			Points: quote.Points, BonusPoints: quote.BonusPoints,
			FeePoints: quote.FeePoints, CreditedPoints: quote.CreditedPoints,
		})
	}
	return result, nil
}

func (uc *RechargeUseCase) Quote(rawPoints string, requestedMethods ...string) (*RechargeQuoteResult, error) {
	config, err := uc.currentConfig()
	if err != nil {
		return nil, err
	}
	if len(requestedMethods) > 0 && strings.TrimSpace(requestedMethods[0]) != "" {
		method, ok := domain.NormalizeRechargePaymentMethod(requestedMethods[0])
		if !ok {
			return nil, domain.ErrInvalidRecharge
		}
		if !rechargePaymentMethodAvailable(config, method) {
			return nil, domain.ErrRechargeConfigUnavailable
		}
		config.PaymentMethod = method
	} else {
		method, ok := domain.NormalizeRechargePaymentMethod(config.PaymentMethod)
		if !ok {
			return nil, domain.ErrRechargeConfigUnavailable
		}
		if strings.TrimSpace(config.PaymentMethod) == "" {
			method = defaultRechargePaymentMethod(config)
		}
		if !rechargePaymentMethodAvailable(config, method) {
			return nil, domain.ErrRechargeConfigUnavailable
		}
		config.PaymentMethod = method
	}
	quote, _, err := rechargeAmounts(config, rawPoints)
	return quote, err
}

func (uc *RechargeUseCase) Create(ctx context.Context, request CreateRechargeRequest) (*CreateRechargeResult, error) {
	if request.UserID == 0 {
		return nil, domain.ErrInvalidRecharge
	}
	if strings.TrimSpace(request.IdempotencyKey) == "" {
		return nil, domain.ErrIdempotencyRequired
	}
	if len(strings.TrimSpace(request.IdempotencyKey)) > 128 {
		return nil, domain.ErrInvalidIdempotencyKey
	}
	if uc == nil || uc.repo == nil || uc.gateway == nil {
		return nil, domain.ErrRechargeConfigUnavailable
	}
	if uc.queue == nil {
		return nil, domain.ErrRechargeQueueUnavailable
	}
	config, err := uc.currentConfig()
	if err != nil || !anyRechargeGatewayEnabled(config) || config.MaxPendingOrders <= 0 {
		return nil, domain.ErrRechargeConfigUnavailable
	}
	method, ok := domain.NormalizeRechargePaymentMethod(request.PaymentMethod)
	if !ok {
		return nil, domain.ErrInvalidRecharge
	}
	if strings.TrimSpace(request.PaymentMethod) == "" {
		method = defaultRechargePaymentMethod(config)
	}
	if !rechargePaymentMethodAvailable(config, method) {
		return nil, domain.ErrRechargeConfigUnavailable
	}
	config.PaymentMethod = method
	if method == domain.RechargePaymentMethodEpusdtUSDTTron {
		config.Provider = "epusdt"
	} else {
		config.Provider = "epay"
	}
	if err := validateRechargeGatewayConfig(config); err != nil {
		return nil, err
	}
	quote, payment, amountErr := rechargeAmounts(config, request.Points)
	belowMinimum := errors.Is(amountErr, domain.ErrRechargePaymentBelowMinimum)
	if amountErr != nil && !belowMinimum {
		return nil, amountErr
	}
	now := uc.now()
	recharge := domain.Recharge{
		RechargeNo:        "RC" + platform.NewUUIDV7CompactUpper(),
		UserID:            request.UserID,
		PaymentMethod:     method,
		RechargeQuota:     quote.CreditedPoints,
		PaymentAmount:     payment,
		Status:            domain.RechargeStatusPaying,
		GatewayConfigHash: rechargeGatewayConfigHash(config),
		CreatedAt:         now,
		UpdatedAt:         now,
	}
	created, err := uc.repo.CreateRecharge(ctx, CreateRechargeCommand{
		Recharge:                  recharge,
		GatewayConfig:             config,
		MaxPendingOrders:          config.MaxPendingOrders,
		IdempotencyKey:            strings.TrimSpace(request.IdempotencyKey),
		RequestFingerprint:        rechargeCreateFingerprint(request.UserID, quote.Points, method),
		LegacyRequestFingerprints: rechargeLegacyFingerprints(request.UserID, quote.Points, method),
		RequireIdempotencyReplay:  belowMinimum,
	})
	if err != nil {
		if belowMinimum && errors.Is(err, domain.ErrRechargePaymentBelowMinimum) {
			return nil, amountErr
		}
		return nil, err
	}
	if !domain.IsPendingRechargeStatus(created.Status) {
		return nil, domain.ErrRechargeExpired
	}
	if !uc.now().Before(created.CreatedAt.Add(domain.RechargeReconciliationWindow())) {
		return nil, domain.ErrRechargeExpired
	}
	if created.GatewayConfigHash != rechargeGatewayConfigHash(config) {
		return nil, domain.ErrRechargeExpired
	}
	paymentCtx, cancel := context.WithTimeout(ctx, rechargePaymentCreateTimeout)
	defer cancel()
	payURL, err := uc.gateway.PaymentURL(paymentCtx, config, *created, request.ClientIP)
	if err != nil {
		return nil, domain.ErrRechargeConfigUnavailable
	}
	return &CreateRechargeResult{
		Recharge:  *created,
		PayURL:    payURL,
		ExpiresAt: created.CreatedAt.Add(domain.RechargeReconciliationWindow()),
	}, nil
}

func (uc *RechargeUseCase) Get(ctx context.Context, userID uint, rechargeNo string) (*domain.Recharge, error) {
	if userID == 0 || strings.TrimSpace(rechargeNo) == "" {
		return nil, domain.ErrInvalidRecharge
	}
	recharge, err := uc.repo.GetRechargeByNo(ctx, strings.TrimSpace(rechargeNo))
	if err != nil {
		return nil, err
	}
	if recharge.UserID != userID {
		return nil, domain.ErrRechargeNotFound
	}
	return recharge, nil
}

func (uc *RechargeUseCase) NotifyCallback(ctx context.Context, rechargeNo string) error {
	rechargeNo = strings.TrimSpace(rechargeNo)
	if !domain.IsValidRechargeNo(rechargeNo) {
		return nil
	}
	if uc == nil || uc.repo == nil {
		return domain.ErrRechargeConfigUnavailable
	}
	marked, err := uc.repo.MarkRechargeCallback(ctx, rechargeNo, uc.now())
	if err != nil || !marked || uc.queue == nil {
		return err
	}
	// The dispatcher sees the persisted callback state and retries delivery if this enqueue fails.
	_ = uc.queue.Enqueue(ctx, RechargeTask{RechargeNo: rechargeNo})
	return nil
}

func (uc *RechargeUseCase) Dispatch(ctx context.Context) error {
	if uc == nil || uc.repo == nil {
		return domain.ErrRechargeConfigUnavailable
	}
	if uc.queue == nil {
		return domain.ErrRechargeQueueUnavailable
	}
	now := uc.now()
	if _, err := uc.repo.ExpirePendingRecharges(ctx, now.Add(-domain.RechargeReconciliationWindow()), now); err != nil {
		return err
	}
	recharges, err := uc.repo.ListDueRecharges(ctx, now, defaultRechargeDispatchLimit)
	if err != nil {
		return err
	}
	for _, recharge := range recharges {
		if err := uc.queue.Enqueue(ctx, RechargeTask{RechargeNo: recharge.RechargeNo}); err != nil {
			return err
		}
	}
	return nil
}

func (uc *RechargeUseCase) Reconcile(ctx context.Context, task RechargeTask) error {
	now := uc.now()
	recharge, config, generation, claimed, err := uc.repo.ClaimRechargeQuery(
		ctx,
		strings.TrimSpace(task.RechargeNo),
		now,
		now.Add(domain.RechargeQueryLease),
	)
	if errors.Is(err, domain.ErrRechargeNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if !claimed {
		return nil
	}
	deadline := recharge.CreatedAt.Add(domain.RechargeReconciliationWindow())
	if !now.Before(deadline) {
		return uc.repo.FailRecharge(ctx, recharge.RechargeNo, generation, "query_timeout", now)
	}
	if validateRechargeGatewayConfig(config) != nil || recharge.GatewayConfigHash != rechargeGatewayConfigHash(config) {
		return uc.repo.RecordRechargeQuery(ctx, recharge.RechargeNo, generation, now)
	}
	requestTimeout := config.RequestTimeout
	if requestTimeout <= 0 || requestTimeout > maxRechargeQueryTimeout {
		requestTimeout = defaultRechargeQueryTimeout
	}
	queryDeadline := now.Add(requestTimeout)
	if deadline.Before(queryDeadline) {
		queryDeadline = deadline
	}
	queryCtx, cancel := context.WithDeadline(ctx, queryDeadline)
	query, queryErr := uc.gateway.Query(queryCtx, config, *recharge)
	cancel()
	queriedAt := uc.now()
	if queryErr != nil {
		if errors.Is(queryErr, domain.ErrRechargeQueryMismatch) {
			return uc.repo.FailRecharge(ctx, recharge.RechargeNo, generation, "query_mismatch", queriedAt)
		}
		if !queriedAt.Before(deadline) {
			return uc.repo.FailRecharge(ctx, recharge.RechargeNo, generation, "query_timeout", queriedAt)
		}
		return uc.repo.RecordRechargeQuery(ctx, recharge.RechargeNo, generation, queriedAt)
	}
	if !queriedAt.Before(deadline) {
		return uc.repo.FailRecharge(ctx, recharge.RechargeNo, generation, "query_timeout", queriedAt)
	}
	if query.Paid {
		credited, err := uc.repo.CreditRecharge(ctx, CreditRechargeCommand{
			RechargeNo: recharge.RechargeNo, GatewayTradeNo: query.GatewayTrade,
			PaidAt: query.PaidAt, QueriedAt: queriedAt,
		})
		if errors.Is(err, domain.ErrRechargeQueryMismatch) {
			return uc.repo.FailRecharge(ctx, recharge.RechargeNo, generation, "query_mismatch", queriedAt)
		}
		if errors.Is(err, domain.ErrRechargeExpired) {
			return nil
		}
		if err == nil {
			uc.notifyRechargeCredited(ctx, credited)
		}
		return err
	}
	if query.Terminal {
		return uc.repo.FailRecharge(ctx, recharge.RechargeNo, generation, "gateway_status", queriedAt)
	}
	return uc.repo.RecordRechargeQuery(ctx, recharge.RechargeNo, generation, queriedAt)
}

func (uc *RechargeUseCase) notifyRechargeCredited(ctx context.Context, recharge *domain.Recharge) {
	if uc == nil || uc.delivery == nil || uc.users == nil || uc.wallets == nil || recharge == nil {
		return
	}
	wallet, err := uc.wallets.GetWallet(ctx, recharge.UserID)
	if err == nil {
		err = sendRechargeCreditedNotification(
			ctx, uc.delivery, uc.users, recharge.UserID,
			recharge.RechargeNo, recharge.RechargeQuota, wallet.Wallet.ConsumerBalance,
		)
	}
	if err != nil {
		slog.Warn("send recharge notification failed", "user_id", recharge.UserID, "recharge_no", recharge.RechargeNo, "error", err)
	}
}

func (uc *RechargeUseCase) currentConfig() (RechargeConfig, error) {
	if uc == nil || uc.config == nil {
		return RechargeConfig{}, domain.ErrRechargeConfigUnavailable
	}
	config, err := uc.config.Current()
	if err != nil {
		return RechargeConfig{}, domain.ErrRechargeConfigUnavailable
	}
	if config.RequestTimeout <= 0 {
		config.RequestTimeout = defaultRechargeQueryTimeout
	}
	return config, nil
}

func validateRechargeGatewayConfig(config RechargeConfig) error {
	method, ok := domain.NormalizeRechargePaymentMethod(config.PaymentMethod)
	if !ok {
		return domain.ErrRechargeConfigUnavailable
	}
	if method == domain.RechargePaymentMethodEpusdtUSDTTron {
		currency := strings.ToUpper(strings.TrimSpace(config.EpusdtCurrency))
		if currency != "" && currency != "CNY" && currency != "USDT" {
			return domain.ErrRechargeConfigUnavailable
		}
		if strings.TrimSpace(config.EpusdtPID) == "" || strings.TrimSpace(config.EpusdtAPISecret) == "" ||
			strings.TrimSpace(config.EpusdtToken) == "" || strings.TrimSpace(config.EpusdtNetwork) == "" {
			return domain.ErrRechargeConfigUnavailable
		}
		if !validHTTPSBaseURL(config.EpusdtGatewayURL) || !validHTTPSURL(config.EpusdtNotifyURL) || !validHTTPSURL(config.EpusdtReturnURL) {
			return domain.ErrRechargeConfigUnavailable
		}
		if !strings.EqualFold(strings.TrimSpace(config.EpusdtToken), "USDT") || !strings.EqualFold(strings.TrimSpace(config.EpusdtNetwork), "tron") {
			return domain.ErrRechargeConfigUnavailable
		}
		return nil
	}

	version := strings.ToLower(strings.TrimSpace(config.Version))
	if strings.TrimSpace(config.MerchantID) == "" ||
		(version == "v1" && strings.TrimSpace(config.MerchantKey) == "") ||
		(version == "v2" && (strings.TrimSpace(config.PrivateKey) == "" || strings.TrimSpace(config.PlatformPublicKey) == "")) ||
		(version != "v1" && version != "v2") {
		return domain.ErrRechargeConfigUnavailable
	}
	if !validHTTPSURL(config.GatewayURL) || !validHTTPSURL(config.NotifyURL) || !validHTTPSURL(config.ReturnURL) {
		return domain.ErrRechargeConfigUnavailable
	}
	return nil
}

func validHTTPSURL(raw string) bool {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	return err == nil && strings.EqualFold(parsed.Scheme, "https") && parsed.Host != "" && parsed.User == nil
}

func validHTTPSBaseURL(raw string) bool {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	return validHTTPSURL(raw) && err == nil && !parsed.ForceQuery && parsed.RawQuery == "" && parsed.Fragment == ""
}

func anyRechargeGatewayEnabled(config RechargeConfig) bool {
	return config.Enabled || config.EpusdtEnabled
}

func rechargePaymentMethodAvailable(config RechargeConfig, method string) bool {
	switch method {
	case domain.RechargePaymentMethodAlipay:
		return config.Enabled && validateRechargeGatewayConfig(RechargeConfig{
			PaymentMethod: domain.RechargePaymentMethodAlipay, Version: config.Version,
			GatewayURL: config.GatewayURL, MerchantID: config.MerchantID, MerchantKey: config.MerchantKey,
			PrivateKey: config.PrivateKey, PlatformPublicKey: config.PlatformPublicKey,
			NotifyURL: config.NotifyURL, ReturnURL: config.ReturnURL,
		}) == nil
	case domain.RechargePaymentMethodEpusdtUSDTTron:
		return config.EpusdtEnabled && strings.EqualFold(strings.TrimSpace(config.EpusdtCurrency), "USDT") && validEpusdtPointsPerUSDT(config.EpusdtPointsPerUSDT) && validEpusdtMinimumPaymentAmount(config.EpusdtMinimumPaymentAmount) && validateRechargeGatewayConfig(RechargeConfig{
			PaymentMethod:    domain.RechargePaymentMethodEpusdtUSDTTron,
			EpusdtGatewayURL: config.EpusdtGatewayURL, EpusdtPID: config.EpusdtPID,
			EpusdtCurrency: config.EpusdtCurrency, EpusdtPointsPerUSDT: config.EpusdtPointsPerUSDT,
			EpusdtAPISecret: config.EpusdtAPISecret, EpusdtToken: config.EpusdtToken,
			EpusdtNetwork: config.EpusdtNetwork, EpusdtNotifyURL: config.EpusdtNotifyURL,
			EpusdtReturnURL: config.EpusdtReturnURL,
		}) == nil
	default:
		return false
	}
}

func availableRechargePaymentMethods(config RechargeConfig) []string {
	methods := make([]string, 0, 2)
	if rechargePaymentMethodAvailable(config, domain.RechargePaymentMethodAlipay) {
		methods = append(methods, domain.RechargePaymentMethodAlipay)
	}
	if rechargePaymentMethodAvailable(config, domain.RechargePaymentMethodEpusdtUSDTTron) {
		methods = append(methods, domain.RechargePaymentMethodEpusdtUSDTTron)
	}
	return methods
}

func defaultRechargePaymentMethod(config RechargeConfig) string {
	if method, ok := domain.NormalizeRechargePaymentMethod(config.PaymentMethod); ok && rechargePaymentMethodAvailable(config, method) {
		return method
	}
	methods := availableRechargePaymentMethods(config)
	if len(methods) > 0 {
		return methods[0]
	}
	return domain.RechargePaymentMethodAlipay
}

func rechargeCreateFingerprint(userID uint, points, method string) string {
	if strings.TrimSpace(method) == "" || method == domain.RechargePaymentMethodAlipay {
		// Keep the pre-payment-method EPay fingerprint replayable after rollout.
		return fingerprint("recharges.create", userID, points)
	}
	return fingerprint("recharges.create", userID, points, method)
}

func rechargeLegacyFingerprints(userID uint, points, method string) []string {
	if strings.TrimSpace(method) != "" && method != domain.RechargePaymentMethodAlipay {
		return nil
	}
	// Before migration 68, EPay fingerprints used the RMB amount. Migration 68
	// multiplied recharge points by the fixed 1000-point yuan scale; only an
	// exact two-decimal quotient can have been a valid legacy request amount.
	legacyAmount, err := domain.ParseMoney(points)
	if err != nil || !legacyAmount.IsPositive() {
		return nil
	}
	legacyAmount = legacyAmount.Div(decimal.NewFromInt(1000))
	if !legacyAmount.Equal(legacyAmount.Round(2)) {
		return nil
	}
	return []string{legacyFingerprint("recharges.create", userID, domain.MoneyString(legacyAmount))}
}

func rechargeAmounts(config RechargeConfig, rawPoints string) (*RechargeQuoteResult, string, error) {
	points, err := domain.ParseMoney(rawPoints)
	if err != nil || !points.IsPositive() || !points.Equal(points.Truncate(0)) {
		return nil, "", domain.ErrInvalidAmount
	}
	minimum, err := domain.ParseMoney(config.MinPoints)
	if err != nil || minimum.IsNegative() || points.LessThan(minimum) {
		return nil, "", domain.ErrInvalidAmount
	}
	method, ok := domain.NormalizeRechargePaymentMethod(config.PaymentMethod)
	if !ok {
		return nil, "", domain.ErrRechargeConfigUnavailable
	}
	bonus := decimal.Zero
	for _, tier := range config.Tiers {
		tierPoints, pointsErr := domain.ParseMoney(tier.Points)
		if pointsErr == nil && points.Equal(tierPoints) {
			bonus, err = domain.ParseMoney(tier.BonusPoints)
			if err != nil || bonus.IsNegative() {
				return nil, "", domain.ErrRechargeConfigUnavailable
			}
			break
		}
	}
	fee := decimal.Zero
	paymentCurrency := "CNY"
	var payment decimal.Decimal
	if method == domain.RechargePaymentMethodEpusdtUSDTTron {
		pointsPerUSDT, rateErr := domain.ParseMoney(config.EpusdtPointsPerUSDT)
		if rateErr != nil || !pointsPerUSDT.IsPositive() {
			return nil, "", domain.ErrRechargeConfigUnavailable
		}
		paymentCurrency = "USDT"
		payment = points.Div(pointsPerUSDT).RoundCeil(2)
	} else {
		rate, rateErr := domain.ParseMoney(config.FeeRate)
		if rateErr != nil || rate.IsNegative() || rate.GreaterThan(decimal.NewFromInt(100)) {
			return nil, "", domain.ErrRechargeConfigUnavailable
		}
		capPoints, capErr := domain.ParseMoney(config.FeeCapPoints)
		if capErr != nil || capPoints.IsNegative() {
			return nil, "", domain.ErrRechargeConfigUnavailable
		}
		pointsPerYuan, pointsErr := domain.ParseMoney(config.PointsPerYuan)
		if pointsErr != nil || !pointsPerYuan.IsPositive() {
			return nil, "", domain.ErrRechargeConfigUnavailable
		}
		fee = points.Mul(rate).Div(decimal.NewFromInt(100)).RoundCeil(6)
		if capPoints.IsPositive() && fee.GreaterThan(capPoints) {
			fee = capPoints
		}
		payment = points.Add(fee).Div(pointsPerYuan).RoundCeil(2)
	}
	credited := points.Add(bonus)
	if payment.GreaterThan(decimal.RequireFromString("9999999999999999.99")) {
		return nil, "", domain.ErrInvalidAmount
	}
	for _, value := range []decimal.Decimal{points, bonus, fee, credited} {
		if _, err := domain.ParseMoney(domain.MoneyString(value)); err != nil {
			return nil, "", domain.ErrInvalidAmount
		}
	}
	result := &RechargeQuoteResult{
		Points: domain.MoneyString(points), BonusPoints: domain.MoneyString(bonus),
		FeePoints: domain.MoneyString(fee), CreditedPoints: domain.MoneyString(credited),
		PaymentAmount: payment.StringFixed(2), PaymentCurrency: paymentCurrency,
	}
	if err := validateRechargePaymentAmount(method, result.PaymentAmount, config.EpusdtMinimumPaymentAmount); err != nil {
		return result, result.PaymentAmount, err
	}
	return result, result.PaymentAmount, nil
}

func validEpusdtPointsPerUSDT(raw string) bool {
	rate, err := domain.ParseMoney(raw)
	return err == nil && rate.IsPositive()
}

func validEpusdtMinimumPaymentAmount(raw string) bool {
	minimum, err := domain.ParseMoney(raw)
	return err == nil && !minimum.LessThan(decimal.New(2, -2)) && minimum.Equal(minimum.Round(2))
}

func validateRechargePaymentAmount(method, raw, minimumRaw string) error {
	if method != domain.RechargePaymentMethodEpusdtUSDTTron {
		return nil
	}
	amount, err := domain.ParseMoney(raw)
	if err != nil || !amount.IsPositive() {
		return domain.ErrInvalidAmount
	}
	minimum, err := domain.ParseMoney(minimumRaw)
	if err != nil || minimum.LessThan(decimal.New(2, -2)) || !minimum.Equal(minimum.Round(2)) {
		return domain.ErrRechargeConfigUnavailable
	}
	if amount.LessThan(minimum) {
		return &domain.RechargePaymentBelowMinimumError{
			MinimumPaymentAmount: domain.MoneyString(minimum),
			PaymentCurrency:      "USDT",
		}
	}
	return nil
}

func rechargeGatewayConfigHash(config RechargeConfig) string {
	method, _ := domain.NormalizeRechargePaymentMethod(config.PaymentMethod)
	if method == domain.RechargePaymentMethodEpusdtUSDTTron {
		parts := []string{
			"epusdt", strings.TrimSpace(config.EpusdtGatewayURL), strings.TrimSpace(config.EpusdtPID),
			strings.TrimSpace(config.EpusdtAPISecret), strings.TrimSpace(config.EpusdtToken),
			strings.TrimSpace(config.EpusdtNetwork), strings.TrimSpace(config.EpusdtNotifyURL),
			strings.TrimSpace(config.EpusdtReturnURL), strings.TrimSpace(config.EpusdtAllowedHosts),
		}
		// Keep the old hash layout for snapshots that predate the explicit
		// currency field; new direct-USDT snapshots bind their protocol mode.
		if currency := strings.TrimSpace(config.EpusdtCurrency); currency != "" {
			parts = append(parts, strings.ToUpper(currency))
		}
		hash := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
		return hex.EncodeToString(hash[:])
	}
	// Keep the historical EPay hash byte-for-byte compatible for in-flight
	// orders created before payment methods were introduced.
	credential := config.MerchantKey
	if strings.EqualFold(strings.TrimSpace(config.Version), "v2") {
		credential = config.PrivateKey + "\x00" + config.PlatformPublicKey
	}
	hash := sha256.Sum256([]byte(strings.Join([]string{
		strings.ToLower(strings.TrimSpace(config.Version)), strings.TrimSpace(config.GatewayURL),
		strings.TrimSpace(config.MerchantID), credential,
	}, "\x00")))
	return hex.EncodeToString(hash[:])
}
