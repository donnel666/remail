package infra

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/donnel666/remail/internal/platform"
	protoapp "github.com/donnel666/remail/internal/proto/app"
	"github.com/donnel666/remail/internal/proto/domain"
	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"
)

const bulkTTL = 24 * time.Hour

type BulkSelection struct {
	Mode        string         `json:"mode"`
	ResourceIDs []uint         `json:"resourceIds,omitempty"`
	Filter      ResourceFilter `json:"filter,omitempty"`
}
type BulkTask struct {
	TaskID         string        `json:"taskId"`
	BatchID        string        `json:"batchId"`
	CommandKey     string        `json:"commandKey,omitempty"`
	ClaimToken     string        `json:"claimToken"`
	Fingerprint    string        `json:"fingerprint"`
	Action         string        `json:"action"`
	Selection      BulkSelection `json:"selection"`
	OperatorUserID uint          `json:"operatorUserId"`
	OwnerID        *uint         `json:"ownerId,omitempty"`
	AfterID        uint          `json:"afterId"`
	ThroughID      uint          `json:"throughId"`
	RequestID      string        `json:"requestId"`
	AdminSearch    bool          `json:"adminSearch,omitempty"`
	SearchOwnerIDs []uint        `json:"searchOwnerIds,omitempty"`
}
type BulkStatus struct {
	TaskID         string         `json:"taskId"`
	Kind           string         `json:"kind"`
	ResourceType   string         `json:"resourceType"`
	Status         string         `json:"status"`
	Action         string         `json:"action"`
	OperatorUserID uint           `json:"operatorUserId"`
	OwnerID        *uint          `json:"ownerId,omitempty"`
	Requested      int            `json:"requested"`
	Processed      int            `json:"processed"`
	Affected       int            `json:"affected"`
	Skipped        int            `json:"skipped"`
	Attempts       int            `json:"attempts"`
	MaxAttempts    int            `json:"maxAttempts"`
	ReasonCounts   map[string]int `json:"reasonCounts"`
	AfterID        uint           `json:"afterId"`
	ThroughID      uint           `json:"throughId"`
	Fingerprint    string         `json:"-"`
	RequestID      string         `json:"requestId"`
	CreatedAt      time.Time      `json:"createdAt"`
	StartedAt      *time.Time     `json:"startedAt,omitempty"`
	UpdatedAt      time.Time      `json:"updatedAt"`
	FinishedAt     *time.Time     `json:"finishedAt,omitempty"`
}

func BulkStatusKey(id string) string { return "remail:proto:bulk:status:" + id }
func bulkLeaseKey(id string) string  { return "remail:proto:bulk:lease:" + id }
func bulkLeaseValue(task BulkTask) string {
	return task.Fingerprint + ":" + task.ClaimToken + ":" + task.TaskID
}

var bulkReserve = redis.NewScript(`
local existing=redis.call('GET',KEYS[1])
if existing then return {0,existing} end
local taskId='proto_bulk:'..redis.call('INCR',KEYS[2])
local value=ARGV[1]..':'..taskId
local task=string.gsub(ARGV[3],'"taskId":""','"taskId":"'..taskId..'"',1)
local status=string.gsub(ARGV[4],'"taskId":""','"taskId":"'..taskId..'"',1)
redis.call('SET',KEYS[1],value,'PX',ARGV[2])
redis.call('SET',KEYS[3]..taskId,status,'PX',ARGV[2])
redis.call('SET',KEYS[4],task,'PX',ARGV[2])
redis.call('SADD',KEYS[5],ARGV[5])
return {1,value}
`)
var bulkRenew = redis.NewScript(`if redis.call('GET',KEYS[1]) ~= ARGV[1] then return 0 end; redis.call('PEXPIRE',KEYS[1],ARGV[2]); return 1`)
var bulkStore = redis.NewScript(`
if redis.call('GET',KEYS[1]) ~= ARGV[1] then return 0 end
local current=redis.call('GET',KEYS[2]); local next=cjson.decode(ARGV[2])
if current then
  local previous=cjson.decode(current)
  if previous.afterId > next.afterId or previous.status == 'succeeded' then return 0 end
end
redis.call('SET',KEYS[2],ARGV[2],'PX',ARGV[3]); return 1
`)

func normalizeBulk(selection BulkSelection) (BulkSelection, error) {
	if selection.Mode == "" && len(selection.ResourceIDs) > 0 {
		selection.Mode = "ids"
	}
	switch selection.Mode {
	case "ids":
		if len(selection.ResourceIDs) < 1 || len(selection.ResourceIDs) > 10000 {
			return selection, domain.ErrInvalidResource
		}
		ids := append([]uint(nil), selection.ResourceIDs...)
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		selection.ResourceIDs = nil
		for _, id := range ids {
			if id == 0 {
				return selection, domain.ErrInvalidResource
			}
			if len(selection.ResourceIDs) == 0 || selection.ResourceIDs[len(selection.ResourceIDs)-1] != id {
				selection.ResourceIDs = append(selection.ResourceIDs, id)
			}
		}
	case "filter":
		if len(selection.ResourceIDs) > 0 {
			return selection, domain.ErrInvalidResource
		}
		var err error
		selection.Filter, err = normalizeFilter(selection.Filter)
		if err != nil {
			return selection, err
		}
	default:
		return selection, domain.ErrInvalidResource
	}
	return selection, nil
}
func (s *Service) SubmitBulk(ctx context.Context, action string, selection BulkSelection, operator uint, owner *uint, key, requestID string) (*BulkStatus, error) {
	if s.Redis == nil || s.Queue == nil {
		return nil, domain.ErrDependency
	}
	if operator == 0 || key == "" || len(key) > 128 {
		return nil, domain.ErrInvalidResource
	}
	switch action {
	case "validate", "history", "disable", "publish", "unpublish", "delete":
	default:
		return nil, domain.ErrInvalidResource
	}
	if owner != nil && action != "validate" && action != "publish" && action != "delete" {
		return nil, domain.ErrInvalidResource
	}
	selection, err := normalizeBulk(selection)
	if err != nil {
		return nil, err
	}
	if owner != nil {
		selection.Filter.OwnerID = owner
		selection.Filter.ExcludeDeleted = true
		selection.Filter.AdminSearch = false
		selection.Filter.SearchOwnerIDs = nil
	} else {
		selection.Filter.AdminSearch = true
	}
	encoded, _ := json.Marshal(selection)
	fingerprint := Fingerprint(operator, fmt.Sprintf("%s:%d", action, ownerValue(owner)), encoded)
	hash := sha256.Sum256([]byte(fmt.Sprintf("%d:%s", operator, key)))
	batchID := fmt.Sprintf("%x", hash[:16])
	task := BulkTask{BatchID: batchID, CommandKey: "proto_bulk:" + batchID, ClaimToken: platform.NewUUIDV7String(), Fingerprint: fingerprint, Action: action, Selection: selection, OperatorUserID: operator, OwnerID: owner, RequestID: requestID, AdminSearch: selection.Filter.AdminSearch, SearchOwnerIDs: selection.Filter.SearchOwnerIDs}
	now := s.Now().UTC()
	status := &BulkStatus{Kind: "proto_resource_bulk", ResourceType: "proto", Status: "queued", Action: action, OperatorUserID: operator, OwnerID: owner, Requested: len(selection.ResourceIDs), ReasonCounts: map[string]int{}, MaxAttempts: platform.BackgroundTaskMaxRetryValue() + 1, RequestID: requestID, CreatedAt: now, UpdatedAt: now}
	if selection.Mode == "filter" {
		var maximum struct{ ID uint }
		if err := resourceFilterQuery(s.dbFor(ctx), selection.Filter, "").Select("id").Order("id DESC").Limit(1).Scan(&maximum).Error; err != nil {
			return nil, err
		}
		task.ThroughID = maximum.ID
		status.ThroughID = maximum.ID
		var count int64
		if err := resourceFilterQuery(s.dbFor(ctx), selection.Filter, "").Where("id <= ?", maximum.ID).Count(&count).Error; err != nil {
			return nil, err
		}
		status.Requested = int(count)
	}
	taskJSON, _ := json.Marshal(task)
	statusJSON, _ := json.Marshal(status)
	reservation, err := bulkReserve.Run(ctx, s.Redis, []string{bulkLeaseKey(batchID), "remail:proto:bulk:sequence", "remail:proto:bulk:status:", bulkPayloadKey(batchID), "remail:proto:bulk:pending"}, fingerprint+":"+task.ClaimToken, bulkTTL.Milliseconds(), string(taskJSON), string(statusJSON), batchID).Slice()
	if err != nil {
		return nil, err
	}
	value, ok := reservation[1].(string)
	if !ok {
		return nil, domain.ErrDependency
	}
	parts := strings.SplitN(value, ":", 3)
	if len(parts) != 3 || parts[0] != fingerprint {
		return nil, domain.ErrImportConflict
	}
	id := parts[2]
	status, err = s.GetBulkStatus(ctx, id, operator, owner == nil)
	if err != nil {
		return nil, err
	}
	// The reservation atomically stores the canonical cursor and observation.
	// A replay or periodic seeder repairs a process exit before Redis enqueue.
	if status.Status == "queued" || status.Status == "running" {
		if err := s.resumeBulk(ctx, batchID); err != nil {
			return nil, err
		}
	}
	return status, nil
}
func bulkPayloadKey(batchID string) string { return "remail:proto:bulk:payload:" + batchID }
func (s *Service) resumeBulk(ctx context.Context, batchID string) error {
	raw, err := s.Redis.Get(ctx, bulkPayloadKey(batchID)).Bytes()
	if err != nil {
		return err
	}
	var task BulkTask
	if err := json.Unmarshal(raw, &task); err != nil {
		return err
	}
	status, err := s.GetBulkStatus(ctx, task.TaskID, task.OperatorUserID, true)
	if err != nil {
		return err
	}
	if status.Status == "succeeded" || status.Status == "failed" {
		return s.Redis.SRem(ctx, "remail:proto:bulk:pending", batchID).Err()
	}
	task.AfterID = status.AfterID
	task.ThroughID = status.ThroughID
	return s.enqueueBulk(ctx, task)
}
func (s *Service) DispatchPendingBulk(ctx context.Context) error {
	if s.Redis == nil || s.Queue == nil {
		return nil
	}
	ids, _, err := s.Redis.SScan(ctx, "remail:proto:bulk:pending", 0, "", 100).Result()
	if err != nil {
		return err
	}
	var result error
	for _, id := range ids {
		err := s.resumeBulk(ctx, id)
		if errors.Is(err, redis.Nil) || errors.Is(err, domain.ErrResourceMissing) {
			err = s.Redis.SRem(ctx, "remail:proto:bulk:pending", id).Err()
		}
		result = errors.Join(result, err)
	}
	return result
}
func (s *Service) GetBulkStatus(ctx context.Context, id string, operator uint, admin bool) (*BulkStatus, error) {
	if s.Redis == nil {
		return nil, domain.ErrDependency
	}
	raw, err := s.Redis.Get(ctx, BulkStatusKey(id)).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, domain.ErrResourceMissing
	}
	if err != nil {
		return nil, err
	}
	var status BulkStatus
	if err := json.Unmarshal(raw, &status); err != nil {
		return nil, err
	}
	if !admin && status.OperatorUserID != operator {
		return nil, domain.ErrResourceMissing
	}
	return &status, nil
}
func (s *Service) enqueueBulk(ctx context.Context, task BulkTask) error {
	payload, err := json.Marshal(task)
	if err != nil {
		return err
	}
	_, err = s.Queue.EnqueueContext(ctx, asynq.NewTask(protoapp.TaskBulk, payload), asynq.Queue(protoapp.QueueProtoImport), asynq.Unique(2*time.Minute), asynq.Timeout(2*time.Minute), asynq.MaxRetry(platform.BackgroundTaskMaxRetryValue()), asynq.Retention(0))
	if errors.Is(err, asynq.ErrDuplicateTask) {
		return nil
	}
	return err
}
func (s *Service) storeBulkStatus(ctx context.Context, task BulkTask, status *BulkStatus) error {
	encoded, err := json.Marshal(status)
	if err != nil {
		return err
	}
	stored, err := bulkStore.Run(ctx, s.Redis, []string{bulkLeaseKey(task.BatchID), BulkStatusKey(task.TaskID)}, bulkLeaseValue(task), string(encoded), bulkTTL.Milliseconds()).Int()
	if err != nil {
		return err
	}
	if stored != 1 {
		return domain.ErrInvalidClaim
	}
	return nil
}
func (s *Service) ProcessBulk(ctx context.Context, task BulkTask) error {
	if s.Redis == nil || s.Queue == nil || task.TaskID == "" || task.ClaimToken == "" {
		return domain.ErrInvalidResource
	}
	owned, err := bulkRenew.Run(ctx, s.Redis, []string{bulkLeaseKey(task.BatchID)}, bulkLeaseValue(task), bulkTTL.Milliseconds()).Int()
	if err != nil {
		return err
	}
	if owned != 1 {
		return nil
	}
	status, err := s.GetBulkStatus(ctx, task.TaskID, task.OperatorUserID, true)
	if err != nil {
		return err
	}
	if status.Status == "succeeded" || status.Status == "failed" {
		return nil
	}
	if status.AfterID > task.AfterID {
		task.AfterID = status.AfterID
	}
	if status.ThroughID > 0 {
		task.ThroughID = status.ThroughID
	}
	ids := []uint{}
	if task.Selection.Mode == "ids" {
		for _, id := range task.Selection.ResourceIDs {
			if id > task.AfterID {
				ids = append(ids, id)
				if len(ids) == 101 {
					break
				}
			}
		}
	} else {
		filter := task.Selection.Filter
		filter.AdminSearch = task.AdminSearch
		filter.SearchOwnerIDs = task.SearchOwnerIDs
		if task.OwnerID != nil {
			filter.OwnerID = task.OwnerID
			filter.ExcludeDeleted = true
		}
		if err := resourceFilterQuery(s.dbFor(ctx), filter, "").Where("id > ? AND id <= ?", task.AfterID, task.ThroughID).Order("id ASC").Limit(101).Pluck("id", &ids).Error; err != nil {
			return err
		}
	}
	// Persist each bounded page before applying commands. A retry must revisit
	// the same IDs even when the command changed the filter's status/forSale.
	pageKey := fmt.Sprintf("remail:proto:bulk:page:%s:%d", task.TaskID, task.AfterID)
	encodedIDs, _ := json.Marshal(ids)
	if _, err := s.Redis.SetNX(ctx, pageKey, string(encodedIDs), bulkTTL).Result(); err != nil {
		return err
	}
	pageBytes, err := s.Redis.Get(ctx, pageKey).Bytes()
	if err != nil {
		return err
	}
	if err := json.Unmarshal(pageBytes, &ids); err != nil {
		return err
	}
	done := len(ids) <= 100
	if len(ids) > 100 {
		ids = ids[:100]
	}
	status.Status = "running"
	status.ThroughID = task.ThroughID
	started := s.Now().UTC()
	if status.StartedAt == nil {
		status.StartedAt = &started
	}
	retried, _ := asynq.GetRetryCount(ctx)
	status.Attempts = max(status.Attempts, retried+1)
	status.UpdatedAt = started
	if err := s.storeBulkStatus(ctx, task, status); err != nil {
		return err
	}
	commandKey := task.CommandKey
	if commandKey == "" {
		// Jobs accepted before stable command keys must finish using their original receipts.
		commandKey = task.TaskID
	}
	for _, id := range ids {
		_, err := s.ExecuteCommand(ctx, Command{ResourceID: id, Action: task.Action, OperatorUserID: task.OperatorUserID, ScopeOwnerID: task.OwnerID, IdempotencyKey: fmt.Sprintf("%s:%d", commandKey, id), RequestID: task.RequestID, Bulk: true})
		if err != nil {
			reason := bulkSkipReason(err)
			if reason == "" {
				return err
			}
			status.Skipped++
			status.ReasonCounts[reason]++
		} else {
			status.Affected++
		}
		status.Processed++
		status.AfterID = id
	}
	now := s.Now().UTC()
	status.UpdatedAt = now
	if done {
		status.Status = "succeeded"
		status.FinishedAt = &now
	}
	if err := s.storeBulkStatus(ctx, task, status); err != nil {
		return err
	}
	if done {
		return s.Redis.SRem(ctx, "remail:proto:bulk:pending", task.BatchID).Err()
	}
	task.AfterID = status.AfterID
	return s.enqueueBulk(ctx, task)
}
func bulkSkipReason(err error) string {
	switch {
	case errors.Is(err, domain.ErrResourceMissing):
		return "not_found"
	case errors.Is(err, domain.ErrResourceBusy):
		return "active_allocation"
	case errors.Is(err, domain.ErrResourceNotPrivate):
		return "not_private"
	case errors.Is(err, domain.ErrInvalidResource):
		return "invalid_status"
	}
	return ""
}
func (s *Service) FailBulk(ctx context.Context, task BulkTask) error {
	status, err := s.GetBulkStatus(ctx, task.TaskID, task.OperatorUserID, true)
	if err != nil {
		return err
	}
	now := s.Now().UTC()
	status.Status = "failed"
	status.UpdatedAt = now
	status.FinishedAt = &now
	return s.storeBulkStatus(ctx, task, status)
}
