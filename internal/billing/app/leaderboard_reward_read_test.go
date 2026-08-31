package app

import (
	"context"
	"testing"
	"time"
)

type rewardReadRepoStub struct {
	WalletRepository
	settlement *LeaderboardRewardSettlement
}

func (s rewardReadRepoStub) LatestLeaderboardRewards(context.Context, int) (*LeaderboardRewardSettlement, error) {
	return s.settlement, nil
}

func TestLatestBotLeaderboardRewardsRedactsIdentity(t *testing.T) {
	start := time.Date(2026, 8, 29, 16, 0, 0, 0, time.UTC)
	uc := NewWalletUseCase(rewardReadRepoStub{settlement: &LeaderboardRewardSettlement{
		BusinessDate: "2026-08-30", PeriodStart: start, PeriodEnd: start.Add(24 * time.Hour), SettledAt: start.Add(25 * time.Hour),
		Rewards: []LeaderboardRewardRecord{{UserID: 42, Rank: 1, Score: 7, Amount: "10.000000"}},
	}})
	got, err := uc.LatestBotLeaderboardRewards(context.Background(), 10)
	if err != nil {
		t.Fatalf("LatestBotLeaderboardRewards: %v", err)
	}
	if !got.Available || len(got.Items) != 1 || got.Items[0].Name == "42" || got.Items[0].RewardAmount != "10.000000" {
		t.Fatalf("unsafe result: %+v", got)
	}
}
