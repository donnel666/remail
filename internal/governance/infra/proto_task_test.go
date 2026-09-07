package infra

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	governanceapp "github.com/donnel666/remail/internal/governance/app"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestProtoBulkTaskProjectsIndependentRedisProgress(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	require.NoError(t, server.Set("remail:proto:bulk:status:proto_bulk:7", `{
    "taskId":"proto_bulk:7","status":"succeeded","action":"disable",
    "attempts":1,"maxAttempts":4,"requested":3,"processed":3,"affected":2,"skipped":1,
    "reasonCounts":{"not_found":1},"claimToken":"not-published",
    "createdAt":"2026-09-07T01:00:00Z","updatedAt":"2026-09-07T01:00:02Z"
}`))
	service := governanceapp.NewAdminTaskQueryService(NewAdminTaskViewRepo(nil, client))
	task, err := service.Get(context.Background(), "proto_bulk:7")
	require.NoError(t, err)
	require.Equal(t, "proto_bulk:7", task.TaskID())
	require.Equal(t, governanceapp.AdminTaskBizProtoResourceBulk, task.BizType)
	require.Equal(t, governanceapp.AdminTaskKindBulkDisable, task.Kind)
	require.EqualValues(t, 2, task.Progress.Succeeded)
	require.Equal(t, []governanceapp.AdminTaskReasonCount{{Reason: "not_found", Count: 1}}, task.Progress.ReasonCounts)
	_, err = service.Get(context.Background(), "proto_bulk:8")
	require.ErrorIs(t, err, governanceapp.ErrAdminTaskNotFound)
}
