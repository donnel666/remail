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

func TestLatestBotLeaderboardRewardsMatchesWebDisplayName(t *testing.T) {
	start := time.Date(2026, 8, 29, 16, 0, 0, 0, time.UTC)
	uc := NewWalletUseCase(rewardReadRepoStub{settlement: &LeaderboardRewardSettlement{
		BusinessDate: "2026-08-30", PeriodStart: start, PeriodEnd: start.Add(24 * time.Hour), SettledAt: start.Add(25 * time.Hour),
		Rewards: []LeaderboardRewardRecord{
			{UserID: 42, Nickname: " Winner ", Email: "private@example.com", Rank: 1, Score: 7, Amount: "10.000000"},
			{UserID: 43, Email: "fallback@example.com", Rank: 2, Score: 6, Amount: "5.000000"},
		},
	}})
	got, err := uc.LatestBotLeaderboardRewards(context.Background(), 10)
	if err != nil {
		t.Fatalf("LatestBotLeaderboardRewards: %v", err)
	}
	if !got.Available || len(got.Items) != 2 || got.Items[0].Name != "Winner" || got.Items[1].Name != "fallback" || got.Items[0].RewardAmount != "10.000000" {
		t.Fatalf("unexpected result: %+v", got)
	}
}
