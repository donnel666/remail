package infra

import (
	"testing"

	"github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/require"
)

func TestTransactionContentionClassification(t *testing.T) {
	lockTimeout := &mysql.MySQLError{Number: 1205}
	deadlock := &mysql.MySQLError{Number: 1213}

	require.True(t, isDeadlockError(lockTimeout))
	require.False(t, isDeadlockVictim(lockTimeout))
	require.Equal(t, "1205", mysqlRetryEvent(lockTimeout))
	require.True(t, isDeadlockError(deadlock))
	require.True(t, isDeadlockVictim(deadlock))
	require.Equal(t, "1213", mysqlRetryEvent(deadlock))
}
