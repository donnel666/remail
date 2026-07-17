package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/donnel666/remail/internal/core/domain"
	governancedomain "github.com/donnel666/remail/internal/governance/domain"
)

// AdminDomainBulkTask is the Redis-coordinated cursor for one asynchronous
// domain publish/unpublish/delete batch. Like the resource validation batch it
// carries its own progress cursor in the Asynq payload and is fenced by a Redis
// lease, so it needs no durable per-command table. A lost batch can be re-submitted.
type AdminDomainBulkTask struct {
	BatchID        string                   `json:"batchId"`
	ClaimToken     string                   `json:"claimToken,omitempty"`
	Action         string                   `json:"action"`
	Selection      AdminDomainBulkSelection `json:"selection"`
	AfterID        uint                     `json:"afterId"`
	ThroughID      uint                     `json:"throughId"`
	OperatorUserID uint                     `json:"operatorUserId"`
	RequestID      string                   `json:"requestId"`
	Path           string                   `json:"path"`
}

type AdminDomainBulkPageResult struct {
	Affected  int
	Skipped   int
	AfterID   uint
	ThroughID uint
	Done      bool
}

// AdminDomainBulkQueue is the Redis lease + Asynq transport for domain bulk
// batches. It mirrors the resource validation batch queue. EnqueueAdminDomainBulk
// reports whether this call newly accepted the batch so acceptance is audited once.
type AdminDomainBulkQueue interface {
	EnqueueAdminDomainBulk(ctx context.Context, task AdminDomainBulkTask) (bool, error)
	RefreshAdminDomainBulk(ctx context.Context, task AdminDomainBulkTask) (bool, error)
	ReleaseAdminDomainBulk(ctx context.Context, task AdminDomainBulkTask) error
}

const adminDomainBulkPageSize = 1000

// SubmitBulkState accepts a domain publish/unpublish/delete batch (explicit IDs
// or filter) for asynchronous, page-by-page execution. Large domain tables no
// longer block the request thread; the durable worker walks them one bounded
// page at a time under a Redis lease, exactly like resource validation batches.
func (s *AdminDomainCommandService) SubmitBulkState(ctx context.Context, action string, selection AdminDomainBulkSelection, operatorUserID uint, idempotencyKey, requestID, path string) (*AdminDomainBulkResult, error) {
	if s == nil || s.repo == nil || s.logs == nil || s.owners == nil || s.bulkQueue == nil || operatorUserID == 0 || !validAdminDomainStateBulkAction(action) {
		return nil, domain.ErrInvalidResourceCommand
	}
	key, err := normalizeAdminResourceIdempotencyKey(idempotencyKey)
	if err != nil {
		return nil, err
	}
	selection, fingerprintValue, err := s.normalizeBulkSelection(ctx, selection)
	if err != nil {
		return nil, err
	}
	fingerprint, err := adminResourceCommandFingerprint(struct {
		Action    string `json:"action"`
		Selection any    `json:"selection"`
	}{action, fingerprintValue})
	if err != nil {
		return nil, err
	}
	// The worker pages explicit IDs with an id cursor; normalizeBulkSelection
	// already returned them unique and ascending.
	task := AdminDomainBulkTask{
		BatchID:        "admin-domain-bulk:" + adminSensitiveValueFingerprint(fmt.Sprintf("%d:%s:%s", operatorUserID, key, fingerprint)),
		Action:         action,
		Selection:      selection,
		OperatorUserID: operatorUserID,
		RequestID:      strings.TrimSpace(requestID),
		Path:           strings.TrimSpace(path),
	}
	accepted, err := s.bulkQueue.EnqueueAdminDomainBulk(ctx, task)
	if err != nil {
		return nil, err
	}
	// Audit exactly once, only after the batch is genuinely accepted (initial
	// lease claim + enqueue). Deduped re-submissions return accepted=false. The
	// batch is already queued, so an audit-store hiccup degrades to a warning
	// rather than failing an operation that will still run.
	if accepted {
		if logErr := s.logs.Create(ctx, &governancedomain.OperationLog{
			OperatorUserID: operatorUserID, OperationType: "core.admin_domain." + action + "_bulk", ResourceType: "domain_resource", ResourceID: "batch",
			Path: strings.TrimSpace(path), Result: "success", RequestID: strings.TrimSpace(requestID),
			SafeSummary: "Domain resource batch command accepted for asynchronous execution.",
		}); logErr != nil {
			slog.Warn("admin domain bulk acceptance audit log failed", "operator_user_id", operatorUserID, "action", action, "error", logErr)
		}
	}
	// Explicit IDs have an exact bounded count; filter matching is deferred to
	// the worker, so its accepted count is only known as pages complete.
	result := &AdminDomainBulkResult{}
	if selection.Mode == AdminDomainBulkIDs {
		result.Requested = len(selection.ResourceIDs)
	}
	return result, nil
}

// ProcessBulkStateBatch runs one bounded page of a domain bulk batch and either
// finishes (releasing the lease) or re-enqueues itself with the advanced cursor.
func (s *AdminDomainCommandService) ProcessBulkStateBatch(ctx context.Context, task AdminDomainBulkTask) error {
	if s == nil || s.repo == nil || s.bulkQueue == nil || strings.TrimSpace(task.BatchID) == "" || task.OperatorUserID == 0 || !validAdminDomainStateBulkAction(task.Action) {
		return domain.ErrInvalidResourceCommand
	}
	owned, err := s.bulkQueue.RefreshAdminDomainBulk(ctx, task)
	if err != nil {
		return err
	}
	if !owned {
		return nil
	}
	page, err := s.applyDomainBulkPage(ctx, task, adminDomainBulkPageSize)
	if err != nil {
		return err
	}
	if page.Done {
		return s.bulkQueue.ReleaseAdminDomainBulk(ctx, task)
	}
	task.AfterID = page.AfterID
	task.ThroughID = page.ThroughID
	_, err = s.bulkQueue.EnqueueAdminDomainBulk(ctx, task)
	return err
}

// ReleaseBulkBatch drops the live Redis lease owned by this cursor token, used
// by the worker to abandon a batch that can no longer be admitted.
func (s *AdminDomainCommandService) ReleaseBulkBatch(ctx context.Context, task AdminDomainBulkTask) error {
	if s == nil || s.bulkQueue == nil {
		return nil
	}
	return s.bulkQueue.ReleaseAdminDomainBulk(ctx, task)
}

func (s *AdminDomainCommandService) applyDomainBulkPage(ctx context.Context, task AdminDomainBulkTask, limit int) (*AdminDomainBulkPageResult, error) {
	result := &AdminDomainBulkPageResult{AfterID: task.AfterID, ThroughID: task.ThroughID}
	var ids []uint
	switch task.Selection.Mode {
	case AdminDomainBulkIDs:
		ids = adminDomainBulkIDPage(task.Selection.ResourceIDs, task.AfterID, limit+1)
	case AdminDomainBulkFilter:
		if result.ThroughID == 0 {
			// Freeze a high-water mark on the first page so rows inserted while
			// the batch runs are never swept in.
			throughID, err := s.repo.MaxAdminDomainID(ctx, task.Selection.Filter)
			if err != nil {
				return nil, err
			}
			if throughID == 0 {
				result.Done = true
				return result, nil
			}
			result.ThroughID = throughID
		}
		pageIDs, err := s.repo.ListAdminDomainBulkPageIDs(ctx, task.Selection.Filter, task.AfterID, result.ThroughID, limit+1)
		if err != nil {
			return nil, err
		}
		ids = pageIDs
	default:
		return nil, domain.ErrInvalidResourceCommand
	}
	result.Done = len(ids) <= limit
	if len(ids) > limit {
		ids = ids[:limit]
	}
	if len(ids) == 0 {
		result.Done = true
		return result, nil
	}
	nextAfter := ids[len(ids)-1]
	if nextAfter <= task.AfterID {
		return nil, fmt.Errorf("admin domain bulk batch made no progress past id %d", task.AfterID)
	}
	if err := s.repo.WithTx(ctx, func(txCtx context.Context) error {
		for _, id := range ids {
			root, resource, lockErr := s.repo.LockAdminDomain(txCtx, id)
			if errors.Is(lockErr, domain.ErrResourceNotFound) {
				result.Skipped++
				continue
			}
			if lockErr != nil {
				return lockErr
			}
			changed, _, applyErr := s.applyDomainBulkRow(txCtx, task.Action, root, resource)
			if applyErr != nil {
				return applyErr
			}
			if changed {
				result.Affected++
			} else {
				result.Skipped++
			}
		}
		return nil
	}); err != nil {
		return nil, err
	}
	result.AfterID = nextAfter
	return result, nil
}

// adminDomainBulkIDPage returns up to limit explicit IDs greater than afterID.
// selection.ResourceIDs is frozen ascending at submission time.
func adminDomainBulkIDPage(ids []uint, afterID uint, limit int) []uint {
	page := make([]uint, 0, limit)
	for _, id := range ids {
		if id <= afterID {
			continue
		}
		page = append(page, id)
		if len(page) == limit {
			break
		}
	}
	return page
}

func validAdminDomainStateBulkAction(action string) bool {
	switch action {
	case "publish", "unpublish", "delete":
		return true
	default:
		return false
	}
}
