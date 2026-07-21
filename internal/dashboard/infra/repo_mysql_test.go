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
	"github.com/donnel666/remail/internal/platform/testmysql"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	dashboardapp "github.com/donnel666/remail/internal/dashboard/app"
)

var dashboardMySQLTestServer = testmysql.New("remail_dashboard_test")

func TestMain(m *testing.M) {
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
	// user 4: one code order with a receipt — tied with user 3 (both 1) to exercise
	// the leaderboard's ordinal tie-break (user 3 ranks ahead of user 4 by id).
	seedDashboardOrder(t, db, 6, 4, 10, 20, "code", "3.00", receiveStart, ref)
	// A successfully activated purchase order also counts in the user leaderboard,
	// but stays excluded from code-receipt metrics.
	seedDashboardOrder(t, db, 5, 2, 10, 20, "purchase", "10.00", ref, ref)
	require.NoError(t, db.Table("orders").Where("id = ?", 5).Update("activated_at", ref).Error)
	// Project 1 (Test) orders succeed but must not affect the public leaderboard.
	seedDashboardOrder(t, db, 7, 2, 1, 22, "code", "1.00", receiveStart, ref)
	seedDashboardOrder(t, db, 8, 3, 1, 22, "code", "1.00", receiveStart, ref)
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
	var orders, codeOrders int
	var spend float64
	for _, r := range orderRows {
		orders += r.Orders
		codeOrders += r.CodeOrders
		spend += r.Spend
	}
	require.Equal(t, 5, orders)     // includes one project 1 (Test) code order
	require.Equal(t, 3, codeOrders) // personal metrics still count that project
	require.InDelta(t, 36.00, spend, 0.001)

	receiptRows, err := repo.ReceiptBuckets(ctx, 2, dayFmt, from, to)
	require.NoError(t, err)
	var received int
	for _, r := range receiptRows {
		received += r.Received
		require.Equal(t, 30, r.AvgSeconds)
	}
	require.Equal(t, 3, received) // order 5 purchase excluded; order 7 project 1 included

	ranking, err := repo.ProjectCodeRanking(ctx, 2, from, to)
	require.NoError(t, err)
	require.Len(t, ranking, 3)
	// All three tie at 1; order by project_id ASC.
	require.Equal(t, "Test", ranking[0].Name)
	require.Equal(t, 1, ranking[0].Count)
	require.Equal(t, "Microsoft", ranking[1].Name)
	require.Equal(t, "Telegram", ranking[2].Name)

	spendRows, err := repo.ProjectSpendBuckets(ctx, 2, []uint{10, 11}, dayFmt, from, to)
	require.NoError(t, err)
	byProject := map[uint]float64{}
	for _, r := range spendRows {
		byProject[r.ProjectID] += r.Spend
	}
	require.InDelta(t, 30.00, byProject[10], 0.001)
	require.InDelta(t, 5.00, byProject[11], 0.001)

	todayOrders, todayReceipts, err := repo.TodayCounts(ctx, 2, since)
	require.NoError(t, err)
	require.Equal(t, 5, todayOrders)
	require.Equal(t, 3, todayReceipts)

	avg, err := repo.RangeAvgReceiptSeconds(ctx, 2, from, to)
	require.NoError(t, err)
	require.Equal(t, 30, avg)

	for _, since := range []*time.Time{nil, &since} {
		leaders, err := repo.Leaderboard(ctx, since, 10)
		require.NoError(t, err)
		require.Len(t, leaders, 3)
		require.Equal(t, uint(2), leaders[0].UserID)
		require.Equal(t, 3, leaders[0].Count)
		require.Equal(t, "Buyer", leaders[0].Nickname)
		// users 3 and 4 tie at 1; ordered by user_id ASC.
		require.Equal(t, uint(3), leaders[1].UserID)
		require.Equal(t, 1, leaders[1].Count)
		require.Equal(t, uint(4), leaders[2].UserID)
		require.Equal(t, 1, leaders[2].Count)
	}

	standing2, err := repo.UserStanding(ctx, 2, nil)
	require.NoError(t, err)
	require.Equal(t, 3, standing2.Count)
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
	require.Equal(t, 3, cachedLeaders[0].Count)
	cachedStanding, err := cachedRepo.UserStanding(ctx, 2, &todayStart)
	require.NoError(t, err)
	require.Equal(t, 3, cachedStanding.Count)
}

// TestAdminViewRepoMySQL drives the platform-wide admin aggregates against real
// MySQL: the microsoft/domain product-type split, user new/active counts, the
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
	    (2, 'buyer@test.local', 'h', 'Buyer', 'active', 'user', ?, ?),
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

	// orders: 1 microsoft code, 2 domain code, 1 microsoft purchase (all charged).
	seedTypedOrder(t, db, 1, 2, 10, 20, "microsoft", "code", "12.00", receiveStart, ref)
	seedTypedOrder(t, db, 2, 2, 10, 20, "domain", "code", "5.00", receiveStart, ref)
	seedTypedOrder(t, db, 3, 2, 10, 20, "microsoft", "purchase", "8.00", ref, ref)
	seedTypedOrder(t, db, 4, 2, 10, 20, "domain", "code", "5.00", receiveStart, ref)
	seedDashboardReceipt(t, db, 1, 101, ref)
	seedDashboardReceipt(t, db, 2, 102, ref)
	seedDashboardReceipt(t, db, 4, 103, ref)
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

	repo := NewAdminViewRepo(db)
	from := ref.Add(-6 * time.Hour)
	to := ref.Add(6 * time.Hour)
	const dayFmt = "%Y-%m-%d"

	orderRows, err := repo.OrderTrend(ctx, dayFmt, from, to)
	require.NoError(t, err)
	require.Equal(t, 4, sumCountBuckets(orderRows))

	codeOrders, err := repo.CodeOrderTrend(ctx, dayFmt, from, to)
	require.NoError(t, err)
	ms, domain := 0, 0
	for _, r := range codeOrders {
		if r.ProductType == "domain" {
			domain += r.Count
		} else {
			ms += r.Count
		}
	}
	require.Equal(t, 1, ms)     // order 1
	require.Equal(t, 2, domain) // orders 2 and 4 (purchase order 3 excluded)

	receipts, err := repo.CodeReceiptTrend(ctx, dayFmt, from, to)
	require.NoError(t, err)
	msR, domainR := 0, 0
	for _, r := range receipts {
		require.Equal(t, 30, r.AvgSeconds)
		if r.ProductType == "domain" {
			domainR += r.Received
		} else {
			msR += r.Received
		}
	}
	require.Equal(t, 1, msR)
	require.Equal(t, 2, domainR)

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

	ranking, err := repo.ProjectCodeRanking(ctx, from, to, 10)
	require.NoError(t, err)
	require.Len(t, ranking, 1)
	require.Equal(t, "Microsoft", ranking[0].Name)
	require.Equal(t, 3, ranking[0].Count) // 3 code receipts across the project
}

func sumCountBuckets(rows []dashboardapp.CountBucket) int {
	total := 0
	for _, r := range rows {
		total += r.Count
	}
	return total
}
