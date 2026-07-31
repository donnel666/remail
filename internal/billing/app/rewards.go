package app

import (
	"context"
	cryptorand "crypto/rand"
	"fmt"
	"math"
	"math/big"
	"strings"
	"time"

	"github.com/donnel666/remail/internal/businessday"
	"github.com/donnel666/remail/internal/money"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"github.com/shopspring/decimal"
)

type DailyCheckinCommand struct {
	UserID         uint
	BusinessDate   string
	RewardAmount   string
	CheckedInAt    time.Time
	IdempotencyKey string
	RequestID      string
}

type DailyCheckinResult struct {
	Enabled         bool
	BusinessDate    string
	FirstClaim      bool
	RewardAmount    string
	CheckedInAt     time.Time
	ConsumerBalance string
}

type RewardRepository interface {
	ClaimDailyCheckin(ctx context.Context, command DailyCheckinCommand) (*DailyCheckinResult, error)
}

func (uc *WalletUseCase) ClaimDailyCheckin(ctx context.Context, userID uint, requestID string) (*DailyCheckinResult, error) {
	now := uc.now()
	date, _, _ := businessday.Bounds(now)
	if userID == 0 {
		return nil, fmt.Errorf("invalid user")
	}
	settings := runtimeconfig.Snapshot()
	if strings.TrimSpace(settings.String("daily_checkin_enabled", "false")) != "true" {
		return &DailyCheckinResult{BusinessDate: date, RewardAmount: "0.00"}, nil
	}
	rules, err := runtimeconfig.ParseCheckinRewardRules(settings.String("daily_checkin_reward_rules", "[]"))
	if err != nil {
		return nil, fmt.Errorf("load daily check-in reward rules: %w", err)
	}
	if len(rules) == 0 {
		return nil, fmt.Errorf("daily check-in reward rules are empty")
	}
	reward, err := randomCheckinReward(rules)
	if err != nil {
		return nil, err
	}
	repo, ok := uc.repo.(RewardRepository)
	if !ok {
		return nil, fmt.Errorf("daily rewards unavailable")
	}
	result, err := repo.ClaimDailyCheckin(ctx, DailyCheckinCommand{
		UserID:         userID,
		BusinessDate:   date,
		RewardAmount:   reward,
		CheckedInAt:    now,
		IdempotencyKey: fmt.Sprintf("daily_checkin:%d:%s", userID, date),
		RequestID:      strings.TrimSpace(requestID),
	})
	if result != nil {
		result.Enabled = true
	}
	return result, err
}

func randomCheckinReward(rules []runtimeconfig.CheckinRewardRule) (string, error) {
	var total int64
	for _, rule := range rules {
		if rule.ProbabilityUnits <= 0 || total > math.MaxInt64-rule.ProbabilityUnits {
			return "", fmt.Errorf("draw daily check-in reward: invalid weights")
		}
		total += rule.ProbabilityUnits
	}
	if total <= 0 {
		return "", fmt.Errorf("draw daily check-in reward: weights are empty")
	}
	roll, err := cryptorand.Int(cryptorand.Reader, big.NewInt(total))
	if err != nil {
		return "", fmt.Errorf("draw daily check-in reward tier: %w", err)
	}
	index := checkinRewardIndexAt(rules, roll.Int64())
	if index < 0 {
		return "", fmt.Errorf("draw daily check-in reward: invalid weight total")
	}
	// Descending tiers map to (next lower bound, current upper bound]; the lowest tier is fixed.
	upper := checkinRewardAmount(rules[index])
	lower := upper
	if index+1 < len(rules) {
		lower = checkinRewardAmount(rules[index+1])
	}
	if index+1 < len(rules) && upper <= lower {
		return "", fmt.Errorf("draw daily check-in reward: invalid range")
	}
	var offset int64
	if span := upper - lower; span > 0 {
		value, err := cryptorand.Int(cryptorand.Reader, big.NewInt(span))
		if err != nil {
			return "", fmt.Errorf("draw daily check-in reward amount: %w", err)
		}
		offset = value.Int64()
	}
	return checkinRewardAt(rules, index, offset), nil
}

func checkinRewardIndexAt(rules []runtimeconfig.CheckinRewardRule, roll int64) int {
	var cumulative int64
	for i, rule := range rules {
		cumulative += rule.ProbabilityUnits
		if roll < cumulative {
			return i
		}
	}
	return -1
}

func checkinRewardAt(rules []runtimeconfig.CheckinRewardRule, index int, offset int64) string {
	reward := checkinRewardAmount(rules[index])
	if index+1 < len(rules) {
		reward = checkinRewardAmount(rules[index+1]) + offset + 1
	}
	return money.Format(decimal.NewFromInt(reward))
}

func checkinRewardAmount(rule runtimeconfig.CheckinRewardRule) int64 {
	amount, err := money.Parse(rule.Amount)
	if err != nil {
		return 0
	}
	return amount.IntPart()
}
