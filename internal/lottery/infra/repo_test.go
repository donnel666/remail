package infra

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	lotteryapp "github.com/donnel666/remail/internal/lottery/app"
	lotterydomain "github.com/donnel666/remail/internal/lottery/domain"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestAddEntryReturnsExistingEntryAfterLotteryClosed(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:lottery-entry-%s?mode=memory&cache=shared", t.Name())), &gorm.Config{TranslateError: true})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&LotteryModel{}, &EntryModel{}, &PayoutModel{}))
	weights, err := json.Marshal(lotterydomain.TierWeights{Consolation: 80, Normal: 15, Lucky: 5})
	require.NoError(t, err)
	now := time.Now().UTC()
	lottery := LotteryModel{
		PublicToken: "closed-lottery", CreatedByUserID: 1, FundingUserID: 1, Title: "Closed",
		TotalAmount: "10.00", MinPayout: "1.00", MaxPayout: "5.00", TierWeightsJSON: string(weights),
		ParticipantCount: 1, MaxParticipants: 10, Status: string(lotterydomain.StatusCompleted), AlgorithmVersion: "bounded-tier-v1",
		IdempotencyKey: "closed-entry-test", CreatedAt: now, UpdatedAt: now,
	}
	require.NoError(t, db.Create(&lottery).Error)
	entry := EntryModel{LotteryID: lottery.ID, UserID: 2, RegisteredAt: now.Add(-24 * time.Hour), CreatedAt: now}
	require.NoError(t, db.Create(&entry).Error)

	result, err := NewRepo(db).AddEntry(context.Background(), lottery.ID, entry.UserID, entry.RegisteredAt, func() time.Time { return now.Add(time.Minute) })
	require.NoError(t, err)
	require.True(t, result.AlreadyExists)
	require.Equal(t, entry.ID, result.Entry.ID)
}

func TestAddEntryUsesFreshClockForDrawDeadline(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:lottery-entry-deadline-%s?mode=memory&cache=shared", t.Name())), &gorm.Config{TranslateError: true})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&LotteryModel{}, &EntryModel{}, &PayoutModel{}))
	weights, err := json.Marshal(lotterydomain.TierWeights{Consolation: 80, Normal: 15, Lucky: 5})
	require.NoError(t, err)
	start := time.Now().UTC()
	drawAt := start.Add(time.Minute)
	lottery := LotteryModel{
		PublicToken: "deadline-lottery", CreatedByUserID: 1, FundingUserID: 1, Title: "Deadline",
		TotalAmount: "100.00", MinPayout: "1.00", MaxPayout: "20.00", TierWeightsJSON: string(weights),
		DrawAt: &drawAt, MaxParticipants: 10, Status: string(lotterydomain.StatusOpen), AlgorithmVersion: "bounded-tier-v1",
		IdempotencyKey: "deadline-test", CreatedAt: start, UpdatedAt: start,
	}
	require.NoError(t, db.Create(&lottery).Error)

	_, err = NewRepo(db).AddEntry(context.Background(), lottery.ID, 2, start, func() time.Time { return drawAt.Add(time.Second) })
	require.ErrorIs(t, err, lotterydomain.ErrLotteryClosed)
}

func TestRecordBillingTransactionsRejectsMissingPayout(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:lottery-transactions-%s?mode=memory&cache=shared", t.Name())), &gorm.Config{TranslateError: true})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&LotteryModel{}, &EntryModel{}, &PayoutModel{}))
	weights, err := json.Marshal(lotterydomain.TierWeights{Consolation: 80, Normal: 15, Lucky: 5})
	require.NoError(t, err)
	now := time.Now().UTC()
	lottery := LotteryModel{
		PublicToken: "missing-payout", CreatedByUserID: 1, FundingUserID: 1, Title: "Missing payout",
		TotalAmount: "10.00", MinPayout: "1.00", MaxPayout: "5.00", TierWeightsJSON: string(weights),
		MaxParticipants: 10, Status: string(lotterydomain.StatusSettling), AlgorithmVersion: "bounded-tier-v1",
		IdempotencyKey: "missing-payout-test", CreatedAt: now, UpdatedAt: now,
	}
	require.NoError(t, db.Create(&lottery).Error)

	err = NewRepo(db).RecordBillingTransactions(context.Background(), lottery.ID, map[uint]string{2: "TX-MISSING"}, "0.00")
	require.ErrorContains(t, err, "payout")
}

func TestRecordBillingTransactionsIsIdempotentAndRejectsTransactionReplacement(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:lottery-transaction-idempotency-%s?mode=memory&cache=shared", t.Name())), &gorm.Config{TranslateError: true})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&LotteryModel{}, &EntryModel{}, &PayoutModel{}))
	weights, err := json.Marshal(lotterydomain.TierWeights{Consolation: 80, Normal: 15, Lucky: 5})
	require.NoError(t, err)
	now := time.Now().UTC()
	lottery := LotteryModel{
		PublicToken: "transaction-idempotency", CreatedByUserID: 1, FundingUserID: 1, Title: "Transaction idempotency",
		TotalAmount: "10.00", MinPayout: "1.00", MaxPayout: "5.00", TierWeightsJSON: string(weights),
		ParticipantTarget: intPtr(2), MaxParticipants: 2, Status: string(lotterydomain.StatusSettling), AlgorithmVersion: "bounded-tier-v1",
		IdempotencyKey: "transaction-idempotency-test", CreatedAt: now, UpdatedAt: now,
	}
	require.NoError(t, db.Create(&lottery).Error)
	require.NoError(t, db.Create(&PayoutModel{LotteryID: lottery.ID, UserID: 2, Tier: string(lotterydomain.TierConsolation), Amount: "4.00", CreatedAt: now}).Error)
	repo := NewRepo(db)
	require.NoError(t, repo.RecordBillingTransactions(context.Background(), lottery.ID, map[uint]string{2: "TX-ONE"}, "6.00"))
	require.NoError(t, repo.RecordBillingTransactions(context.Background(), lottery.ID, map[uint]string{2: "TX-ONE"}, "6.00"))

	err = repo.RecordBillingTransactions(context.Background(), lottery.ID, map[uint]string{2: "TX-TWO"}, "6.00")
	require.ErrorContains(t, err, "payout")
}

func TestLookupWinnerStatsAggregatesLuckyAndConsolationAwards(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:lottery-history-%s-%d?mode=memory&cache=shared", t.Name(), time.Now().UnixNano())), &gorm.Config{TranslateError: true})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&PayoutModel{}))
	now := time.Now().UTC()
	rows := []PayoutModel{
		{LotteryID: 1, UserID: 7, Tier: string(lotterydomain.TierLucky), Amount: "20.00", CreatedAt: now},
		{LotteryID: 2, UserID: 7, Tier: string(lotterydomain.TierConsolation), Amount: "1.00", CreatedAt: now},
		{LotteryID: 3, UserID: 7, Tier: string(lotterydomain.TierConsolation), Amount: "1.00", CreatedAt: now},
		{LotteryID: 4, UserID: 8, Tier: string(lotterydomain.TierLucky), Amount: "20.00", CreatedAt: now},
		{LotteryID: 5, UserID: 8, Tier: string(lotterydomain.TierNormal), Amount: "5.00", CreatedAt: now},
	}
	require.NoError(t, db.Create(&rows).Error)

	stats, err := NewRepo(db).LookupWinnerStats(context.Background(), []uint{7, 8, 9})
	require.NoError(t, err)
	require.Equal(t, lotteryapp.WinnerStats{LuckyCount: 1, ConsolationCount: 2}, stats[7])
	require.Equal(t, lotteryapp.WinnerStats{LuckyCount: 1}, stats[8])
	_, ok := stats[9]
	require.False(t, ok)
}

func TestLookupWinnerStatsIgnoresProvisionalPayouts(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:lottery-history-final-%s-%d?mode=memory&cache=shared", t.Name(), time.Now().UnixNano())), &gorm.Config{TranslateError: true})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&LotteryModel{}, &PayoutModel{}))
	now := time.Now().UTC()
	weights, err := json.Marshal(lotterydomain.TierWeights{Normal: 1, Lucky: 1})
	require.NoError(t, err)
	completed := LotteryModel{
		PublicToken: "completed-history", CreatedByUserID: 1, FundingUserID: 1, Title: "Completed",
		TotalAmount: "10.00", MinPayout: "1.00", MaxPayout: "10.00", TierWeightsJSON: string(weights),
		MaxParticipants: 2, Status: string(lotterydomain.StatusCompleted), AlgorithmVersion: "fixed-tier-v3",
		IdempotencyKey: "completed-history", CreatedAt: now, UpdatedAt: now,
	}
	settling := completed
	settling.PublicToken = "settling-history"
	settling.IdempotencyKey = "settling-history"
	settling.Status = string(lotterydomain.StatusSettling)
	require.NoError(t, db.Create(&completed).Error)
	require.NoError(t, db.Create(&settling).Error)
	require.NoError(t, db.Create(&PayoutModel{LotteryID: completed.ID, UserID: 7, Tier: string(lotterydomain.TierLucky), Amount: "10.00", CreatedAt: now}).Error)
	require.NoError(t, db.Create(&PayoutModel{LotteryID: settling.ID, UserID: 7, Tier: string(lotterydomain.TierConsolation), Amount: "1.00", CreatedAt: now}).Error)

	stats, err := NewRepo(db).LookupWinnerStats(context.Background(), []uint{7})
	require.NoError(t, err)
	require.Equal(t, lotteryapp.WinnerStats{LuckyCount: 1}, stats[7])
}

func intPtr(value int) *int { return &value }
