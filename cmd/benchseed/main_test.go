package main

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/donnel666/remail/internal/platform/testmysql"
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
