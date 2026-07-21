package infra

import (
	"context"
	"strings"
	"testing"
	"time"

	governanceapp "github.com/donnel666/remail/internal/governance/app"
	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestAdminLogRepoListsFilteredSafeLogsMySQL(t *testing.T) {
	db := newGovernanceMySQLTestDB(t)
	base := seedGovernanceLogs(t, db)
	repo := NewAdminLogRepo(db)

	from := base.Add(30 * time.Minute)
	systems, total, err := repo.ListSystemLogs(context.Background(), governanceapp.AdminLogListFilter{
		Level: "warning", Search: "authorization", From: &from, Limit: 20,
	})
	require.NoError(t, err)
	require.Equal(t, int64(1), total)
	require.Len(t, systems, 1)
	require.Equal(t, "resource.validation_failed", systems[0].EventType)
	require.Contains(t, systems[0].Detail, "category=authorization")
	require.Contains(t, systems[0].Detail, "password=***")
	for _, secret := range []string{"historical-password", "historical-refresh", "message.eml"} {
		require.NotContains(t, systems[0].Detail, secret)
	}

	operations, total, err := repo.ListOperationLogs(context.Background(), governanceapp.AdminLogListFilter{
		Result: "success", Search: "ops@example.com", Limit: 20,
	})
	require.NoError(t, err)
	require.Equal(t, int64(1), total)
	require.Len(t, operations, 1)
	require.Equal(t, "ops@example.com", operations[0].Operator)
	require.Equal(t, "core.resource.validate", operations[0].OperationType)

	systemCount, err := repo.CountSystemLogs(context.Background())
	require.NoError(t, err)
	operationCount, err := repo.CountOperationLogs(context.Background())
	require.NoError(t, err)
	require.Equal(t, int64(3), systemCount)
	require.Equal(t, int64(2), operationCount)

	var baselineCount int64
	require.NoError(t, db.Table("casbin_rule").
		Where("ptype = 'p' AND v1 = 'governance:log' AND v3 = 'allow'").
		Where("(v0 = 'role:admin' AND v2 = 'read') OR (v0 = 'role:super_admin' AND v2 IN ('read', 'operate'))").
		Count(&baselineCount).Error)
	require.Equal(t, int64(3), baselineCount)
	var adminOperate int64
	require.NoError(t, db.Table("casbin_rule").
		Where("ptype = 'p' AND v0 = 'role:admin' AND v1 = 'governance:log' AND v2 = 'operate' AND v3 = 'allow'").
		Count(&adminOperate).Error)
	require.Zero(t, adminOperate)
}

func TestAdminLogRepoCleanupStaysInsideSelectedTableMySQL(t *testing.T) {
	db := newGovernanceMySQLTestDB(t)
	seedGovernanceLogs(t, db)
	repo := NewAdminLogRepo(db)
	cutoff := time.Date(2030, time.January, 1, 0, 0, 0, 0, time.UTC)

	removed, err := repo.CleanupLogs(context.Background(), governanceapp.AdminLogCategorySystem, cutoff, cleanupAudit("governance.system_logs.cleanup"))
	require.NoError(t, err)
	require.Equal(t, int64(3), removed)
	systems, err := repo.CountSystemLogs(context.Background())
	require.NoError(t, err)
	operations, err := repo.CountOperationLogs(context.Background())
	require.NoError(t, err)
	require.Zero(t, systems)
	require.Equal(t, int64(3), operations)

	removed, err = repo.CleanupLogs(context.Background(), governanceapp.AdminLogCategoryOperation, cutoff, cleanupAudit("governance.operation_logs.cleanup"))
	require.NoError(t, err)
	require.Equal(t, int64(3), removed)
	operations, err = repo.CountOperationLogs(context.Background())
	require.NoError(t, err)
	require.Equal(t, int64(1), operations)
	var remaining OperationLogModel
	require.NoError(t, db.First(&remaining).Error)
	require.Equal(t, "governance.operation_logs.cleanup", remaining.OperationType)
}

func TestAdminLogRepoCleanupRollsBackWhenAuditFailsMySQL(t *testing.T) {
	db := newGovernanceMySQLTestDB(t)
	seedGovernanceLogs(t, db)
	repo := NewAdminLogRepo(db)
	audit := cleanupAudit(strings.Repeat("x", 101))

	removed, err := repo.CleanupLogs(context.Background(), governanceapp.AdminLogCategorySystem, time.Date(2030, time.January, 1, 0, 0, 0, 0, time.UTC), audit)

	require.Error(t, err)
	require.Zero(t, removed)
	systems, countErr := repo.CountSystemLogs(context.Background())
	require.NoError(t, countErr)
	operations, countErr := repo.CountOperationLogs(context.Background())
	require.NoError(t, countErr)
	require.Equal(t, int64(3), systems)
	require.Equal(t, int64(2), operations)
}

func cleanupAudit(operationType string) *governancedomain.OperationLog {
	return &governancedomain.OperationLog{
		OperatorUserID: 7102,
		OperationType:  operationType,
		ResourceType:   "system_log",
		ResourceID:     "2030-01-01T00:00:00Z",
		Path:           "/v1/admin/logs/system",
		Result:         "success",
		SafeSummary:    "Logs were removed.",
		RequestID:      "req-cleanup",
	}
}

func seedGovernanceLogs(t *testing.T, db *gorm.DB) time.Time {
	t.Helper()
	base := time.Date(2026, time.July, 20, 8, 0, 0, 0, time.UTC)
	require.NoError(t, db.Exec(`
INSERT INTO users(id, email, password_hash, status, role)
VALUES
    (7101, 'ops@example.com', 'hash', 'active', 'admin'),
    (7102, 'root@example.com', 'hash', 'active', 'super_admin')`).Error)
	require.NoError(t, db.Exec(`
INSERT INTO system_logs(level, module, event_type, request_id, biz_type, biz_id, message, detail, created_at)
VALUES
    ('info', 'governance', 'governance.retention_completed', 'req-1', 'retention', 'daily', 'Daily retention cleanup completed.', 'authorization cleanup completed', ?),
    ('warning', 'core', 'resource.validation_failed', 'req-2', 'resource', '61', 'Resource validation failed.', 'category=authorization password=historical-password refresh_token=historical-refresh mailtransport/inbound/2026/07/21/message.eml', ?),
    ('error', 'mailmatch', 'resource_fetch_failed', 'req-3', 'resource', '61', 'Resource mail fetch failed.', 'category=upstream_unavailable', ?)`,
		base, base.Add(time.Hour), base.Add(2*time.Hour)).Error)
	require.NoError(t, db.Exec(`
INSERT INTO operation_logs(operator_user_id, operation_type, resource_type, resource_id, path, result, safe_summary, request_id, created_at)
VALUES
    (7101, 'core.resource.validate', 'resource', '61', '/v1/admin/resources/:resourceId/validate', 'success', 'Resource validation accepted.', 'req-4', ?),
    (7102, 'iam.user.update', 'user', '99', '/v1/admin/users/:userId', 'failure', 'User update failed.', 'req-5', ?)`,
		base.Add(3*time.Hour), base.Add(4*time.Hour)).Error)
	return base
}
