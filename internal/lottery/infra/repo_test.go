package infra

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

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

	result, err := NewRepo(db).AddEntry(context.Background(), lottery.ID, entry.UserID, entry.RegisteredAt, now.Add(time.Minute))
	require.NoError(t, err)
	require.True(t, result.AlreadyExists)
	require.Equal(t, entry.ID, result.Entry.ID)
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

func intPtr(value int) *int { return &value }
