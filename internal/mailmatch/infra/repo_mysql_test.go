package infra

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/donnel666/remail/internal/mailmatch/app"
	"github.com/donnel666/remail/internal/mailmatch/domain"
	"github.com/donnel666/remail/internal/platform/testmysql"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

var mailmatchMySQLTestServer = testmysql.New("remail_mailmatch_repo_test")

func TestMain(m *testing.M) {
	code := m.Run()
	_ = mailmatchMySQLTestServer.Close(context.Background())
	os.Exit(code)
}

func newMailmatchMySQLTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	return mailmatchMySQLTestServer.Database(t, mailmatchMigrationsDir(t))
}

func mailmatchMigrationsDir(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	return filepath.Clean(filepath.Join(filepath.Dir(file), "../../..", "migrations"))
}

func TestOrderDeliveryHeadConstraintsMySQL(t *testing.T) {
	db := newMailmatchMySQLTestDB(t)
	orderID := seedMailmatchOrder(t, db, "OR_DELIVERY_CONSTRAINTS")
	now := time.Now().UTC()
	messageID := seedMailmatchMessage(t, db, "123456", now, "a")

	err := db.Exec(`
INSERT INTO mailmatch_order_delivery_heads(order_id, message_id, message_received_at)
VALUES (99999, ?, ?)`, messageID, now).Error
	require.Error(t, err)

	err = db.Exec(`
INSERT INTO mailmatch_order_delivery_heads(order_id, message_id, message_received_at)
VALUES (?, 99999, ?)`, orderID, now).Error
	require.Error(t, err)
}

func TestCreateCodeOrderDeliveryDoesNotOverwriteMySQL(t *testing.T) {
	db := newMailmatchMySQLTestDB(t)
	orderID := seedMailmatchOrder(t, db, "OR_DELIVERY_ONCE")
	repo := NewRepo(db, nil)
	ctx := context.Background()
	firstAt := time.Now().UTC().Add(-time.Minute)
	secondAt := time.Now().UTC()
	first := domain.Message{ID: seedMailmatchMessage(t, db, "111111", firstAt, "b"), ReceivedAt: firstAt, VerificationCode: "111111"}
	second := domain.Message{ID: seedMailmatchMessage(t, db, "222222", secondAt, "c"), ReceivedAt: secondAt, VerificationCode: "222222"}

	require.NoError(t, repo.CreateCodeOrderDelivery(ctx, orderID, first))
	require.NoError(t, repo.CreateCodeOrderDelivery(ctx, orderID, second))

	delivery, err := repo.FindOrderDelivery(ctx, orderID)
	require.NoError(t, err)
	require.NotNil(t, delivery)
	require.NotNil(t, delivery.Message)
	require.Equal(t, first.ID, delivery.Message.ID)
	require.Equal(t, "111111", delivery.Message.VerificationCode)
	require.WithinDuration(t, firstAt, delivery.ReceivedAt, time.Second)

	require.NoError(t, db.Delete(&MessageModel{}, first.ID).Error)
	delivery, err = repo.FindOrderDelivery(ctx, orderID)
	require.NoError(t, err)
	require.NotNil(t, delivery)
	require.Nil(t, delivery.Message)
	require.WithinDuration(t, firstAt, delivery.ReceivedAt, time.Second)
}

func TestAdvancePurchaseOrderDeliveryOnlyMovesForwardMySQL(t *testing.T) {
	db := newMailmatchMySQLTestDB(t)
	orderID := seedMailmatchOrder(t, db, "OR_DELIVERY_LATEST")
	repo := NewRepo(db, nil)
	ctx := context.Background()
	baseAt := time.Now().UTC().Add(-10 * time.Minute)
	newerAt := baseAt.Add(5 * time.Minute)
	olderAt := baseAt.Add(-5 * time.Minute)
	first := domain.Message{ID: seedMailmatchMessage(t, db, "", baseAt, "d"), ReceivedAt: baseAt}
	newer := domain.Message{ID: seedMailmatchMessage(t, db, "222222", newerAt, "e"), ReceivedAt: newerAt, VerificationCode: "222222"}
	older := domain.Message{ID: seedMailmatchMessage(t, db, "333333", olderAt, "f"), ReceivedAt: olderAt, VerificationCode: "333333"}

	require.NoError(t, repo.AdvancePurchaseOrderDelivery(ctx, orderID, first))
	require.NoError(t, repo.AdvancePurchaseOrderDelivery(ctx, orderID, newer))
	require.NoError(t, repo.AdvancePurchaseOrderDelivery(ctx, orderID, older))

	delivery, err := repo.FindOrderDelivery(ctx, orderID)
	require.NoError(t, err)
	require.NotNil(t, delivery)
	require.NotNil(t, delivery.Message)
	require.Equal(t, newer.ID, delivery.Message.ID)
	require.Equal(t, "222222", delivery.Message.VerificationCode)
	require.WithinDuration(t, newerAt, delivery.ReceivedAt, time.Second)
}

func TestUpsertMessagesPreservesMatchedCodeMySQL(t *testing.T) {
	db := newMailmatchMySQLTestDB(t)
	orderID := seedMailmatchOrder(t, db, "OR_MESSAGE_MONOTONIC")
	repo := NewRepo(db, nil)
	ctx := context.Background()
	now := time.Now().UTC()
	message := domain.Message{
		EmailResourceID:  100,
		ResourceType:     domain.ResourceTypeMicrosoft,
		MatchedOrderID:   &orderID,
		Recipient:        "user@example.com",
		RawBody:          "Your code is 123456",
		BodyPreview:      "Your code is 123456",
		VerificationCode: "123456",
		DedupeKey:        "1111111111111111111111111111111111111111111111111111111111111111",
		Status:           domain.MessageStatusMatched,
		ReceivedAt:       now,
	}
	_, err := repo.UpsertMessages(ctx, []domain.Message{message})
	require.NoError(t, err)
	message.VerificationCode = ""
	message.RawBody = ""
	message.BodyPreview = ""
	message.Status = domain.MessageStatusIgnored
	_, err = repo.UpsertMessages(ctx, []domain.Message{message})
	require.NoError(t, err)

	var stored struct {
		Status           string
		MatchedOrderID   *uint  `gorm:"column:matched_order_id"`
		RawBody          string `gorm:"column:raw_body"`
		BodyPreview      string `gorm:"column:body_preview"`
		VerificationCode string `gorm:"column:verification_code"`
	}
	require.NoError(t, db.Table("mailmatch_messages").
		Select("status, matched_order_id, raw_body, body_preview, verification_code").
		Where("email_resource_id = ? AND dedupe_key = ?", message.EmailResourceID, message.DedupeKey).
		Take(&stored).Error)
	require.Equal(t, "matched", stored.Status)
	require.Equal(t, orderID, *stored.MatchedOrderID)
	require.Equal(t, "Your code is 123456", stored.RawBody)
	require.Equal(t, "Your code is 123456", stored.BodyPreview)
	require.Equal(t, "123456", stored.VerificationCode)
}

func TestListOrderMessagesOnlyReturnsPersistedOwnershipMySQL(t *testing.T) {
	db := newMailmatchMySQLTestDB(t)
	orderID := seedMailmatchOrder(t, db, "OR_MESSAGE_OWNER")
	repo := NewRepo(db, nil)
	now := time.Now().UTC().Add(-time.Second).Truncate(time.Second)
	owned := domain.Message{
		EmailResourceID:  100,
		ResourceType:     domain.ResourceTypeMicrosoft,
		MatchedOrderID:   &orderID,
		Recipient:        "user@example.com",
		RawBody:          "Owned code 123456",
		VerificationCode: "123456",
		DedupeKey:        "2222222222222222222222222222222222222222222222222222222222222222",
		Status:           domain.MessageStatusMatched,
		ReceivedAt:       now,
	}
	ambiguous := owned
	ambiguous.MatchedOrderID = nil
	ambiguous.DedupeKey = "3333333333333333333333333333333333333333333333333333333333333333"
	ambiguous.MatchDiagnostic = "Message matched multiple active order services."
	ambiguous.Status = domain.MessageStatusReceived
	_, err := repo.UpsertMessages(context.Background(), []domain.Message{owned, ambiguous})
	require.NoError(t, err)

	startedAt := now.Add(-time.Hour)
	items, err := repo.ListOrderMessages(context.Background(), app.OrderScope{
		OrderID:          orderID,
		ServiceMode:      "purchase",
		AllocationType:   domain.ResourceTypeMicrosoft,
		ReceiveStartedAt: &startedAt,
	}, 30)
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.Equal(t, "123456", items[0].VerificationCode)
	require.Equal(t, orderID, *items[0].MatchedOrderID)
	require.Empty(t, items[0].RawBody)

	detail, err := repo.FindOrderMessage(context.Background(), orderID, items[0].ID)
	require.NoError(t, err)
	require.Equal(t, "Owned code 123456", detail.RawBody)
}

func TestListMatchingScopesByRecipientNormalizesMicrosoftAliasesMySQL(t *testing.T) {
	db := newMailmatchMySQLTestDB(t)
	orderID := seedMailmatchOrder(t, db, "OR_NORMALIZED_RECIPIENT")
	now := time.Now().UTC().Truncate(time.Second)
	require.NoError(t, db.Exec(`
UPDATE microsoft_resources
SET email_address = 'firstname@example.com'
WHERE id = 100`).Error)
	require.NoError(t, db.Exec(`
INSERT INTO project_mail_rules(project_id, rule_type, pattern, enabled) VALUES
    (10, 'sender', 'sender@example\\.net', TRUE),
    (10, 'recipient', 'exact', TRUE),
    (10, 'recipient', 'dot', TRUE),
    (10, 'recipient', 'plus', TRUE)`).Error)
	require.NoError(t, db.Exec(`
INSERT INTO wallet_transactions(
    transaction_no, user_id, transaction_type, balance_bucket, direction,
    amount, balance_before, balance_after, biz_type, biz_id, idempotency_key
) VALUES ('TX_NORMALIZED_RECIPIENT', 2, 'debit', 'consumer', 'out', -1, 10, 9, 'order', 'OR_NORMALIZED_RECIPIENT', 'TX_NORMALIZED_RECIPIENT')`).Error)
	var debitID uint
	require.NoError(t, db.Table("wallet_transactions").Select("id").Where("transaction_no = ?", "TX_NORMALIZED_RECIPIENT").Scan(&debitID).Error)
	require.NoError(t, db.Exec(`
INSERT INTO allocation_order_guards(order_no, type)
VALUES ('OR_NORMALIZED_RECIPIENT', 'microsoft')`).Error)
	require.NoError(t, db.Exec(`
INSERT INTO microsoft_allocations(
    order_no, project_id, product_id, resource_id, supply_scope, mailbox, email
) VALUES ('OR_NORMALIZED_RECIPIENT', 10, 20, 100, 'public', 'main', 'firstname@example.com')`).Error)
	var allocationID uint
	require.NoError(t, db.Table("microsoft_allocations").Select("id").Where("order_no = ?", "OR_NORMALIZED_RECIPIENT").Scan(&allocationID).Error)
	require.NoError(t, db.Table("orders").Where("id = ?", orderID).Updates(map[string]any{
		"status":             "active",
		"debit_tx_id":        debitID,
		"allocation_type":    "microsoft",
		"microsoft_alloc_id": allocationID,
		"delivery_email":     "firstname@example.com",
		"receive_started_at": now.Add(-time.Minute),
		"receive_until":      now.Add(10 * time.Minute),
	}).Error)

	repo := NewRepo(db, nil)
	for _, recipient := range []string{"firstname+tag@example.com", "first.name@example.com"} {
		scopes, err := repo.ListMatchingScopesByRecipient(context.Background(), domain.ResourceTypeMicrosoft, 100, recipient, now)
		require.NoError(t, err)
		require.Len(t, scopes, 1, recipient)
		require.Equal(t, orderID, scopes[0].OrderID)
		require.Equal(t, "firstname@example.com", scopes[0].Recipient)
	}
}

func TestHistoricalProjectScopeLockAndLegacyClearMySQL(t *testing.T) {
	db := newMailmatchMySQLTestDB(t)
	seedMailmatchOrder(t, db, "OR_HISTORY_MATCH")
	require.NoError(t, db.Exec(`
INSERT INTO project_mail_rules(project_id, rule_type, pattern, enabled)
VALUES
    (10, 'recipient', 'exact', TRUE),
    (10, 'sender', 'noreply@github\\.com', TRUE)`).Error)
	repo := NewRepo(db, nil)
	scopes, err := repo.ListHistoricalProjectScopes(context.Background())
	require.NoError(t, err)
	require.Len(t, scopes, 1)
	require.Len(t, scopes[0].Rules, 2)

	first := time.Now().UTC().Add(-24 * time.Hour)
	last := time.Now().UTC()
	require.NoError(t, db.Exec(`
INSERT INTO microsoft_resource_project_matches(
    resource_id, project_id, first_matched_at, last_matched_at, evidence_count, last_scanned_at
) VALUES (100, 10, ?, ?, 1, ?)`, first, last, last).Error)
	require.NoError(t, repo.WithTx(context.Background(), func(txCtx context.Context) error {
		lockedScopes, err := repo.ListHistoricalProjectScopesForUpdate(txCtx)
		require.NoError(t, err)
		require.Equal(t, scopes, lockedScopes)
		return repo.ClearLegacyMicrosoftProjectHistory(txCtx, 100, 10)
	}))

	var legacyCount int64
	require.NoError(t, db.Table("microsoft_resource_project_matches").Where("resource_id = ? AND project_id = ?", 100, 10).Count(&legacyCount).Error)
	require.Zero(t, legacyCount)
	var historicalOrders int64
	require.NoError(t, db.Table("orders").Where("order_no LIKE 'HIST-%'").Count(&historicalOrders).Error)
	require.Zero(t, historicalOrders, "MailMatch must not write Trade-owned order facts directly")
}

func TestResourceFetchStateConstraintsMySQL(t *testing.T) {
	db := newMailmatchMySQLTestDB(t)
	seedMailmatchFetchResource(t, db)

	err := db.Exec(`
INSERT INTO mailmatch_resource_fetch_states(email_resource_id)
VALUES (99999)`).Error
	require.Error(t, err)

	require.NoError(t, db.Exec(`
INSERT INTO mailmatch_resource_fetch_states(email_resource_id)
VALUES (100)`).Error)

	err = db.Exec(`
UPDATE mailmatch_resource_fetch_states
SET status = 'invalid'
WHERE email_resource_id = 100`).Error
	require.Error(t, err)

	err = db.Exec(`
UPDATE mailmatch_resource_fetch_states
SET failures = 4
WHERE email_resource_id = 100`).Error
	require.Error(t, err)
}

func seedMailmatchOrder(t *testing.T, db *gorm.DB, orderNo string) uint {
	t.Helper()
	require.NoError(t, db.Exec(`
INSERT INTO users(id, email, password_hash, nickname, enabled, role) VALUES
	    (1, 'supplier@test.local', 'hash', 'supplier', TRUE, 'supplier'),
	    (2, 'buyer@test.local', 'hash', 'buyer', TRUE, 'user'),
	    (3, 'history-owner@test.local', 'hash', 'history-owner', TRUE, 'super_admin')`).Error)
	require.NoError(t, db.Exec(`
INSERT INTO projects(id, name, target_platform, status, access_type, loose_match)
VALUES (10, 'MailMatch Project', 'mailmatch', 'listed', 'public', TRUE)`).Error)
	require.NoError(t, db.Exec(`
INSERT INTO email_resources(id, type, owner_user_id) VALUES (100, 'microsoft', 1)`).Error)
	require.NoError(t, db.Exec(`
INSERT INTO microsoft_resources(id, resource_type, email_address, email_domain, password, client_id, refresh_token, status)
VALUES (100, 'microsoft', 'main@example.com', 'example.com', 'secret', 'client', 'rt', 'normal')`).Error)
	require.NoError(t, db.Exec(`
INSERT INTO project_products(
    id, project_id, type, status, code_enabled, purchase_enabled,
    code_price, purchase_price, code_supplier_price, purchase_supplier_price,
    code_window_minutes, activation_window_minutes, warranty_minutes,
    main_weight, dot_weight, plus_weight
) VALUES (20, 10, 'microsoft', 'enabled', TRUE, TRUE, 1.00, 2.00, 0.50, 1.00, 10, 60, 60, 1, 0, 0)`).Error)
	require.NoError(t, db.Exec(`
INSERT INTO orders(
    order_no, user_id, project_id, project_product_id, product_type, service_mode,
    supply_policy, status, pay_amount, refund_amount, delivery_email,
    client_channel, idempotency_key, request_fingerprint, service_cleanup_status
) VALUES (
    ?, 2, 10, 20, 'microsoft', 'code',
    'public_only', 'pending_payment', 1.00, 0.00, '',
    'console', ?, REPEAT('a', 64), 'none'
)`, orderNo, orderNo+"-idem").Error)
	var orderID uint
	require.NoError(t, db.Table("orders").Select("id").Where("order_no = ?", orderNo).Scan(&orderID).Error)
	require.NotZero(t, orderID)
	return orderID
}

func seedMailmatchMessage(t *testing.T, db *gorm.DB, code string, receivedAt time.Time, suffix string) uint {
	t.Helper()
	dedupeKey := "000000000000000000000000000000000000000000000000000000000000000" + suffix
	require.Len(t, dedupeKey, 64)
	require.NoError(t, db.Exec(`
INSERT INTO mailmatch_messages(
    email_resource_id, resource_type, recipient, sender, subject, raw_body,
    verification_code, dedupe_key, status, received_at
) VALUES (100, 'microsoft', 'user@example.com', 'noreply@example.com', 'Code', ?, ?, ?, 'matched', ?)`,
		"Your code is "+code,
		code,
		dedupeKey,
		receivedAt,
	).Error)
	var messageID uint
	require.NoError(t, db.Table("mailmatch_messages").Select("id").Where("dedupe_key = ?", dedupeKey).Scan(&messageID).Error)
	require.NotZero(t, messageID)
	return messageID
}

func seedMailmatchFetchResource(t *testing.T, db *gorm.DB) {
	t.Helper()
	require.NoError(t, db.Exec(`
INSERT INTO users(id, email, password_hash, nickname, enabled, role) VALUES
    (1, 'supplier@test.local', 'hash', 'supplier', TRUE, 'supplier'),
    (2, 'buyer@test.local', 'hash', 'buyer', TRUE, 'user')`).Error)
	require.NoError(t, db.Exec(`
INSERT INTO email_resources(id, type, owner_user_id) VALUES
    (100, 'microsoft', 1)`).Error)
	require.NoError(t, db.Exec(`
INSERT INTO microsoft_resources(id, resource_type, email_address, email_domain, password, client_id, refresh_token, status)
VALUES (100, 'microsoft', 'main@example.com', 'example.com', 'secret', 'client', 'rt', 'normal')`).Error)
	require.NoError(t, db.Exec(`
INSERT INTO projects(id, name, target_platform, status, access_type, loose_match)
VALUES (10, 'MailMatch Fetch Project', 'mailmatch', 'listed', 'public', TRUE)`).Error)
	require.NoError(t, db.Exec(`
INSERT INTO project_products(
    id, project_id, type, status, code_enabled, purchase_enabled,
    code_price, purchase_price, code_supplier_price, purchase_supplier_price,
    code_window_minutes, activation_window_minutes, warranty_minutes,
    main_weight, dot_weight, plus_weight
) VALUES (20, 10, 'microsoft', 'enabled', TRUE, TRUE, 1.00, 2.00, 0.50, 1.00, 10, 60, 60, 1, 0, 0)`).Error)
	for _, orderNo := range []string{"OR_FETCH_RESOURCE_A", "OR_FETCH_RESOURCE_B"} {
		require.NoError(t, db.Exec(`
INSERT INTO orders(
    order_no, user_id, project_id, project_product_id, product_type, service_mode,
    supply_policy, status, pay_amount, refund_amount, delivery_email,
    client_channel, idempotency_key, request_fingerprint, service_cleanup_status
) VALUES (
    ?, 2, 10, 20, 'microsoft', 'code',
    'public_only', 'pending_payment', 1.00, 0.00, '',
    'console', ?, REPEAT('b', 64), 'none'
)`, orderNo, orderNo+"-idem").Error)
	}
}
