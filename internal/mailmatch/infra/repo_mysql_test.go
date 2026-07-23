package infra

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/donnel666/remail/internal/mailmatch/app"
	"github.com/donnel666/remail/internal/mailmatch/domain"
	"github.com/donnel666/remail/internal/platform/testmysql"
	"github.com/pressly/goose/v3"
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

func TestMessageProjectionMigrationDownRestoresLegacyDecisionMySQL(t *testing.T) {
	db := newMailmatchMySQLTestDB(t)
	orderID := seedMailmatchOrder(t, db, "OR_PROJECTION_DOWN")
	require.NoError(t, db.Exec(`
INSERT INTO wallet_transactions(
    transaction_no, user_id, transaction_type, balance_bucket, direction,
    amount, balance_before, balance_after, biz_type, biz_id, idempotency_key
) VALUES ('TX_PROJECTION_DOWN', 2, 'debit', 'consumer', 'out', -1, 10, 9, 'order', 'OR_PROJECTION_DOWN', 'TX_PROJECTION_DOWN')`).Error)
	require.NoError(t, db.Exec(`
UPDATE orders
SET status = 'failed', debit_tx_id = (
    SELECT id FROM wallet_transactions WHERE transaction_no = 'TX_PROJECTION_DOWN'
), failure_code = 'unknown'
WHERE id = ?`, orderID).Error)
	receivedAt := time.Now().UTC().Truncate(time.Second)
	messageID := seedMailmatchMessage(t, db, "654321", receivedAt, "z")
	require.NoError(t, db.Table("mailmatch_messages").Where("id = ?", messageID).Updates(map[string]any{
		"matched_order_id": nil, "status": "received", "verification_code": "", "match_diagnostic": "",
	}).Error)
	require.NoError(t, db.Create(&MessageProjectionModel{
		MessageID: messageID, MatchedOrderID: &orderID, Status: string(domain.MessageStatusMatched),
		VerificationCode: "654321", MatchDiagnostic: "projection decision", MessageReceivedAt: receivedAt,
	}).Error)

	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, goose.SetDialect("mysql"))
	require.NoError(t, goose.DownTo(sqlDB, mailmatchMigrationsDir(t), 39))

	var legacy MessageModel
	require.NoError(t, db.First(&legacy, messageID).Error)
	require.Equal(t, &orderID, legacy.MatchedOrderID)
	require.Equal(t, string(domain.MessageStatusMatched), legacy.Status)
	require.Equal(t, "654321", legacy.VerificationCode)
	require.Equal(t, "projection decision", legacy.MatchDiagnostic)
	var pendingRefundCount int64
	require.NoError(t, db.Table("orders").
		Where("id = ? AND status = 'failed' AND debit_tx_id IS NOT NULL AND refund_tx_id IS NULL AND refund_amount = 0", orderID).
		Count(&pendingRefundCount).Error)
	require.Equal(t, int64(1), pendingRefundCount)
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

func TestAppendMessagesKeepFactsImmutableAndMatchedOwnershipTerminalMySQL(t *testing.T) {
	db := newMailmatchMySQLTestDB(t)
	orderID := seedMailmatchOrder(t, db, "OR_MESSAGE_APPEND_ONLY")
	repo := NewRepo(db, nil)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	first := domain.Message{
		EmailResourceID:  100,
		ResourceType:     domain.ResourceTypeMicrosoft,
		MatchedOrderID:   &orderID,
		Recipient:        "first@example.com",
		Sender:           "sender@example.net",
		Subject:          "First subject",
		RawBody:          "Your code is 123456",
		BodyPreview:      "Your code is 123456",
		VerificationCode: "123456",
		DedupeKey:        fmt.Sprintf("%064x", 2),
		Status:           domain.MessageStatusMatched,
		MatchDiagnostic:  "Matched the active order.",
		ReceivedAt:       now,
	}
	second := first
	second.MatchedOrderID = nil
	second.Recipient = "second@example.com"
	second.Subject = "Second subject"
	second.VerificationCode = ""
	second.DedupeKey = fmt.Sprintf("%064x", 1)
	second.Status = domain.MessageStatusIgnored
	second.MatchDiagnostic = "No active order matched."

	stored, inserted, err := repo.AppendMessages(ctx, []domain.Message{first, second})
	require.NoError(t, err)
	require.Equal(t, 2, inserted)
	require.Len(t, stored, 2)
	require.Equal(t, first.DedupeKey, stored[0].DedupeKey)
	require.Equal(t, second.DedupeKey, stored[1].DedupeKey)
	require.NotZero(t, stored[0].ID)
	require.NotZero(t, stored[1].ID)
	first.ID = stored[0].ID
	second.ID = stored[1].ID
	projected, newlyMatched, err := repo.InsertMessageProjections(ctx, []domain.Message{first, second})
	require.NoError(t, err)
	require.Len(t, projected, 2)
	require.Equal(t, []uint{stored[0].ID}, newlyMatched)
	require.Equal(t, orderID, *projected[0].MatchedOrderID)

	var factBefore MessageModel
	require.NoError(t, db.Where("id = ?", stored[0].ID).Take(&factBefore).Error)
	var projectionBefore MessageProjectionModel
	require.NoError(t, db.Where("message_id = ?", stored[0].ID).Take(&projectionBefore).Error)

	changed := first
	changed.ID = stored[0].ID
	changed.MatchedOrderID = nil
	changed.Recipient = "changed@example.com"
	changed.Subject = "Changed subject"
	changed.RawBody = "changed body"
	changed.BodyPreview = "changed preview"
	changed.VerificationCode = "654321"
	changed.Status = domain.MessageStatusIgnored
	changed.MatchDiagnostic = "Changed diagnostic."
	changed.ReceivedAt = now.Add(time.Hour)
	resolved, inserted, err := repo.AppendMessages(ctx, []domain.Message{changed})
	require.NoError(t, err)
	require.Zero(t, inserted)
	require.Len(t, resolved, 1)
	require.Equal(t, stored[0].ID, resolved[0].ID)
	require.Equal(t, first.Recipient, resolved[0].Recipient)
	require.Equal(t, first.Subject, resolved[0].Subject)
	require.Equal(t, first.RawBody, resolved[0].RawBody)
	require.Empty(t, resolved[0].VerificationCode)
	_, newlyMatched, err = repo.InsertMessageProjections(ctx, []domain.Message{changed})
	require.NoError(t, err)
	require.Empty(t, newlyMatched)

	var factAfter MessageModel
	require.NoError(t, db.Where("id = ?", stored[0].ID).Take(&factAfter).Error)
	var projectionAfter MessageProjectionModel
	require.NoError(t, db.Where("message_id = ?", stored[0].ID).Take(&projectionAfter).Error)
	require.Equal(t, factBefore, factAfter)
	require.Nil(t, factAfter.MatchedOrderID)
	require.Equal(t, "received", factAfter.Status)
	require.Empty(t, factAfter.VerificationCode)
	require.Empty(t, factAfter.MatchDiagnostic)
	require.Equal(t, projectionBefore, projectionAfter)
	require.Equal(t, orderID, *projectionAfter.MatchedOrderID)
	require.Equal(t, "matched", projectionAfter.Status)
	require.Equal(t, "123456", projectionAfter.VerificationCode)
	invalidProjection := second
	invalidProjection.ID = stored[1].ID
	invalidProjection.MatchedOrderID = &orderID
	invalidProjection.Status = domain.MessageStatusIgnored
	_, _, err = repo.InsertMessageProjections(ctx, []domain.Message{invalidProjection})
	require.Error(t, err)
	promoted := second
	promoted.ID = stored[1].ID
	promoted.MatchedOrderID = &orderID
	promoted.Status = domain.MessageStatusMatched
	promoted.VerificationCode = "654321"
	projected, newlyMatched, err = repo.InsertMessageProjections(ctx, []domain.Message{promoted})
	require.NoError(t, err)
	require.Equal(t, []uint{stored[1].ID}, newlyMatched)
	require.Equal(t, orderID, *projected[0].MatchedOrderID)
	require.Equal(t, "654321", projected[0].VerificationCode)

	_, _, err = repo.AppendMessages(ctx, []domain.Message{{
		EmailResourceID: 99999,
		ResourceType:    domain.ResourceTypeMicrosoft,
		Recipient:       "missing@example.com",
		DedupeKey:       fmt.Sprintf("%064x", 99999),
		Status:          domain.MessageStatusIgnored,
		ReceivedAt:      now,
	}})
	require.ErrorIs(t, err, domain.ErrMessageNotFound)
	_, _, err = repo.InsertMessageProjections(ctx, []domain.Message{{
		ID:         99999,
		Status:     domain.MessageStatusIgnored,
		ReceivedAt: now,
	}})
	require.ErrorIs(t, err, domain.ErrMessageNotFound)
}

func TestAppendMessagesConcurrentOverlapMySQL(t *testing.T) {
	db := newMailmatchMySQLTestDB(t)
	seedMailmatchOrder(t, db, "OR_MESSAGE_APPEND_CONCURRENT")
	repo := NewRepo(db, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	now := time.Now().UTC().Truncate(time.Second)
	const (
		workers     = 12
		sharedCount = 8
		uniqueCount = 4
	)
	start := make(chan struct{})
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			<-start
			messages := make([]domain.Message, 0, sharedCount+uniqueCount)
			for i := sharedCount - 1; i >= 0; i-- {
				messages = append(messages, concurrentAppendMessage(now, uint64(i+1), worker))
			}
			for i := uniqueCount - 1; i >= 0; i-- {
				key := uint64((worker+1)*1000 + i)
				messages = append(messages, concurrentAppendMessage(now, key, worker))
			}
			stored, _, err := repo.AppendMessages(ctx, messages)
			if err == nil {
				for i := range messages {
					messages[i].ID = stored[i].ID
				}
				_, _, err = repo.InsertMessageProjections(ctx, messages)
			}
			errs <- err
		}(worker)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}

	expected := int64(sharedCount + workers*uniqueCount)
	var messageCount int64
	require.NoError(t, db.Model(&MessageModel{}).Where("email_resource_id = ?", 100).Count(&messageCount).Error)
	require.Equal(t, expected, messageCount)
	var projectionCount int64
	require.NoError(t, db.Model(&MessageProjectionModel{}).Count(&projectionCount).Error)
	require.Equal(t, expected, projectionCount)
}

func TestProjectionAndDeliveryCommitAfterSecondFenceMySQL(t *testing.T) {
	db := newMailmatchMySQLTestDB(t)
	orderID := seedMailmatchOrder(t, db, "OR_MESSAGE_FENCE")
	repo := NewRepo(db, nil)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	decision := domain.Message{
		EmailResourceID: 100, ResourceType: domain.ResourceTypeMicrosoft,
		MatchedOrderID: &orderID, Recipient: "main@example.com",
		Sender: "sender@example.net", Subject: "Fence code",
		RawBody: "Your code is 123456", BodyPreview: "Your code is 123456",
		VerificationCode: "123456", DedupeKey: fmt.Sprintf("%064x", 7001),
		Status: domain.MessageStatusMatched, ReceivedAt: now,
	}
	facts, inserted, err := repo.AppendMessages(ctx, []domain.Message{decision})
	require.NoError(t, err)
	require.Equal(t, 1, inserted)
	require.Len(t, facts, 1)
	require.Nil(t, facts[0].MatchedOrderID)
	require.Equal(t, domain.MessageStatusReceived, facts[0].Status)
	require.Empty(t, facts[0].VerificationCode)
	decision.ID = facts[0].ID

	// The first fence and pure fact append have completed. A failed second
	// fence must leave no ownership that order reads can observe.
	err = repo.WithTx(ctx, func(context.Context) error {
		return domain.ErrFetchJobConflict
	})
	require.ErrorIs(t, err, domain.ErrFetchJobConflict)
	startedAt := now.Add(-time.Minute)
	items, err := repo.ListOrderMessages(ctx, app.OrderScope{
		OrderID: orderID, ServiceMode: "purchase",
		AllocationType: domain.ResourceTypeMicrosoft, ReceiveStartedAt: &startedAt,
	}, 30)
	require.NoError(t, err)
	require.Empty(t, items)
	pending, err := repo.ListUnprojectedMessages(ctx, []uint{100}, 100)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.Equal(t, facts[0].ID, pending[0].ID)

	wantRollback := fmt.Errorf("injected delivery failure")
	err = repo.WithTx(ctx, func(txCtx context.Context) error {
		projected, _, err := repo.InsertMessageProjections(txCtx, []domain.Message{decision})
		if err != nil {
			return err
		}
		if err := repo.CreateCodeOrderDelivery(txCtx, orderID, projected[0]); err != nil {
			return err
		}
		return wantRollback
	})
	require.ErrorIs(t, err, wantRollback)
	var projectionCount int64
	require.NoError(t, db.Model(&MessageProjectionModel{}).Count(&projectionCount).Error)
	require.Zero(t, projectionCount)
	var deliveryCount int64
	require.NoError(t, db.Model(&OrderDeliveryHeadModel{}).Count(&deliveryCount).Error)
	require.Zero(t, deliveryCount)

	err = repo.WithTx(ctx, func(txCtx context.Context) error {
		projected, _, err := repo.InsertMessageProjections(txCtx, []domain.Message{decision})
		if err != nil {
			return err
		}
		return repo.CreateCodeOrderDelivery(txCtx, orderID, projected[0])
	})
	require.NoError(t, err)
	delivery, err := repo.FindOrderDelivery(ctx, orderID)
	require.NoError(t, err)
	require.NotNil(t, delivery)
	require.NotNil(t, delivery.Message)
	require.Equal(t, "123456", delivery.Message.VerificationCode)
	items, err = repo.ListOrderMessages(ctx, app.OrderScope{
		OrderID: orderID, ServiceMode: "purchase",
		AllocationType: domain.ResourceTypeMicrosoft, ReceiveStartedAt: &startedAt,
	}, 30)
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.Equal(t, "123456", items[0].VerificationCode)
	pending, err = repo.ListUnprojectedMessages(ctx, []uint{100}, 100)
	require.NoError(t, err)
	require.Empty(t, pending)
}

func concurrentAppendMessage(now time.Time, key uint64, worker int) domain.Message {
	return domain.Message{
		EmailResourceID: 100,
		ResourceType:    domain.ResourceTypeMicrosoft,
		Recipient:       "concurrent@example.com",
		Sender:          fmt.Sprintf("worker-%d@example.net", worker),
		Subject:         "Concurrent append",
		RawBody:         "immutable body",
		DedupeKey:       fmt.Sprintf("%064x", key),
		Status:          domain.MessageStatusIgnored,
		MatchDiagnostic: "No active order matched.",
		ReceivedAt:      now,
	}
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

func TestReadPickupBatchLoadsOneSnapshotMySQL(t *testing.T) {
	db := newMailmatchMySQLTestDB(t)
	orderID := seedMailmatchOrder(t, db, "OR_PICKUP_BATCH")
	now := time.Now().UTC().Truncate(time.Second)
	require.NoError(t, db.Table("microsoft_resources").Where("id = ?", 100).Update("credential_revision", 7).Error)
	require.NoError(t, db.Exec(`
INSERT INTO wallet_transactions(
    transaction_no, user_id, transaction_type, balance_bucket, direction,
    amount, balance_before, balance_after, biz_type, biz_id, idempotency_key
) VALUES ('TX_PICKUP_BATCH', 2, 'debit', 'consumer', 'out', -1, 10, 9, 'order', 'OR_PICKUP_BATCH', 'TX_PICKUP_BATCH')`).Error)
	var debitID uint
	require.NoError(t, db.Table("wallet_transactions").Select("id").Where("transaction_no = ?", "TX_PICKUP_BATCH").Scan(&debitID).Error)
	require.NoError(t, db.Exec(`
INSERT INTO allocation_order_guards(order_no, type)
VALUES ('OR_PICKUP_BATCH', 'microsoft')`).Error)
	require.NoError(t, db.Exec(`
INSERT INTO microsoft_allocations(
    order_no, project_id, product_id, resource_id, supply_scope, mailbox, email
) VALUES ('OR_PICKUP_BATCH', 10, 20, 100, 'public', 'main', 'main@example.com')`).Error)
	var allocationID uint
	require.NoError(t, db.Table("microsoft_allocations").Select("id").Where("order_no = ?", "OR_PICKUP_BATCH").Scan(&allocationID).Error)
	require.NoError(t, db.Table("orders").Where("id = ?", orderID).Updates(map[string]any{
		"status":             "active",
		"debit_tx_id":        debitID,
		"allocation_type":    "microsoft",
		"microsoft_alloc_id": allocationID,
		"delivery_email":     "main@example.com",
		"receive_started_at": now.Add(-time.Minute),
		"receive_until":      now.Add(10 * time.Minute),
	}).Error)
	require.NoError(t, db.Exec(`
INSERT INTO order_tokens(token_prefix, token_plain, order_no, enabled)
VALUES ('bulk-prefix', 'bulk-token', 'OR_PICKUP_BATCH', 1)`).Error)
	require.NoError(t, db.Exec(`
INSERT INTO mailmatch_resource_fetch_states(email_resource_id, status, cooldown_until)
VALUES (100, 'normal', ?)`, now.Add(time.Minute)).Error)
	repo := NewRepo(db, nil)
	message := domain.Message{
		EmailResourceID:  100,
		ResourceType:     domain.ResourceTypeMicrosoft,
		MatchedOrderID:   &orderID,
		Recipient:        "main@example.com",
		RawBody:          "Your code is 123456",
		VerificationCode: "123456",
		DedupeKey:        "4444444444444444444444444444444444444444444444444444444444444444",
		Status:           domain.MessageStatusMatched,
		ReceivedAt:       now,
	}
	stored, err := repo.UpsertMessages(context.Background(), []domain.Message{message})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`
INSERT INTO mailmatch_order_delivery_heads(order_id, message_id, message_received_at)
VALUES (?, ?, ?)`, orderID, stored[0].ID, now).Error)

	credentials := make([]app.PickupCredential, 100)
	for i := range credentials {
		credentials[i] = app.PickupCredential{Email: "main@example.com", Token: "bulk-token"}
	}
	credentials[1].Email = "wrong@example.com"
	started := time.Now()
	reads, err := repo.ReadPickupBatch(context.Background(), credentials, now, 40)
	elapsed := time.Since(started)
	t.Logf("100-item pickup database read completed in %s", elapsed)
	require.NoError(t, err)
	require.Less(t, elapsed, 10*time.Second)
	require.Len(t, reads, 100)
	require.NoError(t, reads[0].Err)
	require.Equal(t, orderID, reads[0].Scope.OrderID)
	require.Equal(t, uint64(7), reads[0].Scope.CredentialRevision)
	require.NotNil(t, reads[0].Delivery)
	require.Equal(t, stored[0].ID, reads[0].Delivery.Message.ID)
	require.Len(t, reads[0].Messages, 1)
	require.Equal(t, stored[0].ID, reads[0].Messages[0].ID)
	require.ErrorIs(t, reads[1].Err, domain.ErrPickupCredentialInvalid)
	for i := 2; i < len(reads); i++ {
		require.NoError(t, reads[i].Err)
		require.Equal(t, orderID, reads[i].Scope.OrderID)
	}
}

func TestFirstFetchStateCreationAvoidsGapDeadlockMySQL(t *testing.T) {
	db := newMailmatchMySQLTestDB(t)
	seedMailmatchFetchResource(t, db)
	require.NoError(t, db.Exec(`
INSERT INTO email_resources(id, type, owner_user_id)
VALUES (101, 'microsoft', 1)`).Error)
	repo := NewRepo(db, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ready := make(chan struct{}, 2)
	release := make(chan struct{})
	errorsByResource := make(map[uint]error)
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, resourceID := range []uint{100, 101} {
		wg.Add(1)
		go func(id uint) {
			defer wg.Done()
			err := repo.WithTx(ctx, func(txCtx context.Context) error {
				if err := repo.EnsureFetchStates(txCtx, []uint{id}); err != nil {
					return err
				}
				ready <- struct{}{}
				select {
				case <-release:
				case <-ctx.Done():
					return ctx.Err()
				}
				if _, err := repo.FindFetchStatesForUpdate(txCtx, []uint{id}); err != nil {
					return err
				}
				now := time.Now().UTC()
				job := &domain.FetchJob{
					ID: id, EmailResourceID: id, OrderNo: fmt.Sprintf("ORDER-%d", id),
					Purpose: domain.FetchPurposeAutoRefresh, Status: domain.FetchJobPending,
				}
				return repo.RequestFetchBatch(txCtx, []*domain.FetchJob{job}, now.Add(time.Minute), now)
			})
			mu.Lock()
			errorsByResource[id] = err
			mu.Unlock()
		}(resourceID)
	}
	for range 2 {
		select {
		case <-ready:
		case <-ctx.Done():
			t.Fatal("timed out while creating concurrent fetch states")
		}
	}
	close(release)
	wg.Wait()
	require.NoError(t, errorsByResource[100])
	require.NoError(t, errorsByResource[101])
	now := time.Now().UTC()
	jobs := []*domain.FetchJob{
		{ID: 101, EmailResourceID: 101, OrderNo: "ORDER-BATCH-101", Purpose: domain.FetchPurposeAutoRefresh, ExpectedCredentialRevision: 11},
		{ID: 100, EmailResourceID: 100, OrderNo: "ORDER-BATCH-100", Purpose: domain.FetchPurposeAutoRefresh, ExpectedCredentialRevision: 10},
	}
	require.NoError(t, repo.WithTx(ctx, func(txCtx context.Context) error {
		if err := repo.EnsureFetchStates(txCtx, []uint{101, 100}); err != nil {
			return err
		}
		if _, err := repo.FindFetchStatesForUpdate(txCtx, []uint{101, 100}); err != nil {
			return err
		}
		return repo.RequestFetchBatch(txCtx, jobs, now.Add(time.Minute), now)
	}))
	require.Equal(t, uint64(2), jobs[0].Generation)
	require.Equal(t, uint64(2), jobs[1].Generation)
	var states []FetchStateModel
	require.NoError(t, db.Where("email_resource_id IN ?", []uint{100, 101}).Order("email_resource_id").Find(&states).Error)
	require.Len(t, states, 2)
	for i, state := range states {
		require.Equal(t, uint64(2), state.Generation)
		require.Equal(t, string(domain.FetchJobPending), state.Status)
		require.Equal(t, uint64(10+i), state.ExpectedCredentialRevision)
	}
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

func TestListMatchingScopesByRecipientNormalizesExplicitAliasVariantsMySQL(t *testing.T) {
	db := newMailmatchMySQLTestDB(t)
	orderID := seedMailmatchOrder(t, db, "OR_EXPLICIT_ALIAS_PLUS")
	now := time.Now().UTC().Truncate(time.Second)
	require.NoError(t, db.Exec(`
INSERT INTO project_mail_rules(project_id, rule_type, pattern, enabled) VALUES
    (10, 'recipient', 'exact', TRUE),
    (10, 'recipient', 'dot', TRUE),
    (10, 'recipient', 'plus', TRUE)`).Error)
	require.NoError(t, db.Exec(`
INSERT INTO wallet_transactions(
    transaction_no, user_id, transaction_type, balance_bucket, direction,
    amount, balance_before, balance_after, biz_type, biz_id, idempotency_key
) VALUES ('TX_EXPLICIT_ALIAS_PLUS', 2, 'debit', 'consumer', 'out', -1, 10, 9, 'order', 'OR_EXPLICIT_ALIAS_PLUS', 'TX_EXPLICIT_ALIAS_PLUS')`).Error)
	var debitID uint
	require.NoError(t, db.Table("wallet_transactions").Select("id").Where("transaction_no = ?", "TX_EXPLICIT_ALIAS_PLUS").Scan(&debitID).Error)
	require.NoError(t, db.Exec(`
INSERT INTO explicit_aliases(resource_id, owner_user_id, email, status)
VALUES (100, 1, 'explicitalias@example.com', 'normal')`).Error)
	var aliasID uint
	require.NoError(t, db.Table("explicit_aliases").Select("id").Where("resource_id = ? AND email = ?", 100, "explicitalias@example.com").Scan(&aliasID).Error)
	require.NoError(t, db.Exec(`
INSERT INTO allocation_order_guards(order_no, type)
VALUES ('OR_EXPLICIT_ALIAS_PLUS', 'microsoft')`).Error)
	require.NoError(t, db.Exec(`
INSERT INTO microsoft_allocations(
    order_no, project_id, product_id, resource_id, supply_scope, mailbox, explicit_alias_id, email
) VALUES ('OR_EXPLICIT_ALIAS_PLUS', 10, 20, 100, 'public', 'alias', ?, 'explicitalias@example.com')`, aliasID).Error)
	var allocationID uint
	require.NoError(t, db.Table("microsoft_allocations").Select("id").Where("order_no = ?", "OR_EXPLICIT_ALIAS_PLUS").Scan(&allocationID).Error)
	require.NoError(t, db.Table("orders").Where("id = ?", orderID).Updates(map[string]any{
		"status":             "active",
		"debit_tx_id":        debitID,
		"allocation_type":    "microsoft",
		"microsoft_alloc_id": allocationID,
		"delivery_email":     "explicitalias@example.com",
		"receive_started_at": now.Add(-time.Minute),
		"receive_until":      now.Add(10 * time.Minute),
	}).Error)

	repo := NewRepo(db, nil)
	for _, recipient := range []string{"explicitalias+tag@example.com", "explicit.alias@example.com"} {
		scopes, err := repo.ListMatchingScopesByRecipient(
			context.Background(),
			domain.ResourceTypeMicrosoft,
			100,
			recipient,
			now,
		)
		require.NoError(t, err)
		require.Len(t, scopes, 1, recipient)
		require.Equal(t, orderID, scopes[0].OrderID)
		require.Equal(t, "explicitalias@example.com", scopes[0].Recipient)
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
INSERT INTO users(id, email, password_hash, nickname, status, role) VALUES
	    (1, 'supplier@test.local', 'hash', 'supplier', 'active', 'supplier'),
	    (2, 'buyer@test.local', 'hash', 'buyer', 'active', 'user'),
	    (3, 'history-owner@test.local', 'hash', 'history-owner', 'active', 'super_admin')`).Error)
	return seedMailmatchOrderFacts(t, db, orderNo)
}

func seedMailmatchOrderLegacyEnabled(t *testing.T, db *gorm.DB, orderNo string) uint {
	t.Helper()
	require.NoError(t, db.Exec(`
INSERT INTO users(id, email, password_hash, nickname, enabled, role) VALUES
	    (1, 'supplier@test.local', 'hash', 'supplier', TRUE, 'supplier'),
	    (2, 'buyer@test.local', 'hash', 'buyer', TRUE, 'user'),
	    (3, 'history-owner@test.local', 'hash', 'history-owner', TRUE, 'super_admin')`).Error)
	return seedMailmatchOrderFacts(t, db, orderNo)
}

func seedMailmatchOrderFacts(t *testing.T, db *gorm.DB, orderNo string) uint {
	t.Helper()
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
INSERT INTO users(id, email, password_hash, nickname, status, role) VALUES
    (1, 'supplier@test.local', 'hash', 'supplier', 'active', 'supplier'),
    (2, 'buyer@test.local', 'hash', 'buyer', 'active', 'user')`).Error)
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
