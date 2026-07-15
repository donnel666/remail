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

func TestHistoricalProjectMatchesAreIdempotentMySQL(t *testing.T) {
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
	match := app.HistoricalProjectMatch{
		ResourceID:     100,
		ProjectID:      10,
		FirstMatchedAt: first,
		LastMatchedAt:  last,
		EvidenceCount:  3,
		ScannedAt:      last,
	}
	require.NoError(t, repo.UpsertMicrosoftProjectMatches(context.Background(), []app.HistoricalProjectMatch{match}))
	match.FirstMatchedAt = first.Add(time.Hour)
	match.LastMatchedAt = last.Add(time.Hour)
	match.EvidenceCount = 2
	match.ScannedAt = last.Add(time.Hour)
	require.NoError(t, repo.UpsertMicrosoftProjectMatches(context.Background(), []app.HistoricalProjectMatch{match}))

	var stored microsoftResourceProjectMatchModel
	require.NoError(t, db.First(&stored, "resource_id = ? AND project_id = ?", 100, 10).Error)
	require.Equal(t, 3, stored.EvidenceCount)
	require.WithinDuration(t, first, stored.FirstMatchedAt, time.Millisecond)
	require.WithinDuration(t, last.Add(time.Hour), stored.LastMatchedAt, time.Millisecond)
}

func TestResourceFetchStateAndActiveJobConstraintsMySQL(t *testing.T) {
	db := newMailmatchMySQLTestDB(t)
	seedMailmatchFetchResource(t, db)
	now := time.Now().UTC()

	err := db.Exec(`
INSERT INTO mailmatch_resource_fetch_states(email_resource_id)
VALUES (99999)`).Error
	require.Error(t, err)

	require.NoError(t, db.Exec(`
INSERT INTO mailmatch_resource_fetch_states(email_resource_id)
VALUES (100)`).Error)

	require.NoError(t, db.Exec(`
INSERT INTO mailmatch_fetch_jobs(
    order_no, purpose, allocation_type, allocation_id, project_id, email_resource_id,
    recipient, status, since_at, until_at
) VALUES (
    'OR_FETCH_RESOURCE_A', 'order_fetch', 'microsoft', 1000, 10, 100,
    'a@example.com', 'pending', ?, ?
)`, now.Add(-time.Hour), now).Error)

	err = db.Exec(`
INSERT INTO mailmatch_fetch_jobs(
    order_no, purpose, allocation_type, allocation_id, project_id, email_resource_id,
    recipient, status, since_at, until_at
) VALUES (
    'OR_FETCH_RESOURCE_B', 'order_fetch', 'microsoft', 1001, 10, 100,
    'b@example.com', 'pending', ?, ?
)`, now.Add(-time.Hour), now).Error
	require.Error(t, err)

	require.NoError(t, db.Exec(`
UPDATE mailmatch_fetch_jobs
SET status = 'succeeded'
WHERE order_no = 'OR_FETCH_RESOURCE_A'`).Error)
	require.NoError(t, db.Exec(`
INSERT INTO mailmatch_fetch_jobs(
    order_no, purpose, allocation_type, allocation_id, project_id, email_resource_id,
    recipient, status, since_at, until_at
) VALUES (
    'OR_FETCH_RESOURCE_B', 'order_fetch', 'microsoft', 1001, 10, 100,
    'b@example.com', 'pending', ?, ?
)`, now.Add(-time.Hour), now).Error)
}

func seedMailmatchOrder(t *testing.T, db *gorm.DB, orderNo string) uint {
	t.Helper()
	require.NoError(t, db.Exec(`
INSERT INTO users(id, email, password_hash, nickname, enabled, role) VALUES
    (1, 'supplier@test.local', 'hash', 'supplier', TRUE, 'supplier'),
    (2, 'buyer@test.local', 'hash', 'buyer', TRUE, 'user')`).Error)
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
