package app

import (
	"context"
	"reflect"
	"sort"
	"testing"

	"github.com/donnel666/remail/internal/core/domain"
	governancedomain "github.com/donnel666/remail/internal/governance/domain"
)

// domainBulkQueueStub records enqueued batch tasks and controls whether the
// initial enqueue reports the batch as newly accepted.
type domainBulkQueueStub struct {
	enqueued []AdminDomainBulkTask
	accepted bool
}

func (q *domainBulkQueueStub) EnqueueAdminDomainBulk(_ context.Context, task AdminDomainBulkTask) (bool, error) {
	q.enqueued = append(q.enqueued, task)
	return q.accepted, nil
}

func (q *domainBulkQueueStub) RefreshAdminDomainBulk(context.Context, AdminDomainBulkTask) (bool, error) {
	return true, nil
}

func (q *domainBulkQueueStub) ReleaseAdminDomainBulk(context.Context, AdminDomainBulkTask) error {
	return nil
}

type countingLogStub struct{ count int }

func (l *countingLogStub) Create(context.Context, *governancedomain.OperationLog) error {
	l.count++
	return nil
}

func TestSubmitBulkStateEnqueuesSortedUniqueIDsAndAuditsOnce(t *testing.T) {
	queue := &domainBulkQueueStub{accepted: true}
	logs := &countingLogStub{}
	service := NewAdminDomainCommandService(&adminDomainCommandRepoStub{}, nil, nil, logs)
	service.SetPorts(&adminDomainOwnersStub{}, nil)
	service.SetBulkQueue(queue)

	result, err := service.SubmitBulkState(
		context.Background(), "delete",
		AdminDomainBulkSelection{Mode: AdminDomainBulkIDs, ResourceIDs: []uint{3, 1, 2, 2}},
		7, "idem-key-1", "req-1", "/v1/admin/domains/delete",
	)
	if err != nil {
		t.Fatalf("SubmitBulkState() error = %v", err)
	}
	if result.Requested != 3 {
		t.Fatalf("Requested = %d, want 3 (deduped)", result.Requested)
	}
	if len(queue.enqueued) != 1 {
		t.Fatalf("enqueued %d tasks, want 1", len(queue.enqueued))
	}
	// The worker's id cursor requires ascending unique ids; this guards the
	// assumption that lets SubmitBulkState skip an explicit sort.
	if got := queue.enqueued[0].Selection.ResourceIDs; !reflect.DeepEqual(got, []uint{1, 2, 3}) {
		t.Fatalf("task ids = %v, want sorted unique [1 2 3]", got)
	}
	if logs.count != 1 {
		t.Fatalf("audit logs = %d, want exactly 1 on acceptance", logs.count)
	}
}

func TestSubmitBulkStateSkipsAuditWhenDeduped(t *testing.T) {
	queue := &domainBulkQueueStub{accepted: false} // lease already held by an in-flight batch
	logs := &countingLogStub{}
	service := NewAdminDomainCommandService(&adminDomainCommandRepoStub{}, nil, nil, logs)
	service.SetPorts(&adminDomainOwnersStub{}, nil)
	service.SetBulkQueue(queue)

	if _, err := service.SubmitBulkState(
		context.Background(), "publish",
		AdminDomainBulkSelection{Mode: AdminDomainBulkIDs, ResourceIDs: []uint{5}},
		7, "idem-key-1", "req-1", "/v1/admin/domains/publish",
	); err != nil {
		t.Fatalf("SubmitBulkState() error = %v", err)
	}
	if logs.count != 0 {
		t.Fatalf("audit logs = %d, want 0 for a deduped re-submission", logs.count)
	}
}

// domainBulkPageRepoStub implements only the repository methods the async page
// walker touches; the embedded nil interface panics on anything unexpected.
type domainBulkPageRepoStub struct {
	AdminDomainCommandRepository
	store map[uint]*domain.MailDomainResource
}

func (s *domainBulkPageRepoStub) WithTx(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

func (s *domainBulkPageRepoStub) MaxAdminDomainID(context.Context, AdminDomainListFilter) (uint, error) {
	var maxID uint
	for id := range s.store {
		if id > maxID {
			maxID = id
		}
	}
	return maxID, nil
}

func (s *domainBulkPageRepoStub) ListAdminDomainBulkPageIDs(_ context.Context, _ AdminDomainListFilter, afterID, throughID uint, limit int) ([]uint, error) {
	ids := make([]uint, 0, len(s.store))
	for id := range s.store {
		if id > afterID && id <= throughID {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	if len(ids) > limit {
		ids = ids[:limit]
	}
	return ids, nil
}

func (s *domainBulkPageRepoStub) LockAdminDomain(_ context.Context, id uint) (*domain.EmailResource, *domain.MailDomainResource, error) {
	resource, ok := s.store[id]
	if !ok {
		return nil, nil, domain.ErrResourceNotFound
	}
	return &domain.EmailResource{ID: id, Type: domain.ResourceTypeDomain, OwnerUserID: 9, Version: 1}, resource, nil
}

func (s *domainBulkPageRepoStub) SaveAdminDomain(context.Context, *domain.EmailResource, *domain.MailDomainResource, uint64, uint) error {
	return nil
}

func TestApplyDomainBulkPageWalksCursorAndAppliesUnpublish(t *testing.T) {
	store := map[uint]*domain.MailDomainResource{
		1: {ID: 1, Purpose: domain.PurposeSale, Status: domain.DomainStatusNormal},
		2: {ID: 2, Purpose: domain.PurposeSale, Status: domain.DomainStatusNormal},
		3: {ID: 3, Purpose: domain.PurposeNotSale, Status: domain.DomainStatusNormal}, // already target -> skip
		4: {ID: 4, Purpose: domain.PurposeSale, Status: domain.DomainStatusNormal},
		5: {ID: 5, Purpose: domain.PurposeSale, Status: domain.DomainStatusNormal},
	}
	service := NewAdminDomainCommandService(&domainBulkPageRepoStub{store: store}, nil, nil, &adminDomainLogStub{})

	task := AdminDomainBulkTask{Action: "unpublish", OperatorUserID: 9, BatchID: "batch", Selection: AdminDomainBulkSelection{Mode: AdminDomainBulkFilter}}
	affected, skipped, pages := 0, 0, 0
	for pages < 100 {
		page, err := service.applyDomainBulkPage(context.Background(), task, 2)
		if err != nil {
			t.Fatalf("applyDomainBulkPage page %d: %v", pages, err)
		}
		affected += page.Affected
		skipped += page.Skipped
		pages++
		if page.Done {
			break
		}
		if page.AfterID <= task.AfterID {
			t.Fatalf("cursor did not advance: %d", page.AfterID)
		}
		task.AfterID = page.AfterID
		task.ThroughID = page.ThroughID
	}
	if affected != 4 || skipped != 1 {
		t.Fatalf("affected=%d skipped=%d, want 4 and 1", affected, skipped)
	}
	if pages != 3 {
		t.Fatalf("pages=%d, want 3 (bounded to 2 rows each over id ceiling 5)", pages)
	}
	for id, r := range store {
		if r.Purpose != domain.PurposeNotSale {
			t.Fatalf("resource %d purpose=%s, want not_sale after batch", id, r.Purpose)
		}
	}
}

func TestAdminDomainBulkIDPage(t *testing.T) {
	ids := []uint{1, 2, 3, 4, 5}
	cases := []struct {
		afterID uint
		limit   int
		want    []uint
	}{
		{0, 2, []uint{1, 2}},
		{2, 2, []uint{3, 4}},
		{4, 2, []uint{5}},
		{5, 2, nil},
	}
	for _, tc := range cases {
		got := adminDomainBulkIDPage(ids, tc.afterID, tc.limit)
		if len(got) == 0 && len(tc.want) == 0 {
			continue
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("adminDomainBulkIDPage(afterID=%d, limit=%d) = %v, want %v", tc.afterID, tc.limit, got, tc.want)
		}
	}
}
