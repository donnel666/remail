package infra

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	governanceapp "github.com/donnel666/remail/internal/governance/app"
	"github.com/redis/go-redis/v9"
)

// Proto owns execution and its Redis cursor. Governance only reads safe progress.
func (r *AdminTaskViewRepo) findProtoBulkTask(ctx context.Context, id uint64) (*governanceapp.AdminTaskView, error) {
	if r.redis == nil {
		return nil, errors.New("proto bulk task store is unavailable")
	}
	ref := governanceapp.AdminTaskRef{Source: governanceapp.AdminTaskSourceProtoBulk, ID: id}
	raw, err := r.redis.Get(ctx, "remail:proto:bulk:status:proto_bulk:"+strconv.FormatUint(id, 10)).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, governanceapp.ErrAdminTaskNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read Proto bulk task: %w", err)
	}
	var status struct {
		TaskID       string           `json:"taskId"`
		Action       string           `json:"action"`
		Status       string           `json:"status"`
		Attempts     int              `json:"attempts"`
		MaxAttempts  int              `json:"maxAttempts"`
		Requested    int64            `json:"requested"`
		Processed    int64            `json:"processed"`
		Affected     int64            `json:"affected"`
		Skipped      int64            `json:"skipped"`
		ReasonCounts map[string]int64 `json:"reasonCounts"`
		CreatedAt    time.Time        `json:"createdAt"`
		StartedAt    *time.Time       `json:"startedAt"`
		UpdatedAt    time.Time        `json:"updatedAt"`
		FinishedAt   *time.Time       `json:"finishedAt"`
	}
	if err := json.Unmarshal(raw, &status); err != nil {
		return nil, errors.New("proto bulk task progress is invalid")
	}
	if status.TaskID != ref.String() || !validAdminBulkTaskStatus(status.Status) || status.Attempts < 0 || status.MaxAttempts < 1 || status.Requested < 0 || status.Processed < 0 || status.Affected < 0 || status.Skipped < 0 || status.Affected+status.Skipped > status.Processed || status.CreatedAt.IsZero() || status.UpdatedAt.IsZero() {
		return nil, errors.New("proto bulk task progress is invalid")
	}
	kind := governanceapp.AdminTaskKindBulkDisable
	if status.Action != "disable" {
		kind, err = adminBulkTaskKind(status.Action)
		if err != nil {
			return nil, err
		}
	}
	reasons := make(map[string]int64, len(status.ReasonCounts))
	for reason, count := range status.ReasonCounts {
		if count < 0 {
			return nil, errors.New("proto bulk task reason count is invalid")
		}
		reasons[safeReason(reason)] += count
	}
	return &governanceapp.AdminTaskView{
		Ref: ref, BizType: governanceapp.AdminTaskBizProtoResourceBulk, BizID: id,
		Kind: kind, Status: status.Status, Attempts: status.Attempts, MaxAttempts: status.MaxAttempts,
		QueuedAt: status.CreatedAt, StartedAt: status.StartedAt, UpdatedAt: status.UpdatedAt, FinishedAt: status.FinishedAt,
		Progress: &governanceapp.AdminTaskProgress{Total: status.Requested, Processed: status.Processed, Succeeded: status.Affected, Skipped: status.Skipped, ReasonCounts: reasonCountMap(reasons)},
	}, nil
}
