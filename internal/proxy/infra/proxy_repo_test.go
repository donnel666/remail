package infra

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOrderProxyServersFixedVectorsAndWeights(t *testing.T) {
	servers := []proxyServerCandidate{
		{ID: 11, ServerIP: "2001:db8::1", CapacityWeight: 1},
		{ID: 22, ServerIP: "2001:db8::2", CapacityWeight: 1},
		{ID: 33, ServerIP: "198.51.100.3", CapacityWeight: 3},
	}
	for seed, expected := range map[string][]uint{
		"alpha@example.com": {33, 11, 22},
		"req-0001":          {22, 33, 11},
		"req-0002":          {33, 11, 22},
	} {
		ordered := orderProxyServers(seed, servers)
		actual := make([]uint, len(ordered))
		for i := range ordered {
			actual[i] = ordered[i].ID
		}
		require.Equal(t, expected, actual, seed)
	}

	first := make(map[uint]int)
	for i := 0; i < 20000; i++ {
		first[orderProxyServers(fmt.Sprintf("binding-%d", i), servers)[0].ID]++
	}
	require.InDelta(t, 0.6, float64(first[33])/20000, 0.03)
	require.InDelta(t, 0.2, float64(first[11])/20000, 0.03)
	require.InDelta(t, 0.2, float64(first[22])/20000, 0.03)
}
