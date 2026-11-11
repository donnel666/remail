package platform

import (
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestFixedBackgroundGateReservesAndConsumesSlots(t *testing.T) {
	gate := NewFixedBackgroundExecutionGate(2)

	reserved, releaseReservation := ReserveBackgroundDispatch(gate, 2)
	require.Equal(t, 2, reserved)
	require.Zero(t, gate.Available())

	release, admitted := gate.TryAcquire()
	require.True(t, admitted)
	require.Zero(t, gate.Available())
	release()
	require.Equal(t, 1, gate.Available())

	releaseReservation(1)
	require.Equal(t, 2, gate.Available())
	limit, active, reserved := gate.Stats()
	require.Equal(t, 2, limit)
	require.Zero(t, active)
	require.Zero(t, reserved)
}

func TestFixedBackgroundGateKeepsConcurrentActiveTasksAtLimit(t *testing.T) {
	gate := NewFixedBackgroundExecutionGate(10)
	reservations, _ := ReserveBackgroundDispatch(gate, 10)
	require.Equal(t, 10, reservations)

	releases := make([]func(), 0, 10)
	for range 10 {
		release, admitted := gate.TryAcquire()
		require.True(t, admitted)
		releases = append(releases, release)
	}
	_, admitted := gate.TryAcquire()
	require.False(t, admitted)
	for _, release := range releases {
		release()
	}
	limit, active, reserved := gate.Stats()
	require.Equal(t, 10, limit)
	require.Zero(t, active)
	require.Zero(t, reserved)
}

func TestFixedBackgroundGateSharesLimitAcrossInstances(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	first := NewFixedBackgroundExecutionGate(2, client).WithName("shared")
	second := NewFixedBackgroundExecutionGate(2, client).WithName("shared")

	reserved, releaseReservation := ReserveBackgroundDispatch(first, 2)
	require.Equal(t, 2, reserved)
	release, admitted := second.TryAcquire()
	require.True(t, admitted)
	releaseSecond, admitted := first.TryAcquire()
	require.True(t, admitted)
	_, admitted = second.TryAcquire()
	require.False(t, admitted)
	release()
	releaseSecond()
	releaseReservation(1)
	require.Equal(t, 2, first.Available())
}
