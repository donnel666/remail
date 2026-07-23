package infra

import (
	"testing"

	"github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/require"
)

func TestWholeTransactionRollbackErrorExcludesLockTimeout(t *testing.T) {
	require.True(t, isWholeTransactionRollbackError(&mysql.MySQLError{Number: 1213}))
	require.False(t, isWholeTransactionRollbackError(&mysql.MySQLError{Number: 1205}))
	require.True(t, isDeadlockError(&mysql.MySQLError{Number: 1205}))
	require.Equal(t, "1205", mysqlRetryEvent(&mysql.MySQLError{Number: 1205}))
	require.Equal(t, "1213", mysqlRetryEvent(&mysql.MySQLError{Number: 1213}))
}
