package app

import (
	"context"
	"testing"
	"time"

	"github.com/donnel666/remail/internal/core/domain"
	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	"github.com/stretchr/testify/require"
)

type adminResourceBulkRepoStub struct{}

func (adminResourceBulkRepoStub) MaxCandidateID(context.Context, AdminResourceBulkFilterValue, time.Time) (uint, error) {
	return 0, nil
}
func (adminResourceBulkRepoStub) ListCandidateIDs(context.Context, AdminResourceBulkFilterValue, uint, uint, int, time.Time) ([]uint, error) {
	return nil, nil
}

type adminResourceBulkQueueStub struct {
	tasks    []AdminResourceBulkTask
	released bool
}

func (q *adminResourceBulkQueueStub) EnqueueAdminResourceBulk(_ context.Context, task AdminResourceBulkTask) (bool, error) {
	initial := task.ClaimToken == ""
	if initial {
		task.ClaimToken = "claim"
	}
	q.tasks = append(q.tasks, task)
	return initial, nil
}
func (*adminResourceBulkQueueStub) RefreshAdminResourceBulk(context.Context, AdminResourceBulkTask) (bool, error) {
	return true, nil
}
func (q *adminResourceBulkQueueStub) ReleaseAdminResourceBulk(context.Context, AdminResourceBulkTask) error {
	q.released = true
	return nil
}

type adminResourceBulkCommandRepoStub struct {
	resources map[uint]domain.MicrosoftResource
}

func (r *adminResourceBulkCommandRepoStub) WithTx(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}
func (*adminResourceBulkCommandRepoStub) ReserveAdminCommand(context.Context, AdminResourceCommandReceipt) ([]byte, bool, error) {
	return nil, false, nil
}
func (*adminResourceBulkCommandRepoStub) CompleteAdminCommand(context.Context, uint, string, []byte) error {
	return nil
}
func (r *adminResourceBulkCommandRepoStub) LockAdminMicrosoft(_ context.Context, resourceID uint) (*domain.EmailResource, *domain.MicrosoftResource, error) {
	resource, ok := r.resources[resourceID]
	if !ok {
		return nil, nil, domain.ErrResourceNotFound
	}
	return &domain.EmailResource{ID: resourceID, Type: domain.ResourceTypeMicrosoft, OwnerUserID: 7, Version: 1}, &resource, nil
}
func (*adminResourceBulkCommandRepoStub) SaveAdminMicrosoft(context.Context, *domain.EmailResource, *domain.MicrosoftResource, uint64) error {
	return nil
}

type adminResourceBulkLogStub struct{ count int }

func (l *adminResourceBulkLogStub) Create(context.Context, *governancedomain.OperationLog) error {
	l.count++
	return nil
}

type adminResourceBulkMaintenanceStub struct{ ids []uint }

func (m *adminResourceBulkMaintenanceStub) SubmitAdminResourceMaintenance(_ context.Context, command AdminResourceMaintenanceCommand) (string, error) {
	m.ids = append(m.ids, command.ResourceID)
	return "", nil
}

func TestAdminResourceBulkUsesRedisCursorAndBusinessRows(t *testing.T) {
	queue := &adminResourceBulkQueueStub{}
	logs := &adminResourceBulkLogStub{}
	commandRepo := &adminResourceBulkCommandRepoStub{resources: map[uint]domain.MicrosoftResource{
		2: {ID: 2, Status: domain.MicrosoftStatusIdentifying, ClientID: "client", RefreshToken: "token"},
		3: {ID: 3, Status: domain.MicrosoftStatusNormal, ClientID: "client", RefreshToken: "token"},
	}}
	commands := NewAdminResourceCommandService(commandRepo, nil, logs)
	maintenance := &adminResourceBulkMaintenanceStub{}
	service := NewAdminResourceBulkService(adminResourceBulkRepoStub{}, queue, commands)
	service.SetMaintenancePort(maintenance)

	command, reused, err := service.Submit(
		context.Background(), AdminResourceBulkHistory,
		AdminResourceBulkSelection{Mode: AdminResourceBulkIDs, ResourceIDs: []uint{3, 2, 3}},
		9, "bulk-key", "request", "/v1/admin/resources/maintenance",
	)
	require.NoError(t, err)
	require.False(t, reused)
	require.Equal(t, 2, command.MatchedCount)
	require.Equal(t, 1, logs.count)
	require.Len(t, queue.tasks, 1)
	require.Equal(t, []uint{2, 3}, queue.tasks[0].Selection.ResourceIDs)

	require.NoError(t, service.Process(context.Background(), queue.tasks[0]))
	require.Equal(t, []uint{2, 3}, maintenance.ids)
	require.True(t, queue.released)
}
