package infra

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
)

// Keep a bounded retry for database-level deadlocks and lock timeouts that can
// still originate outside the alias transaction.
const aliasTransactionAttempts = 4

func isAliasDeadlockError(err error) bool {
	var mysqlErr *mysql.MySQLError
	return errors.As(err, &mysqlErr) && (mysqlErr.Number == 1213 || mysqlErr.Number == 1205)
}

// withAliasDeadlockRetry runs fn inside a top-level transaction, retrying the
// whole transaction on a deadlock. fn must reset any state it accumulates into
// captured variables at its start, because it can run more than once.
func withAliasDeadlockRetry(ctx context.Context, db *gorm.DB, fn func(*gorm.DB) error) error {
	return retryOnDeadlock(ctx, aliasTransactionAttempts, func() error {
		// READ COMMITTED avoids retaining empty-range next-key locks while each
		// resource independently reserves rows in the shared attempt indexes.
		return db.WithContext(ctx).Transaction(fn, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	})
}

func retryOnDeadlock(ctx context.Context, attempts int, run func() error) error {
	var err error
	for attempt := 0; attempt < attempts; attempt++ {
		err = run()
		if err == nil || !isAliasDeadlockError(err) {
			return err
		}
		if attempt+1 == attempts {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(aliasDeadlockBackoff(attempt)):
		}
	}
	return err
}

func aliasDeadlockBackoff(attempt int) time.Duration {
	if attempt < 0 {
		attempt = 0
	}
	if attempt > aliasTransactionAttempts-1 {
		attempt = aliasTransactionAttempts - 1
	}
	return time.Duration(attempt+1) * 20 * time.Millisecond
}
