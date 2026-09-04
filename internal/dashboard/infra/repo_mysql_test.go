package infra

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/donnel666/remail/internal/businessday"
	"github.com/donnel666/remail/internal/platform/testmysql"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	dashboardapp "github.com/donnel666/remail/internal/dashboard/app"
)

var dashboardMySQLTestServer = testmysql.New("remail_dashboard_test")

func TestMain(m *testing.M) {
	// Production runs with TZ=Asia/Shanghai and the MySQL DSN uses Asia/Shanghai.
	// Keep DATETIME encoding and SQL buckets deterministic on UTC CI hosts too.
	time.Local = businessday.Shanghai
	code := m.Run()
	_ = dashboardMySQLTestServer.Close(context.Background())
	os.Exit(code)
}

func newDashboardMySQLTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	migrationsDir := filepath.Clean(filepath.Join(filepath.Dir(file), "../../..", "migrations"))
	return dashboardMySQLTestServer.Database(t, migrationsDir)
}

// seedDashboardOrder inserts a minimal but constraint-valid order. status stays
// at the default 'pending_payment' so the debit/allocation/delivery CHECKs need
// no extra columns; the dashboard counts orders regardless of status.
func seedDashboardOrder(t *testing.T, db *gorm.DB, id, userID, projectID, productID uint, mode string, pay string, receiveStarted, createdAt time.Time) {
	seedTypedOrder(t, db, id, userID, projectID, productID, "microsoft", mode, pay, receiveStarted, createdAt)
}

func seedTypedOrder(t *testing.T, db *gorm.DB, id, userID, projectID, productID uint, productType, mode string, pay string, receiveStarted, createdAt time.Time) {
	t.Helper()
	fp := strings.Repeat("a", 64)
	require.NoError(t, db.Exec(`
INSERT INTO orders (id, order_no, user_id, project_id, project_product_id, product_type, service_mode,
    pay_amount, debit_tx_id, client_channel, idempotency_key, request_fingerprint, receive_started_at, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, 1, 'console', ?, ?, ?, ?, ?)`,
		id, "ORD-"+strconv.Itoa(int(id)), userID, projectID, productID, productType, mode, pay,
		"idem-"+strconv.Itoa(int(id)), fp, receiveStarted.UTC(), createdAt.UTC(), createdAt.UTC(),
	).Error)
}

func seedDashboardReceipt(t *testing.T, db *gorm.DB, orderID, messageID uint, receivedAt time.Time) {
	t.Helper()
	require.NoError(t, db.Exec(`
INSERT INTO mailmatch_messages (id, email_resource_id, resource_type, recipient, dedupe_key, received_at, created_at, updated_at)
VALUES (?, 1, 'microsoft', 'r@test.local', ?, ?, ?, ?)`,
		messageID, strings.Repeat("d", 60)+strconv.Itoa(int(messageID)), receivedAt.UTC(), receivedAt.UTC(), receivedAt.UTC(),
	).Error)
	require.NoError(t, db.Exec(`
INSERT INTO mailmatch_order_delivery_heads (order_id, message_id, message_received_at) VALUES (?, ?, ?)`,
		orderID, messageID, receivedAt.UTC(),
	).Error)
}

// TestConsoleDashboardViewRepoMySQL drives every raw aggregate query against a
// real MySQL so the SQL (DATE_FORMAT bucketing, delivery-head JOINs, the
// GROUP BY leaderboard and the standing subquery Count) is exercised end to end.
func TestConsoleDashboardViewRepoMySQL(t *testing.T) {
	db := newDashboardMySQLTestDB(t)
	ctx := context.Background()

	require.NoError(t, db.Exec(`
INSERT INTO users(id, email, password_hash, nickname, status, role) VALUES
    (2, 'buyer@test.local', 'hash', 'Buyer', 'active', 'user'),
    (3, 'regular@test.local', 'hash', '', 'active', 'user'),
    (4, 'four@test.local', 'hash', 'Four', 'active', 'user')`).Error)
	require.NoError(t, db.Exec(`
INSERT INTO projects(id, name, target_platform, logo_url, status, access_type, loose_match) VALUES
    (1, 'Test', 'trade', '', 'listed', 'public', TRUE),
    (10, 'Microsoft', 'trade', '', 'listed', 'public', TRUE),
    (11, 'Telegram', 'trade', '', 'listed', 'public', TRUE)`).Error)
	require.NoError(t, db.Exec(`
INSERT INTO project_products(
    id, project_id, type, status, code_enabled, purchase_enabled,
    code_price, purchase_price, code_supplier_price, purchase_supplier_price,
    code_window_minutes, activation_window_minutes, warranty_minutes,
    main_weight, dot_weight, plus_weight) VALUES
    (22, 1, 'microsoft', 'enabled', TRUE, TRUE, 1.00, 2.00, 0.50, 1.00, 10, 60, 1440, 1, 0, 0),
    (20, 10, 'microsoft', 'enabled', TRUE, TRUE, 1.00, 2.00, 0.50, 1.00, 10, 60, 1440, 1, 0, 0),
    (21, 11, 'microsoft', 'enabled', TRUE, TRUE, 1.00, 2.00, 0.50, 1.00, 10, 60, 1440, 1, 0, 0)`).Error)
	require.NoError(t, db.Exec(`
INSERT INTO wallets(user_id, consumer_balance, total_spend) VALUES (2, 640.12, 1200.50)`).Error)
	require.NoError(t, db.Exec(`
INSERT INTO email_resources(id, type, owner_user_id) VALUES (1, 'microsoft', 2)`).Error)
	// One debit transaction referenced by every seeded order's debit_tx_id so the
	// "actually charged" filter (debit_tx_id IS NOT NULL) keeps them.
	require.NoError(t, db.Exec(`
INSERT INTO wallet_transactions(id, transaction_no, user_id, transaction_type, balance_bucket, direction,
    amount, balance_before, balance_after, biz_type, biz_id)
VALUES (1, 'TX-1', 2, 'debit', 'consumer', 'out', -1.00, 100.00, 99.00, 'order', 'ORD')`).Error)

	ref := time.Now().UTC().Truncate(24 * time.Hour).Add(12 * time.Hour) // today, noon UTC
	receiveStart := ref.Add(-30 * time.Second)

	// user 2: two code orders with receipts, one unactivated purchase and one
	// activated purchase seeded below.
	seedDashboardOrder(t, db, 1, 2, 10, 20, "code", "12.00", receiveStart, ref)
	seedDashboardOrder(t, db, 2, 2, 10, 20, "purchase", "8.00", ref, ref)
	seedDashboardOrder(t, db, 3, 2, 11, 21, "code", "5.00", receiveStart, ref)
	// user 3: one code order with a receipt.
	seedDashboardOrder(t, db, 4, 3, 10, 20, "code", "3.00", receiveStart, ref)
	// user 4: one code order with a receipt — tied with user 3 (both 1 and the
	// same final success time) to exercise the user-id tie-break.
	seedDashboardOrder(t, db, 6, 4, 10, 20, "code", "3.00", receiveStart, ref)
	// A successfully activated purchase order also counts in the user leaderboard,
	// but stays excluded from code-receipt metrics.
	seedDashboardOrder(t, db, 5, 2, 10, 20, "purchase", "10.00", ref.Add(-45*time.Second), ref)
	require.NoError(t, db.Table("orders").Where("id = ?", 5).Update("activated_at", ref).Error)
	// A normal project 1 order still counts; leaderboard exclusion is based on
	// the HIST- order prefix, not a project ID.
	seedDashboardOrder(t, db, 7, 2, 1, 22, "code", "1.00", receiveStart, ref)
	seedDashboardOrder(t, db, 8, 3, 10, 20, "code", "1.00", receiveStart, ref)
	require.NoError(t, db.Table("orders").Where("id = ?", 8).Update("order_no", "HIST-TEST").Error)
	// Historical scan orders are internal records and must not inflate the user's
	// selected-range or today order counts.
	seedDashboardOrder(t, db, 9, 2, 10, 20, "purchase", "0.00", receiveStart, ref)
	require.NoError(t, db.Table("orders").Where("id = ?", 9).Update("order_no", "HIST-COUNT-TEST").Error)
	seedDashboardReceipt(t, db, 1, 101, ref)
	seedDashboardReceipt(t, db, 3, 102, ref)
	seedDashboardReceipt(t, db, 4, 103, ref)
	seedDashboardReceipt(t, db, 5, 104, ref)
	seedDashboardReceipt(t, db, 6, 105, ref)
	seedDashboardReceipt(t, db, 7, 106, ref)
	seedDashboardReceipt(t, db, 8, 107, ref)

	repo := NewViewRepo(db, nil)
	from := ref.Add(-6 * time.Hour)
	to := ref.Add(6 * time.Hour)
	since := time.Date(ref.Year(), ref.Month(), ref.Day(), 0, 0, 0, 0, time.UTC)
	const dayFmt = "%Y-%m-%d"

	balance, spent, err := repo.WalletSummary(ctx, 2)
	require.NoError(t, err)
	require.Equal(t, 640.12, balance)
	require.Equal(t, 1200.50, spent)

	orderRows, err := repo.OrderBuckets(ctx, 2, dayFmt, from, to)
	require.NoError(t, err)
	var orders, codeOrders, purchaseOrders int
	var spend float64
	for _, r := range orderRows {
		orders += r.Orders
		codeOrders += r.CodeOrders
		purchaseOrders += r.PurchaseOrders
		spend += r.Spend
	}
	require.Equal(t, 5, orders)     // includes one project 1 (Test) code order
	require.Equal(t, 3, codeOrders) // personal metrics still count that project
	require.Equal(t, 2, purchaseOrders)
	require.InDelta(t, 36.00, spend, 0.001)

	receiptRows, err := repo.ReceiptBuckets(ctx, 2, dayFmt, from, to)
	require.NoError(t, err)
	var received int
	for _, r := range receiptRows {
		received += r.Received
		require.Equal(t, 30, r.AvgSeconds)
	}
	require.Equal(t, 3, received) // order 5 purchase excluded; order 7 project 1 included

	activationRows, err := repo.PurchaseActivationBuckets(ctx, 2, dayFmt, from, to)
	require.NoError(t, err)
	require.Len(t, activationRows, 1)
	require.Equal(t, 1, activationRows[0].Activated)
	require.Equal(t, 45, activationRows[0].AvgSeconds)
	require.EqualValues(t, 45, activationRows[0].TotalSeconds)
	require.Equal(t, 1, activationRows[0].Timed)

	ranking, err := repo.ProjectCodeRanking(ctx, 2, from, to)
	require.NoError(t, err)
	require.Len(t, ranking, 3)
	require.Equal(t, "Microsoft", ranking[0].Name)
	require.Equal(t, 2, ranking[0].Count) // one delivered code order plus one activated purchase
	require.Equal(t, "Test", ranking[1].Name)
	require.Equal(t, 1, ranking[1].Count)
	require.Equal(t, "Telegram", ranking[2].Name)

	spendRows, err := repo.ProjectSpendBuckets(ctx, 2, dayFmt, from, to)
	require.NoError(t, err)
	byProject := map[uint]float64{}
	projectNames := map[uint]string{}
	for _, r := range spendRows {
		byProject[r.ProjectID] += r.Spend
		projectNames[r.ProjectID] = r.Name
	}
	require.InDelta(t, 30.00, byProject[10], 0.001)
	require.InDelta(t, 5.00, byProject[11], 0.001)
	require.InDelta(t, 1.00, byProject[1], 0.001)
	require.Equal(t, "Microsoft", projectNames[10])
	require.Equal(t, "Telegram", projectNames[11])

	avg, err := repo.RangeAvgReceiptSeconds(ctx, 2, from, to)
	require.NoError(t, err)
	require.Equal(t, 30, avg)

	for _, since := range []*time.Time{nil, &since} {
		leaders, err := repo.Leaderboard(ctx, since, 10)
		require.NoError(t, err)
		require.Len(t, leaders, 3)
		require.Equal(t, uint(2), leaders[0].UserID)
		require.Equal(t, 4, leaders[0].Count)
		require.Equal(t, "Buyer", leaders[0].Nickname)
		// users 3 and 4 tie at 1; ordered by user_id ASC.
		require.Equal(t, uint(3), leaders[1].UserID)
		require.Equal(t, 1, leaders[1].Count)
		require.Equal(t, uint(4), leaders[2].UserID)
		require.Equal(t, 1, leaders[2].Count)
	}

	standing2, err := repo.UserStanding(ctx, 2, nil)
	require.NoError(t, err)
	require.Equal(t, 4, standing2.Count)
	require.Equal(t, 1, standing2.Rank)

	// Tied users get distinct ordinal ranks matching the leaderboard order, not a
	// shared competition rank: user 3 is #2, user 4 is #3.
	standing3, err := repo.UserStanding(ctx, 3, nil)
	require.NoError(t, err)
	require.Equal(t, 1, standing3.Count)
	require.Equal(t, 2, standing3.Rank)

	standing4, err := repo.UserStanding(ctx, 4, nil)
	require.NoError(t, err)
	require.Equal(t, 1, standing4.Count)
	require.Equal(t, 3, standing4.Rank)

	redisServer := miniredis.RunT(t)
	redisClient := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	t.Cleanup(func() { require.NoError(t, redisClient.Close()) })
	cachedRepo := NewViewRepo(db, redisClient)
	todayStart := dashboardapp.TodayStart(ref)
	require.NoError(t, cachedRepo.RefreshLeaderboardCache(ctx, todayStart))
	cachedLeaders, err := cachedRepo.Leaderboard(ctx, &todayStart, 10)
	require.NoError(t, err)
	require.Len(t, cachedLeaders, 3)
	require.Equal(t, uint(2), cachedLeaders[0].UserID)
	require.Equal(t, 4, cachedLeaders[0].Count)
	cachedStanding, err := cachedRepo.UserStanding(ctx, 2, &todayStart)
	require.NoError(t, err)
	require.Equal(t, 4, cachedStanding.Count)
}

// TestAdminViewRepoMySQL drives the platform-wide admin aggregates against real
// MySQL: the product-type split, user new/active counts, the
// inventory snapshot WHERE clauses and the global project code ranking.
func TestAdminViewRepoMySQL(t *testing.T) {
	db := newDashboardMySQLTestDB(t)
	ctx := context.Background()

	ref := time.Now().UTC().Truncate(24 * time.Hour).Add(12 * time.Hour)
	receiveStart := ref.Add(-30 * time.Second)
	beforeRange := ref.Add(-30 * 24 * time.Hour)

	require.NoError(t, db.Exec(`
INSERT INTO users(id, email, password_hash, nickname, status, role, created_at, last_login_at) VALUES
	    (1, 'base@test.local', 'h', 'Base', 'active', 'user', ?, NULL),
	    (2, 'buyer@test.local', 'h', 'Buyer', 'active', 'supplier', ?, ?),
	    (3, 'fresh@test.local', 'h', 'Fresh', 'active', 'user', ?, NULL),
	    (4, 'deleted@test.local', 'h', 'Deleted', 'deleted', 'user', ?, ?)`,
		beforeRange, ref, ref, ref, beforeRange, ref).Error)
	require.NoError(t, db.Exec(`
INSERT INTO api_keys(id, user_id, name, key_prefix, key_plain, last_used_at, created_at, updated_at) VALUES
	    (1, 1, 'outside', 'rk-outside', 'rk-outside-plain', ?, ?, ?),
	    (2, 2, 'login-and-key', 'rk-buyer', 'rk-buyer-plain', ?, ?, ?),
	    (3, 3, 'key-only', 'rk-fresh', 'rk-fresh-plain', ?, ?, ?),
	    (4, 4, 'deleted-user', 'rk-deleted', 'rk-deleted-plain', ?, ?, ?)`,
		beforeRange, beforeRange, beforeRange,
		ref, ref, ref,
		ref, ref, ref,
		ref, beforeRange, beforeRange).Error)
	require.NoError(t, db.Exec(`
INSERT INTO projects(id, name, target_platform, logo_url, status, access_type, loose_match)
VALUES (10, 'Microsoft', 'trade', '', 'listed', 'public', TRUE)`).Error)
	require.NoError(t, db.Exec(`
INSERT INTO project_products(
    id, project_id, type, status, code_enabled, purchase_enabled,
    code_price, purchase_price, code_supplier_price, purchase_supplier_price,
    code_window_minutes, activation_window_minutes, warranty_minutes,
    main_weight, dot_weight, plus_weight)
VALUES (20, 10, 'microsoft', 'enabled', TRUE, TRUE, 1.00, 2.00, 0.50, 1.00, 10, 60, 1440, 1, 0, 0)`).Error)
	require.NoError(t, db.Exec(`
INSERT INTO email_resources(id, type, owner_user_id) VALUES (1, 'microsoft', 2)`).Error)
	require.NoError(t, db.Exec(`
INSERT INTO wallet_transactions(id, transaction_no, user_id, transaction_type, balance_bucket, direction,
    amount, balance_before, balance_after, biz_type, biz_id)
VALUES (1, 'TX-1', 2, 'debit', 'consumer', 'out', -1.00, 100.00, 99.00, 'order', 'ORD')`).Error)

	// Valid range cohort starts with 1 Microsoft code, 2 domain code and 2
	// Microsoft purchases (one activated), all charged.
	seedTypedOrder(t, db, 1, 2, 10, 20, "microsoft", "code", "12.00", receiveStart, ref)
	seedTypedOrder(t, db, 2, 2, 10, 20, "domain", "code", "5.00", receiveStart, ref)
	seedTypedOrder(t, db, 3, 2, 10, 20, "microsoft", "purchase", "8.00", ref, ref)
	seedTypedOrder(t, db, 4, 2, 10, 20, "domain", "code", "5.00", receiveStart, ref)
	seedTypedOrder(t, db, 9, 2, 10, 20, "microsoft", "purchase", "8.00", ref.Add(-45*time.Second), ref)
	require.NoError(t, db.Table("orders").Where("id = ?", 9).Update("activated_at", ref).Error)
	// Gmail variants share allocation_type=gmail, so dashboard grouping must use
	// the ordered SKU instead of collapsing them into the Gmail product.
	seedTypedOrder(t, db, 12, 2, 10, 20, "gmail", "code", "1.00", receiveStart, ref)
	seedTypedOrder(t, db, 13, 2, 10, 20, "gmail_variant", "code", "1.00", receiveStart, ref)
	seedTypedOrder(t, db, 14, 2, 10, 20, "icloud", "code", "1.00", receiveStart, ref)
	seedTypedOrder(t, db, 15, 2, 10, 20, "gmail", "purchase", "1.00", ref.Add(-35*time.Second), ref)
	seedTypedOrder(t, db, 16, 2, 10, 20, "gmail_variant", "purchase", "1.00", ref.Add(-40*time.Second), ref)
	seedTypedOrder(t, db, 17, 2, 10, 20, "icloud", "purchase", "1.00", ref, ref)
	require.NoError(t, db.Exec(`UPDATE orders SET allocation_type = CASE
WHEN product_type IN ('gmail', 'gmail_variant') THEN 'gmail'
WHEN product_type = 'icloud' THEN 'icloud'
END WHERE id BETWEEN 12 AND 17`).Error)
	require.NoError(t, db.Table("orders").Where("id IN ?", []uint{15, 16}).Update("activated_at", ref).Error)
	// Historical purchases are charged records but not platform orders.
	seedTypedOrder(t, db, 6, 2, 10, 20, "microsoft", "purchase", "0.00", ref, ref)
	require.NoError(t, db.Table("orders").Where("id = ?", 6).Update("order_no", "HIST-ADMIN-COUNT-TEST").Error)
	seedTypedOrder(t, db, 7, 2, 10, 20, "microsoft", "code", "0.00", receiveStart, ref)
	require.NoError(t, db.Table("orders").Where("id = ?", 7).Update("order_no", "HIST-ADMIN-CODE-TEST").Error)
	seedTypedOrder(t, db, 8, 2, 10, 20, "microsoft", "code", "0.00", receiveStart, ref)
	require.NoError(t, db.Table("orders").Where("id = ?", 8).Update("debit_tx_id", nil).Error)
	seedDashboardReceipt(t, db, 1, 101, ref)
	seedDashboardReceipt(t, db, 2, 102, ref)
	seedDashboardReceipt(t, db, 4, 103, ref)
	seedDashboardReceipt(t, db, 7, 105, ref)
	seedDashboardReceipt(t, db, 8, 106, ref)
	seedDashboardReceipt(t, db, 12, 112, ref)
	seedDashboardReceipt(t, db, 13, 113, ref)
	seedDashboardReceipt(t, db, 14, 114, ref)
	// A valid paid/code fact outside the selected range must not affect any
	// range-scoped order, receipt or ranking metric.
	seedTypedOrder(t, db, 5, 2, 10, 20, "microsoft", "code", "9.00", beforeRange, beforeRange)
	seedDashboardReceipt(t, db, 5, 104, beforeRange)

	// Microsoft inventory: normal+for_sale+graph (available), normal not-for-sale
	// (total only), deleted (excluded from total).
	require.NoError(t, db.Exec(`
INSERT INTO email_resources(id, type, owner_user_id) VALUES (2,'microsoft',2),(3,'microsoft',2),(4,'microsoft',2)`).Error)
	require.NoError(t, db.Exec(`
INSERT INTO microsoft_resources(id, email_address, password, status, for_sale, graph_available) VALUES
    (2, 'a@x.test', 'p', 'normal', TRUE, TRUE),
    (3, 'b@x.test', 'p', 'normal', FALSE, TRUE),
    (4, 'c@x.test', 'p', 'deleted', TRUE, TRUE)`).Error)
	require.NoError(t, db.Exec(`
INSERT INTO email_resources(id, type, owner_user_id) VALUES
	    (20,'gmail',2),(21,'gmail',2),(22,'gmail',4),
	    (30,'icloud',2),(31,'icloud',2),(32,'icloud',2),
	    (40,'domain',2)`).Error)
	require.NoError(t, db.Exec(`
INSERT INTO gmail_resources(
    id, resource_type, owner_user_id, email, identity, password,
    two_factor_secret, app_password, for_sale, status, alloc_bucket
) VALUES
    (20, 'gmail', 2, 'available@gmail.com', 'available@gmail.com', 'p', 'secret', 'app', TRUE, 'normal', 20),
    (21, 'gmail', 2, 'private@gmail.com', 'private@gmail.com', 'p', 'secret', 'app', FALSE, 'normal', 21),
	    (22, 'gmail', 4, 'unavailable@gmail.com', 'unavailable@gmail.com', 'p', 'secret', 'app', TRUE, 'normal', 22)`).Error)
	require.NoError(t, db.Table("users").Where("id = ?", 4).Update("role", "supplier").Error)
	require.NoError(t, db.Exec(`
INSERT INTO mail_servers(id, owner_user_id, server_address, status)
VALUES (40, 2, 'mail.relay.example', 'online')`).Error)
	require.NoError(t, db.Exec(`
INSERT INTO domain_resources(id, resource_type, owner_user_id, domain, mail_server_id, purpose, status)
VALUES (40, 'domain', 2, 'relay.example', 40, 'binding', 'normal')`).Error)
	require.NoError(t, db.Exec(`
INSERT INTO icloud_resources(id, primary_email, expire_at, for_sale, status, alias_count) VALUES
	    (30, 'available@icloud.com', DATE_ADD(UTC_TIMESTAMP(), INTERVAL 1 DAY), TRUE, 'normal', 1),
	    (31, 'private@icloud.com', DATE_ADD(UTC_TIMESTAMP(), INTERVAL 1 DAY), FALSE, 'normal', 1),
	    (32, 'deleted@icloud.com', DATE_ADD(UTC_TIMESTAMP(), INTERVAL 1 DAY), TRUE, 'deleted', 1)`).Error)
	require.NoError(t, db.Exec(`
INSERT INTO icloud_aliases(resource_id, anonymous_id, email, forward_to_email, status)
VALUES (30, 'available-alias', 'available-alias@icloud.com', 'inbox@relay.example', 'normal')`).Error)
	previousForwardingSuffixes := runtimeconfig.String(runtimeconfig.ICloudForwardingSuffixesKey, "")
	runtimeconfig.Set(runtimeconfig.ICloudForwardingSuffixesKey, "relay.example")
	t.Cleanup(func() {
		if previousForwardingSuffixes == "" {
			runtimeconfig.Delete(runtimeconfig.ICloudForwardingSuffixesKey)
		} else {
			runtimeconfig.Set(runtimeconfig.ICloudForwardingSuffixesKey, previousForwardingSuffixes)
		}
	})
	repo := NewAdminViewRepo(db)
	from := ref.Add(-6 * time.Hour)
	to := ref.Add(6 * time.Hour)
	const dayFmt = "%Y-%m-%d"

	orderRows, err := repo.OrderTrend(ctx, dayFmt, from, to)
	require.NoError(t, err)
	require.Equal(t, 11, sumCountBuckets(orderRows))

	codeOrders, err := repo.CodeOrderTrend(ctx, dayFmt, from, to)
	require.NoError(t, err)
	codeCounts := map[string]int{}
	for _, r := range codeOrders {
		codeCounts[r.ProductType] += r.Count
	}
	require.Equal(t, map[string]int{"microsoft": 1, "domain": 2, "gmail": 1, "gmail_variant": 1, "icloud": 1}, codeCounts)

	receipts, err := repo.CodeReceiptTrend(ctx, dayFmt, from, to)
	require.NoError(t, err)
	receiptCounts := map[string]int{}
	for _, r := range receipts {
		require.Equal(t, 30, r.AvgSeconds)
		require.EqualValues(t, 30*r.Received, r.TotalSeconds)
		require.Equal(t, r.Received, r.Timed)
		receiptCounts[r.ProductType] += r.Received
	}
	require.Equal(t, map[string]int{"microsoft": 1, "domain": 2, "gmail": 1, "gmail_variant": 1, "icloud": 1}, receiptCounts)

	purchases, err := repo.PurchaseSummaries(ctx, from, to)
	require.NoError(t, err)
	purchasesByType := map[string]dashboardapp.PurchaseSummary{}
	for _, purchase := range purchases {
		purchasesByType[purchase.ProductType] = purchase.PurchaseSummary
	}
	require.Equal(t, dashboardapp.PurchaseSummary{Orders: 2, Activated: 1, TotalSeconds: 45, Timed: 1}, purchasesByType["microsoft"])
	require.Equal(t, dashboardapp.PurchaseSummary{Orders: 1, Activated: 1, TotalSeconds: 35, Timed: 1}, purchasesByType["gmail"])
	require.Equal(t, dashboardapp.PurchaseSummary{Orders: 1, Activated: 1, TotalSeconds: 40, Timed: 1}, purchasesByType["gmail_variant"])
	require.Equal(t, dashboardapp.PurchaseSummary{Orders: 1}, purchasesByType["icloud"])

	newUsers, err := repo.NewUserTrend(ctx, dayFmt, from, to)
	require.NoError(t, err)
	require.Equal(t, 2, sumCountBuckets(newUsers)) // users 2 and 3

	activeUsers, err := repo.ActiveUserTrend(ctx, dayFmt, from, to)
	require.NoError(t, err)
	// User 2 is counted once despite both login and key activity; user 3 is
	// active via key only. User 1 is outside the range and user 4 is deleted.
	require.Equal(t, 2, sumCountBuckets(activeUsers))

	totalUsers, err := repo.TotalUsers(ctx)
	require.NoError(t, err)
	require.Equal(t, 3, totalUsers) // deleted user 4 is excluded

	snap, err := repo.InventorySnapshot(ctx)
	require.NoError(t, err)
	require.Equal(t, 2, snap.MicrosoftTotal)     // res 2 and 3 (4 deleted)
	require.Equal(t, 1, snap.MicrosoftAvailable) // res 2 only
	require.Equal(t, 0, snap.DomainTotal)        // no generated mailboxes seeded
	require.Equal(t, 0, snap.DomainAvailable)
	require.Equal(t, 3, snap.GmailTotal)
	require.Equal(t, 1, snap.GmailAvailable)
	require.Equal(t, 2, snap.ICloudTotal)
	require.Equal(t, 1, snap.ICloudAvailable)

	ranking, err := repo.ProjectCodeRanking(ctx, from, to, 10)
	require.NoError(t, err)
	require.Len(t, ranking, 1)
	require.Equal(t, "Microsoft", ranking[0].Name)
	require.Equal(t, 6, ranking[0].Count) // 6 code receipts across the project

	// 2025-12-31 16:30 UTC is 2026-01-01 00:30 in Shanghai. This covers the
	// UTC/Shanghai day and year boundary without applying a second +08:00 shift
	// to DATETIME values already encoded in Asia/Shanghai.
	boundary := time.Date(2025, 12, 31, 16, 30, 0, 0, time.UTC)
	require.NoError(t, db.Exec(`
INSERT INTO users(id, email, password_hash, nickname, status, role, created_at, last_login_at)
VALUES (10, 'boundary@test.local', 'h', 'Boundary', 'active', 'user', ?, ?)`, boundary, boundary).Error)
	seedTypedOrder(t, db, 10, 10, 10, 20, "microsoft", "code", "1.00", boundary.Add(-30*time.Second), boundary)
	seedDashboardReceipt(t, db, 10, 110, boundary)
	seedTypedOrder(t, db, 11, 10, 10, 20, "microsoft", "purchase", "2.00", boundary.Add(-45*time.Second), boundary)
	require.NoError(t, db.Table("orders").Where("id = ?", 11).Update("activated_at", boundary).Error)
	boundaryFrom, boundaryTo := boundary.Add(-30*time.Minute), boundary.Add(30*time.Minute)

	boundaryOrders, err := repo.OrderTrend(ctx, dayFmt, boundaryFrom, boundaryTo)
	require.NoError(t, err)
	require.Equal(t, []dashboardapp.CountBucket{{Bucket: "2026-01-01", Count: 2}}, boundaryOrders)

	const hourFmt = "%Y-%m-%d %H:00:00"
	boundaryCodeOrders, err := repo.CodeOrderTrend(ctx, hourFmt, boundaryFrom, boundaryTo)
	require.NoError(t, err)
	require.Len(t, boundaryCodeOrders, 1)
	require.Equal(t, "2026-01-01 00:00:00", boundaryCodeOrders[0].Bucket)

	boundaryReceipts, err := repo.CodeReceiptTrend(ctx, dayFmt, boundaryFrom, boundaryTo)
	require.NoError(t, err)
	require.Len(t, boundaryReceipts, 1)
	require.Equal(t, "2026-01-01", boundaryReceipts[0].Bucket)

	boundaryUsers, err := repo.NewUserTrend(ctx, dayFmt, boundaryFrom, boundaryTo)
	require.NoError(t, err)
	require.Equal(t, []dashboardapp.CountBucket{{Bucket: "2026-01-01", Count: 1}}, boundaryUsers)

	boundaryActiveUsers, err := repo.ActiveUserTrend(ctx, hourFmt, boundaryFrom, boundaryTo)
	require.NoError(t, err)
	require.Equal(t, []dashboardapp.CountBucket{{Bucket: "2026-01-01 00:00:00", Count: 1}}, boundaryActiveUsers)

	boundaryPurchases, err := repo.PurchaseSummaries(ctx, boundaryFrom, boundaryTo)
	require.NoError(t, err)
	require.Equal(t, []dashboardapp.TypePurchaseSummary{{ProductType: "microsoft", PurchaseSummary: dashboardapp.PurchaseSummary{Orders: 1, Activated: 1, TotalSeconds: 45, Timed: 1}}}, boundaryPurchases)

	consoleRepo := NewViewRepo(db, nil)
	consoleOrders, err := consoleRepo.OrderBuckets(ctx, 10, hourFmt, boundaryFrom, boundaryTo)
	require.NoError(t, err)
	require.Len(t, consoleOrders, 1)
	require.Equal(t, "2026-01-01 00:00:00", consoleOrders[0].Bucket)
	require.Equal(t, 1, consoleOrders[0].CodeOrders)
	require.Equal(t, 1, consoleOrders[0].PurchaseOrders)

	consoleReceipts, err := consoleRepo.ReceiptBuckets(ctx, 10, dayFmt, boundaryFrom, boundaryTo)
	require.NoError(t, err)
	require.Len(t, consoleReceipts, 1)
	require.Equal(t, "2026-01-01", consoleReceipts[0].Bucket)

	consoleActivations, err := consoleRepo.PurchaseActivationBuckets(ctx, 10, hourFmt, boundaryFrom, boundaryTo)
	require.NoError(t, err)
	require.Len(t, consoleActivations, 1)
	require.Equal(t, "2026-01-01 00:00:00", consoleActivations[0].Bucket)

	consoleSpend, err := consoleRepo.ProjectSpendBuckets(ctx, 10, dayFmt, boundaryFrom, boundaryTo)
	require.NoError(t, err)
	require.Len(t, consoleSpend, 1)
	require.Equal(t, "2026-01-01", consoleSpend[0].Bucket)
}

func sumCountBuckets(rows []dashboardapp.CountBucket) int {
	total := 0
	for _, r := range rows {
		total += r.Count
	}
	return total
}
