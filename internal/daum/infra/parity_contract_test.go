package infra

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	daumapp "github.com/donnel666/remail/internal/daum/app"
	"github.com/donnel666/remail/internal/daum/domain"
	"github.com/donnel666/remail/internal/daum/infra/protocol"
	"github.com/donnel666/remail/internal/platform"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"github.com/hibiken/asynq"
	"github.com/stretchr/testify/require"
)

func TestDaumCombinedEditRollsBackEveryFieldAndCanUnlink(t *testing.T) {
	s, _ := newDaumAsyncTestService(t)
	ctx := context.Background()
	id, _, err := s.ImportLine(ctx, 7, domain.ImportLine{Email: "typo@daum.net", Password: "old-login", AppPassword: "old-app", KakaoEmail: "keep@kakao.com", PickupURL: "https://remail.test/pickup?email=aux@example.test&token=fixture"})
	require.NoError(t, err)
	canonical, appPassword, badURL, sale, quality := "correct@daum.net", "new-app", "invalid", true, 99
	cmd := Command{ResourceID: id, Version: 1, Action: "edit", Email: &canonical, AppPassword: &appPassword, PickupURL: &badURL, ForSale: &sale, QualityScore: &quality, OperatorUserID: 7, IdempotencyKey: "combined"}
	_, err = s.ExecuteCommand(ctx, cmd)
	require.Error(t, err)
	var before Resource
	require.NoError(t, s.DB.First(&before, id).Error)
	require.Equal(t, "typo@daum.net", before.EmailAddress)
	require.Equal(t, "old-app", before.AppPassword)
	require.False(t, before.ForSale)
	require.Zero(t, before.QualityScore)
	aliases, err := s.ListAliases(ctx, id)
	require.NoError(t, err)
	require.Equal(t, "typo@hanmail.net", aliases[0].EmailAddress)
	cmd.PickupURL, cmd.PickupURLSet = nil, true
	result, err := s.ExecuteCommand(ctx, cmd)
	require.NoError(t, err)
	require.True(t, result.Changed)
	var after Resource
	require.NoError(t, s.DB.First(&after, id).Error)
	require.Equal(t, canonical, after.EmailAddress)
	require.Equal(t, appPassword, after.AppPassword)
	require.Empty(t, after.Password, "old identity login password must never follow the corrected account")
	require.Equal(t, "keep@kakao.com", after.KakaoEmail)
	require.Zero(t, after.QualityScore, "credentials supersede a stale manual quality value")
	require.True(t, after.ForSale)
	var count int64
	require.NoError(t, s.DB.Model(&BindingMailbox{}).Where("resource_id = ?", id).Count(&count).Error)
	require.Zero(t, count)
	replayed, err := s.ExecuteCommand(ctx, cmd)
	require.NoError(t, err)
	require.True(t, replayed.Reused)
	require.Equal(t, result.Version, replayed.Version)
	cmd.PickupURLSet = false
	_, err = s.ExecuteCommand(ctx, cmd)
	require.ErrorIs(t, err, domain.ErrImportConflict, "absent and explicit null have different idempotency fingerprints")
}

func TestDaumManualValidationNeedsNoVersionAndPreservesDiagnostics(t *testing.T) {
	s, _ := newDaumAsyncTestService(t)
	ctx := context.Background()
	id, _, err := s.ImportLine(ctx, 7, domain.ImportLine{Email: "manual@daum.net", AppPassword: "fixture"})
	require.NoError(t, err)
	checked := s.Now().Add(-time.Hour).Truncate(time.Second)
	require.NoError(t, s.DB.Model(&Resource{}).Where("id = ?", id).Updates(map[string]any{"status": domain.StatusAbnormal, "quality_score": 66, "last_checked_at": checked, "last_safe_error": "old", "validation_failures": 3}).Error)
	result, err := s.ExecuteCommand(ctx, Command{ResourceID: id, Action: "validate", OperatorUserID: 7, IdempotencyKey: "manual"})
	require.NoError(t, err)
	require.Equal(t, domain.StatusPending, result.Status)
	row, err := s.GetResource(ctx, id, nil)
	require.NoError(t, err)
	require.Equal(t, 66, row.QualityScore)
	require.True(t, checked.Equal(*row.LastCheckedAt))
	require.Zero(t, row.ValidationFailures)
	require.Empty(t, row.LastSafeError)
}

func TestDaumDeletedImportCanMoveOwnerWithoutDroppingShortFormatFields(t *testing.T) {
	s, _ := newDaumAsyncTestService(t)
	ctx := context.Background()
	id, _, err := s.ImportLine(ctx, 7, domain.ImportLine{Email: "restore@daum.net", Password: "preserved", AppPassword: "fixture", KakaoEmail: "keep@kakao.com", BindingAddress: "aux@example.test"})
	require.NoError(t, err)
	require.NoError(t, s.SetStatus(ctx, id, nil, domain.StatusDeleted))
	restored, outcome, err := s.ImportLine(ctx, 8, domain.ImportLine{Email: "restore@hanmail2.net", AppPassword: "new-fixture"})
	require.NoError(t, err)
	require.Equal(t, id, restored)
	require.Equal(t, "restored", outcome)
	var row Resource
	require.NoError(t, s.DB.First(&row, id).Error)
	require.EqualValues(t, 8, row.OwnerUserID)
	require.Equal(t, "preserved", row.Password)
	require.Equal(t, "keep@kakao.com", row.KakaoEmail)
	var binding BindingMailbox
	require.NoError(t, s.DB.Where("resource_id = ?", id).Take(&binding).Error)
	require.EqualValues(t, 8, binding.OwnerUserID)
	var root resourceRoot
	require.NoError(t, s.DB.First(&root, id).Error)
	require.EqualValues(t, 8, root.OwnerUserID)
	_, _, err = s.ImportLine(ctx, 7, domain.ImportLine{Email: row.EmailAddress, AppPassword: "other"})
	require.ErrorIs(t, err, domain.ErrResourceConflict)
}

func TestDaumBulkNoopAndPageRollbackKeepAccurateProgress(t *testing.T) {
	s, _ := newDaumAsyncTestService(t)
	ctx := context.Background()
	q := setupBulk(t, s)
	var ids []uint
	for i := 0; i < 3; i++ {
		id, _, err := s.ImportLine(ctx, 7, domain.ImportLine{Email: fmt.Sprintf("page%d@daum.net", i), AppPassword: "fixture"})
		require.NoError(t, err)
		ids = append(ids, id)
	}
	require.NoError(t, s.SetForSale(ctx, ids[0], nil, true))
	status, err := s.SubmitBulk(ctx, "publish", BulkSelection{Mode: "ids", ResourceIDs: ids}, 7, nil, "atomic-page", "request")
	require.NoError(t, err)
	require.NoError(t, s.DB.Exec(fmt.Sprintf("CREATE TRIGGER fail_page BEFORE UPDATE ON daum_resources WHEN NEW.id = %d AND NEW.for_sale = 1 BEGIN SELECT RAISE(ABORT, 'page infrastructure failure'); END", ids[2])).Error)
	var task BulkTask
	require.NoError(t, json.Unmarshal(q.next().Payload(), &task))
	require.Error(t, s.ProcessBulk(ctx, task))
	row, err := s.GetResource(ctx, ids[1], nil)
	require.NoError(t, err)
	require.False(t, row.ForSale, "a later row failure must roll back the complete page")
	require.NoError(t, s.DB.Exec("DROP TRIGGER fail_page").Error)
	require.NoError(t, s.ProcessBulk(ctx, task))
	status, err = s.GetBulkStatus(ctx, status.TaskID, 7, true)
	require.NoError(t, err)
	require.Equal(t, 2, status.Affected)
	require.Equal(t, 1, status.Skipped)
	require.Equal(t, 1, status.ReasonCounts["already_target"])
	require.Equal(t, 3, status.Processed)
}

func TestDaumSynchronousDisableIsIdempotentWithoutRedis(t *testing.T) {
	s, _ := newDaumAsyncTestService(t)
	ctx := context.Background()
	id, _, err := s.ImportLine(ctx, 7, domain.ImportLine{Email: "disable@daum.net", AppPassword: "fixture"})
	require.NoError(t, err)
	first, err := s.DisableBatch(ctx, []uint{id, id}, 7, "disable", "request")
	require.NoError(t, err)
	require.Equal(t, 1, first.Affected)
	row, err := s.GetResource(ctx, id, nil)
	require.NoError(t, err)
	require.Equal(t, domain.StatusDisabled, row.Status)
	result, err := s.DisableBatch(ctx, []uint{id}, 7, "disable", "request")
	require.NoError(t, err)
	require.Equal(t, first, result)
	result, err = s.DisableBatch(ctx, []uint{id}, 7, "another-disable", "request")
	require.NoError(t, err)
	require.Zero(t, result.Affected)
	require.Equal(t, []BulkReasonCount{{Reason: "already_target", Count: 1}}, result.ReasonCounts)
}

func TestDaumBulkLimitsShareMicrosoftRuntimeConfiguration(t *testing.T) {
	runtimeconfig.Set("admin_resource_bulk_max_ids", "2")
	t.Cleanup(func() { runtimeconfig.Delete("admin_resource_bulk_max_ids") })
	_, err := normalizeBulk(BulkSelection{Mode: "ids", ResourceIDs: []uint{1, 2, 3}})
	require.ErrorIs(t, err, domain.ErrInvalidResource)
	_, err = normalizeBulk(BulkSelection{Mode: "ids", ResourceIDs: []uint{1, 2}})
	require.NoError(t, err)
}

func TestDaumDedicatedValidationBatchUsesSeparateLimitAndPageSetting(t *testing.T) {
	runtimeconfig.Set("admin_resource_bulk_max_ids", "2")
	runtimeconfig.Set("resource_validation_max_ids", "3")
	runtimeconfig.Set("validation_batch_page_size", "2")
	t.Cleanup(func() {
		runtimeconfig.Delete("admin_resource_bulk_max_ids")
		runtimeconfig.Delete("resource_validation_max_ids")
		runtimeconfig.Delete("validation_batch_page_size")
	})
	s, _ := newDaumAsyncTestService(t)
	ctx := context.Background()
	q := setupBulk(t, s)
	var ids []uint
	for i := 0; i < 3; i++ {
		id, _, err := s.ImportLine(ctx, 7, domain.ImportLine{Email: fmt.Sprintf("validation-page%d@daum.net", i), AppPassword: "fixture"})
		require.NoError(t, err)
		ids = append(ids, id)
	}
	selection := BulkSelection{Mode: "ids", ResourceIDs: ids}
	_, err := s.SubmitBulk(ctx, "validate", selection, 7, nil, "generic", "request")
	require.ErrorIs(t, err, domain.ErrInvalidResource)
	status, err := s.SubmitValidations(ctx, selection, 7, nil, "dedicated", "request")
	require.NoError(t, err)
	var task BulkTask
	require.NoError(t, json.Unmarshal(q.next().Payload(), &task))
	require.NoError(t, s.ProcessBulk(ctx, task))
	status, err = s.GetBulkStatus(ctx, status.TaskID, 7, true)
	require.NoError(t, err)
	require.Equal(t, 2, status.Processed)
	require.Equal(t, "running", status.Status)
	drainBulk(t, s, q)
	status, err = s.GetBulkStatus(ctx, status.TaskID, 7, true)
	require.NoError(t, err)
	require.Equal(t, 3, status.Processed)
	require.Equal(t, "succeeded", status.Status)
}

type dispatchCallbackQueue struct {
	callback func(context.Context, *asynq.Task) error
}

func (q dispatchCallbackQueue) EnqueueContext(ctx context.Context, task *asynq.Task, _ ...asynq.Option) (*asynq.TaskInfo, error) {
	return nil, q.callback(ctx, task)
}

func TestDaumValidationEnqueueStatusDoesNotOverwriteImmediateDeferral(t *testing.T) {
	for _, immediate := range []bool{false, true} {
		t.Run(fmt.Sprint(immediate), func(t *testing.T) {
			s, _ := newDaumAsyncTestService(t)
			ctx := context.Background()
			id, _, err := s.ImportLine(ctx, 7, domain.ImportLine{Email: "queued@daum.net", AppPassword: "fixture"})
			require.NoError(t, err)
			s.Proxies = nil
			queue := dispatchCallbackQueue{callback: func(ctx context.Context, task *asynq.Task) error {
				if !immediate {
					return nil
				}
				payload, err := daumapp.DecodeValidationTask(task)
				if err != nil {
					return err
				}
				return s.ProcessValidation(ctx, payload)
			}}
			_, err = s.DispatchPendingValidations(ctx, queue, 10)
			require.NoError(t, err)
			row, err := s.GetResource(ctx, id, nil)
			require.NoError(t, err)
			if immediate {
				require.Equal(t, domain.StatusPending, row.Status)
			} else {
				require.Equal(t, domain.StatusValidating, row.Status)
			}
		})
	}
}

func TestDaumOlderDispatcherCannotOverwriteANewerDuplicateReservation(t *testing.T) {
	s, _ := newDaumAsyncTestService(t)
	ctx := context.Background()
	id, _, err := s.ImportLine(ctx, 7, domain.ImportLine{Email: "dispatch-race@daum.net", AppPassword: "fixture"})
	require.NoError(t, err)
	s.Proxies = nil
	queue := dispatchCallbackQueue{callback: func(ctx context.Context, task *asynq.Task) error {
		payload, err := daumapp.DecodeValidationTask(task)
		if err != nil {
			return err
		}
		if err := s.ProcessValidation(ctx, payload); err != nil {
			return err
		}
		_, err = s.DispatchPendingValidations(ctx, busyDuplicateQueue{}, 1)
		return err
	}}
	_, err = s.DispatchPendingValidations(ctx, queue, 1)
	require.NoError(t, err)
	row, err := s.GetResource(ctx, id, nil)
	require.NoError(t, err)
	require.Equal(t, domain.StatusPending, row.Status)
}

func TestDaumValidationDispatcherUsesSharedConfiguredWindow(t *testing.T) {
	runtimeconfig.Set("validation_dispatch_maximum", "129")
	t.Cleanup(func() { runtimeconfig.Delete("validation_dispatch_maximum") })
	s, _ := newDaumAsyncTestService(t)
	ctx := context.Background()
	for i := 0; i < 130; i++ {
		_, _, err := s.ImportLine(ctx, 7, domain.ImportLine{Email: fmt.Sprintf("window%d@daum.net", i), AppPassword: "fixture"})
		require.NoError(t, err)
	}
	q := &daumQueueStub{}
	accepted, err := s.DispatchPendingValidations(ctx, q, 0)
	require.NoError(t, err)
	require.Equal(t, 129, accepted)
	require.Len(t, q.tasks, 129)
	s.BackgroundExecution = closedDaumBackgroundGate{}
	accepted, err = s.DispatchPendingValidations(ctx, q, 0)
	require.NoError(t, err)
	require.Zero(t, accepted)
	require.Len(t, q.tasks, 129)
}

type closedDaumBackgroundGate struct{}

func (closedDaumBackgroundGate) TryAcquire() (func(), bool) { return nil, false }
func (closedDaumBackgroundGate) Snapshot() platform.BackgroundLoadSnapshot {
	return platform.BackgroundLoadSnapshot{}
}

func TestDaumHistoryTimeoutSettingsMatchMicrosoft(t *testing.T) {
	runtimeconfig.Set("imap_full_history_timeout_minutes", "45")
	runtimeconfig.Set("project_history_timeout_minutes", "60")
	t.Cleanup(func() {
		runtimeconfig.Delete("imap_full_history_timeout_minutes")
		runtimeconfig.Delete("project_history_timeout_minutes")
	})
	require.Equal(t, 45*time.Minute, historyMailboxTimeout())
	require.Equal(t, 61*time.Minute, historyRunLease())
	runtimeconfig.Set("imap_full_history_timeout_minutes", "180")
	require.Equal(t, 2*time.Hour, historyMailboxTimeout())
}

func TestDaumHistoryStreamRetryResetsDiscardedEvidence(t *testing.T) {
	s := newHistoryTestService(t)
	resource, task := historyTestResource(t, s, "retry@daum.net", domain.StatusIdentifying)
	require.NoError(t, s.processHistory(context.Background(), task, func(_ context.Context, _ uint, _ uint64, request protocol.FetchRequest) (protocol.FetchResult, error) {
		require.NoError(t, request.OnMessages([]protocol.Message{historyMessage("discarded", resource.EmailAddress, s.Now())}))
		require.NotNil(t, request.OnReset)
		request.OnReset()
		return protocol.FetchResult{Complete: true}, nil
	}))
	var count int64
	require.NoError(t, s.DB.Model(&historyTestUsage{}).Count(&count).Error)
	require.Zero(t, count)
}

func TestDaumProjectInfrastructureRecoveryPreservesCheckpoint(t *testing.T) {
	s := newHistoryTestService(t)
	ctx := context.Background()
	resource, _ := historyTestResource(t, s, "checkpoint@daum.net", domain.StatusNormal)
	require.NoError(t, s.ScheduleProjectHistory(ctx, 10, "checkpoint"))
	require.NoError(t, s.DB.Model(&ProjectHistoryState{}).Where("project_id = 10").Updates(map[string]any{"after_id": resource.ID, "through_id": resource.ID + 5, "failures": 2}).Error)
	require.NoError(t, s.ReleaseProjectHistoryAssignment(ctx, ProjectHistoryTask{ProjectID: 10, Generation: 1, AfterID: resource.ID}))
	var state ProjectHistoryState
	require.NoError(t, s.DB.First(&state, 10).Error)
	require.EqualValues(t, 2, state.Generation)
	require.Equal(t, resource.ID, state.AfterID)
	require.Equal(t, resource.ID+5, state.ThroughID)
	require.Equal(t, 2, state.Failures)
	require.Equal(t, "pending", state.Status)
	require.True(t, errors.Is(s.processProjectHistory(ctx, ProjectHistoryTask{ProjectID: 10, Generation: 1}, nil), domain.ErrInvalidClaim))
}
