package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/donnel666/remail/internal/governance/domain"
	"github.com/stretchr/testify/require"
)

type adminLogRepoStub struct {
	systemItems         []AdminSystemLogView
	operationItems      []AdminOperationLogView
	systemTotal         int64
	operationTotal      int64
	systemCount         int64
	operationCount      int64
	cleanupCategory     string
	cleanupBefore       time.Time
	cleanupAudit        *domain.OperationLog
	cleanupRemoved      int64
	cleanupCalls        int
	cleanupErr          error
	lastSystemFilter    AdminLogListFilter
	lastOperationFilter AdminLogListFilter
}

func (s *adminLogRepoStub) ListSystemLogs(_ context.Context, filter AdminLogListFilter) ([]AdminSystemLogView, int64, error) {
	s.lastSystemFilter = filter
	return s.systemItems, s.systemTotal, nil
}

func (s *adminLogRepoStub) ListOperationLogs(_ context.Context, filter AdminLogListFilter) ([]AdminOperationLogView, int64, error) {
	s.lastOperationFilter = filter
	return s.operationItems, s.operationTotal, nil
}

func (s *adminLogRepoStub) CountSystemLogs(context.Context) (int64, error) {
	return s.systemCount, nil
}

func (s *adminLogRepoStub) CountOperationLogs(context.Context) (int64, error) {
	return s.operationCount, nil
}

func (s *adminLogRepoStub) CleanupLogs(_ context.Context, category string, before time.Time, audit *domain.OperationLog) (int64, error) {
	s.cleanupCalls++
	s.cleanupCategory = category
	s.cleanupBefore = before
	if audit != nil {
		copied := *audit
		s.cleanupAudit = &copied
	}
	return s.cleanupRemoved, s.cleanupErr
}

func TestAdminLogServiceListsSafeStreamsWithFacets(t *testing.T) {
	from := time.Date(2026, time.July, 1, 0, 0, 0, 0, time.UTC)
	repo := &adminLogRepoStub{
		systemItems:    []AdminSystemLogView{{ID: 11, Level: AdminLogLevelWarning}},
		systemTotal:    1,
		systemCount:    12,
		operationCount: 9,
	}
	service := NewAdminLogService(repo)

	result, err := service.ListSystem(context.Background(), AdminLogListFilter{
		Level: " warning ", Search: " request-7 ", From: &from, Offset: 20,
	})

	require.NoError(t, err)
	require.Len(t, result.Items, 1)
	require.Equal(t, int64(1), result.Total)
	require.Equal(t, AdminLogFacets{System: 12, Operation: 9}, result.Facets)
	require.Equal(t, AdminLogDefaultLimit, result.Limit)
	require.Equal(t, "warning", repo.lastSystemFilter.Level)
	require.Equal(t, "request-7", repo.lastSystemFilter.Search)
}

func TestAdminLogServiceValidatesStreamSpecificFilters(t *testing.T) {
	service := NewAdminLogService(&adminLogRepoStub{})
	from := time.Date(2026, time.July, 2, 0, 0, 0, 0, time.UTC)
	to := from.Add(-time.Second)

	for _, testCase := range []struct {
		name   string
		system bool
		filter AdminLogListFilter
	}{
		{name: "unknown level", system: true, filter: AdminLogListFilter{Level: "fatal"}},
		{name: "unknown result", filter: AdminLogListFilter{Result: "pending"}},
		{name: "invalid range", system: true, filter: AdminLogListFilter{From: &from, To: &to}},
		{name: "large page", filter: AdminLogListFilter{Limit: AdminLogMaxLimit + 1}},
		{name: "large search", system: true, filter: AdminLogListFilter{Search: string(make([]rune, 201))}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			var err error
			if testCase.system {
				_, err = service.ListSystem(context.Background(), testCase.filter)
			} else {
				_, err = service.ListOperations(context.Background(), testCase.filter)
			}
			require.ErrorIs(t, err, ErrInvalidAdminLogQuery)
		})
	}
}

func TestAdminLogCleanupIsScopedAndKeepsAnAuditRecord(t *testing.T) {
	cutoff := time.Date(2030, time.January, 1, 0, 0, 0, 0, time.UTC)
	for _, testCase := range []struct {
		name          string
		category      string
		wantRemoved   int64
		wantOperation string
	}{
		{
			name: "system", category: AdminLogCategorySystem,
			wantRemoved:   5003,
			wantOperation: "governance.system_logs.cleanup",
		},
		{
			name: "operation", category: AdminLogCategoryOperation,
			wantRemoved:   7,
			wantOperation: "governance.operation_logs.cleanup",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			repo := &adminLogRepoStub{cleanupRemoved: testCase.wantRemoved}
			service := NewAdminLogService(repo)

			removed, err := service.Cleanup(context.Background(), AdminLogCleanupCommand{
				Category: testCase.category, Before: cutoff, OperatorUserID: 7,
				Path: "/v1/admin/logs/" + testCase.category, RequestID: "req-cleanup",
			})

			require.NoError(t, err)
			require.Equal(t, testCase.wantRemoved, removed)
			require.Equal(t, 1, repo.cleanupCalls)
			require.Equal(t, testCase.category, repo.cleanupCategory)
			require.Equal(t, cutoff, repo.cleanupBefore)
			require.NotNil(t, repo.cleanupAudit)
			require.Equal(t, testCase.wantOperation, repo.cleanupAudit.OperationType)
			require.Equal(t, uint(7), repo.cleanupAudit.OperatorUserID)
			require.Equal(t, "success", repo.cleanupAudit.Result)
			require.Equal(t, "req-cleanup", repo.cleanupAudit.RequestID)
			require.NotEmpty(t, repo.cleanupAudit.SafeSummary)
		})
	}
}

func TestAdminLogCleanupDoesNotReportRolledBackRows(t *testing.T) {
	repo := &adminLogRepoStub{cleanupRemoved: 3, cleanupErr: errors.New("audit failed")}
	service := NewAdminLogService(repo)

	removed, err := service.Cleanup(context.Background(), AdminLogCleanupCommand{
		Category: AdminLogCategorySystem, Before: time.Now().Add(time.Hour), OperatorUserID: 7,
	})

	require.Zero(t, removed)
	require.ErrorIs(t, err, ErrAdminLogUnavailable)
}
