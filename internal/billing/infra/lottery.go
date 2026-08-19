package infra

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	billingapp "github.com/donnel666/remail/internal/billing/app"
	"github.com/donnel666/remail/internal/billing/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type LotterySettlementModel struct {
	LotteryID          uint      `gorm:"primaryKey;column:lottery_id"`
	RequestFingerprint string    `gorm:"type:char(64);not null;column:request_fingerprint"`
	ResponseJSON       *string   `gorm:"type:json;column:response_json"`
	CreatedAt          time.Time `gorm:"not null;autoCreateTime;column:created_at"`
	UpdatedAt          time.Time `gorm:"not null;autoUpdateTime;column:updated_at"`
}

func (LotterySettlementModel) TableName() string { return "billing_lottery_settlements" }

// SettleLotteryPool is the only batch ledger writer exposed to the lottery
// bounded context. The caller never receives a repository or wallet model.
func (r *BillingRepo) SettleLotteryPool(ctx context.Context, req billingapp.LotterySettlementCommand) (*billingapp.LotterySettlementResult, error) {
	if strings.TrimSpace(req.RequestFingerprint) == "" {
		return nil, domain.ErrIdempotencyRequired
	}
	var result billingapp.LotterySettlementResult
	err := r.withTx(ctx, func(txCtx context.Context, tx *gorm.DB) error {
		receipt := LotterySettlementModel{LotteryID: req.LotteryID, RequestFingerprint: req.RequestFingerprint}
		if err := tx.WithContext(txCtx).Clauses(clause.OnConflict{DoNothing: true}).Create(&receipt).Error; err != nil {
			return fmt.Errorf("create lottery settlement receipt: %w", err)
		}
		if err := tx.WithContext(txCtx).Clauses(clause.Locking{Strength: "UPDATE"}).First(&receipt, "lottery_id = ?", req.LotteryID).Error; err != nil {
			return fmt.Errorf("lock lottery settlement receipt: %w", err)
		}
		if receipt.RequestFingerprint != req.RequestFingerprint {
			return domain.ErrIdempotencyConflict
		}
		if receipt.ResponseJSON != nil && strings.TrimSpace(*receipt.ResponseJSON) != "" {
			if err := json.Unmarshal([]byte(*receipt.ResponseJSON), &result); err != nil {
				return fmt.Errorf("decode idempotent lottery settlement: %w", err)
			}
			result.Replayed = true
			return nil
		}
		response, err := r.settleLotteryPoolInTx(txCtx, tx, req)
		if err != nil {
			return err
		}
		if err := json.Unmarshal(response, &result); err != nil {
			return fmt.Errorf("decode lottery settlement: %w", err)
		}
		responseJSON := string(response)
		if err := tx.WithContext(txCtx).Model(&LotterySettlementModel{}).Where("lottery_id = ?", req.LotteryID).Update("response_json", responseJSON).Error; err != nil {
			return fmt.Errorf("finish lottery settlement receipt: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &result, nil
}

func (r *BillingRepo) settleLotteryPoolInTx(ctx context.Context, tx *gorm.DB, req billingapp.LotterySettlementCommand) ([]byte, error) {
	if req.LotteryID == 0 {
		return nil, domain.ErrInvalidFilter
	}
	total, err := domain.ParseMoney(req.TotalAmount)
	if err != nil || total.IsNegative() {
		return nil, domain.ErrInvalidAmount
	}
	unused, err := domain.ParseMoney(req.UnusedAmount)
	if err != nil || unused.IsNegative() {
		return nil, domain.ErrInvalidAmount
	}
	awards := append([]billingapp.LotteryAward(nil), req.Awards...)
	sort.Slice(awards, func(i, j int) bool { return awards[i].UserID < awards[j].UserID })
	seen := make(map[uint]struct{}, len(awards))
	sum := unused
	ids := make([]uint, 0, len(awards))
	for _, award := range awards {
		if award.UserID == 0 {
			return nil, domain.ErrInvalidFilter
		}
		if _, ok := seen[award.UserID]; ok {
			return nil, domain.ErrInvalidFilter
		}
		seen[award.UserID] = struct{}{}
		amount, parseErr := domain.ParseMoney(award.Amount)
		if parseErr != nil || !amount.IsPositive() {
			return nil, domain.ErrInvalidAmount
		}
		award.Amount = domain.MoneyString(amount)
		sum = sum.Add(amount)
		ids = append(ids, award.UserID)
	}
	if !sum.Equal(total) {
		return nil, domain.ErrInvalidAmount
	}
	if existing, found, err := r.existingLotteryAwardsInTx(ctx, tx, req, awards); err != nil {
		return nil, err
	} else if found {
		return json.Marshal(billingapp.LotterySettlementResult{Awards: existing, Replayed: true})
	}
	wallets, err := r.lockWalletsInTx(ctx, tx, ids...)
	if err != nil {
		return nil, err
	}

	result := billingapp.LotterySettlementResult{
		Awards: make([]billingapp.LotteryAwardResult, 0, len(awards)),
	}
	for _, award := range awards {
		amount, _ := domain.ParseMoney(award.Amount)
		transaction, err := r.createConsumerTransaction(ctx, tx, wallets[award.UserID], consumerTransactionRequest{
			UserID:          award.UserID,
			Amount:          domain.MoneyString(amount),
			Direction:       domain.TransactionDirectionIn,
			TransactionType: domain.TransactionTypeCredit,
			BizType:         "lottery_award",
			BizID:           fmt.Sprintf("%d:%d", req.LotteryID, award.UserID),
			IdempotencyKey:  fmt.Sprintf("lottery:settle:%d:award:%d", req.LotteryID, award.UserID),
			RequestID:       req.RequestID,
		})
		if err != nil {
			return nil, err
		}
		if err := resetBalanceWarningsInTx(ctx, tx, award.UserID); err != nil {
			return nil, err
		}
		result.Awards = append(result.Awards, billingapp.LotteryAwardResult{
			UserID: award.UserID, Amount: domain.MoneyString(amount), Transaction: transaction.Transaction,
		})
	}
	return json.Marshal(result)
}

func (r *BillingRepo) existingLotteryAwardsInTx(ctx context.Context, tx *gorm.DB, req billingapp.LotterySettlementCommand, awards []billingapp.LotteryAward) ([]billingapp.LotteryAwardResult, bool, error) {
	prefix := fmt.Sprintf("%d:", req.LotteryID)
	var models []WalletTransactionModel
	if err := tx.WithContext(ctx).
		Where("biz_type = ? AND biz_id LIKE ?", "lottery_award", prefix+"%").
		Order("user_id ASC, id ASC").Find(&models).Error; err != nil {
		return nil, false, fmt.Errorf("find existing lottery awards: %w", err)
	}
	if len(models) == 0 {
		return nil, false, nil
	}
	expected := make(map[uint]string, len(awards))
	for _, award := range awards {
		amount, err := domain.ParseMoney(award.Amount)
		if err != nil {
			return nil, false, domain.ErrInvalidAmount
		}
		expected[award.UserID] = domain.MoneyString(amount)
	}
	result := make([]billingapp.LotteryAwardResult, 0, len(models))
	for _, model := range models {
		_, userText, ok := strings.Cut(model.BizID, ":")
		if !ok {
			return nil, false, domain.ErrIdempotencyConflict
		}
		parsedUserID, err := strconv.ParseUint(userText, 10, 64)
		if err != nil || parsedUserID == 0 {
			return nil, false, domain.ErrIdempotencyConflict
		}
		userID := uint(parsedUserID)
		amount, err := domain.ParseMoney(model.Amount)
		if err != nil || expected[userID] != domain.MoneyString(amount) {
			return nil, false, domain.ErrIdempotencyConflict
		}
		delete(expected, userID)
		result = append(result, billingapp.LotteryAwardResult{
			UserID: userID, Amount: domain.MoneyString(amount), Transaction: transactionModelToDomain(model),
		})
	}
	if len(expected) != 0 {
		return nil, false, domain.ErrIdempotencyConflict
	}
	return result, true, nil
}

var _ billingapp.LotterySettlementRepository = (*BillingRepo)(nil)
