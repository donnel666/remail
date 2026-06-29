package platform

import (
	"database/sql"
	"fmt"
	"log/slog"

	"github.com/pressly/goose/v3"
)

// RunMigrations executes all pending goose migrations from the specified directory.
func RunMigrations(db *sql.DB, migrationsDir string) error {
	goose.SetTableName("goose_db_version")

	if err := goose.SetDialect("mysql"); err != nil {
		return fmt.Errorf("goose set dialect: %w", err)
	}

	if err := goose.Up(db, migrationsDir); err != nil {
		return fmt.Errorf("goose up: %w", err)
	}

	slog.Info("database migrations completed")
	return nil
}
