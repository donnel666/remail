package app

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	AdminTaskDefaultLimit = 20
	AdminTaskMaxLimit     = 100

	AdminTaskBizMicrosoftResource       = "microsoft_resource"
	AdminTaskBizDomainResource          = "domain_resource"
	AdminTaskBizMicrosoftResourceImport = "microsoft_resource_import"
	AdminTaskBizMicrosoftResourceBulk   = "microsoft_resource_bulk"

	AdminTaskKindImport        = "import"
	AdminTaskKindAlias         = "alias"
	AdminTaskKindToken         = "token"
	AdminTaskKindFetch         = "fetch"
	AdminTaskKindHistory       = "history"
	AdminTaskKindBulkValidate  = "bulk_validation"
	AdminTaskKindBulkAlias     = "bulk_alias"
	AdminTaskKindBulkHistory   = "bulk_history"
	AdminTaskKindBulkToken     = "bulk_token"
	AdminTaskKindBulkPublish   = "bulk_publish"
	AdminTaskKindBulkUnpublish = "bulk_unpublish"
	AdminTaskKindBulkDelete    = "bulk_delete"

	AdminTaskStatusQueued    = "queued"
	AdminTaskStatusRunning   = "running"
	AdminTaskStatusSucceeded = "succeeded"
	AdminTaskStatusFailed    = "failed"
	AdminTaskStatusUncertain = "uncertain"
	AdminTaskStatusCanceled  = "canceled"

	AdminTaskSourceImport        = "import"
	AdminTaskSourceAlias         = "alias"
	AdminTaskSourceAliasSchedule = "alias_schedule"
	AdminTaskSourceToken         = "token"
	AdminTaskSourceFetch         = "fetch"
)

var (
	ErrInvalidAdminTaskQuery = errors.New("invalid administrator task query")
	ErrAdminTaskNotFound     = errors.New("administrator task not found")
	ErrAdminTaskResourceGone = errors.New("administrator task resource not found")
	ErrAdminTaskUnavailable  = errors.New("administrator task view unavailable")
)

// AdminTaskRef is a stable, source-qualified reference. Source IDs are only
// identifiers; table names, dispatch tokens, claims and fencing facts never
// cross this application boundary.
type AdminTaskRef struct {
	Source string
	ID     uint64
}

func (r AdminTaskRef) String() string {
	if !isAdminTaskSource(r.Source) || r.ID == 0 {
		return ""
	}
	return r.Source + ":" + strconv.FormatUint(r.ID, 10)
}

func ParseAdminTaskRef(value string) (AdminTaskRef, error) {
	value = strings.TrimSpace(value)
	if len(value) < 3 || len(value) > 160 || strings.Count(value, ":") != 1 {
		return AdminTaskRef{}, ErrInvalidAdminTaskQuery
	}
	parts := strings.SplitN(value, ":", 2)
	if !isAdminTaskSource(parts[0]) || parts[1] == "" {
		return AdminTaskRef{}, ErrInvalidAdminTaskQuery
	}
	id, err := strconv.ParseUint(parts[1], 10, 64)
	if err != nil || id == 0 {
		return AdminTaskRef{}, ErrInvalidAdminTaskQuery
	}
	return AdminTaskRef{Source: parts[0], ID: id}, nil
}

func isAdminTaskSource(value string) bool {
	switch value {
	case AdminTaskSourceImport,
		AdminTaskSourceAlias,
		AdminTaskSourceAliasSchedule,
		AdminTaskSourceToken,
		AdminTaskSourceFetch:
		return true
	default:
		return false
	}
}

type AdminTaskReasonCount struct {
	Reason string
	Count  int64
}

type AdminTaskProgress struct {
	Total        int64
	Processed    int64
	Succeeded    int64
	Skipped      int64
	Failed       int64
	ReasonCounts []AdminTaskReasonCount
}

// AdminTaskView is the safe published language shared by the administrator
// Tasks API and read-only consumer adapters. Source and SourceID are internal
// routing facts and are deliberately omitted by API response mappers.
type AdminTaskView struct {
	Ref                AdminTaskRef
	BizType            string
	BizID              uint64
	Kind               string
	Status             string
	Attempts           int
	MaxAttempts        int
	CredentialRevision *uint64
	QueuedAt           time.Time
	StartedAt          *time.Time
	FinishedAt         *time.Time
	UpdatedAt          time.Time
	Progress           *AdminTaskProgress
}

func (t AdminTaskView) TaskID() string {
	return t.Ref.String()
}

type AdminTaskListFilter struct {
	BizType string
	BizID   uint
	Kind    string
	Status  string
	Offset  int
	Limit   int
}

type AdminTaskListResult struct {
	Items     []AdminTaskView
	Total     int64
	Succeeded int64
	Offset    int
	Limit     int
}

type AdminTaskViewRepository interface {
	MicrosoftResourceExists(ctx context.Context, resourceID uint) (bool, error)
	DomainResourceExists(ctx context.Context, resourceID uint) (bool, error)
	ListForMicrosoftResource(ctx context.Context, filter AdminTaskListFilter) ([]AdminTaskView, int64, int64, error)
	ListForDomainResource(ctx context.Context, filter AdminTaskListFilter) ([]AdminTaskView, int64, int64, error)
	FindByRef(ctx context.Context, ref AdminTaskRef) (*AdminTaskView, error)
}

type AdminTaskQueryService struct {
	repo AdminTaskViewRepository
}

func NewAdminTaskQueryService(repo AdminTaskViewRepository) *AdminTaskQueryService {
	return &AdminTaskQueryService{repo: repo}
}

func (s *AdminTaskQueryService) List(ctx context.Context, filter AdminTaskListFilter) (*AdminTaskListResult, error) {
	if s == nil || s.repo == nil {
		return nil, ErrAdminTaskUnavailable
	}
	normalized, err := normalizeAdminTaskListFilter(filter)
	if err != nil {
		return nil, err
	}
	var exists bool
	var items []AdminTaskView
	var total, succeeded int64
	switch normalized.BizType {
	case AdminTaskBizMicrosoftResource:
		exists, err = s.repo.MicrosoftResourceExists(ctx, normalized.BizID)
	case AdminTaskBizDomainResource:
		exists, err = s.repo.DomainResourceExists(ctx, normalized.BizID)
	}
	if err != nil {
		return nil, fmt.Errorf("%w: check task resource", ErrAdminTaskUnavailable)
	}
	if !exists {
		return nil, ErrAdminTaskResourceGone
	}
	switch normalized.BizType {
	case AdminTaskBizMicrosoftResource:
		items, total, succeeded, err = s.repo.ListForMicrosoftResource(ctx, normalized)
	case AdminTaskBizDomainResource:
		items, total, succeeded, err = s.repo.ListForDomainResource(ctx, normalized)
	}
	if err != nil {
		return nil, fmt.Errorf("%w: list normalized tasks", ErrAdminTaskUnavailable)
	}
	if items == nil {
		items = make([]AdminTaskView, 0)
	}
	return &AdminTaskListResult{
		Items:     items,
		Total:     total,
		Succeeded: succeeded,
		Offset:    normalized.Offset,
		Limit:     normalized.Limit,
	}, nil
}

func (s *AdminTaskQueryService) Get(ctx context.Context, taskID string) (*AdminTaskView, error) {
	if s == nil || s.repo == nil {
		return nil, ErrAdminTaskUnavailable
	}
	ref, err := ParseAdminTaskRef(taskID)
	if err != nil {
		return nil, err
	}
	task, err := s.repo.FindByRef(ctx, ref)
	if errors.Is(err, ErrAdminTaskNotFound) {
		return nil, err
	}
	if err != nil {
		return nil, fmt.Errorf("%w: find normalized task", ErrAdminTaskUnavailable)
	}
	if task == nil {
		return nil, ErrAdminTaskNotFound
	}
	return task, nil
}

func normalizeAdminTaskListFilter(filter AdminTaskListFilter) (AdminTaskListFilter, error) {
	filter.BizType = strings.TrimSpace(filter.BizType)
	filter.Kind = strings.TrimSpace(filter.Kind)
	filter.Status = strings.TrimSpace(filter.Status)
	if (filter.BizType != AdminTaskBizMicrosoftResource && filter.BizType != AdminTaskBizDomainResource) || filter.BizID == 0 || filter.Offset < 0 {
		return AdminTaskListFilter{}, ErrInvalidAdminTaskQuery
	}
	if filter.Limit == 0 {
		filter.Limit = AdminTaskDefaultLimit
	}
	if filter.Limit < 1 || filter.Limit > AdminTaskMaxLimit {
		return AdminTaskListFilter{}, ErrInvalidAdminTaskQuery
	}
	if filter.Kind != "" && !isAdminTaskKind(filter.Kind) {
		return AdminTaskListFilter{}, ErrInvalidAdminTaskQuery
	}
	if filter.Status != "" && !isAdminTaskStatus(filter.Status) {
		return AdminTaskListFilter{}, ErrInvalidAdminTaskQuery
	}
	return filter, nil
}

func isAdminTaskKind(value string) bool {
	switch value {
	case AdminTaskKindImport,
		AdminTaskKindAlias,
		AdminTaskKindToken,
		AdminTaskKindFetch,
		AdminTaskKindHistory,
		AdminTaskKindBulkValidate,
		AdminTaskKindBulkAlias,
		AdminTaskKindBulkHistory,
		AdminTaskKindBulkToken,
		AdminTaskKindBulkPublish,
		AdminTaskKindBulkUnpublish,
		AdminTaskKindBulkDelete:
		return true
	default:
		return false
	}
}

func isAdminTaskStatus(value string) bool {
	switch value {
	case AdminTaskStatusQueued,
		AdminTaskStatusRunning,
		AdminTaskStatusSucceeded,
		AdminTaskStatusFailed,
		AdminTaskStatusUncertain,
		AdminTaskStatusCanceled:
		return true
	default:
		return false
	}

}
