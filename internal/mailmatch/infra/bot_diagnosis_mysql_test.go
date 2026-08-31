package infra

import (
	"context"
	"testing"
	"time"

	"github.com/donnel666/remail/internal/mailmatch/domain"
	"github.com/stretchr/testify/require"
)

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

	lookup, err := repo.LookupCodeDiagnosis(context.Background(), 2, "main@example.com", 10)
	require.NoError(t, err)
	require.True(t, lookup.EmailOrderExists)
	require.Len(t, lookup.Orders, 1)
	require.Equal(t, uint(100), lookup.Orders[0].EmailResourceID)
	require.NotNil(t, lookup.Orders[0].DeliveryStoredAt)
	require.WithinDuration(t, now.Add(-time.Minute), *lookup.Orders[0].DeliveryStoredAt, time.Millisecond)
	require.False(t, lookup.Orders[0].ResourceAbnormalRefunded)

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
        'Microsoft resource is permanently unavailable.')`).Error)

	lookup, err = repo.LookupCodeDiagnosis(context.Background(), 2, "main@example.com", 10)
	require.NoError(t, err)
	require.True(t, lookup.Orders[0].ResourceAbnormalRefunded)
}
