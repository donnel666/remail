package api

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestProjectHistoryCapacityIsLimitedToFourWorkers(t *testing.T) {
	releases := make([]func(), 0, projectHistoryConcurrency)
	for range projectHistoryConcurrency {
		release, admitted := acquireProjectHistoryCapacity(nil)
		require.True(t, admitted)
		releases = append(releases, release)
	}
	_, admitted := acquireProjectHistoryCapacity(nil)
	require.False(t, admitted)
	for _, release := range releases {
		release()
	}
}
