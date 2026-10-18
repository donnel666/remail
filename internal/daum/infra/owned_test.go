package infra

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/donnel666/remail/internal/daum/domain"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestOwnedDaumQueriesCannotMatchOrCountAliases(t *testing.T) {
	s, _ := newDaumAsyncTestService(t)
	ctx := context.Background()
	id, _, err := s.ImportLine(ctx, 7, domain.ImportLine{Email: "owner@daum.net", AppPassword: "fixture"})
	require.NoError(t, err)
	_, _, err = s.ImportLine(ctx, 8, domain.ImportLine{Email: "other@daum.net", AppPassword: "fixture"})
	require.NoError(t, err)
	for _, filter := range []ResourceFilter{{}, {Search: "owner"}, {Suffix: "daum.net"}} {
		page, err := s.ListOwnedResources(ctx, 7, filter)
		require.NoError(t, err)
		require.EqualValues(t, 1, page.Total)
		require.Len(t, page.Items, 1)
		require.Equal(t, id, page.Items[0].ID)
		require.Equal(t, []SuffixFacet{{Key: "daum.net", Count: 1}}, page.Facets.Suffixes)
		require.EqualValues(t, 1, page.Facets.All)
	}
	for _, filter := range []ResourceFilter{{Search: "owner@hanmail.net"}, {Search: "owner@hanmail2.net"}, {Suffix: "hanmail.net"}, {Suffix: "hanmail2.net"}, {Search: "other"}} {
		page, err := s.ListOwnedResources(ctx, 7, filter)
		require.NoError(t, err)
		require.Empty(t, page.Items)
		require.Zero(t, page.Total)
		for _, suffix := range page.Facets.Suffixes {
			require.Equal(t, "daum.net", suffix.Key)
		}
	}
	// The console does not even need permission to read the alias table.
	require.NoError(t, s.DB.Migrator().DropTable(&ExplicitAlias{}))
	page, err := s.ListOwnedResources(ctx, 7, ResourceFilter{Suffix: "daum.net"})
	require.NoError(t, err)
	require.EqualValues(t, 1, page.Total)
}

func TestOwnedDaumBulkRestoresScopeAfterTaskSerialization(t *testing.T) {
	for i, filter := range []ResourceFilter{{}, {Search: "owner@hanmail.net"}, {Suffix: "hanmail2.net"}} {
		t.Run([]string{"all", "alias_search", "alias_suffix"}[i], func(t *testing.T) {
			s, _ := newDaumAsyncTestService(t)
			ctx := context.Background()
			owner := uint(7)
			id, _, err := s.ImportLine(ctx, owner, domain.ImportLine{Email: "owner@daum.net", AppPassword: "fixture"})
			require.NoError(t, err)
			other, _, err := s.ImportLine(ctx, 8, domain.ImportLine{Email: "other@daum.net", AppPassword: "fixture"})
			require.NoError(t, err)
			deleted, _, err := s.ImportLine(ctx, owner, domain.ImportLine{Email: "deleted@daum.net", AppPassword: "fixture"})
			require.NoError(t, err)
			require.NoError(t, s.SetStatus(ctx, deleted, &owner, domain.StatusDeleted))
			server := miniredis.RunT(t)
			s.Redis = redis.NewClient(&redis.Options{Addr: server.Addr()})
			t.Cleanup(func() { _ = s.Redis.Close() })
			queue := &daumQueueStub{}
			s.Queue = queue
			status, err := s.SubmitBulk(ctx, "publish", BulkSelection{Mode: "filter", Filter: filter}, owner, &owner, "owned-publish", "")
			require.NoError(t, err)
			require.NotEmpty(t, queue.tasks)
			var task BulkTask
			require.NoError(t, json.Unmarshal(queue.tasks[0].Payload(), &task))
			require.False(t, task.Selection.Filter.MainAddressOnly, "query-only flags are not in JSON")
			require.NoError(t, s.ProcessBulk(ctx, task))
			status, err = s.GetBulkStatus(ctx, status.TaskID, owner, false)
			require.NoError(t, err)
			want := 0
			if filter.Search == "" && filter.Suffix == "" {
				want = 1
			}
			require.Equal(t, want, status.Requested)
			require.Equal(t, want, status.Affected)
			row, err := s.GetResource(ctx, id, &owner)
			require.NoError(t, err)
			require.Equal(t, want == 1, row.ForSale)
			row, err = s.GetResource(ctx, other, nil)
			require.NoError(t, err)
			require.False(t, row.ForSale)
			_, err = s.GetBulkStatus(ctx, status.TaskID, 8, false)
			require.ErrorIs(t, err, domain.ErrResourceMissing)
			status.OwnerID = nil
			raw, err := json.Marshal(status)
			require.NoError(t, err)
			require.NoError(t, s.Redis.Set(ctx, BulkStatusKey(status.TaskID), raw, 0).Err())
			_, err = s.GetBulkStatus(ctx, status.TaskID, owner, false)
			require.ErrorIs(t, err, domain.ErrResourceMissing, "administrator tasks are not user-scoped tasks")
		})
	}
}

func TestOwnedDaumCommandsEnforceOwnershipOnReplayAndPrivateDeletion(t *testing.T) {
	s, _ := newDaumAsyncTestService(t)
	ctx := context.Background()
	owner := uint(7)
	id, _, err := s.ImportLine(ctx, owner, domain.ImportLine{Email: "command@daum.net", AppPassword: "fixture"})
	require.NoError(t, err)
	command := Command{ResourceID: id, Version: 1, Action: "publish", OperatorUserID: owner, ScopeOwnerID: &owner, IdempotencyKey: "publish-owned"}
	result, err := s.ExecuteCommand(ctx, command)
	require.NoError(t, err)
	require.True(t, result.ForSale)
	_, err = s.ExecuteCommand(ctx, Command{ResourceID: id, Version: result.Version, Action: "delete", OperatorUserID: owner, ScopeOwnerID: &owner, IdempotencyKey: "delete-public"})
	require.ErrorIs(t, err, domain.ErrResourceNotPrivate)
	other := uint(8)
	require.NoError(t, s.UpdateResource(ctx, id, nil, &other))
	_, err = s.ExecuteCommand(ctx, command)
	require.ErrorIs(t, err, domain.ErrResourceMissing, "a cached receipt cannot bypass current ownership")
}

func TestOwnedDaumBulkIDsCannotDeletePublicOrForeignResources(t *testing.T) {
	s, _ := newDaumAsyncTestService(t)
	ctx := context.Background()
	owner := uint(7)
	private, _, err := s.ImportLine(ctx, owner, domain.ImportLine{Email: "private@daum.net", AppPassword: "fixture"})
	require.NoError(t, err)
	public, _, err := s.ImportLine(ctx, owner, domain.ImportLine{Email: "public@daum.net", AppPassword: "fixture"})
	require.NoError(t, err)
	require.NoError(t, s.SetForSale(ctx, public, &owner, true))
	foreign, _, err := s.ImportLine(ctx, 8, domain.ImportLine{Email: "foreign@daum.net", AppPassword: "fixture"})
	require.NoError(t, err)
	server := miniredis.RunT(t)
	s.Redis = redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = s.Redis.Close() })
	queue := &daumQueueStub{}
	s.Queue = queue
	status, err := s.SubmitBulk(ctx, "delete", BulkSelection{Mode: "ids", ResourceIDs: []uint{private, public, foreign}}, owner, &owner, "mixed-owned-delete", "")
	require.NoError(t, err)
	var task BulkTask
	require.NoError(t, json.Unmarshal(queue.tasks[0].Payload(), &task))
	require.NoError(t, s.ProcessBulk(ctx, task))
	status, err = s.GetBulkStatus(ctx, status.TaskID, owner, false)
	require.NoError(t, err)
	require.Equal(t, 3, status.Processed)
	require.Equal(t, 1, status.Affected)
	require.Equal(t, 2, status.Skipped)
	for _, id := range []uint{public, foreign} {
		row, err := s.GetResource(ctx, id, nil)
		require.NoError(t, err)
		require.NotEqual(t, domain.StatusDeleted, row.Status)
	}
}
