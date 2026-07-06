package infra

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	billingapp "github.com/donnel666/remail/internal/billing/app"
	"github.com/donnel666/remail/internal/billing/domain"
	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	governanceinfra "github.com/donnel666/remail/internal/governance/infra"
	"github.com/go-sql-driver/mysql"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type WalletModel struct {
	UserID            uint      `gorm:"primaryKey;column:user_id"`
	ConsumerBalance   string    `gorm:"type:decimal(18,2);not null;default:0;column:consumer_balance"`
	SupplierAvailable string    `gorm:"type:decimal(18,2);not null;default:0;column:supplier_available"`
	SupplierFrozen    string    `gorm:"type:decimal(18,2);not null;default:0;column:supplier_frozen"`
	CreatedAt         time.Time `gorm:"not null;autoCreateTime;column:created_at"`
	UpdatedAt         time.Time `gorm:"not null;autoUpdateTime;column:updated_at"`
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
	Amount          string    `gorm:"type:decimal(18,2);not null"`
	BalanceBefore   string    `gorm:"type:decimal(18,2);not null;column:balance_before"`
	BalanceAfter    string    `gorm:"type:decimal(18,2);not null;column:balance_after"`
	BizType         string    `gorm:"type:varchar(32);not null;column:biz_type"`
	BizID           string    `gorm:"type:varchar(128);not null;column:biz_id"`
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
	ID            uint      `gorm:"primaryKey;autoIncrement"`
	RechargeNo    string    `gorm:"type:varchar(64);not null;column:recharge_no"`
	UserID        uint      `gorm:"not null;column:user_id"`
	PaymentMethod string    `gorm:"type:varchar(32);not null;column:payment_method"`
	RechargeQuota string    `gorm:"type:decimal(18,2);not null;column:recharge_quota"`
	PaymentAmount string    `gorm:"type:decimal(18,2);not null;column:payment_amount"`
	Status        string    `gorm:"type:varchar(32);not null;default:'paying'"`
	CreatedAt     time.Time `gorm:"not null;autoCreateTime;column:created_at"`
	UpdatedAt     time.Time `gorm:"not null;autoUpdateTime;column:updated_at"`
}

func (RechargeModel) TableName() string {
	return "recharges"
}

type CardKeyModel struct {
	Key             string     `gorm:"primaryKey;type:varchar(128);column:card_key"`
	Amount          string     `gorm:"type:decimal(18,2);not null"`
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

func (CardKeyRedemptionModel) TableName() string {
	return "card_key_redemptions"
}

type BillingRepo struct {
	db            *gorm.DB
	operationLogs operationLogWriter
}

func NewBillingRepo(db *gorm.DB) *BillingRepo {
	return &BillingRepo{
		db:            db,
		operationLogs: governanceinfra.NewOperationLogRepo(db),
	}
}

type operationLogWriter interface {
	CreateInTx(ctx context.Context, tx *gorm.DB, log *governancedomain.OperationLog) error
}

func (r *BillingRepo) GetOrCreateWalletSummary(ctx context.Context, userID uint) (*domain.WalletSummary, error) {
	wallet, err := r.getOrCreateWallet(ctx, r.db.WithContext(ctx), userID)
	if err != nil {
		return nil, err
	}

	var spend string
	if err := r.db.WithContext(ctx).
		Model(&WalletTransactionModel{}).
		Select("COALESCE(SUM(amount), 0)").
		Where("user_id = ? AND balance_bucket = ? AND direction = ?", userID, domain.BalanceBucketConsumer, domain.TransactionDirectionOut).
		Scan(&spend).Error; err != nil {
		return nil, fmt.Errorf("sum wallet spend: %w", err)
	}
	var orderCount int64
	if err := r.db.WithContext(ctx).
		Model(&WalletTransactionModel{}).
		Where("user_id = ? AND balance_bucket = ? AND direction = ?", userID, domain.BalanceBucketConsumer, domain.TransactionDirectionOut).
		Count(&orderCount).Error; err != nil {
		return nil, fmt.Errorf("count wallet spend: %w", err)
	}
	normalizedSpend, err := normalizeDBMoney(spend)
	if err != nil {
		return nil, err
	}
	return &domain.WalletSummary{
		Wallet:          walletModelToDomain(wallet),
		HistoricalSpend: normalizedSpend,
		OrderCount:      orderCount,
	}, nil
}

func (r *BillingRepo) ListTransactions(ctx context.Context, filter billingapp.TransactionListFilter, offset, limit int) ([]domain.Transaction, error) {
	var models []WalletTransactionModel
	query := r.db.WithContext(ctx).Model(&WalletTransactionModel{})
	query = applyTransactionFilter(query, filter)
	if err := query.Order("created_at DESC, id DESC").Offset(offset).Limit(limit).Find(&models).Error; err != nil {
		return nil, fmt.Errorf("list wallet transactions: %w", err)
	}
	items := make([]domain.Transaction, len(models))
	for i := range models {
		items[i] = transactionModelToDomain(models[i])
	}
	return items, nil
}

func (r *BillingRepo) CountTransactions(ctx context.Context, filter billingapp.TransactionListFilter) (int64, error) {
	var total int64
	query := r.db.WithContext(ctx).Model(&WalletTransactionModel{})
	query = applyTransactionFilter(query, filter)
	if err := query.Count(&total).Error; err != nil {
		return 0, fmt.Errorf("count wallet transactions: %w", err)
	}
	return total, nil
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
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		response, replayed, err := r.withIdempotencyInTx(ctx, tx, req.UserID, "wallet.adjust", req.IdempotencyKey, req.RequestFingerprint, func(writeTx *gorm.DB) ([]byte, error) {
			created, err := r.adjustConsumerBalanceInTx(ctx, writeTx, req)
			if err != nil {
				return nil, err
			}
			if err := r.createOperationLogInTx(ctx, writeTx, req.OperationLog); err != nil {
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
		result := tx.WithContext(ctx).Model(&CardKeyModel{}).Where("card_key = ?", req.CardKey).Updates(updates)
		if result.Error != nil {
			return fmt.Errorf("update card key: %w", result.Error)
		}
		if result.RowsAffected == 0 {
			return domain.ErrCardNotFound
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
	if err := tx.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&model).Error; err != nil {
		return nil, false, fmt.Errorf("create idempotency key: %w", err)
	}

	var stored IdempotencyKeyModel
	if err := tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("owner_user_id = ? AND idempotency_key = ? AND operation = ?", ownerUserID, idempotencyKey, operation).
		First(&stored).Error; err != nil {
		return nil, false, fmt.Errorf("lock idempotency key: %w", err)
	}
	if stored.RequestFingerprint != fingerprint {
		return nil, false, domain.ErrIdempotencyConflict
	}
	if stored.Status == "succeeded" && stored.ResponseJSON.Valid && strings.TrimSpace(stored.ResponseJSON.String) != "" {
		return []byte(stored.ResponseJSON.String), true, nil
	}

	response, err := run(tx)
	if err != nil {
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

	wallet, err := r.lockWalletInTx(ctx, tx, req.UserID)
	if err != nil {
		return nil, err
	}
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
	card.RedeemedCount++
	return &billingapp.RedeemCardResult{
		Wallet:      result.Wallet,
		Transaction: result.Transaction,
		Card:        cardModelToDomain(card),
	}, nil
}

func (r *BillingRepo) adjustConsumerBalanceInTx(ctx context.Context, tx *gorm.DB, req billingapp.AdjustConsumerBalanceCommand) (*billingapp.AdjustBalanceResult, error) {
	wallet, err := r.lockWalletInTx(ctx, tx, req.UserID)
	if err != nil {
		return nil, err
	}
	bizType := "admin_wallet_adjustment"
	if req.TransactionType == domain.TransactionTypeRefund {
		bizType = "wallet_refund"
	}
	result, err := r.createConsumerTransaction(ctx, tx, wallet, consumerTransactionRequest{
		UserID:          req.UserID,
		Amount:          req.Amount,
		Direction:       req.Direction,
		TransactionType: req.TransactionType,
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

func (r *BillingRepo) getOrCreateWallet(ctx context.Context, tx *gorm.DB, userID uint) (WalletModel, error) {
	if userID == 0 {
		return WalletModel{}, domain.ErrInvalidFilter
	}
	model := WalletModel{
		UserID:            userID,
		ConsumerBalance:   "0.00",
		SupplierAvailable: "0.00",
		SupplierFrozen:    "0.00",
	}
	if err := tx.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&model).Error; err != nil {
		return WalletModel{}, fmt.Errorf("ensure wallet: %w", err)
	}
	var wallet WalletModel
	if err := tx.WithContext(ctx).First(&wallet, "user_id = ?", userID).Error; err != nil {
		return WalletModel{}, fmt.Errorf("find wallet: %w", err)
	}
	return wallet, nil
}

func (r *BillingRepo) lockWalletInTx(ctx context.Context, tx *gorm.DB, userID uint) (*WalletModel, error) {
	if _, err := r.getOrCreateWallet(ctx, tx, userID); err != nil {
		return nil, err
	}
	var wallet WalletModel
	if err := tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		First(&wallet, "user_id = ?", userID).Error; err != nil {
		return nil, fmt.Errorf("lock wallet: %w", err)
	}
	return &wallet, nil
}

type consumerTransactionRequest struct {
	UserID          uint
	Amount          string
	Direction       domain.TransactionDirection
	TransactionType domain.TransactionType
	BizType         string
	BizID           string
	IdempotencyKey  string
	RequestID       string
}

func (r *BillingRepo) createConsumerTransaction(ctx context.Context, tx *gorm.DB, wallet *WalletModel, req consumerTransactionRequest) (*billingapp.AdjustBalanceResult, error) {
	amount, err := domain.ParseMoney(req.Amount)
	if err != nil || !amount.IsPositive() {
		return nil, domain.ErrInvalidAmount
	}
	before, err := domain.ParseMoney(wallet.ConsumerBalance)
	if err != nil {
		return nil, err
	}
	var afterString string
	switch req.Direction {
	case domain.TransactionDirectionIn:
		afterString = domain.MoneyString(before.Add(amount))
	case domain.TransactionDirectionOut:
		if before.LessThan(amount) {
			return nil, domain.ErrInsufficientBalance
		}
		afterString = domain.MoneyString(before.Sub(amount))
	default:
		return nil, domain.ErrInvalidTransactionType
	}
	amountString := domain.MoneyString(amount)
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
	if err := tx.WithContext(ctx).
		Model(&WalletModel{}).
		Where("user_id = ?", wallet.UserID).
		Update("consumer_balance", afterString).Error; err != nil {
		return nil, fmt.Errorf("update wallet balance: %w", err)
	}
	wallet.ConsumerBalance = afterString
	wallet.UpdatedAt = time.Now().UTC()
	return &billingapp.AdjustBalanceResult{
		Wallet:      walletModelToDomain(*wallet),
		Transaction: transactionModelToDomain(transaction),
	}, nil
}

func applyTransactionFilter(query *gorm.DB, filter billingapp.TransactionListFilter) *gorm.DB {
	if filter.UserID != 0 {
		query = query.Where("user_id = ?", filter.UserID)
	}
	if search := strings.TrimSpace(filter.Search); search != "" {
		like := "%" + search + "%"
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
		IdempotencyKey:  model.IdempotencyKey,
		RequestID:       model.RequestID,
		CreatedAt:       model.CreatedAt,
	}
}

func rechargeModelToDomain(model RechargeModel) domain.Recharge {
	return domain.Recharge{
		ID:            model.ID,
		RechargeNo:    model.RechargeNo,
		UserID:        model.UserID,
		PaymentMethod: model.PaymentMethod,
		RechargeQuota: normalizeMoneyString(model.RechargeQuota),
		PaymentAmount: normalizeMoneyString(model.PaymentAmount),
		Status:        domain.RechargeStatus(model.Status),
		CreatedAt:     model.CreatedAt,
		UpdatedAt:     model.UpdatedAt,
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
	return "TX" + strings.ToUpper(strings.ReplaceAll(uuid.NewString(), "-", ""))
}

func trimBizID(value string) string {
	trimmed := strings.TrimSpace(value)
	if len(trimmed) <= 128 {
		return trimmed
	}
	return trimmed[:128]
}

func isDuplicateKeyError(err error) bool {
	var mysqlErr *mysql.MySQLError
	return errors.As(err, &mysqlErr) && mysqlErr.Number == 1062
}
