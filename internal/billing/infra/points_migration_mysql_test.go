package infra

import (
	"context"
	"database/sql"
	"testing"

	"github.com/donnel666/remail/internal/platform"
	"github.com/donnel666/remail/internal/platform/testmysql"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"
)

var pointsMigrationMySQLTestServer = testmysql.New("remail_points_migration")

func TestPointsMigrationConvertsEveryStoredAmountExactlyOnceMySQL(t *testing.T) {
	db := pointsMigrationMySQLTestServer.Database(t, copyBillingMigrationsThrough(t, 67))
	sqlDB, err := db.DB()
	require.NoError(t, err)
	goose.SetTableName("goose_db_version")
	require.NoError(t, goose.SetDialect("mysql"))
	version, err := goose.GetDBVersion(sqlDB)
	require.NoError(t, err)
	require.EqualValues(t, 67, version)
	require.NoError(t, db.Exec("DELETE FROM system_settings").Error)

	ctx := context.Background()
	conn, err := sqlDB.Conn(ctx)
	require.NoError(t, err)
	require.NoError(t, execPointsMigrationSQL(ctx, conn,
		"SET FOREIGN_KEY_CHECKS = 0",
		`INSERT INTO wallets(
    user_id, consumer_balance, supplier_available, supplier_frozen, total_spend, total_recharged
) VALUES (9001, 1.234567, 2.5, 3.75, 4.125, 5.875)`,
		`INSERT INTO wallet_transactions(
    id, transaction_no, user_id, transaction_type, balance_bucket, direction,
    amount, balance_before, balance_after, biz_type, biz_id
) VALUES (9001, 'POINTS-TX-1', 9001, 'debit', 'consumer', 'out', -0.25, 5, 4.75, 'order', 'points-seed')`,
		`INSERT INTO project_products(
    id, project_id, type, code_price, purchase_price, code_supplier_price, purchase_supplier_price
) VALUES (9001, 9001, 'microsoft', 0.000001, 0.01, 0.005, 1.234567)`,
		`UPDATE user_groups SET topup_threshold = 2.5 WHERE id = 1`,
		`INSERT INTO card_keys(card_key, amount) VALUES ('POINTS-CARD-1', 0.01)`,
		`INSERT INTO referral_rewards(
    id, inviter_user_id, invitee_user_id, invite_code, source_transaction_id, source_amount, reward_amount
) VALUES (9001, 9001, 9002, 'POINTS-INVITE', 9001, 0.02, 0.003)`,
		`INSERT INTO recharges(
    id, recharge_no, user_id, payment_method, recharge_quota, payment_amount, status
) VALUES (9001, 'POINTS-RECHARGE-1', 9001, 'alipay', 10.125, 0.02, 'credited')`,
		`INSERT INTO daily_checkins(
    id, user_id, business_date, reward_amount, checked_in_at
) VALUES (9001, 9001, '2026-07-28', 0.00001, '2026-07-28 08:00:00')`,
		`INSERT INTO leaderboard_settlements(
    id, business_date, period_start, period_end, rules_snapshot, settled_at
) VALUES (
    9001, '2026-07-28', '2026-07-27 00:00:00', '2026-07-28 00:00:00',
    '[{"rankFrom":1,"rankTo":1,"amount":0.03},{"rankFrom":2,"rankTo":2,"amount":1.234567}]',
    '2026-07-28 00:01:00'
)`,
		`INSERT INTO leaderboard_rewards(
    id, settlement_id, user_id, rank_no, score, reward_amount, wallet_transaction_id
) VALUES (9001, 9001, 9001, 1, 10, 0.03, 9001)`,
		`INSERT INTO orders(
    id, order_no, user_id, project_id, project_product_id, product_type, service_mode,
    supply_policy, status, failure_code, pay_amount, random_microsoft_pay_amount,
    random_domain_pay_amount, refund_amount, debit_tx_id, refund_tx_id,
    allocation_type, delivery_email, client_channel,
    idempotency_key, request_fingerprint
) VALUES
    (9101, 'POINTS-ORDER-PENDING',   9001, 9001, 9001, 'microsoft', 'code', 'private_first', 'pending_payment', '',        0.001, NULL,  NULL, 0,     NULL, NULL, NULL,        '',                  'console', 'points-order-1', REPEAT('a', 64)),
    (9102, 'POINTS-ORDER-PAID',      9001, 9001, 9001, 'microsoft', 'code', 'private_first', 'paid',            '',        0.002, NULL,  NULL, 0,     9001, NULL, NULL,        '',                  'console', 'points-order-2', REPEAT('b', 64)),
    (9103, 'POINTS-ORDER-ACTIVE',    9001, 9001, 9001, 'microsoft', 'code', 'private_first', 'active',          '',        0.003, NULL,  NULL, 0,     9001, NULL, 'microsoft', 'active@example.test', 'console', 'points-order-3', REPEAT('c', 64)),
    (9104, 'POINTS-ORDER-COMPLETED', 9001, 9001, 9001, 'microsoft', 'code', 'private_first', 'completed',       '',        0.004, NULL,  NULL, 0,     9001, NULL, 'microsoft', 'done@example.test',   'console', 'points-order-4', REPEAT('d', 64)),
    (9105, 'POINTS-ORDER-REFUNDED',  9001, 9001, 9001, 'microsoft', 'code', 'private_first', 'refunded',        '',        0.005, NULL,  NULL, 0.004, 9001, 9002, NULL,        '',                  'console', 'points-order-5', REPEAT('e', 64)),
    (9106, 'POINTS-ORDER-FAILED',    9001, 9001, 9001, 'microsoft', 'code', 'private_first', 'failed',          'unknown', 0.006, NULL,  NULL, 0,     NULL, NULL, NULL,        '',                  'console', 'points-order-6', REPEAT('f', 64)),
    (9107, 'POINTS-ORDER-CLOSED',    9001, 9001, 9001, 'microsoft', 'code', 'private_first', 'closed',          '',        0.007, NULL,  NULL, 0,     NULL, NULL, NULL,        '',                  'console', 'points-order-7', REPEAT('1', 64)),
    (9108, 'POINTS-ORDER-RANDOM',    9001, 9001, 9001, 'random',    'code', 'private_first', 'pending_payment', '',        0.01,  0.008, 0.009, 0,     NULL, NULL, NULL,        '',                  'console', 'points-order-8', REPEAT('2', 64))`,
	))

	legacyRefundMessage := "平台已退款 " + string(rune(0x00a5)) + "6.8 并关闭工单。"
	legacyWithdrawalMessage := "提现金额：" + string(rune(0xffe5)) + "12.50\n提现方式：支付宝\n备注：请处理"
	legacyWithdrawalPreview := "提现金额：" + string(rune(0xffe5)) + "12.50 提现方式：支付宝 备注：请处理"
	_, err = conn.ExecContext(ctx, `INSERT INTO aftersale_tickets(
    id, ticket_no, ticket_type, title, status, requester_user_id, order_no,
    pay_amount, service_mode, resolution_kind, refund_amount,
    last_message_preview, last_message_sender_type
) VALUES (9001, 'POINTS-TICKET-1', 'order', 'refund', 'closed', 9001, 'POINTS-ORDER-REFUNDED',
          6.8, 'code', 'refunded', 6.8, ?, 'system')`, legacyRefundMessage)
	require.NoError(t, err)
	_, err = conn.ExecContext(ctx, `INSERT INTO aftersale_tickets(
    id, ticket_no, ticket_type, title, status, requester_user_id,
    pay_amount, refund_amount, last_message_preview, last_message_sender_type
) VALUES (9002, 'POINTS-TICKET-2', 'general', '供应商提现申请', 'open', 9001,
          0, 0, ?, 'user')`, legacyWithdrawalPreview)
	require.NoError(t, err)
	_, err = conn.ExecContext(ctx, `INSERT INTO aftersale_ticket_messages(
    id, ticket_no, sender_type, content
) VALUES
    (9001, 'POINTS-TICKET-1', 'system', ?),
    (9002, 'POINTS-TICKET-1', 'user', ?),
    (9003, 'POINTS-TICKET-2', 'user', ?)`, legacyRefundMessage, legacyRefundMessage, legacyWithdrawalMessage)
	require.NoError(t, err)
	require.NoError(t, execPointsMigrationSQL(ctx, conn,
		`INSERT INTO system_settings(`+"`key`, `value`"+`) VALUES
    ('registration_reward_amount', '0.00001'),
    ('single_rebate_cap', '1.1'),
    ('cumulative_rebate_cap', '2.2'),
    ('min_topup_amount', '10'),
    ('topup_fee_cap', '0.5'),
    ('default_project_microsoft_code_price', '0.008'),
    ('default_project_microsoft_code_supplier_price', '0.005'),
    ('default_project_microsoft_purchase_price', '0.01'),
    ('default_project_microsoft_purchase_supplier_price', '0.007'),
    ('default_project_domain_code_price', '0.08'),
    ('default_project_domain_code_supplier_price', '0.04'),
    ('default_project_domain_purchase_price', '0'),
    ('default_project_domain_purchase_supplier_price', '0'),
    ('topup_amount_presets', '[10,20.5]'),
    ('topup_amount_bonus', '{"10":0.5,"20.5":1.25}'),
    ('daily_checkin_reward_rules', '[{"amount":0.00001,"probability":1}]'),
    ('leaderboard_reward_rules', '[{"rankFrom":1,"rankTo":2,"amount":0.02}]')`,
		"SET FOREIGN_KEY_CHECKS = 1",
	))
	require.NoError(t, conn.Close())

	require.NoError(t, goose.UpTo(sqlDB, billingMigrationsDir(t), 68))
	version, err = goose.GetDBVersion(sqlDB)
	require.NoError(t, err)
	require.EqualValues(t, 68, version)
	require.NoError(t, platform.VerifyPointsUnitMigration(sqlDB))

	requirePointsMigrationValues(t, sqlDB, `SELECT
    consumer_balance, supplier_available, supplier_frozen, total_spend, total_recharged
FROM wallets WHERE user_id = 9001`,
		"1234.567000", "2500.000000", "3750.000000", "4125.000000", "5875.000000")
	requirePointsMigrationValues(t, sqlDB, `SELECT amount, balance_before, balance_after
FROM wallet_transactions WHERE id = 9001`, "-250.000000", "5000.000000", "4750.000000")
	requirePointsMigrationValues(t, sqlDB, `SELECT
    code_price, purchase_price, code_supplier_price, purchase_supplier_price
FROM project_products WHERE id = 9001`, "0.001000", "10.000000", "5.000000", "1234.567000")
	requirePointsMigrationValues(t, sqlDB, `SELECT topup_threshold FROM user_groups WHERE id = 1`, "2500.000000")
	requirePointsMigrationValues(t, sqlDB, `SELECT amount FROM card_keys WHERE card_key = 'POINTS-CARD-1'`, "10.000000")
	requirePointsMigrationValues(t, sqlDB, `SELECT source_amount, reward_amount FROM referral_rewards WHERE id = 9001`, "20.000000", "3.000000")
	requirePointsMigrationValues(t, sqlDB, `SELECT recharge_quota, payment_amount FROM recharges WHERE id = 9001`, "10125.000000", "0.02")
	requirePointsMigrationValues(t, sqlDB, `SELECT reward_amount FROM daily_checkins WHERE id = 9001`, "0.010000")
	requirePointsMigrationValues(t, sqlDB, `SELECT reward_amount FROM leaderboard_rewards WHERE id = 9001`, "30.000000")
	requirePointsMigrationValues(t, sqlDB, `SELECT pay_amount, refund_amount FROM aftersale_tickets WHERE id = 9001`, "6800.000000", "6800.000000")

	rows, err := sqlDB.Query(`SELECT id, pay_amount FROM orders WHERE id BETWEEN 9101 AND 9108 ORDER BY id`)
	require.NoError(t, err)
	defer rows.Close()
	expectedOrderAmounts := []string{"1.000000", "2.000000", "3.000000", "4.000000", "5.000000", "6.000000", "7.000000", "10.000000"}
	for index, want := range expectedOrderAmounts {
		require.True(t, rows.Next())
		var id int
		var got string
		require.NoError(t, rows.Scan(&id, &got))
		require.Equal(t, 9101+index, id)
		require.Equal(t, want, got)
	}
	require.False(t, rows.Next())
	require.NoError(t, rows.Err())
	requirePointsMigrationValues(t, sqlDB, `SELECT refund_amount FROM orders WHERE id = 9105`, "4.000000")
	requirePointsMigrationValues(t, sqlDB, `SELECT random_microsoft_pay_amount, random_domain_pay_amount FROM orders WHERE id = 9108`, "8.000000", "9.000000")

	for key, want := range map[string]string{
		"registration_reward_amount":                        "0.010000",
		"single_rebate_cap":                                 "1100.000000",
		"cumulative_rebate_cap":                             "2200.000000",
		"min_topup_amount":                                  "10000.000000",
		"topup_fee_cap":                                     "500.000000",
		"default_project_microsoft_code_price":              "8.000000",
		"default_project_microsoft_code_supplier_price":     "5.000000",
		"default_project_microsoft_purchase_price":          "10.000000",
		"default_project_microsoft_purchase_supplier_price": "7.000000",
		"default_project_domain_code_price":                 "80.000000",
		"default_project_domain_code_supplier_price":        "40.000000",
		"default_project_domain_purchase_price":             "0.000000",
		"default_project_domain_purchase_supplier_price":    "0.000000",
	} {
		t.Run("setting_"+key, func(t *testing.T) {
			var got string
			require.NoError(t, sqlDB.QueryRow(
				"SELECT CAST(`value` AS DECIMAL(18,6)) FROM system_settings WHERE `key` = ?", key,
			).Scan(&got))
			require.Equal(t, want, got)
		})
	}

	var raw string
	require.NoError(t, sqlDB.QueryRow("SELECT `value` FROM system_settings WHERE `key` = 'topup_amount_presets'").Scan(&raw))
	require.JSONEq(t, `[10000,20500]`, raw)
	require.NoError(t, sqlDB.QueryRow("SELECT `value` FROM system_settings WHERE `key` = 'topup_amount_bonus'").Scan(&raw))
	require.JSONEq(t, `{"10000.000000":500,"20500.000000":1250}`, raw)
	require.NoError(t, sqlDB.QueryRow("SELECT `value` FROM system_settings WHERE `key` = 'daily_checkin_reward_rules'").Scan(&raw))
	require.JSONEq(t, `[{"amount":0.01,"probability":1}]`, raw)
	require.NoError(t, sqlDB.QueryRow("SELECT `value` FROM system_settings WHERE `key` = 'leaderboard_reward_rules'").Scan(&raw))
	require.JSONEq(t, `[{"rankFrom":1,"rankTo":2,"amount":20}]`, raw)
	require.NoError(t, sqlDB.QueryRow("SELECT rules_snapshot FROM leaderboard_settlements WHERE id = 9001").Scan(&raw))
	require.JSONEq(t, `[{"rankFrom":1,"rankTo":1,"amount":30},{"rankFrom":2,"rankTo":2,"amount":1234.567}]`, raw)
	requirePointsMigrationValues(t, sqlDB, "SELECT `value` FROM system_settings WHERE `key` = 'points_per_yuan'", "1000")

	var systemMessage, userMessage, preview, withdrawalMessage, withdrawalPreview string
	require.NoError(t, sqlDB.QueryRow("SELECT content FROM aftersale_ticket_messages WHERE id = 9001").Scan(&systemMessage))
	require.NoError(t, sqlDB.QueryRow("SELECT content FROM aftersale_ticket_messages WHERE id = 9002").Scan(&userMessage))
	require.NoError(t, sqlDB.QueryRow("SELECT last_message_preview FROM aftersale_tickets WHERE id = 9001").Scan(&preview))
	require.NoError(t, sqlDB.QueryRow("SELECT content FROM aftersale_ticket_messages WHERE id = 9003").Scan(&withdrawalMessage))
	require.NoError(t, sqlDB.QueryRow("SELECT last_message_preview FROM aftersale_tickets WHERE id = 9002").Scan(&withdrawalPreview))
	require.Equal(t, "平台已退款 6800 积分并关闭工单。", systemMessage)
	require.Equal(t, legacyRefundMessage, userMessage)
	require.Equal(t, systemMessage, preview)
	require.Equal(t, "提现积分：12500 积分\n提现方式：支付宝\n备注：请处理", withdrawalMessage)
	require.Equal(t, "提现积分：12500 积分 提现方式：支付宝 备注：请处理", withdrawalPreview)

	var legacyColumns int
	require.NoError(t, sqlDB.QueryRow(`SELECT COUNT(*)
FROM information_schema.columns
WHERE table_schema = DATABASE() AND column_name = 'amount_unit_version'`).Scan(&legacyColumns))
	require.Zero(t, legacyColumns)

	// A second UpTo is a no-op because version 68 and the data committed together.
	require.NoError(t, goose.UpTo(sqlDB, billingMigrationsDir(t), 68))
	requirePointsMigrationValues(t, sqlDB, `SELECT consumer_balance FROM wallets WHERE user_id = 9001`, "1234.567000")

	// Down fails before Goose can mark this irreversible migration unapplied.
	err = goose.DownTo(sqlDB, billingMigrationsDir(t), 67)
	require.ErrorContains(t, err, "irreversible")
	version, err = goose.GetDBVersion(sqlDB)
	require.NoError(t, err)
	require.EqualValues(t, 68, version)
	requirePointsMigrationValues(t, sqlDB, `SELECT consumer_balance FROM wallets WHERE user_id = 9001`, "1234.567000")

	// The persistent marker also blocks a second application if version
	// metadata is changed manually.
	require.NoError(t, db.Exec("DELETE FROM goose_db_version WHERE version_id = 68").Error)
	version, err = goose.GetDBVersion(sqlDB)
	require.NoError(t, err)
	require.EqualValues(t, 67, version)
	require.ErrorContains(t, platform.VerifyPointsUnitMigration(sqlDB), "current schema version 67")
	err = goose.UpTo(sqlDB, billingMigrationsDir(t), 68)
	require.ErrorContains(t, err, "chk_points_unit_guard")
	version, versionErr := goose.GetDBVersion(sqlDB)
	require.NoError(t, versionErr)
	require.EqualValues(t, 67, version)
	requirePointsMigrationValues(t, sqlDB, `SELECT consumer_balance FROM wallets WHERE user_id = 9001`, "1234.567000")
}

func TestPointsMigrationFailureRollsBackAllEarlierUpdatesMySQL(t *testing.T) {
	db := pointsMigrationMySQLTestServer.Database(t, copyBillingMigrationsThrough(t, 67))
	sqlDB, err := db.DB()
	require.NoError(t, err)
	goose.SetTableName("goose_db_version")
	require.NoError(t, goose.SetDialect("mysql"))
	version, err := goose.GetDBVersion(sqlDB)
	require.NoError(t, err)
	require.EqualValues(t, 67, version)
	require.NoError(t, db.Exec("DELETE FROM system_settings").Error)

	ctx := context.Background()
	conn, err := sqlDB.Conn(ctx)
	require.NoError(t, err)
	require.NoError(t, execPointsMigrationSQL(ctx, conn,
		"SET FOREIGN_KEY_CHECKS = 0",
		`INSERT INTO wallets(user_id, consumer_balance) VALUES (9201, 1)`,
		`INSERT INTO project_products(id, project_id, type, code_price) VALUES (9201, 9201, 'microsoft', 0.01)`,
		"SET FOREIGN_KEY_CHECKS = 1",
	))
	require.NoError(t, conn.Close())
	require.NoError(t, db.Exec(`ALTER TABLE system_settings
ADD CONSTRAINT chk_points_migration_forced_failure CHECK (`+"`key`"+` <> 'points_per_yuan')`).Error)

	err = goose.UpTo(sqlDB, billingMigrationsDir(t), 68)
	require.ErrorContains(t, err, "chk_points_migration_forced_failure")
	requirePointsMigrationValues(t, sqlDB, `SELECT consumer_balance FROM wallets WHERE user_id = 9201`, "1.000000")
	requirePointsMigrationValues(t, sqlDB, `SELECT code_price FROM project_products WHERE id = 9201`, "0.010000")
	version, versionErr := goose.GetDBVersion(sqlDB)
	require.NoError(t, versionErr)
	require.EqualValues(t, 67, version)
	var pointsSettingCount int
	require.NoError(t, sqlDB.QueryRow("SELECT COUNT(*) FROM system_settings WHERE `key` = 'points_per_yuan'").Scan(&pointsSettingCount))
	require.Zero(t, pointsSettingCount)
	var markerCount int
	require.NoError(t, sqlDB.QueryRow("SELECT COUNT(*) FROM system_settings WHERE `key` = 'points_unit_migration_v1'").Scan(&markerCount))
	require.Zero(t, markerCount)
	require.Error(t, platform.VerifyPointsUnitMigration(sqlDB))

	require.NoError(t, db.Exec(`ALTER TABLE system_settings
DROP CONSTRAINT chk_points_migration_forced_failure`).Error)
	require.NoError(t, goose.UpTo(sqlDB, billingMigrationsDir(t), 68))
	requirePointsMigrationValues(t, sqlDB, `SELECT consumer_balance FROM wallets WHERE user_id = 9201`, "1000.000000")
	requirePointsMigrationValues(t, sqlDB, `SELECT code_price FROM project_products WHERE id = 9201`, "10.000000")
	require.NoError(t, sqlDB.QueryRow("SELECT COUNT(*) FROM system_settings WHERE `key` = 'points_unit_migration_v1'").Scan(&markerCount))
	require.Equal(t, 1, markerCount)
	require.NoError(t, platform.VerifyPointsUnitMigration(sqlDB))
}

func copyBillingMigrationsThrough(t *testing.T, maximum int) string {
	t.Helper()
	return testmysql.MigrationsThrough(t, billingMigrationsDir(t), maximum)
}

func execPointsMigrationSQL(ctx context.Context, conn *sql.Conn, statements ...string) error {
	for _, statement := range statements {
		if _, err := conn.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}

func requirePointsMigrationValues(t *testing.T, db *sql.DB, query string, want ...string) {
	t.Helper()
	got := make([]string, len(want))
	dest := make([]any, len(got))
	for i := range got {
		dest[i] = &got[i]
	}
	require.NoError(t, db.QueryRow(query).Scan(dest...))
	require.Equal(t, want, got)
}
