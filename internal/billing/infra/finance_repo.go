package infra

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	billingapp "github.com/donnel666/remail/internal/billing/app"
	"github.com/donnel666/remail/internal/billing/domain"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const maxRedemptionRows = 500

// ---- Cards --------------------------------------------------------------

// ListAllCards returns every card matching the search/status filter without
// pagination. The admin console loads the full set to compute owner facets and
// filter by owner role/group in memory (owner identity lives in IAM).
func (r *BillingRepo) ListAllCards(ctx context.Context, filter billingapp.CardListFilter) ([]domain.CardKey, error) {
	var models []CardKeyModel
	query := applyCardFilter(r.db.WithContext(ctx).Model(&CardKeyModel{}), filter)
	if err := query.Order("created_at DESC, card_key DESC").Find(&models).Error; err != nil {
		return nil, fmt.Errorf("list all card keys: %w", err)
	}
	items := make([]domain.CardKey, len(models))
	for i := range models {
		items[i] = cardModelToDomain(models[i])
	}
	return items, nil
}

const cardBulkChunkSize = 5000

// SetCardsStatus flips the status of the given cards, skipping rows already in
// the target status; the returned count is the number actually changed. The key
// set is chunked so a large filter-mode selection can't blow the SQL placeholder
// limit (mirrors IAM's bulk chunking).
func (r *BillingRepo) SetCardsStatus(ctx context.Context, cardKeys []string, status domain.CardKeyStatus) (int, error) {
	var affected int64
	for start := 0; start < len(cardKeys); start += cardBulkChunkSize {
		end := start + cardBulkChunkSize
		if end > len(cardKeys) {
			end = len(cardKeys)
		}
		result := r.db.WithContext(ctx).Model(&CardKeyModel{}).
			Where("card_key IN ? AND status <> ?", cardKeys[start:end], string(status)).
			Update("status", string(status))
		if result.Error != nil {
			return int(affected), fmt.Errorf("set card status: %w", result.Error)
		}
		affected += result.RowsAffected
	}
	return int(affected), nil
}

// ListCardRedemptions returns a card's redemptions newest first plus the card's
// amount (each redemption credited the card's face amount).
func (r *BillingRepo) ListCardRedemptions(ctx context.Context, cardKey string, limit int) ([]domain.CardRedemption, string, error) {
	if limit <= 0 || limit > maxRedemptionRows {
		limit = maxRedemptionRows
	}
	var card CardKeyModel
	if err := r.db.WithContext(ctx).First(&card, "card_key = ?", cardKey).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, "", domain.ErrCardNotFound
		}
		return nil, "", fmt.Errorf("find card key: %w", err)
	}
	var models []CardKeyRedemptionModel
	if err := r.db.WithContext(ctx).
		Where("card_key = ?", cardKey).
		Order("redeemed_at DESC, id DESC").
		Limit(limit).
		Find(&models).Error; err != nil {
		return nil, "", fmt.Errorf("list card redemptions: %w", err)
	}
	items := make([]domain.CardRedemption, len(models))
	for i := range models {
		items[i] = domain.CardRedemption{
			ID:            models[i].ID,
			CardKey:       models[i].CardKey,
			UserID:        models[i].UserID,
			TransactionID: models[i].TransactionID,
			RequestID:     models[i].RequestID,
			RedeemedAt:    models[i].RedeemedAt,
		}
	}
	return items, normalizeMoneyString(card.Amount), nil
}

// ---- Transactions -------------------------------------------------------

func applyAdminTransactionFilter(query *gorm.DB, filter billingapp.AdminTransactionFilter) *gorm.DB {
	if filter.Type != "" {
		query = query.Where("transaction_type = ?", string(filter.Type))
	}
	if filter.Direction != "" {
		query = query.Where("direction = ?", string(filter.Direction))
	}
	if filter.CreatedFrom != nil {
		query = query.Where("created_at >= ?", filter.CreatedFrom.UTC())
	}
	if filter.CreatedTo != nil {
		query = query.Where("created_at <= ?", filter.CreatedTo.UTC())
	}
	if search := strings.TrimSpace(filter.Search); search != "" {
		like := search + "%"
		clauses := "transaction_no LIKE ? OR biz_id LIKE ? OR transaction_type LIKE ?"
		args := []any{like, like, like}
		if filter.SearchUserID != 0 {
			clauses += " OR user_id = ?"
			args = append(args, filter.SearchUserID)
		}
		if len(filter.SearchUserIDs) > 0 {
			clauses += " OR user_id IN ?"
			args = append(args, filter.SearchUserIDs)
		}
		query = query.Where("("+clauses+")", args...)
	}
	return query
}

func (r *BillingRepo) ListAdminTransactions(ctx context.Context, filter billingapp.AdminTransactionFilter, offset, limit int) ([]billingapp.AdminTransaction, int64, error) {
	var total int64
	if err := applyAdminTransactionFilter(r.db.WithContext(ctx).Model(&WalletTransactionModel{}), filter).
		Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("count admin transactions: %w", err)
	}
	var models []WalletTransactionModel
	if err := applyAdminTransactionFilter(r.db.WithContext(ctx).Model(&WalletTransactionModel{}), filter).
		Order("id DESC").
		Offset(offset).
		Limit(limit).
		Find(&models).Error; err != nil {
		return nil, 0, fmt.Errorf("list admin transactions: %w", err)
	}
	nos := make([]string, len(models))
	for i := range models {
		nos[i] = models[i].TransactionNo
	}
	reversedBy, err := r.reversalsByOriginalNo(ctx, nos)
	if err != nil {
		return nil, 0, err
	}
	items := make([]billingapp.AdminTransaction, len(models))
	for i := range models {
		items[i] = adminTransactionFromModel(models[i], reversedBy)
	}
	return items, total, nil
}

func (r *BillingRepo) GetAdminTransaction(ctx context.Context, id uint) (*billingapp.AdminTransaction, error) {
	var model WalletTransactionModel
	if err := r.db.WithContext(ctx).First(&model, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, domain.ErrTransactionNotFound
		}
		return nil, fmt.Errorf("find transaction: %w", err)
	}
	reversedBy, err := r.reversalsByOriginalNo(ctx, []string{model.TransactionNo})
	if err != nil {
		return nil, err
	}
	at := adminTransactionFromModel(model, reversedBy)
	return &at, nil
}

// reversalsByOriginalNo maps an original transaction_no to the transaction_no of
// the compensating entry that reverses it (derived, never stored on the row).
func (r *BillingRepo) reversalsByOriginalNo(ctx context.Context, nos []string) (map[string]string, error) {
	out := make(map[string]string, len(nos))
	if len(nos) == 0 {
		return out, nil
	}
	var rows []struct {
		TransactionNo string `gorm:"column:transaction_no"`
		ReversalOfNo  string `gorm:"column:reversal_of_no"`
	}
	if err := r.db.WithContext(ctx).Model(&WalletTransactionModel{}).
		Select("transaction_no, reversal_of_no").
		Where("reversal_of_no IN ?", nos).
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("load reversals: %w", err)
	}
	for _, row := range rows {
		out[row.ReversalOfNo] = row.TransactionNo
	}
	return out, nil
}

func adminTransactionFromModel(model WalletTransactionModel, reversedBy map[string]string) billingapp.AdminTransaction {
	at := billingapp.AdminTransaction{Transaction: transactionModelToDomain(model)}
	if no, ok := reversedBy[model.TransactionNo]; ok {
		at.Reversed = true
		reversedByNo := no
		at.ReversedByNo = &reversedByNo
	}
	return at
}

func (r *BillingRepo) ReverseTransaction(ctx context.Context, cmd billingapp.ReverseTransactionCommand) (*billingapp.ReverseTransactionResult, error) {
	var result billingapp.ReverseTransactionResult
	err := r.withTx(ctx, func(txCtx context.Context, tx *gorm.DB) error {
		response, replayed, err := r.withIdempotencyInTx(txCtx, tx, cmd.Original.UserID, "transactions.reverse", cmd.IdempotencyKey, cmd.RequestFingerprint, func(writeTx *gorm.DB) ([]byte, error) {
			created, err := r.reverseTransactionInTx(txCtx, writeTx, cmd)
			if err != nil {
				return nil, err
			}
			if err := r.createOperationLogInTx(txCtx, writeTx, cmd.OperationLog); err != nil {
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
				return fmt.Errorf("decode idempotent reversal: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &result, nil
}

func (r *BillingRepo) reverseTransactionInTx(ctx context.Context, tx *gorm.DB, cmd billingapp.ReverseTransactionCommand) (*billingapp.ReverseTransactionResult, error) {
	var original WalletTransactionModel
	if err := tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		First(&original, "id = ?", cmd.Original.ID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, domain.ErrTransactionNotFound
		}
		return nil, fmt.Errorf("lock transaction: %w", err)
	}
	if original.ReversalOfNo != nil {
		return nil, domain.ErrTransactionNotReversible
	}
	var existing int64
	if err := tx.WithContext(ctx).Model(&WalletTransactionModel{}).
		Where("reversal_of_no = ?", original.TransactionNo).
		Count(&existing).Error; err != nil {
		return nil, fmt.Errorf("check existing reversal: %w", err)
	}
	if existing > 0 {
		return nil, domain.ErrTransactionAlreadyReversed
	}

	wallet, err := r.lockWalletInTx(ctx, tx, original.UserID)
	if err != nil {
		return nil, err
	}
	reversalOfNo := original.TransactionNo
	created, err := r.createLedgerEntryInTx(ctx, tx, wallet, ledgerEntryRequest{
		UserID:          original.UserID,
		Bucket:          domain.BalanceBucket(original.BalanceBucket),
		Direction:       domain.OppositeDirection(domain.TransactionDirection(original.Direction)),
		TransactionType: domain.TransactionTypeManualAdjustment,
		BizType:         "reversal",
		BizID:           original.TransactionNo,
		ReversalOfNo:    &reversalOfNo,
		Amount:          absMoney(original.Amount),
		IdempotencyKey:  cmd.IdempotencyKey,
		RequestID:       cmd.RequestID,
	})
	if err != nil {
		return nil, err
	}
	reversedByNo := created.Transaction.TransactionNo
	return &billingapp.ReverseTransactionResult{
		Original: billingapp.AdminTransaction{
			Transaction:  transactionModelToDomain(original),
			Reversed:     true,
			ReversedByNo: &reversedByNo,
		},
		Reversal: billingapp.AdminTransaction{Transaction: created.Transaction},
	}, nil
}

// ---- Wallets ------------------------------------------------------------

func (r *BillingRepo) WithdrawSupplier(ctx context.Context, cmd billingapp.WithdrawSupplierCommand) (*billingapp.AdjustBalanceResult, error) {
	var result billingapp.AdjustBalanceResult
	err := r.withTx(ctx, func(txCtx context.Context, tx *gorm.DB) error {
		response, replayed, err := r.withIdempotencyInTx(txCtx, tx, cmd.UserID, "wallets.withdraw", cmd.IdempotencyKey, cmd.RequestFingerprint, func(writeTx *gorm.DB) ([]byte, error) {
			wallet, err := r.lockWalletInTx(txCtx, writeTx, cmd.UserID)
			if err != nil {
				return nil, err
			}
			created, err := r.createLedgerEntryInTx(txCtx, writeTx, wallet, ledgerEntryRequest{
				UserID:          cmd.UserID,
				Bucket:          domain.BalanceBucketSupplierAvailable,
				Direction:       domain.TransactionDirectionOut,
				TransactionType: domain.TransactionTypeWithdrawal,
				BizType:         "withdrawal",
				BizID:           cmd.BizID,
				Amount:          cmd.Amount,
				IdempotencyKey:  cmd.IdempotencyKey,
				RequestID:       cmd.RequestID,
			})
			if err != nil {
				return nil, err
			}
			if err := r.createOperationLogInTx(txCtx, writeTx, cmd.OperationLog); err != nil {
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
				return fmt.Errorf("decode idempotent withdrawal: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &result, nil
}

func (r *BillingRepo) GetWalletsByUserIDs(ctx context.Context, userIDs []uint) (map[uint]domain.Wallet, error) {
	out := make(map[uint]domain.Wallet, len(userIDs))
	if len(userIDs) == 0 {
		return out, nil
	}
	var models []WalletModel
	if err := r.db.WithContext(ctx).Where("user_id IN ?", userIDs).Find(&models).Error; err != nil {
		return nil, fmt.Errorf("list wallets by ids: %w", err)
	}
	for _, m := range models {
		out[m.UserID] = walletModelToDomain(m)
	}
	return out, nil
}

// ---- Shared ledger writer ----------------------------------------------

type ledgerEntryRequest struct {
	UserID          uint
	Bucket          domain.BalanceBucket
	Direction       domain.TransactionDirection
	TransactionType domain.TransactionType
	BizType         string
	BizID           string
	ReversalOfNo    *string
	Amount          string // positive magnitude
	IdempotencyKey  string
	RequestID       string
}

// createLedgerEntryInTx appends one ledger row against any wallet bucket and
// moves that bucket balance (lock held by caller). Rejects an outbound entry
// that would drive the bucket negative (INV-B1). Consumer credit/debit keep
// their own path; this serves withdrawal and reversal.
func (r *BillingRepo) createLedgerEntryInTx(ctx context.Context, tx *gorm.DB, wallet *WalletModel, req ledgerEntryRequest) (*billingapp.AdjustBalanceResult, error) {
	amount, err := domain.ParseMoney(req.Amount)
	if err != nil || !amount.IsPositive() {
		return nil, domain.ErrInvalidAmount
	}
	column, currentStr, err := bucketColumnValue(wallet, req.Bucket)
	if err != nil {
		return nil, err
	}
	before, err := domain.ParseMoney(currentStr)
	if err != nil {
		return nil, err
	}
	var afterDec, signed decimal.Decimal
	switch req.Direction {
	case domain.TransactionDirectionIn:
		afterDec = before.Add(amount)
		signed = amount
	case domain.TransactionDirectionOut:
		if before.LessThan(amount) {
			return nil, domain.ErrInsufficientBalance
		}
		afterDec = before.Sub(amount)
		signed = amount.Neg()
	default:
		return nil, domain.ErrInvalidTransactionType
	}
	afterStr := domain.MoneyString(afterDec)
	transaction := WalletTransactionModel{
		TransactionNo:   nextTransactionNo(),
		UserID:          req.UserID,
		TransactionType: string(req.TransactionType),
		BalanceBucket:   string(req.Bucket),
		Direction:       string(req.Direction),
		Amount:          domain.MoneyString(signed),
		BalanceBefore:   domain.MoneyString(before),
		BalanceAfter:    afterStr,
		BizType:         req.BizType,
		BizID:           req.BizID,
		ReversalOfNo:    req.ReversalOfNo,
		IdempotencyKey:  req.IdempotencyKey,
		RequestID:       req.RequestID,
	}
	if err := tx.WithContext(ctx).Create(&transaction).Error; err != nil {
		return nil, fmt.Errorf("create wallet transaction: %w", err)
	}
	if err := tx.WithContext(ctx).
		Model(&WalletModel{}).
		Where("user_id = ?", wallet.UserID).
		Update(column, afterStr).Error; err != nil {
		return nil, fmt.Errorf("update wallet balance: %w", err)
	}
	setBucketValue(wallet, req.Bucket, afterStr)
	wallet.UpdatedAt = time.Now().UTC()
	return &billingapp.AdjustBalanceResult{
		Wallet:      walletModelToDomain(*wallet),
		Transaction: transactionModelToDomain(transaction),
	}, nil
}

func bucketColumnValue(wallet *WalletModel, bucket domain.BalanceBucket) (string, string, error) {
	switch bucket {
	case domain.BalanceBucketConsumer:
		return "consumer_balance", wallet.ConsumerBalance, nil
	case domain.BalanceBucketSupplierAvailable:
		return "supplier_available", wallet.SupplierAvailable, nil
	case domain.BalanceBucketSupplierFrozen:
		return "supplier_frozen", wallet.SupplierFrozen, nil
	default:
		return "", "", domain.ErrInvalidBalanceBucket
	}
}

func setBucketValue(wallet *WalletModel, bucket domain.BalanceBucket, value string) {
	switch bucket {
	case domain.BalanceBucketConsumer:
		wallet.ConsumerBalance = value
	case domain.BalanceBucketSupplierAvailable:
		wallet.SupplierAvailable = value
	case domain.BalanceBucketSupplierFrozen:
		wallet.SupplierFrozen = value
	}
}

func absMoney(value string) string {
	amount, err := domain.ParseMoney(value)
	if err != nil {
		return "0.00"
	}
	return domain.MoneyString(amount.Abs())
}

// ---- Finance summary ----------------------------------------------------

// FinanceLedgerBuckets aggregates the ledger by time bucket and category within
// the range, excluding compensating reversal rows (reversal_of_no set) so
// reversed pairs do not double count. Money is returned as strings.
func (r *BillingRepo) FinanceLedgerBuckets(ctx context.Context, granularity string, from, to time.Time) ([]billingapp.LedgerBucketRow, error) {
	format := "%Y-%m-%d"
	if granularity == "hour" {
		format = "%Y-%m-%d %H:00:00"
	}
	// format is a fixed internal constant, not user input.
	selectSQL := fmt.Sprintf(`DATE_FORMAT(created_at, '%s') AS bucket,
		COALESCE(SUM(CASE WHEN transaction_type IN ('recharge','card_redeem') THEN ABS(amount) ELSE 0 END),0) AS recharge,
		COALESCE(SUM(CASE WHEN transaction_type = 'debit' THEN ABS(amount) ELSE 0 END),0) AS spend,
		COALESCE(SUM(CASE WHEN transaction_type = 'withdrawal' THEN ABS(amount) ELSE 0 END),0) AS withdraw,
		COALESCE(SUM(CASE WHEN transaction_type = 'refund' THEN ABS(amount) ELSE 0 END),0) AS refund,
		COALESCE(SUM(CASE WHEN balance_bucket = 'supplier_available' AND direction = 'in' THEN ABS(amount) ELSE 0 END),0) AS supplier_settlement,
		COALESCE(SUM(CASE WHEN transaction_type = 'credit' AND biz_type LIKE 'referral%%' THEN ABS(amount) ELSE 0 END),0) AS account_revenue`, format)

	var rows []struct {
		Bucket             string `gorm:"column:bucket"`
		Recharge           string `gorm:"column:recharge"`
		Spend              string `gorm:"column:spend"`
		Withdraw           string `gorm:"column:withdraw"`
		Refund             string `gorm:"column:refund"`
		SupplierSettlement string `gorm:"column:supplier_settlement"`
		AccountRevenue     string `gorm:"column:account_revenue"`
	}
	if err := r.db.WithContext(ctx).Model(&WalletTransactionModel{}).
		Select(selectSQL).
		Where("reversal_of_no IS NULL AND created_at >= ? AND created_at <= ?", from.UTC(), to.UTC()).
		Group("bucket").
		Order("bucket ASC").
		Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("finance ledger buckets: %w", err)
	}
	out := make([]billingapp.LedgerBucketRow, len(rows))
	for i := range rows {
		out[i] = billingapp.LedgerBucketRow{
			Bucket:             rows[i].Bucket,
			Recharge:           rows[i].Recharge,
			Spend:              rows[i].Spend,
			Withdraw:           rows[i].Withdraw,
			Refund:             rows[i].Refund,
			SupplierSettlement: rows[i].SupplierSettlement,
			AccountRevenue:     rows[i].AccountRevenue,
		}
	}
	return out, nil
}

// HotOrderItems ranks projects or products by paid amount within the range,
// joined to the project name from core (products have no name of their own, so
// they are labelled by project + product type). dimension is "project" or
// "product".
func (r *BillingRepo) HotOrderItems(ctx context.Context, dimension string, from, to time.Time, limit int) ([]billingapp.HotItem, error) {
	if limit <= 0 {
		limit = 10
	}
	var rows []struct {
		ID          uint    `gorm:"column:id"`
		Name        *string `gorm:"column:name"`
		ProductType *string `gorm:"column:product_type"`
		Amount      string  `gorm:"column:amount"`
		Count       int64   `gorm:"column:count"`
	}
	query := r.db.WithContext(ctx).Table("orders AS o").
		Joins("LEFT JOIN projects AS p ON p.id = o.project_id").
		Where("o.created_at >= ? AND o.created_at <= ?", from.UTC(), to.UTC()).
		// Rank real captured sales only; pending/failed/refunded/closed orders
		// are not revenue (trade OrderStatus values, referenced by literal to
		// avoid a billing→trade import cycle).
		Where("o.status IN ?", []string{"paid", "active", "completed"}).
		Order("amount DESC").
		Limit(limit)
	if dimension == "product" {
		query = query.
			Select("o.project_product_id AS id, MAX(p.name) AS name, MAX(o.product_type) AS product_type, COALESCE(SUM(o.pay_amount - o.refund_amount),0) AS amount, COUNT(*) AS count").
			Group("o.project_product_id")
	} else {
		query = query.
			Select("o.project_id AS id, MAX(p.name) AS name, COALESCE(SUM(o.pay_amount - o.refund_amount),0) AS amount, COUNT(*) AS count").
			Group("o.project_id")
	}
	if err := query.Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("hot order items: %w", err)
	}
	items := make([]billingapp.HotItem, len(rows))
	for i := range rows {
		items[i] = billingapp.HotItem{
			Name:   hotItemName(rows[i].ID, rows[i].Name, rows[i].ProductType),
			Amount: normalizeMoneyString(rows[i].Amount),
			Count:  rows[i].Count,
		}
	}
	return items, nil
}

func hotItemName(id uint, name, productType *string) string {
	label := ""
	if name != nil {
		label = strings.TrimSpace(*name)
	}
	if label == "" {
		return "#" + strconv.FormatUint(uint64(id), 10)
	}
	if productType != nil && strings.TrimSpace(*productType) != "" {
		label += " (" + strings.TrimSpace(*productType) + ")"
	}
	return label
}
