package api

import (
	"testing"

	allocdomain "github.com/donnel666/remail/internal/alloc/domain"
	"github.com/donnel666/remail/internal/trade/domain"
	"github.com/stretchr/testify/require"
)

func TestMapAllocationInvalidRequest(t *testing.T) {
	require.ErrorIs(t, mapAllocationError(allocdomain.ErrInvalidAllocationRequest), domain.ErrInvalidOrderRequest)
}
