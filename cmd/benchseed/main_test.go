package main

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/donnel666/remail/internal/platform/testmysql"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"
)

var benchSeedMySQL = testmysql.New("remail_benchseed_test")

func TestMain(m *testing.M) {
	code := m.Run()
	_ = benchSeedMySQL.Close(context.Background())
	os.Exit(code)
}

func TestSmallBenchmarkProfiles(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	migrations := filepath.Clean(filepath.Join(filepath.Dir(file), "../..", "migrations"))
	gormDB := benchSeedMySQL.Database(t, migrations)
	db, err := gormDB.DB()
	require.NoError(t, err)
	ctx := context.Background()

	require.NoError(t, seedFoundation(ctx, db))
	require.NoError(t, seedResources(ctx, db, 3, 2))
	require.NoError(t, seedAliases(ctx, db, 6, 3, 2))
	require.NoError(t, seedOrders(ctx, db, 3, 3, 2))
	require.NoError(t, seedMessages(ctx, db, 3, 3, 3, 2))

	require.NoError(t, seedFoundation(ctx, db))
	require.NoError(t, seedResources(ctx, db, 3, 2))
	require.NoError(t, seedAliases(ctx, db, 6, 3, 2))
	require.NoError(t, seedOrders(ctx, db, 3, 3, 2))
	require.NoError(t, seedMessages(ctx, db, 3, 3, 3, 2))

	for table, expected := range map[string]int64{
		"email_resources":       3,
		"explicit_aliases":      6,
		"orders":                3,
		"microsoft_allocations": 3,
		"order_events":          9,
		"order_tokens":          3,
		"wallet_transactions":   4,
		"mailmatch_messages":    3,
	} {
		var count int64
		require.NoError(t, gormDB.Table(table).Count(&count).Error)
		require.Equal(t, expected, count, table)
	}
}

func TestMigrationsReset(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	migrations := filepath.Clean(filepath.Join(filepath.Dir(file), "../..", "migrations"))
	gormDB := benchSeedMySQL.Database(t, migrations)
	db, err := gormDB.DB()
	require.NoError(t, err)
	require.NoError(t, goose.SetDialect("mysql"))
	require.NoError(t, goose.Reset(db, migrations))

	var usersTableCount int
	require.NoError(t, db.QueryRow(`
SELECT COUNT(*)
FROM information_schema.tables
WHERE table_schema = DATABASE()
  AND table_name = 'users'`).Scan(&usersTableCount))
	require.Zero(t, usersTableCount)
}

func TestMoneyPrecisionMigration(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	migrations := filepath.Clean(filepath.Join(filepath.Dir(file), "../..", "migrations"))
	gormDB := benchSeedMySQL.Database(t, migrations)

	ledgerColumns := map[string][]string{
		"card_keys":           {"amount"},
		"orders":              {"pay_amount", "refund_amount"},
		"recharges":           {"recharge_quota"},
		"referral_rewards":    {"source_amount", "reward_amount"},
		"wallet_transactions": {"amount", "balance_before", "balance_after"},
		"wallets":             {"consumer_balance", "supplier_available", "supplier_frozen", "total_spend"},
	}
	for table, columns := range ledgerColumns {
		for _, column := range columns {
			var precision int
			var scale int
			require.NoError(t, gormDB.Raw(`
SELECT numeric_precision, numeric_scale
FROM information_schema.columns
WHERE table_schema = DATABASE() AND table_name = ? AND column_name = ?`, table, column).
				Row().Scan(&precision, &scale))
			require.Equal(t, 18, precision, table+"."+column)
			require.Equal(t, 6, scale, table+"."+column)
		}
	}

	var paymentPrecision int
	var paymentScale int
	require.NoError(t, gormDB.Raw(`
SELECT numeric_precision, numeric_scale
FROM information_schema.columns
WHERE table_schema = DATABASE()
  AND table_name = 'recharges'
  AND column_name = 'payment_amount'`).
		Row().Scan(&paymentPrecision, &paymentScale))
	require.Equal(t, 18, paymentPrecision)
	require.Equal(t, 2, paymentScale)
}
