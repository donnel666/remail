package app

import (
	"context"
	"fmt"
	"time"

	"github.com/donnel666/remail/internal/botdisplay"
)

type LeaderboardRewardRecord struct {
	UserID   uint
	Nickname string
	Rank     int
	Score    int
	Amount   string
}

type LeaderboardRewardSettlement struct {
	BusinessDate string
	PeriodStart  time.Time
	PeriodEnd    time.Time
	SettledAt    time.Time
	Rewards      []LeaderboardRewardRecord
}

type BotLeaderboardRewardItem struct {
	Rank         int
	Name         string
	SuccessCount int
	RewardAmount string
}

type BotLeaderboardRewards struct {
	Available    bool
	BusinessDate string
	PeriodStart  *time.Time
	PeriodEnd    *time.Time
	SettledAt    *time.Time
	Items        []BotLeaderboardRewardItem
}

type leaderboardRewardReadRepository interface {
	LatestLeaderboardRewards(ctx context.Context, limit int) (*LeaderboardRewardSettlement, error)
}

func (uc *WalletUseCase) LatestBotLeaderboardRewards(ctx context.Context, limit int) (*BotLeaderboardRewards, error) {
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	repo, ok := uc.repo.(leaderboardRewardReadRepository)
	if !ok {
		return nil, fmt.Errorf("leaderboard rewards unavailable")
	}
	settlement, err := repo.LatestLeaderboardRewards(ctx, limit)
	if err != nil {
		return nil, err
	}
	result := &BotLeaderboardRewards{Items: []BotLeaderboardRewardItem{}}
	if settlement == nil {
		return result, nil
	}
	result.Available = true
	result.BusinessDate = settlement.BusinessDate
	result.PeriodStart = &settlement.PeriodStart
	result.PeriodEnd = &settlement.PeriodEnd
	result.SettledAt = &settlement.SettledAt
	result.Items = make([]BotLeaderboardRewardItem, len(settlement.Rewards))
	for i, reward := range settlement.Rewards {
		result.Items[i] = BotLeaderboardRewardItem{
			Rank: reward.Rank, Name: botdisplay.Name(reward.Nickname, reward.UserID),
			SuccessCount: reward.Score, RewardAmount: reward.Amount,
		}
	}
	return result, nil
}
