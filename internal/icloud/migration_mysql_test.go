package icloud

import (
	"path/filepath"
	"runtime"
	"testing"

	"github.com/donnel666/remail/internal/platform"
	"github.com/donnel666/remail/internal/platform/testmysql"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

var iCloudOnboardingMigrationMySQL = testmysql.New("remail_icloud_onboarding_migration")

func TestICloudOnboardingMigrationMySQL(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	source := filepath.Clean(filepath.Join(filepath.Dir(file), "../..", "migrations"))
	through110 := testmysql.MigrationsThrough(t, source, 110)
	through114 := testmysql.MigrationsThrough(t, source, 114)
	through115 := testmysql.MigrationsThrough(t, source, 115)
	db := iCloudOnboardingMigrationMySQL.Database(t, through110)
	sqlDB, err := db.DB()
	require.NoError(t, err)

	if err := platform.RunMigrations(sqlDB, through114); err != nil {
		var engine, name, status string
		_ = db.Raw("SHOW ENGINE INNODB STATUS").Row().Scan(&engine, &name, &status)
		t.Fatalf("migrate through 114: %v\n%s", err, status)
	}
	for _, table := range []string{
		"kitesim_phone_bindings", "kitesim_phone_usage_events",
		"icloud_resource_credentials", "icloud_account_onboarding_imports", "icloud_account_onboarding_tasks",
	} {
		require.True(t, db.Migrator().HasTable(table), table)
	}
	for _, column := range []string{"account_role", "family_primary_resource_id", "region", "country_code", "icloud_opened", "bound_phone_number", "bound_phone_source", "kitesim_phone_id", "family_invite_url"} {
		require.True(t, db.Migrator().HasColumn("icloud_resources", column), column)
	}
	for _, column := range []string{"sms_cooldown_until", "sms_cooldown_stage", "sms_consecutive_failures", "sms_blacklisted_until", "sms_last_used_at"} {
		require.True(t, db.Migrator().HasColumn("kitesim_phones", column), column)
	}
	for _, column := range []string{"import_id", "secret_payload"} {
		var nullable string
		require.NoError(t, db.Raw(`SELECT IS_NULLABLE FROM information_schema.columns
			WHERE table_schema = DATABASE() AND table_name = 'icloud_account_onboarding_tasks' AND column_name = ?`, column).Scan(&nullable).Error)
		require.Equal(t, "YES", nullable, column)
	}
	for _, index := range []string{"uk_icloud_onboard_task_active_email", "uk_icloud_onboard_task_active_refresh"} {
		require.True(t, db.Migrator().HasIndex("icloud_account_onboarding_tasks", index), index)
	}
	for _, constraint := range []string{"fk_icloud_resources_family_primary", "fk_icloud_onboard_task_import", "fk_icloud_onboard_task_resource"} {
		require.True(t, db.Migrator().HasConstraint("icloud_account_onboarding_tasks", constraint) || db.Migrator().HasConstraint("icloud_resources", constraint), constraint)
	}

	require.NoError(t, db.Exec("SET FOREIGN_KEY_CHECKS = 0").Error)
	t.Cleanup(func() { _ = db.Exec("SET FOREIGN_KEY_CHECKS = 1").Error })
	insertRefresh := func(resourceID uint, email string) error {
		return db.Exec(`INSERT INTO icloud_account_onboarding_tasks(
			resource_id, task_kind, line_number, primary_email, account_role, region,
			icloud_opened, status, stage, dispatch_status, generation, max_attempts
		) VALUES (?, 'refresh', 0, ?, 'primary', 'US', 1, 'processing', 'accepted', 'pending', 1, 5)`, resourceID, email).Error
	}
	require.NoError(t, insertRefresh(990111, "refresh-one@example.com"))
	require.ErrorIs(t, insertRefresh(990111, "refresh-two@example.com"), gorm.ErrDuplicatedKey)
	require.NoError(t, db.Table("icloud_account_onboarding_tasks").Where("primary_email = ?", "refresh-one@example.com").Update("status", "completed").Error)
	require.NoError(t, insertRefresh(990111, "refresh-two@example.com"))
	require.NoError(t, db.Exec(`INSERT INTO icloud_account_onboarding_imports(
		id, owner_user_id, operator_user_id, status, accepted_count, resource_expire_at,
		idempotency_key, request_fingerprint
	) VALUES (990115, 990001, 990001, 'processing', 1, DATE_ADD(CURRENT_TIMESTAMP(3), INTERVAL 1 MONTH),
		'reservation-backfill', REPEAT('a', 64))`).Error)
	require.NoError(t, db.Exec(`INSERT INTO icloud_account_onboarding_tasks(
		import_id, task_kind, line_number, primary_email, account_role, region,
		icloud_opened, status, stage, dispatch_status, generation, max_attempts
	) VALUES (990115, 'onboarding', 1, 'waiting-reset@example.com', 'child', 'US',
		1, 'waiting', 'waiting_family_reset', 'waiting', 1, 5)`).Error)
	require.NoError(t, db.Exec("SET FOREIGN_KEY_CHECKS = 1").Error)
	require.NoError(t, platform.RunMigrations(sqlDB, through115))
	require.True(t, db.Migrator().HasColumn("icloud_account_onboarding_tasks", "icloud_activation_confirmed_at"))
	require.True(t, db.Migrator().HasTable("icloud_apple_id_reservations"))
	var reservation iCloudAppleIDReservationModel
	require.NoError(t, db.First(&reservation, "email_key = ?", "waiting-reset@example.com").Error)
	require.Equal(t, iCloudAppleIDReservationOnboarding, reservation.OwnerKind)
	require.Equal(t, uint(990115), reservation.OwnerID)

	goose.SetTableName("goose_db_version")
	require.NoError(t, goose.SetDialect("mysql"))
	require.NoError(t, goose.DownTo(sqlDB, through115, 110))
	require.False(t, db.Migrator().HasTable("icloud_account_onboarding_tasks"))
	require.False(t, db.Migrator().HasTable("icloud_apple_id_reservations"))
	require.False(t, db.Migrator().HasColumn("icloud_resources", "account_role"))
	require.False(t, db.Migrator().HasColumn("kitesim_phones", "sms_cooldown_until"))
	require.NoError(t, platform.RunMigrations(sqlDB, through115))
	require.True(t, db.Migrator().HasTable("icloud_account_onboarding_tasks"))
	require.True(t, db.Migrator().HasTable("icloud_apple_id_reservations"))
}
