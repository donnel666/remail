package app

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHistoricalDaumOrderIdentityIncludesFullNormalizedAddress(t *testing.T) {
	command := HistoricalDaumAllocationCommand{ResourceID: ^uint(0), ProjectID: ^uint(0), Email: strings.Repeat("a", 240) + "@hanmail2.net"}
	first := historicalDaumAllocationOrderNo(command)
	require.LessOrEqual(t, len(first), 64)
	command.Email = "  " + strings.ToUpper(command.Email) + "  "
	require.Equal(t, first, historicalDaumAllocationOrderNo(command))
	command.Email = "corrected@hanmail2.net"
	require.NotEqual(t, first, historicalDaumAllocationOrderNo(command), "same resource/project/suffix with a corrected local part is a new identity")
}
