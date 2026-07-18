package platform

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestTTLCacheStaysBoundedAndPurgesExpiredEntries(t *testing.T) {
	now := time.Date(2026, 7, 18, 0, 0, 0, 0, time.UTC)
	cache := NewTTLCache[int, int]()
	cache.now = func() time.Time { return now }

	for key := 0; key < ttlCacheMaxEntries; key++ {
		cache.Set(key, key, time.Second)
	}
	now = now.Add(2 * time.Second)
	cache.Set(ttlCacheMaxEntries, 1, time.Minute)
	require.Len(t, cache.items, 1)

	for key := ttlCacheMaxEntries + 1; key < ttlCacheMaxEntries*2+100; key++ {
		cache.Set(key, key, time.Minute)
	}
	require.LessOrEqual(t, len(cache.items), ttlCacheMaxEntries)
}
