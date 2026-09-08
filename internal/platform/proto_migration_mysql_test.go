package platform_test

import (
	"context"
	"database/sql"
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

func TestProtoMigrationsResumeMySQL(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	target := testmysql.MigrationsThrough(t, filepath.Join(filepath.Dir(file), "../../migrations"), 137)
	baseline := testmysql.MigrationsThrough(t, target, 135)
	server := testmysql.New("remail_proto_migration_test")
	t.Cleanup(func() { require.NoError(t, server.Close(context.Background())) })

	newDB := func(t *testing.T) (*gorm.DB, *sql.DB) {
		t.Helper()
		db := server.Database(t, baseline)
		sqlDB, err := db.DB()
		require.NoError(t, err)
		return db, sqlDB
	}
	assertVersion := func(t *testing.T, db *sql.DB, want int64) {
		t.Helper()
		version, err := goose.GetDBVersion(db)
		require.NoError(t, err)
		require.Equal(t, want, version)
	}
	assertRootCheck := func(t *testing.T, db *gorm.DB, enforced string, includesProto bool) {
		t.Helper()
		var check struct{ Enforced, CheckClause string }
		require.NoError(t, db.Raw(`SELECT tc.enforced, cc.check_clause
FROM information_schema.table_constraints AS tc
JOIN information_schema.check_constraints AS cc
  ON cc.constraint_schema = tc.constraint_schema AND cc.constraint_name = tc.constraint_name
WHERE tc.constraint_schema = DATABASE() AND tc.table_name = 'email_resources'
  AND tc.constraint_name = 'chk_email_resources_type'`).Scan(&check).Error)
		require.Equal(t, enforced, check.Enforced)
		// MySQL may expose escaped literal quotes in CHECK_CLAUSE.
		clause := strings.ToLower(strings.ReplaceAll(check.CheckClause, "\\", ""))
		if includesProto {
			require.Contains(t, clause, "'proto'")
		} else {
			require.NotContains(t, clause, "'proto'")
		}
	}
	assertUpgraded := func(t *testing.T, db *gorm.DB, sqlDB *sql.DB) {
		t.Helper()
		assertVersion(t, sqlDB, 137)
		assertRootCheck(t, db, "NO", true)
		for _, column := range []string{"email_domain", "long_lived", "quality_score", "alloc_bucket"} {
			require.True(t, db.Migrator().HasColumn("proto_resources", column), column)
		}
		require.True(t, db.Migrator().HasColumn("proto_command_receipts", "reservation_token"))
		require.True(t, db.Migrator().HasColumn("proto_resource_imports", "attempts"))
		require.True(t, db.Migrator().HasTable("proto_project_history_scan_states"))
		require.True(t, db.Migrator().HasIndex("proto_allocations", "idx_proto_allocation_history"))
		require.False(t, db.Migrator().HasConstraint("proto_resources", "chk_proto_resource_for_sale"))
		for _, column := range []string{"proto_alloc_id", "proto_resource_id", "resource_id"} {
			require.False(t, db.Migrator().HasColumn("orders", column), column)
		}
	}
	legacyDB := func(t *testing.T) (*gorm.DB, *sql.DB) {
		t.Helper()
		db, sqlDB := newDB(t)
		require.NoError(t, goose.UpTo(sqlDB, target, 136))
		for _, statement := range []string{
			`INSERT INTO users(id, email, password_hash, nickname, status, role)
			 VALUES (1, 'proto-migration@example.com', 'hash', 'migration', 'active', 'super_admin')`,
			`INSERT INTO email_resources(id, type, owner_user_id) VALUES (4097, 'proto', 1)`,
			`INSERT INTO proto_resources(id, owner_user_id, email_address, password, status, for_sale)
			 VALUES (4097, 1, 'saved@Example.com', 'preserve-secret', 'normal', TRUE)`,
			`INSERT INTO proto_resource_imports(id, owner_user_id, operator_user_id, source_object_key,
			 idempotency_key, request_fingerprint, status, accepted_count, imported_count)
			 VALUES (1, 1, 1, 'private/proto/original.txt', 'import-one', REPEAT('i', 64), 'imported', 1, 1)`,
			`INSERT INTO proto_resource_import_items(id, import_id, line_number, resource_id, outcome)
			 VALUES (1, 1, 1, 4097, 'imported')`,
			`INSERT INTO proto_command_receipts(id, operator_user_id, resource_id, command, idempotency_key,
			 request_fingerprint, status) VALUES (1, 1, 4097, 'validate', 'request-one', REPEAT('r', 64), 'accepted')`,
		} {
			require.NoError(t, db.Exec(statement).Error)
		}
		return db, sqlDB
	}
	assertFacts := func(t *testing.T, db *gorm.DB, receipts, items int64) {
		t.Helper()
		var resource struct {
			EmailAddress string
			Password     string
			EmailDomain  string
			AllocBucket  uint16
		}
		require.NoError(t, db.Table("proto_resources").Where("id = 4097").Take(&resource).Error)
		require.Equal(t, "saved@Example.com", resource.EmailAddress)
		require.Equal(t, "preserve-secret", resource.Password)
		require.Equal(t, "example.com", resource.EmailDomain)
		require.Equal(t, uint16(1), resource.AllocBucket)
		for table, want := range map[string]int64{
			"proto_resources": 1, "proto_resource_imports": 1,
			"proto_resource_import_items": items, "proto_command_receipts": receipts,
		} {
			var count int64
			require.NoError(t, db.Table(table).Count(&count).Error)
			require.Equal(t, want, count, table)
		}
		var status string
		require.NoError(t, db.Table("proto_command_receipts").Select("status").Where("id = 1").Scan(&status).Error)
		require.Equal(t, "failed", status, "legacy accepted did not prove that its business command committed")
	}

	t.Run("upgrade-from-135", func(t *testing.T) {
		db, sqlDB := newDB(t)
		assertVersion(t, sqlDB, 135)
		assertRootCheck(t, db, "NO", false)
		require.NoError(t, db.Exec(`INSERT INTO users(id, email, password_hash, nickname, status, role)
VALUES (1, 'existing-roots@example.com', 'hash', 'existing', 'active', 'super_admin')`).Error)
		// The pre-existing NOT ENFORCED policy permits an unvalidated root. Keep
		// this tiny sentinel to catch accidental whole-table CHECK validation.
		require.NoError(t, db.Exec(`INSERT INTO email_resources(id, type, owner_user_id, version, created_at, updated_at) VALUES
(101, 'microsoft', 1, 2, '2020-01-01', '2021-01-01'),
(102, 'domain', 1, 3, '2020-01-02', '2021-01-02'),
(103, 'gmail', 1, 5, '2020-01-03', '2021-01-03'),
(104, 'icloud', 1, 8, '2020-01-04', '2021-01-04'),
(105, 'legacy_unvalidated', 1, 13, '2020-01-05', '2021-01-05')`).Error)
		type rootSnapshot struct {
			ID                   uint
			Type                 string
			OwnerUserID, Version uint64
			CreatedAt, UpdatedAt time.Time
		}
		var before []rootSnapshot
		require.NoError(t, db.Table("email_resources").Order("id").Find(&before).Error)
		require.Len(t, before, 5)
		assertRootsUnchanged := func() {
			t.Helper()
			var after []rootSnapshot
			require.NoError(t, db.Table("email_resources").Order("id").Find(&after).Error)
			require.Equal(t, before, after)
		}
		require.NoError(t, goose.UpTo(sqlDB, target, 136))
		assertVersion(t, sqlDB, 136)
		assertRootCheck(t, db, "NO", true)
		assertRootsUnchanged()
		require.NoError(t, platform.RunMigrations(sqlDB, target))
		assertUpgraded(t, db, sqlDB)
		assertRootsUnchanged()
		require.NoError(t, goose.DownTo(sqlDB, target, 135))
		assertVersion(t, sqlDB, 135)
		assertRootCheck(t, db, "NO", false)
		assertRootsUnchanged()
	})

	t.Run("resume-136-after-enforced-ddl-before-version-record", func(t *testing.T) {
		db, sqlDB := legacyDB(t)
		// Reproduce the previous 136 definition using only valid rows. This DB
		// is separate from the unvalidated-root sentinel fixture above.
		require.NoError(t, db.Exec("ALTER TABLE email_resources ALTER CHECK chk_email_resources_type ENFORCED").Error)
		assertRootCheck(t, db, "YES", true)
		require.NoError(t, db.Exec("DELETE FROM goose_db_version WHERE version_id = 136").Error)
		assertVersion(t, sqlDB, 135)
		require.NoError(t, platform.RunMigrations(sqlDB, target))
		assertUpgraded(t, db, sqlDB)
		assertFacts(t, db, 1, 1)
	})

	for _, tc := range []struct {
		name, insert, repair, wantError string
		receipts, items                 int64
	}{
		{
			name: "duplicate-receipt-key-preflight",
			insert: `INSERT INTO proto_command_receipts(id, operator_user_id, resource_id, command,
			 idempotency_key, request_fingerprint, status)
			 VALUES (2, 1, 4097, 'disable', 'request-one', REPEAT('s', 64), 'completed')`,
			repair:    "UPDATE proto_command_receipts SET idempotency_key = 'request-two' WHERE id = 2",
			wantError: "conflicting Proto command keys", receipts: 2, items: 1,
		},
		{
			name: "orphan-import-resource-preflight",
			insert: `INSERT INTO proto_resource_import_items(id, import_id, line_number, resource_id, outcome)
			 VALUES (2, 1, 2, 999999, 'imported')`,
			repair:    "UPDATE proto_resource_import_items SET resource_id = 4097 WHERE id = 2",
			wantError: "invalid Proto import references", receipts: 1, items: 2,
		},
		{
			name: "invalid-import-line-preflight",
			insert: `INSERT INTO proto_resource_import_items(id, import_id, line_number, resource_id, outcome)
			 VALUES (2, 1, 0, 4097, 'imported')`,
			repair:    "UPDATE proto_resource_import_items SET line_number = 2 WHERE id = 2",
			wantError: "invalid Proto import references", receipts: 1, items: 2,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, sqlDB := legacyDB(t)
			require.NoError(t, db.Exec(tc.insert).Error)
			require.ErrorContains(t, platform.RunMigrations(sqlDB, target), tc.wantError)
			assertVersion(t, sqlDB, 136)
			require.False(t, db.Migrator().HasColumn("proto_resources", "email_domain"))
			require.False(t, db.Migrator().HasColumn("proto_resources", "long_lived"))
			require.False(t, db.Migrator().HasColumn("proto_command_receipts", "reservation_token"))
			require.True(t, db.Migrator().HasConstraint("proto_resources", "chk_proto_resource_for_sale"))
			var status string
			require.NoError(t, db.Table("proto_command_receipts").Select("status").Where("id = 1").Scan(&status).Error)
			require.Equal(t, "accepted", status, "preflight must leave existing command facts unchanged")
			require.NoError(t, db.Exec(tc.repair).Error)
			require.NoError(t, platform.RunMigrations(sqlDB, target))
			assertUpgraded(t, db, sqlDB)
			assertFacts(t, db, tc.receipts, tc.items)
		})
	}

	t.Run("resume-137-partial-and-fully-committed-ddl", func(t *testing.T) {
		db, sqlDB := legacyDB(t)
		require.NoError(t, db.Exec(`ALTER TABLE proto_resources
		 DROP CHECK chk_proto_resource_for_sale,
		 ADD COLUMN email_domain VARCHAR(255) NOT NULL DEFAULT '',
		 ADD COLUMN quality_score INT NOT NULL DEFAULT 0`).Error)
		require.NoError(t, platform.RunMigrations(sqlDB, target))
		assertUpgraded(t, db, sqlDB)
		assertFacts(t, db, 1, 1)
		require.NoError(t, db.Exec("UPDATE proto_resources SET status = 'pending' WHERE id = 4097").Error)
		require.NoError(t, db.Exec("DELETE FROM goose_db_version WHERE version_id = 137").Error)
		assertVersion(t, sqlDB, 136)
		require.NoError(t, platform.RunMigrations(sqlDB, target))
		assertUpgraded(t, db, sqlDB)
		assertFacts(t, db, 1, 1)
		var forSale bool
		require.NoError(t, db.Table("proto_resources").Select("for_sale").Where("id = 4097 AND status = 'pending'").Scan(&forSale).Error)
		require.True(t, forSale, "resuming must preserve supply intent for pending resources")
	})
}
