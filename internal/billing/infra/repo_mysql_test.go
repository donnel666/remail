package infra

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	billingapp "github.com/donnel666/remail/internal/billing/app"
	"github.com/donnel666/remail/internal/billing/domain"
	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	"github.com/donnel666/remail/internal/platform/testmysql"
	settingsdomain "github.com/donnel666/remail/internal/systemsettings/domain"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

var billingMySQLTestServer = testmysql.New("remail_billing_test")

func TestMain(m *testing.M) {
	code := m.Run()
	_ = billingMySQLTestServer.Close(context.Background())
	_ = pointsMigrationMySQLTestServer.Close(context.Background())
	os.Exit(code)
}

func newBillingMySQLTestDB(t *testing.T, dsnOptions ...string) *gorm.DB {
	t.Helper()
	return billingMySQLTestServer.Database(t, billingMigrationsDir(t), dsnOptions...)
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

func TestWalletSummaryIncludesSupplierFulfillmentMetricsMySQL(t *testing.T) {
	db := newBillingMySQLTestDB(t)
	ctx := context.Background()
	ownerID := createBillingTestUser(t, db, "supplier-metrics@example.com")
	newOwnerID := createBillingTestUser(t, db, "supplier-metrics-new-owner@example.com")
	buyerID := createBillingTestUser(t, db, "supplier-metrics-buyer@example.com")

	require.NoError(t, db.Exec(`
INSERT INTO mail_servers(id, owner_user_id, server_address, mx_record, status)
VALUES (9701, ?, 'mx.metrics.test', 'mx.metrics.test', 'online')`, ownerID).Error)
	require.NoError(t, db.Exec("INSERT INTO email_resources(id, type, owner_user_id) VALUES (9701, 'domain', ?)", ownerID).Error)
	require.NoError(t, db.Exec(`
INSERT INTO domain_resources(id, domain, owner_user_id, mail_server_id, purpose, status)
VALUES (9701, 'supplier-metrics.example.com', ?, 9701, 'sale', 'normal')`, ownerID).Error)
	require.NoError(t, db.Exec(`
INSERT INTO generated_mailboxes(id, resource_id, owner_user_id, email, status)
VALUES (9701, 9701, ?, 'orders@supplier-metrics.example.com', 'normal')`, ownerID).Error)
	require.NoError(t, db.Exec(`
INSERT INTO projects(id, name, target_platform, status)
VALUES (9701, 'Supplier Metrics', 'test', 'listed')`).Error)
	require.NoError(t, db.Exec(`
INSERT INTO project_products(id, project_id, type, main_weight, dot_weight, plus_weight)
VALUES (9701, 9701, 'domain', 0, 0, 0)`).Error)

	type metricOrder struct {
		orderNo     string
		scope       string
		paid        bool
		serviceMode string
		success     bool
		waiting     bool
	}
	orders := []metricOrder{
		{orderNo: "supplier-metrics-code-success", scope: "public", paid: true, serviceMode: "code", success: true},
		{orderNo: "supplier-metrics-purchase-success", scope: "public", paid: true, serviceMode: "purchase", success: true},
		{orderNo: "supplier-metrics-waiting", scope: "public", paid: true, serviceMode: "code", waiting: true},
		{orderNo: "supplier-metrics-failed", scope: "public", paid: true, serviceMode: "code"},
		{orderNo: "supplier-metrics-owned", scope: "owned", paid: true, serviceMode: "purchase", success: true},
		{orderNo: "supplier-metrics-unpaid", scope: "public", serviceMode: "code"},
		{orderNo: "HIST-supplier-metrics", scope: "public", paid: true, serviceMode: "purchase", success: true},
	}
	for i, item := range orders {
		orderNo := item.orderNo
		require.NoError(t, db.Exec("INSERT INTO allocation_order_guards(order_no, type) VALUES (?, 'domain')", orderNo).Error)
		require.NoError(t, db.Exec(`
	INSERT INTO domain_allocations(order_no, project_id, product_id, resource_id, supply_scope, mailbox_id, email, status, released_at)
	VALUES (?, 9701, 9701, 9701, ?, 9701, 'orders@supplier-metrics.example.com', 'released', NOW())`,
			orderNo, item.scope).Error)
		var debitTxID any
		if item.paid {
			transactionID := 9900 + i
			require.NoError(t, db.Exec(`
	INSERT INTO wallet_transactions(id, transaction_no, user_id, transaction_type, balance_bucket, direction, amount, balance_before, balance_after, biz_type, biz_id)
	VALUES (?, ?, ?, 'debit', 'consumer', 'out', 0, 0, 0, 'order', ?)`,
				transactionID, fmt.Sprintf("supplier-metrics-tx-%d", i), buyerID, orderNo).Error)
			debitTxID = transactionID
		}
		status := "closed"
		receiveUntil := time.Now().UTC().Add(-time.Minute)
		if item.waiting || item.success {
			status = "active"
			receiveUntil = time.Now().UTC().Add(time.Hour)
		}
		var activatedAt any
		if item.serviceMode == "purchase" && item.success {
			activatedAt = time.Now().UTC()
		}
		require.NoError(t, db.Exec(`
	INSERT INTO orders(
	    order_no, user_id, project_id, project_product_id, product_type, service_mode,
	    supply_policy, status, pay_amount, refund_amount, code_window_minutes,
	    activation_window_minutes, warranty_minutes, allocation_type, delivery_email,
	    debit_tx_id, receive_started_at, receive_until, activated_at,
	    client_channel, idempotency_key, request_fingerprint, service_cleanup_status
	)
	VALUES (?, ?, 9701, 9701, 'domain', ?, 'public_only', ?, 0, 0, 10, 10, 10, 'domain',
	        'orders@supplier-metrics.example.com', ?, NOW(), ?, ?, 'console', ?, ?, 'none')`,
			orderNo, buyerID, item.serviceMode, status, debitTxID, receiveUntil, activatedAt,
			orderNo, fmt.Sprintf("%064x", i+1)).Error)
		if item.serviceMode == "code" && item.success {
			var orderID uint
			require.NoError(t, db.Table("orders").Select("id").Where("order_no = ?", orderNo).Scan(&orderID).Error)
			require.NoError(t, db.Exec(`
	INSERT INTO mailmatch_messages(email_resource_id, resource_type, recipient, dedupe_key, status, received_at)
	VALUES (9701, 'domain', 'orders@supplier-metrics.example.com', ?, 'matched', NOW())`, fmt.Sprintf("%064x", 100+i)).Error)
			var messageID uint
			require.NoError(t, db.Table("mailmatch_messages").Select("id").Order("id DESC").Limit(1).Scan(&messageID).Error)
			require.NoError(t, db.Exec(`
INSERT INTO mailmatch_order_delivery_heads(order_id, message_id, message_received_at)
VALUES (?, ?, NOW())`, orderID, messageID).Error)
		}
	}
	repo := NewBillingRepo(db)
	summary, err := repo.GetOrCreateWalletSummary(ctx, ownerID)
	require.NoError(t, err)
	require.EqualValues(t, 4, summary.SupplierAllocationCount)
	require.Equal(t, 66.7, summary.SupplierFulfillmentSuccessRate)
	newOwnerSummary, err := NewBillingRepo(db).GetOrCreateWalletSummary(ctx, newOwnerID)
	require.NoError(t, err)
	require.Zero(t, newOwnerSummary.SupplierAllocationCount)
	require.Zero(t, newOwnerSummary.SupplierFulfillmentSuccessRate)

	require.NoError(t, db.Model(&WalletModel{}).Where("user_id = ?", ownerID).Update("supplier_available", "10.000000").Error)
	command := billingapp.TransferSupplierBalanceCommand{
		UserID: ownerID, Amount: "1.000000", IdempotencyKey: "supplier-metrics-transfer",
		RequestFingerprint: strings.Repeat("c", 64), RequestID: "supplier-metrics-transfer-request",
	}
	transferred, err := repo.TransferSupplierBalance(ctx, command)
	require.NoError(t, err)
	require.EqualValues(t, 4, transferred.SupplierAllocationCount)
	require.Equal(t, 66.7, transferred.SupplierFulfillmentSuccessRate)
	replayed, err := repo.TransferSupplierBalance(ctx, command)
	require.NoError(t, err)
	require.EqualValues(t, 4, replayed.SupplierAllocationCount)
	require.Equal(t, 66.7, replayed.SupplierFulfillmentSuccessRate)

	require.NoError(t, db.Table("email_resources").Where("id = 9701").Update("owner_user_id", newOwnerID).Error)
	oldOwnerSummary, err := repo.GetOrCreateWalletSummary(ctx, ownerID)
	require.NoError(t, err)
	require.Zero(t, oldOwnerSummary.SupplierAllocationCount)
	require.Zero(t, oldOwnerSummary.SupplierFulfillmentSuccessRate)
	newOwnerSummary, err = NewBillingRepo(db).GetOrCreateWalletSummary(ctx, newOwnerID)
	require.NoError(t, err)
	require.EqualValues(t, 4, newOwnerSummary.SupplierAllocationCount)
	require.Equal(t, 66.7, newOwnerSummary.SupplierFulfillmentSuccessRate)
}

func TestBillingRepoRedeemCardMySQL(t *testing.T) {
	db := newBillingMySQLTestDB(t)
	ctx := context.Background()
	userID := createBillingTestUser(t, db, "buyer@example.com")
	require.NoError(t, db.Exec(`
INSERT INTO user_groups(code, name, description, enabled, api_concurrency_limit, price_discount_ratio, topup_threshold, auto_upgrade_enabled)
VALUES ('vip-card', 'VIP Card', '', 1, 10, 0.900000, 20.000000, 1)`).Error)
	var vipGroupID uint
	require.NoError(t, db.Table("user_groups").Select("id").Where("code = ?", "vip-card").Scan(&vipGroupID).Error)
	repo := NewBillingRepo(db)
	_, err := repo.GetOrCreateWalletSummary(ctx, userID)
	require.NoError(t, err)
	require.NoError(t, db.Model(&WalletModel{}).Where("user_id = ?", userID).Updates(map[string]any{
		"balance_warning_level": 3,
		"balance_warning_cycle": 8,
	}).Error)

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
	require.False(t, result.Replayed)
	var warningState WalletModel
	require.NoError(t, db.Select("balance_warning_level", "balance_warning_cycle").First(&warningState, "user_id = ?", userID).Error)
	require.Equal(t, 0, warningState.BalanceWarningLevel)
	require.Equal(t, uint64(9), warningState.BalanceWarningCycle)

	summary, err := repo.GetOrCreateWalletSummary(ctx, userID)
	require.NoError(t, err)
	require.Equal(t, "25.50", summary.Wallet.ConsumerBalance)
	require.Equal(t, "25.50", summary.TotalRecharged)
	var userGroupID uint
	require.NoError(t, db.Table("users").Select("user_group_id").Where("id = ?", userID).Scan(&userGroupID).Error)
	require.Equal(t, vipGroupID, userGroupID)

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
	require.True(t, replay.Replayed)

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

	_, err = repo.ReverseTransaction(ctx, billingapp.ReverseTransactionCommand{
		Original:           result.Transaction,
		IdempotencyKey:     "reverse-card-001",
		RequestFingerprint: "reverse-card-001-fingerprint",
		RequestID:          "req-reverse-card-001",
		Now:                time.Now().UTC(),
	})
	require.NoError(t, err)
	summary, err = repo.GetOrCreateWalletSummary(ctx, userID)
	require.NoError(t, err)
	require.Equal(t, "0.00", summary.Wallet.ConsumerBalance)
	require.Equal(t, "0.00", summary.TotalRecharged)
}

func TestBillingRepoReferralRewardOnFirstCardRedemptionMySQL(t *testing.T) {
	runtimeconfig.Replace([]settingsdomain.Setting{
		{Key: "first_order_rebate_ratio", Value: "0.8"},
		{Key: "single_rebate_cap", Value: "60"},
		{Key: "cumulative_rebate_cap", Value: "70"},
		{Key: "rebate_expiry_days", Value: "30"},
	})
	t.Cleanup(func() { runtimeconfig.Replace(nil) })
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
	require.Equal(t, "60.00", referrals.TotalEarned)
	require.Equal(t, "60.00", referrals.PendingRewards)
	var firstReward ReferralRewardModel
	require.NoError(t, db.Where("invitee_user_id = ?", inviteeID).First(&firstReward).Error)
	require.NotNil(t, firstReward.ExpiresAt)
	require.WithinDuration(t, first.Transaction.CreatedAt.AddDate(0, 0, 30), *firstReward.ExpiresAt, time.Second)

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
	require.Equal(t, "70.00", transfer.TransferredAmount)
	require.Equal(t, 2, transfer.TransferredCount)
	require.Equal(t, "70.00", transfer.Wallet.ConsumerBalance)

	referrals, err = repo.GetReferralSummary(ctx, inviterID)
	require.NoError(t, err)
	require.Equal(t, "70.00", referrals.TotalEarned)
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

func TestBillingRepoRecordHistoricalZeroDebitDoesNotTouchWalletMySQL(t *testing.T) {
	db := newBillingMySQLTestDB(t)
	ctx := context.Background()
	userID := createBillingTestUser(t, db, "historical-zero@example.com")
	repo := NewBillingRepo(db)
	_, err := repo.GetOrCreateWalletSummary(ctx, userID)
	require.NoError(t, err)
	require.NoError(t, db.Model(&WalletModel{}).Where("user_id = ?", userID).Updates(map[string]any{
		"consumer_balance": "123.45", "total_spend": "67.89", "spend_count": 11,
	}).Error)

	type walletFacts struct {
		ConsumerBalance string
		TotalSpend      string
		SpendCount      int64
	}
	var before walletFacts
	require.NoError(t, db.Table("wallets").Where("user_id = ?", userID).Take(&before).Error)
	command := billingapp.AdjustConsumerBalanceCommand{
		UserID: userID, Reason: "order:HIST-1",
		IdempotencyKey: "history:HIST-1:debit",
		RequestID:      "history-request", Now: time.Now().UTC(),
	}
	first, err := repo.RecordHistoricalZeroDebit(ctx, command)
	require.NoError(t, err)

	var after walletFacts
	require.NoError(t, db.Table("wallets").Where("user_id = ?", userID).Take(&after).Error)
	require.Equal(t, before, after)
	var transaction WalletTransactionModel
	require.NoError(t, db.Where("id = ?", first.ID).Take(&transaction).Error)
	require.Equal(t, "0.000000", transaction.Amount)
	require.Equal(t, "0.000000", transaction.BalanceBefore)
	require.Equal(t, "0.000000", transaction.BalanceAfter)
	require.Equal(t, "historical_order", transaction.BizType)
	_, err = repo.ReverseTransaction(ctx, billingapp.ReverseTransactionCommand{
		Original: *first, IdempotencyKey: "reverse-history:HIST-1",
		RequestFingerprint: "reverse-history:HIST-1", RequestID: "reverse-history-request", Now: time.Now().UTC(),
	})
	require.ErrorIs(t, err, domain.ErrTransactionNotReversible)
	var reversals int64
	require.NoError(t, db.Model(&WalletTransactionModel{}).Where("reversal_of_no = ?", first.TransactionNo).Count(&reversals).Error)
	require.Zero(t, reversals)
	var count int64
	require.NoError(t, db.Model(&WalletTransactionModel{}).Where("idempotency_key = ?", command.IdempotencyKey).Count(&count).Error)
	require.EqualValues(t, 1, count)
	require.NoError(t, db.Model(&IdempotencyKeyModel{}).Where("owner_user_id = ? AND idempotency_key = ?", userID, command.IdempotencyKey).Count(&count).Error)
	require.Zero(t, count)
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
		{"recharges", "idx_recharges_gateway_trade_no"},
		{"recharges", "idx_recharges_reconcile_due"},
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

func TestBillingRepoCreditRechargeExactlyOnceMySQL(t *testing.T) {
	db := newBillingMySQLTestDB(t)
	ctx := context.Background()
	userID := createBillingTestUser(t, db, "recharge-buyer@example.com")
	repo := NewBillingRepo(db)
	now := time.Now().UTC()
	created, err := repo.CreateRecharge(ctx, billingapp.CreateRechargeCommand{
		Recharge: domain.Recharge{
			RechargeNo: "RC-EXACTLY-ONCE", UserID: userID, PaymentMethod: "alipay",
			RechargeQuota: "75.00", PaymentAmount: "75.00", Status: domain.RechargeStatusPaying,
			GatewayConfigHash: "hash", CreatedAt: now, UpdatedAt: now,
		},
		MaxPendingOrders: 2,
		IdempotencyKey:   "recharge-create-1", RequestFingerprint: "recharge-fingerprint-1",
	})
	require.NoError(t, err)
	require.Equal(t, "RC-EXACTLY-ONCE", created.RechargeNo)
	require.NoError(t, db.Model(&WalletModel{}).Where("user_id = ?", userID).Updates(map[string]any{
		"balance_warning_level": 3,
		"balance_warning_cycle": 8,
	}).Error)

	start := make(chan struct{})
	errorsFound := make(chan error, 2)
	for range 2 {
		go func() {
			<-start
			_, creditErr := repo.CreditRecharge(ctx, billingapp.CreditRechargeCommand{
				RechargeNo: created.RechargeNo, GatewayTradeNo: "GW-EXACTLY-ONCE", QueriedAt: now.Add(time.Minute),
			})
			errorsFound <- creditErr
		}()
	}
	close(start)
	require.NoError(t, <-errorsFound)
	require.NoError(t, <-errorsFound)

	summary, err := repo.GetOrCreateWalletSummary(ctx, userID)
	require.NoError(t, err)
	require.Equal(t, "75.00", summary.Wallet.ConsumerBalance)
	require.Equal(t, "75.00", summary.TotalRecharged)
	var transactionCount int64
	require.NoError(t, db.Model(&WalletTransactionModel{}).
		Where("user_id = ? AND transaction_type = ? AND biz_id = ?", userID, domain.TransactionTypeRecharge, created.RechargeNo).
		Count(&transactionCount).Error)
	require.EqualValues(t, 1, transactionCount)
	var warningState WalletModel
	require.NoError(t, db.Select("balance_warning_level", "balance_warning_cycle").First(&warningState, "user_id = ?", userID).Error)
	require.Equal(t, 0, warningState.BalanceWarningLevel)
	require.Equal(t, uint64(9), warningState.BalanceWarningCycle)

	second, err := repo.CreateRecharge(ctx, billingapp.CreateRechargeCommand{
		Recharge: domain.Recharge{
			RechargeNo: "RC-DUPLICATE-GATEWAY", UserID: userID, PaymentMethod: "alipay",
			RechargeQuota: "20.00", PaymentAmount: "20.00", Status: domain.RechargeStatusPaying,
			GatewayConfigHash: "hash", CreatedAt: now, UpdatedAt: now,
		},
		MaxPendingOrders: 2,
		IdempotencyKey:   "recharge-create-2", RequestFingerprint: "recharge-fingerprint-2",
	})
	require.NoError(t, err)
	_, err = repo.CreditRecharge(ctx, billingapp.CreditRechargeCommand{
		RechargeNo: second.RechargeNo, GatewayTradeNo: "GW-EXACTLY-ONCE", QueriedAt: now.Add(2 * time.Minute),
	})
	require.ErrorIs(t, err, domain.ErrRechargeQueryMismatch)
	third, err := repo.CreateRecharge(ctx, billingapp.CreateRechargeCommand{
		Recharge: domain.Recharge{
			RechargeNo: "RC-SECOND-PENDING", UserID: userID, PaymentMethod: "alipay",
			RechargeQuota: "30.00", PaymentAmount: "30.00", Status: domain.RechargeStatusPaying,
			GatewayConfigHash: "hash", CreatedAt: now, UpdatedAt: now,
		},
		MaxPendingOrders: 2,
		IdempotencyKey:   "recharge-create-3", RequestFingerprint: "recharge-fingerprint-3",
	})
	require.NoError(t, err)
	require.Equal(t, "RC-SECOND-PENDING", third.RechargeNo)
	_, err = repo.CreateRecharge(ctx, billingapp.CreateRechargeCommand{
		Recharge: domain.Recharge{
			RechargeNo: "RC-PENDING-LIMIT", UserID: userID, PaymentMethod: "alipay",
			RechargeQuota: "40.00", PaymentAmount: "40.00", Status: domain.RechargeStatusPaying,
			GatewayConfigHash: "hash", CreatedAt: now, UpdatedAt: now,
		},
		MaxPendingOrders: 2,
		IdempotencyKey:   "recharge-create-4", RequestFingerprint: "recharge-fingerprint-4",
	})
	require.ErrorIs(t, err, domain.ErrRechargePending)
	summary, err = repo.GetOrCreateWalletSummary(ctx, userID)
	require.NoError(t, err)
	require.Equal(t, "75.00", summary.Wallet.ConsumerBalance)
	require.NoError(t, repo.FailRecharge(ctx, second.RechargeNo, 0, "query_mismatch", now.Add(2*time.Minute)))
	_, err = repo.CreditRecharge(ctx, billingapp.CreditRechargeCommand{
		RechargeNo: second.RechargeNo, GatewayTradeNo: "GW-RECOVERED", QueriedAt: now.Add(2*time.Minute + 30*time.Second),
	})
	require.NoError(t, err)
	summary, err = repo.GetOrCreateWalletSummary(ctx, userID)
	require.NoError(t, err)
	require.Equal(t, "95.00", summary.Wallet.ConsumerBalance)
	require.Equal(t, "95.00", summary.TotalRecharged)
}

func TestBillingRepoRechargePendingLimitIsSerializedMySQL(t *testing.T) {
	db := newBillingMySQLTestDB(t)
	ctx := context.Background()
	userID := createBillingTestUser(t, db, "recharge-limit@example.com")
	repo := NewBillingRepo(db)
	_, err := repo.GetOrCreateWalletSummary(ctx, userID)
	require.NoError(t, err)

	now := time.Now().UTC()
	start := make(chan struct{})
	results := make(chan error, 2)
	for index := range 2 {
		go func() {
			<-start
			_, createErr := repo.CreateRecharge(ctx, billingapp.CreateRechargeCommand{
				Recharge: domain.Recharge{
					RechargeNo: fmt.Sprintf("RC-CONCURRENT-%d", index), UserID: userID, PaymentMethod: "alipay",
					RechargeQuota: "10.00", PaymentAmount: "10.00", Status: domain.RechargeStatusPaying,
					GatewayConfigHash: "hash", CreatedAt: now, UpdatedAt: now,
				},
				MaxPendingOrders:   1,
				IdempotencyKey:     fmt.Sprintf("recharge-concurrent-%d", index),
				RequestFingerprint: fmt.Sprintf("recharge-concurrent-fingerprint-%d", index),
			})
			results <- createErr
		}()
	}
	close(start)

	succeeded, limited := 0, 0
	for range 2 {
		switch createErr := <-results; {
		case createErr == nil:
			succeeded++
		case errors.Is(createErr, domain.ErrRechargePending):
			limited++
		default:
			require.NoError(t, createErr)
		}
	}
	require.Equal(t, 1, succeeded)
	require.Equal(t, 1, limited)

	var pending int64
	require.NoError(t, db.Model(&RechargeModel{}).Where("user_id = ? AND status IN ?", userID, pendingRechargeStatuses()).Count(&pending).Error)
	require.EqualValues(t, 1, pending)
}

func TestBillingRepoRechargeCallbackAndSchedulingMySQL(t *testing.T) {
	db := newBillingMySQLTestDB(t)
	repo := NewBillingRepo(db)
	ctx := context.Background()
	now := time.Date(2026, 7, 25, 10, 0, 0, 0, time.UTC)
	snapshotConfig := billingapp.RechargeConfig{
		Enabled: true, Version: "v1", GatewayURL: "https://pay.example.com", MerchantID: "1000", MerchantKey: "snapshot-secret",
		NotifyURL: "https://app.example.com/v1/payments/webhooks/epay/v1", ReturnURL: "https://app.example.com/wallet", RequestTimeout: 5 * time.Second,
	}
	snapshotBytes, err := json.Marshal(snapshotConfig)
	require.NoError(t, err)
	snapshot := string(snapshotBytes)
	lastFastWait := now.Add(-4 * time.Second)
	lastFastDue := now.Add(-domain.RechargeFastQueryInterval)
	lastSlowWait := now.Add(-29 * time.Second)
	lastSlowDue := now.Add(-domain.RechargeSlowQueryInterval)
	models := []RechargeModel{
		{RechargeNo: "RC-PAYING-WAIT", Status: string(domain.RechargeStatusPaying), CreatedAt: now.Add(-59 * time.Second)},
		{RechargeNo: "RC-PAYING-DUE", Status: string(domain.RechargeStatusPaying), CreatedAt: now.Add(-domain.RechargeCallbackFallbackDelay)},
		{RechargeNo: "RC-CALLBACK-FIRST", Status: string(domain.RechargeStatusCallback), CreatedAt: now.Add(-20 * time.Second)},
		{RechargeNo: "RC-FAST-WAIT", Status: string(domain.RechargeStatusCallback), QueryAttempts: 9, LastQueriedAt: &lastFastWait, CreatedAt: now.Add(-30 * time.Second)},
		{RechargeNo: "RC-FAST-DUE", Status: string(domain.RechargeStatusCallback), QueryAttempts: 9, LastQueriedAt: &lastFastDue, CreatedAt: now.Add(-31 * time.Second)},
		{RechargeNo: "RC-SLOW-WAIT", Status: string(domain.RechargeStatusCallback), QueryAttempts: 10, LastQueriedAt: &lastSlowWait, CreatedAt: now.Add(-90 * time.Second)},
		{RechargeNo: "RC-SLOW-DUE", Status: string(domain.RechargeStatusCallback), QueryAttempts: 10, LastQueriedAt: &lastSlowDue, CreatedAt: now.Add(-91 * time.Second)},
		{RechargeNo: "RC-EXPIRED", Status: string(domain.RechargeStatusPaying), CreatedAt: now.Add(-domain.RechargeReconciliationWindow)},
	}
	for index := range models {
		models[index].UserID = createBillingTestUser(t, db, fmt.Sprintf("recharge-schedule-%d@example.com", index))
		models[index].PaymentMethod = "alipay"
		models[index].RechargeQuota = "10.00"
		models[index].PaymentAmount = "10.00"
		models[index].GatewayConfigJSON = &snapshot
		models[index].UpdatedAt = models[index].CreatedAt
		require.NoError(t, db.Create(&models[index]).Error)
	}

	due, err := repo.ListDueRecharges(ctx, now, 20)
	require.NoError(t, err)
	dueNos := make([]string, len(due))
	for index := range due {
		dueNos[index] = due[index].RechargeNo
	}
	require.ElementsMatch(t, []string{"RC-PAYING-DUE", "RC-CALLBACK-FIRST", "RC-FAST-DUE", "RC-SLOW-DUE"}, dueNos)

	marked, err := repo.MarkRechargeCallback(ctx, "RC-PAYING-WAIT", now)
	require.NoError(t, err)
	require.True(t, marked)
	marked, err = repo.MarkRechargeCallback(ctx, "RC-PAYING-WAIT", now)
	require.NoError(t, err)
	require.False(t, marked)
	marked, err = repo.MarkRechargeCallback(ctx, "RC-EXPIRED", now)
	require.NoError(t, err)
	require.False(t, marked)
	marked, err = repo.MarkRechargeCallback(ctx, "RC-UNKNOWN", now)
	require.NoError(t, err)
	require.False(t, marked)

	claimedRecharge, claimedConfig, generation, claimed, err := repo.ClaimRechargeQuery(
		ctx, "RC-CALLBACK-FIRST", now, now.Add(domain.RechargeQueryLease),
	)
	require.NoError(t, err)
	require.True(t, claimed)
	require.Equal(t, "RC-CALLBACK-FIRST", claimedRecharge.RechargeNo)
	require.Equal(t, "snapshot-secret", claimedConfig.MerchantKey)
	require.Equal(t, 1, generation)

	_, _, _, claimed, err = repo.ClaimRechargeQuery(ctx, "RC-CALLBACK-FIRST", now, now.Add(domain.RechargeQueryLease))
	require.NoError(t, err)
	require.False(t, claimed)
	require.NoError(t, repo.FailRecharge(ctx, "RC-CALLBACK-FIRST", generation+1, "query_mismatch", now))
	var claimedModel RechargeModel
	require.NoError(t, db.First(&claimedModel, "recharge_no = ?", "RC-CALLBACK-FIRST").Error)
	require.Equal(t, string(domain.RechargeStatusCallback), claimedModel.Status)
	require.NotNil(t, claimedModel.QueryLeaseUntil)
	require.Zero(t, claimedModel.QueryAttempts)

	require.NoError(t, repo.RecordRechargeQuery(ctx, "RC-CALLBACK-FIRST", generation, now))
	claimedModel = RechargeModel{}
	require.NoError(t, db.First(&claimedModel, "recharge_no = ?", "RC-CALLBACK-FIRST").Error)
	require.Nil(t, claimedModel.QueryLeaseUntil)
	require.Equal(t, 1, claimedModel.QueryAttempts)
}

func TestBillingRepoDailyCheckinIsConcurrentAndNoWinCannotRerollMySQL(t *testing.T) {
	db := newBillingMySQLTestDB(t, "clientFoundRows=true")
	repo := NewBillingRepo(db)
	ctx := context.Background()
	userID := createBillingTestUser(t, db, "daily-checkin@example.com")
	command := billingapp.DailyCheckinCommand{
		UserID: userID, BusinessDate: "2026-07-27", RewardAmount: "5.00",
		CheckedInAt: time.Date(2026, 7, 26, 16, 0, 0, 0, time.UTC), IdempotencyKey: "daily_checkin:test",
	}

	const callers = 8
	start := make(chan struct{})
	results := make(chan *billingapp.DailyCheckinResult, callers)
	errs := make(chan error, callers)
	for range callers {
		go func() {
			<-start
			result, err := repo.ClaimDailyCheckin(ctx, command)
			results <- result
			errs <- err
		}()
	}
	close(start)
	firstClaims := 0
	for range callers {
		require.NoError(t, <-errs)
		result := <-results
		if result.FirstClaim {
			firstClaims++
		}
		require.Equal(t, "5.00", result.RewardAmount)
	}
	require.Equal(t, 1, firstClaims)
	var facts, transactions int64
	require.NoError(t, db.Model(&DailyCheckinModel{}).Count(&facts).Error)
	require.NoError(t, db.Model(&WalletTransactionModel{}).Where("biz_type = ?", "daily_checkin").Count(&transactions).Error)
	require.EqualValues(t, 1, facts)
	require.EqualValues(t, 1, transactions)
	wallet, err := repo.GetOrCreateWalletSummary(ctx, userID)
	require.NoError(t, err)
	require.Equal(t, "5.00", wallet.Wallet.ConsumerBalance)

	noWinUserID := createBillingTestUser(t, db, "daily-checkin-no-win@example.com")
	command.UserID, command.BusinessDate, command.RewardAmount = noWinUserID, "2026-07-28", "0.00"
	first, err := repo.ClaimDailyCheckin(ctx, command)
	require.NoError(t, err)
	require.True(t, first.FirstClaim)
	command.RewardAmount = "100.00"
	replay, err := repo.ClaimDailyCheckin(ctx, command)
	require.NoError(t, err)
	require.False(t, replay.FirstClaim)
	require.Equal(t, "0.00", replay.RewardAmount)
}

func TestBillingRepoLeaderboardSettlementCreditsOnceFromSuccessWindowMySQL(t *testing.T) {
	db := newBillingMySQLTestDB(t, "clientFoundRows=true")
	repo := NewBillingRepo(db)
	ctx := context.Background()
	latest, found, err := repo.LatestLeaderboardSettlementDate(ctx)
	require.NoError(t, err)
	require.False(t, found)
	require.Empty(t, latest)
	firstUser := createBillingTestUser(t, db, "leader-one@example.com")
	secondUser := createBillingTestUser(t, db, "leader-two@example.com")
	require.NoError(t, db.Exec(`
INSERT INTO projects(id, name, target_platform, logo_url, status, access_type, loose_match)
VALUES (9001, 'Rewards', 'trade', '', 'listed', 'public', TRUE)`).Error)
	require.NoError(t, db.Exec(`
INSERT INTO project_products(
    id, project_id, type, status, code_enabled, purchase_enabled,
    code_price, purchase_price, code_supplier_price, purchase_supplier_price,
    code_window_minutes, activation_window_minutes, warranty_minutes,
    main_weight, dot_weight, plus_weight)
VALUES (9001, 9001, 'microsoft', 'enabled', TRUE, TRUE, 1, 1, 1, 1, 10, 60, 1440, 1, 0, 0)`).Error)
	start := time.Date(2026, 7, 26, 16, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	seedRewardPurchaseOrder(t, db, 9101, firstUser, start.Add(30*time.Minute)) // created before start, succeeds inside
	seedRewardPurchaseOrder(t, db, 9102, firstUser, start.Add(2*time.Hour))
	seedRewardPurchaseOrder(t, db, 9103, secondUser, start.Add(3*time.Hour))
	seedRewardPurchaseOrder(t, db, 9104, secondUser, end) // half-open upper bound
	seedRewardCodeOrderDeliveredLate(t, db, 9105, 9201, firstUser, end.Add(-time.Minute), end.Add(time.Minute))
	requireExplainUsesIndex(t, db, "idx_mailmatch_delivery_heads_received", `
EXPLAIN SELECT o.user_id, h.message_received_at
FROM mailmatch_order_delivery_heads AS h FORCE INDEX (idx_mailmatch_delivery_heads_received)
JOIN orders AS o ON o.id = h.order_id
WHERE h.message_received_at >= ? AND h.message_received_at < ?`, start, end)
	requireExplainUsesIndex(t, db, "idx_orders_activated", `
EXPLAIN SELECT o.user_id, o.activated_at
FROM orders AS o FORCE INDEX (idx_orders_activated)
WHERE o.activated_at >= ? AND o.activated_at < ?`, start, end)

	rules := []runtimeconfig.LeaderboardRewardRule{{RankFrom: 1, RankTo: 1, Amount: "10.00"}, {RankFrom: 2, RankTo: 2, Amount: "5.00"}}
	command := billingapp.LeaderboardSettlementCommand{
		BusinessDate: "2026-07-27", PeriodStart: start, PeriodEnd: end, Rules: rules,
		RulesJSON: `[{"rankFrom":1,"rankTo":1,"amount":10},{"rankFrom":2,"rankTo":2,"amount":5}]`, SettledAt: end,
	}
	result, err := repo.SettleLeaderboard(ctx, command)
	require.NoError(t, err)
	require.True(t, result.Created)
	require.Equal(t, []billingapp.LeaderboardWinner{
		{UserID: firstUser, Rank: 1, Score: 3, Amount: "10.00"},
		{UserID: secondUser, Rank: 2, Score: 1, Amount: "5.00"},
	}, result.Winners)
	latest, found, err = repo.LatestLeaderboardSettlementDate(ctx)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "2026-07-27", latest)

	replay, err := repo.SettleLeaderboard(ctx, command)
	require.NoError(t, err)
	require.False(t, replay.Created)
	firstWallet, err := repo.GetOrCreateWalletSummary(ctx, firstUser)
	require.NoError(t, err)
	secondWallet, err := repo.GetOrCreateWalletSummary(ctx, secondUser)
	require.NoError(t, err)
	require.Equal(t, "10.00", firstWallet.Wallet.ConsumerBalance)
	require.Equal(t, "5.00", secondWallet.Wallet.ConsumerBalance)

	command.BusinessDate = "2026-07-29"
	command.PeriodStart, command.PeriodEnd = end.Add(24*time.Hour), end.Add(48*time.Hour)
	empty, err := repo.SettleLeaderboard(ctx, command)
	require.NoError(t, err)
	require.True(t, empty.Created)
	require.Empty(t, empty.Winners)
	var settlementCount, rewardCount, transactionCount int64
	require.NoError(t, db.Model(&LeaderboardSettlementModel{}).Count(&settlementCount).Error)
	require.NoError(t, db.Model(&LeaderboardRewardModel{}).Count(&rewardCount).Error)
	require.NoError(t, db.Model(&WalletTransactionModel{}).Where("biz_type = ?", "leaderboard_reward").Count(&transactionCount).Error)
	require.EqualValues(t, 2, settlementCount)
	require.EqualValues(t, 2, rewardCount)
	require.EqualValues(t, 2, transactionCount)
}

func seedRewardPurchaseOrder(t *testing.T, db *gorm.DB, id, userID uint, activatedAt time.Time) {
	t.Helper()
	require.NoError(t, db.Exec(`
INSERT INTO orders(id, order_no, user_id, project_id, project_product_id, product_type, service_mode,
    pay_amount, client_channel, idempotency_key, request_fingerprint, activated_at, created_at, updated_at)
VALUES (?, ?, ?, 9001, 9001, 'microsoft', 'purchase', 0, 'console', ?, ?, ?, ?, ?)`,
		id, fmt.Sprintf("REWARD-%d", id), userID, fmt.Sprintf("reward-%d", id), strings.Repeat("f", 64),
		activatedAt.UTC(), activatedAt.Add(-time.Hour).UTC(), activatedAt.UTC(),
	).Error)
}

func seedRewardCodeOrderDeliveredLate(t *testing.T, db *gorm.DB, orderID, messageID, userID uint, receivedAt, insertedAt time.Time) {
	t.Helper()
	require.NoError(t, db.Exec(`
INSERT INTO email_resources(id, type, owner_user_id) VALUES (9001, 'microsoft', ?)`, userID).Error)
	require.NoError(t, db.Exec(`
INSERT INTO orders(id, order_no, user_id, project_id, project_product_id, product_type, service_mode,
    pay_amount, client_channel, idempotency_key, request_fingerprint, created_at, updated_at)
VALUES (?, ?, ?, 9001, 9001, 'microsoft', 'code', 0, 'console', ?, ?, ?, ?)`,
		orderID, fmt.Sprintf("REWARD-%d", orderID), userID, fmt.Sprintf("reward-%d", orderID), strings.Repeat("e", 64),
		receivedAt.Add(-time.Hour).UTC(), receivedAt.Add(-time.Hour).UTC(),
	).Error)
	require.NoError(t, db.Exec(`
INSERT INTO mailmatch_messages(id, email_resource_id, resource_type, recipient, dedupe_key, received_at, created_at, updated_at)
VALUES (?, 9001, 'microsoft', 'late@example.com', ?, ?, ?, ?)`,
		messageID, strings.Repeat("d", 64), receivedAt.UTC(), insertedAt.UTC(), insertedAt.UTC(),
	).Error)
	require.NoError(t, db.Exec(`
INSERT INTO mailmatch_order_delivery_heads(order_id, message_id, message_received_at) VALUES (?, ?, ?)`,
		orderID, messageID, receivedAt.UTC(),
	).Error)
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
