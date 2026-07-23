package infra

import (
	"errors"
	"fmt"
	"testing"

	"github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/require"
)

func TestMySQLContentionEventPreservesServerErrorNumber(t *testing.T) {
	tests := []struct {
		name  string
		err   error
		event string
		ok    bool
	}{
		{name: "lock wait timeout", err: &mysql.MySQLError{Number: 1205}, event: "1205", ok: true},
		{name: "deadlock", err: fmt.Errorf("projection failed: %w", &mysql.MySQLError{Number: 1213}), event: "1213", ok: true},
		{name: "other mysql error", err: &mysql.MySQLError{Number: 1062}, ok: false},
		{name: "non mysql error", err: errors.New("failed"), ok: false},
		{name: "nil", err: nil, ok: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			event, ok := mysqlContentionEvent(tt.err)
			require.Equal(t, tt.ok, ok)
			require.Equal(t, tt.event, event)
		})
	}
}
