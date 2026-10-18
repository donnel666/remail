package infra

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	daumapp "github.com/donnel666/remail/internal/daum/app"
	"github.com/donnel666/remail/internal/daum/domain"
	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	"github.com/donnel666/remail/internal/platform"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const bulkTTL = 24 * time.Hour

type BulkSelection struct {
	Mode        string         `json:"mode"`
	ResourceIDs []uint         `json:"resourceIds,omitempty"`
	Filter      ResourceFilter `json:"filter,omitempty"`
}
type BulkTask struct {
	TaskID          string        `json:"taskId"`
	BatchID         string        `json:"batchId"`
	CommandKey      string        `json:"commandKey,omitempty"`
	ClaimToken      string        `json:"claimToken"`
	Fingerprint     string        `json:"fingerprint"`
	Action          string        `json:"action"`
	Selection       BulkSelection `json:"selection"`
	OperatorUserID  uint          `json:"operatorUserId"`
	OwnerID         *uint         `json:"ownerId,omitempty"`
	AfterID         uint          `json:"afterId"`
	ThroughID       uint          `json:"throughId"`
	RequestID       string        `json:"requestId"`
	AdminSearch     bool          `json:"adminSearch,omitempty"`
	SearchOwnerIDs  []uint        `json:"searchOwnerIds,omitempty"`
	ValidationBatch bool          `json:"validationBatch,omitempty"`
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
type bulkPage struct {
	ResourceIDs []uint `json:"resourceIds"`
	PageSize    int    `json:"pageSize"`
}

func BulkStatusKey(id string) string { return "remail:daum:bulk:status:" + id }
func bulkLeaseKey(id string) string  { return "remail:daum:bulk:lease:" + id }
func bulkLeaseValue(task BulkTask) string {
	return task.Fingerprint + ":" + task.ClaimToken + ":" + task.TaskID
}

var bulkReserve = redis.NewScript(`
local existing=redis.call('GET',KEYS[1])
if existing then
  local taskId=string.match(existing,'^[^:]+:[^:]+:(.+)$')
  local status=taskId and redis.call('GET',KEYS[3]..taskId)
  return {0,existing,status or ''}
end
local taskId='daum_bulk:'..redis.call('INCR',KEYS[2])
local value=ARGV[1]..':'..taskId
local task=string.gsub(ARGV[3],'"taskId":""','"taskId":"'..taskId..'"',1)
local status=string.gsub(ARGV[4],'"taskId":""','"taskId":"'..taskId..'"',1)
redis.call('SET',KEYS[1],value,'PX',ARGV[2])
redis.call('SET',KEYS[3]..taskId,status,'PX',ARGV[2])
redis.call('SET',KEYS[4],task,'PX',ARGV[2])
redis.call('SADD',KEYS[5],ARGV[5])
return {1,value,status}
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
	return normalizeBulkLimit(selection, min(runtimeconfig.Int("admin_resource_bulk_max_ids", 1000, 1), 1000))
}
func normalizeBulkLimit(selection BulkSelection, maximum int) (BulkSelection, error) {
	if selection.Mode == "" && len(selection.ResourceIDs) > 0 {
		selection.Mode = "ids"
	}
	switch selection.Mode {
	case "ids":
		if len(selection.ResourceIDs) < 1 || len(selection.ResourceIDs) > maximum {
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
	return s.submitBulk(ctx, action, selection, operator, owner, key, requestID, min(runtimeconfig.Int("admin_resource_bulk_max_ids", 1000, 1), 1000), false)
}
func (s *Service) SubmitValidations(ctx context.Context, selection BulkSelection, operator uint, owner *uint, key, requestID string) (*BulkStatus, error) {
	return s.submitBulk(ctx, "validate", selection, operator, owner, key, requestID, min(runtimeconfig.Int("resource_validation_max_ids", 10000, 1), 10000), true)
}
func (s *Service) submitBulk(ctx context.Context, action string, selection BulkSelection, operator uint, owner *uint, key, requestID string, maximum int, validationBatch bool) (*BulkStatus, error) {
	if s.Redis == nil || s.Queue == nil {
		return nil, domain.ErrDependency
	}
	if operator == 0 || key == "" || len(key) > 128 || (owner != nil && *owner != operator) {
		return nil, domain.ErrInvalidResource
	}
	switch action {
	case "validate", "history", "publish", "unpublish", "delete":
	default:
		return nil, domain.ErrInvalidResource
	}
	if owner != nil && action != "validate" && action != "publish" && action != "delete" {
		return nil, domain.ErrInvalidResource
	}
	selection, err := normalizeBulkLimit(selection, maximum)
	if err != nil {
		return nil, err
	}
	if owner != nil {
		selection.Filter = ownedResourceFilter(selection.Filter, *owner)
	} else {
		selection.Filter.AdminSearch = true
	}
	encoded, _ := json.Marshal(selection)
	fingerprint := Fingerprint(operator, fmt.Sprintf("%s:%d", action, ownerValue(owner)), encoded)
	hash := sha256.Sum256([]byte(fmt.Sprintf("%d:%s", operator, key)))
	batchID := fmt.Sprintf("%x", hash[:16])
	task := BulkTask{BatchID: batchID, CommandKey: "daum_bulk:" + batchID, ClaimToken: platform.NewUUIDV7String(), Fingerprint: fingerprint, Action: action, Selection: selection, OperatorUserID: operator, OwnerID: owner, RequestID: requestID, AdminSearch: selection.Filter.AdminSearch, SearchOwnerIDs: selection.Filter.SearchOwnerIDs}
	task.ValidationBatch = validationBatch
	now := s.Now().UTC()
	status := &BulkStatus{Kind: "daum_resource_bulk", ResourceType: "daum", Status: "queued", Action: action, OperatorUserID: operator, OwnerID: owner, Requested: len(selection.ResourceIDs), ReasonCounts: map[string]int{}, MaxAttempts: platform.BackgroundTaskMaxRetryValue() + 1, RequestID: requestID, CreatedAt: now, UpdatedAt: now}
	taskJSON, _ := json.Marshal(task)
	statusJSON, _ := json.Marshal(status)
	reservation, err := bulkReserve.Run(ctx, s.Redis, []string{bulkLeaseKey(batchID), "remail:daum:bulk:sequence", "remail:daum:bulk:status:", bulkPayloadKey(batchID), "remail:daum:bulk:pending"}, fingerprint+":"+task.ClaimToken, bulkTTL.Milliseconds(), string(taskJSON), string(statusJSON), batchID).Slice()
	if err != nil {
		return nil, err
	}
	if len(reservation) != 3 {
		return nil, domain.ErrDependency
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
	receipt, ok := reservation[2].(string)
	if !ok {
		return nil, domain.ErrDependency
	}
	if receipt == "" {
		return nil, domain.ErrResourceMissing
	}
	status, err = decodeBulkStatus([]byte(receipt), operator, owner == nil)
	if err != nil {
		return nil, err
	}
	if status.TaskID != id {
		return nil, domain.ErrDependency
	}
	// The confirmed receipt remains accepted when dispatch must wait for the periodic seeder.
	if status.Status == "queued" || status.Status == "running" {
		if err := s.resumeBulk(ctx, batchID); err != nil {
			slog.WarnContext(ctx, "Daum bulk accepted; queue dispatch deferred", "task_id", status.TaskID, "action", action, "request_id", requestID)
		}
	}
	return status, nil
}
func bulkPayloadKey(batchID string) string { return "remail:daum:bulk:payload:" + batchID }
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
		return s.Redis.SRem(ctx, "remail:daum:bulk:pending", batchID).Err()
	}
	task.AfterID = status.AfterID
	task.ThroughID = status.ThroughID
	return s.enqueueBulk(ctx, task)
}
func (s *Service) DispatchPendingBulk(ctx context.Context) error {
	if s.Redis == nil || s.Queue == nil {
		return nil
	}
	ids, _, err := s.Redis.SScan(ctx, "remail:daum:bulk:pending", 0, "", 100).Result()
	if err != nil {
		return err
	}
	var result error
	for _, id := range ids {
		err := s.resumeBulk(ctx, id)
		if errors.Is(err, redis.Nil) || errors.Is(err, domain.ErrResourceMissing) {
			err = s.Redis.SRem(ctx, "remail:daum:bulk:pending", id).Err()
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
	return decodeBulkStatus(raw, operator, admin)
}
func decodeBulkStatus(raw []byte, operator uint, admin bool) (*BulkStatus, error) {
	var status BulkStatus
	if err := json.Unmarshal(raw, &status); err != nil {
		return nil, err
	}
	if !admin && (status.OperatorUserID != operator || status.OwnerID == nil || *status.OwnerID != operator) {
		return nil, domain.ErrResourceMissing
	}
	return &status, nil
}
func (s *Service) enqueueBulk(ctx context.Context, task BulkTask) error {
	payload, err := json.Marshal(task)
	if err != nil {
		return err
	}
	_, err = s.Queue.EnqueueContext(ctx, asynq.NewTask(daumapp.TaskBulk, payload), asynq.Queue(daumapp.QueueDaumImport), asynq.Unique(30*time.Minute), asynq.Timeout(30*time.Minute), asynq.MaxRetry(platform.BackgroundTaskMaxRetryValue()), asynq.Retention(0))
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
	if task.OwnerID != nil {
		if *task.OwnerID != task.OperatorUserID {
			return domain.ErrInvalidResource
		}
		// Query-only flags are not serialized in the durable task payload.
		task.Selection.Filter = ownedResourceFilter(task.Selection.Filter, *task.OwnerID)
		task.AdminSearch, task.SearchOwnerIDs = false, nil
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
	if status.StartedAt == nil && task.Selection.Mode == "filter" {
		filter := task.Selection.Filter
		filter.AdminSearch, filter.SearchOwnerIDs = task.AdminSearch, task.SearchOwnerIDs
		var maximum struct{ ID uint }
		if err := resourceFilterQuery(s.dbFor(ctx), filter, "").Select("id").Order("id DESC").Limit(1).Scan(&maximum).Error; err != nil {
			return err
		}
		status.ThroughID = maximum.ID
		var count int64
		if err := resourceFilterQuery(s.dbFor(ctx), filter, "").Where("id <= ?", maximum.ID).Count(&count).Error; err != nil {
			return err
		}
		status.Requested = int(count)
	}
	task.ThroughID = status.ThroughID
	status.Status = "running"
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
	ids := []uint{}
	pageSize := 100
	if task.ValidationBatch {
		pageSize = min(runtimeconfig.Int("validation_batch_page_size", 1000, 1), 10000)
	}
	if task.Selection.Mode == "ids" {
		for _, id := range task.Selection.ResourceIDs {
			if id > task.AfterID {
				ids = append(ids, id)
				if len(ids) == pageSize+1 {
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
		if err := resourceFilterQuery(s.dbFor(ctx), filter, "").Where("id > ? AND id <= ?", task.AfterID, task.ThroughID).Order("id ASC").Limit(pageSize+1).Pluck("id", &ids).Error; err != nil {
			return err
		}
	}
	// Persist each bounded page before applying commands. A retry must revisit
	// the same IDs even when the command changed the filter's status/forSale.
	pageKey := fmt.Sprintf("remail:daum:bulk:page:%s:%d", task.TaskID, task.AfterID)
	encodedIDs, _ := json.Marshal(bulkPage{ResourceIDs: ids, PageSize: pageSize})
	if _, err := s.Redis.SetNX(ctx, pageKey, string(encodedIDs), bulkTTL).Result(); err != nil {
		return err
	}
	pageBytes, err := s.Redis.Get(ctx, pageKey).Bytes()
	if err != nil {
		return err
	}
	var page bulkPage
	if err := json.Unmarshal(pageBytes, &page); err != nil {
		// Existing batches stored only IDs and used a fixed 100-row page.
		if err := json.Unmarshal(pageBytes, &ids); err != nil {
			return err
		}
		pageSize = 100
	} else {
		if page.PageSize < 1 || page.PageSize > 10000 {
			return domain.ErrInvalidResource
		}
		ids, pageSize = page.ResourceIDs, page.PageSize
	}
	done := len(ids) <= pageSize
	if len(ids) > pageSize {
		ids = ids[:pageSize]
	}
	commandKey := task.CommandKey
	if commandKey == "" {
		// Jobs accepted before stable command keys must finish using their original receipts.
		commandKey = task.TaskID
	}
	if err := s.transaction(ctx, func(ctx context.Context, tx *gorm.DB) error {
		for _, id := range ids {
			var result *CommandResult
			err := tx.Transaction(func(rowTx *gorm.DB) error {
				var err error
				result, err = s.ExecuteCommand(platform.WithGormTx(ctx, rowTx), Command{ResourceID: id, Action: task.Action, OperatorUserID: task.OperatorUserID, ScopeOwnerID: task.OwnerID, IdempotencyKey: fmt.Sprintf("%s:%d", commandKey, id), RequestID: task.RequestID, Bulk: true})
				return err
			})
			reason := ""
			if err != nil {
				reason = bulkSkipReason(err)
				if reason == "" {
					return err
				}
			} else if !result.Changed {
				reason = "already_target"
			}
			if reason != "" {
				status.Skipped++
				status.ReasonCounts[reason]++
			} else {
				status.Affected++
			}
			status.Processed++
			status.AfterID = id
		}
		return nil
	}); err != nil {
		return err
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
		return s.Redis.SRem(ctx, "remail:daum:bulk:pending", task.BatchID).Err()
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
	case errors.Is(err, errIneligibleOwner):
		return "owner_ineligible"
	case errors.Is(err, domain.ErrInvalidResource):
		return "invalid_state"
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

type BulkStateResult struct {
	Requested           int               `json:"requested"`
	Affected            int               `json:"affected"`
	Skipped             int               `json:"skipped"`
	AffectedResourceIDs []uint            `json:"affectedResourceIds,omitempty"`
	SkippedResourceIDs  []uint            `json:"skippedResourceIds,omitempty"`
	ReasonCounts        []BulkReasonCount `json:"reasonCounts"`
}
type BulkReasonCount struct {
	Reason string `json:"reason"`
	Count  int    `json:"count"`
}

func (s *Service) DisableBatch(ctx context.Context, ids []uint, operator uint, key, requestID string) (*BulkStateResult, error) {
	selection, err := normalizeBulk(BulkSelection{Mode: "ids", ResourceIDs: ids})
	key = strings.TrimSpace(key)
	if err != nil || operator == 0 || key == "" || len(key) > 128 {
		return nil, domain.ErrInvalidResource
	}
	ids = selection.ResourceIDs
	payload, _ := json.Marshal(ids)
	fingerprint := Fingerprint(operator, "disable_batch", payload)
	result := &BulkStateResult{Requested: len(ids), ReasonCounts: []BulkReasonCount{}}
	err = s.transaction(ctx, func(ctx context.Context, tx *gorm.DB) error {
		token := platform.NewUUIDV7String()
		receipt := commandReceipt{OperatorUserID: operator, ResourceID: ids[0], Command: "disable_batch", IdempotencyKey: key, RequestFingerprint: fingerprint, ReservationToken: token, Status: "processing"}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&receipt).Error; err != nil {
			return err
		}
		var stored commandReceipt
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("operator_user_id = ? AND idempotency_key = ?", operator, key).Take(&stored).Error; err != nil {
			return err
		}
		if stored.Command != receipt.Command || stored.RequestFingerprint != fingerprint {
			return domain.ErrImportConflict
		}
		if stored.ReservationToken != token {
			if stored.Status != "succeeded" || stored.ResultJSON == nil {
				return domain.ErrImportConflict
			}
			return json.Unmarshal([]byte(*stored.ResultJSON), result)
		}
		reasons := map[string]int{}
		for _, id := range ids {
			var changed bool
			err := tx.Transaction(func(rowTx *gorm.DB) error {
				row, err := lockResource(rowTx, id, nil)
				if err != nil {
					return err
				}
				changed = row.Status != domain.StatusDisabled
				return s.SetStatus(platform.WithGormTx(ctx, rowTx), id, nil, domain.StatusDisabled)
			})
			reason := ""
			if err != nil {
				reason = bulkSkipReason(err)
				if reason == "" {
					return err
				}
			} else if !changed {
				reason = "already_target"
			}
			if reason != "" {
				result.Skipped++
				result.SkippedResourceIDs = append(result.SkippedResourceIDs, id)
				reasons[reason]++
			} else {
				result.Affected++
				result.AffectedResourceIDs = append(result.AffectedResourceIDs, id)
			}
		}
		for reason, count := range reasons {
			result.ReasonCounts = append(result.ReasonCounts, BulkReasonCount{reason, count})
		}
		sort.Slice(result.ReasonCounts, func(i, j int) bool { return result.ReasonCounts[i].Reason < result.ReasonCounts[j].Reason })
		if s.OperationLogs != nil {
			if err := s.OperationLogs.Create(ctx, &governancedomain.OperationLog{OperatorUserID: operator, OperationType: "daum.resource.disable_batch", ResourceType: "daum_resource", ResourceID: fmt.Sprint(ids[0]), Result: "success", SafeSummary: "Daum resource batch disable completed.", RequestID: requestID}); err != nil {
				return err
			}
		}
		encoded, err := json.Marshal(result)
		if err != nil {
			return err
		}
		return tx.Model(&commandReceipt{}).Where("id = ?", stored.ID).Updates(map[string]any{"status": "succeeded", "result_json": string(encoded), "updated_at": s.Now().UTC()}).Error
	})
	return result, err
}
