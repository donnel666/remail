package app

import (
	"context"
	"errors"
	"testing"
	"time"

	billingapp "github.com/donnel666/remail/internal/billing/app"
	billingdomain "github.com/donnel666/remail/internal/billing/domain"
	lotterydomain "github.com/donnel666/remail/internal/lottery/domain"
	"github.com/donnel666/remail/internal/money"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

func TestAllocatePaysEveryEntryWithinBoundsAndPreservesPool(t *testing.T) {
	entries := make([]lotterydomain.Entry, 100)
	for i := range entries {
		entries[i] = lotterydomain.Entry{LotteryID: 7, UserID: uint(i + 1)}
	}
	payouts, unused, err := Allocate(entries, "1000.00", "1.00", "20.00", lotterydomain.TierWeights{
		Normal: 43,
		Lucky:  5,
	})
	require.NoError(t, err)
	require.Len(t, payouts, len(entries))

	total := decimal.Zero
	tiers := map[lotterydomain.Tier]int{}
	distinct := map[string]struct{}{}
	for _, payout := range payouts {
		amount, parseErr := money.Parse(payout.Amount)
		require.NoError(t, parseErr)
		require.True(t, amount.IsInteger(), payout.Amount)
		require.True(t, amount.GreaterThanOrEqual(decimal.NewFromInt(1)))
		require.True(t, amount.LessThanOrEqual(decimal.NewFromInt(20)))
		total = total.Add(amount)
		tiers[payout.Tier]++
		distinct[payout.Amount] = struct{}{}
		switch payout.Tier {
		case lotterydomain.TierLucky:
			require.Equal(t, "20.00", payout.Amount)
		case lotterydomain.TierConsolation:
			require.Equal(t, "1.00", payout.Amount)
		case lotterydomain.TierNormal:
			require.True(t, amount.GreaterThanOrEqual(decimal.NewFromInt(1)))
			require.True(t, amount.LessThanOrEqual(decimal.NewFromInt(20)))
		}
	}
	require.Equal(t, "0.00", unused)
	require.True(t, total.Equal(decimal.NewFromInt(1000)))
	require.Equal(t, 52, tiers[lotterydomain.TierConsolation])
	require.Equal(t, 43, tiers[lotterydomain.TierNormal])
	require.Equal(t, 5, tiers[lotterydomain.TierLucky])
	require.Greater(t, len(distinct), 1)
}

func TestAllocateDistributesPoolWhenConfiguredCountsNeedAdjustment(t *testing.T) {
	entries := []lotterydomain.Entry{{LotteryID: 8, UserID: 1}, {LotteryID: 8, UserID: 2}}
	payouts, unused, err := Allocate(entries, "12.00", "1.00", "10.00", lotterydomain.TierWeights{Normal: 1, Lucky: 1})
	require.NoError(t, err)
	require.Len(t, payouts, 2)
	require.Equal(t, "0.00", unused)
	paid := decimal.Zero
	for _, payout := range payouts {
		amount, parseErr := money.Parse(payout.Amount)
		require.NoError(t, parseErr)
		paid = paid.Add(amount)
	}
	require.True(t, paid.Equal(decimal.NewFromInt(12)))
	require.Equal(t, 1, countTier(payouts, lotterydomain.TierLucky))
	require.Equal(t, 1, countTier(payouts, lotterydomain.TierNormal))
}

func TestAllocateRejectsFixedCountsWhenPoolIsTooSmall(t *testing.T) {
	entries := make([]lotterydomain.Entry, 10)
	for i := range entries {
		entries[i] = lotterydomain.Entry{LotteryID: 9, UserID: uint(i + 1)}
	}
	_, unused, err := Allocate(entries, "20.00", "1.00", "20.00", lotterydomain.TierWeights{Normal: 0, Lucky: 1})
	require.ErrorIs(t, err, lotterydomain.ErrLotteryInsufficientParticipants)
	require.Equal(t, "0.00", unused)
}

func TestAllocateRejectsPoolAboveConfiguredMaximum(t *testing.T) {
	entries := []lotterydomain.Entry{{LotteryID: 12, UserID: 1}, {LotteryID: 12, UserID: 2}}
	_, unused, err := Allocate(entries, "21.00", "1.00", "10.00", lotterydomain.TierWeights{})
	require.ErrorIs(t, err, lotterydomain.ErrLotteryInsufficientParticipants)
	require.Equal(t, "0.00", unused)
}

func TestAllocateKeepsEachTierInsideItsConfiguredAmountRule(t *testing.T) {
	entries := make([]lotterydomain.Entry, 5)
	for i := range entries {
		entries[i] = lotterydomain.Entry{LotteryID: 14, UserID: uint(i + 1)}
	}
	for total := int64(17); total <= 20; total++ {
		payouts, unused, err := Allocate(entries, money.Format(decimal.NewFromInt(total)), "1.00", "7.00", lotterydomain.TierWeights{
			Normal: 1,
			Lucky:  2,
		})
		require.NoError(t, err, "pool=%d", total)
		require.Equal(t, "0.00", unused, "pool=%d", total)
		paid := decimal.Zero
		for _, payout := range payouts {
			amount, parseErr := money.Parse(payout.Amount)
			require.NoError(t, parseErr, "pool=%d", total)
			paid = paid.Add(amount)
			switch payout.Tier {
			case lotterydomain.TierLucky:
				require.Equal(t, "7.00", payout.Amount, "pool=%d", total)
			case lotterydomain.TierConsolation:
				require.Equal(t, "1.00", payout.Amount, "pool=%d", total)
			case lotterydomain.TierNormal:
				require.True(t, amount.GreaterThanOrEqual(decimal.NewFromInt(1)), "pool=%d", total)
				require.True(t, amount.LessThanOrEqual(decimal.NewFromInt(7)), "pool=%d", total)
			default:
				require.Failf(t, "unknown tier", "tier=%q pool=%d", payout.Tier, total)
			}
		}
		require.True(t, paid.Equal(decimal.NewFromInt(total)), "pool=%d", total)
	}
}

func TestAllocateHandlesProductionScalePool(t *testing.T) {
	const participants = 3000
	entries := make([]lotterydomain.Entry, participants)
	for i := range entries {
		entries[i] = lotterydomain.Entry{LotteryID: 11, UserID: uint(i + 1)}
	}

	payouts, unused, err := Allocate(entries, "3000000.00", "1.00", "3000.00", lotterydomain.TierWeights{
		Normal: 1000,
		Lucky:  10,
	})
	require.NoError(t, err)
	require.Len(t, payouts, participants)

	paid := decimal.Zero
	maxAward := decimal.RequireFromString("3000.00")
	for _, payout := range payouts {
		amount, parseErr := money.Parse(payout.Amount)
		require.NoError(t, parseErr)
		require.True(t, amount.IsInteger(), payout.Amount)
		require.True(t, amount.GreaterThanOrEqual(decimal.NewFromInt(1)))
		require.True(t, amount.LessThanOrEqual(maxAward), payout.Amount)
		paid = paid.Add(amount)
	}
	require.Equal(t, "0.00", unused)
	require.True(t, paid.Equal(decimal.RequireFromString("3000000")))
	require.Equal(t, 10, countTier(payouts, lotterydomain.TierLucky))
	require.Equal(t, 1000, countTier(payouts, lotterydomain.TierNormal))
}

func TestRankEntriesByHistoryPrioritizesLeastServedUsers(t *testing.T) {
	entries := []lotterydomain.Entry{
		{UserID: 1},
		{UserID: 2},
		{UserID: 3},
		{UserID: 4},
	}
	stats := map[uint]WinnerStats{
		1: {LuckyCount: 0, ConsolationCount: 0}, // 1000
		2: {ConsolationCount: 1},                // 1010
		3: {LuckyCount: 1},                      // 500
		4: {LuckyCount: 2},                      // 0
	}
	require.NoError(t, rankEntriesByHistory(entries, stats))
	got := entryUserIDs(entries)
	require.Equal(t, []uint{2, 1, 3, 4}, got)
}

func TestRankEntriesByHistoryRandomizesEqualScoreBoundary(t *testing.T) {
	entries := []lotterydomain.Entry{{UserID: 1}, {UserID: 2}, {UserID: 3}}
	stats := map[uint]WinnerStats{3: {LuckyCount: 1}}
	require.NoError(t, rankEntriesByHistory(entries, stats))
	firstTwo := map[uint]bool{entries[0].UserID: true, entries[1].UserID: true}
	require.True(t, firstTwo[1])
	require.True(t, firstTwo[2])
	require.Equal(t, uint(3), entries[2].UserID)
}

func TestRankedAllocationKeepsHistoryOrderForTiers(t *testing.T) {
	entries := []lotterydomain.Entry{{LotteryID: 15, UserID: 1}, {LotteryID: 15, UserID: 2}, {LotteryID: 15, UserID: 3}}
	payouts, unused, err := allocateFixedCountsRanked(entries, "15.00", "1.00", "10.00", lotterydomain.TierWeights{Normal: 1, Lucky: 1})
	require.NoError(t, err)
	require.Equal(t, "0.00", unused)
	require.Equal(t, uint(1), payouts[0].UserID)
	require.Equal(t, lotterydomain.TierLucky, payouts[0].Tier)
	require.Equal(t, uint(2), payouts[1].UserID)
	require.Equal(t, lotterydomain.TierNormal, payouts[1].Tier)
	require.Equal(t, uint(3), payouts[2].UserID)
	require.Equal(t, lotterydomain.TierConsolation, payouts[2].Tier)
}

type weightedDrawRepoStub struct {
	Repository
	lottery *lotterydomain.Lottery
	entries []lotterydomain.Entry
	stats   map[uint]WinnerStats
	payouts []lotterydomain.Payout
}

func (r *weightedDrawRepoStub) GetByID(context.Context, uint) (*lotterydomain.Lottery, error) {
	return r.lottery, nil
}

func (r *weightedDrawRepoStub) ListAllEntries(context.Context, uint) ([]lotterydomain.Entry, error) {
	return append([]lotterydomain.Entry(nil), r.entries...), nil
}

func (r *weightedDrawRepoStub) LookupWinnerStats(context.Context, []uint) (map[uint]WinnerStats, error) {
	return r.stats, nil
}

func (r *weightedDrawRepoStub) GetPayouts(context.Context, uint) ([]lotterydomain.Payout, error) {
	return append([]lotterydomain.Payout(nil), r.payouts...), nil
}

func (r *weightedDrawRepoStub) SavePayouts(_ context.Context, _ uint, payouts []lotterydomain.Payout) error {
	r.payouts = append([]lotterydomain.Payout(nil), payouts...)
	return nil
}

func (r *weightedDrawRepoStub) RecordBillingTransactions(context.Context, uint, map[uint]string, string) error {
	return nil
}

func (r *weightedDrawRepoStub) Complete(_ context.Context, _ uint, status lotterydomain.Status, _ string, _ time.Time) error {
	r.lottery.Status = status
	return nil
}

type weightedDrawBillingStub struct{}

func (weightedDrawBillingStub) SettleLotteryPool(_ context.Context, req billingapp.LotterySettlementRequest) (*billingapp.LotterySettlementResult, error) {
	result := &billingapp.LotterySettlementResult{Awards: make([]billingapp.LotteryAwardResult, len(req.Awards))}
	for i, award := range req.Awards {
		result.Awards[i] = billingapp.LotteryAwardResult{UserID: award.UserID, Amount: award.Amount, Transaction: billingdomain.Transaction{TransactionNo: "TX"}}
	}
	return result, nil
}

func TestDrawUsesHistoryScoresToChooseTiers(t *testing.T) {
	repo := &weightedDrawRepoStub{
		lottery: &lotterydomain.Lottery{
			ID: 16, Status: lotterydomain.StatusSettling, AlgorithmVersion: algorithmVersion,
			TotalAmount: "15.00", MinPayout: "1.00", MaxPayout: "10.00",
			TierWeights: lotterydomain.TierWeights{Normal: 1, Lucky: 1},
		},
		entries: []lotterydomain.Entry{{LotteryID: 16, UserID: 1}, {LotteryID: 16, UserID: 2}, {LotteryID: 16, UserID: 3}},
		stats:   map[uint]WinnerStats{2: {ConsolationCount: 1}, 3: {LuckyCount: 1}},
	}
	service := NewService(repo, weightedDrawBillingStub{}, nil, nil, nil)
	require.NoError(t, service.Draw(context.Background(), 16))
	require.Len(t, repo.payouts, 3)
	byUser := make(map[uint]lotterydomain.Payout, len(repo.payouts))
	for _, payout := range repo.payouts {
		byUser[payout.UserID] = payout
	}
	require.Equal(t, lotterydomain.TierLucky, byUser[2].Tier)
	require.Equal(t, lotterydomain.TierNormal, byUser[1].Tier)
	require.Equal(t, lotterydomain.TierConsolation, byUser[3].Tier)
}

func TestDrawCancelsWhenFixedPoolCannotFitTheEntries(t *testing.T) {
	repo := &weightedDrawRepoStub{
		lottery: &lotterydomain.Lottery{
			ID: 17, Status: lotterydomain.StatusSettling, AlgorithmVersion: algorithmVersion,
			TotalAmount: "100.00", MinPayout: "1.00", MaxPayout: "10.00",
			TierWeights: lotterydomain.TierWeights{Lucky: 1},
		},
		entries: []lotterydomain.Entry{{LotteryID: 17, UserID: 1}, {LotteryID: 17, UserID: 2}},
		stats:   map[uint]WinnerStats{},
	}
	service := NewService(repo, weightedDrawBillingStub{}, nil, nil, nil)
	require.NoError(t, service.Draw(context.Background(), 17))
	require.Equal(t, lotterydomain.StatusCancelled, repo.lottery.Status)
	require.Empty(t, repo.payouts)
}

func countTier(payouts []lotterydomain.Payout, tier lotterydomain.Tier) int {
	count := 0
	for _, payout := range payouts {
		if payout.Tier == tier {
			count++
		}
	}
	return count
}

func TestAllocateRejectsNonPositivePool(t *testing.T) {
	entries := []lotterydomain.Entry{{LotteryID: 10, UserID: 1}}
	_, _, err := Allocate(entries, "-1.00", "0.01", "1.00", lotterydomain.TierWeights{
		Consolation: 80,
		Normal:      15,
		Lucky:       5,
	})
	require.ErrorIs(t, err, lotterydomain.ErrLotteryInvalidRules)
}

func TestValidateRulesRejectsFractionalLotteryAmounts(t *testing.T) {
	drawAt := time.Now().Add(time.Hour)
	_, _, _, _, err := validateRules(CreateRequest{
		TotalAmount: "100.50", MinPayout: "1.00", MaxPayout: "20.00",
		TierWeights: lotterydomain.TierWeights{Consolation: 80, Normal: 15, Lucky: 5},
		DrawAt:      &drawAt,
	})
	require.ErrorIs(t, err, lotterydomain.ErrLotteryInvalidRules)
}

func TestLegacyAllocationKeepsLedgerPrecision(t *testing.T) {
	entries := make([]lotterydomain.Entry, 10)
	for i := range entries {
		entries[i] = lotterydomain.Entry{LotteryID: 13, UserID: uint(i + 1)}
	}
	payouts, unused, err := allocate(entries, "100.50", "1.25", "20.75", lotterydomain.TierWeights{
		Consolation: 80,
		Normal:      15,
		Lucky:       5,
	}, false)
	require.NoError(t, err)
	require.Len(t, payouts, len(entries))
	paid := decimal.Zero
	for _, payout := range payouts {
		amount, parseErr := money.Parse(payout.Amount)
		require.NoError(t, parseErr)
		paid = paid.Add(amount)
	}
	unusedAmount, parseErr := money.Parse(unused)
	require.NoError(t, parseErr)
	require.True(t, paid.Add(unusedAmount).Equal(decimal.RequireFromString("100.50")))
}

func TestValidateRulesAcceptsEitherEarlyDrawCondition(t *testing.T) {
	drawAt := time.Now().Add(time.Hour)
	target := 10
	base := CreateRequest{
		TotalAmount: "100.00", MinPayout: "1.00", MaxPayout: "20.00",
		TierWeights: lotterydomain.TierWeights{Consolation: 80, Normal: 15, Lucky: 5},
		DrawAt:      &drawAt, ParticipantTarget: &target,
	}
	_, _, _, capacity, err := validateRules(base)
	require.NoError(t, err)
	require.Equal(t, 10, capacity)

	withoutTime := base
	withoutTime.DrawAt = nil
	_, _, _, capacity, err = validateRules(withoutTime)
	require.NoError(t, err)
	require.Equal(t, target, capacity)

	withoutTarget := base
	withoutTarget.ParticipantTarget = nil
	_, _, _, capacity, err = validateRules(withoutTarget)
	require.NoError(t, err)
	require.Equal(t, 100, capacity)

	withoutConditions := withoutTarget
	withoutConditions.DrawAt = nil
	_, _, _, _, err = validateRules(withoutConditions)
	require.ErrorIs(t, err, lotterydomain.ErrLotteryInvalidRules)
}

type createLotteryRepoStub struct {
	Repository
	created     *lotterydomain.Lottery
	existing    *lotterydomain.Lottery
	findCalls   int
	createCalls int
}

type enterLotteryRepoStub struct {
	Repository
	lottery *lotterydomain.Lottery
	entry   *lotterydomain.Entry
	payout  *lotterydomain.Payout
}

func (r *enterLotteryRepoStub) GetByToken(context.Context, string) (*lotterydomain.Lottery, error) {
	return r.lottery, nil
}

func (r *enterLotteryRepoStub) FindEntry(context.Context, uint, uint) (*lotterydomain.Entry, error) {
	return r.entry, nil
}

func (r *enterLotteryRepoStub) FindPayout(context.Context, uint, uint) (*lotterydomain.Payout, error) {
	return r.payout, nil
}

type lotteryUserDirectoryStub struct {
	user *User
	err  error
}

func (d lotteryUserDirectoryStub) FindLotteryUser(context.Context, uint) (*User, error) {
	return d.user, d.err
}

func (d lotteryUserDirectoryStub) LookupLotteryUsers(context.Context, []uint) (map[uint]User, error) {
	return nil, nil
}

func TestEnterReportsTheSpecificAccountRequirement(t *testing.T) {
	now := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	repo := &enterLotteryRepoStub{lottery: &lotterydomain.Lottery{
		ID: 1, CreatedByUserID: 99, Status: lotterydomain.StatusOpen, MaxParticipants: 10,
		MinAccountAgeDays: 30,
	}}
	service := NewService(repo, nil, lotteryUserDirectoryStub{user: &User{
		ID: 7, Status: "active", CreatedAt: now.AddDate(0, 0, -3),
	}}, nil, nil)
	service.SetNow(func() time.Time { return now })

	_, err := service.Enter(context.Background(), "public-token", 7)
	var rejected *lotterydomain.EntryRejectedError
	require.ErrorAs(t, err, &rejected)
	require.Equal(t, lotterydomain.EntryRejectedAge, rejected.Code)
	require.Equal(t, 30, rejected.RequiredDays)
	require.True(t, errors.Is(err, lotterydomain.ErrLotteryNotEligible))
}

func TestEnterReportsInactiveAccountsSeparately(t *testing.T) {
	now := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	repo := &enterLotteryRepoStub{lottery: &lotterydomain.Lottery{
		ID: 1, CreatedByUserID: 99, Status: lotterydomain.StatusOpen, MaxParticipants: 10,
	}}
	service := NewService(repo, nil, lotteryUserDirectoryStub{user: &User{
		ID: 7, Status: "disabled", CreatedAt: now.AddDate(0, 0, -100),
	}}, nil, nil)
	service.SetNow(func() time.Time { return now })

	_, err := service.Enter(context.Background(), "public-token", 7)
	var rejected *lotterydomain.EntryRejectedError
	require.ErrorAs(t, err, &rejected)
	require.Equal(t, lotterydomain.EntryRejectedInactive, rejected.Code)
	require.True(t, errors.Is(err, lotterydomain.ErrLotteryNotEligible))
}

func TestEnterPropagatesUserDirectoryErrors(t *testing.T) {
	repo := &enterLotteryRepoStub{lottery: &lotterydomain.Lottery{
		ID: 1, CreatedByUserID: 99, Status: lotterydomain.StatusOpen, MaxParticipants: 10,
	}}
	directoryErr := errors.New("user directory unavailable")
	service := NewService(repo, nil, lotteryUserDirectoryStub{err: directoryErr}, nil, nil)

	_, err := service.Enter(context.Background(), "public-token", 7)
	require.ErrorIs(t, err, directoryErr)
}

func TestPublicHidesPayoutUntilLotteryCompletes(t *testing.T) {
	for _, status := range []lotterydomain.Status{lotterydomain.StatusOpen, lotterydomain.StatusSettling, lotterydomain.StatusCompleted} {
		t.Run(string(status), func(t *testing.T) {
			repo := &enterLotteryRepoStub{
				lottery: &lotterydomain.Lottery{ID: 1, Status: status},
				entry:   &lotterydomain.Entry{LotteryID: 1, UserID: 7},
				payout:  &lotterydomain.Payout{LotteryID: 1, UserID: 7, Amount: "8.00"},
			}
			service := NewService(repo, nil, nil, nil, nil)
			_, entry, payout, err := service.Public(context.Background(), "public-token", 7)
			require.NoError(t, err)
			require.NotNil(t, entry)
			if status == lotterydomain.StatusCompleted {
				require.NotNil(t, payout)
			} else {
				require.Nil(t, payout)
			}
		})
	}
}

func TestPublicHidesPayoutWhenUserHasNoEntry(t *testing.T) {
	repo := &enterLotteryRepoStub{
		lottery: &lotterydomain.Lottery{ID: 1, Status: lotterydomain.StatusCompleted},
		payout:  &lotterydomain.Payout{LotteryID: 1, UserID: 7, Amount: "8.00"},
	}
	service := NewService(repo, nil, nil, nil, nil)

	_, entry, payout, err := service.Public(context.Background(), "public-token", 7)
	require.NoError(t, err)
	require.Nil(t, entry)
	require.Nil(t, payout)
}

func (r *createLotteryRepoStub) FindByIdempotency(context.Context, uint, string) (*lotterydomain.Lottery, error) {
	r.findCalls++
	if r.existing != nil {
		return r.existing, nil
	}
	return nil, lotterydomain.ErrLotteryNotFound
}

func (r *createLotteryRepoStub) Create(_ context.Context, lottery *lotterydomain.Lottery) error {
	r.createCalls++
	lottery.ID = 42
	r.created = lottery
	return nil
}

type drawQueueStub struct {
	calls int
	at    []*time.Time
}

func (q *drawQueueStub) EnqueueDraw(_ context.Context, _ uint, at *time.Time) error {
	q.calls++
	q.at = append(q.at, at)
	return nil
}

func TestCreateUsesOnlyConfiguredDrawCondition(t *testing.T) {
	now := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	target := 10
	cases := []struct {
		name        string
		drawAt      *time.Time
		participant *int
		queueCalls  int
		queuedAt    bool
		wantMax     int
	}{
		{name: "time only", drawAt: ptrTime(now.Add(time.Hour)), queueCalls: 1, queuedAt: true, wantMax: 100},
		{name: "participants only", participant: &target, wantMax: target},
		{name: "both", drawAt: ptrTime(now.Add(time.Hour)), participant: &target, queueCalls: 1, queuedAt: true, wantMax: target},
		{name: "neither", wantMax: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := &createLotteryRepoStub{}
			queue := &drawQueueStub{}
			service := NewService(repo, nil, nil, nil, queue)
			service.SetNow(func() time.Time { return now })
			result, err := service.Create(context.Background(), CreateRequest{
				CreatedByUserID:   1,
				Title:             "Test lottery",
				TotalAmount:       "100.00",
				MinPayout:         "1.00",
				MaxPayout:         "20.00",
				TierWeights:       lotterydomain.TierWeights{Consolation: 80, Normal: 15, Lucky: 5},
				DrawAt:            tc.drawAt,
				ParticipantTarget: tc.participant,
				IdempotencyKey:    "create-" + tc.name,
			})
			if tc.name == "neither" {
				require.ErrorIs(t, err, lotterydomain.ErrLotteryInvalidRules)
				require.Nil(t, result)
				require.Zero(t, queue.calls)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, result)
			require.Equal(t, tc.wantMax, result.Lottery.MaxParticipants)
			require.Equal(t, tc.queueCalls, queue.calls)
			if tc.queuedAt {
				require.Len(t, queue.at, 1)
				require.NotNil(t, queue.at[0])
			} else {
				require.Empty(t, queue.at)
			}
		})
	}
}

func TestCreateReplaysExistingActivityAfterDrawTime(t *testing.T) {
	now := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	drawAt := now.Add(-time.Hour)
	target := 10
	req := CreateRequest{
		CreatedByUserID:   1,
		Title:             "Past draw retry",
		TotalAmount:       "100.00",
		MinPayout:         "1.00",
		MaxPayout:         "20.00",
		TierWeights:       lotterydomain.TierWeights{Consolation: 80, Normal: 15, Lucky: 5},
		DrawAt:            &drawAt,
		ParticipantTarget: &target,
		IdempotencyKey:    "past-draw-retry",
	}
	total, minPayout, maxPayout, _, err := validateRules(req)
	require.NoError(t, err)
	existing := &lotterydomain.Lottery{
		ID: 77, CreatedByUserID: req.CreatedByUserID, Title: req.Title,
		TotalAmount: total, MinPayout: minPayout, MaxPayout: maxPayout,
		TierWeights: req.TierWeights, MinAccountAgeDays: req.MinAccountAgeDays,
		DrawAt: &drawAt, ParticipantTarget: &target,
		RequestFingerprint: lotteryRequestFingerprint(req, total, minPayout, maxPayout),
		Status:             lotterydomain.StatusCompleted,
	}
	repo := &createLotteryRepoStub{existing: existing}
	service := NewService(repo, nil, nil, nil, nil)
	service.SetNow(func() time.Time { return now })

	result, err := service.Create(context.Background(), req)
	require.NoError(t, err)
	require.True(t, result.Replayed)
	require.Same(t, existing, result.Lottery)
	require.Equal(t, 1, repo.findCalls)
	require.Zero(t, repo.createCalls)
}

func ptrTime(value time.Time) *time.Time { return &value }

func TestRemainingUnusedRebuildsSettlementAfterPayoutsWereSaved(t *testing.T) {
	unused, err := remainingUnused("10.00", []lotterydomain.Payout{{Amount: "2.25"}, {Amount: "3.75"}})
	require.NoError(t, err)
	require.Equal(t, "4.00", unused)
}

func TestValidateSettlementAwardsRejectsPartialBillingResult(t *testing.T) {
	payouts := []lotterydomain.Payout{
		{UserID: 1, Amount: "2.00"},
		{UserID: 2, Amount: "3.00"},
	}
	awards := []billingapp.LotteryAwardResult{{
		UserID:      1,
		Amount:      "2.00",
		Transaction: billingdomain.Transaction{TransactionNo: "TX-1"},
	}}
	require.ErrorIs(t, validateSettlementAwards(payouts, awards), lotterydomain.ErrLotterySettlement)
}

func TestDistributeBonusUsesOneRoundSnapshot(t *testing.T) {
	amounts := []int64{1, 1}
	require.NoError(t, distributeBonus(amounts, []int64{1, 1}, 98, 100))
	require.Equal(t, []int64{50, 50}, amounts)
}

func TestValidateRulesAllowsFlatTargetDrawWhenAllAwardsAreMinimum(t *testing.T) {
	drawAt := time.Now().Add(time.Hour)
	target := 100
	base := CreateRequest{
		TotalAmount: "100.00", MinPayout: "1.00", MaxPayout: "20.00",
		TierWeights: lotterydomain.TierWeights{Consolation: 80, Normal: 15, Lucky: 5},
		DrawAt:      &drawAt, ParticipantTarget: &target,
	}
	_, _, _, _, err := validateRules(base)
	require.NoError(t, err)

	base.TotalAmount = "99.00"
	_, _, _, _, err = validateRules(base)
	require.ErrorIs(t, err, lotterydomain.ErrLotteryInvalidRules)

	base.TotalAmount = "2001.00"
	base.ParticipantTarget = &target
	_, _, _, _, err = validateRules(base)
	require.ErrorIs(t, err, lotterydomain.ErrLotteryInvalidRules)
}

func TestValidateRulesRejectsInfeasibleFixedPrizeCounts(t *testing.T) {
	drawAt := time.Now().Add(time.Hour)
	target := 2
	_, _, _, _, err := validateRules(CreateRequest{
		TotalAmount: "12.00", MinPayout: "1.00", MaxPayout: "10.00",
		TierWeights: lotterydomain.TierWeights{Normal: 0, Lucky: 1},
		DrawAt:      &drawAt, ParticipantTarget: &target,
	})
	require.ErrorIs(t, err, lotterydomain.ErrLotteryInvalidRules)
}

func TestCreateRoutesLegacyPercentageRequestsToV2(t *testing.T) {
	drawAt := time.Now().Add(time.Hour)
	repo := &createLotteryRepoStub{}
	service := NewService(repo, nil, nil, nil, nil)
	_, err := service.Create(context.Background(), CreateRequest{
		CreatedByUserID: 1, Title: "legacy", TotalAmount: "100.00", MinPayout: "1.00", MaxPayout: "20.00",
		TierWeights: lotterydomain.TierWeights{Consolation: 80, Normal: 15, Lucky: 5},
		DrawAt:      ptrTime(drawAt), IdempotencyKey: "legacy-v2",
	})
	require.NoError(t, err)
	require.Equal(t, legacyAlgorithmVersionV2, repo.created.AlgorithmVersion)
}
