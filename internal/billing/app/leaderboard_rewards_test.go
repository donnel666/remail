package app

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/donnel666/remail/internal/businessday"
	maildomain "github.com/donnel666/remail/internal/mailtransport/domain"
	settingsdomain "github.com/donnel666/remail/internal/systemsettings/domain"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
)

type leaderboardRewardRepoStub struct {
	WalletRepository
	result   *LeaderboardSettlementResult
	latest   string
	found    bool
	commands []LeaderboardSettlementCommand
}

func (s *leaderboardRewardRepoStub) LatestLeaderboardSettlementDate(context.Context) (string, bool, error) {
	return s.latest, s.found, nil
}

func (s *leaderboardRewardRepoStub) SettleLeaderboard(_ context.Context, command LeaderboardSettlementCommand) (*LeaderboardSettlementResult, error) {
	s.commands = append(s.commands, command)
	return s.result, nil
}

type leaderboardDirectoryStub struct{}

func (leaderboardDirectoryStub) LookupUsers(context.Context, []uint) (map[uint]UserDirectoryEntry, error) {
	return map[uint]UserDirectoryEntry{7: {UserID: 7, Email: "winner@example.com", Status: "active"}}, nil
}
func (leaderboardDirectoryStub) ListUsers(context.Context, UserDirectoryQuery) (UserDirectoryPage, error) {
	return UserDirectoryPage{}, nil
}

type failingLeaderboardDelivery struct{ calls int }

func (s *failingLeaderboardDelivery) Send(context.Context, maildomain.OutboundMessage) error {
	s.calls++
	return errors.New("smtp unavailable")
}

func TestLeaderboardMailFailureDoesNotFailCommittedSettlement(t *testing.T) {
	runtimeconfig.Replace([]settingsdomain.Setting{
		{Key: "leaderboard_reward_enabled", Value: "true"},
		{Key: "leaderboard_reward_rules", Value: `[{"rankFrom":1,"rankTo":1,"amount":10}]`},
		{Key: "leaderboard_settlement_time", Value: "00:00"},
	})
	t.Cleanup(func() { runtimeconfig.Replace(nil) })
	repo := &leaderboardRewardRepoStub{result: &LeaderboardSettlementResult{
		Created: true, BusinessDate: "2026-07-27", Winners: []LeaderboardWinner{{UserID: 7, Rank: 1, Score: 3, Amount: "10.00"}},
	}}
	delivery := &failingLeaderboardDelivery{}
	uc := NewWalletUseCase(repo)
	uc.now = func() time.Time { return time.Date(2026, 7, 28, 0, 5, 0, 0, time.FixedZone("Asia/Shanghai", 8*60*60)) }
	uc.SetUserDirectory(leaderboardDirectoryStub{})
	uc.SetMailDelivery(delivery)

	if err := uc.SettleDueLeaderboard(context.Background()); err != nil {
		t.Fatalf("mail failure must stay auxiliary: %v", err)
	}
	if len(repo.commands) != 1 || delivery.calls != 1 {
		t.Fatalf("got repo calls %d and delivery calls %d, want 1 each", len(repo.commands), delivery.calls)
	}
}

func TestLeaderboardSettlementCatchesUpEveryMissingDay(t *testing.T) {
	runtimeconfig.Replace([]settingsdomain.Setting{
		{Key: "leaderboard_reward_enabled", Value: "true"},
		{Key: "leaderboard_reward_rules", Value: `[{"rankFrom":1,"rankTo":1,"amount":10}]`},
		{Key: "leaderboard_settlement_time", Value: "00:00"},
	})
	t.Cleanup(func() { runtimeconfig.Replace(nil) })
	repo := &leaderboardRewardRepoStub{
		latest: "2026-07-25", found: true,
		result: &LeaderboardSettlementResult{Created: true},
	}
	uc := NewWalletUseCase(repo)
	uc.now = func() time.Time { return time.Date(2026, 7, 29, 0, 5, 0, 0, businessday.Shanghai) }

	if err := uc.SettleDueLeaderboard(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := make([]string, len(repo.commands))
	for i := range repo.commands {
		got[i] = repo.commands[i].BusinessDate
	}
	want := []string{"2026-07-26", "2026-07-27", "2026-07-28"}
	if !slices.Equal(got, want) {
		t.Fatalf("settled dates %v, want %v", got, want)
	}
}

func TestLeaderboardSettlementWaitsForLateMailStabilityWindow(t *testing.T) {
	runtimeconfig.Replace([]settingsdomain.Setting{
		{Key: "leaderboard_reward_enabled", Value: "true"},
		{Key: "leaderboard_reward_rules", Value: `[{"rankFrom":1,"rankTo":1,"amount":10}]`},
		{Key: "leaderboard_settlement_time", Value: "00:00"},
	})
	t.Cleanup(func() { runtimeconfig.Replace(nil) })
	repo := &leaderboardRewardRepoStub{
		latest: "2026-07-26", found: true,
		result: &LeaderboardSettlementResult{Created: true},
	}
	now := time.Date(2026, 7, 28, 0, 4, 0, 0, businessday.Shanghai)
	uc := NewWalletUseCase(repo)
	uc.now = func() time.Time { return now }

	if err := uc.SettleDueLeaderboard(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(repo.commands) != 0 {
		t.Fatalf("settled before stability window: %v", repo.commands)
	}
	now = now.Add(time.Minute)
	if err := uc.SettleDueLeaderboard(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(repo.commands) != 1 || repo.commands[0].BusinessDate != "2026-07-27" {
		t.Fatalf("settlements after stability window: %+v", repo.commands)
	}
}
