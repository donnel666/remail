package infra

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	coreapp "github.com/donnel666/remail/internal/core/app"
	"github.com/donnel666/remail/internal/core/domain"
	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	governanceinfra "github.com/donnel666/remail/internal/governance/infra"
	"github.com/stretchr/testify/require"
)

type adminBulkQueueStub struct {
	tasks       []coreapp.AdminResourceBulkTask
	dispatchers int
}

type failSecondBulkAllocationGuard struct {
	calls int
}

func (g *failSecondBulkAllocationGuard) AssertNoActiveAllocations(context.Context, []uint) error {
	g.calls++
	if g.calls == 2 {
		return fmt.Errorf("injected second resource failure")
	}
	return nil
}

func (q *adminBulkQueueStub) EnqueueAdminResourceBulk(_ context.Context, task coreapp.AdminResourceBulkTask) error {
	q.tasks = append(q.tasks, task)
	return nil
}

func (q *adminBulkQueueStub) EnqueueAdminResourceBulkDispatcher(context.Context, time.Duration) error {
	q.dispatchers++
	return nil
}

func TestAdminResourceBulkFilterRunsDurablyAndIsIdempotentMySQL(t *testing.T) {
	db := newCoreMySQLTestDB(t)
	insertAdminCommandUsers(t, db)
	resourceRepo := NewResourceRepo(db)
	validationRepo := NewResourceValidationRepo(db)
	validation := coreapp.NewResourceValidationUseCase(resourceRepo, validationRepo, adminCommandValidationQueue{}, nil)
	adminRepo := NewAdminResourceRepo(db)
	commands := coreapp.NewAdminResourceCommandService(adminRepo, validation, governanceinfra.NewOperationLogRepo(db))
	commands.SetPorts(adminCommandOwners(), nil, &adminCommandBindingPort{}, &adminCommandAllocationGuard{})
	bulkRepo := NewAdminResourceBulkRepo(db)
	queue := &adminBulkQueueStub{}
	service := coreapp.NewAdminResourceBulkService(bulkRepo, queue, commands)

	outlookRoot := &domain.EmailResource{Type: domain.ResourceTypeMicrosoft, OwnerUserID: 1}
	outlook := &domain.MicrosoftResource{
		EmailAddress: "bulk-filter@outlook.com", Password: "secret", Status: domain.MicrosoftStatusNormal, ForSale: true,
	}
	require.NoError(t, resourceRepo.CreateMicrosoft(context.Background(), outlookRoot, outlook))
	hotmailRoot := &domain.EmailResource{Type: domain.ResourceTypeMicrosoft, OwnerUserID: 1}
	hotmail := &domain.MicrosoftResource{
		EmailAddress: "bulk-other@hotmail.com", Password: "secret", Status: domain.MicrosoftStatusNormal, ForSale: true,
	}
	require.NoError(t, resourceRepo.CreateMicrosoft(context.Background(), hotmailRoot, hotmail))

	command, reused, err := service.Submit(
		context.Background(),
		coreapp.AdminResourceBulkUnpublish,
		coreapp.AdminResourceBulkSelection{
			Mode: coreapp.AdminResourceBulkFilter,
			Filter: coreapp.AdminResourceBulkFilterValue{
				Suffix:  "@outlook.com",
				ForSale: boolPointer(true),
			},
		},
		9,
		"bulk-filter-key",
		"req-bulk-filter",
		"/v1/admin/resources/unpublish",
	)
	require.NoError(t, err)
	require.False(t, reused)
	require.NotZero(t, command.ID)
	require.Equal(t, "queued", command.Status)
	require.Equal(t, 1, queue.dispatchers)

	require.NoError(t, service.DispatchPending(context.Background(), 10))
	require.Len(t, queue.tasks, 1)
	require.NoError(t, service.Process(context.Background(), queue.tasks[0]))

	storedCommand, err := service.Get(context.Background(), command.ID)
	require.NoError(t, err)
	require.Equal(t, "succeeded", storedCommand.Status)
	require.Equal(t, 1, storedCommand.MatchedCount)
	require.Equal(t, 1, storedCommand.ProcessedCount)
	require.Equal(t, 1, storedCommand.AffectedCount)
	require.Zero(t, storedCommand.SkippedCount)
	require.Equal(t, outlookRoot.ID, storedCommand.CheckpointResourceID)

	var outlookStored, hotmailStored MicrosoftResourceModel
	require.NoError(t, db.First(&outlookStored, outlookRoot.ID).Error)
	require.NoError(t, db.First(&hotmailStored, hotmailRoot.ID).Error)
	require.False(t, outlookStored.ForSale)
	require.True(t, hotmailStored.ForSale)

	replayed, reused, err := service.Submit(
		context.Background(),
		coreapp.AdminResourceBulkUnpublish,
		coreapp.AdminResourceBulkSelection{
			Mode: coreapp.AdminResourceBulkFilter,
			Filter: coreapp.AdminResourceBulkFilterValue{
				Suffix:  "@outlook.com",
				ForSale: boolPointer(true),
			},
		},
		9,
		"bulk-filter-key",
		"req-bulk-filter-replay",
		"/v1/admin/resources/unpublish",
	)
	require.NoError(t, err)
	require.True(t, reused)
	require.Equal(t, command.ID, replayed.ID)

	_, _, err = service.Submit(
		context.Background(),
		coreapp.AdminResourceBulkDelete,
		coreapp.AdminResourceBulkSelection{Mode: coreapp.AdminResourceBulkFilter},
		9,
		"bulk-filter-key",
		"req-bulk-filter-conflict",
		"/v1/admin/resources/delete",
	)
	require.ErrorIs(t, err, domain.ErrResourceIdempotencyConflict)

	var logs int64
	require.NoError(t, db.Table("operation_logs").
		Where("operation_type = ? AND resource_id = ?", "core.admin_resource.unpublish_bulk", "bulk:"+uintString(command.ID)).
		Count(&logs).Error)
	require.EqualValues(t, 1, logs)
}

func TestAdminResourceBulkFilterSnapshotsOwnerSearchMySQL(t *testing.T) {
	db := newCoreMySQLTestDB(t)
	insertAdminCommandUsers(t, db)
	resourceRepo := NewResourceRepo(db)
	validationRepo := NewResourceValidationRepo(db)
	validation := coreapp.NewResourceValidationUseCase(resourceRepo, validationRepo, adminCommandValidationQueue{}, nil)
	commands := coreapp.NewAdminResourceCommandService(NewAdminResourceRepo(db), validation, governanceinfra.NewOperationLogRepo(db))
	owners := adminCommandOwners()
	owner := owners.owners[1]
	owner.Nickname = "Alice Supplier"
	owners.owners[1] = owner
	commands.SetPorts(owners, nil, &adminCommandBindingPort{}, &adminCommandAllocationGuard{})
	queue := &adminBulkQueueStub{}
	service := coreapp.NewAdminResourceBulkService(NewAdminResourceBulkRepo(db), queue, commands)

	aliceRoot := &domain.EmailResource{Type: domain.ResourceTypeMicrosoft, OwnerUserID: 1}
	aliceResource := &domain.MicrosoftResource{
		EmailAddress: "owner-search-one@outlook.com", Password: "secret", Status: domain.MicrosoftStatusNormal, ForSale: true,
	}
	require.NoError(t, resourceRepo.CreateMicrosoft(context.Background(), aliceRoot, aliceResource))
	bobRoot := &domain.EmailResource{Type: domain.ResourceTypeMicrosoft, OwnerUserID: 2}
	bobResource := &domain.MicrosoftResource{
		EmailAddress: "owner-search-two@outlook.com", Password: "secret", Status: domain.MicrosoftStatusNormal, ForSale: true,
	}
	require.NoError(t, resourceRepo.CreateMicrosoft(context.Background(), bobRoot, bobResource))

	command, reused, err := service.Submit(
		context.Background(),
		coreapp.AdminResourceBulkUnpublish,
		coreapp.AdminResourceBulkSelection{
			Mode:   coreapp.AdminResourceBulkFilter,
			Filter: coreapp.AdminResourceBulkFilterValue{Search: "Alice Supplier"},
		},
		9,
		"bulk-owner-search-key",
		"req-bulk-owner-search",
		"/v1/admin/resources/unpublish",
	)
	require.NoError(t, err)
	require.False(t, reused)
	require.Equal(t, []uint{1}, command.Selection.Filter.OwnerIDs)

	// Execution uses the accepted snapshot even if the owner's display data
	// changes before the durable worker runs.
	owner.Nickname = "Renamed Supplier"
	owners.owners[1] = owner
	require.NoError(t, service.DispatchPending(context.Background(), 10))
	require.Len(t, queue.tasks, 1)
	require.NoError(t, service.Process(context.Background(), queue.tasks[0]))

	stored, err := service.Get(context.Background(), command.ID)
	require.NoError(t, err)
	require.Equal(t, "succeeded", stored.Status)
	require.Equal(t, []uint{1}, stored.Selection.Filter.OwnerIDs)
	require.Equal(t, 1, stored.MatchedCount)
	require.Equal(t, 1, stored.AffectedCount)

	var aliceStored, bobStored MicrosoftResourceModel
	require.NoError(t, db.First(&aliceStored, aliceRoot.ID).Error)
	require.NoError(t, db.First(&bobStored, bobRoot.ID).Error)
	require.False(t, aliceStored.ForSale)
	require.True(t, bobStored.ForSale)
}

func TestAdminResourceBulkSuccessfulPagesResetRetryBudgetMySQL(t *testing.T) {
	db := newCoreMySQLTestDB(t)
	insertAdminCommandUsers(t, db)
	repo := NewAdminResourceBulkRepo(db)

	for _, total := range []int{299, 300, 301} {
		t.Run(fmt.Sprintf("resources_%d", total), func(t *testing.T) {
			resourceIDs := make([]uint, total)
			base := uint(total * 1000)
			for i := range resourceIDs {
				resourceIDs[i] = base + uint(i+1)
			}
			command := coreapp.AdminResourceBulkCommand{
				OperatorUserID: 9,
				Action:         coreapp.AdminResourceBulkUnpublish,
				Selection: coreapp.AdminResourceBulkSelection{
					Mode:        coreapp.AdminResourceBulkIDs,
					ResourceIDs: resourceIDs,
				},
				SelectionFingerprint: fmt.Sprintf("%064x", total),
				IdempotencyKey:       fmt.Sprintf("bulk-page-boundary-%d", total),
				Status:               "queued",
				MaxAttempts:          3,
				ReasonCounts:         map[string]int64{},
			}
			created, err := repo.CreateWithLog(context.Background(), &command, nil)
			require.NoError(t, err)
			require.True(t, created)

			completedPages := 0
			for {
				now := time.Now().UTC()
				dispatchable, err := repo.ClaimDispatchable(
					context.Background(), 1, now.Add(-20*time.Minute), now.Add(-time.Hour),
				)
				require.NoError(t, err)
				require.Len(t, dispatchable, 1)
				running, claimed, err := repo.MarkRunning(context.Background(), command.ID, dispatchable[0].DispatchToken)
				require.NoError(t, err)
				require.True(t, claimed)
				require.Equal(t, 1, running.Attempts)

				candidates, err := repo.ListCandidateIDs(context.Background(), running, 100, now)
				require.NoError(t, err)
				checkpoint := running.CheckpointResourceID
				if len(candidates) > 0 {
					checkpoint = candidates[len(candidates)-1]
				}
				done := len(candidates) < 100
				require.NoError(t, repo.CompletePage(
					context.Background(), command.ID, running.ClaimToken, checkpoint,
					0, len(candidates), 0, len(candidates), running.ReasonCounts, done,
				))
				completedPages++

				stored, err := repo.FindByID(context.Background(), command.ID)
				require.NoError(t, err)
				require.Zero(t, stored.Attempts, "a successful page starts a fresh retry budget")
				if done {
					require.Equal(t, "succeeded", stored.Status)
					require.Equal(t, total, stored.ProcessedCount)
					break
				}
				require.Equal(t, "queued", stored.Status)
			}

			expectedPages := (total + 99) / 100
			if total%100 == 0 {
				expectedPages++
			}
			require.Equal(t, expectedPages, completedPages)
		})
	}
}

func TestAdminResourceBulkSerializesConcurrentIdempotentRequestsMySQL(t *testing.T) {
	db := newCoreMySQLTestDB(t)
	insertAdminCommandUsers(t, db)
	repo := NewAdminResourceBulkRepo(db)

	const workerCount = 10
	start := make(chan struct{})
	commands := make([]coreapp.AdminResourceBulkCommand, workerCount)
	createdByWorker := make([]bool, workerCount)
	errorsByWorker := make([]error, workerCount)
	var workers sync.WaitGroup
	for index := 0; index < workerCount; index++ {
		workers.Add(1)
		go func(worker int) {
			defer workers.Done()
			<-start
			requestID := fmt.Sprintf("req-bulk-concurrent-%03d", worker)
			command := coreapp.AdminResourceBulkCommand{
				OperatorUserID: 9,
				Action:         coreapp.AdminResourceBulkUnpublish,
				Selection: coreapp.AdminResourceBulkSelection{
					Mode: coreapp.AdminResourceBulkFilter,
					Filter: coreapp.AdminResourceBulkFilterValue{
						Suffix: "@outlook.com",
					},
				},
				SelectionFingerprint: strings.Repeat("e", 64),
				IdempotencyKey:       "bulk-concurrent-shared-key",
				Status:               "queued",
				MaxAttempts:          3,
				ReasonCounts:         map[string]int64{},
				RequestID:            requestID,
				Path:                 "/v1/admin/resources/unpublish",
			}
			createdByWorker[worker], errorsByWorker[worker] = repo.CreateWithLog(
				context.Background(),
				&command,
				&governancedomain.OperationLog{
					OperatorUserID: 9,
					OperationType:  "core.admin_resource.unpublish_bulk",
					ResourceType:   "microsoft_resource",
					ResourceID:     "filter",
					Path:           command.Path,
					Result:         "success",
					SafeSummary:    "Microsoft resource batch command accepted.",
					RequestID:      requestID,
				},
			)
			commands[worker] = command
		}(index)
	}
	close(start)
	workers.Wait()

	createdCount := 0
	commandIDs := make(map[uint64]struct{}, workerCount)
	for index, err := range errorsByWorker {
		require.NoError(t, err, "worker %d", index)
		if createdByWorker[index] {
			createdCount++
		}
		require.NotZero(t, commands[index].ID)
		commandIDs[commands[index].ID] = struct{}{}
	}
	require.Equal(t, 1, createdCount)
	require.Len(t, commandIDs, 1)

	var commandCount, logCount int64
	require.NoError(t, db.Model(&AdminResourceBulkCommandModel{}).
		Where("operator_user_id = ? AND idempotency_key = ?", 9, "bulk-concurrent-shared-key").
		Count(&commandCount).Error)
	require.NoError(t, db.Table("operation_logs").
		Where("operation_type = ?", "core.admin_resource.unpublish_bulk").
		Count(&logCount).Error)
	require.EqualValues(t, 1, commandCount)
	require.EqualValues(t, 1, logCount)
}

func TestAdminResourceBulkValidationTracksReusedAndInvalidResourcesMySQL(t *testing.T) {
	db := newCoreMySQLTestDB(t)
	insertAdminCommandUsers(t, db)
	resourceRepo := NewResourceRepo(db)
	validationRepo := NewResourceValidationRepo(db)
	validation := coreapp.NewResourceValidationUseCase(resourceRepo, validationRepo, adminCommandValidationQueue{}, nil)
	adminRepo := NewAdminResourceRepo(db)
	commands := coreapp.NewAdminResourceCommandService(adminRepo, validation, governanceinfra.NewOperationLogRepo(db))
	commands.SetPorts(adminCommandOwners(), nil, &adminCommandBindingPort{}, &adminCommandAllocationGuard{})
	queue := &adminBulkQueueStub{}
	service := coreapp.NewAdminResourceBulkService(NewAdminResourceBulkRepo(db), queue, commands)

	pendingRoot := &domain.EmailResource{Type: domain.ResourceTypeMicrosoft, OwnerUserID: 1}
	pending := &domain.MicrosoftResource{EmailAddress: "bulk-validation@outlook.com", Password: "secret", Status: domain.MicrosoftStatusPending}
	require.NoError(t, resourceRepo.CreateMicrosoft(context.Background(), pendingRoot, pending))
	disabledRoot := &domain.EmailResource{Type: domain.ResourceTypeMicrosoft, OwnerUserID: 1}
	disabled := &domain.MicrosoftResource{EmailAddress: "bulk-disabled@outlook.com", Password: "secret", Status: domain.MicrosoftStatusDisabled}
	require.NoError(t, resourceRepo.CreateMicrosoft(context.Background(), disabledRoot, disabled))

	command, _, err := service.Submit(
		context.Background(),
		coreapp.AdminResourceBulkValidate,
		coreapp.AdminResourceBulkSelection{
			Mode:        coreapp.AdminResourceBulkIDs,
			ResourceIDs: []uint{disabledRoot.ID, pendingRoot.ID, 999999},
		},
		9,
		"bulk-validation-key",
		"req-bulk-validation",
		"/v1/admin/resources/validations",
	)
	require.NoError(t, err)
	require.NoError(t, service.DispatchPending(context.Background(), 10))
	require.Len(t, queue.tasks, 1)
	require.NoError(t, service.Process(context.Background(), queue.tasks[0]))

	stored, err := service.Get(context.Background(), command.ID)
	require.NoError(t, err)
	require.Equal(t, "succeeded", stored.Status)
	require.Equal(t, 3, stored.MatchedCount)
	require.Equal(t, 3, stored.ProcessedCount)
	require.Equal(t, 1, stored.AffectedCount)
	require.Equal(t, 2, stored.SkippedCount)
	require.EqualValues(t, 1, stored.ReasonCounts["invalid_state"])
	require.EqualValues(t, 1, stored.ReasonCounts["not_found"])

	var jobs []ResourceValidationModel
	require.NoError(t, db.Where("resource_id = ?", pendingRoot.ID).Find(&jobs).Error)
	require.Len(t, jobs, 1)
	require.EqualValues(t, 1, jobs[0].ExpectedCredentialRevision)
}

func TestAdminResourceBulkPageRollsBackStateAndProgressTogetherMySQL(t *testing.T) {
	db := newCoreMySQLTestDB(t)
	insertAdminCommandUsers(t, db)
	resourceRepo := NewResourceRepo(db)
	validationRepo := NewResourceValidationRepo(db)
	validation := coreapp.NewResourceValidationUseCase(resourceRepo, validationRepo, adminCommandValidationQueue{}, nil)
	adminRepo := NewAdminResourceRepo(db)
	commands := coreapp.NewAdminResourceCommandService(adminRepo, validation, governanceinfra.NewOperationLogRepo(db))
	guard := &failSecondBulkAllocationGuard{}
	commands.SetPorts(adminCommandOwners(), nil, &adminCommandBindingPort{}, guard)
	queue := &adminBulkQueueStub{}
	service := coreapp.NewAdminResourceBulkService(NewAdminResourceBulkRepo(db), queue, commands)

	firstRoot := &domain.EmailResource{Type: domain.ResourceTypeMicrosoft, OwnerUserID: 1}
	first := &domain.MicrosoftResource{EmailAddress: "bulk-page-first@outlook.com", Password: "secret", Status: domain.MicrosoftStatusNormal, ForSale: true}
	require.NoError(t, resourceRepo.CreateMicrosoft(context.Background(), firstRoot, first))
	secondRoot := &domain.EmailResource{Type: domain.ResourceTypeMicrosoft, OwnerUserID: 1}
	second := &domain.MicrosoftResource{EmailAddress: "bulk-page-second@outlook.com", Password: "secret", Status: domain.MicrosoftStatusNormal, ForSale: true}
	require.NoError(t, resourceRepo.CreateMicrosoft(context.Background(), secondRoot, second))

	command, _, err := service.Submit(
		context.Background(),
		coreapp.AdminResourceBulkDelete,
		coreapp.AdminResourceBulkSelection{Mode: coreapp.AdminResourceBulkIDs, ResourceIDs: []uint{firstRoot.ID, secondRoot.ID}},
		9,
		"bulk-page-rollback-key",
		"req-bulk-page-rollback",
		"/v1/admin/resources/delete",
	)
	require.NoError(t, err)
	require.NoError(t, service.DispatchPending(context.Background(), 10))
	require.Len(t, queue.tasks, 1)
	require.NoError(t, service.Process(context.Background(), queue.tasks[0]), "retryable page failure is persisted for the durable dispatcher")

	stored, err := service.Get(context.Background(), command.ID)
	require.NoError(t, err)
	require.Equal(t, "queued", stored.Status)
	require.Zero(t, stored.CheckpointResourceID)
	require.Zero(t, stored.ProcessedCount)
	require.Zero(t, stored.AffectedCount)
	require.Zero(t, stored.SkippedCount)

	var firstStored, secondStored MicrosoftResourceModel
	require.NoError(t, db.First(&firstStored, firstRoot.ID).Error)
	require.NoError(t, db.First(&secondStored, secondRoot.ID).Error)
	require.Equal(t, string(domain.MicrosoftStatusNormal), firstStored.Status)
	require.Equal(t, string(domain.MicrosoftStatusNormal), secondStored.Status)
	require.True(t, firstStored.ForSale)
	require.True(t, secondStored.ForSale)

	require.NoError(t, service.DispatchPending(context.Background(), 10))
	require.Len(t, queue.tasks, 2)
	require.NoError(t, service.Process(context.Background(), queue.tasks[1]))

	stored, err = service.Get(context.Background(), command.ID)
	require.NoError(t, err)
	require.Equal(t, "succeeded", stored.Status)
	require.Zero(t, stored.Attempts)
	require.Equal(t, 2, stored.ProcessedCount)
	require.Equal(t, 2, stored.AffectedCount)

	require.NoError(t, db.First(&firstStored, firstRoot.ID).Error)
	require.NoError(t, db.First(&secondStored, secondRoot.ID).Error)
	require.Equal(t, string(domain.MicrosoftStatusDeleted), firstStored.Status)
	require.Equal(t, string(domain.MicrosoftStatusDeleted), secondStored.Status)
	require.False(t, firstStored.ForSale)
	require.False(t, secondStored.ForSale)
}

func boolPointer(value bool) *bool { return &value }

func uintString(value uint64) string {
	return fmt.Sprintf("%d", value)
}
