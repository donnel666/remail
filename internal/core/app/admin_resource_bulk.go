package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/donnel666/remail/internal/core/domain"
	governancedomain "github.com/donnel666/remail/internal/governance/domain"
)

type AdminResourceBulkAction string

const (
	AdminResourceBulkValidate  AdminResourceBulkAction = "validate"
	AdminResourceBulkPublish   AdminResourceBulkAction = "publish"
	AdminResourceBulkUnpublish AdminResourceBulkAction = "unpublish"
	AdminResourceBulkDelete    AdminResourceBulkAction = "delete"
)

type AdminResourceBulkSelectionMode string

const (
	AdminResourceBulkIDs    AdminResourceBulkSelectionMode = "ids"
	AdminResourceBulkFilter AdminResourceBulkSelectionMode = "filter"
)

type AdminResourceBulkFilterValue struct {
	Search         string                         `json:"search,omitempty"`
	Suffix         string                         `json:"suffix,omitempty"`
	Status         domain.MicrosoftResourceStatus `json:"status,omitempty"`
	ForSale        *bool                          `json:"forSale,omitempty"`
	LongLived      *bool                          `json:"longLived,omitempty"`
	GraphAvailable *bool                          `json:"graphAvailable,omitempty"`
	TokenHealth    string                         `json:"tokenHealth,omitempty"`
	CreatedFrom    *time.Time                     `json:"createdFrom,omitempty"`
	CreatedTo      *time.Time                     `json:"createdTo,omitempty"`
	OwnerIDs       []uint                         `json:"ownerIds,omitempty"`
}

func (f AdminResourceBulkFilterValue) ListFilter() AdminMicrosoftListFilter {
	return AdminMicrosoftListFilter{
		Search: f.Search, Suffix: f.Suffix, Status: f.Status, ForSale: f.ForSale,
		LongLived: f.LongLived, GraphAvailable: f.GraphAvailable, TokenHealth: f.TokenHealth,
		CreatedFrom: f.CreatedFrom, CreatedTo: f.CreatedTo, OwnerIDs: append([]uint(nil), f.OwnerIDs...),
	}
}

type AdminResourceBulkSelection struct {
	Mode        AdminResourceBulkSelectionMode `json:"mode"`
	ResourceIDs []uint                         `json:"resourceIds,omitempty"`
	Filter      AdminResourceBulkFilterValue   `json:"filter,omitempty"`
}

type AdminResourceBulkCommand struct {
	ID                   uint64
	OperatorUserID       uint
	Action               AdminResourceBulkAction
	Selection            AdminResourceBulkSelection
	SelectionFingerprint string
	IdempotencyKey       string
	MaxResourceID        uint
	CheckpointResourceID uint
	Status               string
	MatchedCount         int
	ProcessedCount       int
	AffectedCount        int
	SkippedCount         int
	ReasonCounts         map[string]int64
	Attempts             int
	MaxAttempts          int
	ClaimToken           string
	DispatchToken        string
	LastSafeError        string
	RequestID            string
	Path                 string
	DispatchedAt         *time.Time
	StartedAt            *time.Time
	FinishedAt           *time.Time
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

type AdminResourceBulkTask struct {
	CommandID     uint64 `json:"commandId"`
	DispatchToken string `json:"dispatchToken"`
}

type AdminResourceBulkRepository interface {
	CreateWithLog(ctx context.Context, command *AdminResourceBulkCommand, log *governancedomain.OperationLog) (bool, error)
	FindByID(ctx context.Context, id uint64) (*AdminResourceBulkCommand, error)
	ClaimDispatchable(ctx context.Context, limit int, runningStaleBefore, queuedDispatchStaleBefore time.Time) ([]AdminResourceBulkCommand, error)
	MarkRunning(ctx context.Context, id uint64, dispatchToken string) (*AdminResourceBulkCommand, bool, error)
	ListCandidateIDs(ctx context.Context, command *AdminResourceBulkCommand, limit int, now time.Time) ([]uint, error)
	CompletePage(ctx context.Context, id uint64, claimToken string, checkpoint uint, matched, processed, affected, skipped int, reasons map[string]int64, done bool) error
	MarkRetryableFailure(ctx context.Context, id uint64, claimToken, safeError string) (bool, error)
	MarkDispatchFailed(ctx context.Context, id uint64, dispatchToken, safeError string) error
}

type AdminResourceBulkQueue interface {
	EnqueueAdminResourceBulk(ctx context.Context, task AdminResourceBulkTask) error
	EnqueueAdminResourceBulkDispatcher(ctx context.Context, delay time.Duration) error
}

type AdminResourceBulkService struct {
	repo     AdminResourceBulkRepository
	queue    AdminResourceBulkQueue
	commands *AdminResourceCommandService
	now      func() time.Time
}

const (
	AdminResourceBulkMaxExplicitIDs = 1000
	adminResourceBulkPageSize       = 100
	adminResourceBulkRunningStale   = 20 * time.Minute
	adminResourceBulkDispatchLease  = time.Hour
)

func NewAdminResourceBulkService(repo AdminResourceBulkRepository, queue AdminResourceBulkQueue, commands *AdminResourceCommandService) *AdminResourceBulkService {
	return &AdminResourceBulkService{repo: repo, queue: queue, commands: commands, now: func() time.Time { return time.Now().UTC() }}
}

func (s *AdminResourceBulkService) Submit(ctx context.Context, action AdminResourceBulkAction, selection AdminResourceBulkSelection, operatorUserID uint, idempotencyKey, requestID, path string) (*AdminResourceBulkCommand, bool, error) {
	if s == nil || s.repo == nil || s.queue == nil || s.commands == nil || operatorUserID == 0 || !validAdminBulkAction(action) {
		return nil, false, domain.ErrInvalidResourceCommand
	}
	normalized, err := normalizeAdminBulkSelection(selection)
	if err != nil {
		return nil, false, err
	}
	if normalized.Mode == AdminResourceBulkFilter && action == "disable" {
		return nil, false, domain.ErrInvalidResourceCommand
	}
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if idempotencyKey == "" || len(idempotencyKey) > 128 {
		return nil, false, domain.ErrInvalidResourceCommand
	}
	// The fingerprint represents the caller's request. The owner IDs resolved
	// below are an internal execution snapshot and may change between retries.
	fingerprint, err := adminBulkFingerprint(action, normalized)
	if err != nil {
		return nil, false, err
	}
	if normalized.Mode == AdminResourceBulkFilter && normalized.Filter.Search != "" {
		if s.commands.owners == nil {
			return nil, false, domain.ErrResourceDependency
		}
		matched, searchErr := s.commands.owners.SearchAdminOwners(ctx, normalized.Filter.Search, 1000)
		if searchErr != nil {
			return nil, false, fmt.Errorf("search admin resource owners for batch: %w", searchErr)
		}
		ownerIDs := make([]uint, 0, len(matched))
		for _, owner := range matched {
			if owner.ID > 0 {
				ownerIDs = append(ownerIDs, owner.ID)
			}
		}
		normalized.Filter.OwnerIDs = uniqueAdminResourceIDs(ownerIDs)
	}
	command := &AdminResourceBulkCommand{
		OperatorUserID: operatorUserID, Action: action, Selection: normalized,
		SelectionFingerprint: fingerprint, IdempotencyKey: idempotencyKey,
		Status: "queued", MaxAttempts: 3, ReasonCounts: map[string]int64{},
		RequestID: strings.TrimSpace(requestID), Path: strings.TrimSpace(path),
	}
	created, err := s.repo.CreateWithLog(ctx, command, &governancedomain.OperationLog{
		OperatorUserID: operatorUserID,
		OperationType:  "core.admin_resource." + string(action) + "_bulk",
		ResourceType:   "microsoft_resource",
		ResourceID:     string(normalized.Mode),
		Path:           strings.TrimSpace(path),
		Result:         "success",
		SafeSummary:    "Microsoft resource batch command accepted.",
		RequestID:      strings.TrimSpace(requestID),
	})
	if err != nil {
		return nil, false, err
	}
	if created {
		s.ScheduleDispatcher(ctx, 0)
	}
	return command, !created, nil
}

func (s *AdminResourceBulkService) Get(ctx context.Context, commandID uint64) (*AdminResourceBulkCommand, error) {
	if s == nil || s.repo == nil || commandID == 0 {
		return nil, domain.ErrResourceNotFound
	}
	command, err := s.repo.FindByID(ctx, commandID)
	if err != nil {
		return nil, err
	}
	if command == nil {
		return nil, domain.ErrResourceNotFound
	}
	return command, nil
}

func (s *AdminResourceBulkService) DispatchPending(ctx context.Context, limit int) error {
	if s == nil || s.repo == nil || s.queue == nil {
		return domain.ErrResourceDependency
	}
	if limit <= 0 {
		limit = 32
	}
	now := s.now()
	commands, err := s.repo.ClaimDispatchable(ctx, limit, now.Add(-adminResourceBulkRunningStale), now.Add(-adminResourceBulkDispatchLease))
	if err != nil {
		return err
	}
	var result error
	for i := range commands {
		err := s.queue.EnqueueAdminResourceBulk(ctx, AdminResourceBulkTask{CommandID: commands[i].ID, DispatchToken: commands[i].DispatchToken})
		if err == nil {
			continue
		}
		result = errors.Join(result, err)
		if releaseErr := s.repo.MarkDispatchFailed(ctx, commands[i].ID, commands[i].DispatchToken, "Batch queue is temporarily unavailable."); releaseErr != nil {
			result = errors.Join(result, releaseErr)
		}
	}
	return result
}

func (s *AdminResourceBulkService) Process(ctx context.Context, task AdminResourceBulkTask) error {
	if s == nil || s.repo == nil || s.commands == nil || task.CommandID == 0 || strings.TrimSpace(task.DispatchToken) == "" {
		return domain.ErrInvalidResourceCommand
	}
	command, claimed, err := s.repo.MarkRunning(ctx, task.CommandID, task.DispatchToken)
	if err != nil || !claimed {
		return err
	}
	ids, err := s.repo.ListCandidateIDs(ctx, command, adminResourceBulkPageSize, s.now())
	if err != nil {
		return s.retry(ctx, command, err)
	}
	if len(ids) == 0 {
		return s.repo.CompletePage(ctx, command.ID, command.ClaimToken, command.CheckpointResourceID, 0, 0, 0, 0, command.ReasonCounts, true)
	}
	reasons := cloneAdminBulkReasons(command.ReasonCounts)
	affected, skipped := 0, 0
	checkpoint := ids[len(ids)-1]
	done := len(ids) < adminResourceBulkPageSize
	// Both explicit-ID and filter selections capture their matched total at
	// acceptance time (repo CreateWithLog), so per-page processing must never
	// re-add matches or the command would report roughly double the real count.
	matched := 0
	err = s.commands.repo.WithTx(ctx, func(txCtx context.Context) error {
		for _, resourceID := range ids {
			var changed bool
			var reason string
			var itemErr error
			if command.Action == AdminResourceBulkValidate {
				changed, reason, itemErr = s.commands.validateOneForBulk(txCtx, resourceID, command.RequestID, command.Path)
			} else {
				changed, reason, itemErr = s.commands.applyStateOneForBulk(txCtx, AdminMicrosoftStateCommand(command.Action), resourceID)
			}
			if itemErr != nil {
				return itemErr
			}
			if changed {
				affected++
			} else {
				skipped++
				if reason == "" {
					reason = "not_changed"
				}
				reasons[reason]++
			}
		}
		return s.repo.CompletePage(txCtx, command.ID, command.ClaimToken, checkpoint, matched, len(ids), affected, skipped, reasons, done)
	})
	if err != nil {
		return s.retry(ctx, command, err)
	}
	if command.Action == AdminResourceBulkValidate && affected > 0 && s.commands.validation != nil {
		s.commands.validation.ScheduleDispatcher(ctx, 0)
	}
	if !done {
		s.ScheduleDispatcher(ctx, 0)
	}
	return nil
}

// ReleaseDispatch returns a fenced, not-yet-started command to its durable
// dispatcher. It is only needed while draining Asynq messages created by older
// releases with MaxRetry(0).
func (s *AdminResourceBulkService) ReleaseDispatch(ctx context.Context, task AdminResourceBulkTask) error {
	if s == nil || s.repo == nil || task.CommandID == 0 || strings.TrimSpace(task.DispatchToken) == "" {
		return nil
	}
	return s.repo.MarkDispatchFailed(ctx, task.CommandID, task.DispatchToken, "")
}

func (s *AdminResourceBulkService) retry(ctx context.Context, command *AdminResourceBulkCommand, cause error) error {
	exhausted, err := s.repo.MarkRetryableFailure(ctx, command.ID, command.ClaimToken, "Batch processing failed temporarily.")
	if err != nil {
		return err
	}
	if !exhausted {
		s.ScheduleDispatcher(ctx, time.Second)
		return nil
	}
	return cause
}

func (s *AdminResourceBulkService) ScheduleDispatcher(ctx context.Context, delay time.Duration) {
	if s == nil || s.queue == nil {
		return
	}
	_ = s.queue.EnqueueAdminResourceBulkDispatcher(ctx, delay)
}

func (s *AdminResourceCommandService) validateOneForBulk(ctx context.Context, resourceID uint, requestID, path string) (bool, string, error) {
	if s == nil || s.repo == nil || s.validation == nil {
		return false, "", domain.ErrResourceDependency
	}
	created := false
	err := s.repo.WithTx(ctx, func(txCtx context.Context) error {
		root, resource, err := s.repo.LockAdminMicrosoft(txCtx, resourceID)
		if err != nil {
			return err
		}
		if resource.Status == domain.MicrosoftStatusDeleted || resource.Status == domain.MicrosoftStatusDisabled {
			return domain.ErrInvalidResourceStatus
		}
		_, wasCreated, err := s.createValidationTx(txCtx, root, resource, requestID, path)
		created = wasCreated
		return err
	})
	if err != nil {
		if errors.Is(err, domain.ErrResourceNotFound) {
			return false, "not_found", nil
		}
		if errors.Is(err, domain.ErrInvalidResourceStatus) {
			return false, "invalid_state", nil
		}
		return false, "", err
	}
	if !created {
		return false, "active_task_reused", nil
	}
	return true, "", nil
}

func (s *AdminResourceCommandService) applyStateOneForBulk(ctx context.Context, command AdminMicrosoftStateCommand, resourceID uint) (bool, string, error) {
	changed := false
	err := s.repo.WithTx(ctx, func(txCtx context.Context) error {
		root, resource, err := s.repo.LockAdminMicrosoft(txCtx, resourceID)
		if err != nil {
			return err
		}
		beforeStatus, beforeForSale := resource.Status, resource.ForSale
		switch command {
		case AdminMicrosoftPublish:
			if _, err := s.validateOwner(txCtx, root.OwnerUserID, true); err != nil {
				return err
			}
			err = resource.PublishAdmin()
		case AdminMicrosoftUnpublish:
			err = resource.UnpublishAdmin()
		case AdminMicrosoftDelete:
			if s.allocations == nil {
				return domain.ErrResourceDependency
			}
			if err := s.allocations.AssertNoActiveAllocations(txCtx, []uint{root.ID}); err != nil {
				return err
			}
			err = resource.DeleteAdmin()
		default:
			return domain.ErrInvalidResourceCommand
		}
		if err != nil {
			return err
		}
		changed = beforeStatus != resource.Status || beforeForSale != resource.ForSale
		if !changed {
			return nil
		}
		return s.repo.SaveAdminMicrosoft(txCtx, root, resource, root.Version)
	})
	if err != nil {
		switch {
		case errors.Is(err, domain.ErrResourceNotFound):
			return false, "not_found", nil
		case errors.Is(err, domain.ErrInvalidResourceStatus):
			return false, "invalid_state", nil
		case errors.Is(err, domain.ErrInvalidResourceOwner):
			return false, "owner_ineligible", nil
		case errors.Is(err, domain.ErrResourceHasAllocation):
			return false, "active_allocation", nil
		default:
			return false, "", err
		}
	}
	if !changed {
		return false, "already_target", nil
	}
	return true, "", nil
}

func validAdminBulkAction(action AdminResourceBulkAction) bool {
	switch action {
	case AdminResourceBulkValidate, AdminResourceBulkPublish, AdminResourceBulkUnpublish, AdminResourceBulkDelete:
		return true
	default:
		return false
	}
}

func normalizeAdminBulkSelection(selection AdminResourceBulkSelection) (AdminResourceBulkSelection, error) {
	switch selection.Mode {
	case AdminResourceBulkIDs:
		selection.ResourceIDs = uniqueAdminResourceIDs(selection.ResourceIDs)
		if len(selection.ResourceIDs) == 0 {
			return selection, domain.ErrResourceNotFound
		}
		if len(selection.ResourceIDs) > AdminResourceBulkMaxExplicitIDs {
			return selection, domain.ErrResourceSelectionTooLarge
		}
		selection.Filter = AdminResourceBulkFilterValue{}
	case AdminResourceBulkFilter:
		filter, err := normalizeAdminMicrosoftFilter(selection.Filter.ListFilter())
		if err != nil {
			return selection, err
		}
		selection.ResourceIDs = nil
		selection.Filter = AdminResourceBulkFilterValue{
			Search: filter.Search, Suffix: filter.Suffix, Status: filter.Status,
			ForSale: filter.ForSale, LongLived: filter.LongLived, GraphAvailable: filter.GraphAvailable,
			TokenHealth: filter.TokenHealth, CreatedFrom: filter.CreatedFrom, CreatedTo: filter.CreatedTo,
		}
	default:
		return selection, domain.ErrInvalidResourceCommand
	}
	return selection, nil
}

func adminBulkFingerprint(action AdminResourceBulkAction, selection AdminResourceBulkSelection) (string, error) {
	payload, err := json.Marshal(struct {
		Action    AdminResourceBulkAction    `json:"action"`
		Selection AdminResourceBulkSelection `json:"selection"`
	}{Action: action, Selection: selection})
	if err != nil {
		return "", fmt.Errorf("marshal admin bulk fingerprint: %w", err)
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:]), nil
}

func cloneAdminBulkReasons(values map[string]int64) map[string]int64 {
	result := make(map[string]int64, len(values))
	for key, value := range values {
		result[key] = value
	}
	return result
}
