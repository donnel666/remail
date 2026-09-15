package kitesim

import (
	"context"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/donnel666/remail/internal/platform"
	"github.com/donnel666/remail/internal/platform/testmysql"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

var kitesimMigrationMySQL = testmysql.New("remail_kitesim_migration")

func TestICloudPhoneAllocationExclusivityMySQL(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	source := filepath.Clean(filepath.Join(filepath.Dir(file), "../..", "migrations"))
	server := testmysql.New("remail_phone_exclusivity")
	t.Cleanup(func() { require.NoError(t, server.Close(context.Background())) })
	db := server.Database(t, testmysql.MigrationsThrough(t, source, 142))
	require.NoError(t, db.Exec("INSERT INTO kitesim_accounts (account, password) VALUES ('hold@example.com', 'test')").Error)
	require.NoError(t, db.Exec(`INSERT INTO kitesim_phones (account_id, provider_order_id, order_no, phone_number, status, order_status, raw_payload)
		VALUES (1, 'hold-1', 'hold-1', '14165550001', 1, 1, JSON_OBJECT()),
		       (1, 'hold-2', 'hold-2', '14165550002', 1, 1, JSON_OBJECT())`).Error)
	require.NoError(t, db.Exec("INSERT INTO kitesim_phone_bindings (phone_id, consumer_type, consumer_key, source) VALUES (1, 'icloud', 'hold@example.com', 'matched')").Error)
	var binding phoneBindingModel
	require.NoError(t, db.Where("consumer_key = ?", "hold@example.com").Take(&binding).Error)
	s := NewService(db, nil)

	// Both allocators establish their snapshot before either takes the phone lock.
	ready, release := make(chan struct{}, 2), make(chan struct{})
	require.NoError(t, db.Callback().Query().After("gorm:query").Register("phone_hold_race", func(tx *gorm.DB) {
		_, locked := tx.Statement.Clauses["FOR"]
		if tx.Statement.Table == "kitesim_phones" && len(tx.Statement.Selects) == 3 && !locked {
			ready <- struct{}{}
			<-release
		}
	}))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	results := make(chan error, 2)
	for _, email := range []string{"race-a@example.com", "race-b@example.com"} {
		go func() { _, err := s.BindICloudSMSPhone(ctx, email, "14165550002"); results <- err }()
	}
	arrived := 0
	for arrived < 2 {
		select {
		case <-ready:
			arrived++
		case <-ctx.Done():
			close(release)
			t.Fatal("allocators did not reach the phone lock")
		}
	}
	close(release)
	first, second := <-results, <-results
	if first != nil {
		first, second = second, first
	}
	require.NoError(t, first)
	require.ErrorIs(t, second, ErrSMSPhoneExclusive)
	require.NoError(t, db.Callback().Query().Remove("phone_hold_race"))
	require.False(t, db.Migrator().HasColumn("kitesim_phone_bindings", "phone_confirmed_at"))
	var count int64
	require.NoError(t, db.Table("kitesim_phone_bindings").Count(&count).Error)
	require.EqualValues(t, 2, count)
}

func TestKitesimSMSLinkMigrationMySQL(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	source := filepath.Clean(filepath.Join(filepath.Dir(file), "../..", "migrations"))
	db := kitesimMigrationMySQL.Database(t, testmysql.MigrationsThrough(t, source, 107))
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, platform.RunMigrations(sqlDB, testmysql.MigrationsThrough(t, source, 140)))
	require.NoError(t, db.Exec("INSERT INTO kitesim_accounts (account, password) VALUES ('links@example.com', 'password')").Error)
	require.NoError(t, db.Exec(`INSERT INTO kitesim_phones (account_id, provider_order_id, order_no, phone_number, status, order_status, raw_payload)
		VALUES (1, 'phone-1', 'order-1', '14165550001', 1, 1, JSON_OBJECT()),
		       (1, 'phone-2', 'order-2', '14165550002', 1, 1, JSON_OBJECT())`).Error)
	through141 := testmysql.MigrationsThrough(t, source, 141)
	require.NoError(t, platform.RunMigrations(sqlDB, through141))
	var count int64
	require.NoError(t, db.Table("kitesim_phones").Where("sms_link_token_hash IS NULL").Count(&count).Error)
	require.Equal(t, int64(2), count)
	hash := strings.Repeat("a", 64)
	require.NoError(t, db.Table("kitesim_phones").Where("id = 1").Update("sms_link_token_hash", hash).Error)
	require.ErrorIs(t, db.Table("kitesim_phones").Where("id = 2").Update("sms_link_token_hash", hash).Error, gorm.ErrDuplicatedKey)
	require.NoError(t, goose.DownTo(sqlDB, through141, 140))
	require.False(t, db.Migrator().HasColumn("kitesim_phones", "sms_link_token_hash"))
	require.NoError(t, platform.RunMigrations(sqlDB, through141))
	require.NoError(t, db.Table("kitesim_phones").Count(&count).Error)
	require.Equal(t, int64(2), count)
}

func TestKitesimRechargeGlobalLockMigrationMySQL(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	source := filepath.Clean(filepath.Join(filepath.Dir(file), "../..", "migrations"))
	through107 := testmysql.MigrationsThrough(t, source, 107)
	through108 := testmysql.MigrationsThrough(t, source, 108)
	db := kitesimMigrationMySQL.Database(t, through107)
	sqlDB, err := db.DB()
	require.NoError(t, err)

	require.NoError(t, platform.RunMigrations(sqlDB, through108))
	require.True(t, db.Migrator().HasIndex("kitesim_operations", "uk_kitesim_operations_active_recharge_scope"))

	goose.SetTableName("goose_db_version")
	require.NoError(t, goose.SetDialect("mysql"))
	require.NoError(t, goose.DownTo(sqlDB, through108, 107))
	require.False(t, db.Migrator().HasTable("kitesim_operations"))
	require.NoError(t, platform.RunMigrations(sqlDB, through108))
	require.True(t, db.Migrator().HasColumn("kitesim_operations", "active_recharge_scope"))
	require.True(t, db.Migrator().HasIndex("kitesim_operations", "uk_kitesim_operations_active_recharge_scope"))

	require.NoError(t, db.Exec(`INSERT INTO kitesim_accounts(account, password) VALUES
		('a@example.com', 'password-a'), ('b@example.com', 'password-b')`).Error)
	var accountIDs []uint
	require.NoError(t, db.Table("kitesim_accounts").Order("id").Pluck("id", &accountIDs).Error)
	require.Len(t, accountIDs, 2)

	insertRecharge := func(accountID uint, status, key string) error {
		return db.Exec(`INSERT INTO kitesim_operations(
			kind, account_id, requested_count, amount, status,
			operator_user_id, idempotency_key, request_fingerprint, queued_at
		) VALUES ('recharge', ?, 1, 10, ?, 1, ?, REPEAT('a', 64), CURRENT_TIMESTAMP(3))`,
			accountID, status, key).Error
	}
	require.NoError(t, insertRecharge(accountIDs[0], "uncertain", "recharge-a"))
	require.ErrorIs(t, insertRecharge(accountIDs[1], "queued", "recharge-b"), gorm.ErrDuplicatedKey)
	require.NoError(t, db.Table("kitesim_operations").Where("account_id = ?", accountIDs[0]).Update("status", "failed").Error)
	require.NoError(t, insertRecharge(accountIDs[1], "queued", "recharge-b"))
}
