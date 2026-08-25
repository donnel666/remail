package domain

import "errors"

var (
	ErrInvalidAmount               = errors.New("billing: invalid amount")
	ErrInvalidBalanceBucket        = errors.New("billing: invalid balance bucket")
	ErrInvalidTransactionType      = errors.New("billing: invalid transaction type")
	ErrInvalidRecharge             = errors.New("billing: invalid recharge")
	ErrRechargeNotFound            = errors.New("billing: recharge not found")
	ErrRechargeExpired             = errors.New("billing: recharge reconciliation expired")
	ErrRechargeConfigUnavailable   = errors.New("billing: recharge payment config unavailable")
	ErrRechargeQueueUnavailable    = errors.New("billing: recharge reconciliation queue unavailable")
	ErrRechargeQueryMismatch       = errors.New("billing: recharge gateway query mismatch")
	ErrRechargePending             = errors.New("billing: recharge already pending")
	ErrInvalidCardKey              = errors.New("billing: invalid card key")
	ErrInvalidCardStatus           = errors.New("billing: invalid card status")
	ErrInsufficientBalance         = errors.New("billing: insufficient balance")
	ErrCardNotFound                = errors.New("billing: card key not found")
	ErrCardDisabled                = errors.New("billing: card key disabled")
	ErrCardExpired                 = errors.New("billing: card key expired")
	ErrCardExhausted               = errors.New("billing: card key exhausted")
	ErrCardAlreadyRedeemed         = errors.New("billing: card key already redeemed")
	ErrDuplicateCardKey            = errors.New("billing: duplicate card key")
	ErrIdempotencyRequired         = errors.New("billing: idempotency key required")
	ErrInvalidIdempotencyKey       = errors.New("billing: invalid idempotency key")
	ErrIdempotencyConflict         = errors.New("billing: idempotency conflict")
	ErrInvalidFilter               = errors.New("billing: invalid filter")
	ErrNoReferralRewards           = errors.New("billing: no referral rewards available")
	ErrReferralRewardStateConflict = errors.New("billing: referral reward state conflict")
	ErrTransactionNotFound         = errors.New("billing: transaction not found")
	ErrTransactionAlreadyReversed  = errors.New("billing: transaction already reversed")
	ErrTransactionNotReversible    = errors.New("billing: transaction is not reversible")
)

// ErrRechargeGatewayAuthUnavailable means the provider rejected the
// credentials used for a read-only query. Callers may retry with the current
// provider configuration; it is not evidence that the payment response was
// invalid or that the order is unpaid.
var ErrRechargeGatewayAuthUnavailable = errors.New("billing: recharge gateway authentication unavailable")
