package infra

import (
	"context"
	"errors"
	"time"

	"github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
)

// Import and bulk commands use short transactions and database uniqueness for
// conflict resolution. A small retry budget is enough for transient MySQL
// deadlocks without hiding a persistent lock-order problem behind a long tail.
const retryableMySQLTransactionAttempts = 3

func isDuplicateKeyError(err error) bool {
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return true
	}
	var mysqlErr *mysql.MySQLError
	return errors.As(err, &mysqlErr) && mysqlErr.Number == 1062
}

func isForeignKeyError(err error) bool {
	var mysqlErr *mysql.MySQLError
	return errors.As(err, &mysqlErr) && mysqlErr.Number == 1452
}

func isRetryableMySQLConflict(err error) bool {
	var mysqlErr *mysql.MySQLError
	return errors.As(err, &mysqlErr) && (mysqlErr.Number == 1213 || mysqlErr.Number == 1205)
}

func withRetryableMySQLTransaction(ctx context.Context, db *gorm.DB, fn func(*gorm.DB) error) error {
	var err error
	for attempt := 0; attempt < retryableMySQLTransactionAttempts; attempt++ {
		err = db.WithContext(ctx).Transaction(fn)
		if err == nil || !isRetryableMySQLConflict(err) {
			return err
		}
		if attempt+1 == retryableMySQLTransactionAttempts {
			return err
		}
		timer := time.NewTimer(retryableMySQLBackoff(attempt))
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return err
}

func retryableMySQLBackoff(attempt int) time.Duration {
	if attempt < 0 {
		attempt = 0
	}
	if attempt > retryableMySQLTransactionAttempts-1 {
		attempt = retryableMySQLTransactionAttempts - 1
	}
	return time.Duration(attempt+1) * 25 * time.Millisecond
}
