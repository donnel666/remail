package infra

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	billingapp "github.com/donnel666/remail/internal/billing/app"
	"github.com/donnel666/remail/internal/billing/domain"
	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	governanceinfra "github.com/donnel666/remail/internal/governance/infra"
	"github.com/donnel666/remail/internal/money"
	"github.com/donnel666/remail/internal/platform"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"github.com/donnel666/remail/internal/trade/successranking"
	"github.com/go-sql-driver/mysql"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type WalletModel struct {
	UserID              uint      `gorm:"primaryKey;column:user_id"`
	ConsumerBalance     string    `gorm:"type:decimal(18,6);not null;default:0;column:consumer_balance"`
	SupplierAvailable   string    `gorm:"type:decimal(18,6);not null;default:0;column:supplier_available"`
	SupplierFrozen      string    `gorm:"type:decimal(18,6);not null;default:0;column:supplier_frozen"`
	TotalRecharged      string    `gorm:"type:decimal(18,6);not null;default:0;column:total_recharged"`
	TotalSpend          string    `gorm:"type:decimal(18,6);not null;default:0;column:total_spend"`
	SpendCount          int64     `gorm:"not null;default:0;column:spend_count"`
	BalanceWarningLevel int       `gorm:"not null;default:4;column:balance_warning_level"`
	BalanceWarningCycle uint64    `gorm:"not null;default:0;column:balance_warning_cycle"`
	CreatedAt           time.Time `gorm:"not null;autoCreateTime;column:created_at"`
	UpdatedAt           time.Time `gorm:"not null;autoUpdateTime;column:updated_at"`
}

func (WalletModel) TableName() string {
	return "wallets"
}

type WalletTransactionModel struct {
	ID              uint      `gorm:"primaryKey;autoIncrement"`
	TransactionNo   string    `gorm:"type:varchar(64);not null;column:transaction_no"`
	UserID          uint      `gorm:"not null;column:user_id"`
	TransactionType string    `gorm:"type:varchar(32);not null;column:transaction_type"`
	BalanceBucket   string    `gorm:"type:varchar(32);not null;column:balance_bucket"`
	Direction       string    `gorm:"type:varchar(8);not null"`
	Amount          string    `gorm:"type:decimal(18,6);not null"`
	BalanceBefore   string    `gorm:"type:decimal(18,6);not null;column:balance_before"`
	BalanceAfter    string    `gorm:"type:decimal(18,6);not null;column:balance_after"`
	BizType         string    `gorm:"type:varchar(32);not null;column:biz_type"`
	BizID           string    `gorm:"type:varchar(128);not null;column:biz_id"`
	ReversalOfNo    *string   `gorm:"type:varchar(64);column:reversal_of_no"`
	IdempotencyKey  string    `gorm:"type:varchar(128);not null;default:'';column:idempotency_key"`
	RequestID       string    `gorm:"type:varchar(64);not null;default:'';column:request_id"`
	CreatedAt       time.Time `gorm:"not null;autoCreateTime;column:created_at"`
}

func (WalletTransactionModel) TableName() string {
	return "wallet_transactions"
}

type IdempotencyKeyModel struct {
	ID                 uint           `gorm:"primaryKey;autoIncrement"`
	OwnerUserID        uint           `gorm:"not null;column:owner_user_id"`
	IdempotencyKey     string         `gorm:"type:varchar(128);not null;column:idempotency_key"`
	Operation          string         `gorm:"type:varchar(64);not null"`
	RequestFingerprint string         `gorm:"type:char(64);not null;column:request_fingerprint"`
	Status             string         `gorm:"type:varchar(32);not null;default:'succeeded'"`
	ResponseJSON       sql.NullString `gorm:"type:json;column:response_json"`
	CreatedAt          time.Time      `gorm:"not null;autoCreateTime;column:created_at"`
	UpdatedAt          time.Time      `gorm:"not null;autoUpdateTime;column:updated_at"`
}

func (IdempotencyKeyModel) TableName() string {
	return "idempotency_keys"
}

type RechargeModel struct {
	ID                uint       `gorm:"primaryKey;autoIncrement"`
	RechargeNo        string     `gorm:"type:varchar(64);not null;column:recharge_no"`
	UserID            uint       `gorm:"not null;column:user_id"`
	PaymentMethod     string     `gorm:"type:varchar(32);not null;column:payment_method"`
	RechargeQuota     string     `gorm:"type:decimal(18,6);not null;column:recharge_quota"`
	PaymentAmount     string     `gorm:"type:decimal(18,2);not null;column:payment_amount"`
	Status            string     `gorm:"type:varchar(32);not null;default:'paying'"`
	GatewayTradeNo    *string    `gorm:"type:varchar(64);column:gateway_trade_no"`
	GatewayConfigHash string     `gorm:"type:char(64);not null;default:'';column:gateway_config_hash"`
	FailureReason     string     `gorm:"type:varchar(64);not null;default:'';column:failure_reason"`
	QueryAttempts     int        `gorm:"not null;default:0;column:query_attempts"`
	LastQueriedAt     *time.Time `gorm:"column:last_queried_at"`
	QueryGeneration   int        `gorm:"not null;default:0;column:query_generation"`
	QueryLeaseUntil   *time.Time `gorm:"column:query_lease_until"`
	GatewayConfigJSON *string    `gorm:"type:longtext;column:gateway_config_snapshot"`
	PaidAt            *time.Time `gorm:"column:paid_at"`
	ReconciledAt      *time.Time `gorm:"column:reconciled_at"`
	CreatedAt         time.Time  `gorm:"not null;autoCreateTime;column:created_at"`
	UpdatedAt         time.Time  `gorm:"not null;autoUpdateTime;column:updated_at"`
}

func (RechargeModel) TableName() string {
	return "recharges"
}

type CardKeyModel struct {
	Key             string     `gorm:"primaryKey;type:varchar(128);column:card_key"`
	Amount          string     `gorm:"type:decimal(18,6);not null"`
	Status          string     `gorm:"type:varchar(32);not null;default:'enabled'"`
	MaxRedemptions  int        `gorm:"not null;default:1;column:max_redemptions"`
	RedeemedCount   int        `gorm:"not null;default:0;column:redeemed_count"`
	ExpireAt        *time.Time `gorm:"column:expire_at"`
	CreatedByUserID *uint      `gorm:"column:created_by_user_id"`
	CreatedAt       time.Time  `gorm:"not null;autoCreateTime;column:created_at"`
	UpdatedAt       time.Time  `gorm:"not null;autoUpdateTime;column:updated_at"`
}

func (CardKeyModel) TableName() string {
	return "card_keys"
}

type CardKeyRedemptionModel struct {
	ID            uint      `gorm:"primaryKey;autoIncrement"`
	CardKey       string    `gorm:"type:varchar(128);not null;column:card_key"`
	UserID        uint      `gorm:"not null;column:user_id"`
	TransactionID uint      `gorm:"not null;column:transaction_id"`
	RequestID     string    `gorm:"type:varchar(64);not null;default:'';column:request_id"`
	RedeemedAt    time.Time `gorm:"not null;autoCreateTime;column:redeemed_at"`
}

type DailyCheckinModel struct {
	ID                  uint      `gorm:"primaryKey;autoIncrement"`
	UserID              uint      `gorm:"not null;column:user_id"`
	BusinessDate        string    `gorm:"type:date;not null;column:business_date"`
	RewardAmount        string    `gorm:"type:decimal(18,6);not null;column:reward_amount"`
	WalletTransactionID *uint     `gorm:"column:wallet_transaction_id"`
	CheckedInAt         time.Time `gorm:"not null;column:checked_in_at"`
	CreatedAt           time.Time `gorm:"not null;autoCreateTime;column:created_at"`
}

func (DailyCheckinModel) TableName() string { return "daily_checkins" }

type LeaderboardSettlementModel struct {
	ID            uint      `gorm:"primaryKey;autoIncrement"`
	BusinessDate  string    `gorm:"type:date;not null;column:business_date"`
	PeriodStart   time.Time `gorm:"not null;column:period_start"`
	PeriodEnd     time.Time `gorm:"not null;column:period_end"`
	RulesSnapshot string    `gorm:"type:json;not null;column:rules_snapshot"`
	Status        string    `gorm:"type:varchar(32);not null"`
	SettledAt     time.Time `gorm:"not null;column:settled_at"`
	CreatedAt     time.Time `gorm:"not null;autoCreateTime;column:created_at"`
}

func (LeaderboardSettlementModel) TableName() string { return "leaderboard_settlements" }

type LeaderboardRewardModel struct {
	ID                  uint      `gorm:"primaryKey;autoIncrement"`
	SettlementID        uint      `gorm:"not null;column:settlement_id"`
	UserID              uint      `gorm:"not null;column:user_id"`
	Rank                int       `gorm:"not null;column:rank_no"`
	Score               int       `gorm:"not null;column:score"`
	RewardAmount        string    `gorm:"type:decimal(18,6);not null;column:reward_amount"`
	WalletTransactionID uint      `gorm:"not null;column:wallet_transaction_id"`
	CreatedAt           time.Time `gorm:"not null;autoCreateTime;column:created_at"`
}

func (LeaderboardRewardModel) TableName() string { return "leaderboard_rewards" }

func (CardKeyRedemptionModel) TableName() string {
	return "card_key_redemptions"
}

type ReferralRewardModel struct {
	ID                    uint       `gorm:"primaryKey;autoIncrement"`
	InviterUserID         uint       `gorm:"not null;column:inviter_user_id"`
	InviteeUserID         uint       `gorm:"not null;column:invitee_user_id"`
	InviteCode            string     `gorm:"type:varchar(64);not null;column:invite_code"`
	SourceTransactionID   uint       `gorm:"not null;column:source_transaction_id"`
	TransferTransactionID *uint      `gorm:"column:transfer_transaction_id"`
	SourceAmount          string     `gorm:"type:decimal(18,6);not null;column:source_amount"`
	RewardAmount          string     `gorm:"type:decimal(18,6);not null;column:reward_amount"`
	Status                string     `gorm:"type:varchar(32);not null;default:'available'"`
	TransferredAt         *time.Time `gorm:"column:transferred_at"`
	ExpiresAt             *time.Time `gorm:"column:expires_at"`
	CreatedAt             time.Time  `gorm:"not null;autoCreateTime;column:created_at"`
}

func (ReferralRewardModel) TableName() string {
	return "referral_rewards"
}

type BillingRepo struct {
	db                   *gorm.DB
	operationLogs        operationLogWriter
	hasGmailAllocations  bool
	hasICloudAllocations bool
}

func NewBillingRepo(db *gorm.DB) *BillingRepo {
	repo := &BillingRepo{
		db:            db,
		operationLogs: governanceinfra.NewOperationLogRepo(db),
	}
	if db != nil {
		repo.hasGmailAllocations = db.Migrator().HasTable("gmail_allocations")
		repo.hasICloudAllocations = db.Migrator().HasTable("icloud_allocations")
	}
	return repo
}

func (r *BillingRepo) withTx(ctx context.Context, fn func(context.Context, *gorm.DB) error) error {
	if tx, ok := platform.GormTxFromContext(ctx); ok {
		return fn(ctx, tx.WithContext(ctx))
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return fn(platform.WithGormTx(ctx, tx), tx)
	})
}

type operationLogWriter interface {
	CreateInTx(ctx context.Context, tx *gorm.DB, log *governancedomain.OperationLog) error
}

func (r *BillingRepo) GetOrCreateWalletSummary(ctx context.Context, userID uint) (*domain.WalletSummary, error) {
	wallet, err := r.getOrCreateWallet(ctx, r.db.WithContext(ctx), userID)
	if err != nil {
		return nil, err
	}
	summary, err := walletSummaryFromModel(wallet)
	if err != nil {
		return nil, err
	}
	if err := r.populateSupplierFulfillmentMetrics(ctx, r.db.WithContext(ctx), userID, summary); err != nil {
		return nil, err
	}
	return summary, nil
}

const supplierFulfillmentMetricsPrefix = `
SELECT COUNT(*) AS allocation_count,
       COALESCE(ROUND(
           SUM(CASE
               WHEN o.status NOT IN ('refunded', 'failed') AND (
                   (o.service_mode = 'code' AND EXISTS (
                       SELECT 1 FROM mailmatch_order_delivery_heads h WHERE h.order_id = o.id
                   )) OR
                   (o.service_mode = 'purchase' AND o.activated_at IS NOT NULL)
               ) THEN 1 ELSE 0
           END) * 100.0 / NULLIF(SUM(CASE
               WHEN (o.status NOT IN ('refunded', 'failed') AND (
                   (o.service_mode = 'code' AND EXISTS (
                       SELECT 1 FROM mailmatch_order_delivery_heads h WHERE h.order_id = o.id
                   )) OR
                   (o.service_mode = 'purchase' AND o.activated_at IS NOT NULL)
               )) OR
               o.status IN ('refunded', 'failed', 'closed') OR
               (o.receive_until IS NOT NULL AND o.receive_until <= ?)
               THEN 1 ELSE 0
           END), 0),
           1
       ), 0) AS fulfillment_success_rate
FROM (`

const supplierMicrosoftAllocationsSQL = `SELECT ma.order_no
    FROM microsoft_allocations ma
    JOIN email_resources er ON er.id = ma.resource_id AND er.type = 'microsoft'
    WHERE er.owner_user_id = ? AND ma.supply_scope = 'public'`

const supplierDomainAllocationsSQL = `SELECT da.order_no
    FROM domain_allocations da
    JOIN email_resources er ON er.id = da.resource_id AND er.type = 'domain'
    WHERE er.owner_user_id = ? AND da.supply_scope = 'public'`

const supplierGmailAllocationsSQL = `SELECT ga.order_no
    FROM gmail_allocations ga
    JOIN email_resources er ON er.id = ga.resource_id AND er.type = 'gmail'
    WHERE ga.source = 'local' AND er.owner_user_id = ? AND ga.supply_scope = 'public'`

const supplierICloudAllocationsSQL = `SELECT ia.order_no
    FROM icloud_allocations ia
    JOIN email_resources er ON er.id = ia.resource_id AND er.type = 'icloud'
    WHERE er.owner_user_id = ? AND ia.supply_scope = 'public'`

const supplierFulfillmentMetricsSuffix = `) allocations
JOIN orders o ON o.order_no = allocations.order_no
WHERE o.debit_tx_id IS NOT NULL
  AND o.order_no NOT LIKE 'HIST-%'`

func (r *BillingRepo) populateSupplierFulfillmentMetrics(ctx context.Context, db *gorm.DB, userID uint, summary *domain.WalletSummary) error {
	var metrics struct {
		AllocationCount        int64   `gorm:"column:allocation_count"`
		FulfillmentSuccessRate float64 `gorm:"column:fulfillment_success_rate"`
	}
	allocationQueries := []string{supplierMicrosoftAllocationsSQL, supplierDomainAllocationsSQL}
	args := []any{time.Now().UTC(), userID, userID}
	if r.hasGmailAllocations {
		allocationQueries = append(allocationQueries, supplierGmailAllocationsSQL)
		args = append(args, userID)
	}
	if r.hasICloudAllocations {
		allocationQueries = append(allocationQueries, supplierICloudAllocationsSQL)
		args = append(args, userID)
	}
	query := supplierFulfillmentMetricsPrefix + strings.Join(allocationQueries, "\nUNION ALL\n") + supplierFulfillmentMetricsSuffix
	if err := db.WithContext(ctx).Raw(query, args...).Scan(&metrics).Error; err != nil {
		return fmt.Errorf("load supplier fulfillment metrics: %w", err)
	}
	summary.SupplierAllocationCount = metrics.AllocationCount
	summary.SupplierFulfillmentSuccessRate = metrics.FulfillmentSuccessRate
	return nil
}

func walletSummaryFromModel(wallet WalletModel) (*domain.WalletSummary, error) {
	totalRecharged, err := normalizeDBMoney(wallet.TotalRecharged)
	if err != nil {
		return nil, err
	}
	normalizedSpend, err := normalizeDBMoney(wallet.TotalSpend)
	if err != nil {
		return nil, err
	}
	return &domain.WalletSummary{
		Wallet:          walletModelToDomain(wallet),
		TotalRecharged:  totalRecharged,
		HistoricalSpend: normalizedSpend,
		OrderCount:      wallet.SpendCount,
	}, nil
}

func (r *BillingRepo) LockConsumerWallet(ctx context.Context, userID uint) error {
	if userID == 0 {
		return domain.ErrInvalidFilter
	}
	return r.withTx(ctx, func(ctx context.Context, tx *gorm.DB) error {
		_, err := r.lockWalletInTx(ctx, tx, userID)
		return err
	})
}

func (r *BillingRepo) GetReferralSummary(ctx context.Context, userID uint) (*domain.ReferralSummary, error) {
	if userID == 0 {
		return nil, domain.ErrInvalidFilter
	}

	var inviteCount int64
	if err := r.db.WithContext(ctx).
		Table("invite_uses AS iu").
		Joins("JOIN invites AS i ON i.code = iu.invite_code").
		Where("i.invite_kind = ? AND i.referral_owner_user_id = ?", "referral", userID).
		Count(&inviteCount).Error; err != nil {
		return nil, fmt.Errorf("count referral invites: %w", err)
	}

	var totalEarned string
	if err := r.db.WithContext(ctx).
		Model(&ReferralRewardModel{}).
		Select("COALESCE(SUM(reward_amount), 0)").
		Where("inviter_user_id = ?", userID).
		Scan(&totalEarned).Error; err != nil {
		return nil, fmt.Errorf("sum referral rewards: %w", err)
	}
	totalEarned, err := normalizeDBMoney(totalEarned)
	if err != nil {
		return nil, err
	}
	var pendingRewards string
	if err := r.db.WithContext(ctx).
		Model(&ReferralRewardModel{}).
		Select("COALESCE(SUM(reward_amount), 0)").
		Where("inviter_user_id = ? AND status = ? AND (expires_at IS NULL OR expires_at > ?)", userID, "available", time.Now().UTC()).
		Scan(&pendingRewards).Error; err != nil {
		return nil, fmt.Errorf("sum pending referral rewards: %w", err)
	}
	pendingRewards, err = normalizeDBMoney(pendingRewards)
	if err != nil {
		return nil, err
	}

	return &domain.ReferralSummary{
		InviteCount:    inviteCount,
		PendingRewards: pendingRewards,
		TotalEarned:    totalEarned,
	}, nil
}

func (r *BillingRepo) ListTransactions(ctx context.Context, filter billingapp.TransactionListFilter, afterID uint, limit int) ([]domain.Transaction, *uint, error) {
	var models []WalletTransactionModel
	query := r.db.WithContext(ctx).Model(&WalletTransactionModel{})
	query = applyTransactionFilter(query, filter)
	if afterID > 0 {
		query = query.Where("id < ?", afterID)
	}
	if err := query.Order("id DESC").Limit(limit + 1).Find(&models).Error; err != nil {
		return nil, nil, fmt.Errorf("list wallet transactions: %w", err)
	}
	var nextAfterID *uint
	if len(models) > limit {
		models = models[:limit]
		next := models[len(models)-1].ID
		nextAfterID = &next
	}
	items := make([]domain.Transaction, len(models))
	for i := range models {
		items[i] = transactionModelToDomain(models[i])
	}
	return items, nextAfterID, nil
}

func (r *BillingRepo) ListRecharges(ctx context.Context, filter billingapp.RechargeListFilter, offset, limit int) ([]domain.Recharge, error) {
	var models []RechargeModel
	query := r.db.WithContext(ctx).Model(&RechargeModel{})
	query = applyRechargeFilter(query, filter)
	if err := query.Order("created_at DESC, id DESC").Offset(offset).Limit(limit).Find(&models).Error; err != nil {
		return nil, fmt.Errorf("list recharges: %w", err)
	}
	items := make([]domain.Recharge, len(models))
	for i := range models {
		items[i] = rechargeModelToDomain(models[i])
	}
	return items, nil
}

func (r *BillingRepo) CountRecharges(ctx context.Context, filter billingapp.RechargeListFilter) (int64, error) {
	var total int64
	query := r.db.WithContext(ctx).Model(&RechargeModel{})
	query = applyRechargeFilter(query, filter)
	if err := query.Count(&total).Error; err != nil {
		return 0, fmt.Errorf("count recharges: %w", err)
	}
	return total, nil
}

func (r *BillingRepo) CreateRecharge(ctx context.Context, command billingapp.CreateRechargeCommand) (*domain.Recharge, error) {
	if command.MaxPendingOrders <= 0 {
		return nil, domain.ErrRechargeConfigUnavailable
	}
	if _, err := r.getOrCreateWallet(ctx, r.db, command.Recharge.UserID); err != nil {
		return nil, err
	}
	var result domain.Recharge
	snapshot, err := json.Marshal(command.GatewayConfig)
	if err != nil {
		return nil, fmt.Errorf("encode recharge gateway config: %w", err)
	}
	err = r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var wallet WalletModel
		if err := tx.WithContext(ctx).Select("user_id").Clauses(clause.Locking{Strength: "UPDATE"}).First(&wallet, "user_id = ?", command.Recharge.UserID).Error; err != nil {
			return fmt.Errorf("lock wallet for recharge: %w", err)
		}
		response, replayed, err := r.withIdempotencyInTxAliases(ctx, tx, command.Recharge.UserID, "recharges.create", command.IdempotencyKey, command.RequestFingerprint, command.LegacyRequestFingerprints, func(writeTx *gorm.DB) ([]byte, error) {
			if command.RequireIdempotencyReplay {
				return nil, domain.ErrRechargePaymentBelowMinimum
			}
			var pending int64
			if err := writeTx.WithContext(ctx).Model(&RechargeModel{}).
				Where("user_id = ? AND status IN ? AND created_at > ?", command.Recharge.UserID, pendingRechargeStatuses(), command.Recharge.CreatedAt.Add(-domain.RechargeReconciliationWindow())).
				Count(&pending).Error; err != nil {
				return nil, fmt.Errorf("count pending recharges: %w", err)
			}
			if pending >= int64(command.MaxPendingOrders) {
				return nil, domain.ErrRechargePending
			}
			model := rechargeModelFromDomain(command.Recharge)
			encoded := string(snapshot)
			model.GatewayConfigJSON = &encoded
			if err := writeTx.WithContext(ctx).Create(&model).Error; err != nil {
				return nil, fmt.Errorf("create recharge: %w", err)
			}
			result = rechargeModelToDomain(model)
			return json.Marshal(result)
		})
		if err != nil {
			return err
		}
		if replayed {
			if err := json.Unmarshal(response, &result); err != nil {
				return fmt.Errorf("decode idempotent recharge: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return r.GetRechargeByNo(ctx, result.RechargeNo)
}

func (r *BillingRepo) GetRechargeByNo(ctx context.Context, rechargeNo string) (*domain.Recharge, error) {
	var model RechargeModel
	if err := r.db.WithContext(ctx).First(&model, "recharge_no = ?", rechargeNo).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, domain.ErrRechargeNotFound
		}
		return nil, fmt.Errorf("find recharge: %w", err)
	}
	recharge := rechargeModelToDomain(model)
	return &recharge, nil
}

func (r *BillingRepo) MarkRechargeCallback(ctx context.Context, rechargeNo string, callbackAt time.Time) (bool, error) {
	// Clear the throttle for this one-time wake-up; keep any active lease so an
	// in-flight query is coalesced instead of issuing a concurrent provider call.
	result := r.db.WithContext(ctx).
		Model(&RechargeModel{}).
		Where("recharge_no = ? AND status = ? AND created_at > ?", rechargeNo, domain.RechargeStatusPaying, callbackAt.Add(-domain.RechargeReconciliationWindow())).
		Updates(map[string]any{
			"status":          string(domain.RechargeStatusCallback),
			"last_queried_at": nil,
			"updated_at":      callbackAt,
		})
	if result.Error != nil {
		return false, fmt.Errorf("mark recharge callback: %w", result.Error)
	}
	return result.RowsAffected > 0, nil
}

func (r *BillingRepo) ListDueRecharges(ctx context.Context, now time.Time, limit int) ([]domain.Recharge, error) {
	var models []RechargeModel
	if err := r.db.WithContext(ctx).
		Where(
			"created_at > ? AND (status IN ? OR (status = ? AND ((payment_method = ? AND created_at <= ?) OR ((payment_method IS NULL OR payment_method <> ?) AND created_at <= ?)))) AND (query_lease_until IS NULL OR query_lease_until <= ?) AND (last_queried_at IS NULL OR (query_attempts < ? AND last_queried_at <= ?) OR (query_attempts >= ? AND last_queried_at <= ?))",
			now.Add(-domain.RechargeReconciliationWindow()),
			[]string{string(domain.RechargeStatusCallback), string(domain.RechargeStatusReconciled)},
			domain.RechargeStatusPaying,
			domain.RechargePaymentMethodEpusdtUSDTTron,
			now.Add(-domain.RechargeEpusdtFallbackDelay),
			domain.RechargePaymentMethodEpusdtUSDTTron,
			now.Add(-domain.RechargeCallbackFallbackDelay),
			now,
			domain.RechargeFastQueryLimit,
			now.Add(-domain.RechargeFastQueryInterval),
			domain.RechargeFastQueryLimit,
			now.Add(-domain.RechargeSlowQueryInterval),
		).
		Order("COALESCE(last_queried_at, created_at) ASC, id ASC").
		Limit(limit).
		Find(&models).Error; err != nil {
		return nil, fmt.Errorf("list due recharges: %w", err)
	}
	items := make([]domain.Recharge, len(models))
	for i := range models {
		items[i] = rechargeModelToDomain(models[i])
	}
	return items, nil
}

func (r *BillingRepo) ExpirePendingRecharges(ctx context.Context, createdBefore, now time.Time) (int64, error) {
	result := r.db.WithContext(ctx).
		Model(&RechargeModel{}).
		Where("status IN ? AND created_at <= ?", pendingRechargeStatuses(), createdBefore).
		Updates(map[string]any{
			"status":            string(domain.RechargeStatusFailed),
			"failure_reason":    "query_timeout",
			"query_lease_until": nil,
			"reconciled_at":     &now,
			"updated_at":        now,
		})
	if result.Error != nil {
		return 0, fmt.Errorf("expire pending recharges: %w", result.Error)
	}
	return result.RowsAffected, nil
}

func (r *BillingRepo) ClaimRechargeQuery(ctx context.Context, rechargeNo string, claimedAt, leaseUntil time.Time) (*domain.Recharge, billingapp.RechargeConfig, int, bool, error) {
	var model RechargeModel
	claimed := false
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.WithContext(ctx).
			Model(&RechargeModel{}).
			Where(
				"recharge_no = ? AND created_at > ? AND (status IN ? OR (status = ? AND ((payment_method = ? AND created_at <= ?) OR ((payment_method IS NULL OR payment_method <> ?) AND created_at <= ?)))) AND (query_lease_until IS NULL OR query_lease_until <= ?) AND (last_queried_at IS NULL OR (query_attempts < ? AND last_queried_at <= ?) OR (query_attempts >= ? AND last_queried_at <= ?))",
				rechargeNo,
				claimedAt.Add(-domain.RechargeReconciliationWindow()),
				[]string{string(domain.RechargeStatusCallback), string(domain.RechargeStatusReconciled)},
				domain.RechargeStatusPaying,
				domain.RechargePaymentMethodEpusdtUSDTTron,
				claimedAt.Add(-domain.RechargeEpusdtFallbackDelay),
				domain.RechargePaymentMethodEpusdtUSDTTron,
				claimedAt.Add(-domain.RechargeCallbackFallbackDelay),
				claimedAt,
				domain.RechargeFastQueryLimit,
				claimedAt.Add(-domain.RechargeFastQueryInterval),
				domain.RechargeFastQueryLimit,
				claimedAt.Add(-domain.RechargeSlowQueryInterval),
			).
			Updates(map[string]any{
				"query_generation":  gorm.Expr("query_generation + 1"),
				"query_lease_until": &leaseUntil,
			})
		if result.Error != nil {
			return fmt.Errorf("claim recharge query: %w", result.Error)
		}
		if result.RowsAffected == 0 {
			return nil
		}
		claimed = true
		return tx.WithContext(ctx).First(&model, "recharge_no = ?", rechargeNo).Error
	})
	if err != nil {
		return nil, billingapp.RechargeConfig{}, 0, false, err
	}
	if !claimed {
		return nil, billingapp.RechargeConfig{}, 0, false, nil
	}
	if model.GatewayConfigJSON == nil {
		return nil, billingapp.RechargeConfig{}, 0, false, r.quarantineRechargeSnapshot(
			ctx, rechargeNo, model.QueryGeneration, claimedAt, errors.New("recharge gateway config snapshot is missing"),
		)
	}
	rawSnapshot := strings.TrimSpace(*model.GatewayConfigJSON)
	if rawSnapshot == "" || rawSnapshot == "null" || rawSnapshot == "{}" {
		return nil, billingapp.RechargeConfig{}, 0, false, r.quarantineRechargeSnapshot(
			ctx, rechargeNo, model.QueryGeneration, claimedAt, errors.New("recharge gateway config snapshot is empty"),
		)
	}
	var config billingapp.RechargeConfig
	if err := json.Unmarshal([]byte(rawSnapshot), &config); err != nil {
		return nil, billingapp.RechargeConfig{}, 0, false, r.quarantineRechargeSnapshot(
			ctx, rechargeNo, model.QueryGeneration, claimedAt, fmt.Errorf("decode recharge gateway config: %w", err),
		)
	}
	if strings.TrimSpace(config.GatewayURL) == "" && strings.TrimSpace(config.EpusdtGatewayURL) == "" {
		return nil, billingapp.RechargeConfig{}, 0, false, r.quarantineRechargeSnapshot(
			ctx, rechargeNo, model.QueryGeneration, claimedAt, errors.New("recharge gateway config snapshot has no gateway"),
		)
	}
	recharge := rechargeModelToDomain(model)
	return &recharge, config, model.QueryGeneration, true, nil
}

func (r *BillingRepo) quarantineRechargeSnapshot(ctx context.Context, rechargeNo string, generation int, failedAt time.Time, snapshotErr error) error {
	if err := r.FailRecharge(ctx, rechargeNo, generation, "migration_missing_gateway_snapshot", failedAt); err != nil {
		return errors.Join(snapshotErr, fmt.Errorf("quarantine recharge snapshot: %w", err))
	}
	return snapshotErr
}

func (r *BillingRepo) RecordRechargeQuery(ctx context.Context, rechargeNo string, generation int, queriedAt time.Time) error {
	if err := r.db.WithContext(ctx).
		Model(&RechargeModel{}).
		Where("recharge_no = ? AND query_generation = ? AND status IN ?", rechargeNo, generation, pendingRechargeStatuses()).
		Updates(map[string]any{
			"query_attempts":    gorm.Expr("query_attempts + 1"),
			"last_queried_at":   &queriedAt,
			"query_lease_until": nil,
			"updated_at":        queriedAt,
		}).Error; err != nil {
		return fmt.Errorf("record recharge query: %w", err)
	}
	return nil
}

func (r *BillingRepo) FailRecharge(ctx context.Context, rechargeNo string, generation int, reason string, failedAt time.Time) error {
	if err := r.db.WithContext(ctx).
		Model(&RechargeModel{}).
		Where("recharge_no = ? AND query_generation = ? AND status IN ?", rechargeNo, generation, pendingRechargeStatuses()).
		Updates(map[string]any{
			"status":            string(domain.RechargeStatusFailed),
			"failure_reason":    reason,
			"query_attempts":    gorm.Expr("query_attempts + 1"),
			"last_queried_at":   &failedAt,
			"query_lease_until": nil,
			"reconciled_at":     &failedAt,
			"updated_at":        failedAt,
		}).Error; err != nil {
		return fmt.Errorf("fail recharge: %w", err)
	}
	return nil
}

func (r *BillingRepo) CreditRecharge(ctx context.Context, command billingapp.CreditRechargeCommand) (*domain.Recharge, error) {
	var credited domain.Recharge
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var model RechargeModel
		if err := tx.WithContext(ctx).
			Clauses(clause.Locking{Strength: "UPDATE"}).
			First(&model, "recharge_no = ?", command.RechargeNo).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return domain.ErrRechargeNotFound
			}
			return fmt.Errorf("lock recharge: %w", err)
		}
		if domain.RechargeStatus(model.Status) == domain.RechargeStatusCredited {
			credited = rechargeModelToDomain(model)
			return nil
		}
		queriedInTime := command.QueriedAt.Before(model.CreatedAt.Add(domain.RechargeReconciliationWindow()))
		verifiedFailureRace := domain.RechargeStatus(model.Status) == domain.RechargeStatusFailed && queriedInTime
		if (!domain.IsPendingRechargeStatus(domain.RechargeStatus(model.Status)) && !verifiedFailureRace) ||
			!queriedInTime || strings.TrimSpace(command.GatewayTradeNo) == "" {
			return domain.ErrRechargeExpired
		}

		relation, err := r.findReferralRelationInTx(ctx, tx, model.UserID)
		if err != nil {
			return err
		}
		wallets, err := r.lockWalletsInTx(ctx, tx, model.UserID, relation.InviterUserID)
		if err != nil {
			return err
		}
		transaction, err := r.createConsumerTransaction(ctx, tx, wallets[model.UserID], consumerTransactionRequest{
			UserID:          model.UserID,
			Amount:          model.RechargeQuota,
			Direction:       domain.TransactionDirectionIn,
			TransactionType: domain.TransactionTypeRecharge,
			BizType:         "recharge_" + strings.TrimSpace(model.PaymentMethod),
			BizID:           model.RechargeNo,
			IdempotencyKey:  "recharge:" + model.RechargeNo,
		})
		if err != nil {
			return err
		}
		if err := r.settleReferralRewardInTx(ctx, tx, relation, transaction.Transaction); err != nil {
			return err
		}
		if err := resetBalanceWarningsInTx(ctx, tx, model.UserID); err != nil {
			return err
		}

		tradeNo := strings.TrimSpace(command.GatewayTradeNo)
		paidAt := command.PaidAt
		if paidAt == nil {
			paidAt = &command.QueriedAt
		}
		settledAt := command.QueriedAt
		if model.UpdatedAt.After(settledAt) {
			settledAt = model.UpdatedAt
		}
		updates := map[string]any{
			"status":            string(domain.RechargeStatusCredited),
			"gateway_trade_no":  tradeNo,
			"failure_reason":    "",
			"query_attempts":    gorm.Expr("query_attempts + 1"),
			"last_queried_at":   &settledAt,
			"query_lease_until": nil,
			"paid_at":           paidAt,
			"reconciled_at":     &settledAt,
			"updated_at":        settledAt,
		}
		if err := tx.WithContext(ctx).Model(&RechargeModel{}).Where("id = ?", model.ID).Updates(updates).Error; err != nil {
			if isDuplicateKeyError(err) {
				return domain.ErrRechargeQueryMismatch
			}
			return fmt.Errorf("credit recharge: %w", err)
		}
		if err := tx.WithContext(ctx).First(&model, model.ID).Error; err != nil {
			return fmt.Errorf("reload credited recharge: %w", err)
		}
		credited = rechargeModelToDomain(model)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &credited, nil
}

func pendingRechargeStatuses() []string {
	return []string{
		string(domain.RechargeStatusPaying),
		string(domain.RechargeStatusCallback),
		string(domain.RechargeStatusReconciled),
	}
}

func (r *BillingRepo) RedeemCard(ctx context.Context, req billingapp.RedeemCardCommand) (*billingapp.RedeemCardResult, error) {
	var result billingapp.RedeemCardResult
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		response, replayed, err := r.withIdempotencyInTx(ctx, tx, req.UserID, "cards.redeem", req.IdempotencyKey, req.RequestFingerprint, func(writeTx *gorm.DB) ([]byte, error) {
			created, err := r.redeemCardInTx(ctx, writeTx, req)
			if err != nil {
				return nil, err
			}
			result = *created
			return json.Marshal(created)
		})
		if err != nil {
			return err
		}
		if replayed {
			if err := json.Unmarshal(response, &result); err != nil {
				return fmt.Errorf("decode idempotent card redemption: %w", err)
			}
			result.Replayed = true
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &result, nil
}

func (r *BillingRepo) TransferReferralRewards(ctx context.Context, req billingapp.TransferReferralRewardsCommand) (*billingapp.TransferReferralRewardsResult, error) {
	var result billingapp.TransferReferralRewardsResult
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		response, replayed, err := r.withIdempotencyInTx(ctx, tx, req.UserID, "referrals.transfer", req.IdempotencyKey, req.RequestFingerprint, func(writeTx *gorm.DB) ([]byte, error) {
			created, err := r.transferReferralRewardsInTx(ctx, writeTx, req)
			if err != nil {
				return nil, err
			}
			result = *created
			return json.Marshal(created)
		})
		if err != nil {
			return err
		}
		if replayed {
			if err := json.Unmarshal(response, &result); err != nil {
				return fmt.Errorf("decode idempotent referral transfer: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &result, nil
}

func (r *BillingRepo) AdjustConsumerBalance(ctx context.Context, req billingapp.AdjustConsumerBalanceCommand) (*billingapp.AdjustBalanceResult, error) {
	var result billingapp.AdjustBalanceResult
	err := r.withTx(ctx, func(txCtx context.Context, tx *gorm.DB) error {
		wallet, err := r.lockWalletInTx(txCtx, tx, req.UserID)
		if err != nil {
			return err
		}
		response, replayed, err := r.withIdempotencyInTx(txCtx, tx, req.UserID, "wallet.adjust", req.IdempotencyKey, req.RequestFingerprint, func(writeTx *gorm.DB) ([]byte, error) {
			created, err := r.adjustConsumerBalanceInTx(txCtx, writeTx, wallet, req)
			if err != nil {
				return nil, err
			}
			if err := r.createOperationLogInTx(txCtx, writeTx, req.OperationLog); err != nil {
				return nil, err
			}
			result = *created
			return json.Marshal(created)
		})
		if err != nil {
			return err
		}
		if replayed {
			if err := json.Unmarshal(response, &result); err != nil {
				return fmt.Errorf("decode idempotent wallet adjustment: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &result, nil
}

func (r *BillingRepo) RecordHistoricalZeroDebit(ctx context.Context, req billingapp.AdjustConsumerBalanceCommand) (*domain.Transaction, error) {
	db := r.db
	if tx, ok := platform.GormTxFromContext(ctx); ok {
		db = tx
	}
	transaction := WalletTransactionModel{
		TransactionNo: nextTransactionNo(), UserID: req.UserID,
		TransactionType: string(domain.TransactionTypeDebit),
		BalanceBucket:   string(domain.BalanceBucketConsumer),
		Direction:       string(domain.TransactionDirectionOut),
		Amount:          "0.00", BalanceBefore: "0.00", BalanceAfter: "0.00",
		BizType: "historical_order", BizID: trimBizID(req.Reason),
		IdempotencyKey: req.IdempotencyKey, RequestID: req.RequestID, CreatedAt: req.Now,
	}
	if err := db.WithContext(ctx).Create(&transaction).Error; err != nil {
		return nil, fmt.Errorf("create historical zero debit: %w", err)
	}
	result := transactionModelToDomain(transaction)
	return &result, nil
}

func (r *BillingRepo) ClaimDailyCheckin(ctx context.Context, command billingapp.DailyCheckinCommand) (*billingapp.DailyCheckinResult, error) {
	var result billingapp.DailyCheckinResult
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		checkin := DailyCheckinModel{UserID: command.UserID, BusinessDate: command.BusinessDate, RewardAmount: command.RewardAmount, CheckedInAt: command.CheckedInAt}
		created := tx.WithContext(ctx).Create(&checkin)
		if errors.Is(created.Error, gorm.ErrDuplicatedKey) {
			if err := tx.WithContext(ctx).Where("user_id = ? AND business_date = ?", command.UserID, command.BusinessDate).First(&checkin).Error; err != nil {
				return fmt.Errorf("load daily check-in: %w", err)
			}
			wallet, err := r.lockWalletInTx(ctx, tx, command.UserID)
			if err != nil {
				return err
			}
			result = checkinResult(checkin, wallet.ConsumerBalance, false)
			return nil
		}
		if created.Error != nil {
			return fmt.Errorf("create daily check-in: %w", created.Error)
		}

		wallet, err := r.lockWalletInTx(ctx, tx, command.UserID)
		if err != nil {
			return err
		}
		if command.RewardAmount != "0.00" {
			credited, err := r.createConsumerTransaction(ctx, tx, wallet, consumerTransactionRequest{
				UserID: command.UserID, Amount: command.RewardAmount, Direction: domain.TransactionDirectionIn,
				TransactionType: domain.TransactionTypeCredit, BizType: "daily_checkin", BizID: command.BusinessDate,
				IdempotencyKey: command.IdempotencyKey, RequestID: command.RequestID,
			})
			if err != nil {
				return err
			}
			checkin.WalletTransactionID = &credited.Transaction.ID
			if err := tx.WithContext(ctx).Model(&DailyCheckinModel{}).Where("id = ?", checkin.ID).Update("wallet_transaction_id", credited.Transaction.ID).Error; err != nil {
				return fmt.Errorf("link daily check-in transaction: %w", err)
			}
		}
		result = checkinResult(checkin, wallet.ConsumerBalance, true)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &result, nil
}

func checkinResult(model DailyCheckinModel, balance string, first bool) billingapp.DailyCheckinResult {
	return billingapp.DailyCheckinResult{
		BusinessDate: model.BusinessDate, FirstClaim: first, RewardAmount: normalizeMoneyString(model.RewardAmount),
		CheckedInAt: model.CheckedInAt, ConsumerBalance: normalizeMoneyString(balance),
	}
}

func (r *BillingRepo) SettleLeaderboard(ctx context.Context, command billingapp.LeaderboardSettlementCommand) (*billingapp.LeaderboardSettlementResult, error) {
	result := billingapp.LeaderboardSettlementResult{BusinessDate: command.BusinessDate}
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		settlement := LeaderboardSettlementModel{
			BusinessDate: command.BusinessDate, PeriodStart: command.PeriodStart, PeriodEnd: command.PeriodEnd,
			RulesSnapshot: command.RulesJSON, Status: "completed", SettledAt: command.SettledAt,
		}
		created := tx.WithContext(ctx).Create(&settlement)
		if errors.Is(created.Error, gorm.ErrDuplicatedKey) {
			return nil
		}
		if created.Error != nil {
			return fmt.Errorf("create leaderboard settlement: %w", created.Error)
		}
		result.Created = true

		maxRank := 0
		for _, rule := range command.Rules {
			maxRank = max(maxRank, rule.RankTo)
		}
		rows, err := successranking.Query(ctx, tx, &command.PeriodStart, &command.PeriodEnd, maxRank)
		if err != nil {
			return fmt.Errorf("rank successful orders: %w", err)
		}
		for i, row := range rows {
			if amount := leaderboardRewardForRank(command.Rules, i+1); amount != "" {
				result.Winners = append(result.Winners, billingapp.LeaderboardWinner{UserID: row.UserID, Rank: i + 1, Score: row.Score, Amount: amount})
			}
		}
		ids := make([]uint, len(result.Winners))
		for i, winner := range result.Winners {
			ids[i] = winner.UserID
		}
		wallets, err := r.lockWalletsInTx(ctx, tx, ids...)
		if err != nil {
			return err
		}
		for _, winner := range result.Winners {
			credited, err := r.createConsumerTransaction(ctx, tx, wallets[winner.UserID], consumerTransactionRequest{
				UserID: winner.UserID, Amount: winner.Amount, Direction: domain.TransactionDirectionIn,
				TransactionType: domain.TransactionTypeCredit, BizType: "leaderboard_reward",
				BizID:          fmt.Sprintf("%s:rank:%d", command.BusinessDate, winner.Rank),
				IdempotencyKey: fmt.Sprintf("leaderboard_reward:%s:%d", command.BusinessDate, winner.UserID),
			})
			if err != nil {
				return err
			}
			reward := LeaderboardRewardModel{
				SettlementID: settlement.ID, UserID: winner.UserID, Rank: winner.Rank, Score: winner.Score,
				RewardAmount: winner.Amount, WalletTransactionID: credited.Transaction.ID,
			}
			if err := tx.WithContext(ctx).Create(&reward).Error; err != nil {
				return fmt.Errorf("create leaderboard reward: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &result, nil
}

func (r *BillingRepo) LatestLeaderboardSettlementDate(ctx context.Context) (string, bool, error) {
	var latest sql.NullTime
	if err := r.db.WithContext(ctx).Model(&LeaderboardSettlementModel{}).Select("MAX(business_date)").Scan(&latest).Error; err != nil {
		return "", false, fmt.Errorf("load latest leaderboard settlement: %w", err)
	}
	if !latest.Valid {
		return "", false, nil
	}
	return latest.Time.Format(time.DateOnly), true, nil
}

func leaderboardRewardForRank(rules []runtimeconfig.LeaderboardRewardRule, rank int) string {
	for _, rule := range rules {
		if rank >= rule.RankFrom && rank <= rule.RankTo {
			return rule.Amount
		}
	}
	return ""
}

func (r *BillingRepo) ListCards(ctx context.Context, filter billingapp.CardListFilter, offset, limit int) ([]domain.CardKey, error) {
	var models []CardKeyModel
	query := r.db.WithContext(ctx).Model(&CardKeyModel{})
	query = applyCardFilter(query, filter)
	if err := query.Order("created_at DESC, card_key DESC").Offset(offset).Limit(limit).Find(&models).Error; err != nil {
		return nil, fmt.Errorf("list card keys: %w", err)
	}
	items := make([]domain.CardKey, len(models))
	for i := range models {
		items[i] = cardModelToDomain(models[i])
	}
	return items, nil
}

func (r *BillingRepo) CountCards(ctx context.Context, filter billingapp.CardListFilter) (int64, error) {
	var total int64
	query := r.db.WithContext(ctx).Model(&CardKeyModel{})
	query = applyCardFilter(query, filter)
	if err := query.Count(&total).Error; err != nil {
		return 0, fmt.Errorf("count card keys: %w", err)
	}
	return total, nil
}

func (r *BillingRepo) CreateCards(ctx context.Context, req billingapp.CreateCardsCommand) ([]domain.CardKey, error) {
	var created []domain.CardKey
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		response, replayed, err := r.withIdempotencyInTx(ctx, tx, req.OwnerUserID, "cards.create", req.IdempotencyKey, req.RequestFingerprint, func(writeTx *gorm.DB) ([]byte, error) {
			models := make([]CardKeyModel, 0, len(req.Cards))
			for _, card := range req.Cards {
				models = append(models, cardModelFromDomain(card))
			}
			if err := writeTx.WithContext(ctx).Create(&models).Error; err != nil {
				if isDuplicateKeyError(err) {
					return nil, domain.ErrDuplicateCardKey
				}
				return nil, fmt.Errorf("create card keys: %w", err)
			}
			if err := r.createOperationLogInTx(ctx, writeTx, req.OperationLog); err != nil {
				return nil, err
			}
			created = make([]domain.CardKey, len(models))
			for i := range models {
				created[i] = cardModelToDomain(models[i])
			}
			return json.Marshal(created)
		})
		if err != nil {
			return err
		}
		if replayed {
			if err := json.Unmarshal(response, &created); err != nil {
				return fmt.Errorf("decode idempotent card creation: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return created, nil
}

func (r *BillingRepo) UpdateCard(ctx context.Context, req billingapp.UpdateCardCommand) (*domain.CardKey, error) {
	updates := map[string]any{}
	if req.Status != nil {
		updates["status"] = string(*req.Status)
	}
	if req.ExpireAtSet {
		updates["expire_at"] = req.ExpireAt
	}
	if req.MaxRedemptions != nil {
		updates["max_redemptions"] = *req.MaxRedemptions
	}
	if len(updates) == 0 {
		var model CardKeyModel
		if err := r.db.WithContext(ctx).First(&model, "card_key = ?", req.CardKey).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, domain.ErrCardNotFound
			}
			return nil, fmt.Errorf("find card key: %w", err)
		}
		card := cardModelToDomain(model)
		return &card, nil
	}
	var model CardKeyModel
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).First(&model, "card_key = ?", req.CardKey).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return domain.ErrCardNotFound
			}
			return fmt.Errorf("lock card key: %w", err)
		}
		if req.MaxRedemptions != nil && *req.MaxRedemptions < model.RedeemedCount {
			return domain.ErrInvalidCardKey
		}
		if err := tx.WithContext(ctx).Model(&CardKeyModel{}).Where("card_key = ?", req.CardKey).Updates(updates).Error; err != nil {
			return fmt.Errorf("update card key: %w", err)
		}
		if err := tx.WithContext(ctx).First(&model, "card_key = ?", req.CardKey).Error; err != nil {
			return fmt.Errorf("reload card key: %w", err)
		}
		return r.createOperationLogInTx(ctx, tx, req.OperationLog)
	})
	if err != nil {
		return nil, err
	}
	card := cardModelToDomain(model)
	return &card, nil
}

func (r *BillingRepo) withIdempotencyInTx(
	ctx context.Context,
	tx *gorm.DB,
	ownerUserID uint,
	operation string,
	idempotencyKey string,
	fingerprint string,
	run func(*gorm.DB) ([]byte, error),
) ([]byte, bool, error) {
	return r.withIdempotencyInTxAliases(ctx, tx, ownerUserID, operation, idempotencyKey, fingerprint, nil, run)
}

func (r *BillingRepo) withIdempotencyInTxAliases(
	ctx context.Context,
	tx *gorm.DB,
	ownerUserID uint,
	operation string,
	idempotencyKey string,
	fingerprint string,
	legacyFingerprints []string,
	run func(*gorm.DB) ([]byte, error),
) ([]byte, bool, error) {
	if strings.TrimSpace(idempotencyKey) == "" || strings.TrimSpace(fingerprint) == "" {
		return nil, false, domain.ErrIdempotencyRequired
	}
	model := IdempotencyKeyModel{
		OwnerUserID:        ownerUserID,
		IdempotencyKey:     idempotencyKey,
		Operation:          operation,
		RequestFingerprint: fingerprint,
		Status:             "processing",
	}
	created := tx.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&model)
	if created.Error != nil {
		return nil, false, fmt.Errorf("create idempotency key: %w", created.Error)
	}

	var stored IdempotencyKeyModel
	if err := tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("owner_user_id = ? AND idempotency_key = ? AND operation = ?", ownerUserID, idempotencyKey, operation).
		First(&stored).Error; err != nil {
		return nil, false, fmt.Errorf("lock idempotency key: %w", err)
	}
	fingerprintMatches := stored.RequestFingerprint == fingerprint
	if !fingerprintMatches {
		for _, legacy := range legacyFingerprints {
			if stored.RequestFingerprint == legacy {
				fingerprintMatches = true
				break
			}
		}
	}
	if !fingerprintMatches {
		return nil, false, domain.ErrIdempotencyConflict
	}
	if stored.Status == "succeeded" && stored.ResponseJSON.Valid && strings.TrimSpace(stored.ResponseJSON.String) != "" {
		return []byte(stored.ResponseJSON.String), true, nil
	}

	response, err := run(tx)
	if err != nil {
		// Expected batch-item failures may be committed by the parent transaction.
		// Remove only the receipt created by this attempt; unexpected errors still
		// abort the parent transaction and roll every write back.
		if created.RowsAffected == 1 {
			if deleteErr := tx.WithContext(ctx).Delete(&IdempotencyKeyModel{}, stored.ID).Error; deleteErr != nil {
				return nil, false, fmt.Errorf("discard failed idempotency key: %w: %v", err, deleteErr)
			}
		}
		return nil, false, err
	}
	if err := tx.WithContext(ctx).
		Model(&IdempotencyKeyModel{}).
		Where("id = ?", stored.ID).
		Updates(map[string]any{
			"status":        "succeeded",
			"response_json": string(response),
		}).Error; err != nil {
		return nil, false, fmt.Errorf("finish idempotency key: %w", err)
	}
	return response, false, nil
}

func (r *BillingRepo) createOperationLogInTx(ctx context.Context, tx *gorm.DB, log *governancedomain.OperationLog) error {
	if log == nil {
		return nil
	}
	if r.operationLogs == nil {
		return nil
	}
	return r.operationLogs.CreateInTx(ctx, tx, log)
}

func (r *BillingRepo) redeemCardInTx(ctx context.Context, tx *gorm.DB, req billingapp.RedeemCardCommand) (*billingapp.RedeemCardResult, error) {
	var card CardKeyModel
	if err := tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		First(&card, "card_key = ?", req.CardKey).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, domain.ErrCardNotFound
		}
		return nil, fmt.Errorf("lock card key: %w", err)
	}
	if domain.CardKeyStatus(card.Status) != domain.CardKeyStatusEnabled {
		return nil, domain.ErrCardDisabled
	}
	if card.ExpireAt != nil && !card.ExpireAt.After(req.Now) {
		return nil, domain.ErrCardExpired
	}
	var existing CardKeyRedemptionModel
	err := tx.WithContext(ctx).First(&existing, "card_key = ? AND user_id = ?", req.CardKey, req.UserID).Error
	if err == nil {
		return nil, domain.ErrCardAlreadyRedeemed
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("find card redemption: %w", err)
	}
	if card.RedeemedCount >= card.MaxRedemptions {
		return nil, domain.ErrCardExhausted
	}

	referral, err := r.findReferralRelationInTx(ctx, tx, req.UserID)
	if err != nil {
		return nil, err
	}
	wallets, err := r.lockWalletsInTx(ctx, tx, req.UserID, referral.InviterUserID)
	if err != nil {
		return nil, err
	}
	wallet := wallets[req.UserID]
	result, err := r.createConsumerTransaction(ctx, tx, wallet, consumerTransactionRequest{
		UserID:          req.UserID,
		Amount:          card.Amount,
		Direction:       domain.TransactionDirectionIn,
		TransactionType: domain.TransactionTypeCardRedeem,
		BizType:         "card_key",
		BizID:           card.Key,
		IdempotencyKey:  req.IdempotencyKey,
		RequestID:       req.RequestID,
	})
	if err != nil {
		return nil, err
	}
	if err := resetBalanceWarningsInTx(ctx, tx, req.UserID); err != nil {
		return nil, err
	}
	if err := tx.WithContext(ctx).
		Model(&CardKeyModel{}).
		Where("card_key = ?", req.CardKey).
		UpdateColumn("redeemed_count", gorm.Expr("redeemed_count + ?", 1)).Error; err != nil {
		return nil, fmt.Errorf("increment card redemption count: %w", err)
	}
	redemption := CardKeyRedemptionModel{
		CardKey:       req.CardKey,
		UserID:        req.UserID,
		TransactionID: result.Transaction.ID,
		RequestID:     req.RequestID,
	}
	if err := tx.WithContext(ctx).Create(&redemption).Error; err != nil {
		if isDuplicateKeyError(err) {
			return nil, domain.ErrCardAlreadyRedeemed
		}
		return nil, fmt.Errorf("create card redemption: %w", err)
	}
	if err := r.settleReferralRewardInTx(ctx, tx, referral, result.Transaction); err != nil {
		return nil, err
	}
	card.RedeemedCount++
	return &billingapp.RedeemCardResult{
		Wallet:      result.Wallet,
		Transaction: result.Transaction,
		Card:        cardModelToDomain(card),
	}, nil
}

func resetBalanceWarningsInTx(ctx context.Context, tx *gorm.DB, userID uint) error {
	if err := tx.WithContext(ctx).Model(&WalletModel{}).Where("user_id = ?", userID).Updates(map[string]any{
		"balance_warning_level": 0,
		"balance_warning_cycle": gorm.Expr("balance_warning_cycle + 1"),
	}).Error; err != nil {
		return fmt.Errorf("reset balance warnings after credit: %w", err)
	}
	return nil
}

func (r *BillingRepo) adjustConsumerBalanceInTx(ctx context.Context, tx *gorm.DB, wallet *WalletModel, req billingapp.AdjustConsumerBalanceCommand) (*billingapp.AdjustBalanceResult, error) {
	bizType := strings.TrimSpace(req.BizType)
	if bizType == "" {
		bizType = "admin_wallet_adjustment"
		if req.TransactionType == domain.TransactionTypeRefund {
			bizType = "wallet_refund"
		}
	}
	result, err := r.createConsumerTransaction(ctx, tx, wallet, consumerTransactionRequest{
		UserID:          req.UserID,
		Amount:          req.Amount,
		Direction:       req.Direction,
		TransactionType: req.TransactionType,
		ClampToBalance:  req.ClampToBalance,
		BizType:         bizType,
		BizID:           trimBizID(req.Reason),
		IdempotencyKey:  req.IdempotencyKey,
		RequestID:       req.RequestID,
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (r *BillingRepo) transferReferralRewardsInTx(ctx context.Context, tx *gorm.DB, req billingapp.TransferReferralRewardsCommand) (*billingapp.TransferReferralRewardsResult, error) {
	wallet, err := r.lockWalletInTx(ctx, tx, req.UserID)
	if err != nil {
		return nil, err
	}

	var rewards []ReferralRewardModel
	if err := tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("inviter_user_id = ? AND status = ? AND (expires_at IS NULL OR expires_at > ?)", req.UserID, "available", req.Now).
		Order("id ASC").
		Find(&rewards).Error; err != nil {
		return nil, fmt.Errorf("lock referral rewards: %w", err)
	}
	if len(rewards) == 0 {
		return nil, domain.ErrNoReferralRewards
	}

	total := decimal.Zero
	rewardIDs := make([]uint, 0, len(rewards))
	for _, reward := range rewards {
		amount, err := domain.ParseMoney(reward.RewardAmount)
		if err != nil || !amount.IsPositive() {
			return nil, domain.ErrInvalidAmount
		}
		total = total.Add(amount)
		rewardIDs = append(rewardIDs, reward.ID)
	}
	amountString := domain.MoneyString(total)
	result, err := r.createConsumerTransaction(ctx, tx, wallet, consumerTransactionRequest{
		UserID:          req.UserID,
		Amount:          amountString,
		Direction:       domain.TransactionDirectionIn,
		TransactionType: domain.TransactionTypeCredit,
		BizType:         "referral_transfer",
		BizID:           req.IdempotencyKey,
		IdempotencyKey:  req.IdempotencyKey,
		RequestID:       req.RequestID,
	})
	if err != nil {
		return nil, err
	}

	now := req.Now
	updates := map[string]any{
		"status":                  "transferred",
		"transfer_transaction_id": result.Transaction.ID,
		"transferred_at":          &now,
	}
	updated := tx.WithContext(ctx).
		Model(&ReferralRewardModel{}).
		Where("id IN ? AND status = ?", rewardIDs, "available").
		Updates(updates)
	if updated.Error != nil {
		return nil, fmt.Errorf("mark referral rewards transferred: %w", updated.Error)
	}
	if updated.RowsAffected != int64(len(rewardIDs)) {
		return nil, domain.ErrReferralRewardStateConflict
	}

	return &billingapp.TransferReferralRewardsResult{
		Wallet:            result.Wallet,
		Transaction:       result.Transaction,
		TransferredAmount: amountString,
		TransferredCount:  len(rewards),
	}, nil
}

type referralRelation struct {
	InviterUserID uint
	InviteCode    string
}

func (r *BillingRepo) findReferralRelationInTx(ctx context.Context, tx *gorm.DB, inviteeUserID uint) (referralRelation, error) {
	var relation referralRelation
	if err := tx.WithContext(ctx).
		Table("invite_uses AS iu").
		Select("i.referral_owner_user_id AS inviter_user_id, iu.invite_code").
		Joins("JOIN invites AS i ON i.code = iu.invite_code").
		Where("iu.user_id = ? AND i.invite_kind = ? AND i.referral_owner_user_id IS NOT NULL", inviteeUserID, "referral").
		Order("iu.used_at ASC, iu.id ASC").
		Limit(1).
		Scan(&relation).Error; err != nil {
		return referralRelation{}, fmt.Errorf("find referral relation: %w", err)
	}
	if relation.InviterUserID == inviteeUserID {
		return referralRelation{}, nil
	}
	return relation, nil
}

func (r *BillingRepo) settleReferralRewardInTx(
	ctx context.Context,
	tx *gorm.DB,
	relation referralRelation,
	source domain.Transaction,
) error {
	if relation.InviterUserID == 0 || relation.InviteCode == "" {
		return nil
	}
	if source.Direction != domain.TransactionDirectionIn {
		return domain.ErrInvalidAmount
	}

	sourceAmount, err := domain.ParseMoney(source.Amount)
	if err != nil || !sourceAmount.IsPositive() {
		return domain.ErrInvalidAmount
	}
	settings := runtimeconfig.Snapshot()
	ratio := decimalSetting(settings.String("first_order_rebate_ratio", "0.8"), decimal.RequireFromString("0.8"), decimal.Zero, decimal.NewFromInt(1))
	singleCap := decimalSetting(settings.String("single_rebate_cap", "0"), decimal.Zero, decimal.Zero, decimal.Zero)
	cumulativeCap := decimalSetting(settings.String("cumulative_rebate_cap", "0"), decimal.Zero, decimal.Zero, decimal.Zero)
	totalEarned := decimal.Zero
	if cumulativeCap.IsPositive() {
		var rawTotal string
		if err := tx.WithContext(ctx).
			Model(&ReferralRewardModel{}).
			Clauses(clause.Locking{Strength: "UPDATE"}).
			Select("COALESCE(SUM(reward_amount), 0)").
			Where("inviter_user_id = ?", relation.InviterUserID).
			Scan(&rawTotal).Error; err != nil {
			return fmt.Errorf("sum cumulative referral rewards: %w", err)
		}
		totalEarned, err = domain.ParseMoney(rawTotal)
		if err != nil {
			return err
		}
	}
	rewardAmount := calculateReferralReward(sourceAmount, totalEarned, ratio, singleCap, cumulativeCap)
	rewardAmountString := domain.MoneyString(rewardAmount)
	if rewardAmountString == "0.00" {
		return nil
	}
	var expiresAt *time.Time
	if expiryDays := settings.Int("rebate_expiry_days", 90, 0); expiryDays > 0 {
		expiry := source.CreatedAt.AddDate(0, 0, expiryDays)
		expiresAt = &expiry
	}

	reward := ReferralRewardModel{
		InviterUserID:       relation.InviterUserID,
		InviteeUserID:       source.UserID,
		InviteCode:          relation.InviteCode,
		SourceTransactionID: source.ID,
		SourceAmount:        source.Amount,
		RewardAmount:        rewardAmountString,
		Status:              "available",
		ExpiresAt:           expiresAt,
	}
	if err := tx.WithContext(ctx).Create(&reward).Error; err != nil {
		if isDuplicateKeyError(err) {
			return nil
		}
		return fmt.Errorf("create referral reward: %w", err)
	}
	return nil
}

func decimalSetting(value string, fallback, minimum, maximum decimal.Decimal) decimal.Decimal {
	parsed, err := money.Parse(value)
	if err != nil || parsed.LessThan(minimum) || maximum.IsPositive() && parsed.GreaterThan(maximum) {
		return fallback
	}
	return parsed
}

func calculateReferralReward(source, totalEarned, ratio, singleCap, cumulativeCap decimal.Decimal) decimal.Decimal {
	reward := source.Mul(ratio)
	if singleCap.IsPositive() && reward.GreaterThan(singleCap) {
		reward = singleCap
	}
	if cumulativeCap.IsPositive() {
		remaining := cumulativeCap.Sub(totalEarned)
		if !remaining.IsPositive() {
			return decimal.Zero
		}
		if reward.GreaterThan(remaining) {
			reward = remaining
		}
	}
	return reward
}

func (r *BillingRepo) getOrCreateWallet(ctx context.Context, tx *gorm.DB, userID uint) (WalletModel, error) {
	if userID == 0 {
		return WalletModel{}, domain.ErrInvalidFilter
	}
	var wallet WalletModel
	if err := tx.WithContext(ctx).First(&wallet, "user_id = ?", userID).Error; err == nil {
		return wallet, nil
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return WalletModel{}, fmt.Errorf("find wallet: %w", err)
	}
	model := defaultWalletModel(userID)
	if err := tx.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&model).Error; err != nil {
		return WalletModel{}, fmt.Errorf("ensure wallet: %w", err)
	}
	if err := tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		First(&wallet, "user_id = ?", userID).Error; err != nil {
		return WalletModel{}, fmt.Errorf("find wallet: %w", err)
	}
	return wallet, nil
}

func defaultWalletModel(userID uint) WalletModel {
	return WalletModel{
		UserID:              userID,
		ConsumerBalance:     "0.00",
		SupplierAvailable:   "0.00",
		SupplierFrozen:      "0.00",
		TotalRecharged:      "0.00",
		TotalSpend:          "0.00",
		SpendCount:          0,
		BalanceWarningLevel: 4,
	}
}

func (r *BillingRepo) lockWalletInTx(ctx context.Context, tx *gorm.DB, userID uint) (*WalletModel, error) {
	var wallet WalletModel
	err := tx.WithContext(ctx).First(&wallet, "user_id = ?", userID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		model := defaultWalletModel(userID)
		if err := tx.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&model).Error; err != nil {
			return nil, fmt.Errorf("ensure wallet: %w", err)
		}
	} else if err != nil {
		return nil, fmt.Errorf("find wallet for lock: %w", err)
	}
	if err := tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		First(&wallet, "user_id = ?", userID).Error; err != nil {
		return nil, fmt.Errorf("lock wallet: %w", err)
	}
	return &wallet, nil
}

func (r *BillingRepo) lockWalletsInTx(ctx context.Context, tx *gorm.DB, userIDs ...uint) (map[uint]*WalletModel, error) {
	unique := make(map[uint]struct{}, len(userIDs))
	ids := make([]uint, 0, len(userIDs))
	for _, userID := range userIDs {
		if userID == 0 {
			continue
		}
		if _, ok := unique[userID]; ok {
			continue
		}
		unique[userID] = struct{}{}
		ids = append(ids, userID)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })

	wallets := make(map[uint]*WalletModel, len(ids))
	for _, userID := range ids {
		wallet, err := r.lockWalletInTx(ctx, tx, userID)
		if err != nil {
			return nil, err
		}
		wallets[userID] = wallet
	}
	return wallets, nil
}

type consumerTransactionRequest struct {
	UserID          uint
	Amount          string
	Direction       domain.TransactionDirection
	TransactionType domain.TransactionType
	ClampToBalance  bool
	BizType         string
	BizID           string
	IdempotencyKey  string
	RequestID       string
}

func (r *BillingRepo) createConsumerTransaction(ctx context.Context, tx *gorm.DB, wallet *WalletModel, req consumerTransactionRequest) (*billingapp.AdjustBalanceResult, error) {
	amount, err := domain.ParseMoney(req.Amount)
	if err != nil || !validConsumerTransactionAmount(amount, req.TransactionType) {
		return nil, domain.ErrInvalidAmount
	}
	before, err := domain.ParseMoney(wallet.ConsumerBalance)
	if err != nil {
		return nil, err
	}
	if req.ClampToBalance && req.Direction == domain.TransactionDirectionOut && before.IsPositive() && before.LessThan(amount) {
		amount = before
	}
	var afterString string
	var signedAmount decimal.Decimal
	switch req.Direction {
	case domain.TransactionDirectionIn:
		afterString = domain.MoneyString(before.Add(amount))
		signedAmount = amount
	case domain.TransactionDirectionOut:
		if before.LessThan(amount) {
			return nil, domain.ErrInsufficientBalance
		}
		afterString = domain.MoneyString(before.Sub(amount))
		signedAmount = amount.Neg()
	default:
		return nil, domain.ErrInvalidTransactionType
	}
	if _, err := domain.ParseMoney(afterString); err != nil {
		return nil, err
	}
	amountString := domain.MoneyString(signedAmount)
	beforeString := domain.MoneyString(before)
	transaction := WalletTransactionModel{
		TransactionNo:   nextTransactionNo(),
		UserID:          req.UserID,
		TransactionType: string(req.TransactionType),
		BalanceBucket:   string(domain.BalanceBucketConsumer),
		Direction:       string(req.Direction),
		Amount:          amountString,
		BalanceBefore:   beforeString,
		BalanceAfter:    afterString,
		BizType:         req.BizType,
		BizID:           req.BizID,
		IdempotencyKey:  req.IdempotencyKey,
		RequestID:       req.RequestID,
	}
	if err := tx.WithContext(ctx).Create(&transaction).Error; err != nil {
		return nil, fmt.Errorf("create wallet transaction: %w", err)
	}
	updates := map[string]any{"consumer_balance": afterString}
	if req.Direction == domain.TransactionDirectionOut {
		updates["total_spend"] = gorm.Expr("total_spend + ?", domain.MoneyString(amount))
		updates["spend_count"] = gorm.Expr("spend_count + 1")
	}
	if isCumulativeRechargeType(req.TransactionType) {
		updates["total_recharged"] = gorm.Expr("total_recharged + ?", domain.MoneyString(amount))
	}
	if err := tx.WithContext(ctx).
		Model(&WalletModel{}).
		Where("user_id = ?", wallet.UserID).
		Updates(updates).Error; err != nil {
		return nil, fmt.Errorf("update wallet balance: %w", err)
	}
	wallet.ConsumerBalance = afterString
	if req.Direction == domain.TransactionDirectionOut {
		totalSpend, err := domain.ParseMoney(wallet.TotalSpend)
		if err != nil {
			return nil, err
		}
		wallet.TotalSpend = domain.MoneyString(totalSpend.Add(amount))
		wallet.SpendCount++
	}
	if isCumulativeRechargeType(req.TransactionType) {
		totalRecharged, err := domain.ParseMoney(wallet.TotalRecharged)
		if err != nil {
			return nil, err
		}
		wallet.TotalRecharged = domain.MoneyString(totalRecharged.Add(amount))
		if err := r.autoUpgradeUserGroupInTx(ctx, tx, req.UserID, wallet.TotalRecharged); err != nil {
			return nil, err
		}
	}
	wallet.UpdatedAt = time.Now().UTC()
	return &billingapp.AdjustBalanceResult{
		Wallet:      walletModelToDomain(*wallet),
		Transaction: transactionModelToDomain(transaction),
	}, nil
}

func isCumulativeRechargeType(transactionType domain.TransactionType) bool {
	return transactionType == domain.TransactionTypeRecharge || transactionType == domain.TransactionTypeCardRedeem
}

func (r *BillingRepo) autoUpgradeUserGroupInTx(ctx context.Context, tx *gorm.DB, userID uint, totalRecharged string) error {
	var current struct {
		UserGroupID uint `gorm:"column:user_group_id"`
	}
	if err := tx.WithContext(ctx).
		Table("users").
		Select("user_group_id").
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ?", userID).
		Take(&current).Error; err != nil {
		return fmt.Errorf("load membership for upgrade: %w", err)
	}
	var currentGroup struct {
		TopupThreshold string `gorm:"column:topup_threshold"`
	}
	if err := tx.WithContext(ctx).
		Table("user_groups").
		Select("topup_threshold").
		Where("id = ?", current.UserGroupID).
		Take(&currentGroup).Error; err != nil {
		return fmt.Errorf("load membership threshold: %w", err)
	}

	var target struct {
		ID uint `gorm:"column:id"`
	}
	if err := tx.WithContext(ctx).
		Table("user_groups").
		Select("id").
		Where("enabled = ? AND auto_upgrade_enabled = ?", true, true).
		Where("topup_threshold > ? AND topup_threshold <= ?", currentGroup.TopupThreshold, totalRecharged).
		Order("topup_threshold DESC, id DESC").
		Take(&target).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return fmt.Errorf("find membership upgrade: %w", err)
	}

	result := tx.WithContext(ctx).
		Table("users").
		Where("id = ? AND user_group_id = ?", userID, current.UserGroupID).
		Updates(map[string]any{"user_group_id": target.ID, "updated_at": time.Now().UTC()})
	if result.Error != nil {
		return fmt.Errorf("apply membership upgrade: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("apply membership upgrade: user group changed concurrently")
	}
	return nil
}

func validConsumerTransactionAmount(amount decimal.Decimal, transactionType domain.TransactionType) bool {
	if amount.IsNegative() {
		return false
	}
	if transactionType == domain.TransactionTypeDebit || transactionType == domain.TransactionTypeRefund {
		return true
	}
	return amount.IsPositive()
}

func applyTransactionFilter(query *gorm.DB, filter billingapp.TransactionListFilter) *gorm.DB {
	if filter.UserID != 0 {
		query = query.Where("user_id = ?", filter.UserID)
	}
	if filter.TransactionType != "" {
		query = query.Where("transaction_type = ?", filter.TransactionType)
	}
	if search := strings.TrimSpace(filter.Search); search != "" {
		like := search + "%"
		query = query.Where("transaction_no LIKE ? OR biz_id LIKE ?", like, like)
	}
	return query
}

func applyRechargeFilter(query *gorm.DB, filter billingapp.RechargeListFilter) *gorm.DB {
	if filter.UserID != 0 {
		query = query.Where("user_id = ?", filter.UserID)
	}
	if filter.Status != "" {
		query = query.Where("status = ?", filter.Status)
	}
	if search := strings.TrimSpace(filter.Search); search != "" {
		like := "%" + search + "%"
		query = query.Where("recharge_no LIKE ? OR payment_method LIKE ?", like, like)
	}
	return query
}

func applyCardFilter(query *gorm.DB, filter billingapp.CardListFilter) *gorm.DB {
	if filter.Status != "" {
		query = query.Where("status = ?", filter.Status)
	}
	if search := strings.TrimSpace(filter.Search); search != "" {
		query = query.Where("card_key LIKE ?", "%"+search+"%")
	}
	return query
}

// ListConsumerBalances returns consumer balances for the given users without
// creating wallet rows. Users without a wallet are omitted (caller defaults to
// zero).
func (r *BillingRepo) ListConsumerBalances(ctx context.Context, userIDs []uint) (map[uint]string, error) {
	if len(userIDs) == 0 {
		return map[uint]string{}, nil
	}
	var models []WalletModel
	if err := r.db.WithContext(ctx).
		Select("user_id", "consumer_balance").
		Where("user_id IN ?", userIDs).
		Find(&models).Error; err != nil {
		return nil, fmt.Errorf("list consumer balances: %w", err)
	}
	return mapConsumerBalances(models)
}

func (r *BillingRepo) ClaimBalanceWarnings(ctx context.Context, limit int) ([]billingapp.BalanceWarningClaim, error) {
	if limit <= 0 {
		limit = 100
	}
	const targetLevel = `CASE
		WHEN consumer_balance <= 500 THEN 4
		WHEN consumer_balance <= 1000 THEN 3
		WHEN consumer_balance <= 2000 THEN 2
		WHEN consumer_balance <= 3000 THEN 1
		ELSE 0 END`
	claims := make([]billingapp.BalanceWarningClaim, 0, limit)
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var wallets []WalletModel
		if err := tx.WithContext(ctx).
			Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).
			Where("consumer_balance <= 3000 AND balance_warning_level < " + targetLevel).
			Order("updated_at ASC, user_id ASC").
			Limit(limit).
			Find(&wallets).Error; err != nil {
			return fmt.Errorf("claim balance warnings: %w", err)
		}
		for i := range wallets {
			target, err := balanceWarningLevel(wallets[i].ConsumerBalance)
			if err != nil {
				return err
			}
			previous := wallets[i].BalanceWarningLevel
			if target <= previous {
				continue
			}
			level := previous + 1
			if err := tx.WithContext(ctx).Model(&WalletModel{}).
				Where("user_id = ?", wallets[i].UserID).
				Update("balance_warning_level", level).Error; err != nil {
				return fmt.Errorf("advance balance warning: %w", err)
			}
			balance, err := normalizeDBMoney(wallets[i].ConsumerBalance)
			if err != nil {
				return err
			}
			claims = append(claims, billingapp.BalanceWarningClaim{
				UserID: wallets[i].UserID, Balance: balance, Cycle: wallets[i].BalanceWarningCycle,
				PreviousLevel: previous, Level: level,
			})
		}
		return nil
	})
	return claims, err
}

func (r *BillingRepo) ReleaseBalanceWarning(ctx context.Context, claim billingapp.BalanceWarningClaim) error {
	result := r.db.WithContext(ctx).Model(&WalletModel{}).
		Where("user_id = ? AND balance_warning_cycle = ? AND balance_warning_level = ?", claim.UserID, claim.Cycle, claim.Level).
		Update("balance_warning_level", claim.PreviousLevel)
	if result.Error != nil {
		return fmt.Errorf("release balance warning: %w", result.Error)
	}
	return nil
}

func balanceWarningLevel(value string) (int, error) {
	balance, err := domain.ParseMoney(value)
	if err != nil {
		return 0, err
	}
	switch {
	case balance.LessThanOrEqual(decimal.NewFromInt(500)):
		return 4, nil
	case balance.LessThanOrEqual(decimal.NewFromInt(1000)):
		return 3, nil
	case balance.LessThanOrEqual(decimal.NewFromInt(2000)):
		return 2, nil
	case balance.LessThanOrEqual(decimal.NewFromInt(3000)):
		return 1, nil
	default:
		return 0, nil
	}
}

func mapConsumerBalances(models []WalletModel) (map[uint]string, error) {
	balances := make(map[uint]string, len(models))
	for _, m := range models {
		balance, err := normalizeDBMoney(m.ConsumerBalance)
		if err != nil {
			return nil, fmt.Errorf("normalize consumer balance for user %d: %w", m.UserID, err)
		}
		balances[m.UserID] = balance
	}
	return balances, nil
}

func walletModelToDomain(model WalletModel) domain.Wallet {
	return domain.Wallet{
		UserID:            model.UserID,
		ConsumerBalance:   normalizeMoneyString(model.ConsumerBalance),
		SupplierAvailable: normalizeMoneyString(model.SupplierAvailable),
		SupplierFrozen:    normalizeMoneyString(model.SupplierFrozen),
		CreatedAt:         model.CreatedAt,
		UpdatedAt:         model.UpdatedAt,
	}
}

func transactionModelToDomain(model WalletTransactionModel) domain.Transaction {
	return domain.Transaction{
		ID:              model.ID,
		TransactionNo:   model.TransactionNo,
		UserID:          model.UserID,
		TransactionType: domain.TransactionType(model.TransactionType),
		BalanceBucket:   domain.BalanceBucket(model.BalanceBucket),
		Direction:       domain.TransactionDirection(model.Direction),
		Amount:          normalizeMoneyString(model.Amount),
		BalanceBefore:   normalizeMoneyString(model.BalanceBefore),
		BalanceAfter:    normalizeMoneyString(model.BalanceAfter),
		BizType:         model.BizType,
		BizID:           model.BizID,
		ReversalOfNo:    model.ReversalOfNo,
		IdempotencyKey:  model.IdempotencyKey,
		RequestID:       model.RequestID,
		CreatedAt:       model.CreatedAt,
	}
}

func rechargeModelToDomain(model RechargeModel) domain.Recharge {
	gatewayTradeNo := ""
	if model.GatewayTradeNo != nil {
		gatewayTradeNo = *model.GatewayTradeNo
	}
	return domain.Recharge{
		ID:                model.ID,
		RechargeNo:        model.RechargeNo,
		UserID:            model.UserID,
		PaymentMethod:     model.PaymentMethod,
		RechargeQuota:     normalizeMoneyString(model.RechargeQuota),
		PaymentAmount:     normalizeMoneyString(model.PaymentAmount),
		Status:            domain.RechargeStatus(model.Status),
		GatewayTradeNo:    gatewayTradeNo,
		GatewayConfigHash: model.GatewayConfigHash,
		FailureReason:     model.FailureReason,
		QueryAttempts:     model.QueryAttempts,
		LastQueriedAt:     model.LastQueriedAt,
		PaidAt:            model.PaidAt,
		ReconciledAt:      model.ReconciledAt,
		CreatedAt:         model.CreatedAt,
		UpdatedAt:         model.UpdatedAt,
	}
}

func rechargeModelFromDomain(recharge domain.Recharge) RechargeModel {
	var gatewayTradeNo *string
	if strings.TrimSpace(recharge.GatewayTradeNo) != "" {
		value := strings.TrimSpace(recharge.GatewayTradeNo)
		gatewayTradeNo = &value
	}
	return RechargeModel{
		ID:                recharge.ID,
		RechargeNo:        recharge.RechargeNo,
		UserID:            recharge.UserID,
		PaymentMethod:     recharge.PaymentMethod,
		RechargeQuota:     recharge.RechargeQuota,
		PaymentAmount:     recharge.PaymentAmount,
		Status:            string(recharge.Status),
		GatewayTradeNo:    gatewayTradeNo,
		GatewayConfigHash: recharge.GatewayConfigHash,
		FailureReason:     recharge.FailureReason,
		QueryAttempts:     recharge.QueryAttempts,
		LastQueriedAt:     recharge.LastQueriedAt,
		PaidAt:            recharge.PaidAt,
		ReconciledAt:      recharge.ReconciledAt,
		CreatedAt:         recharge.CreatedAt,
		UpdatedAt:         recharge.UpdatedAt,
	}
}

func cardModelToDomain(model CardKeyModel) domain.CardKey {
	return domain.CardKey{
		Key:             model.Key,
		Amount:          normalizeMoneyString(model.Amount),
		Status:          domain.CardKeyStatus(model.Status),
		MaxRedemptions:  model.MaxRedemptions,
		RedeemedCount:   model.RedeemedCount,
		ExpireAt:        model.ExpireAt,
		CreatedByUserID: model.CreatedByUserID,
		CreatedAt:       model.CreatedAt,
		UpdatedAt:       model.UpdatedAt,
	}
}

func cardModelFromDomain(card domain.CardKey) CardKeyModel {
	return CardKeyModel{
		Key:             card.Key,
		Amount:          card.Amount,
		Status:          string(card.Status),
		MaxRedemptions:  card.MaxRedemptions,
		RedeemedCount:   card.RedeemedCount,
		ExpireAt:        card.ExpireAt,
		CreatedByUserID: card.CreatedByUserID,
		CreatedAt:       card.CreatedAt,
		UpdatedAt:       card.UpdatedAt,
	}
}

func normalizeMoneyString(value string) string {
	normalized, err := normalizeDBMoney(value)
	if err != nil {
		return "0.00"
	}
	return normalized
}

func normalizeDBMoney(value string) (string, error) {
	amount, err := domain.ParseMoney(value)
	if err != nil {
		return "", err
	}
	return domain.MoneyString(amount), nil
}

func nextTransactionNo() string {
	return "TX" + platform.NewUUIDV7CompactUpper()
}

func trimBizID(value string) string {
	trimmed := strings.TrimSpace(value)
	if len(trimmed) <= 128 {
		return trimmed
	}
	return trimmed[:128]
}

func isDuplicateKeyError(err error) bool {
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return true
	}
	var mysqlErr *mysql.MySQLError
	return errors.As(err, &mysqlErr) && mysqlErr.Number == 1062
}

func isWholeTransactionRollbackError(err error) bool {
	var mysqlErr *mysql.MySQLError
	return errors.As(err, &mysqlErr) && mysqlErr.Number == 1213
}
