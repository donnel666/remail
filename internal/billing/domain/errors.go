package domain

import "errors"

var (
	ErrInvalidAmount               = errors.New("billing: invalid amount")
	ErrInvalidBalanceBucket        = errors.New("billing: invalid balance bucket")
	ErrInvalidTransactionType      = errors.New("billing: invalid transaction type")
	ErrInvalidRecharge             = errors.New("billing: invalid recharge")
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
	ErrIdempotencyConflict         = errors.New("billing: idempotency conflict")
	ErrInvalidFilter               = errors.New("billing: invalid filter")
	ErrNoReferralRewards           = errors.New("billing: no referral rewards available")
	ErrReferralRewardStateConflict = errors.New("billing: referral reward state conflict")
	ErrTransactionNotFound         = errors.New("billing: transaction not found")
	ErrTransactionAlreadyReversed  = errors.New("billing: transaction already reversed")
	ErrTransactionNotReversible    = errors.New("billing: transaction is not reversible")
)
