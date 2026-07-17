package domain

import (
	"strings"
	"time"

	moneyfmt "github.com/donnel666/remail/internal/money"
	"github.com/shopspring/decimal"
)

type BalanceBucket string

const (
	BalanceBucketConsumer          BalanceBucket = "consumer"
	BalanceBucketSupplierAvailable BalanceBucket = "supplier_available"
	BalanceBucketSupplierFrozen    BalanceBucket = "supplier_frozen"
)

type TransactionDirection string

const (
	TransactionDirectionIn  TransactionDirection = "in"
	TransactionDirectionOut TransactionDirection = "out"
)

type TransactionType string

const (
	TransactionTypeRecharge         TransactionType = "recharge"
	TransactionTypeDebit            TransactionType = "debit"
	TransactionTypeRefund           TransactionType = "refund"
	TransactionTypeFreeze           TransactionType = "freeze"
	TransactionTypeCredit           TransactionType = "credit"
	TransactionTypeWithdrawal       TransactionType = "withdrawal"
	TransactionTypeManualAdjustment TransactionType = "manual_adjustment"
	TransactionTypeCardRedeem       TransactionType = "card_redeem"
	TransactionTypeTransfer         TransactionType = "transfer"
)

type RechargeStatus string

const (
	RechargeStatusPaying     RechargeStatus = "paying"
	RechargeStatusCallback   RechargeStatus = "callback"
	RechargeStatusReconciled RechargeStatus = "reconciled"
	RechargeStatusCredited   RechargeStatus = "credited"
	RechargeStatusFailed     RechargeStatus = "failed"
)

type CardKeyStatus string

const (
	CardKeyStatusEnabled  CardKeyStatus = "enabled"
	CardKeyStatusDisabled CardKeyStatus = "disabled"
)

type Wallet struct {
	UserID            uint
	ConsumerBalance   string
	SupplierAvailable string
	SupplierFrozen    string
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

type WalletSummary struct {
	Wallet          Wallet
	HistoricalSpend string
	OrderCount      int64
}

type ReferralSummary struct {
	InviteCount    int64
	PendingRewards string
	TotalEarned    string
}

type Transaction struct {
	ID              uint
	TransactionNo   string
	UserID          uint
	TransactionType TransactionType
	BalanceBucket   BalanceBucket
	Direction       TransactionDirection
	Amount          string
	BalanceBefore   string
	BalanceAfter    string
	BizType         string
	BizID           string
	ReversalOfNo    *string
	IdempotencyKey  string
	RequestID       string
	CreatedAt       time.Time
}

type Recharge struct {
	ID            uint
	RechargeNo    string
	UserID        uint
	PaymentMethod string
	RechargeQuota string
	PaymentAmount string
	Status        RechargeStatus
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

type CardKey struct {
	Key             string
	Amount          string
	Status          CardKeyStatus
	MaxRedemptions  int
	RedeemedCount   int
	ExpireAt        *time.Time
	CreatedByUserID *uint
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

type CardRedemption struct {
	ID            uint
	CardKey       string
	UserID        uint
	TransactionID uint
	RequestID     string
	RedeemedAt    time.Time
}

func IsValidBalanceBucket(bucket BalanceBucket) bool {
	switch bucket {
	case BalanceBucketConsumer, BalanceBucketSupplierAvailable, BalanceBucketSupplierFrozen:
		return true
	default:
		return false
	}
}

func IsValidCardStatus(status CardKeyStatus) bool {
	switch status {
	case CardKeyStatusEnabled, CardKeyStatusDisabled:
		return true
	default:
		return false
	}
}

func IsValidRechargeStatus(status RechargeStatus) bool {
	switch status {
	case RechargeStatusPaying, RechargeStatusCallback, RechargeStatusReconciled, RechargeStatusCredited, RechargeStatusFailed:
		return true
	default:
		return false
	}
}

func NormalizeRechargeStatus(status string) (RechargeStatus, bool) {
	switch RechargeStatus(strings.ToLower(strings.TrimSpace(status))) {
	case RechargeStatusPaying:
		return RechargeStatusPaying, true
	case RechargeStatusCallback:
		return RechargeStatusCallback, true
	case RechargeStatusReconciled:
		return RechargeStatusReconciled, true
	case RechargeStatusCredited:
		return RechargeStatusCredited, true
	case RechargeStatusFailed:
		return RechargeStatusFailed, true
	default:
		return "", false
	}
}

func NormalizeCardStatus(status string) (CardKeyStatus, bool) {
	switch CardKeyStatus(strings.ToLower(strings.TrimSpace(status))) {
	case CardKeyStatusEnabled:
		return CardKeyStatusEnabled, true
	case CardKeyStatusDisabled:
		return CardKeyStatusDisabled, true
	default:
		return "", false
	}
}

// OppositeDirection returns the compensating direction for a ledger reversal.
func OppositeDirection(direction TransactionDirection) TransactionDirection {
	if direction == TransactionDirectionOut {
		return TransactionDirectionIn
	}
	return TransactionDirectionOut
}

func NormalizeTransactionType(value string) (TransactionType, bool) {
	switch TransactionType(strings.ToLower(strings.TrimSpace(value))) {
	case TransactionTypeRecharge, TransactionTypeDebit, TransactionTypeRefund, TransactionTypeFreeze,
		TransactionTypeCredit, TransactionTypeWithdrawal, TransactionTypeManualAdjustment,
		TransactionTypeCardRedeem, TransactionTypeTransfer:
		return TransactionType(strings.ToLower(strings.TrimSpace(value))), true
	default:
		return "", false
	}
}

func NormalizeTransactionDirection(value string) (TransactionDirection, bool) {
	switch TransactionDirection(strings.ToLower(strings.TrimSpace(value))) {
	case TransactionDirectionIn:
		return TransactionDirectionIn, true
	case TransactionDirectionOut:
		return TransactionDirectionOut, true
	default:
		return "", false
	}
}

func NormalizePositiveMoney(value string) (string, error) {
	amount, err := ParseMoney(value)
	if err != nil || !amount.IsPositive() {
		return "", ErrInvalidAmount
	}
	return MoneyString(amount), nil
}

func NormalizeNonNegativeMoney(value string) (string, error) {
	amount, err := ParseMoney(value)
	if err != nil || amount.IsNegative() {
		return "", ErrInvalidAmount
	}
	return MoneyString(amount), nil
}

func ParseMoney(value string) (decimal.Decimal, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return decimal.Zero, ErrInvalidAmount
	}
	amount, err := moneyfmt.Parse(trimmed)
	if err != nil {
		return decimal.Zero, ErrInvalidAmount
	}
	return amount, nil
}

func MoneyString(amount decimal.Decimal) string {
	return moneyfmt.Format(amount)
}
