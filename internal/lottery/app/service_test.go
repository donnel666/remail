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
		Consolation: 80,
		Normal:      15,
		Lucky:       5,
	})
	require.NoError(t, err)
	require.Len(t, payouts, len(entries))

	total := decimal.Zero
	tiers := map[lotterydomain.Tier]int{}
	distinct := map[string]struct{}{}
	for _, payout := range payouts {
		amount, parseErr := money.Parse(payout.Amount)
		require.NoError(t, parseErr)
		require.True(t, amount.GreaterThanOrEqual(decimal.NewFromInt(1)))
		require.True(t, amount.LessThanOrEqual(decimal.NewFromInt(20)))
		total = total.Add(amount)
		tiers[payout.Tier]++
		distinct[payout.Amount] = struct{}{}
	}
	unusedAmount, err := money.Parse(unused)
	require.NoError(t, err)
	require.True(t, total.Add(unusedAmount).Equal(decimal.NewFromInt(1000)))
	require.Equal(t, 80, tiers[lotterydomain.TierConsolation])
	require.Equal(t, 15, tiers[lotterydomain.TierNormal])
	require.Equal(t, 5, tiers[lotterydomain.TierLucky])
	require.Greater(t, len(distinct), 1)
}

func TestAllocateLeavesUnusedAmountAbovePerPersonMaximum(t *testing.T) {
	entries := []lotterydomain.Entry{{LotteryID: 8, UserID: 1}, {LotteryID: 8, UserID: 2}}
	payouts, unused, err := Allocate(entries, "100.00", "1.00", "10.00", lotterydomain.TierWeights{Consolation: 80, Normal: 15, Lucky: 5})
	require.NoError(t, err)
	require.Len(t, payouts, 2)
	require.Equal(t, "81.80", unused)
	require.NotEqual(t, payouts[0].Amount, payouts[1].Amount)
}

func TestAllocateCapsOneAccountAtThreeTimesNominalAverage(t *testing.T) {
	entries := make([]lotterydomain.Entry, 10)
	for i := range entries {
		entries[i] = lotterydomain.Entry{LotteryID: 9, UserID: uint(i + 1)}
	}
	payouts, _, err := Allocate(entries, "100.00", "1.00", "100.00", lotterydomain.TierWeights{Consolation: 80, Normal: 15, Lucky: 5})
	require.NoError(t, err)
	for _, payout := range payouts {
		amount, parseErr := money.Parse(payout.Amount)
		require.NoError(t, parseErr)
		require.True(t, amount.LessThanOrEqual(decimal.NewFromInt(30)), payout.Amount)
	}
}

func TestAllocateHandlesProductionScalePool(t *testing.T) {
	const participants = 3000
	entries := make([]lotterydomain.Entry, participants)
	for i := range entries {
		entries[i] = lotterydomain.Entry{LotteryID: 11, UserID: uint(i + 1)}
	}

	payouts, unused, err := Allocate(entries, "3000000.00", "1.00", "10000000.00", lotterydomain.TierWeights{
		Consolation: 80,
		Normal:      15,
		Lucky:       5,
	})
	require.NoError(t, err)
	require.Len(t, payouts, participants)

	paid := decimal.Zero
	maxAward := decimal.RequireFromString("3000.00") // three times the nominal 1000-point average
	for _, payout := range payouts {
		amount, parseErr := money.Parse(payout.Amount)
		require.NoError(t, parseErr)
		require.True(t, amount.GreaterThanOrEqual(decimal.NewFromInt(1)))
		require.True(t, amount.LessThanOrEqual(maxAward), payout.Amount)
		paid = paid.Add(amount)
	}
	unusedAmount, err := money.Parse(unused)
	require.NoError(t, err)
	require.True(t, paid.Add(unusedAmount).Equal(decimal.RequireFromString("3000000.00")))
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
	require.Equal(t, 99, capacity)

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
		{name: "time only", drawAt: ptrTime(now.Add(time.Hour)), queueCalls: 1, queuedAt: true, wantMax: 99},
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

func TestValidateRulesRejectsFlatTargetDraw(t *testing.T) {
	drawAt := time.Now().Add(time.Hour)
	target := 100
	base := CreateRequest{
		TotalAmount: "100.00", MinPayout: "1.00", MaxPayout: "20.00",
		TierWeights: lotterydomain.TierWeights{Consolation: 80, Normal: 15, Lucky: 5},
		DrawAt:      &drawAt, ParticipantTarget: &target,
	}
	_, _, _, _, err := validateRules(base)
	require.ErrorIs(t, err, lotterydomain.ErrLotteryInvalidRules)

	base.TotalAmount = "300.00"
	_, _, _, _, err = validateRules(base)
	require.NoError(t, err)
}
