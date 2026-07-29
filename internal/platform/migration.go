package platform

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"log/slog"

	"github.com/pressly/goose/v3"
)

const migrationLockName = "remail:goose:migrations"

const requiredPointsMigrationVersion int64 = 68

// RunMigrations executes all pending goose migrations from the specified directory.
func RunMigrations(db *sql.DB, migrationsDir string) (runErr error) {
	goose.SetTableName("goose_db_version")

	if err := goose.SetDialect("mysql"); err != nil {
		return fmt.Errorf("goose set dialect: %w", err)
	}

	// ponytail: one server-wide lock is the smallest safe cutover lock; split it
	// per database only if independent schemas must migrate concurrently.
	lockConn, err := db.Conn(context.Background())
	if err != nil {
		return fmt.Errorf("acquire migration connection: %w", err)
	}
	defer lockConn.Close()

	var acquired sql.NullInt64
	if err := lockConn.QueryRowContext(context.Background(), "SELECT GET_LOCK(?, ?)", migrationLockName, 300).Scan(&acquired); err != nil {
		return fmt.Errorf("acquire migration lock: %w", err)
	}
	if !acquired.Valid || acquired.Int64 != 1 {
		return fmt.Errorf("acquire migration lock: timed out")
	}
	defer func() {
		var released sql.NullInt64
		err := lockConn.QueryRowContext(context.Background(), "SELECT RELEASE_LOCK(?)", migrationLockName).Scan(&released)
		if err == nil && released.Valid && released.Int64 == 1 {
			return
		}
		// Do not return a pooled connection that may still own the named lock.
		_ = lockConn.Raw(func(any) error { return driver.ErrBadConn })
		if err == nil {
			err = fmt.Errorf("migration lock was not owned")
		}
		if runErr == nil {
			runErr = fmt.Errorf("release migration lock: %w", err)
		} else {
			slog.Error("release database migration lock failed", "error", err)
		}
	}()

	if err := goose.Up(db, migrationsDir); err != nil {
		return fmt.Errorf("goose up: %w", err)
	}

	slog.Info("database migrations completed")
	return nil
}

// VerifyPointsUnitMigration prevents the points application from serving a
// database whose irreversible unit conversion did not commit.
func VerifyPointsUnitMigration(db *sql.DB) error {
	var version int64
	var applied bool
	if err := db.QueryRow(`
SELECT version_id, is_applied
FROM goose_db_version
ORDER BY id DESC
LIMIT 1`).Scan(&version, &applied); err != nil {
		return fmt.Errorf("read database migration version: %w", err)
	}
	if !applied || version < requiredPointsMigrationVersion {
		return fmt.Errorf("points unit migration is not applied: current schema version %d", version)
	}

	var marker string
	if err := db.QueryRow("SELECT `value` FROM system_settings WHERE `key` = 'points_unit_migration_v1'").Scan(&marker); err != nil {
		return fmt.Errorf("read points unit migration marker: %w", err)
	}
	if marker != "completed" {
		return fmt.Errorf("points unit migration marker is invalid")
	}
	return nil
}
