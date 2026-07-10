package app

import (
	"context"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/donnel666/remail/internal/governance/domain"
	"github.com/stretchr/testify/require"
)

func TestRetentionRunOnceDeletesInboundObjectsAndOrphans(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	oldRecordedKey := "mailtransport/inbound/2026/03/01/recorded.eml"
	oldOrphanKey := "mailtransport/inbound/2026/03/01/orphan.eml"
	oldReferencedKey := "mailtransport/inbound/2026/03/01/referenced.eml"
	recentOrphanKey := "mailtransport/inbound/2026/07/09/recent.eml"

	repo := &retentionRepoStub{
		inboundRows: map[uint64]string{
			1: oldRecordedKey,
		},
		messageCutoffs: make(map[string]time.Time),
		existingKeys: map[string]struct{}{
			oldRecordedKey:   {},
			oldReferencedKey: {},
		},
	}
	files := &retentionFileStoreStub{
		objects: map[string]domain.PrivateObject{
			oldRecordedKey: {
				ObjectKey:    oldRecordedKey,
				LastModified: now.AddDate(0, 0, -120),
			},
			oldOrphanKey: {
				ObjectKey:    oldOrphanKey,
				LastModified: now.AddDate(0, 0, -120),
			},
			oldReferencedKey: {
				ObjectKey:    oldReferencedKey,
				LastModified: now.AddDate(0, 0, -120),
			},
			recentOrphanKey: {
				ObjectKey:    recentOrphanKey,
				LastModified: now.AddDate(0, 0, -1),
			},
		},
	}
	for i := 0; i < retentionBatchSize+1; i++ {
		key := "mailtransport/inbound/2026/03/02/bulk-" + strings.Repeat("0", 5-len(intString(i))) + intString(i) + ".eml"
		files.objects[key] = domain.PrivateObject{
			ObjectKey:    key,
			LastModified: now.AddDate(0, 0, -120),
		}
	}
	service := NewRetentionService(repo, files, nil)
	service.now = func() time.Time { return now }

	service.RunOnce(ctx)

	require.NotContains(t, files.objects, oldRecordedKey)
	require.NotContains(t, files.objects, oldOrphanKey)
	require.Contains(t, files.objects, oldReferencedKey)
	require.Contains(t, files.objects, recentOrphanKey)
	require.Empty(t, repo.inboundRows)
	require.Equal(t, now.AddDate(0, 0, -3), repo.messageCutoffs["microsoft"])
	require.Equal(t, now.AddDate(0, 0, -30), repo.messageCutoffs["domain"])
	for objectKey := range files.objects {
		require.NotContains(t, objectKey, "/bulk-")
	}
}

type retentionRepoStub struct {
	inboundRows    map[uint64]string
	messageCutoffs map[string]time.Time
	existingKeys   map[string]struct{}
}

func (r *retentionRepoStub) DeleteIdempotencyKeysBefore(context.Context, time.Time, int) (int64, error) {
	return 0, nil
}

func (r *retentionRepoStub) DeleteMailmatchMessagesBefore(_ context.Context, before time.Time, resourceType string, _ int) (int64, error) {
	r.messageCutoffs[resourceType] = before
	return 0, nil
}

func (r *retentionRepoStub) DeleteFetchJobsTerminalBefore(context.Context, time.Time, int) (int64, error) {
	return 0, nil
}

func (r *retentionRepoStub) DeleteAllocationDailyUsagesBefore(context.Context, time.Time, int) (int64, error) {
	return 0, nil
}

func (r *retentionRepoStub) DeleteResourceValidationJobsTerminalBefore(context.Context, time.Time, int) (int64, error) {
	return 0, nil
}

func (r *retentionRepoStub) DeleteProxyCheckJobsTerminalBefore(context.Context, time.Time, int) (int64, error) {
	return 0, nil
}

func (r *retentionRepoStub) DeleteOutboundMailsTerminalBefore(context.Context, time.Time, int) (int64, error) {
	return 0, nil
}

func (r *retentionRepoStub) DeleteSystemLogsBefore(context.Context, time.Time, int) (int64, error) {
	return 0, nil
}

func (r *retentionRepoStub) ListInboundMailObjectsBefore(context.Context, time.Time, int) ([]RetentionInboundMailObject, error) {
	items := make([]RetentionInboundMailObject, 0, len(r.inboundRows))
	for id, objectKey := range r.inboundRows {
		items = append(items, RetentionInboundMailObject{ID: id, ObjectKey: objectKey})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	return items, nil
}

func (r *retentionRepoStub) ListExistingInboundObjectKeys(_ context.Context, objectKeys []string) (map[string]struct{}, error) {
	out := make(map[string]struct{}, len(objectKeys))
	for _, objectKey := range objectKeys {
		if _, ok := r.existingKeys[objectKey]; ok {
			out[objectKey] = struct{}{}
		}
	}
	return out, nil
}

func (r *retentionRepoStub) DeleteInboundMailsByID(_ context.Context, ids []uint64) (int64, error) {
	var deleted int64
	for _, id := range ids {
		if objectKey, ok := r.inboundRows[id]; ok {
			delete(r.inboundRows, id)
			delete(r.existingKeys, objectKey)
			deleted++
		}
	}
	return deleted, nil
}

type retentionFileStoreStub struct {
	objects map[string]domain.PrivateObject
}

func (s *retentionFileStoreStub) SavePrivate(context.Context, domain.PrivateFile) (*domain.StoredPrivateFile, error) {
	return nil, nil
}

func (s *retentionFileStoreStub) SavePrivateStream(context.Context, domain.PrivateFileStream) (*domain.StoredPrivateFile, error) {
	return nil, nil
}

func (s *retentionFileStoreStub) ReadPrivate(context.Context, string) (*domain.PrivateFile, error) {
	return nil, nil
}

func (s *retentionFileStoreStub) DeletePrivate(_ context.Context, objectKey string) error {
	delete(s.objects, objectKey)
	return nil
}

func (s *retentionFileStoreStub) ListPrivate(_ context.Context, prefix string, startAfter string, limit int) ([]domain.PrivateObject, error) {
	items := make([]domain.PrivateObject, 0, len(s.objects))
	for _, object := range s.objects {
		if strings.HasPrefix(object.ObjectKey, prefix) && (startAfter == "" || object.ObjectKey > startAfter) {
			items = append(items, object)
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ObjectKey < items[j].ObjectKey })
	if limit > 0 && len(items) > limit {
		return items[:limit], nil
	}
	return items, nil
}

func intString(v int) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}
