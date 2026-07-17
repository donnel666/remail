package infra

import (
	"context"
	"errors"
	"time"

	"github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
)

// Microsoft alias write transactions insert into microsoft_alias_attempts,
// whose foreign key to microsoft_resources is shared with the validation and
// token-refresh subsystems. Concurrent background workers therefore acquire
// shared/exclusive locks on the same microsoft_resources rows in different
// orders and form transient InnoDB lock cycles (Error 1213). MySQL rolls back
// the loser and asks it to "try restarting transaction"; a small bounded retry
// absorbs that instead of surfacing it as a task failure. This mirrors the
// deadlock retry already used by the alloc, trade and core infra repositories.
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
		return db.WithContext(ctx).Transaction(fn)
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
