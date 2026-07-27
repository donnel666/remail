package app

import (
	"context"
	cryptorand "crypto/rand"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/donnel666/remail/internal/businessday"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
)

const probabilityUnits = int64(1_000_000)

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
	value, err := cryptorand.Int(cryptorand.Reader, big.NewInt(probabilityUnits))
	if err != nil {
		return "", fmt.Errorf("draw daily check-in reward: %w", err)
	}
	return checkinRewardAt(rules, value.Int64()), nil
}

func checkinRewardAt(rules []runtimeconfig.CheckinRewardRule, roll int64) string {
	var cumulative int64
	for _, rule := range rules {
		cumulative += rule.ProbabilityUnits
		if roll < cumulative {
			return rule.Amount
		}
	}
	return "0.00"
}
