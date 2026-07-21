package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/donnel666/remail/internal/governance/domain"
)

const (
	AdminLogDefaultLimit = 20
	AdminLogMaxLimit     = 100

	AdminLogCategorySystem    = "system"
	AdminLogCategoryOperation = "operation"

	AdminLogLevelInfo    = "info"
	AdminLogLevelWarning = "warning"
	AdminLogLevelError   = "error"

	AdminOperationResultSuccess = "success"
	AdminOperationResultFailure = "failure"
)

var (
	ErrInvalidAdminLogQuery = errors.New("invalid administrator log query")
	ErrAdminLogUnavailable  = errors.New("administrator log view unavailable")
)

type AdminLogListFilter struct {
	Level  string
	Result string
	Search string
	From   *time.Time
	To     *time.Time
	Offset int
	Limit  int
}

type AdminSystemLogView struct {
	ID        uint64
	Level     string
	Module    string
	EventType string
	RequestID string
	BizType   string
	BizID     string
	Message   string
	Detail    string
	CreatedAt time.Time
}

type AdminOperationLogView struct {
	ID             uint64
	OperatorUserID uint
	Operator       string
	OperationType  string
	ResourceType   string
	ResourceID     string
	Path           string
	Result         string
	SafeSummary    string
	RequestID      string
	CreatedAt      time.Time
}

type AdminLogFacets struct {
	System    int64
	Operation int64
}

type AdminSystemLogListResult struct {
	Items  []AdminSystemLogView
	Total  int64
	Facets AdminLogFacets
	Offset int
	Limit  int
}

type AdminOperationLogListResult struct {
	Items  []AdminOperationLogView
	Total  int64
	Facets AdminLogFacets
	Offset int
	Limit  int
}

type AdminLogCleanupCommand struct {
	Category       string
	Before         time.Time
	OperatorUserID uint
	Path           string
	RequestID      string
}

type AdminLogRepository interface {
	ListSystemLogs(ctx context.Context, filter AdminLogListFilter) ([]AdminSystemLogView, int64, error)
	ListOperationLogs(ctx context.Context, filter AdminLogListFilter) ([]AdminOperationLogView, int64, error)
	CountSystemLogs(ctx context.Context) (int64, error)
	CountOperationLogs(ctx context.Context) (int64, error)
	CleanupLogs(ctx context.Context, category string, before time.Time, audit *domain.OperationLog) (int64, error)
}

type AdminLogService struct {
	repo AdminLogRepository
}

func NewAdminLogService(repo AdminLogRepository) *AdminLogService {
	return &AdminLogService{repo: repo}
}

func (s *AdminLogService) ListSystem(ctx context.Context, filter AdminLogListFilter) (*AdminSystemLogListResult, error) {
	if s == nil || s.repo == nil {
		return nil, ErrAdminLogUnavailable
	}
	filter, err := normalizeAdminLogFilter(filter, AdminLogCategorySystem)
	if err != nil {
		return nil, err
	}
	items, total, err := s.repo.ListSystemLogs(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("%w: list system logs", ErrAdminLogUnavailable)
	}
	facets, err := s.facets(ctx)
	if err != nil {
		return nil, err
	}
	if items == nil {
		items = make([]AdminSystemLogView, 0)
	}
	return &AdminSystemLogListResult{Items: items, Total: total, Facets: facets, Offset: filter.Offset, Limit: filter.Limit}, nil
}

func (s *AdminLogService) ListOperations(ctx context.Context, filter AdminLogListFilter) (*AdminOperationLogListResult, error) {
	if s == nil || s.repo == nil {
		return nil, ErrAdminLogUnavailable
	}
	filter, err := normalizeAdminLogFilter(filter, AdminLogCategoryOperation)
	if err != nil {
		return nil, err
	}
	items, total, err := s.repo.ListOperationLogs(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("%w: list operation logs", ErrAdminLogUnavailable)
	}
	facets, err := s.facets(ctx)
	if err != nil {
		return nil, err
	}
	if items == nil {
		items = make([]AdminOperationLogView, 0)
	}
	return &AdminOperationLogListResult{Items: items, Total: total, Facets: facets, Offset: filter.Offset, Limit: filter.Limit}, nil
}

func (s *AdminLogService) Cleanup(ctx context.Context, command AdminLogCleanupCommand) (int64, error) {
	if s == nil || s.repo == nil {
		return 0, ErrAdminLogUnavailable
	}
	command.Category = strings.TrimSpace(command.Category)
	command.Path = strings.TrimSpace(command.Path)
	command.RequestID = strings.TrimSpace(command.RequestID)
	if command.Before.IsZero() || command.OperatorUserID == 0 || (command.Category != AdminLogCategorySystem && command.Category != AdminLogCategoryOperation) {
		return 0, ErrInvalidAdminLogQuery
	}

	resourceType := "system_log"
	operationType := "governance.system_logs.cleanup"
	displayName := "system log"
	if command.Category == AdminLogCategoryOperation {
		resourceType = "operation_log"
		operationType = "governance.operation_logs.cleanup"
		displayName = "operation log"
	}
	audit := &domain.OperationLog{
		OperatorUserID: command.OperatorUserID,
		OperationType:  operationType,
		ResourceType:   resourceType,
		ResourceID:     command.Before.Format(time.RFC3339),
		Path:           command.Path,
		Result:         AdminOperationResultSuccess,
		SafeSummary:    fmt.Sprintf("%s entries before the selected cutoff were removed.", displayName),
		RequestID:      command.RequestID,
	}
	removed, err := s.repo.CleanupLogs(ctx, command.Category, command.Before, audit)
	if err != nil {
		return 0, fmt.Errorf("%w: cleanup %s logs", ErrAdminLogUnavailable, command.Category)
	}
	return removed, nil
}

func (s *AdminLogService) facets(ctx context.Context) (AdminLogFacets, error) {
	system, err := s.repo.CountSystemLogs(ctx)
	if err != nil {
		return AdminLogFacets{}, fmt.Errorf("%w: count system logs", ErrAdminLogUnavailable)
	}
	operation, err := s.repo.CountOperationLogs(ctx)
	if err != nil {
		return AdminLogFacets{}, fmt.Errorf("%w: count operation logs", ErrAdminLogUnavailable)
	}
	return AdminLogFacets{System: system, Operation: operation}, nil
}

func normalizeAdminLogFilter(filter AdminLogListFilter, category string) (AdminLogListFilter, error) {
	filter.Level = strings.TrimSpace(filter.Level)
	filter.Result = strings.TrimSpace(filter.Result)
	filter.Search = strings.TrimSpace(filter.Search)
	if filter.Offset < 0 || len([]rune(filter.Search)) > 200 {
		return AdminLogListFilter{}, ErrInvalidAdminLogQuery
	}
	if filter.Limit == 0 {
		filter.Limit = AdminLogDefaultLimit
	}
	if filter.Limit < 1 || filter.Limit > AdminLogMaxLimit {
		return AdminLogListFilter{}, ErrInvalidAdminLogQuery
	}
	if filter.From != nil && filter.To != nil && filter.From.After(*filter.To) {
		return AdminLogListFilter{}, ErrInvalidAdminLogQuery
	}
	switch category {
	case AdminLogCategorySystem:
		if filter.Result != "" || (filter.Level != "" && !isAdminLogLevel(filter.Level)) {
			return AdminLogListFilter{}, ErrInvalidAdminLogQuery
		}
	case AdminLogCategoryOperation:
		if filter.Level != "" || (filter.Result != "" && !isAdminOperationResult(filter.Result)) {
			return AdminLogListFilter{}, ErrInvalidAdminLogQuery
		}
	default:
		return AdminLogListFilter{}, ErrInvalidAdminLogQuery
	}
	return filter, nil
}

func isAdminLogLevel(value string) bool {
	switch value {
	case AdminLogLevelInfo, AdminLogLevelWarning, AdminLogLevelError:
		return true
	default:
		return false
	}
}

func isAdminOperationResult(value string) bool {
	switch value {
	case AdminOperationResultSuccess, AdminOperationResultFailure:
		return true
	default:
		return false
	}
}
