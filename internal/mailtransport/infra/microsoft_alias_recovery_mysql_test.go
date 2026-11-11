package infra

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	mailapp "github.com/donnel666/remail/internal/mailtransport/app"
	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestAliasRecoveryPreservesLiveWorkAndReconcilesInterruptedCreationMySQL(t *testing.T) {
	db := newMailTransportMySQLTestDB(t)
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	queueClient := asynq.NewClient(asynq.RedisClientOpt{Addr: server.Addr()})
	t.Cleanup(func() {
		require.NoError(t, queueClient.Close())
		require.NoError(t, client.Close())
	})
	ctx := context.Background()
	now := time.Now().UTC()
	store := NewMicrosoftAliasStore(db, client)
	for index, state := range []string{"running", "queued", "running", "queued"} {
		id := uint(4100 + index)
		createMicrosoftAliasTestResource(t, db, id, "normal")
		updated := now.Add(-time.Hour)
		if index == 2 {
			updated = now
		}
		require.NoError(t, db.Create(&MicrosoftAliasScheduleModel{
			ResourceID: id, Generation: 1, Status: state, ClaimToken: "old-claim",
			NextRunAt: updated, UpdatedAt: updated,
		}).Error)
	}
	running := &MicrosoftAliasAttemptModel{ResourceID: 4100, Candidate: "pending123456@outlook.com", Status: "running", QuotaAt: now.Add(-time.Hour)}
	confirmed := &MicrosoftAliasAttemptModel{ResourceID: 4100, Candidate: "confirmed123456@outlook.com", Status: "succeeded", Category: "added", WasAttempted: true, QuotaAt: now.Add(-time.Hour)}
	require.NoError(t, db.Create(running).Error)
	require.NoError(t, db.Create(confirmed).Error)
	accepted, err := NewMicrosoftAliasQueue(queueClient).EnqueueMicrosoftAlias(ctx, mailapp.MicrosoftAliasTask{ResourceID: 4103, Generation: 1})
	require.NoError(t, err)
	require.True(t, accepted)

	require.NoError(t, store.recoverStaleDispatches(ctx, now))
	for _, id := range []uint{4100, 4101} {
		var schedule MicrosoftAliasScheduleModel
		require.NoError(t, db.First(&schedule, "resource_id=?", id).Error)
		require.Equal(t, "pending", schedule.Status)
		require.EqualValues(t, 2, schedule.Generation)
		require.Empty(t, schedule.ClaimToken)
		require.WithinRange(t, schedule.NextRunAt, now.Add(-time.Second), now.Add(30*time.Second))
	}
	var recent, live MicrosoftAliasScheduleModel
	require.NoError(t, db.First(&recent, "resource_id=4102").Error)
	require.Equal(t, "running", recent.Status)
	require.NoError(t, db.First(&live, "resource_id=4103").Error)
	require.Equal(t, "queued", live.Status)
	require.NoError(t, db.First(running, running.ID).Error)
	require.Equal(t, "uncertain", running.Status)
	require.True(t, running.WasAttempted)
	require.NotNil(t, running.UncertainSince)
	require.NoError(t, db.First(confirmed, confirmed.ID).Error)
	require.Equal(t, "succeeded", confirmed.Status)
	require.ErrorIs(t, store.Defer(ctx, 4100, "old-claim", now, "old worker", false), mailapp.ErrMicrosoftAliasStaleClaim)

	server.FlushAll()
	require.NoError(t, store.recoverStaleDispatches(ctx, now.Add(31*time.Second)))
	require.NoError(t, db.First(&live, "resource_id=4103").Error)
	require.Equal(t, "pending", live.Status)
	require.EqualValues(t, 2, live.Generation)
}

func TestAliasRecoveryCannotReleaseFreshClaimOrGuessThroughRedisFailureMySQL(t *testing.T) {
	db := newMailTransportMySQLTestDB(t)
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr(), MaxRetries: -1})
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	ctx := context.Background()
	now := time.Now().UTC()
	store := NewMicrosoftAliasStore(db, client)
	createMicrosoftAliasTestResource(t, db, 4110, "normal")
	createVerifiedMicrosoftAliasBinding(t, db, 4110)
	require.NoError(t, db.Create(&MicrosoftAliasScheduleModel{
		ResourceID: 4110, Generation: 1, Status: "queued", NextRunAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Hour),
	}).Error)
	server.SetError("ERR recovery test unavailable")
	require.Error(t, store.recoverStaleDispatches(ctx, now))
	var schedule MicrosoftAliasScheduleModel
	require.NoError(t, db.First(&schedule, "resource_id=4110").Error)
	require.Equal(t, "queued", schedule.Status)
	require.EqualValues(t, 1, schedule.Generation)
	server.SetError("")

	task := mailapp.MicrosoftAliasTask{ResourceID: 4110, Generation: 1}
	_, claimed, err := store.Claim(ctx, task, now)
	require.NoError(t, err)
	require.True(t, claimed)
	changed, err := store.markDispatchFailed(ctx, task, now, "stale scan", now.Add(-microsoftAliasTimeout()-time.Minute))
	require.NoError(t, err)
	require.False(t, changed)
	require.NoError(t, db.First(&schedule, "resource_id=4110").Error)
	require.Equal(t, "running", schedule.Status)
	require.EqualValues(t, 1, schedule.Generation)
}
