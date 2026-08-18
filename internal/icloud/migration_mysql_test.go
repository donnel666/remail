package icloud

import (
	"path/filepath"
	"runtime"
	"testing"

	"github.com/donnel666/remail/internal/platform"
	"github.com/donnel666/remail/internal/platform/testmysql"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"
)

var iCloudOnboardingMigrationMySQL = testmysql.New("remail_icloud_onboarding_migration")

func TestICloudOnboardingMigrationMySQL(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	source := filepath.Clean(filepath.Join(filepath.Dir(file), "../..", "migrations"))
	through110 := testmysql.MigrationsThrough(t, source, 110)
	through114 := testmysql.MigrationsThrough(t, source, 114)
	through115 := testmysql.MigrationsThrough(t, source, 115)
	through116 := testmysql.MigrationsThrough(t, source, 116)
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
	require.NoError(t, db.Exec("SET FOREIGN_KEY_CHECKS = 0").Error)
	require.NoError(t, db.Exec(`INSERT INTO email_resources(id, type, owner_user_id)
		VALUES (990116, 'icloud', 990001)`).Error)
	require.NoError(t, db.Exec(`INSERT INTO icloud_resources(
		id, resource_type, primary_email, account_role, region, country_code, icloud_opened,
		bound_phone_number, bound_phone_source, family_invite_url, expire_at, status,
		credential_revision, validation_generation
	) VALUES (
		990116, 'icloud', 'migrate-onboarding@example.com', 'child', 'US', 'US', 0,
		'14155550001', 'manual', '', DATE_ADD(CURRENT_TIMESTAMP(3), INTERVAL 1 MONTH), 'pending', 1, 1
	)`).Error)
	require.NoError(t, db.Exec(`INSERT INTO icloud_account_onboarding_imports(
		id, owner_user_id, operator_user_id, accepted_count, resource_expire_at,
		request_id, idempotency_key, request_fingerprint
	) VALUES (
		990115, 990001, 990001, 1, DATE_ADD(CURRENT_TIMESTAMP(3), INTERVAL 1 MONTH),
		'migrate-request', 'migrate-key', REPEAT('a', 64)
	)`).Error)
	require.NoError(t, db.Exec(`INSERT INTO icloud_account_onboarding_tasks(
		import_id, resource_id, task_kind, line_number, primary_email, account_role, region, country_code,
		icloud_opened, bound_phone_number, bound_phone_source, secret_payload, session_payload,
		status, stage, dispatch_status, generation, expected_credential_revision, max_attempts
	) VALUES (
		990115, 990116, 'onboarding', 1, 'migrate-onboarding@example.com', 'child', 'US', 'US',
		0, '14155550001', 'manual', JSON_OBJECT('password', 'secret'), JSON_OBJECT('flow', 'waiting'),
		'waiting', 'waiting_icloud_activation', 'waiting', 7, 1, 5
	)`).Error)
	require.NoError(t, db.Exec("SET FOREIGN_KEY_CHECKS = 1").Error)
	require.NoError(t, platform.RunMigrations(sqlDB, through115))
	require.True(t, db.Migrator().HasTable("icloud_apple_id_reservations"))
	require.NoError(t, platform.RunMigrations(sqlDB, through116))
	for _, table := range []string{"icloud_account_onboarding_tasks", "icloud_account_onboarding_imports"} {
		require.False(t, db.Migrator().HasTable(table), table)
	}
	for _, column := range []string{"import_id", "resource_id", "task_kind", "onboarding_status", "generation", "secret_payload"} {
		require.True(t, db.Migrator().HasColumn("icloud_resources", column), column)
	}
	require.True(t, db.Migrator().HasIndex("icloud_resources", "idx_icloud_resources_workflow_dispatch"))
	var migrated struct {
		ImportID       uint   `gorm:"column:import_id"`
		ResourceID     uint   `gorm:"column:resource_id"`
		TaskKind       string `gorm:"column:task_kind"`
		Status         string `gorm:"column:onboarding_status"`
		Stage          string `gorm:"column:stage"`
		DispatchStatus string `gorm:"column:dispatch_status"`
		Generation     uint64 `gorm:"column:generation"`
		OperatorUserID uint   `gorm:"column:onboarding_operator_user_id"`
		IdempotencyKey string `gorm:"column:onboarding_idempotency_key"`
	}
	require.NoError(t, db.Table("icloud_resources").Where("id = ?", 990116).Take(&migrated).Error)
	require.Equal(t, uint(990115), migrated.ImportID)
	require.Equal(t, uint(990116), migrated.ResourceID)
	require.Equal(t, "onboarding", migrated.TaskKind)
	require.Equal(t, iCloudOnboardingWaiting, migrated.Status)
	require.Equal(t, "waiting_icloud_activation", migrated.Stage)
	require.Equal(t, "waiting", migrated.DispatchStatus)
	require.Equal(t, uint64(7), migrated.Generation)
	require.Equal(t, uint(990001), migrated.OperatorUserID)
	require.Equal(t, "migrate-key", migrated.IdempotencyKey)

	goose.SetTableName("goose_db_version")
	require.NoError(t, goose.SetDialect("mysql"))
	require.Error(t, goose.DownTo(sqlDB, through116, 115))
	require.False(t, db.Migrator().HasTable("icloud_account_onboarding_tasks"))
	require.True(t, db.Migrator().HasTable("icloud_apple_id_reservations"))
	require.False(t, db.Migrator().HasTable("icloud_account_onboarding_imports"))
	require.True(t, db.Migrator().HasColumn("icloud_resources", "onboarding_status"))
}
