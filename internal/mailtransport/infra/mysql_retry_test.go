package infra

import (
	"context"
	"errors"
	"testing"

	"github.com/go-sql-driver/mysql"
)

func TestRetryOnDeadlockRetriesTransientDeadlockThenSucceeds(t *testing.T) {
	deadlock := &mysql.MySQLError{Number: 1213, Message: "Deadlock found"}
	calls := 0
	err := retryOnDeadlock(context.Background(), aliasTransactionAttempts, func() error {
		calls++
		if calls < 3 {
			return deadlock
		}
		return nil
	})
	if err != nil {
		t.Fatalf("expected success after retries, got %v", err)
	}
	if calls != 3 {
		t.Fatalf("expected 3 attempts, got %d", calls)
	}
}

func TestRetryOnDeadlockStopsOnNonDeadlockError(t *testing.T) {
	sentinel := errors.New("boom")
	calls := 0
	err := retryOnDeadlock(context.Background(), aliasTransactionAttempts, func() error {
		calls++
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected sentinel error, got %v", err)
	}
	if calls != 1 {
		t.Fatalf("non-deadlock error must not retry, got %d attempts", calls)
	}
}

func TestRetryOnDeadlockGivesUpAfterBudget(t *testing.T) {
	deadlock := &mysql.MySQLError{Number: 1213}
	calls := 0
	err := retryOnDeadlock(context.Background(), aliasTransactionAttempts, func() error {
		calls++
		return deadlock
	})
	if !isAliasDeadlockError(err) {
		t.Fatalf("expected deadlock error to surface after budget, got %v", err)
	}
	if calls != aliasTransactionAttempts {
		t.Fatalf("expected %d attempts, got %d", aliasTransactionAttempts, calls)
	}
}

func TestRetryOnDeadlockHonorsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls := 0
	err := retryOnDeadlock(ctx, aliasTransactionAttempts, func() error {
		calls++
		return &mysql.MySQLError{Number: 1213}
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation, got %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected to stop after first attempt on cancelled ctx, got %d", calls)
	}
}
