package infra

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/alicebob/miniredis/v2"
	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	protoapp "github.com/donnel666/remail/internal/proto/app"
	"github.com/donnel666/remail/internal/proto/domain"
	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestCommandRollbackVersionAndIdentityFence(t *testing.T) {
	s, _ := newProtoAsyncTestService(t)
	ctx := context.Background()
	id, _, err := s.ImportLine(ctx, 7, domain.ImportLine{Email: "version@proto.test", Password: "private-password"})
	require.NoError(t, err)
	cmd := Command{ResourceID: id, Version: 99, Action: "publish", OperatorUserID: 7, IdempotencyKey: "failed-command"}
	_, err = s.ExecuteCommand(ctx, cmd)
	require.ErrorIs(t, err, domain.ErrVersionConflict)
	var receipts int64
	require.NoError(t, s.DB.Model(&commandReceipt{}).Count(&receipts).Error)
	require.Zero(t, receipts)
	cmd.Version = 1
	result, err := s.ExecuteCommand(ctx, cmd)
	require.NoError(t, err)
	require.True(t, result.ForSale)
	q := &protoQueueStub{}
	_, err = s.DispatchPendingValidations(ctx, q, 10)
	require.NoError(t, err)
	var task protoapp.ValidationTaskPayload
	require.NoError(t, json.Unmarshal(q.tasks[0].Payload(), &task))
	email := "new-account@proto.test"
	require.NoError(t, s.UpdateResource(ctx, id, &email, nil))
	var raw Resource
	require.NoError(t, s.DB.First(&raw, id).Error)
	require.Empty(t, raw.Password)
	require.True(t, raw.ForSale)
	require.Greater(t, raw.CredentialRevision, task.CredentialRevision)
	require.ErrorIs(t, s.ProcessValidationTODO(ctx, task), domain.ErrInvalidClaim)
}

func TestTODOTerminalGenerationIsNotRedispatched(t *testing.T) {
	s, _ := newProtoAsyncTestService(t)
	ctx := context.Background()
	q := &protoQueueStub{}
	id, _, err := s.ImportLine(ctx, 7, domain.ImportLine{Email: "stable@proto.test", Password: "secret"})
	require.NoError(t, err)
	require.NoError(t, s.SetForSale(ctx, id, nil, true))
	count, err := s.DispatchPendingValidations(ctx, q, 10)
	require.NoError(t, err)
	require.Equal(t, 1, count)
	var task protoapp.ValidationTaskPayload
	require.NoError(t, json.Unmarshal(q.tasks[0].Payload(), &task))
	require.NoError(t, s.ProcessValidationTODO(ctx, task))
	count, err = s.DispatchPendingValidations(ctx, q, 10)
	require.NoError(t, err)
	require.Zero(t, count)
	row, err := s.GetResource(ctx, id, nil)
	require.NoError(t, err)
	require.Equal(t, domain.StatusPending, row.Status)
	require.True(t, row.ForSale)
	_, err = s.ClaimForValidation(ctx, id, nil)
	require.NoError(t, err)
	count, err = s.DispatchPendingValidations(ctx, q, 10)
	require.NoError(t, err)
	require.Equal(t, 1, count)
	row, err = s.GetResource(ctx, id, nil)
	require.NoError(t, err)
	require.NoError(t, s.MarkValidationSuccess(ctx, id, row.ValidationGeneration, row.CredentialRevision, "history-test"))
	count, err = s.DispatchPendingHistory(ctx, q, 10)
	require.NoError(t, err)
	require.Equal(t, 1, count)
	var history protoapp.HistoryTaskPayload
	require.NoError(t, json.Unmarshal(q.tasks[len(q.tasks)-1].Payload(), &history))
	require.NoError(t, s.ProcessHistoryTODO(ctx, history))
	count, err = s.DispatchPendingHistory(ctx, q, 10)
	require.NoError(t, err)
	require.Zero(t, count)
	row, err = s.GetResource(ctx, id, nil)
	require.NoError(t, err)
	require.Equal(t, domain.StatusIdentifying, row.Status)
	require.True(t, row.ForSale)
}

func TestImportChunkFailureRollsBackRootAndResumesCommittedLines(t *testing.T) {
	s, files := newProtoAsyncTestService(t)
	ctx := context.Background()
	var input strings.Builder
	for i := 0; i < 1001; i++ {
		fmt.Fprintf(&input, "account%04d@proto.test----secret\n", i)
	}
	content := []byte(input.String())
	_, err := files.SavePrivate(ctx, governancedomain.PrivateFile{ObjectKey: "source", ContentBytes: content})
	require.NoError(t, err)
	id, _, err := s.CreateImportWithArtifact(ctx, 7, 7, "skip", "chunk-import", "source", "source.txt", "import-test", content)
	require.NoError(t, err)
	require.NoError(t, s.DB.Exec(`CREATE TRIGGER reject_proto_insert BEFORE INSERT ON proto_resources WHEN NEW.email_address = 'account1000@proto.test' BEGIN SELECT RAISE(ABORT, 'injected write failure'); END`).Error)
	_, err = s.ProcessImportTask(ctx, id, 1)
	require.Error(t, err)
	var roots, children int64
	require.NoError(t, s.DB.Model(&resourceRoot{}).Count(&roots).Error)
	require.NoError(t, s.DB.Model(&Resource{}).Count(&children).Error)
	require.EqualValues(t, 1000, roots)
	require.Equal(t, roots, children)
	status, err := s.GetImport(ctx, id)
	require.NoError(t, err)
	require.Equal(t, 1000, status.ImportedCount)
	require.Equal(t, 1001, status.AcceptedCount)
	require.Equal(t, "pending", status.DispatchStatus)
	require.EqualValues(t, 2, status.Generation)
	require.NoError(t, s.DB.Exec("DROP TRIGGER reject_proto_insert").Error)
	ids, err := s.ProcessImportTask(ctx, id, 2)
	require.NoError(t, err)
	require.Len(t, ids, 1001)
	status, err = s.GetImport(ctx, id)
	require.NoError(t, err)
	require.Equal(t, 1001, status.ImportedCount)
	require.Equal(t, "succeeded", status.DispatchStatus)
}

type recoveringQueue struct {
	mu    sync.Mutex
	tasks []*asynq.Task
	fail  bool
}

func (q *recoveringQueue) EnqueueContext(_ context.Context, task *asynq.Task, _ ...asynq.Option) (*asynq.TaskInfo, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.fail {
		return nil, errors.New("queue unavailable")
	}
	q.tasks = append(q.tasks, task)
	return nil, nil
}
func (q *recoveringQueue) next() *asynq.Task {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.tasks) == 0 {
		return nil
	}
	task := q.tasks[0]
	q.tasks = q.tasks[1:]
	return task
}
func setupBulk(t *testing.T, s *Service) *recoveringQueue {
	t.Helper()
	server := miniredis.RunT(t)
	s.Redis = redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = s.Redis.Close() })
	q := &recoveringQueue{}
	s.Queue = q
	return q
}
func drainBulk(t *testing.T, s *Service, q *recoveringQueue) {
	t.Helper()
	for i := 0; i < 100; i++ {
		task := q.next()
		if task == nil {
			return
		}
		var payload BulkTask
		require.NoError(t, json.Unmarshal(task.Payload(), &payload))
		err := s.ProcessBulk(context.Background(), payload)
		if errors.Is(err, domain.ErrInvalidClaim) {
			continue
		}
		require.NoError(t, err)
	}
	t.Fatal("bulk did not finish")
}

func TestBulkRestoresQueueHandoffAndFreezesFilterBoundary(t *testing.T) {
	s, _ := newProtoAsyncTestService(t)
	ctx := context.Background()
	q := setupBulk(t, s)
	for i := 0; i < 105; i++ {
		_, _, err := s.ImportLine(ctx, 7, domain.ImportLine{Email: fmt.Sprintf("bulk%03d@proto.test", i), Password: "secret"})
		require.NoError(t, err)
	}
	q.fail = true
	_, err := s.SubmitBulk(ctx, "publish", BulkSelection{Mode: "filter", Filter: ResourceFilter{Status: domain.StatusPending}}, 7, nil, "filter-batch", "bulk-test")
	require.Error(t, err)
	late, _, err := s.ImportLine(ctx, 7, domain.ImportLine{Email: "late@proto.test", Password: "secret"})
	require.NoError(t, err)
	q.fail = false
	require.NoError(t, s.DispatchPendingBulk(ctx))
	drainBulk(t, s, q)
	status, err := s.SubmitBulk(ctx, "publish", BulkSelection{Mode: "filter", Filter: ResourceFilter{Status: domain.StatusPending}}, 7, nil, "filter-batch", "bulk-test")
	require.NoError(t, err)
	require.Equal(t, "succeeded", status.Status)
	require.Equal(t, 105, status.Affected)
	require.Equal(t, 105, status.Processed)
	row, err := s.GetResource(ctx, late, nil)
	require.NoError(t, err)
	require.False(t, row.ForSale)
	_, err = s.SubmitBulk(ctx, "delete", BulkSelection{Mode: "filter", Filter: ResourceFilter{Status: domain.StatusPending}}, 7, nil, "filter-batch", "bulk-test")
	require.ErrorIs(t, err, domain.ErrImportConflict)
}

func TestBulkConcurrentSubmissionReusesSingleIdentity(t *testing.T) {
	s, _ := newProtoAsyncTestService(t)
	ctx := context.Background()
	q := setupBulk(t, s)
	id, _, err := s.ImportLine(ctx, 7, domain.ImportLine{Email: "concurrent@proto.test", Password: "secret"})
	require.NoError(t, err)
	results := make(chan *BulkStatus, 8)
	errs := make(chan error, 8)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			status, err := s.SubmitBulk(ctx, "publish", BulkSelection{Mode: "ids", ResourceIDs: []uint{id}}, 7, nil, "concurrent-key", "request")
			results <- status
			errs <- err
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	taskID := ""
	for status := range results {
		if taskID == "" {
			taskID = status.TaskID
		}
		require.Equal(t, taskID, status.TaskID)
	}
	drainBulk(t, s, q)
	row, err := s.GetResource(ctx, id, nil)
	require.NoError(t, err)
	require.EqualValues(t, 2, row.Version)
}

func TestIndependentStatusFacetsAndLifecycleRules(t *testing.T) {
	s, _ := newProtoAsyncTestService(t)
	ctx := context.Background()
	a, _, err := s.ImportLine(ctx, 7, domain.ImportLine{Email: "a@one.test", Password: "secret"})
	require.NoError(t, err)
	b, _, err := s.ImportLine(ctx, 7, domain.ImportLine{Email: "b@two.test", Password: "secret"})
	require.NoError(t, err)
	require.NoError(t, s.SetStatus(ctx, b, nil, domain.StatusDisabled))
	page, err := s.ListResources(ctx, ResourceFilter{Status: domain.StatusPending, Limit: 1})
	require.NoError(t, err)
	require.EqualValues(t, 1, page.Total)
	require.EqualValues(t, 2, page.Facets.Status.All)
	require.EqualValues(t, 1, page.Facets.Status.Disabled)
	require.NoError(t, s.SetForSale(ctx, a, nil, true))
	owner := uint(7)
	require.ErrorIs(t, s.SetStatus(ctx, a, &owner, domain.StatusDeleted), domain.ErrResourceNotPrivate)
	require.NoError(t, s.DB.Create(&protoTestAllocation{ResourceID: a, Status: "allocated"}).Error)
	require.NoError(t, s.SetStatus(ctx, a, nil, domain.StatusDisabled))
	require.ErrorIs(t, s.SetStatus(ctx, a, nil, domain.StatusDeleted), domain.ErrResourceBusy)
}

type immediateValidationQueue struct{ service *Service }

func (q immediateValidationQueue) EnqueueContext(ctx context.Context, task *asynq.Task, _ ...asynq.Option) (*asynq.TaskInfo, error) {
	payload, err := protoapp.DecodeValidationTask(task)
	if err != nil {
		return nil, err
	}
	return nil, q.service.ProcessValidationTODO(ctx, payload)
}
func TestValidationWorkerFinishingBeforeActivationCannotResurrectTODO(t *testing.T) {
	s, _ := newProtoAsyncTestService(t)
	ctx := context.Background()
	id, _, err := s.ImportLine(ctx, 7, domain.ImportLine{Email: "early@proto.test", Password: "secret"})
	require.NoError(t, err)
	_, err = s.DispatchPendingValidations(ctx, immediateValidationQueue{s}, 10)
	require.NoError(t, err)
	row, err := s.GetResource(ctx, id, nil)
	require.NoError(t, err)
	require.Equal(t, domain.StatusPending, row.Status)
	require.Equal(t, "proto_validation_todo", row.LastSafeError)
}

type unavailableImportFiles struct{ *protoMemoryFiles }

func (f unavailableImportFiles) ReadPrivate(context.Context, string) (*governancedomain.PrivateFile, error) {
	return nil, errors.New("private storage is unavailable")
}

func TestImportRetryBudgetTerminatesAndPreservesCommittedItems(t *testing.T) {
	s, files := newProtoAsyncTestService(t)
	ctx := context.Background()
	content := []byte("kept@proto.test----secret\npending@proto.test----secret")
	id, _, err := s.CreateImport(ctx, 7, 7, "skip", "budget", content)
	require.NoError(t, err)
	resourceID, _, err := s.ImportLine(ctx, 7, domain.ImportLine{Email: "kept@proto.test", Password: "secret"})
	require.NoError(t, err)
	require.NoError(t, s.DB.Create(&importItem{ImportID: id, LineNumber: 1, ResourceID: &resourceID, Outcome: "imported"}).Error)
	require.NoError(t, s.DB.Model(&importRecord{}).Where("id = ?", id).Updates(map[string]any{"accepted_count": 2, "imported_count": 1}).Error)
	s.Files = unavailableImportFiles{files}
	for attempt := 1; attempt <= 3; attempt++ {
		_, err = s.ProcessImportTask(ctx, id, uint64(attempt))
		require.Error(t, err)
		status, err := s.GetImport(ctx, id)
		require.NoError(t, err)
		require.Equal(t, attempt, status.Attempts)
		require.Equal(t, 1, status.ImportedCount)
		require.Equal(t, 2, status.AcceptedCount)
		if attempt < 3 {
			require.Equal(t, "pending", status.DispatchStatus)
		} else {
			require.Equal(t, domain.ImportFailed, status.Status)
			require.Equal(t, "failed", status.DispatchStatus)
			require.NotNil(t, status.FinishedAt)
		}
	}
	q := &protoQueueStub{}
	require.NoError(t, s.DispatchPendingImports(ctx, q, 100))
	require.Empty(t, q.tasks)
	ids, err := s.ProcessImportTask(ctx, id, 3)
	require.NoError(t, err)
	require.Equal(t, []uint{resourceID}, ids)
	status, err := s.GetImport(ctx, id)
	require.NoError(t, err)
	require.Equal(t, 3, status.Attempts)
}

func TestImportAcceptedTotalIncludesRejectedAndAbortedRows(t *testing.T) {
	for _, strategy := range []string{"skip", "abort"} {
		t.Run(strategy, func(t *testing.T) {
			s, _ := newProtoAsyncTestService(t)
			ctx := context.Background()
			content := []byte("\ufeffgood@proto.test----secret\ninvalid\n\nlast@proto.test----secret\n")
			id, _, err := s.CreateImport(ctx, 7, 7, strategy, "total", content)
			require.NoError(t, err)
			require.NoError(t, s.ProcessImport(ctx, id, 1, 7, strategy, content))
			status, err := s.GetImport(ctx, id)
			require.NoError(t, err)
			require.Equal(t, 3, status.AcceptedCount)
			require.Equal(t, 1, status.Attempts)
			if strategy == "skip" {
				require.Equal(t, 2, status.ImportedCount)
				require.Equal(t, 1, status.SkippedCount)
			} else {
				require.Equal(t, domain.ImportFailed, status.Status)
				require.Zero(t, status.ImportedCount)
				require.Equal(t, 1, status.FailedCount)
			}
		})
	}
}

func TestBulkReceiptIdentitySurvivesRedisLoss(t *testing.T) {
	s, _ := newProtoAsyncTestService(t)
	q := setupBulk(t, s)
	ctx := context.Background()
	id, _, err := s.ImportLine(ctx, 7, domain.ImportLine{Email: "redis-reset@proto.test", Password: "secret"})
	require.NoError(t, err)
	selection := BulkSelection{Mode: "ids", ResourceIDs: []uint{id}}
	first, err := s.SubmitBulk(ctx, "publish", selection, 7, nil, "before-loss", "test")
	require.NoError(t, err)
	drainBulk(t, s, q)
	require.NoError(t, s.SetForSale(ctx, id, nil, false))
	require.NoError(t, s.Redis.FlushDB(ctx).Err()) // This client belongs only to this test's miniredis.
	second, err := s.SubmitBulk(ctx, "publish", selection, 7, nil, "after-loss", "test")
	require.NoError(t, err)
	require.Equal(t, first.TaskID, second.TaskID)
	drainBulk(t, s, q)
	row, err := s.GetResource(ctx, id, nil)
	require.NoError(t, err)
	require.True(t, row.ForSale, "a new client key must not replay a receipt from a reused Redis sequence")
	require.NoError(t, s.SetForSale(ctx, id, nil, false))
	require.NoError(t, s.Redis.FlushDB(ctx).Err())
	_, err = s.SubmitBulk(ctx, "publish", selection, 7, nil, "after-loss", "test")
	require.NoError(t, err)
	drainBulk(t, s, q)
	row, err = s.GetResource(ctx, id, nil)
	require.NoError(t, err)
	require.False(t, row.ForSale, "replaying the same client key must keep its original receipt")
}

func TestBulkLegacyPayloadReplaysExistingResourceReceipt(t *testing.T) {
	s, _ := newProtoAsyncTestService(t)
	q := setupBulk(t, s)
	ctx := context.Background()
	id, _, err := s.ImportLine(ctx, 7, domain.ImportLine{Email: "legacy-bulk@proto.test", Password: "secret"})
	require.NoError(t, err)
	_, err = s.SubmitBulk(ctx, "validate", BulkSelection{Mode: "ids", ResourceIDs: []uint{id}}, 7, nil, "legacy", "test")
	require.NoError(t, err)
	var task BulkTask
	require.NoError(t, json.Unmarshal(q.next().Payload(), &task))
	task.CommandKey = "" // Payload accepted by the previous implementation.
	_, err = s.ExecuteCommand(ctx, Command{ResourceID: id, Action: task.Action, OperatorUserID: task.OperatorUserID, IdempotencyKey: fmt.Sprintf("%s:%d", task.TaskID, id), Bulk: true})
	require.NoError(t, err)
	before, err := s.GetResource(ctx, id, nil)
	require.NoError(t, err)
	require.NoError(t, s.ProcessBulk(ctx, task))
	after, err := s.GetResource(ctx, id, nil)
	require.NoError(t, err)
	require.Equal(t, before.ValidationGeneration, after.ValidationGeneration)
	require.Equal(t, before.Version, after.Version)
}

func TestEditPublishedResourceRequiresSupplierOwner(t *testing.T) {
	s, _ := newProtoAsyncTestService(t)
	ctx := context.Background()
	s.ValidateOwner = func(context.Context, uint) (bool, error) { return true, nil }
	s.ValidateSupplierOwner = func(_ context.Context, id uint) (bool, error) { return id == 7, nil }
	id, _, err := s.ImportLine(ctx, 7, domain.ImportLine{Email: "owner-transfer@proto.test", Password: "secret"})
	require.NoError(t, err)
	require.NoError(t, s.SetForSale(ctx, id, nil, true))
	owner := uint(8)
	command := Command{ResourceID: id, Version: 2, Action: "edit", OwnerID: &owner, OperatorUserID: 7, IdempotencyKey: "transfer"}
	_, err = s.ExecuteCommand(ctx, command)
	require.ErrorIs(t, err, domain.ErrInvalidResource)
	row, err := s.GetResource(ctx, id, nil)
	require.NoError(t, err)
	require.EqualValues(t, 7, row.OwnerUserID)
	require.True(t, row.ForSale)
	require.NoError(t, s.SetForSale(ctx, id, nil, false))
	command.Version = 3
	_, err = s.ExecuteCommand(ctx, command)
	require.NoError(t, err)
	row, err = s.GetResource(ctx, id, nil)
	require.NoError(t, err)
	require.Equal(t, owner, row.OwnerUserID)
	require.False(t, row.ForSale)
}

func TestBulkAdminOwnerSearchSurvivesQueueSerialization(t *testing.T) {
	s, _ := newProtoAsyncTestService(t)
	q := setupBulk(t, s)
	ctx := context.Background()
	first, _, err := s.ImportLine(ctx, 7, domain.ImportLine{Email: "first@proto.test", Password: "secret"})
	require.NoError(t, err)
	second, _, err := s.ImportLine(ctx, 8, domain.ImportLine{Email: "second@proto.test", Password: "secret"})
	require.NoError(t, err)
	status, err := s.SubmitBulk(ctx, "publish", BulkSelection{Mode: "filter", Filter: ResourceFilter{Search: "OwnerName", SearchOwnerIDs: []uint{8}}}, 7, nil, "owner-search", "test")
	require.NoError(t, err)
	require.Equal(t, 1, status.Requested)
	drainBulk(t, s, q)
	for _, id := range []uint{first, second} {
		row, err := s.GetResource(ctx, id, nil)
		require.NoError(t, err)
		require.Equal(t, id == second, row.ForSale)
	}
}
