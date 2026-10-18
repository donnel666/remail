package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/donnel666/remail/internal/daum/domain"
	"github.com/donnel666/remail/internal/daum/infra"
	iamdomain "github.com/donnel666/remail/internal/iam/domain"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

type ownedBulkQueue struct {
	failure  error
	attempts int
	tasks    []*asynq.Task
}

func (q *ownedBulkQueue) EnqueueContext(_ context.Context, task *asynq.Task, _ ...asynq.Option) (*asynq.TaskInfo, error) {
	q.attempts++
	if q.failure != nil {
		return nil, q.failure
	}
	q.tasks = append(q.tasks, task)
	return &asynq.TaskInfo{}, nil
}

type bulkReadFailureHook struct {
	enabled  bool
	prefix   string
	failures int
}

func (*bulkReadFailureHook) DialHook(next redis.DialHook) redis.DialHook { return next }
func (*bulkReadFailureHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}
func (h *bulkReadFailureHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, command redis.Cmder) error {
		if h.enabled && command.Name() == "get" && strings.HasPrefix(fmt.Sprint(command.Args()[1]), h.prefix) {
			h.failures++
			return errors.New("synthetic post-reservation read failure private-token")
		}
		return next(ctx, command)
	}
}

func ownedBulkFixture(t *testing.T) (*Module, *miniredis.Miniredis, *ownedBulkQueue) {
	t.Helper()
	module := ownedTestModule(t)
	server := miniredis.RunT(t)
	module.Service.Redis = redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = module.Service.Redis.Close() })
	queue := &ownedBulkQueue{}
	module.Queue, module.Service.Queue = queue, queue
	return module, server, queue
}

func TestOwnedBulkKeepsReceiptAfterDispatchFailureAndRecoversWithoutReacceptance(t *testing.T) {
	for _, failure := range []string{"queue", "payload_read", "status_read"} {
		t.Run(failure, func(t *testing.T) {
			module, server, queue := ownedBulkFixture(t)
			ctx := context.Background()
			old, _, err := module.Service.ImportLine(ctx, 7, domain.ImportLine{Email: "before@daum.net", AppPassword: "fixture-secret"})
			require.NoError(t, err)
			foreign, _, err := module.Service.ImportLine(ctx, 8, domain.ImportLine{Email: "foreign@daum.net", AppPassword: "fixture-secret"})
			require.NoError(t, err)
			readFailure := &bulkReadFailureHook{enabled: failure != "queue", prefix: "remail:daum:bulk:payload:"}
			if failure == "status_read" {
				readFailure.prefix = "remail:daum:bulk:status:"
			}
			module.Service.Redis.(*redis.Client).AddHook(readFailure)
			if failure == "queue" {
				queue.failure = errors.New("synthetic queue handoff failure private-token hidden@hanmail.net")
			}
			var logs bytes.Buffer
			previous := slog.Default()
			slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
			t.Cleanup(func() { slog.SetDefault(previous) })
			router := ownedTestRouter(module, 7, iamdomain.RoleUser)
			body := `{"selection":{"mode":"filter","filter":{"forSale":false}}}`
			response := ownedRequest(router, http.MethodPost, "/v1/daum/resources/bulk/delete", body, "delete-all")
			require.Equal(t, http.StatusAccepted, response.Code, response.Body.String())
			var accepted daumBulkResponse
			require.NoError(t, json.Unmarshal(response.Body.Bytes(), &accepted))
			require.NotEmpty(t, accepted.TaskID)
			require.Equal(t, "queued", accepted.Status)
			assertOwnedResponseHasNoPrivateDetails(t, response.Body.String())
			require.Empty(t, queue.tasks)
			if failure != "queue" {
				require.Positive(t, readFailure.failures)
			}
			pending, err := module.Service.Redis.SMembers(ctx, "remail:daum:bulk:pending").Result()
			require.NoError(t, err)
			require.Len(t, pending, 1, "accepted work remains available to the periodic dispatcher")
			response = ownedRequest(router, http.MethodPost, "/v1/daum/resources/bulk/delete", body, "delete-all")
			require.Equal(t, http.StatusAccepted, response.Code, response.Body.String())
			var replay daumBulkResponse
			require.NoError(t, json.Unmarshal(response.Body.Bytes(), &replay))
			require.Equal(t, accepted.TaskID, replay.TaskID)
			sequence, err := server.Get("remail:daum:bulk:sequence")
			require.NoError(t, err)
			require.Equal(t, "1", sequence)
			attempts := queue.attempts
			response = ownedRequest(router, http.MethodPost, "/v1/daum/resources/bulk/delete", `{"selection":{"mode":"filter","filter":{"forSale":true}}}`, "delete-all")
			require.Equal(t, http.StatusConflict, response.Code)
			require.Equal(t, attempts, queue.attempts)
			require.Contains(t, logs.String(), "Daum bulk accepted; queue dispatch deferred")
			require.Contains(t, logs.String(), accepted.TaskID)
			for _, secret := range []string{"private-token", "hidden@hanmail.net", "fixture-secret", "synthetic queue", "synthetic post-reservation"} {
				require.NotContains(t, logs.String(), secret)
			}
			readFailure.enabled, queue.failure = false, nil
			later, _, err := module.Service.ImportLine(ctx, 7, domain.ImportLine{Email: "after-acceptance@daum.net", AppPassword: "fixture-secret"})
			require.NoError(t, err)
			require.NoError(t, module.Service.DispatchPendingBulk(ctx))
			require.Len(t, queue.tasks, 1)
			var task infra.BulkTask
			require.NoError(t, json.Unmarshal(queue.tasks[0].Payload(), &task))
			require.Equal(t, accepted.TaskID, task.TaskID)
			require.NoError(t, module.Service.ProcessBulk(ctx, task))
			for _, id := range []uint{old, later} {
				row, err := module.Service.GetResource(ctx, id, nil)
				require.NoError(t, err)
				require.Equal(t, domain.StatusDeleted, row.Status)
			}
			row, err := module.Service.GetResource(ctx, foreign, nil)
			require.NoError(t, err)
			require.NotEqual(t, domain.StatusDeleted, row.Status)
			response = ownedRequest(router, http.MethodPost, "/v1/daum/resources/bulk/delete", body, "delete-all")
			require.Equal(t, http.StatusAccepted, response.Code)
			require.NoError(t, json.Unmarshal(response.Body.Bytes(), &replay))
			require.Equal(t, accepted.TaskID, replay.TaskID)
			require.Equal(t, "succeeded", replay.Status)
			require.Equal(t, 2, replay.Affected)
			require.NoError(t, module.Service.DispatchPendingBulk(ctx))
			require.Len(t, queue.tasks, 1, "finished work is not reaccepted or enqueued on replay")
			response = ownedRequest(ownedTestRouter(module, 8, iamdomain.RoleUser), http.MethodGet, "/v1/daum/resources/bulk-tasks/"+accepted.TaskID, "", "")
			require.Equal(t, http.StatusNotFound, response.Code)
		})
	}
}

func TestOwnedBulkDoesNotAcceptUnconfirmedReservationOrWrongOwnerReceipt(t *testing.T) {
	module, server, queue := ownedBulkFixture(t)
	router := ownedTestRouter(module, 7, iamdomain.RoleUser)
	body := `{"selection":{"mode":"ids","resourceIds":[1]}}`
	server.SetError("synthetic storage unavailable")
	response := ownedRequest(router, http.MethodPost, "/v1/daum/resources/bulk/validate", body, "reservation-error")
	require.Equal(t, http.StatusInternalServerError, response.Code)
	require.Zero(t, queue.attempts)
	require.Empty(t, server.Keys())
	server.SetError("")
	response = ownedRequest(router, http.MethodPost, "/v1/daum/resources/bulk/validate", body, "owner-check")
	require.Equal(t, http.StatusAccepted, response.Code)
	var accepted daumBulkResponse
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &accepted))
	status, err := module.Service.GetBulkStatus(context.Background(), accepted.TaskID, 7, false)
	require.NoError(t, err)
	other := uint(8)
	status.OwnerID = &other
	raw, err := json.Marshal(status)
	require.NoError(t, err)
	require.NoError(t, module.Service.Redis.Set(context.Background(), infra.BulkStatusKey(status.TaskID), raw, 0).Err())
	attempts := queue.attempts
	response = ownedRequest(router, http.MethodPost, "/v1/daum/resources/bulk/validate", body, "owner-check")
	require.Equal(t, http.StatusNotFound, response.Code, "Lua-returned receipts must preserve the owner authorization check")
	require.Equal(t, attempts, queue.attempts)
}

func setOwnedBulkSetting(t *testing.T, key, value string) {
	t.Helper()
	previous, existed := runtimeconfig.Snapshot()[key]
	runtimeconfig.Set(key, value)
	t.Cleanup(func() {
		if existed {
			runtimeconfig.Set(key, previous)
		} else {
			runtimeconfig.Delete(key)
		}
	})
}

func TestOwnedBulkValidationUsesDedicatedConfiguredLimits(t *testing.T) {
	for _, tc := range []struct {
		name, action, ordinaryLimit, validationLimit string
		count, wantStatus                            int
	}{
		{"validation_1001", "validate", "1000", "10000", 1001, http.StatusAccepted},
		{"publish_1001", "publish", "1000", "10000", 1001, http.StatusBadRequest},
		{"delete_1001", "delete", "1000", "10000", 1001, http.StatusBadRequest},
		{"validation_at_limit", "validate", "2", "3", 3, http.StatusAccepted},
		{"validation_over_limit", "validate", "2", "3", 4, http.StatusBadRequest},
		{"publish_at_limit", "publish", "2", "3", 2, http.StatusAccepted},
		{"publish_over_limit", "publish", "2", "3", 3, http.StatusBadRequest},
		{"delete_at_limit", "delete", "2", "3", 2, http.StatusAccepted},
		{"delete_over_limit", "delete", "2", "3", 3, http.StatusBadRequest},
		{"validation_hard_cap", "validate", "1000", "20000", 10001, http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setOwnedBulkSetting(t, "admin_resource_bulk_max_ids", tc.ordinaryLimit)
			setOwnedBulkSetting(t, "resource_validation_max_ids", tc.validationLimit)
			module, _, queue := ownedBulkFixture(t)
			ids := make([]uint, tc.count)
			for i := range ids {
				ids[i] = uint(i + 1)
			}
			body, err := json.Marshal(daumBatchRequest{Selection: &infra.BulkSelection{Mode: "ids", ResourceIDs: ids}})
			require.NoError(t, err)
			response := ownedRequest(ownedTestRouter(module, 7, iamdomain.RoleSupplier), http.MethodPost, "/v1/daum/resources/bulk/"+tc.action, string(body), "configured-limit")
			require.Equal(t, tc.wantStatus, response.Code, response.Body.String())
			if tc.wantStatus != http.StatusAccepted {
				require.Zero(t, queue.attempts)
				return
			}
			require.Len(t, queue.tasks, 1)
			var task infra.BulkTask
			require.NoError(t, json.Unmarshal(queue.tasks[0].Payload(), &task))
			require.Equal(t, tc.action == "validate", task.ValidationBatch)
			require.EqualValues(t, 7, *task.OwnerID)
			require.Len(t, task.Selection.ResourceIDs, tc.count)
		})
	}
}

func TestOwnedBulkValidationKeepsScopeAfterJSONAndUsesValidationPageSize(t *testing.T) {
	for _, mode := range []string{"filter", "ids"} {
		t.Run(mode, func(t *testing.T) {
			setOwnedBulkSetting(t, "validation_batch_page_size", "1")
			module, _, queue := ownedBulkFixture(t)
			ctx := context.Background()
			var ids []uint
			for _, owner := range []uint{7, 8} {
				id, _, err := module.Service.ImportLine(ctx, owner, domain.ImportLine{Email: fmt.Sprintf("scope%d@daum.net", owner), AppPassword: "fixture"})
				require.NoError(t, err)
				ids = append(ids, id)
			}
			selection := infra.BulkSelection{Mode: mode}
			if mode == "ids" {
				selection.ResourceIDs = ids
			}
			body, err := json.Marshal(daumBatchRequest{Selection: &selection})
			require.NoError(t, err)
			router := ownedTestRouter(module, 7, iamdomain.RoleUser)
			response := ownedRequest(router, http.MethodPost, "/v1/daum/resources/bulk/validate", string(body), "validate-scope")
			require.Equal(t, http.StatusAccepted, response.Code, response.Body.String())
			var task infra.BulkTask
			for index := 0; index < len(queue.tasks); index++ {
				require.Less(t, index, 3)
				require.NoError(t, json.Unmarshal(queue.tasks[index].Payload(), &task))
				require.True(t, task.ValidationBatch)
				require.False(t, task.Selection.Filter.MainAddressOnly)
				require.NoError(t, module.Service.ProcessBulk(ctx, task))
			}
			status, err := module.Service.GetBulkStatus(ctx, task.TaskID, 7, false)
			require.NoError(t, err)
			require.Equal(t, "succeeded", status.Status)
			require.Equal(t, 1, status.Affected)
			if mode == "ids" {
				require.Len(t, queue.tasks, 2, "the configured one-row validation page size is respected")
				require.Equal(t, 1, status.Skipped)
			} else {
				require.Equal(t, 1, status.Requested)
				require.Zero(t, status.Skipped)
			}
			for i, id := range ids {
				row, err := module.Service.GetResource(ctx, id, nil)
				require.NoError(t, err)
				require.EqualValues(t, 2-i, row.ValidationGeneration)
			}
			response = ownedRequest(router, http.MethodPost, "/v1/daum/resources/bulk/validate", `{"selection":{"mode":"filter","filter":{"ownerId":8}}}`, "forged-owner")
			require.Equal(t, http.StatusBadRequest, response.Code)
		})
	}
}
