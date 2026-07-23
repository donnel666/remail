package infra

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	billingapp "github.com/donnel666/remail/internal/billing/app"
	"github.com/donnel666/remail/internal/billing/domain"
	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	"github.com/donnel666/remail/internal/platform/testmysql"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

var billingMySQLTestServer = testmysql.New("remail_billing_test")

func TestMain(m *testing.M) {
	code := m.Run()
	_ = billingMySQLTestServer.Close(context.Background())
	os.Exit(code)
}

func newBillingMySQLTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	return billingMySQLTestServer.Database(t, billingMigrationsDir(t))
}

func billingInnoDBMetricCount(t *testing.T, db *gorm.DB, name string) uint64 {
	t.Helper()
	var count uint64
	require.NoError(t, db.Raw(`SELECT COUNT FROM information_schema.innodb_metrics WHERE NAME = ?`, name).Scan(&count).Error)
	return count
}

func billingMigrationsDir(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	return filepath.Clean(filepath.Join(filepath.Dir(file), "../../..", "migrations"))
}

func TestBillingRepoRedeemCardMySQL(t *testing.T) {
	db := newBillingMySQLTestDB(t)
	ctx := context.Background()
	userID := createBillingTestUser(t, db, "buyer@example.com")
	repo := NewBillingRepo(db)

	require.NoError(t, db.Create(&CardKeyModel{
		Key:            "CARD-001",
		Amount:         "25.50",
		Status:         string(domain.CardKeyStatusEnabled),
		MaxRedemptions: 1,
	}).Error)

	result, err := repo.RedeemCard(ctx, billingapp.RedeemCardCommand{
		UserID:             userID,
		CardKey:            "CARD-001",
		IdempotencyKey:     "idem-card-001",
		RequestFingerprint: "fingerprint-card-001",
		RequestID:          "req-card-001",
		Now:                time.Now().UTC(),
	})
	require.NoError(t, err)
	require.Equal(t, "25.50", result.Wallet.ConsumerBalance)
	require.Equal(t, "25.50", result.Transaction.Amount)
	require.Equal(t, domain.TransactionTypeCardRedeem, result.Transaction.TransactionType)
	require.Equal(t, 1, result.Card.RedeemedCount)

	summary, err := repo.GetOrCreateWalletSummary(ctx, userID)
	require.NoError(t, err)
	require.Equal(t, "25.50", summary.Wallet.ConsumerBalance)

	replay, err := repo.RedeemCard(ctx, billingapp.RedeemCardCommand{
		UserID:             userID,
		CardKey:            "CARD-001",
		IdempotencyKey:     "idem-card-001",
		RequestFingerprint: "fingerprint-card-001",
		RequestID:          "req-card-001-retry",
		Now:                time.Now().UTC(),
	})
	require.NoError(t, err)
	require.Equal(t, result.Transaction.TransactionNo, replay.Transaction.TransactionNo)
	require.Equal(t, "25.50", replay.Wallet.ConsumerBalance)

	_, err = repo.RedeemCard(ctx, billingapp.RedeemCardCommand{
		UserID:             userID,
		CardKey:            "CARD-001",
		IdempotencyKey:     "idem-card-001",
		RequestFingerprint: "different-fingerprint",
		RequestID:          "req-card-001-conflict",
		Now:                time.Now().UTC(),
	})
	require.ErrorIs(t, err, domain.ErrIdempotencyConflict)

	_, err = repo.RedeemCard(ctx, billingapp.RedeemCardCommand{
		UserID:             userID,
		CardKey:            "CARD-001",
		IdempotencyKey:     "idem-card-002",
		RequestFingerprint: "fingerprint-card-002",
		RequestID:          "req-card-002",
		Now:                time.Now().UTC(),
	})
	require.ErrorIs(t, err, domain.ErrCardAlreadyRedeemed)

	otherUserID := createBillingTestUser(t, db, "other-buyer@example.com")
	_, err = repo.RedeemCard(ctx, billingapp.RedeemCardCommand{
		UserID:             otherUserID,
		CardKey:            "CARD-001",
		IdempotencyKey:     "idem-card-003",
		RequestFingerprint: "fingerprint-card-003",
		RequestID:          "req-card-003",
		Now:                time.Now().UTC(),
	})
	require.ErrorIs(t, err, domain.ErrCardExhausted)
}

func TestBillingRepoReferralRewardOnFirstCardRedemptionMySQL(t *testing.T) {
	db := newBillingMySQLTestDB(t)
	ctx := context.Background()
	inviterID := createBillingTestUser(t, db, "inviter@example.com")
	inviteeID := createBillingTestUser(t, db, "invitee@example.com")
	secondInviteeID := createBillingTestUser(t, db, "invitee-two@example.com")
	repo := NewBillingRepo(db)

	require.NoError(t, db.Exec(
		"INSERT INTO invites(code, invite_kind, enabled, max_use, used, created_by_user_id, referral_owner_user_id) VALUES (?, ?, ?, ?, ?, ?, ?)",
		"AFFTEST000000001",
		"referral",
		true,
		100,
		1,
		inviterID,
		inviterID,
	).Error)
	require.NoError(t, db.Exec(
		"INSERT INTO invite_uses(invite_code, user_id) VALUES (?, ?)",
		"AFFTEST000000001",
		inviteeID,
	).Error)
	require.NoError(t, db.Exec(
		"INSERT INTO invite_uses(invite_code, user_id) VALUES (?, ?)",
		"AFFTEST000000001",
		secondInviteeID,
	).Error)
	require.NoError(t, db.Create(&[]CardKeyModel{
		{Key: "CARD-REF-001", Amount: "100.00", Status: string(domain.CardKeyStatusEnabled), MaxRedemptions: 1},
		{Key: "CARD-REF-002", Amount: "50.00", Status: string(domain.CardKeyStatusEnabled), MaxRedemptions: 1},
		{Key: "CARD-REF-003", Amount: "25.00", Status: string(domain.CardKeyStatusEnabled), MaxRedemptions: 1},
	}).Error)

	first, err := repo.RedeemCard(ctx, billingapp.RedeemCardCommand{
		UserID:             inviteeID,
		CardKey:            "CARD-REF-001",
		IdempotencyKey:     "idem-ref-card-001",
		RequestFingerprint: "fingerprint-ref-card-001",
		RequestID:          "req-ref-card-001",
		Now:                time.Now().UTC(),
	})
	require.NoError(t, err)
	require.Equal(t, "100.00", first.Wallet.ConsumerBalance)

	inviterWallet, err := repo.GetOrCreateWalletSummary(ctx, inviterID)
	require.NoError(t, err)
	require.Equal(t, "0.00", inviterWallet.Wallet.ConsumerBalance)
	referrals, err := repo.GetReferralSummary(ctx, inviterID)
	require.NoError(t, err)
	require.EqualValues(t, 2, referrals.InviteCount)
	require.Equal(t, "80.00", referrals.TotalEarned)
	require.Equal(t, "80.00", referrals.PendingRewards)

	second, err := repo.RedeemCard(ctx, billingapp.RedeemCardCommand{
		UserID:             inviteeID,
		CardKey:            "CARD-REF-002",
		IdempotencyKey:     "idem-ref-card-002",
		RequestFingerprint: "fingerprint-ref-card-002",
		RequestID:          "req-ref-card-002",
		Now:                time.Now().UTC(),
	})
	require.NoError(t, err)
	require.Equal(t, "150.00", second.Wallet.ConsumerBalance)

	inviterWallet, err = repo.GetOrCreateWalletSummary(ctx, inviterID)
	require.NoError(t, err)
	require.Equal(t, "0.00", inviterWallet.Wallet.ConsumerBalance)

	var rewardCount int64
	require.NoError(t, db.Model(&ReferralRewardModel{}).Where("invitee_user_id = ?", inviteeID).Count(&rewardCount).Error)
	require.EqualValues(t, 1, rewardCount)

	third, err := repo.RedeemCard(ctx, billingapp.RedeemCardCommand{
		UserID:             secondInviteeID,
		CardKey:            "CARD-REF-003",
		IdempotencyKey:     "idem-ref-card-003",
		RequestFingerprint: "fingerprint-ref-card-003",
		RequestID:          "req-ref-card-003",
		Now:                time.Now().UTC(),
	})
	require.NoError(t, err)
	require.Equal(t, "25.00", third.Wallet.ConsumerBalance)

	transfer, err := repo.TransferReferralRewards(ctx, billingapp.TransferReferralRewardsCommand{
		UserID:             inviterID,
		IdempotencyKey:     "idem-ref-transfer-001",
		RequestFingerprint: "fingerprint-ref-transfer-001",
		RequestID:          "req-ref-transfer-001",
		Now:                time.Now().UTC(),
	})
	require.NoError(t, err)
	require.Equal(t, "100.00", transfer.TransferredAmount)
	require.Equal(t, 2, transfer.TransferredCount)
	require.Equal(t, "100.00", transfer.Wallet.ConsumerBalance)

	referrals, err = repo.GetReferralSummary(ctx, inviterID)
	require.NoError(t, err)
	require.Equal(t, "100.00", referrals.TotalEarned)
	require.Equal(t, "0.00", referrals.PendingRewards)

	var transferredRewards []ReferralRewardModel
	require.NoError(t, db.Model(&ReferralRewardModel{}).
		Where("inviter_user_id = ? AND status = ?", inviterID, "transferred").
		Order("id ASC").
		Find(&transferredRewards).Error)
	require.Len(t, transferredRewards, 2)
	require.NotNil(t, transferredRewards[0].TransferTransactionID)
	require.NotNil(t, transferredRewards[1].TransferTransactionID)
	require.Equal(t, *transferredRewards[0].TransferTransactionID, *transferredRewards[1].TransferTransactionID)

	var transferTransactions int64
	require.NoError(t, db.Model(&WalletTransactionModel{}).
		Where("user_id = ? AND biz_type = ?", inviterID, "referral_transfer").
		Count(&transferTransactions).Error)
	require.EqualValues(t, 1, transferTransactions)
}

func TestBillingRepoReferralRewardConstraintsMySQL(t *testing.T) {
	db := newBillingMySQLTestDB(t)
	inviterID := createBillingTestUser(t, db, "constraint-inviter@example.com")
	inviteeID := createBillingTestUser(t, db, "constraint-invitee@example.com")
	otherID := createBillingTestUser(t, db, "constraint-other@example.com")

	require.NoError(t, db.Exec(
		"INSERT INTO invites(code, invite_kind, enabled, max_use, used, created_by_user_id, referral_owner_user_id) VALUES (?, ?, ?, ?, ?, ?, ?)",
		"AFFCONSTRAINT001",
		"referral",
		true,
		100,
		1,
		inviterID,
		inviterID,
	).Error)
	require.NoError(t, db.Exec(
		"INSERT INTO invites(code, invite_kind, enabled, max_use, used, created_by_user_id, referral_owner_user_id) VALUES (?, ?, ?, ?, ?, ?, ?)",
		"ADMINCONSTRAINT",
		"admin",
		true,
		100,
		0,
		inviterID,
		nil,
	).Error)
	require.NoError(t, db.Exec(
		"INSERT INTO invite_uses(invite_code, user_id) VALUES (?, ?)",
		"AFFCONSTRAINT001",
		inviteeID,
	).Error)

	source := WalletTransactionModel{
		TransactionNo:   "TX-CONSTRAINT-SOURCE",
		UserID:          inviteeID,
		TransactionType: string(domain.TransactionTypeCardRedeem),
		BalanceBucket:   string(domain.BalanceBucketConsumer),
		Direction:       string(domain.TransactionDirectionIn),
		Amount:          "10.00",
		BalanceBefore:   "0.00",
		BalanceAfter:    "10.00",
		BizType:         "card_redeem",
		BizID:           "constraint-source",
	}
	require.NoError(t, db.Create(&source).Error)
	transfer := WalletTransactionModel{
		TransactionNo:   "TX-CONSTRAINT-TRANSFER",
		UserID:          inviterID,
		TransactionType: string(domain.TransactionTypeCredit),
		BalanceBucket:   string(domain.BalanceBucketConsumer),
		Direction:       string(domain.TransactionDirectionIn),
		Amount:          "8.00",
		BalanceBefore:   "0.00",
		BalanceAfter:    "8.00",
		BizType:         "referral_transfer",
		BizID:           "constraint-transfer",
	}
	require.NoError(t, db.Create(&transfer).Error)

	require.Error(t, db.Create(&ReferralRewardModel{
		InviterUserID:       inviterID,
		InviteeUserID:       inviteeID,
		InviteCode:          "ADMINCONSTRAINT",
		SourceTransactionID: source.ID,
		SourceAmount:        "10.00",
		RewardAmount:        "8.00",
		Status:              "available",
	}).Error)
	require.Error(t, db.Create(&ReferralRewardModel{
		InviterUserID:       otherID,
		InviteeUserID:       inviteeID,
		InviteCode:          "AFFCONSTRAINT001",
		SourceTransactionID: source.ID,
		SourceAmount:        "10.00",
		RewardAmount:        "8.00",
		Status:              "available",
	}).Error)
	require.Error(t, db.Create(&ReferralRewardModel{
		InviterUserID:       inviterID,
		InviteeUserID:       otherID,
		InviteCode:          "AFFCONSTRAINT001",
		SourceTransactionID: source.ID,
		SourceAmount:        "10.00",
		RewardAmount:        "8.00",
		Status:              "available",
	}).Error)
	require.Error(t, db.Create(&ReferralRewardModel{
		InviterUserID:       inviterID,
		InviteeUserID:       inviteeID,
		InviteCode:          "AFFCONSTRAINT001",
		SourceTransactionID: source.ID,
		SourceAmount:        "10.00",
		RewardAmount:        "8.00",
		Status:              "transferred",
	}).Error)

	now := time.Now().UTC()
	require.NoError(t, db.Create(&ReferralRewardModel{
		InviterUserID:         inviterID,
		InviteeUserID:         inviteeID,
		InviteCode:            "AFFCONSTRAINT001",
		SourceTransactionID:   source.ID,
		TransferTransactionID: &transfer.ID,
		SourceAmount:          "10.00",
		RewardAmount:          "8.00",
		Status:                "transferred",
		TransferredAt:         &now,
	}).Error)
}

func TestBillingRepoAdjustConsumerBalanceMySQL(t *testing.T) {
	db := newBillingMySQLTestDB(t)
	ctx := context.Background()
	userID := createBillingTestUser(t, db, "adjust@example.com")
	repo := NewBillingRepo(db)

	credited, err := repo.AdjustConsumerBalance(ctx, billingapp.AdjustConsumerBalanceCommand{
		UserID:             userID,
		Amount:             "10.00",
		Reason:             "manual credit",
		TransactionType:    domain.TransactionTypeCredit,
		Direction:          domain.TransactionDirectionIn,
		IdempotencyKey:     "idem-credit-001",
		RequestFingerprint: "fingerprint-credit-001",
		RequestID:          "req-credit-001",
		Now:                time.Now().UTC(),
	})
	require.NoError(t, err)
	require.Equal(t, "10.00", credited.Wallet.ConsumerBalance)

	debited, err := repo.AdjustConsumerBalance(ctx, billingapp.AdjustConsumerBalanceCommand{
		UserID:             userID,
		Amount:             "4.50",
		Reason:             "manual debit",
		TransactionType:    domain.TransactionTypeDebit,
		Direction:          domain.TransactionDirectionOut,
		IdempotencyKey:     "idem-debit-001",
		RequestFingerprint: "fingerprint-debit-001",
		RequestID:          "req-debit-001",
		Now:                time.Now().UTC(),
	})
	require.NoError(t, err)
	require.Equal(t, "5.50", debited.Wallet.ConsumerBalance)
	require.Equal(t, "-4.50", debited.Transaction.Amount)
	require.Equal(t, "10.00", debited.Transaction.BalanceBefore)
	require.Equal(t, "5.50", debited.Transaction.BalanceAfter)

	zeroDebit, err := repo.AdjustConsumerBalance(ctx, billingapp.AdjustConsumerBalanceCommand{
		UserID:             userID,
		Amount:             "0.00",
		Reason:             "private stock order",
		TransactionType:    domain.TransactionTypeDebit,
		Direction:          domain.TransactionDirectionOut,
		IdempotencyKey:     "idem-debit-zero",
		RequestFingerprint: "fingerprint-debit-zero",
		RequestID:          "req-debit-zero",
		Now:                time.Now().UTC(),
	})
	require.NoError(t, err)
	require.Equal(t, "5.50", zeroDebit.Wallet.ConsumerBalance)
	require.Equal(t, "0.00", zeroDebit.Transaction.Amount)
	require.Equal(t, "5.50", zeroDebit.Transaction.BalanceBefore)
	require.Equal(t, "5.50", zeroDebit.Transaction.BalanceAfter)

	summary, err := repo.GetOrCreateWalletSummary(ctx, userID)
	require.NoError(t, err)
	require.Equal(t, "4.50", summary.HistoricalSpend)
	require.EqualValues(t, 2, summary.OrderCount)

	require.Error(t, db.Create(&WalletTransactionModel{
		TransactionNo:   "TX-DIR-CONSTRAINT",
		UserID:          userID,
		TransactionType: string(domain.TransactionTypeDebit),
		BalanceBucket:   string(domain.BalanceBucketConsumer),
		Direction:       string(domain.TransactionDirectionIn),
		Amount:          "1.00",
		BalanceBefore:   "5.50",
		BalanceAfter:    "6.50",
		BizType:         "constraint",
		BizID:           "direction",
	}).Error)

	_, err = repo.AdjustConsumerBalance(ctx, billingapp.AdjustConsumerBalanceCommand{
		UserID:             userID,
		Amount:             "6.00",
		Reason:             "manual debit too much",
		TransactionType:    domain.TransactionTypeDebit,
		Direction:          domain.TransactionDirectionOut,
		IdempotencyKey:     "idem-debit-002",
		RequestFingerprint: "fingerprint-debit-002",
		RequestID:          "req-debit-002",
		Now:                time.Now().UTC(),
	})
	require.ErrorIs(t, err, domain.ErrInsufficientBalance)

	clamped, err := repo.AdjustConsumerBalance(ctx, billingapp.AdjustConsumerBalanceCommand{
		UserID:             userID,
		Amount:             "100.00",
		Reason:             "bulk clear",
		TransactionType:    domain.TransactionTypeDebit,
		Direction:          domain.TransactionDirectionOut,
		ClampToBalance:     true,
		IdempotencyKey:     "idem-debit-clamp",
		RequestFingerprint: "fingerprint-debit-clamp",
		RequestID:          "req-debit-clamp",
		Now:                time.Now().UTC(),
	})
	require.NoError(t, err)
	require.Equal(t, "0.00", clamped.Wallet.ConsumerBalance)
	require.Equal(t, "-5.50", clamped.Transaction.Amount)
}

func TestBillingRepoConsumerBalanceSixDecimalPrecisionMySQL(t *testing.T) {
	db := newBillingMySQLTestDB(t)
	ctx := context.Background()
	userID := createBillingTestUser(t, db, "precision@example.com")
	repo := NewBillingRepo(db)
	now := time.Now().UTC()

	credited, err := repo.AdjustConsumerBalance(ctx, billingapp.AdjustConsumerBalanceCommand{
		UserID:             userID,
		Amount:             "0.024",
		Reason:             "precision credit",
		TransactionType:    domain.TransactionTypeCredit,
		Direction:          domain.TransactionDirectionIn,
		IdempotencyKey:     "idem-precision-credit",
		RequestFingerprint: "fingerprint-precision-credit",
		RequestID:          "req-precision-credit",
		Now:                now,
	})
	require.NoError(t, err)
	require.Equal(t, "0.024", credited.Wallet.ConsumerBalance)

	debited, err := repo.AdjustConsumerBalance(ctx, billingapp.AdjustConsumerBalanceCommand{
		UserID:             userID,
		Amount:             "0.008",
		Reason:             "precision debit",
		TransactionType:    domain.TransactionTypeDebit,
		Direction:          domain.TransactionDirectionOut,
		IdempotencyKey:     "idem-precision-debit",
		RequestFingerprint: "fingerprint-precision-debit",
		RequestID:          "req-precision-debit",
		Now:                now,
	})
	require.NoError(t, err)
	require.Equal(t, "0.016", debited.Wallet.ConsumerBalance)
	require.Equal(t, "-0.008", debited.Transaction.Amount)
	require.Equal(t, "0.024", debited.Transaction.BalanceBefore)
	require.Equal(t, "0.016", debited.Transaction.BalanceAfter)

	summary, err := repo.GetOrCreateWalletSummary(ctx, userID)
	require.NoError(t, err)
	require.Equal(t, "0.016", summary.Wallet.ConsumerBalance)
	require.Equal(t, "0.008", summary.HistoricalSpend)
	require.EqualValues(t, 1, summary.OrderCount)

	var storedDebit WalletTransactionModel
	require.NoError(t, db.First(&storedDebit, "id = ?", debited.Transaction.ID).Error)
	require.Equal(t, "-0.008000", storedDebit.Amount)
	require.Equal(t, "0.024000", storedDebit.BalanceBefore)
	require.Equal(t, "0.016000", storedDebit.BalanceAfter)

	refunded, err := repo.AdjustConsumerBalance(ctx, billingapp.AdjustConsumerBalanceCommand{
		UserID:             userID,
		Amount:             "0.008",
		Reason:             "precision refund",
		TransactionType:    domain.TransactionTypeRefund,
		Direction:          domain.TransactionDirectionIn,
		IdempotencyKey:     "idem-precision-refund",
		RequestFingerprint: "fingerprint-precision-refund",
		RequestID:          "req-precision-refund",
		Now:                now,
	})
	require.NoError(t, err)
	require.Equal(t, "0.024", refunded.Wallet.ConsumerBalance)
	require.Equal(t, "0.008", refunded.Transaction.Amount)
	require.Equal(t, "0.016", refunded.Transaction.BalanceBefore)
	require.Equal(t, "0.024", refunded.Transaction.BalanceAfter)

	finalSummary, err := repo.GetOrCreateWalletSummary(ctx, userID)
	require.NoError(t, err)
	require.Equal(t, "0.024", finalSummary.Wallet.ConsumerBalance)
	require.Equal(t, "0.008", finalSummary.HistoricalSpend)
	require.EqualValues(t, 1, finalSummary.OrderCount)
}

func TestBillingRepoCreateCardsIdempotencyMySQL(t *testing.T) {
	db := newBillingMySQLTestDB(t)
	ctx := context.Background()
	userID := createBillingTestUser(t, db, "card-admin@example.com")
	repo := NewBillingRepo(db)

	command := billingapp.CreateCardsCommand{
		OwnerUserID:        userID,
		IdempotencyKey:     "idem-create-cards",
		RequestFingerprint: "fingerprint-create-cards",
		Cards: []domain.CardKey{
			{Key: "CREATE-CARD-001", Amount: "8.00", Status: domain.CardKeyStatusEnabled, MaxRedemptions: 1, CreatedByUserID: &userID},
			{Key: "CREATE-CARD-002", Amount: "8.00", Status: domain.CardKeyStatusEnabled, MaxRedemptions: 1, CreatedByUserID: &userID},
		},
	}
	created, err := repo.CreateCards(ctx, command)
	require.NoError(t, err)
	require.Len(t, created, 2)

	replayed, err := repo.CreateCards(ctx, command)
	require.NoError(t, err)
	require.Len(t, replayed, 2)
	require.Equal(t, created[0].Key, replayed[0].Key)
	require.Equal(t, created[1].Key, replayed[1].Key)
	require.Equal(t, created[0].Amount, replayed[0].Amount)
	require.Equal(t, created[1].Amount, replayed[1].Amount)

	conflictCommand := command
	conflictCommand.RequestFingerprint = "different-fingerprint"
	_, err = repo.CreateCards(ctx, conflictCommand)
	require.ErrorIs(t, err, domain.ErrIdempotencyConflict)

	var cardCount int64
	require.NoError(t, db.Model(&CardKeyModel{}).Where("created_by_user_id = ?", userID).Count(&cardCount).Error)
	require.EqualValues(t, 2, cardCount)
}

func TestBillingRepoConcurrentCardRedemptionMySQL(t *testing.T) {
	db := newBillingMySQLTestDB(t)
	ctx := context.Background()
	repo := NewBillingRepo(db)

	require.NoError(t, db.Create(&CardKeyModel{
		Key:            "CARD-CONCURRENT",
		Amount:         "3.00",
		Status:         string(domain.CardKeyStatusEnabled),
		MaxRedemptions: 1,
	}).Error)

	const workers = 32
	userIDs := make([]uint, 0, workers)
	for i := 0; i < workers; i++ {
		userIDs = append(userIDs, createBillingTestUser(t, db, "card-worker-"+strconv.Itoa(i)+"@example.com"))
	}

	var successes int64
	var unexpected atomic.Value
	var wg sync.WaitGroup
	wg.Add(workers)
	for i, userID := range userIDs {
		go func(index int, userID uint) {
			defer wg.Done()
			_, err := repo.RedeemCard(ctx, billingapp.RedeemCardCommand{
				UserID:             userID,
				CardKey:            "CARD-CONCURRENT",
				IdempotencyKey:     "idem-card-concurrent-" + strconv.Itoa(index),
				RequestFingerprint: "fingerprint-card-concurrent-" + strconv.Itoa(index),
				RequestID:          "req-card-concurrent-" + strconv.Itoa(index),
				Now:                time.Now().UTC(),
			})
			if err == nil {
				atomic.AddInt64(&successes, 1)
				return
			}
			if !errors.Is(err, domain.ErrCardExhausted) {
				unexpected.Store(err)
			}
		}(i, userID)
	}
	wg.Wait()
	require.Nil(t, unexpected.Load())
	require.EqualValues(t, 1, successes)

	var card CardKeyModel
	require.NoError(t, db.First(&card, "card_key = ?", "CARD-CONCURRENT").Error)
	require.Equal(t, 1, card.RedeemedCount)
	var redemptions int64
	require.NoError(t, db.Model(&CardKeyRedemptionModel{}).Where("card_key = ?", "CARD-CONCURRENT").Count(&redemptions).Error)
	require.EqualValues(t, 1, redemptions)
	var txCount int64
	require.NoError(t, db.Model(&WalletTransactionModel{}).Where("biz_type = ? AND biz_id = ?", "card_key", "CARD-CONCURRENT").Count(&txCount).Error)
	require.EqualValues(t, 1, txCount)
}

func TestBillingRepoConcurrentDebitBalanceNonNegativeMySQL(t *testing.T) {
	db := newBillingMySQLTestDB(t)
	ctx := context.Background()
	userID := createBillingTestUser(t, db, "debit-concurrent@example.com")
	repo := NewBillingRepo(db)

	_, err := repo.AdjustConsumerBalance(ctx, billingapp.AdjustConsumerBalanceCommand{
		UserID:             userID,
		Amount:             "10.00",
		Reason:             "seed balance",
		TransactionType:    domain.TransactionTypeCredit,
		Direction:          domain.TransactionDirectionIn,
		IdempotencyKey:     "idem-debit-seed",
		RequestFingerprint: "fingerprint-debit-seed",
		RequestID:          "req-debit-seed",
		Now:                time.Now().UTC(),
	})
	require.NoError(t, err)

	const workers = 40
	var successes int64
	var unexpected atomic.Value
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func(index int) {
			defer wg.Done()
			_, err := repo.AdjustConsumerBalance(ctx, billingapp.AdjustConsumerBalanceCommand{
				UserID:             userID,
				Amount:             "1.00",
				Reason:             "concurrent debit",
				TransactionType:    domain.TransactionTypeDebit,
				Direction:          domain.TransactionDirectionOut,
				IdempotencyKey:     "idem-debit-concurrent-" + strconv.Itoa(index),
				RequestFingerprint: "fingerprint-debit-concurrent-" + strconv.Itoa(index),
				RequestID:          "req-debit-concurrent-" + strconv.Itoa(index),
				Now:                time.Now().UTC(),
			})
			if err == nil {
				atomic.AddInt64(&successes, 1)
				return
			}
			if !errors.Is(err, domain.ErrInsufficientBalance) {
				unexpected.Store(err)
			}
		}(i)
	}
	wg.Wait()
	require.Nil(t, unexpected.Load())
	require.EqualValues(t, 10, successes)

	summary, err := repo.GetOrCreateWalletSummary(ctx, userID)
	require.NoError(t, err)
	require.Equal(t, "0.00", summary.Wallet.ConsumerBalance)
	var debitTransactions int64
	require.NoError(t, db.Model(&WalletTransactionModel{}).
		Where("user_id = ? AND transaction_type = ? AND direction = ?", userID, domain.TransactionTypeDebit, domain.TransactionDirectionOut).
		Count(&debitTransactions).Error)
	require.EqualValues(t, 10, debitTransactions)
}

func TestBillingRepoConcurrentFirstCreditsCreateOneWalletMySQL(t *testing.T) {
	db := newBillingMySQLTestDB(t)
	ctx := context.Background()
	userID := createBillingTestUser(t, db, "credit-first-concurrent@example.com")
	repo := NewBillingRepo(db)

	const workers = 16
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			_, err := repo.AdjustConsumerBalance(ctx, billingapp.AdjustConsumerBalanceCommand{
				UserID:             userID,
				Amount:             "1.00",
				Reason:             "concurrent first credit",
				TransactionType:    domain.TransactionTypeCredit,
				Direction:          domain.TransactionDirectionIn,
				IdempotencyKey:     "idem-credit-first-concurrent-" + strconv.Itoa(index),
				RequestFingerprint: "fingerprint-credit-first-concurrent-" + strconv.Itoa(index),
				RequestID:          "req-credit-first-concurrent-" + strconv.Itoa(index),
				Now:                time.Now().UTC(),
			})
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}

	summary, err := repo.GetOrCreateWalletSummary(ctx, userID)
	require.NoError(t, err)
	require.Equal(t, "16.00", summary.Wallet.ConsumerBalance)
	var walletCount int64
	require.NoError(t, db.Model(&WalletModel{}).Where("user_id = ?", userID).Count(&walletCount).Error)
	require.EqualValues(t, 1, walletCount)
}

func TestBillingRepoWalletFirstAdjustCompetesWithDirectAdjustWithoutDeadlockMySQL(t *testing.T) {
	db := newBillingMySQLTestDB(t)
	userID := createBillingTestUser(t, db, "wallet-first-adjust@example.com")
	repo := NewBillingRepo(db)
	_, err := repo.GetOrCreateWalletSummary(context.Background(), userID)
	require.NoError(t, err)

	command := billingapp.AdjustConsumerBalanceCommand{
		UserID:             userID,
		Amount:             "5.00",
		Reason:             "wallet-first concurrency gate",
		TransactionType:    domain.TransactionTypeCredit,
		Direction:          domain.TransactionDirectionIn,
		IdempotencyKey:     "idem-wallet-first-concurrency",
		RequestFingerprint: "fingerprint-wallet-first-concurrency",
		RequestID:          "req-wallet-first-concurrency",
		Now:                time.Now().UTC(),
	}
	deadlocksBefore := billingInnoDBMetricCount(t, db, "lock_deadlocks")
	timeoutsBefore := billingInnoDBMetricCount(t, db, "lock_timeouts")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	type adjustOutcome struct {
		result *billingapp.AdjustBalanceResult
		err    error
	}
	type walletLockProbeKey struct{}
	walletLocked := make(chan struct{})
	directWalletLockAttempt := make(chan struct{})
	var probeOnce sync.Once
	const probeCallback = "test:direct_wallet_lock_attempt"
	require.NoError(t, db.Callback().Query().Before("gorm:query").Register(probeCallback, func(tx *gorm.DB) {
		_, locking := tx.Statement.Clauses["FOR"]
		if tx.Statement.Context.Value(walletLockProbeKey{}) == true && tx.Statement.Table == "wallets" && locking {
			probeOnce.Do(func() { close(directWalletLockAttempt) })
		}
	}))
	t.Cleanup(func() { require.NoError(t, db.Callback().Query().Remove(probeCallback)) })
	proceed := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(proceed) }) }
	defer release()
	outerResult := make(chan adjustOutcome, 1)
	go func() {
		var adjusted *billingapp.AdjustBalanceResult
		err := repo.withTx(ctx, func(txCtx context.Context, _ *gorm.DB) error {
			if err := repo.LockConsumerWallet(txCtx, userID); err != nil {
				return err
			}
			close(walletLocked)
			select {
			case <-proceed:
			case <-txCtx.Done():
				return txCtx.Err()
			}
			var err error
			adjusted, err = repo.AdjustConsumerBalance(txCtx, command)
			return err
		})
		outerResult <- adjustOutcome{result: adjusted, err: err}
	}()
	<-walletLocked

	directResult := make(chan adjustOutcome, 1)
	go func() {
		directCtx := context.WithValue(ctx, walletLockProbeKey{}, true)
		adjusted, err := repo.AdjustConsumerBalance(directCtx, command)
		directResult <- adjustOutcome{result: adjusted, err: err}
	}()
	select {
	case <-directWalletLockAttempt:
	case <-ctx.Done():
		require.NoError(t, ctx.Err())
	}
	release()

	outer := <-outerResult
	direct := <-directResult
	require.NoError(t, outer.err)
	require.NoError(t, direct.err)
	require.NotNil(t, outer.result)
	require.NotNil(t, direct.result)
	require.Equal(t, outer.result.Transaction.ID, direct.result.Transaction.ID)
	require.Equal(t, "5.00", outer.result.Wallet.ConsumerBalance)
	require.Equal(t, "5.00", direct.result.Wallet.ConsumerBalance)

	var transactionCount, receiptCount int64
	require.NoError(t, db.Model(&WalletTransactionModel{}).
		Where("user_id = ? AND idempotency_key = ?", userID, command.IdempotencyKey).
		Count(&transactionCount).Error)
	require.EqualValues(t, 1, transactionCount)
	require.NoError(t, db.Model(&IdempotencyKeyModel{}).
		Where("owner_user_id = ? AND idempotency_key = ? AND operation = ?", userID, command.IdempotencyKey, "wallet.adjust").
		Count(&receiptCount).Error)
	require.EqualValues(t, 1, receiptCount)
	require.Equal(t, deadlocksBefore, billingInnoDBMetricCount(t, db, "lock_deadlocks"))
	require.Equal(t, timeoutsBefore, billingInnoDBMetricCount(t, db, "lock_timeouts"))
}

func TestBillingRepoIndexesAndExplainMySQL(t *testing.T) {
	db := newBillingMySQLTestDB(t)
	ctx := context.Background()
	userID := createBillingTestUser(t, db, "wallet-explain@example.com")
	repo := NewBillingRepo(db)

	for _, tc := range []struct {
		table string
		index string
	}{
		{"wallet_transactions", "idx_wallet_transactions_user_created"},
		{"wallet_transactions", "idx_wallet_transactions_biz"},
		{"idempotency_keys", "idx_idempotency_owner_key_operation"},
		{"recharges", "idx_recharges_user_created"},
		{"recharges", "idx_recharges_status_created"},
		{"card_keys", "idx_card_keys_status_expire"},
		{"card_key_redemptions", "idx_card_redemptions_card_user"},
		{"invites", "idx_invites_code_referral_owner"},
		{"referral_rewards", "idx_referral_rewards_invitee"},
		{"referral_rewards", "idx_referral_rewards_inviter_created"},
		{"referral_rewards", "idx_referral_rewards_inviter_status"},
		{"referral_rewards", "idx_referral_rewards_transfer_transaction"},
	} {
		requireIndexExists(t, db, tc.table, tc.index)
	}

	for i := 0; i < 5; i++ {
		_, err := repo.AdjustConsumerBalance(ctx, billingapp.AdjustConsumerBalanceCommand{
			UserID:             userID,
			Amount:             "1.00",
			Reason:             "explain seed",
			TransactionType:    domain.TransactionTypeCredit,
			Direction:          domain.TransactionDirectionIn,
			IdempotencyKey:     "idem-explain-" + strconv.Itoa(i),
			RequestFingerprint: "fingerprint-explain-" + strconv.Itoa(i),
			RequestID:          "req-explain-" + strconv.Itoa(i),
			Now:                time.Now().UTC(),
		})
		require.NoError(t, err)
	}

	requireExplainUsesIndex(
		t,
		db,
		"idx_wallet_transactions_user_created",
		"EXPLAIN SELECT * FROM wallet_transactions WHERE user_id = ? ORDER BY created_at DESC, id DESC LIMIT 20",
		userID,
	)
}

func TestBillingRepoTransactionRollbackMySQL(t *testing.T) {
	db := newBillingMySQLTestDB(t)
	ctx := context.Background()
	userID := createBillingTestUser(t, db, "rollback@example.com")
	repo := NewBillingRepo(db)
	repo.operationLogs = failingOperationLogWriter{}

	_, err := repo.AdjustConsumerBalance(ctx, billingapp.AdjustConsumerBalanceCommand{
		UserID:             userID,
		Amount:             "9.00",
		Reason:             "rollback test",
		TransactionType:    domain.TransactionTypeCredit,
		Direction:          domain.TransactionDirectionIn,
		IdempotencyKey:     "idem-rollback-001",
		RequestFingerprint: "fingerprint-rollback-001",
		RequestID:          "req-rollback-001",
		Now:                time.Now().UTC(),
		OperationLog: &governancedomain.OperationLog{
			OperatorUserID: userID,
			OperationType:  "billing.wallet.credit",
			ResourceType:   "billing",
			ResourceID:     "rollback",
			Path:           "/v1/admin/wallets/1/credit",
			Result:         "success",
			SafeSummary:    "Wallet adjusted.",
			RequestID:      "req-rollback-001",
		},
	})
	require.ErrorContains(t, err, "forced operation log failure")

	summary, err := repo.GetOrCreateWalletSummary(ctx, userID)
	require.NoError(t, err)
	require.Equal(t, "0.00", summary.Wallet.ConsumerBalance)
	var transactionCount int64
	require.NoError(t, db.Model(&WalletTransactionModel{}).Where("user_id = ?", userID).Count(&transactionCount).Error)
	require.EqualValues(t, 0, transactionCount)
	var idempotencyCount int64
	require.NoError(t, db.Model(&IdempotencyKeyModel{}).Where("owner_user_id = ? AND idempotency_key = ?", userID, "idem-rollback-001").Count(&idempotencyCount).Error)
	require.EqualValues(t, 0, idempotencyCount)
}

type failingOperationLogWriter struct{}

func (failingOperationLogWriter) CreateInTx(context.Context, *gorm.DB, *governancedomain.OperationLog) error {
	return errors.New("forced operation log failure")
}

func requireIndexExists(t *testing.T, db *gorm.DB, tableName string, indexName string) {
	t.Helper()

	var count int64
	require.NoError(t, db.Raw(
		"SELECT COUNT(*) FROM information_schema.statistics WHERE table_schema = DATABASE() AND table_name = ? AND index_name = ?",
		tableName,
		indexName,
	).Scan(&count).Error)
	require.Positive(t, count, "expected index %s on %s", indexName, tableName)
}

func requireExplainUsesIndex(t *testing.T, db *gorm.DB, expectedKey string, query string, args ...any) {
	t.Helper()

	var rows []struct {
		Key        sql.NullString `gorm:"column:key"`
		Rows       sql.NullInt64  `gorm:"column:rows"`
		AccessType sql.NullString `gorm:"column:type"`
	}
	require.NoError(t, db.Raw(query, args...).Scan(&rows).Error)
	require.NotEmpty(t, rows, "expected EXPLAIN rows for %s", query)
	seenKeys := make([]string, 0, len(rows))
	usedExpectedKey := false
	for _, row := range rows {
		require.True(t, row.Key.Valid, "expected query to use an index: %s", query)
		seenKeys = append(seenKeys, row.Key.String)
		require.True(t, row.Rows.Valid, "expected query to expose row estimate: %s", query)
		require.LessOrEqual(t, row.Rows.Int64, int64(20), "unexpected row estimate for %s using %s", query, row.Key.String)
		require.NotEqual(t, "ALL", row.AccessType.String, "unexpected full table scan for %s", query)
		if row.Key.String == expectedKey {
			usedExpectedKey = true
		}
	}
	require.True(t, usedExpectedKey, "expected query to use index %s, saw %v: %s", expectedKey, seenKeys, query)
}

func createBillingTestUser(t *testing.T, db *gorm.DB, email string) uint {
	t.Helper()
	type userModel struct {
		ID           uint   `gorm:"primaryKey"`
		Email        string `gorm:"column:email"`
		PasswordHash string `gorm:"column:password_hash"`
		Nickname     string `gorm:"column:nickname"`
		Role         string `gorm:"column:role"`
	}
	user := userModel{
		Email:        email,
		PasswordHash: "hash",
		Nickname:     "Billing Test",
		Role:         "user",
	}
	require.NoError(t, db.Table("users").Create(&user).Error)
	return user.ID
}
