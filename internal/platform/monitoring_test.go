package platform

import (
	"math"
	"testing"

	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"
)

func TestMonitoringParsesRedisInfoAndHistogramQuantiles(t *testing.T) {
	info := parseRedisInfo("# Memory\r\nused_memory:2048\r\nmem_fragmentation_ratio:1.25\r\n")
	require.Equal(t, "2048", info["used_memory"])
	require.Equal(t, "1.25", info["mem_fragmentation_ratio"])

	buckets := []*dto.Bucket{
		{UpperBound: ptr(1.0), CumulativeCount: ptr(uint64(50))},
		{UpperBound: ptr(2.0), CumulativeCount: ptr(uint64(95))},
		{UpperBound: ptr(math.Inf(1)), CumulativeCount: ptr(uint64(100))},
	}
	require.InDelta(t, 1, histogramQuantile(0.50, 100, buckets), 0.0001)
	require.InDelta(t, 2, histogramQuantile(0.95, 100, buckets), 0.0001)
}

func ptr[T any](value T) *T { return &value }
