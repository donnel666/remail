package app

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/donnel666/remail/internal/core/domain"
	governancedomain "github.com/donnel666/remail/internal/governance/domain"
)

type AdminResourceBulkAction string

const (
	AdminResourceBulkValidate  AdminResourceBulkAction = "validate"
	AdminResourceBulkAlias     AdminResourceBulkAction = "alias"
	AdminResourceBulkHistory   AdminResourceBulkAction = "history"
	AdminResourceBulkToken     AdminResourceBulkAction = "token"
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

// AdminResourceBulkCommand is the acceptance response only. Bulk execution
// state is deliberately not durable: Redis owns the live cursor while the
// Microsoft resource rows remain the business source of truth.
type AdminResourceBulkCommand struct {
	ID             uint64
	Action         AdminResourceBulkAction
	Status         string
	MatchedCount   int
	ProcessedCount int
	AffectedCount  int
	SkippedCount   int
	ReasonCounts   map[string]int64
	Attempts       int
	MaxAttempts    int
	RequestID      string
	StartedAt      *time.Time
	FinishedAt     *time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// AdminResourceBulkTask is a Redis-only cursor. BatchID identifies the
// operator/idempotency-key scope; RequestFingerprint detects conflicting
// replays while the lease is alive.
type AdminResourceBulkTask struct {
	BatchID            string                     `json:"batchId"`
	ClaimToken         string                     `json:"claimToken,omitempty"`
	RequestFingerprint string                     `json:"requestFingerprint"`
	CommandID          uint64                     `json:"commandId"`
	Action             AdminResourceBulkAction    `json:"action"`
	Selection          AdminResourceBulkSelection `json:"selection"`
	AfterID            uint                       `json:"afterId"`
	ThroughID          uint                       `json:"throughId"`
	OperatorUserID     uint                       `json:"operatorUserId"`
	RequestID          string                     `json:"requestId"`
	Path               string                     `json:"path"`
}

type AdminResourceBulkPageResult struct {
	Affected  int
	Skipped   int
	AfterID   uint
	ThroughID uint
	Done      bool
}

// AdminResourceBulkRepository performs read-only, bounded candidate paging.
// It stores no task or progress rows.
type AdminResourceBulkRepository interface {
	MaxCandidateID(ctx context.Context, filter AdminResourceBulkFilterValue, now time.Time) (uint, error)
	ListCandidateIDs(ctx context.Context, filter AdminResourceBulkFilterValue, afterID, throughID uint, limit int, now time.Time) ([]uint, error)
}

type AdminResourceBulkQueue interface {
	EnqueueAdminResourceBulk(ctx context.Context, task AdminResourceBulkTask) (accepted bool, err error)
	RefreshAdminResourceBulk(ctx context.Context, task AdminResourceBulkTask) (owned bool, err error)
	ReleaseAdminResourceBulk(ctx context.Context, task AdminResourceBulkTask) error
}

type AdminResourceMaintenanceCommand struct {
	Action         AdminResourceBulkAction
	ResourceID     uint
	OperatorUserID uint
	IdempotencyKey string
	RequestID      string
	Path           string
}

type AdminResourceMaintenancePort interface {
	SubmitAdminResourceMaintenance(ctx context.Context, command AdminResourceMaintenanceCommand) (skipReason string, err error)
}

type AdminResourceBulkService struct {
	repo        AdminResourceBulkRepository
	queue       AdminResourceBulkQueue
	commands    *AdminResourceCommandService
	maintenance AdminResourceMaintenancePort
	now         func() time.Time
}

const (
	AdminResourceBulkMaxExplicitIDs = 1000
	adminResourceBulkPageSize       = 100
)

func NewAdminResourceBulkService(repo AdminResourceBulkRepository, queue AdminResourceBulkQueue, commands *AdminResourceCommandService) *AdminResourceBulkService {
	return &AdminResourceBulkService{repo: repo, queue: queue, commands: commands, now: func() time.Time { return time.Now().UTC() }}
}

func (s *AdminResourceBulkService) SetMaintenancePort(port AdminResourceMaintenancePort) {
	if s != nil {
		s.maintenance = port
	}
}

func (s *AdminResourceBulkService) Submit(ctx context.Context, action AdminResourceBulkAction, selection AdminResourceBulkSelection, operatorUserID uint, idempotencyKey, requestID, path string) (*AdminResourceBulkCommand, bool, error) {
	if s == nil || s.repo == nil || s.queue == nil || s.commands == nil || operatorUserID == 0 || !validAdminBulkAction(action) {
		return nil, false, domain.ErrInvalidResourceCommand
	}
	normalized, err := normalizeAdminBulkSelection(selection)
	if err != nil {
		return nil, false, err
	}
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if idempotencyKey == "" || len(idempotencyKey) > 128 {
		return nil, false, domain.ErrInvalidResourceCommand
	}
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

	identity := sha256.Sum256([]byte(fmt.Sprintf("%d:%s", operatorUserID, idempotencyKey)))
	task := AdminResourceBulkTask{
		BatchID:            "admin-resource-bulk:" + hex.EncodeToString(identity[:]),
		RequestFingerprint: fingerprint,
		CommandID:          binary.BigEndian.Uint64(identity[:8]),
		Action:             action,
		Selection:          normalized,
		OperatorUserID:     operatorUserID,
		RequestID:          strings.TrimSpace(requestID),
		Path:               strings.TrimSpace(path),
	}
	if task.CommandID == 0 {
		task.CommandID = 1
	}
	accepted, err := s.queue.EnqueueAdminResourceBulk(ctx, task)
	if err != nil {
		return nil, false, err
	}
	if accepted && s.commands.logs != nil {
		if logErr := s.commands.logs.Create(ctx, &governancedomain.OperationLog{
			OperatorUserID: operatorUserID,
			OperationType:  "core.admin_resource." + string(action) + "_bulk",
			ResourceType:   "microsoft_resource",
			ResourceID:     "batch",
			Path:           task.Path,
			Result:         "success",
			SafeSummary:    "Microsoft resource batch command accepted.",
			RequestID:      task.RequestID,
		}); logErr != nil {
			slog.Warn("admin Microsoft bulk acceptance audit log failed", "operator_user_id", operatorUserID, "action", action, "error", logErr)
		}
	}
	now := s.now()
	matched := 0
	if normalized.Mode == AdminResourceBulkIDs {
		matched = len(normalized.ResourceIDs)
	}
	return &AdminResourceBulkCommand{
		ID: task.CommandID, Action: action, Status: "queued", MatchedCount: matched,
		ReasonCounts: map[string]int64{}, MaxAttempts: 1, RequestID: task.RequestID,
		CreatedAt: now, UpdatedAt: now,
	}, !accepted, nil
}

func (s *AdminResourceBulkService) Process(ctx context.Context, task AdminResourceBulkTask) error {
	if s == nil || s.repo == nil || s.queue == nil || s.commands == nil || strings.TrimSpace(task.BatchID) == "" ||
		strings.TrimSpace(task.ClaimToken) == "" || strings.TrimSpace(task.RequestFingerprint) == "" || task.OperatorUserID == 0 || !validAdminBulkAction(task.Action) {
		return domain.ErrInvalidResourceCommand
	}
	owned, err := s.queue.RefreshAdminResourceBulk(ctx, task)
	if err != nil || !owned {
		return err
	}
	page, err := s.applyPage(ctx, task, adminResourceBulkPageSize)
	if err != nil {
		return err
	}
	if page.Done {
		return s.queue.ReleaseAdminResourceBulk(ctx, task)
	}
	task.AfterID = page.AfterID
	task.ThroughID = page.ThroughID
	_, err = s.queue.EnqueueAdminResourceBulk(ctx, task)
	return err
}

func (s *AdminResourceBulkService) applyPage(ctx context.Context, task AdminResourceBulkTask, limit int) (*AdminResourceBulkPageResult, error) {
	result := &AdminResourceBulkPageResult{AfterID: task.AfterID, ThroughID: task.ThroughID}
	var ids []uint
	var err error
	switch task.Selection.Mode {
	case AdminResourceBulkIDs:
		ids = adminResourceBulkIDPage(task.Selection.ResourceIDs, task.AfterID, limit+1)
	case AdminResourceBulkFilter:
		if result.ThroughID == 0 {
			result.ThroughID, err = s.repo.MaxCandidateID(ctx, task.Selection.Filter, s.now())
			if err != nil {
				return nil, err
			}
			if result.ThroughID == 0 {
				result.Done = true
				return result, nil
			}
		}
		ids, err = s.repo.ListCandidateIDs(ctx, task.Selection.Filter, task.AfterID, result.ThroughID, limit+1, s.now())
		if err != nil {
			return nil, err
		}
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
	checkpoint := ids[len(ids)-1]
	if checkpoint <= task.AfterID {
		return nil, fmt.Errorf("admin resource bulk batch made no progress past id %d", task.AfterID)
	}

	if isAdminResourceMaintenanceAction(task.Action) {
		if s.maintenance == nil {
			return nil, domain.ErrResourceDependency
		}
		for _, resourceID := range ids {
			reason, eligibilityErr := s.commands.maintenanceEligibilityForBulk(ctx, task.Action, resourceID)
			if eligibilityErr != nil {
				return nil, eligibilityErr
			}
			if reason != "" {
				result.Skipped++
				continue
			}
			reason, itemErr := s.maintenance.SubmitAdminResourceMaintenance(ctx, AdminResourceMaintenanceCommand{
				Action: task.Action, ResourceID: resourceID, OperatorUserID: task.OperatorUserID,
				IdempotencyKey: fmt.Sprintf("bulk:%d:%s:%d", task.CommandID, task.Action, resourceID),
				RequestID:      task.RequestID, Path: task.Path,
			})
			if itemErr != nil {
				return nil, itemErr
			}
			if reason == "" {
				result.Affected++
			} else {
				result.Skipped++
			}
		}
		result.AfterID = checkpoint
		return result, nil
	}

	err = s.commands.repo.WithTx(ctx, func(txCtx context.Context) error {
		for _, resourceID := range ids {
			var changed bool
			var itemErr error
			if task.Action == AdminResourceBulkValidate {
				changed, _, itemErr = s.commands.validateOneForBulk(txCtx, resourceID)
			} else {
				changed, _, itemErr = s.commands.applyStateOneForBulk(txCtx, AdminMicrosoftStateCommand(task.Action), resourceID)
			}
			if itemErr != nil {
				return itemErr
			}
			if changed {
				result.Affected++
			} else {
				result.Skipped++
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if task.Action == AdminResourceBulkValidate && result.Affected > 0 && s.commands.validation != nil {
		s.commands.validation.ScheduleDispatcher(ctx, 0)
	}
	result.AfterID = checkpoint
	return result, nil
}

func (s *AdminResourceBulkService) ReleaseBatch(ctx context.Context, task AdminResourceBulkTask) error {
	if s == nil || s.queue == nil {
		return nil
	}
	return s.queue.ReleaseAdminResourceBulk(ctx, task)
}

func adminResourceBulkIDPage(ids []uint, afterID uint, limit int) []uint {
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

func (s *AdminResourceCommandService) maintenanceEligibilityForBulk(ctx context.Context, action AdminResourceBulkAction, resourceID uint) (string, error) {
	if s == nil || s.repo == nil || resourceID == 0 {
		return "", domain.ErrResourceDependency
	}
	var reason string
	err := s.repo.WithTx(ctx, func(txCtx context.Context) error {
		_, resource, err := s.repo.LockAdminMicrosoft(txCtx, resourceID)
		if err != nil {
			return err
		}
		if resource.Status == domain.MicrosoftStatusDeleted {
			reason = "not_found"
			return nil
		}
		switch action {
		case AdminResourceBulkAlias:
			if resource.Status != domain.MicrosoftStatusNormal {
				reason = "invalid_state"
			}
		case AdminResourceBulkHistory:
			if resource.Status != domain.MicrosoftStatusNormal {
				reason = "invalid_state"
			} else if strings.TrimSpace(resource.ClientID) == "" || strings.TrimSpace(resource.RefreshToken) == "" {
				reason = "credentials_missing"
			}
		case AdminResourceBulkToken:
			if strings.TrimSpace(resource.ClientID) == "" || strings.TrimSpace(resource.RefreshToken) == "" {
				reason = "credentials_missing"
			}
		default:
			return domain.ErrInvalidResourceCommand
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, domain.ErrResourceNotFound) {
			return "not_found", nil
		}
		return "", err
	}
	return reason, nil
}

func (s *AdminResourceCommandService) validateOneForBulk(ctx context.Context, resourceID uint) (bool, string, error) {
	if s == nil || s.repo == nil || s.validation == nil {
		return false, "", domain.ErrResourceDependency
	}
	accepted := false
	err := s.repo.WithTx(ctx, func(txCtx context.Context) error {
		root, resource, err := s.repo.LockAdminMicrosoft(txCtx, resourceID)
		if err != nil {
			return err
		}
		changed, err := resource.QueueValidationAdmin()
		if err != nil {
			return err
		}
		if changed {
			if err := s.repo.SaveAdminMicrosoft(txCtx, root, resource, root.Version); err != nil {
				return err
			}
		}
		accepted = resource.Status != domain.MicrosoftStatusValidating
		return nil
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
	if !accepted {
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
	case AdminResourceBulkValidate, AdminResourceBulkAlias, AdminResourceBulkHistory, AdminResourceBulkToken,
		AdminResourceBulkPublish, AdminResourceBulkUnpublish, AdminResourceBulkDelete:
		return true
	default:
		return false
	}
}

func isAdminResourceMaintenanceAction(action AdminResourceBulkAction) bool {
	switch action {
	case AdminResourceBulkAlias, AdminResourceBulkHistory, AdminResourceBulkToken:
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
