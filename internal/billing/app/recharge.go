package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/donnel666/remail/internal/billing/domain"
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
	Amount string
	Bonus  string
}

type RechargeConfig struct {
	Enabled           bool
	Version           string
	GatewayURL        string
	MerchantID        string
	MerchantKey       string
	PrivateKey        string
	PlatformPublicKey string
	NotifyURL         string
	ReturnURL         string
	MinAmount         string
	FeeRate           string
	FeeCap            string
	Tiers             []RechargeTier
	RequestTimeout    time.Duration
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
	Recharge           domain.Recharge
	GatewayConfig      RechargeConfig
	IdempotencyKey     string
	RequestFingerprint string
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
	repo    RechargeRepository
	config  RechargeConfigProvider
	gateway RechargeGateway
	queue   RechargeQueue
	now     func() time.Time
}

type RechargeConfigResult struct {
	Enabled   bool
	MinAmount string
	FeeRate   string
	FeeCap    string
	Tiers     []RechargeTierResult
}

type RechargeTierResult struct {
	Amount        string
	Bonus         string
	RechargeQuota string
	PaymentAmount string
}

type CreateRechargeRequest struct {
	UserID         uint
	Amount         string
	IdempotencyKey string
	ClientIP       string
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
		Enabled:   config.Enabled && validateRechargeGatewayConfig(config) == nil,
		MinAmount: config.MinAmount,
		FeeRate:   config.FeeRate,
		FeeCap:    config.FeeCap,
		Tiers:     make([]RechargeTierResult, 0, len(config.Tiers)),
	}
	for _, tier := range config.Tiers {
		quota, payment, err := rechargeAmounts(config, tier.Amount)
		if err != nil {
			return nil, domain.ErrRechargeConfigUnavailable
		}
		result.Tiers = append(result.Tiers, RechargeTierResult{
			Amount: tier.Amount, Bonus: tier.Bonus,
			RechargeQuota: quota, PaymentAmount: payment,
		})
	}
	return result, nil
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
	if err != nil || !config.Enabled || validateRechargeGatewayConfig(config) != nil {
		return nil, domain.ErrRechargeConfigUnavailable
	}
	quota, payment, err := rechargeAmounts(config, request.Amount)
	if err != nil {
		return nil, err
	}
	amount, err := domain.NormalizePositiveMoney(request.Amount)
	if err != nil {
		return nil, err
	}
	now := uc.now()
	recharge := domain.Recharge{
		RechargeNo:        "RC" + platform.NewUUIDV7CompactUpper(),
		UserID:            request.UserID,
		PaymentMethod:     "alipay",
		RechargeQuota:     quota,
		PaymentAmount:     payment,
		Status:            domain.RechargeStatusPaying,
		GatewayConfigHash: rechargeGatewayConfigHash(config),
		CreatedAt:         now,
		UpdatedAt:         now,
	}
	created, err := uc.repo.CreateRecharge(ctx, CreateRechargeCommand{
		Recharge:           recharge,
		GatewayConfig:      config,
		IdempotencyKey:     strings.TrimSpace(request.IdempotencyKey),
		RequestFingerprint: fingerprint("recharges.create", request.UserID, amount),
	})
	if err != nil {
		return nil, err
	}
	if !domain.IsPendingRechargeStatus(created.Status) {
		return nil, domain.ErrRechargeExpired
	}
	if !uc.now().Before(created.CreatedAt.Add(domain.RechargeReconciliationWindow)) {
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
		ExpiresAt: created.CreatedAt.Add(domain.RechargeReconciliationWindow),
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
	if _, err := uc.repo.ExpirePendingRecharges(ctx, now.Add(-domain.RechargeReconciliationWindow), now); err != nil {
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
	deadline := recharge.CreatedAt.Add(domain.RechargeReconciliationWindow)
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
		_, err := uc.repo.CreditRecharge(ctx, CreditRechargeCommand{
			RechargeNo: recharge.RechargeNo, GatewayTradeNo: query.GatewayTrade,
			PaidAt: query.PaidAt, QueriedAt: queriedAt,
		})
		if errors.Is(err, domain.ErrRechargeQueryMismatch) {
			return uc.repo.FailRecharge(ctx, recharge.RechargeNo, generation, "query_mismatch", queriedAt)
		}
		if errors.Is(err, domain.ErrRechargeExpired) {
			return nil
		}
		return err
	}
	if query.Terminal {
		return uc.repo.FailRecharge(ctx, recharge.RechargeNo, generation, "gateway_status", queriedAt)
	}
	return uc.repo.RecordRechargeQuery(ctx, recharge.RechargeNo, generation, queriedAt)
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
	version := strings.ToLower(strings.TrimSpace(config.Version))
	if strings.TrimSpace(config.MerchantID) == "" ||
		(version == "v1" && strings.TrimSpace(config.MerchantKey) == "") ||
		(version == "v2" && (strings.TrimSpace(config.PrivateKey) == "" || strings.TrimSpace(config.PlatformPublicKey) == "")) ||
		(version != "v1" && version != "v2") {
		return domain.ErrRechargeConfigUnavailable
	}
	for _, raw := range []string{config.GatewayURL, config.NotifyURL, config.ReturnURL} {
		parsed, err := url.Parse(strings.TrimSpace(raw))
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
			return domain.ErrRechargeConfigUnavailable
		}
	}
	return nil
}

func rechargeAmounts(config RechargeConfig, rawAmount string) (string, string, error) {
	amount, err := domain.ParseMoney(rawAmount)
	if err != nil || !amount.IsPositive() || !amount.Equal(amount.Round(2)) {
		return "", "", domain.ErrInvalidAmount
	}
	minimum, err := domain.ParseMoney(config.MinAmount)
	if err != nil || minimum.IsNegative() || amount.LessThan(minimum) {
		return "", "", domain.ErrInvalidAmount
	}
	rate, err := domain.ParseMoney(config.FeeRate)
	if err != nil || rate.IsNegative() || rate.GreaterThan(decimal.NewFromInt(100)) {
		return "", "", domain.ErrRechargeConfigUnavailable
	}
	capAmount, err := domain.ParseMoney(config.FeeCap)
	if err != nil || capAmount.IsNegative() {
		return "", "", domain.ErrRechargeConfigUnavailable
	}
	bonus := decimal.Zero
	for _, tier := range config.Tiers {
		tierAmount, amountErr := domain.ParseMoney(tier.Amount)
		if amountErr == nil && amount.Equal(tierAmount) {
			bonus, err = domain.ParseMoney(tier.Bonus)
			if err != nil || bonus.IsNegative() {
				return "", "", domain.ErrRechargeConfigUnavailable
			}
			break
		}
	}
	fee := amount.Mul(rate).Div(decimal.NewFromInt(100)).Round(2)
	if capAmount.IsPositive() && fee.GreaterThan(capAmount) {
		fee = capAmount
	}
	return domain.MoneyString(amount.Add(bonus)), amount.Add(fee).StringFixed(2), nil
}

func rechargeGatewayConfigHash(config RechargeConfig) string {
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
