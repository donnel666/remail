package gmail

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	"github.com/donnel666/remail/internal/platform"
	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	typeGmailValidationBatch      = "gmail:resource_validation_batch"
	localGmailValidationBatchPage = 100
	localGmailValidationBatchTTL  = 24 * time.Hour
)

type localGmailValidationBatchTask struct {
	BatchID    string `json:"batchId"`
	ClaimToken string `json:"claimToken"`
	Cursor     int    `json:"cursor"`
}

type localGmailValidationBatchRecord struct {
	BatchID        string    `json:"batchId"`
	ClaimToken     string    `json:"claimToken"`
	Fingerprint    string    `json:"fingerprint"`
	OperatorUserID uint      `json:"operatorUserId"`
	RequestID      string    `json:"requestId"`
	Path           string    `json:"path"`
	ResourceIDs    []uint    `json:"resourceIds"`
	Cursor         int       `json:"cursor"`
	Queued         int       `json:"queued"`
	Skipped        int       `json:"skipped"`
	Status         string    `json:"status"`
	AuditLogged    bool      `json:"auditLogged"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

type LocalGmailValidationBatchView struct {
	BatchID   string    `json:"batchId"`
	Status    string    `json:"status"`
	Requested int       `json:"requested"`
	Processed int       `json:"processed"`
	Queued    int       `json:"queued"`
	Skipped   int       `json:"skipped"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
	Reused    bool      `json:"reused,omitempty"`
}

func (s *Service) AcceptAdminLocalResourceValidationBatch(
	ctx context.Context,
	resourceIDs []uint,
	operatorUserID uint,
	idempotencyKey, requestID, path string,
) (*LocalGmailValidationBatchView, error) {
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if s == nil || s.redis == nil || s.queue == nil || operatorUserID == 0 ||
		idempotencyKey == "" || len(idempotencyKey) > 128 {
		return nil, ErrInvalidLocalResource
	}
	resourceIDs = uniqueLocalGmailResourceIDs(resourceIDs)
	if len(resourceIDs) == 0 || len(resourceIDs) > 10000 {
		return nil, ErrInvalidLocalResource
	}
	parts := make([]string, len(resourceIDs))
	for i, id := range resourceIDs {
		parts[i] = strconv.FormatUint(uint64(id), 10)
	}
	fingerprint := stableDigest(fmt.Sprintf("validate_batch|%d|%s", operatorUserID, strings.Join(parts, ",")))
	batchID, reused, err := s.claimLocalGmailValidationBatchRequest(ctx, operatorUserID, idempotencyKey, fingerprint)
	if err != nil {
		return nil, err
	}
	now := s.now().UTC()
	record := localGmailValidationBatchRecord{
		BatchID: batchID, ClaimToken: platform.NewUUIDV7String(), Fingerprint: fingerprint,
		OperatorUserID: operatorUserID, RequestID: strings.TrimSpace(requestID), Path: strings.TrimSpace(path),
		ResourceIDs: resourceIDs, Status: "queued", CreatedAt: now, UpdatedAt: now,
	}
	recordKey := localGmailValidationBatchKey(batchID)
	payload, _ := json.Marshal(record)
	created, err := s.redis.SetNX(ctx, recordKey, payload, localGmailValidationBatchTTL).Result()
	if err != nil {
		return nil, fmt.Errorf("store Gmail validation batch: %w", ErrLocalValidationDependency)
	}
	if !created {
		stored, err := s.localGmailValidationBatchRecord(ctx, batchID)
		if err != nil {
			return nil, err
		}
		if stored.Fingerprint != fingerprint || stored.OperatorUserID != operatorUserID {
			return nil, ErrLocalValidationConflict
		}
		record = *stored
	}
	if !record.AuditLogged && s.logs != nil {
		if err := s.logs.Create(ctx, &governancedomain.OperationLog{
			OperatorUserID: operatorUserID, OperationType: "gmail.resource.validate_batch",
			ResourceType: "gmail_resource", ResourceID: batchID, Path: path, Result: "success",
			SafeSummary: fmt.Sprintf("Accepted %d Gmail resources for asynchronous validation.", len(resourceIDs)),
			RequestID:   requestID,
		}); err != nil {
			return nil, err
		}
		record.AuditLogged = true
		record.UpdatedAt = s.now().UTC()
		if err := s.saveLocalGmailValidationBatchRecord(ctx, record); err != nil {
			return nil, err
		}
	}
	if record.Status != "completed" {
		if err := s.enqueueLocalGmailValidationBatch(ctx, localGmailValidationBatchTask{
			BatchID: record.BatchID, ClaimToken: record.ClaimToken, Cursor: record.Cursor,
		}, 0); err != nil {
			return nil, err
		}
	}
	view := localGmailValidationBatchView(record)
	view.Reused = reused || !created
	return &view, nil
}

func (s *Service) GetAdminLocalResourceValidationBatch(ctx context.Context, batchID string) (*LocalGmailValidationBatchView, error) {
	record, err := s.localGmailValidationBatchRecord(ctx, strings.TrimSpace(batchID))
	if err != nil {
		return nil, err
	}
	view := localGmailValidationBatchView(*record)
	return &view, nil
}

func (s *Service) ProcessLocalResourceValidationBatch(ctx context.Context, task localGmailValidationBatchTask) error {
	if task.BatchID == "" || task.ClaimToken == "" || task.Cursor < 0 {
		return ErrLocalValidationConflict
	}
	record, err := s.localGmailValidationBatchRecord(ctx, task.BatchID)
	if err != nil {
		return err
	}
	if record.ClaimToken != task.ClaimToken {
		return nil
	}
	if task.Cursor < record.Cursor || record.Status == "completed" {
		return nil
	}
	if task.Cursor > record.Cursor {
		return ErrLocalValidationDependency
	}
	end := min(record.Cursor+localGmailValidationBatchPage, len(record.ResourceIDs))
	queued, skipped := 0, 0
	for _, resourceID := range record.ResourceIDs[record.Cursor:end] {
		changed, err := s.markLocalGmailBatchValidationPending(ctx, *record, resourceID)
		if err != nil {
			if errors.Is(err, ErrLocalResourceMissing) || errors.Is(err, ErrInvalidLocalResource) {
				skipped++
				continue
			}
			return err
		}
		if changed {
			queued++
		} else {
			skipped++
		}
	}
	if end < len(record.ResourceIDs) {
		if err := s.enqueueLocalGmailValidationBatch(ctx, localGmailValidationBatchTask{
			BatchID: record.BatchID, ClaimToken: record.ClaimToken, Cursor: end,
		}, time.Second); err != nil {
			return err
		}
	}
	record.Cursor = end
	record.Queued += queued
	record.Skipped += skipped
	record.UpdatedAt = s.now().UTC()
	if end == len(record.ResourceIDs) {
		record.Status = "completed"
	} else {
		record.Status = "processing"
	}
	if err := s.saveLocalGmailValidationBatchRecord(ctx, *record); err != nil {
		return err
	}
	if queued > 0 {
		_ = s.scheduleLocalResourceValidationDispatcher(context.WithoutCancel(ctx), 0)
	}
	return nil
}

func (s *Service) markLocalGmailBatchValidationPending(
	ctx context.Context,
	record localGmailValidationBatchRecord,
	resourceID uint,
) (bool, error) {
	changed := false
	commandHash := stableDigest(record.BatchID + "|" + strconv.FormatUint(uint64(resourceID), 10))
	err := s.dbFor(ctx).Transaction(func(tx *gorm.DB) error {
		var root resourceRootModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND type = ?", resourceID, "gmail").Take(&root).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrLocalResourceMissing
			}
			return err
		}
		var resource localResourceModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", resourceID).Take(&resource).Error; err != nil {
			return err
		}
		if resource.Status == LocalResourceDeleted {
			return ErrLocalResourceMissing
		}
		if resource.Status == LocalResourceDisabled {
			return ErrInvalidLocalResource
		}
		if resource.ValidationCommandHash == commandHash {
			return nil
		}
		now := s.now().UTC()
		nextGeneration := nextAdminLocalResourceGeneration(resource.ValidationGeneration)
		result := tx.Model(&localResourceModel{}).Where("id = ?", resourceID).Updates(map[string]any{
			"status": LocalResourcePending, "validation_generation": nextGeneration,
			"validation_failures": 0, "validation_request_id": record.RequestID,
			"validation_command_hash": commandHash, "last_safe_error": "", "last_checked_at": nil,
			"updated_at": now,
		})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrLocalValidationConflict
		}
		if _, err := ensureGmailMaintenanceRunTx(
			ctx, tx, resource.ID, nextGeneration, gmailMaintenanceValidation,
			resource.CredentialRevision, 0, now,
		); err != nil {
			return err
		}
		if err := tx.Model(&resourceRootModel{}).Where("id = ? AND version = ?", root.ID, root.Version).
			Updates(map[string]any{"version": gorm.Expr("version + 1"), "updated_at": now}).Error; err != nil {
			return err
		}
		changed = true
		return nil
	})
	return changed, err
}

func (s *Service) enqueueLocalGmailValidationBatch(ctx context.Context, task localGmailValidationBatchTask, delay time.Duration) error {
	payload, _ := json.Marshal(task)
	options := []asynq.Option{
		asynq.Queue(platform.QueueResource), asynq.Unique(time.Minute),
		asynq.MaxRetry(platform.BackgroundTaskMaxRetryValue()), asynq.Timeout(30 * time.Second), asynq.Retention(0),
	}
	if delay > 0 {
		options = append(options, asynq.ProcessIn(delay))
	}
	_, err := s.queue.EnqueueContext(ctx, asynq.NewTask(typeGmailValidationBatch, payload), options...)
	if errors.Is(err, asynq.ErrDuplicateTask) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("enqueue Gmail validation batch: %w", ErrLocalValidationDependency)
	}
	return nil
}

func (s *Service) claimLocalGmailValidationBatchRequest(
	ctx context.Context,
	operatorUserID uint,
	idempotencyKey, fingerprint string,
) (string, bool, error) {
	candidate := platform.NewUUIDV7String()
	key := fmt.Sprintf("gmail:validation_batch:request:%d:%s", operatorUserID, stableDigest(idempotencyKey))
	value := fingerprint + "|" + candidate
	const script = `
local current = redis.call('GET', KEYS[1])
if not current then
  redis.call('SET', KEYS[1], ARGV[1], 'PX', ARGV[2])
  return ARGV[1]
end
return current`
	stored, err := s.redis.Eval(ctx, script, []string{key}, value, localGmailValidationBatchTTL.Milliseconds()).Text()
	if err != nil && !errors.Is(err, redis.Nil) {
		return "", false, fmt.Errorf("claim Gmail validation batch: %w", ErrLocalValidationDependency)
	}
	storedFingerprint, batchID, ok := strings.Cut(stored, "|")
	if !ok || storedFingerprint != fingerprint || strings.TrimSpace(batchID) == "" {
		return "", false, ErrLocalValidationConflict
	}
	return batchID, batchID != candidate, nil
}

func (s *Service) localGmailValidationBatchRecord(ctx context.Context, batchID string) (*localGmailValidationBatchRecord, error) {
	if strings.TrimSpace(batchID) == "" {
		return nil, ErrLocalResourceMissing
	}
	payload, err := s.redis.Get(ctx, localGmailValidationBatchKey(batchID)).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, ErrLocalResourceMissing
	}
	if err != nil {
		return nil, fmt.Errorf("load Gmail validation batch: %w", ErrLocalValidationDependency)
	}
	var record localGmailValidationBatchRecord
	if json.Unmarshal(payload, &record) != nil || record.BatchID != batchID || record.ClaimToken == "" {
		return nil, ErrLocalValidationConflict
	}
	return &record, nil
}

func (s *Service) saveLocalGmailValidationBatchRecord(ctx context.Context, record localGmailValidationBatchRecord) error {
	payload, err := json.Marshal(record)
	if err != nil {
		return err
	}
	if err := s.redis.Set(ctx, localGmailValidationBatchKey(record.BatchID), payload, localGmailValidationBatchTTL).Err(); err != nil {
		return fmt.Errorf("save Gmail validation batch: %w", ErrLocalValidationDependency)
	}
	return nil
}

func localGmailValidationBatchKey(batchID string) string {
	return "gmail:resource_validation_batch:" + strings.TrimSpace(batchID)
}

func localGmailValidationBatchView(record localGmailValidationBatchRecord) LocalGmailValidationBatchView {
	return LocalGmailValidationBatchView{
		BatchID: record.BatchID, Status: record.Status, Requested: len(record.ResourceIDs),
		Processed: record.Cursor, Queued: record.Queued, Skipped: record.Skipped,
		CreatedAt: record.CreatedAt, UpdatedAt: record.UpdatedAt,
	}
}

func uniqueLocalGmailResourceIDs(resourceIDs []uint) []uint {
	seen := make(map[uint]struct{}, len(resourceIDs))
	result := make([]uint, 0, len(resourceIDs))
	for _, id := range resourceIDs {
		if id == 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		result = append(result, id)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}
