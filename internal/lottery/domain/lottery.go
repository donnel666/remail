package domain

import (
	"errors"
	"time"
)

type Status string

const (
	// StatusFunding is retained only for records created by the pre-system-grant
	// implementation. New activities are created directly as open.
	StatusFunding   Status = "funding"
	StatusOpen      Status = "open"
	StatusSettling  Status = "settling"
	StatusCompleted Status = "completed"
	StatusCancelled Status = "cancelled"
)

func (s Status) String() string { return string(s) }

type Trigger string

const (
	TriggerTime         Trigger = "time"
	TriggerParticipants Trigger = "participants"
	TriggerNone         Trigger = ""
)

type Tier string

const (
	TierConsolation Tier = "consolation"
	TierNormal      Tier = "normal"
	TierLucky       Tier = "lucky"
)

func (t Tier) String() string { return string(t) }

var (
	ErrLotteryNotFound       = errors.New("lottery not found")
	ErrLotteryClosed         = errors.New("lottery is closed")
	ErrLotteryNotReady       = errors.New("lottery draw conditions are not met")
	ErrLotteryAlreadyEntered = errors.New("already entered this lottery")
	ErrLotteryNotEligible    = errors.New("account is not eligible for this lottery")
	ErrLotteryNoEntries      = errors.New("lottery has no eligible entries")
	ErrLotteryInvalidRules   = errors.New("lottery rules are invalid")
	// ErrLotteryInsufficientParticipants is deterministic: the first draw
	// condition won, but the entries cannot receive the complete pool while
	// respecting the configured per-entry bounds and fixed prize counts.
	ErrLotteryInsufficientParticipants = errors.New("lottery has insufficient participants for its pool")
	ErrLotteryIdempotencyConflict      = errors.New("lottery idempotency key conflicts with an existing request")
	ErrLotterySettlement               = errors.New("lottery settlement is pending")
)

// Entry rejection codes are deliberately small and stable.  The API uses them
// to translate a failed click without exposing the lottery's private rules in
// the public activity payload.
const (
	EntryRejectedInactive = "lottery_account_inactive"
	EntryRejectedAge      = "lottery_account_age"
	EntryRejectedCreator  = "lottery_creator"
	EntryRejectedClosed   = "lottery_closed"
	EntryRejectedFull     = "lottery_full"
)

// EntryRejectedError carries only the reason needed for a safe client-facing
// message.  It still unwraps to the existing sentinel so older callers keep
// their status handling.
type EntryRejectedError struct {
	Code         string
	RequiredDays int
	Cause        error
}

func (e *EntryRejectedError) Error() string {
	if e == nil || e.Cause == nil {
		return ErrLotteryNotEligible.Error()
	}
	return e.Cause.Error()
}

func (e *EntryRejectedError) Unwrap() error {
	if e == nil || e.Cause == nil {
		return ErrLotteryNotEligible
	}
	return e.Cause
}

// TierWeights keeps the existing wire/storage name for compatibility. In new
// campaigns the values are fixed counts: consolation is derived from the
// participants, while normal and lucky are the requested special-prize counts.
// Legacy campaigns continue to interpret all three values as percentages.
type TierWeights struct {
	Consolation int `json:"consolation"`
	Normal      int `json:"normal"`
	Lucky       int `json:"lucky"`
}

func (w TierWeights) Total() int { return w.Consolation + w.Normal + w.Lucky }

func (w TierWeights) Valid() bool {
	return w.Consolation >= 0 && w.Normal >= 0 && w.Lucky >= 0
}

// ValidFixedCounts validates the v3 representation. Consolation is derived
// from the entries, so callers only configure normal and lucky counts.
func (w TierWeights) ValidFixedCounts() bool {
	return w.Consolation == 0 && w.Normal >= 0 && w.Lucky >= 0
}

// ValidLegacyPercentages validates the v1/v2 representation retained for old
// rows and old idempotent clients.
func (w TierWeights) ValidLegacyPercentages() bool {
	return w.Consolation > 0 && w.Consolation <= 100 &&
		w.Normal >= 0 && w.Normal <= 100 &&
		w.Lucky >= 0 && w.Lucky <= 100 && w.Total() == 100
}

type Lottery struct {
	ID                 uint
	PublicToken        string
	CreatedByUserID    uint
	Title              string
	TotalAmount        string
	MinPayout          string
	MaxPayout          string
	TierWeights        TierWeights
	MinAccountAgeDays  int
	DrawAt             *time.Time
	ParticipantTarget  *int
	ParticipantCount   int
	MaxParticipants    int
	Status             Status
	TriggeredBy        Trigger
	TargetReachedAt    *time.Time
	AlgorithmVersion   string
	UnusedAmount       string
	IdempotencyKey     string
	RequestFingerprint string
	CreatedAt          time.Time
	UpdatedAt          time.Time
	SettledAt          *time.Time
}

type Entry struct {
	ID           uint
	LotteryID    uint
	UserID       uint
	RegisteredAt time.Time
	CreatedAt    time.Time
}

type Payout struct {
	ID                   uint
	LotteryID            uint
	UserID               uint
	Tier                 Tier
	Amount               string
	BillingTransactionNo string
	MailQueuedAt         *time.Time
	CreatedAt            time.Time
}
