package app

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/donnel666/remail/internal/businessday"
	mailapp "github.com/donnel666/remail/internal/mailtransport/app"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
)

type LeaderboardSettlementCommand struct {
	BusinessDate string
	PeriodStart  time.Time
	PeriodEnd    time.Time
	Rules        []runtimeconfig.LeaderboardRewardRule
	RulesJSON    string
	SettledAt    time.Time
}

type LeaderboardWinner struct {
	UserID uint
	Rank   int
	Score  int
	Amount string
}

type LeaderboardSettlementResult struct {
	Created      bool
	BusinessDate string
	Winners      []LeaderboardWinner
}

type LeaderboardRewardRepository interface {
	LatestLeaderboardSettlementDate(ctx context.Context) (string, bool, error)
	SettleLeaderboard(ctx context.Context, command LeaderboardSettlementCommand) (*LeaderboardSettlementResult, error)
}

// ponytail: five minutes covers the current 15-second mail-fetch cadence; use a
// provider ingestion watermark if production regularly observes longer lag.
const leaderboardSettlementStabilityDelay = 5 * time.Minute

// SettleDueLeaderboard catches up completed Shanghai business days after the
// mail-ingestion stability window. Database uniqueness arbitrates replicas.
func (uc *WalletUseCase) SettleDueLeaderboard(ctx context.Context) error {
	settings := runtimeconfig.Snapshot()
	if strings.TrimSpace(settings.String("leaderboard_reward_enabled", "false")) != "true" {
		return nil
	}
	rulesJSON := settings.String("leaderboard_reward_rules", "[]")
	rules, err := runtimeconfig.ParseLeaderboardRewardRules(rulesJSON)
	if err != nil {
		return fmt.Errorf("load leaderboard reward rules: %w", err)
	}
	if len(rules) == 0 {
		return fmt.Errorf("leaderboard reward rules are empty")
	}
	hour, minute, err := runtimeconfig.ParseSettlementClock(settings.String("leaderboard_settlement_time", "00:00"))
	if err != nil {
		return fmt.Errorf("load leaderboard settlement time: %w", err)
	}
	now := uc.now()
	_, dueStart, _ := businessday.DueSettlementBounds(now.Add(-leaderboardSettlementStabilityDelay), hour, minute)
	repo, ok := uc.repo.(LeaderboardRewardRepository)
	if !ok {
		return fmt.Errorf("leaderboard rewards unavailable")
	}
	dueDay := dueStart.In(businessday.Shanghai)
	day := dueDay
	latest, found, err := repo.LatestLeaderboardSettlementDate(ctx)
	if err != nil {
		return err
	}
	if found {
		lastDay, parseErr := time.ParseInLocation(time.DateOnly, latest, businessday.Shanghai)
		if parseErr != nil {
			return fmt.Errorf("parse latest leaderboard settlement date: %w", parseErr)
		}
		day = lastDay.AddDate(0, 0, 1)
		if day.After(dueDay) {
			return nil
		}
	}
	for !day.After(dueDay) {
		date, start, end := businessday.Bounds(day)
		result, settleErr := repo.SettleLeaderboard(ctx, LeaderboardSettlementCommand{
			BusinessDate: date, PeriodStart: start, PeriodEnd: end, Rules: rules, RulesJSON: rulesJSON, SettledAt: now,
		})
		if settleErr != nil {
			return settleErr
		}
		if result != nil && result.Created && len(result.Winners) > 0 {
			uc.notifyLeaderboardWinners(ctx, result)
		}
		day = day.AddDate(0, 0, 1)
	}
	return nil
}

func (uc *WalletUseCase) notifyLeaderboardWinners(ctx context.Context, result *LeaderboardSettlementResult) {
	if uc.delivery == nil || uc.users == nil {
		return
	}
	ids := make([]uint, len(result.Winners))
	for i, winner := range result.Winners {
		ids[i] = winner.UserID
	}
	users, err := uc.users.LookupUsers(ctx, ids)
	if err != nil {
		slog.Warn("load leaderboard winners for notification failed", "business_date", result.BusinessDate, "error", err)
		return
	}
	for _, winner := range result.Winners {
		user, ok := users[winner.UserID]
		status := strings.ToLower(strings.TrimSpace(user.Status))
		if !ok || strings.TrimSpace(user.Email) == "" || (status != "" && status != "active") {
			continue
		}
		if err := uc.delivery.Send(ctx, mailapp.LeaderboardRewardMessage(user.Email, result.BusinessDate, winner.Rank, winner.Score, winner.Amount)); err != nil {
			slog.Warn("send leaderboard reward notification failed", "user_id", winner.UserID, "business_date", result.BusinessDate, "error", err)
		}
	}
}
