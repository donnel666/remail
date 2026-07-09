package infra

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

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

func TestOrderSnapshotConstraintsMySQL(t *testing.T) {
	db := newMailmatchMySQLTestDB(t)
	seedMailmatchOrder(t, db, "OR_SNAPSHOT_CONSTRAINTS")
	now := time.Now().UTC()

	err := db.Exec(`
INSERT INTO mailmatch_order_snapshots(order_no, sender, recipient, received_at, subject, body, verification_code)
VALUES ('OR_MISSING_SNAPSHOT', 'noreply@example.com', 'user@example.com', ?, 'Code', 'Body', '123456')`, now).Error
	require.Error(t, err)

	err = db.Exec(`
INSERT INTO mailmatch_order_snapshots(order_no, sender, recipient, received_at, subject, body, verification_code)
VALUES ('OR_SNAPSHOT_CONSTRAINTS', 'noreply@example.com', 'user@example.com', ?, 'Code', 'Body', '')`, now).Error
	require.Error(t, err)
}

func TestCreateOrderSnapshotOnceDoesNotOverwriteMySQL(t *testing.T) {
	db := newMailmatchMySQLTestDB(t)
	seedMailmatchOrder(t, db, "OR_SNAPSHOT_ONCE")
	repo := NewRepo(db, nil)
	ctx := context.Background()
	firstAt := time.Now().UTC().Add(-time.Minute)
	secondAt := time.Now().UTC()

	require.NoError(t, repo.CreateOrderSnapshotOnce(ctx, domain.OrderSnapshot{
		OrderNo:          "OR_SNAPSHOT_ONCE",
		Sender:           "first@example.com",
		Recipient:        "user@example.com",
		ReceivedAt:       firstAt,
		Subject:          "First",
		Body:             "First code 111111",
		VerificationCode: "111111",
	}))
	require.NoError(t, repo.CreateOrderSnapshotOnce(ctx, domain.OrderSnapshot{
		OrderNo:          "OR_SNAPSHOT_ONCE",
		Sender:           "second@example.com",
		Recipient:        "user@example.com",
		ReceivedAt:       secondAt,
		Subject:          "Second",
		Body:             "Second code 222222",
		VerificationCode: "222222",
	}))

	snapshot, err := repo.FindOrderSnapshot(ctx, "OR_SNAPSHOT_ONCE")
	require.NoError(t, err)
	require.NotNil(t, snapshot)
	require.Equal(t, "111111", snapshot.VerificationCode)
	require.Equal(t, "First", snapshot.Subject)
	require.WithinDuration(t, firstAt, snapshot.ReceivedAt, time.Second)
}

func TestUpsertLatestOrderSnapshotOnlyMovesForwardMySQL(t *testing.T) {
	db := newMailmatchMySQLTestDB(t)
	seedMailmatchOrder(t, db, "OR_SNAPSHOT_LATEST")
	repo := NewRepo(db, nil)
	ctx := context.Background()
	baseAt := time.Now().UTC().Add(-10 * time.Minute)
	newerAt := baseAt.Add(5 * time.Minute)
	olderAt := baseAt.Add(-5 * time.Minute)

	require.NoError(t, repo.UpsertLatestOrderSnapshot(ctx, domain.OrderSnapshot{
		OrderNo:          "OR_SNAPSHOT_LATEST",
		Sender:           "first@example.com",
		Recipient:        "user@example.com",
		ReceivedAt:       baseAt,
		Subject:          "First",
		Body:             "First code 111111",
		VerificationCode: "111111",
	}))
	require.NoError(t, repo.UpsertLatestOrderSnapshot(ctx, domain.OrderSnapshot{
		OrderNo:          "OR_SNAPSHOT_LATEST",
		Sender:           "newer@example.com",
		Recipient:        "user@example.com",
		ReceivedAt:       newerAt,
		Subject:          "Newer",
		Body:             "Newer code 222222",
		VerificationCode: "222222",
	}))
	require.NoError(t, repo.UpsertLatestOrderSnapshot(ctx, domain.OrderSnapshot{
		OrderNo:          "OR_SNAPSHOT_LATEST",
		Sender:           "older@example.com",
		Recipient:        "user@example.com",
		ReceivedAt:       olderAt,
		Subject:          "Older",
		Body:             "Older code 333333",
		VerificationCode: "333333",
	}))

	snapshot, err := repo.FindOrderSnapshot(ctx, "OR_SNAPSHOT_LATEST")
	require.NoError(t, err)
	require.NotNil(t, snapshot)
	require.Equal(t, "222222", snapshot.VerificationCode)
	require.Equal(t, "Newer", snapshot.Subject)
	require.WithinDuration(t, newerAt, snapshot.ReceivedAt, time.Second)
}

func seedMailmatchOrder(t *testing.T, db *gorm.DB, orderNo string) {
	t.Helper()
	require.NoError(t, db.Exec(`
INSERT INTO users(id, email, password_hash, nickname, enabled, role) VALUES
    (1, 'supplier@test.local', 'hash', 'supplier', TRUE, 'supplier'),
    (2, 'buyer@test.local', 'hash', 'buyer', TRUE, 'user')`).Error)
	require.NoError(t, db.Exec(`
INSERT INTO projects(id, name, target_platform, status, access_type, loose_match)
VALUES (10, 'MailMatch Project', 'mailmatch', 'listed', 'public', TRUE)`).Error)
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
}
