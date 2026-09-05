package infra

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/donnel666/remail/internal/mailmatch/domain"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type codeDiagnosisMismatchFixture struct {
	db   *gorm.DB
	repo *Repo
	now  time.Time
}

func newCodeDiagnosisMismatchFixture(t *testing.T) codeDiagnosisMismatchFixture {
	t.Helper()
	db := newMailmatchMySQLTestDB(t)
	orderID := seedMailmatchOrder(t, db, "OR_BOT_PROJECT_MISMATCH")
	now := time.Now().UTC().Truncate(time.Second)
	require.NoError(t, db.Exec(`
INSERT INTO wallet_transactions(
    transaction_no, user_id, transaction_type, balance_bucket, direction,
    amount, balance_before, balance_after, biz_type, biz_id, idempotency_key
) VALUES ('TX_BOT_PROJECT_MISMATCH', 2, 'debit', 'consumer', 'out', -1, 10, 9,
          'order', 'OR_BOT_PROJECT_MISMATCH', 'TX_BOT_PROJECT_MISMATCH')`).Error)
	var debitID uint
	require.NoError(t, db.Table("wallet_transactions").Select("id").
		Where("transaction_no = 'TX_BOT_PROJECT_MISMATCH'").Scan(&debitID).Error)
	require.NoError(t, db.Exec(`
INSERT INTO allocation_order_guards(order_no, type) VALUES ('OR_BOT_PROJECT_MISMATCH', 'microsoft')`).Error)
	require.NoError(t, db.Exec(`
INSERT INTO microsoft_allocations(
    order_no, project_id, product_id, resource_id, supply_scope, mailbox, email
) VALUES ('OR_BOT_PROJECT_MISMATCH', 10, 20, 100, 'public', 'main', 'main@example.com')`).Error)
	require.NoError(t, db.Table("orders").Where("id = ?", orderID).Updates(map[string]any{
		"status": "active", "debit_tx_id": debitID, "allocation_type": "microsoft", "delivery_email": "main@example.com",
		"receive_started_at": now.Add(-5 * time.Minute), "receive_until": now.Add(5 * time.Minute),
	}).Error)
	require.NoError(t, db.Exec(`
INSERT INTO project_mail_rules(project_id, rule_type, pattern, enabled) VALUES
    (10, 'recipient', 'exact', TRUE),
    (10, 'sender', 'owned@example\\.net', TRUE),
    (10, 'body', '([0-9]{6})', TRUE)`).Error)
	require.NoError(t, db.Exec(`
INSERT INTO projects(id, name, target_platform, status, access_type, loose_match)
VALUES (11, 'SECRET OTHER PROJECT', 'other', 'listed', 'public', FALSE)`).Error)
	require.NoError(t, db.Exec(`
INSERT INTO project_products(
    id, project_id, type, status, code_enabled, purchase_enabled,
    code_price, purchase_price, code_supplier_price, purchase_supplier_price,
    code_window_minutes, activation_window_minutes, warranty_minutes,
    main_weight, dot_weight, plus_weight
) VALUES (21, 11, 'microsoft', 'enabled', TRUE, FALSE, 1, 0, 0.5, 0, 10, 60, 60, 1, 0, 0)`).Error)
	require.NoError(t, db.Exec(`
INSERT INTO project_mail_rules(project_id, rule_type, pattern, enabled) VALUES
    (11, 'recipient', 'exact', TRUE),
    (11, 'sender', 'other@example\\.net', TRUE),
    (11, 'subject', 'Other login', TRUE),
    (11, 'body', '([0-9]{6})', TRUE)`).Error)
	return codeDiagnosisMismatchFixture{db: db, repo: NewRepo(db, nil), now: now}
}

func TestCodeDiagnosisReadsDeliveryAndAbnormalRefundFactsMySQL(t *testing.T) {
	db := newMailmatchMySQLTestDB(t)
	orderID := seedMailmatchOrder(t, db, "OR_BOT_DIAGNOSIS")
	now := time.Now().UTC().Truncate(time.Second)
	require.NoError(t, db.Exec(`
INSERT INTO wallet_transactions(
    transaction_no, user_id, transaction_type, balance_bucket, direction,
    amount, balance_before, balance_after, biz_type, biz_id, idempotency_key
) VALUES ('TX_BOT_DIAGNOSIS', 2, 'debit', 'consumer', 'out', -1, 10, 9, 'order', 'OR_BOT_DIAGNOSIS', 'TX_BOT_DIAGNOSIS')`).Error)
	var debitID uint
	require.NoError(t, db.Table("wallet_transactions").Select("id").Where("transaction_no = 'TX_BOT_DIAGNOSIS'").Scan(&debitID).Error)
	require.NoError(t, db.Exec(`INSERT INTO allocation_order_guards(order_no, type) VALUES ('OR_BOT_DIAGNOSIS', 'microsoft')`).Error)
	require.NoError(t, db.Exec(`
INSERT INTO microsoft_allocations(
    order_no, project_id, product_id, resource_id, supply_scope, mailbox, email
) VALUES ('OR_BOT_DIAGNOSIS', 10, 20, 100, 'public', 'main', 'main@example.com')`).Error)
	require.NoError(t, db.Table("orders").Where("id = ?", orderID).Updates(map[string]any{
		"status": "active", "debit_tx_id": debitID, "allocation_type": "microsoft",
		"delivery_email": "main@example.com", "receive_started_at": now.Add(-time.Minute), "receive_until": now.Add(time.Minute),
	}).Error)
	repo := NewRepo(db, nil)
	messageID := seedMailmatchMessage(t, db, "123456", now.Add(-time.Minute), "q")
	require.NoError(t, repo.CreateOrderDelivery(context.Background(), orderID, domain.Message{
		ID: messageID, ReceivedAt: now.Add(-time.Minute), VerificationCode: "123456",
	}))
	require.NoError(t, db.Table("mailmatch_messages").Where("id = ?", messageID).Update("created_at", now.Add(-time.Minute)).Error)

	lookup, err := repo.LookupCodeDiagnosis(context.Background(), 2, "main@example.com")
	require.NoError(t, err)
	require.Len(t, lookup.Orders, 1)
	require.Equal(t, uint(10), lookup.Orders[0].ProjectID)
	require.NotEmpty(t, lookup.Orders[0].ProjectName)
	require.Equal(t, uint(100), lookup.Orders[0].EmailResourceID)
	require.NotNil(t, lookup.Orders[0].DeliveryStoredAt)
	require.WithinDuration(t, now.Add(-time.Minute), *lookup.Orders[0].DeliveryStoredAt, time.Millisecond)
	require.False(t, lookup.Orders[0].ResourceAbnormalRefunded)
	otherUser, err := repo.LookupCodeDiagnosis(context.Background(), 3, "main@example.com")
	require.NoError(t, err)
	require.Empty(t, otherUser.Orders)

	require.NoError(t, db.Exec(`
INSERT INTO wallet_transactions(
    transaction_no, user_id, transaction_type, balance_bucket, direction,
    amount, balance_before, balance_after, biz_type, biz_id, idempotency_key
) VALUES ('TX_BOT_DIAGNOSIS_REFUND', 2, 'refund', 'consumer', 'in', 1, 9, 10, 'order', 'OR_BOT_DIAGNOSIS', 'TX_BOT_DIAGNOSIS_REFUND')`).Error)
	var refundID uint
	require.NoError(t, db.Table("wallet_transactions").Select("id").Where("transaction_no = 'TX_BOT_DIAGNOSIS_REFUND'").Scan(&refundID).Error)
	require.NoError(t, db.Table("orders").Where("id = ?", orderID).Updates(map[string]any{
		"status": "refunded", "refund_tx_id": refundID, "refund_amount": "1.000000",
	}).Error)
	require.NoError(t, db.Exec(`
INSERT INTO order_events(event_no, order_no, event_type, from_status, to_status, operator_type, reason)
VALUES ('EV_BOT_DIAGNOSIS_REFUND', 'OR_BOT_DIAGNOSIS', 'order.refunded', 'active', 'refunded', 'system',
        '接码失败')`).Error)

	lookup, err = repo.LookupCodeDiagnosis(context.Background(), 2, "main@example.com")
	require.NoError(t, err)
	require.True(t, lookup.Orders[0].ResourceAbnormalRefunded)
}

func TestCodeDiagnosisOnlyFlagsMailProvenToMatchAnotherProjectMySQL(t *testing.T) {
	fixture := newCodeDiagnosisMismatchFixture(t)
	db, repo, now := fixture.db, fixture.repo, fixture.now
	require.NoError(t, db.Exec(`
INSERT INTO mailmatch_messages(
    email_resource_id, resource_type, recipient, sender, subject, raw_body,
    dedupe_key, status, match_diagnostic, received_at
) VALUES (
    100, 'microsoft', 'main@example.com', 'spam@example.net', 'Unrelated mail', 'Reference 111111',
    REPEAT('b', 64), 'ignored', 'Message did not match any active order service.', ?
)`, now).Error)

	lookup, err := repo.LookupCodeDiagnosis(context.Background(), 2, "main@example.com")
	require.NoError(t, err)
	require.Len(t, lookup.Orders, 1)
	require.False(t, lookup.Orders[0].ProjectMismatch, "unrelated mail must not be called a wrong project")

	require.NoError(t, db.Exec(`
INSERT INTO mailmatch_messages(
    email_resource_id, resource_type, recipient, sender, subject, raw_body,
    dedupe_key, status, match_diagnostic, received_at
) VALUES (
    100, 'microsoft', 'other-user@example.com', 'other@example.net', 'Other login', 'Code 222222',
    REPEAT('d', 64), 'ignored', 'Message did not match any active order service.', ?
)`, now).Error)

	lookup, err = repo.LookupCodeDiagnosis(context.Background(), 2, "main@example.com")
	require.NoError(t, err)
	require.False(t, lookup.Orders[0].ProjectMismatch, "another recipient's matching mail must remain outside the order boundary")

	require.NoError(t, db.Exec(`
INSERT INTO mailmatch_messages(
    email_resource_id, resource_type, recipient, sender, subject, raw_body,
    dedupe_key, status, match_diagnostic, received_at
) VALUES (
    100, 'microsoft', 'main@example.com', 'other@example.net', 'Other login', 'Code 654321',
    REPEAT('c', 64), 'ignored', 'Message did not match any active order service.', ?
)`, now).Error)

	lookup, err = repo.LookupCodeDiagnosis(context.Background(), 2, "main@example.com")
	require.NoError(t, err)
	require.Len(t, lookup.Orders, 1)
	require.True(t, lookup.Orders[0].ProjectMismatch)
	require.Equal(t, uint(10), lookup.Orders[0].ProjectID)
	require.Equal(t, "MailMatch Project", lookup.Orders[0].ProjectName)
}

func TestCodeDiagnosisUsesOnlyIgnoredUnclaimedMailMySQL(t *testing.T) {
	fixture := newCodeDiagnosisMismatchFixture(t)
	claimOrderID := seedCodeDiagnosisClaimOrder(t, fixture.db)
	ctx := context.Background()

	projectedID := seedCodeDiagnosisCandidate(t, fixture.db, 1001, "received", nil, fixture.now)
	require.NoError(t, fixture.db.Create(&MessageProjectionModel{
		MessageID: projectedID, MatchedOrderID: &claimOrderID, Status: string(domain.MessageStatusMatched),
		VerificationCode: "111111", MessageReceivedAt: fixture.now,
	}).Error)
	lookup, err := fixture.repo.LookupCodeDiagnosis(ctx, 2, "main@example.com")
	require.NoError(t, err)
	require.False(t, lookup.Orders[0].ProjectMismatch, "projection-owned mail belongs to its claimed order")

	seedCodeDiagnosisCandidate(t, fixture.db, 1002, "matched", &claimOrderID, fixture.now)
	lookup, err = fixture.repo.LookupCodeDiagnosis(ctx, 2, "main@example.com")
	require.NoError(t, err)
	require.False(t, lookup.Orders[0].ProjectMismatch, "legacy-owned mail belongs to its claimed order")

	seedCodeDiagnosisCandidate(t, fixture.db, 1003, "received", nil, fixture.now)
	lookup, err = fixture.repo.LookupCodeDiagnosis(ctx, 2, "main@example.com")
	require.NoError(t, err)
	require.False(t, lookup.Orders[0].ProjectMismatch, "an unprojected append must not race the matching decision")

	ambiguousID := seedCodeDiagnosisCandidate(t, fixture.db, 1004, "received", nil, fixture.now)
	require.NoError(t, fixture.db.Create(&MessageProjectionModel{
		MessageID: ambiguousID, Status: string(domain.MessageStatusReceived),
		MatchDiagnostic: "Message matched multiple active order services.", MessageReceivedAt: fixture.now,
	}).Error)
	lookup, err = fixture.repo.LookupCodeDiagnosis(ctx, 2, "main@example.com")
	require.NoError(t, err)
	require.False(t, lookup.Orders[0].ProjectMismatch, "ambiguous mail must remain unassigned")

	seedCodeDiagnosisCandidate(t, fixture.db, 1005, "ignored", nil, fixture.now)
	lookup, err = fixture.repo.LookupCodeDiagnosis(ctx, 2, "main@example.com")
	require.NoError(t, err)
	require.True(t, lookup.Orders[0].ProjectMismatch, "ignored unclaimed mail is safe mismatch evidence")
}

func TestCodeDiagnosisFailsClosedForProjectScopedRecipientOverlapMySQL(t *testing.T) {
	fixture := newCodeDiagnosisMismatchFixture(t)
	otherOrderID := seedOverlappingCodeDiagnosisOrder(t, fixture, "OR_BOT_DIAGNOSIS_OTHER")
	messageID := seedCodeDiagnosisCandidate(t, fixture.db, 2001, "received", nil, fixture.now)
	require.NoError(t, fixture.db.Create(&MessageProjectionModel{
		MessageID: messageID, MatchedOrderID: &otherOrderID, Status: string(domain.MessageStatusMatched),
		VerificationCode: "111111", MessageReceivedAt: fixture.now,
	}).Error)

	lookup, err := fixture.repo.LookupCodeDiagnosis(context.Background(), 2, "main@example.com")
	require.NoError(t, err)
	require.False(t, lookup.Orders[0].ProjectMismatch, "another user's claimed mail must not be disclosed")

	seedCodeDiagnosisCandidate(t, fixture.db, 2002, "ignored", nil, fixture.now)
	lookup, err = fixture.repo.LookupCodeDiagnosis(context.Background(), 2, "main@example.com")
	require.NoError(t, err)
	require.False(t, lookup.Orders[0].ProjectMismatch, "shared readable windows make even unclaimed mail ambiguous")
}

func TestCodeDiagnosisFailsClosedForHistoricalRecipientOverlapMySQL(t *testing.T) {
	fixture := newCodeDiagnosisMismatchFixture(t)
	seedOverlappingCodeDiagnosisOrder(t, fixture, "HIST-BOT-DIAGNOSIS-OTHER")
	seedCodeDiagnosisCandidate(t, fixture.db, 2003, "ignored", nil, fixture.now)

	lookup, err := fixture.repo.LookupCodeDiagnosis(context.Background(), 2, "main@example.com")
	require.NoError(t, err)
	require.False(t, lookup.Orders[0].ProjectMismatch, "historical overlap still makes mail ownership ambiguous")
}

func TestCodeDiagnosisFailsClosedForAliasEquivalentHistoricalOverlapMySQL(t *testing.T) {
	fixture := newCodeDiagnosisMismatchFixture(t)
	orderNo := "OR_BOT_DIAGNOSIS_ALIAS_OVERLAP"
	seedOverlappingCodeDiagnosisOrder(t, fixture, orderNo)
	require.NoError(t, fixture.db.Table("orders").Where("order_no = ?", orderNo).
		Update("delivery_email", "ma.in@example.com").Error)
	require.NoError(t, fixture.db.Table("microsoft_allocations").Where("order_no = ?", orderNo).
		Updates(map[string]any{
			"email": "ma.in@example.com", "mailbox": "main", "status": "released",
			"released_at": fixture.now.Add(time.Minute),
		}).Error)
	seedCodeDiagnosisCandidate(t, fixture.db, 2004, "ignored", nil, fixture.now)

	lookup, err := fixture.repo.LookupCodeDiagnosis(context.Background(), 2, "main@example.com")
	require.NoError(t, err)
	require.False(t, lookup.Orders[0].ProjectMismatch, "an alias-equivalent historical order makes ownership ambiguous")
}

func TestCodeDiagnosisFailsClosedForAllocationCreatedInsideReadSkewMySQL(t *testing.T) {
	fixture := newCodeDiagnosisMismatchFixture(t)
	previousSkew := runtimeconfig.String("read_window_skew_minutes", "2")
	runtimeconfig.Set("read_window_skew_minutes", "1")
	t.Cleanup(func() { runtimeconfig.Set("read_window_skew_minutes", previousSkew) })
	orderNo := "OR_BOT_DIAGNOSIS_SKEW_OVERLAP"
	seedOverlappingCodeDiagnosisOrder(t, fixture, orderNo)
	require.NoError(t, fixture.db.Table("orders").Where("order_no = ?", orderNo).
		Update("receive_started_at", fixture.now.Add(90*time.Second)).Error)
	require.NoError(t, fixture.db.Table("microsoft_allocations").Where("order_no = ?", orderNo).
		Update("created_at", fixture.now.Add(90*time.Second)).Error)
	seedCodeDiagnosisCandidate(t, fixture.db, 2005, "ignored", nil, fixture.now)

	lookup, err := fixture.repo.LookupCodeDiagnosis(context.Background(), 2, "main@example.com")
	require.NoError(t, err)
	require.False(t, lookup.Orders[0].ProjectMismatch, "an order created inside read skew makes ownership ambiguous")
}

func TestCodeDiagnosisScansPastFirstHundredMessagesMySQL(t *testing.T) {
	fixture := newCodeDiagnosisMismatchFixture(t)
	seedCodeDiagnosisUnrelatedCandidates(t, fixture, 101, 3000, fixture.now)
	seedCodeDiagnosisCandidate(t, fixture.db, 4000, "ignored", nil, fixture.now.Add(-time.Second))

	lookup, err := fixture.repo.LookupCodeDiagnosis(context.Background(), 2, "main@example.com")
	require.NoError(t, err)
	require.True(t, lookup.Orders[0].ProjectMismatch)
}

func TestCodeDiagnosisAcceptsExactlyScanLimitWithFirstMismatchMySQL(t *testing.T) {
	fixture := newCodeDiagnosisMismatchFixture(t)
	seedCodeDiagnosisCandidate(t, fixture.db, 5000, "ignored", nil, fixture.now)
	seedCodeDiagnosisUnrelatedCandidates(t, fixture, codeDiagnosisMessageScanLimit-1, 6000, fixture.now.Add(-time.Second))

	lookup, err := fixture.repo.LookupCodeDiagnosis(context.Background(), 2, "main@example.com")
	require.NoError(t, err)
	require.True(t, lookup.Orders[0].ProjectMismatch)
}

func TestCodeDiagnosisFailsClosedAboveScanLimitWithFirstMismatchMySQL(t *testing.T) {
	fixture := newCodeDiagnosisMismatchFixture(t)
	seedCodeDiagnosisCandidate(t, fixture.db, 8000, "ignored", nil, fixture.now)
	seedCodeDiagnosisUnrelatedCandidates(t, fixture, codeDiagnosisMessageScanLimit, 9000, fixture.now.Add(-time.Second))

	_, err := fixture.repo.LookupCodeDiagnosis(context.Background(), 2, "main@example.com")
	require.ErrorContains(t, err, "scan limit 1000 exceeded")
}

func seedCodeDiagnosisUnrelatedCandidates(
	t *testing.T,
	fixture codeDiagnosisMismatchFixture,
	count int,
	keyBase int,
	receivedAt time.Time,
) {
	t.Helper()
	messages := make([]domain.Message, count)
	for i := range messages {
		messages[i] = domain.Message{
			EmailResourceID: 100, ResourceType: domain.ResourceTypeMicrosoft,
			Recipient: "main@example.com", Sender: "spam@example.net", Subject: "Unrelated",
			RawBody: "Reference 999999", DedupeKey: fmt.Sprintf("%064x", keyBase+i),
			Status: domain.MessageStatusIgnored, MatchDiagnostic: "Message did not match any active order service.",
			ReceivedAt: receivedAt,
		}
	}
	_, err := fixture.repo.UpsertMessages(context.Background(), messages)
	require.NoError(t, err)
}

func seedCodeDiagnosisClaimOrder(t *testing.T, db *gorm.DB) uint {
	t.Helper()
	require.NoError(t, db.Exec(`
INSERT INTO orders(
    order_no, user_id, project_id, project_product_id, product_type, service_mode,
    supply_policy, status, pay_amount, refund_amount, delivery_email,
    client_channel, idempotency_key, request_fingerprint, service_cleanup_status
) VALUES (
    'OR_BOT_DIAGNOSIS_CLAIM', 3, 11, 21, 'microsoft', 'code',
    'public_only', 'pending_payment', 1, 0, '',
    'console', 'OR_BOT_DIAGNOSIS_CLAIM-idem', REPEAT('e', 64), 'none'
)`).Error)
	var orderID uint
	require.NoError(t, db.Table("orders").Select("id").
		Where("order_no = 'OR_BOT_DIAGNOSIS_CLAIM'").Scan(&orderID).Error)
	require.NotZero(t, orderID)
	return orderID
}

func seedOverlappingCodeDiagnosisOrder(t *testing.T, fixture codeDiagnosisMismatchFixture, orderNo string) uint {
	t.Helper()
	require.NoError(t, fixture.db.Exec(`
INSERT INTO wallet_transactions(
    transaction_no, user_id, transaction_type, balance_bucket, direction,
    amount, balance_before, balance_after, biz_type, biz_id, idempotency_key
) VALUES ('TX_BOT_DIAGNOSIS_OTHER', 3, 'debit', 'consumer', 'out', -1, 10, 9,
          'order', ?, 'TX_BOT_DIAGNOSIS_OTHER')`, orderNo).Error)
	var debitID uint
	require.NoError(t, fixture.db.Table("wallet_transactions").Select("id").
		Where("transaction_no = 'TX_BOT_DIAGNOSIS_OTHER'").Scan(&debitID).Error)
	require.NoError(t, fixture.db.Exec(`
INSERT INTO orders(
    order_no, user_id, project_id, project_product_id, product_type, service_mode,
    supply_policy, status, pay_amount, refund_amount, debit_tx_id, allocation_type,
    delivery_email, receive_started_at, receive_until,
    client_channel, idempotency_key, request_fingerprint, service_cleanup_status
) VALUES (
    ?, 3, 11, 21, 'microsoft', 'code',
    'public_only', 'active', 1, 0, ?, 'microsoft',
    'main@example.com', ?, ?,
    'console', ?, REPEAT('f', 64), 'none'
)`, orderNo, debitID, fixture.now.Add(-time.Minute), fixture.now.Add(time.Minute), orderNo+"-idem").Error)
	require.NoError(t, fixture.db.Exec(`
INSERT INTO allocation_order_guards(order_no, type) VALUES (?, 'microsoft')`, orderNo).Error)
	require.NoError(t, fixture.db.Exec(`
INSERT INTO microsoft_allocations(
    order_no, project_id, product_id, resource_id, supply_scope, mailbox, email, created_at
) VALUES (?, 11, 21, 100, 'public', 'main', 'main@example.com', ?)`, orderNo, fixture.now.Add(-time.Minute)).Error)
	var orderID uint
	require.NoError(t, fixture.db.Table("orders").Select("id").
		Where("order_no = ?", orderNo).Scan(&orderID).Error)
	require.NotZero(t, orderID)
	return orderID
}

func seedCodeDiagnosisCandidate(
	t *testing.T,
	db *gorm.DB,
	key int,
	status string,
	matchedOrderID *uint,
	receivedAt time.Time,
) uint {
	t.Helper()
	dedupeKey := fmt.Sprintf("%064x", key)
	require.NoError(t, db.Exec(`
INSERT INTO mailmatch_messages(
    email_resource_id, resource_type, matched_order_id, recipient, sender, subject, raw_body,
    dedupe_key, status, match_diagnostic, received_at
) VALUES (
    100, 'microsoft', ?, 'main@example.com', 'other@example.net', 'Other login', 'Code 111111',
    ?, ?, '', ?
)`, matchedOrderID, dedupeKey, status, receivedAt).Error)
	var messageID uint
	require.NoError(t, db.Table("mailmatch_messages").Select("id").Where("dedupe_key = ?", dedupeKey).Scan(&messageID).Error)
	require.NotZero(t, messageID)
	return messageID
}
