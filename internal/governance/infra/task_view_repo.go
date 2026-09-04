package infra

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	governanceapp "github.com/donnel666/remail/internal/governance/app"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

// AdminTaskViewRepo composes read-only task facts owned by their source
// contexts. It never updates source rows and deliberately does not select raw
// errors, payloads, filters, object keys, candidates, claims, leases or fencing
// tokens.
type AdminTaskViewRepo struct {
	db    *gorm.DB
	redis redis.UniversalClient
}

func NewAdminTaskViewRepo(db *gorm.DB, redisClients ...redis.UniversalClient) *AdminTaskViewRepo {
	var redisClient redis.UniversalClient
	if len(redisClients) > 0 {
		redisClient = redisClients[0]
	}
	return &AdminTaskViewRepo{db: db, redis: redisClient}
}

const adminResourceBulkStatusKeyPrefix = "remail:core:admin-resource-bulk-status:"

func AdminResourceBulkStatusKey(commandID uint64) string {
	return adminResourceBulkStatusKeyPrefix + strconv.FormatUint(commandID, 10)
}

type adminTaskRow struct {
	Source             string          `gorm:"column:source"`
	SourceID           uint64          `gorm:"column:source_id"`
	ResourceScopeID    uint            `gorm:"column:resource_scope_id"`
	BizType            string          `gorm:"column:biz_type"`
	BizID              uint64          `gorm:"column:biz_id"`
	Kind               string          `gorm:"column:kind"`
	Status             string          `gorm:"column:status"`
	Attempts           int             `gorm:"column:attempts"`
	MaxAttempts        int             `gorm:"column:max_attempts"`
	CredentialRevision sql.NullInt64   `gorm:"column:credential_revision"`
	QueuedAt           adminTaskTime   `gorm:"column:queued_at"`
	StartedAt          adminTaskTime   `gorm:"column:started_at"`
	FinishedAt         adminTaskTime   `gorm:"column:finished_at"`
	UpdatedAt          adminTaskTime   `gorm:"column:updated_at"`
	ProgressTotal      sql.NullInt64   `gorm:"column:progress_total"`
	ProgressProcessed  sql.NullInt64   `gorm:"column:progress_processed"`
	ProgressSucceeded  sql.NullInt64   `gorm:"column:progress_succeeded"`
	ProgressSkipped    sql.NullInt64   `gorm:"column:progress_skipped"`
	ProgressFailed     sql.NullInt64   `gorm:"column:progress_failed"`
	ReasonBuckets      json.RawMessage `gorm:"column:reason_buckets"`
}

type adminTaskReasonRow struct {
	SourceID uint64 `gorm:"column:source_id"`
	Reason   string `gorm:"column:reason"`
	Count    int64  `gorm:"column:count"`
}

const emptyTaskSelect = `
SELECT
    'import' AS source,
    0 AS source_id,
    0 AS resource_scope_id,
    'resource' AS biz_type,
    0 AS biz_id,
    'import' AS kind,
    'succeeded' AS status,
    0 AS attempts,
    1 AS max_attempts,
    NULL AS credential_revision,
    CURRENT_TIMESTAMP AS queued_at,
    NULL AS started_at,
    NULL AS finished_at,
    CURRENT_TIMESTAMP AS updated_at,
    NULL AS progress_total,
    NULL AS progress_processed,
    NULL AS progress_succeeded,
    NULL AS progress_skipped,
    NULL AS progress_failed,
    NULL AS reason_buckets
FROM (SELECT 1) AS empty_task
WHERE 1 = 0`
const importResourceTaskSelect = `
SELECT
    'import' AS source,
    imp.id AS source_id,
    item.resource_id AS resource_scope_id,
    'microsoft_resource_import' AS biz_type,
    imp.id AS biz_id,
    'import' AS kind,
    CASE
		WHEN imp.dispatch_status = 'pending' THEN 'queued'
        ELSE imp.dispatch_status
    END AS status,
    imp.attempts AS attempts,
    imp.max_attempts AS max_attempts,
    NULL AS credential_revision,
    imp.created_at AS queued_at,
    imp.started_at AS started_at,
    imp.finished_at AS finished_at,
    imp.updated_at AS updated_at,
    GREATEST(imp.accepted_count + imp.skipped_count, imp.imported_count + imp.skipped_count) AS progress_total,
    LEAST(
        GREATEST(imp.accepted_count + imp.skipped_count, imp.imported_count + imp.skipped_count),
        imp.imported_count + imp.skipped_count
    ) AS progress_processed,
    imp.imported_count AS progress_succeeded,
    imp.skipped_count AS progress_skipped,
    GREATEST(
        GREATEST(imp.accepted_count + imp.skipped_count, imp.imported_count + imp.skipped_count)
            - imp.imported_count - imp.skipped_count,
        0
    ) AS progress_failed,
    NULL AS reason_buckets
FROM resource_imports AS imp
JOIN (
    SELECT DISTINCT import_id, resource_id
    FROM resource_import_items
    WHERE resource_id IS NOT NULL
) AS item ON item.import_id = imp.id
WHERE imp.resource_type = 'microsoft'`

const aliasAttemptTaskSelect = `
SELECT
    'alias' AS source,
    attempt.id AS source_id,
    attempt.resource_id AS resource_scope_id,
    'microsoft_resource' AS biz_type,
    attempt.resource_id AS biz_id,
    'alias' AS kind,
    attempt.status AS status,
    CASE WHEN attempt.was_attempted THEN 1 ELSE 0 END AS attempts,
    1 AS max_attempts,
    NULL AS credential_revision,
    attempt.created_at AS queued_at,
    CASE WHEN attempt.was_attempted THEN attempt.created_at ELSE NULL END AS started_at,
    attempt.completed_at AS finished_at,
    attempt.updated_at AS updated_at,
    NULL AS progress_total,
    NULL AS progress_processed,
    NULL AS progress_succeeded,
    NULL AS progress_skipped,
    NULL AS progress_failed,
    NULL AS reason_buckets
FROM microsoft_alias_attempts AS attempt`

const aliasScheduleTaskSelect = `
SELECT
    'alias_schedule' AS source,
    schedule.resource_id AS source_id,
    schedule.resource_id AS resource_scope_id,
    'microsoft_resource' AS biz_type,
    schedule.resource_id AS biz_id,
    'alias' AS kind,
    CASE
        WHEN schedule.status = 'queued' THEN 'queued'
        WHEN schedule.status = 'running' THEN 'running'
        WHEN schedule.status = 'pending' AND schedule.next_run_at <= CURRENT_TIMESTAMP(3) THEN 'queued'
        WHEN schedule.status = 'pending' AND schedule.last_run_at IS NULL THEN 'queued'
        WHEN latest_uncertain.id IS NOT NULL THEN 'uncertain'
        WHEN schedule.status = 'pending' THEN 'succeeded'
        ELSE 'failed'
    END AS status,
    CASE
        WHEN schedule.status = 'running' OR latest_uncertain.was_attempted THEN 1
        ELSE 0
    END AS attempts,
    1 AS max_attempts,
    NULL AS credential_revision,
    CASE WHEN schedule.last_run_at IS NULL THEN schedule.created_at ELSE schedule.updated_at END AS queued_at,
    CASE
        WHEN schedule.status = 'running' OR latest_uncertain.id IS NOT NULL THEN schedule.last_run_at
        ELSE NULL
    END AS started_at,
    CASE
        WHEN latest_uncertain.id IS NOT NULL THEN NULL
        WHEN schedule.status = 'paused' THEN schedule.updated_at
        WHEN schedule.status = 'pending' AND schedule.last_run_at IS NOT NULL AND schedule.next_run_at > CURRENT_TIMESTAMP(3)
            THEN schedule.last_run_at
        ELSE NULL
    END AS finished_at,
    schedule.updated_at AS updated_at,
    NULL AS progress_total,
    NULL AS progress_processed,
    NULL AS progress_succeeded,
    NULL AS progress_skipped,
    NULL AS progress_failed,
    NULL AS reason_buckets
FROM microsoft_alias_schedules AS schedule
LEFT JOIN microsoft_alias_attempts AS latest_uncertain
  ON latest_uncertain.id = (
      SELECT MAX(candidate.id)
      FROM microsoft_alias_attempts AS candidate
      WHERE candidate.resource_id = schedule.resource_id
        AND candidate.status = 'uncertain'
  )`

const tokenTaskSelect = `
SELECT
    'token' AS source,
    resource.id AS source_id,
    resource.id AS resource_scope_id,
    'microsoft_resource' AS biz_type,
    resource.id AS biz_id,
    'token' AS kind,
    CASE resource.token_refresh_status
        WHEN 'pending' THEN 'queued'
        WHEN 'processing' THEN 'running'
        WHEN 'abnormal' THEN 'failed'
        ELSE 'succeeded'
    END AS status,
    resource.token_refresh_failures AS attempts,
    3 AS max_attempts,
    resource.token_refresh_expected_credential_revision AS credential_revision,
    COALESCE(resource.token_refresh_requested_at, resource.updated_at) AS queued_at,
    resource.token_refresh_started_at AS started_at,
    resource.token_refresh_finished_at AS finished_at,
    resource.updated_at AS updated_at,
    NULL AS progress_total,
    NULL AS progress_processed,
    NULL AS progress_succeeded,
    NULL AS progress_skipped,
    NULL AS progress_failed,
    NULL AS reason_buckets
FROM microsoft_resources AS resource
WHERE resource.token_refresh_generation > 0`

const fetchTaskSelect = `
SELECT
    'fetch' AS source,
    state.email_resource_id AS source_id,
    state.email_resource_id AS resource_scope_id,
    CASE state.operation_kind
        WHEN 'gmail_resource_fetch' THEN 'gmail_resource'
        WHEN 'icloud_resource_fetch' THEN 'icloud_resource'
        ELSE 'microsoft_resource'
    END AS biz_type,
    state.email_resource_id AS biz_id,
    'fetch' AS kind,
    CASE state.status
        WHEN 'pending' THEN 'queued'
        WHEN 'processing' THEN 'running'
        WHEN 'abnormal' THEN 'failed'
        ELSE 'succeeded'
    END AS status,
    state.failures AS attempts,
    3 AS max_attempts,
    state.expected_credential_revision AS credential_revision,
    state.requested_at AS queued_at,
    state.started_at AS started_at,
    state.finished_at AS finished_at,
    state.updated_at AS updated_at,
    state.fetched_count AS progress_total,
    state.fetched_count AS progress_processed,
    state.stored_count AS progress_succeeded,
    CASE WHEN state.fetched_count > state.stored_count
         THEN state.fetched_count - state.stored_count ELSE 0 END AS progress_skipped,
    0 AS progress_failed,
    NULL AS reason_buckets
FROM mailmatch_admin_resource_fetch_states AS state
WHERE state.operation_kind IN ('resource_fetch', 'gmail_resource_fetch', 'icloud_resource_fetch')`

const resourceHistoryTaskSelect = `
SELECT
    'resource_history' AS source,
    state.email_resource_id AS source_id,
    state.email_resource_id AS resource_scope_id,
    'microsoft_resource' AS biz_type,
    state.email_resource_id AS biz_id,
    'history' AS kind,
    CASE state.status
        WHEN 'pending' THEN 'queued'
        WHEN 'processing' THEN 'running'
        WHEN 'abnormal' THEN 'failed'
        ELSE 'succeeded'
    END AS status,
    state.failures AS attempts,
    3 AS max_attempts,
    state.expected_credential_revision AS credential_revision,
    state.requested_at AS queued_at,
    state.started_at AS started_at,
    state.finished_at AS finished_at,
    state.updated_at AS updated_at,
    NULL AS progress_total,
    NULL AS progress_processed,
    NULL AS progress_succeeded,
    NULL AS progress_skipped,
    NULL AS progress_failed,
    NULL AS reason_buckets
FROM mailmatch_resource_fetch_states AS state
WHERE state.operation_kind = 'resource_history'`

const gmailValidationTaskSelect = `
SELECT
    'gmail_validation' AS source,
    run.id AS source_id,
    run.resource_id AS resource_scope_id,
    'gmail_resource' AS biz_type,
    run.resource_id AS biz_id,
    'validation' AS kind,
    run.status AS status,
    run.attempts AS attempts,
    run.max_attempts AS max_attempts,
    run.credential_revision AS credential_revision,
    run.queued_at AS queued_at,
    run.started_at AS started_at,
    run.finished_at AS finished_at,
    run.updated_at AS updated_at,
    NULL AS progress_total,
    NULL AS progress_processed,
    NULL AS progress_succeeded,
    NULL AS progress_skipped,
    NULL AS progress_failed,
    NULL AS reason_buckets
FROM gmail_maintenance_runs AS run
WHERE run.kind = 'validation'`

const gmailHistoryTaskSelect = `
SELECT
    'gmail_history' AS source,
    run.id AS source_id,
    run.resource_id AS resource_scope_id,
    'gmail_resource' AS biz_type,
    run.resource_id AS biz_id,
    'history' AS kind,
    run.status AS status,
    run.attempts AS attempts,
    run.max_attempts AS max_attempts,
    run.credential_revision AS credential_revision,
    run.queued_at AS queued_at,
    run.started_at AS started_at,
    run.finished_at AS finished_at,
    run.updated_at AS updated_at,
    NULL AS progress_total,
    NULL AS progress_processed,
    NULL AS progress_succeeded,
    NULL AS progress_skipped,
    NULL AS progress_failed,
    NULL AS reason_buckets
FROM gmail_maintenance_runs AS run
WHERE run.kind = 'history'`

const iCloudImportResourceTaskSelect = `
SELECT
    'icloud_import' AS source,
    imp.id AS source_id,
    item.resource_id AS resource_scope_id,
    'icloud_resource_import' AS biz_type,
    imp.id AS biz_id,
    'import' AS kind,
    CASE WHEN imp.dispatch_status = 'pending' THEN 'queued' ELSE imp.dispatch_status END AS status,
    imp.attempts AS attempts,
    imp.max_attempts AS max_attempts,
    NULL AS credential_revision,
    imp.created_at AS queued_at,
    imp.started_at AS started_at,
    imp.finished_at AS finished_at,
    imp.updated_at AS updated_at,
    GREATEST(imp.accepted_count + imp.skipped_count, imp.imported_count + imp.skipped_count) AS progress_total,
    LEAST(
        GREATEST(imp.accepted_count + imp.skipped_count, imp.imported_count + imp.skipped_count),
        imp.imported_count + imp.skipped_count
    ) AS progress_processed,
    imp.imported_count AS progress_succeeded,
    imp.skipped_count AS progress_skipped,
    GREATEST(
        GREATEST(imp.accepted_count + imp.skipped_count, imp.imported_count + imp.skipped_count)
            - imp.imported_count - imp.skipped_count,
        0
    ) AS progress_failed,
    NULL AS reason_buckets
FROM icloud_resource_imports AS imp
JOIN (
    SELECT DISTINCT import_id, resource_id
    FROM icloud_resource_import_items
    WHERE resource_id IS NOT NULL
) AS item ON item.import_id = imp.id`

const iCloudValidationTaskSelect = `
SELECT
    'icloud_validation' AS source,
    run.id AS source_id,
    run.resource_id AS resource_scope_id,
    'icloud_resource' AS biz_type,
    run.resource_id AS biz_id,
    run.kind AS kind,
    run.status AS status,
    run.attempts AS attempts,
    run.max_attempts AS max_attempts,
    run.credential_revision AS credential_revision,
    run.queued_at AS queued_at,
    run.started_at AS started_at,
    run.finished_at AS finished_at,
    run.updated_at AS updated_at,
    NULL AS progress_total,
    NULL AS progress_processed,
    NULL AS progress_succeeded,
    NULL AS progress_skipped,
    NULL AS progress_failed,
    NULL AS reason_buckets
FROM icloud_maintenance_runs AS run`

const iCloudRefreshTaskSelect = `
SELECT
    'icloud_refresh' AS source,
    resource.id AS source_id,
    resource.id AS resource_scope_id,
    'icloud_resource' AS biz_type,
    resource.id AS biz_id,
    'refresh' AS kind,
    CASE
        WHEN resource.onboarding_status = 'completed' THEN 'succeeded'
        WHEN resource.onboarding_status = 'failed' THEN 'failed'
        WHEN resource.onboarding_status = 'waiting' AND resource.dispatch_status = 'waiting' THEN 'uncertain'
        WHEN resource.dispatch_status IN ('pending', 'queued') THEN 'queued'
        ELSE 'running'
    END AS status,
    resource.attempts AS attempts,
    resource.max_attempts AS max_attempts,
    resource.expected_credential_revision AS credential_revision,
    resource.created_at AS queued_at,
    resource.started_at AS started_at,
    resource.finished_at AS finished_at,
    resource.updated_at AS updated_at,
    NULL AS progress_total,
    NULL AS progress_processed,
    NULL AS progress_succeeded,
    NULL AS progress_skipped,
    NULL AS progress_failed,
    NULL AS reason_buckets
FROM icloud_resources AS resource
WHERE resource.task_kind IN ('refresh', 'cookie_recovery')`

// Redis-only bulk cursors are absent from per-resource lists. Their bounded
// live status remains available through the source-qualified lookup below.
const microsoftResourceTaskUnion = importResourceTaskSelect + `
UNION ALL
` + aliasAttemptTaskSelect + `
UNION ALL
` + aliasScheduleTaskSelect + `
WHERE schedule.status IN ('queued', 'running', 'paused')
   OR (schedule.status = 'pending' AND (schedule.last_run_at IS NULL OR schedule.next_run_at <= CURRENT_TIMESTAMP(3)))
   OR latest_uncertain.id IS NOT NULL
UNION ALL
` + tokenTaskSelect + `
UNION ALL
` + fetchTaskSelect + `
UNION ALL
` + resourceHistoryTaskSelect

const domainResourceTaskUnion = emptyTaskSelect

const gmailResourceTaskUnion = gmailValidationTaskSelect + `
UNION ALL
` + gmailHistoryTaskSelect + `
UNION ALL
` + fetchTaskSelect

const iCloudResourceTaskUnion = iCloudImportResourceTaskSelect + `
UNION ALL
` + iCloudValidationTaskSelect + `
UNION ALL
` + iCloudRefreshTaskSelect + `
UNION ALL
` + fetchTaskSelect

const importSingleTaskSelect = `
SELECT
    'import' AS source,
    imp.id AS source_id,
    0 AS resource_scope_id,
    'microsoft_resource_import' AS biz_type,
    imp.id AS biz_id,
    'import' AS kind,
    CASE
		WHEN imp.dispatch_status = 'pending' THEN 'queued'
        ELSE imp.dispatch_status
    END AS status,
    imp.attempts AS attempts,
    imp.max_attempts AS max_attempts,
    NULL AS credential_revision,
    imp.created_at AS queued_at,
    imp.started_at AS started_at,
    imp.finished_at AS finished_at,
    imp.updated_at AS updated_at,
    GREATEST(imp.accepted_count + imp.skipped_count, imp.imported_count + imp.skipped_count) AS progress_total,
    LEAST(
        GREATEST(imp.accepted_count + imp.skipped_count, imp.imported_count + imp.skipped_count),
        imp.imported_count + imp.skipped_count
    ) AS progress_processed,
    imp.imported_count AS progress_succeeded,
    imp.skipped_count AS progress_skipped,
    GREATEST(
        GREATEST(imp.accepted_count + imp.skipped_count, imp.imported_count + imp.skipped_count)
            - imp.imported_count - imp.skipped_count,
        0
    ) AS progress_failed,
    NULL AS reason_buckets
FROM resource_imports AS imp
WHERE imp.resource_type = 'microsoft'`

const iCloudImportSingleTaskSelect = `
SELECT
    'icloud_import' AS source,
    imp.id AS source_id,
    imp.id AS resource_scope_id,
    'icloud_resource_import' AS biz_type,
    imp.id AS biz_id,
    'import' AS kind,
    CASE WHEN imp.dispatch_status = 'pending' THEN 'queued' ELSE imp.dispatch_status END AS status,
    imp.attempts AS attempts,
    imp.max_attempts AS max_attempts,
    NULL AS credential_revision,
    imp.created_at AS queued_at,
    imp.started_at AS started_at,
    imp.finished_at AS finished_at,
    imp.updated_at AS updated_at,
    CASE
        WHEN imp.accepted_count > imp.imported_count THEN imp.accepted_count + imp.skipped_count
        ELSE imp.imported_count + imp.skipped_count
    END AS progress_total,
    imp.imported_count + imp.skipped_count AS progress_processed,
    imp.imported_count AS progress_succeeded,
    imp.skipped_count AS progress_skipped,
    CASE WHEN imp.accepted_count > imp.imported_count THEN imp.accepted_count - imp.imported_count ELSE 0 END AS progress_failed,
    NULL AS reason_buckets
FROM icloud_resource_imports AS imp`

const iCloudOnboardingSingleTaskSelect = `
SELECT
    'icloud_onboarding' AS source,
    resource.import_id AS source_id,
    resource.import_id AS resource_scope_id,
    'icloud_resource_import' AS biz_type,
    resource.import_id AS biz_id,
    'import' AS kind,
    CASE
        WHEN SUM(CASE WHEN resource.onboarding_status IN ('completed', 'failed') THEN 1 ELSE 0 END) < COUNT(*)
             AND SUM(CASE WHEN resource.onboarding_status = 'waiting' AND resource.dispatch_status = 'waiting' THEN 1 ELSE 0 END)
                 = COUNT(*) - SUM(CASE WHEN resource.onboarding_status IN ('completed', 'failed') THEN 1 ELSE 0 END)
            THEN 'uncertain'
        WHEN SUM(CASE WHEN resource.onboarding_status IN ('completed', 'failed') THEN 1 ELSE 0 END) < COUNT(*) THEN 'running'
        WHEN SUM(CASE WHEN resource.onboarding_status = 'failed' THEN 1 ELSE 0 END) = 0 THEN 'succeeded'
        ELSE 'failed'
    END AS status,
    COALESCE(MAX(resource.attempts), 0) AS attempts,
    COALESCE(MAX(resource.max_attempts), 1) AS max_attempts,
    NULL AS credential_revision,
    MIN(resource.created_at) AS queued_at,
    MIN(resource.started_at) AS started_at,
    CASE
        WHEN SUM(CASE WHEN resource.onboarding_status IN ('completed', 'failed') THEN 1 ELSE 0 END) = COUNT(*)
        THEN MAX(resource.updated_at)
        ELSE NULL
    END AS finished_at,
    MAX(resource.updated_at) AS updated_at,
    COUNT(*) AS progress_total,
    SUM(CASE WHEN resource.onboarding_status IN ('completed', 'failed') THEN 1 ELSE 0 END) AS progress_processed,
    SUM(CASE WHEN resource.onboarding_status = 'completed' THEN 1 ELSE 0 END) AS progress_succeeded,
    0 AS progress_skipped,
    SUM(CASE WHEN resource.onboarding_status = 'failed' THEN 1 ELSE 0 END) AS progress_failed,
    NULL AS reason_buckets
FROM icloud_resources AS resource
WHERE resource.task_kind = 'onboarding' AND resource.import_id IS NOT NULL
GROUP BY resource.import_id`

const iCloudImportTaskUnion = iCloudImportSingleTaskSelect + `
UNION ALL
` + iCloudOnboardingSingleTaskSelect

func (r *AdminTaskViewRepo) MicrosoftResourceExists(ctx context.Context, resourceID uint) (bool, error) {
	if r == nil || r.db == nil || resourceID == 0 {
		return false, nil
	}
	var count int64
	if err := r.db.WithContext(ctx).
		Table("email_resources AS root").
		Joins("JOIN microsoft_resources AS microsoft ON microsoft.id = root.id").
		Where("root.id = ? AND root.type = ?", resourceID, "microsoft").
		Count(&count).Error; err != nil {
		return false, fmt.Errorf("check microsoft task resource: %w", err)
	}
	return count > 0, nil
}

func (r *AdminTaskViewRepo) DomainResourceExists(ctx context.Context, resourceID uint) (bool, error) {
	if r == nil || r.db == nil || resourceID == 0 {
		return false, nil
	}
	var count int64
	if err := r.db.WithContext(ctx).
		Table("email_resources AS root").
		Joins("JOIN domain_resources AS domain_resource ON domain_resource.id = root.id").
		Where("root.id = ? AND root.type = ?", resourceID, "domain").
		Count(&count).Error; err != nil {
		return false, fmt.Errorf("check domain task resource: %w", err)
	}
	return count > 0, nil
}

func (r *AdminTaskViewRepo) GmailResourceExists(ctx context.Context, resourceID uint) (bool, error) {
	if r == nil || r.db == nil || resourceID == 0 {
		return false, nil
	}
	var count int64
	if err := r.db.WithContext(ctx).
		Table("email_resources AS root").
		Joins("JOIN gmail_resources AS gmail ON gmail.id = root.id").
		Where("root.id = ? AND root.type = ?", resourceID, "gmail").
		Count(&count).Error; err != nil {
		return false, fmt.Errorf("check gmail task resource: %w", err)
	}
	return count > 0, nil
}

func (r *AdminTaskViewRepo) ICloudResourceExists(ctx context.Context, resourceID uint) (bool, error) {
	if r == nil || r.db == nil || resourceID == 0 {
		return false, nil
	}
	var count int64
	if err := r.db.WithContext(ctx).
		Table("email_resources AS root").
		Joins("JOIN icloud_resources AS icloud ON icloud.id = root.id").
		Where("root.id = ? AND root.type = ?", resourceID, "icloud").
		Count(&count).Error; err != nil {
		return false, fmt.Errorf("check icloud task resource: %w", err)
	}
	return count > 0, nil
}

func (r *AdminTaskViewRepo) ListForMicrosoftResource(ctx context.Context, filter governanceapp.AdminTaskListFilter) ([]governanceapp.AdminTaskView, int64, int64, error) {
	return r.listForResource(ctx, filter, microsoftResourceTaskUnion)
}

func (r *AdminTaskViewRepo) ListForDomainResource(ctx context.Context, filter governanceapp.AdminTaskListFilter) ([]governanceapp.AdminTaskView, int64, int64, error) {
	return r.listForResource(ctx, filter, domainResourceTaskUnion)
}

func (r *AdminTaskViewRepo) ListForGmailResource(ctx context.Context, filter governanceapp.AdminTaskListFilter) ([]governanceapp.AdminTaskView, int64, int64, error) {
	return r.listForResource(ctx, filter, gmailResourceTaskUnion)
}

func (r *AdminTaskViewRepo) ListForICloudResource(ctx context.Context, filter governanceapp.AdminTaskListFilter) ([]governanceapp.AdminTaskView, int64, int64, error) {
	return r.listForResource(ctx, filter, iCloudResourceTaskUnion)
}

func (r *AdminTaskViewRepo) ListForICloudImports(ctx context.Context, filter governanceapp.AdminTaskListFilter) ([]governanceapp.AdminTaskView, int64, int64, error) {
	return r.listForResource(ctx, filter, iCloudImportTaskUnion)
}

func (r *AdminTaskViewRepo) listForResource(ctx context.Context, filter governanceapp.AdminTaskListFilter, taskUnion string) ([]governanceapp.AdminTaskView, int64, int64, error) {
	if r == nil || r.db == nil {
		return nil, 0, 0, errors.New("administrator task database is unavailable")
	}
	outerWhere := `
FROM (` + taskUnion + `) AS normalized
WHERE (? = 0 OR normalized.resource_scope_id = ?)
	  AND (? = '' OR normalized.source = ?)
	  AND (? = '' OR normalized.kind = ?)
	  AND (? = '' OR normalized.status = ?)`
	args := []any{filter.BizID, filter.BizID, filter.Source, filter.Source, filter.Kind, filter.Kind, filter.Status, filter.Status}

	var aggregate struct {
		Total     int64 `gorm:"column:total"`
		Succeeded int64 `gorm:"column:succeeded"`
	}
	if err := r.db.WithContext(ctx).Raw(`
SELECT COUNT(*) AS total,
       COALESCE(SUM(CASE WHEN normalized.status = 'succeeded' THEN 1 ELSE 0 END), 0) AS succeeded
`+outerWhere, args...).Scan(&aggregate).Error; err != nil {
		return nil, 0, 0, fmt.Errorf("count normalized administrator tasks: %w", err)
	}

	rows := make([]adminTaskRow, 0, filter.Limit)
	pageArgs := append(append([]any{}, args...), filter.Limit, filter.Offset)
	if err := r.db.WithContext(ctx).Raw(`
SELECT normalized.*
`+outerWhere+`
ORDER BY normalized.updated_at DESC, normalized.source ASC, normalized.source_id DESC
LIMIT ? OFFSET ?`, pageArgs...).Scan(&rows).Error; err != nil {
		return nil, 0, 0, fmt.Errorf("list normalized administrator tasks: %w", err)
	}
	items := adminTaskViews(rows)
	if err := r.attachImportReasonCounts(ctx, items, governanceapp.AdminTaskSourceImport, "resource_import_items"); err != nil {
		return nil, 0, 0, err
	}
	if err := r.attachImportReasonCounts(ctx, items, governanceapp.AdminTaskSourceICloudImport, "icloud_resource_import_items"); err != nil {
		return nil, 0, 0, err
	}
	return items, aggregate.Total, aggregate.Succeeded, nil
}

func (r *AdminTaskViewRepo) FindByRef(ctx context.Context, ref governanceapp.AdminTaskRef) (*governanceapp.AdminTaskView, error) {
	if r == nil {
		return nil, errors.New("administrator task repository is unavailable")
	}
	if ref.Source == governanceapp.AdminTaskSourceBulk {
		return r.findBulkTask(ctx, ref.ID)
	}
	if r.db == nil {
		return nil, errors.New("administrator task database is unavailable")
	}
	selectSQL, err := singleTaskSelect(ref.Source)
	if err != nil {
		return nil, governanceapp.ErrInvalidAdminTaskQuery
	}
	var row adminTaskRow
	result := r.db.WithContext(ctx).Raw(`
SELECT source_task.*
FROM (`+selectSQL+`) AS source_task
WHERE source_task.source_id = ?
LIMIT 1`, ref.ID).Scan(&row)
	if result.Error != nil {
		return nil, fmt.Errorf("find normalized administrator task: %w", result.Error)
	}
	if result.RowsAffected == 0 || row.SourceID == 0 {
		return nil, governanceapp.ErrAdminTaskNotFound
	}
	items := adminTaskViews([]adminTaskRow{row})
	if len(items) != 1 {
		return nil, governanceapp.ErrAdminTaskNotFound
	}
	if ref.Source == governanceapp.AdminTaskSourceImport {
		if err := r.attachImportReasonCounts(ctx, items, governanceapp.AdminTaskSourceImport, "resource_import_items"); err != nil {
			return nil, err
		}
	}
	if ref.Source == governanceapp.AdminTaskSourceICloudImport {
		if err := r.attachImportReasonCounts(ctx, items, governanceapp.AdminTaskSourceICloudImport, "icloud_resource_import_items"); err != nil {
			return nil, err
		}
	}
	return &items[0], nil
}

func (r *AdminTaskViewRepo) findBulkTask(ctx context.Context, commandID uint64) (*governanceapp.AdminTaskView, error) {
	if r.redis == nil {
		return nil, errors.New("administrator bulk task store is unavailable")
	}
	values, err := r.redis.HGetAll(ctx, AdminResourceBulkStatusKey(commandID)).Result()
	if err != nil {
		return nil, fmt.Errorf("read administrator bulk task: %w", err)
	}
	if len(values) == 0 {
		return nil, governanceapp.ErrAdminTaskNotFound
	}
	kind, err := adminBulkTaskKind(values["action"])
	if err != nil {
		return nil, err
	}
	status := strings.TrimSpace(values["status"])
	if !validAdminBulkTaskStatus(status) {
		return nil, errors.New("administrator bulk task status is invalid")
	}
	attempts, err := adminBulkTaskCount(values, "attempts")
	if err != nil {
		return nil, err
	}
	maxAttempts, err := adminBulkTaskCount(values, "max_attempts")
	if err != nil || maxAttempts < 1 {
		return nil, errors.New("administrator bulk task retry state is invalid")
	}
	queuedAt, err := adminBulkTaskTime(values, "queued_at", true)
	if err != nil {
		return nil, err
	}
	startedAt, err := adminBulkTaskTime(values, "started_at", false)
	if err != nil {
		return nil, err
	}
	finishedAt, err := adminBulkTaskTime(values, "finished_at", false)
	if err != nil {
		return nil, err
	}
	updatedAt, err := adminBulkTaskTime(values, "updated_at", true)
	if err != nil {
		return nil, err
	}
	total, err := adminBulkTaskCount(values, "total")
	if err != nil {
		return nil, err
	}
	processed, err := adminBulkTaskCount(values, "processed")
	if err != nil {
		return nil, err
	}
	succeeded, err := adminBulkTaskCount(values, "succeeded")
	if err != nil {
		return nil, err
	}
	skipped, err := adminBulkTaskCount(values, "skipped")
	if err != nil {
		return nil, err
	}
	failed, err := adminBulkTaskCount(values, "failed")
	if err != nil {
		return nil, err
	}
	if total < processed && status == governanceapp.AdminTaskStatusSucceeded {
		total = processed
	}
	reasons := make(map[string]int64)
	for key, raw := range values {
		if !strings.HasPrefix(key, "reason:") {
			continue
		}
		count, parseErr := strconv.ParseInt(raw, 10, 64)
		if parseErr != nil || count < 0 {
			return nil, errors.New("administrator bulk task reason state is invalid")
		}
		reasons[safeReason(strings.TrimPrefix(key, "reason:"))] += count
	}
	return &governanceapp.AdminTaskView{
		Ref:         governanceapp.AdminTaskRef{Source: governanceapp.AdminTaskSourceBulk, ID: commandID},
		BizType:     governanceapp.AdminTaskBizMicrosoftResourceBulk,
		BizID:       commandID,
		Kind:        kind,
		Status:      status,
		Attempts:    int(attempts),
		MaxAttempts: int(maxAttempts),
		QueuedAt:    *queuedAt,
		StartedAt:   startedAt,
		FinishedAt:  finishedAt,
		UpdatedAt:   *updatedAt,
		Progress: &governanceapp.AdminTaskProgress{
			Total: total, Processed: processed, Succeeded: succeeded, Skipped: skipped, Failed: failed,
			ReasonCounts: reasonCountMap(reasons),
		},
	}, nil
}

func adminBulkTaskKind(action string) (string, error) {
	switch strings.TrimSpace(action) {
	case "validate":
		return governanceapp.AdminTaskKindBulkValidate, nil
	case "alias":
		return governanceapp.AdminTaskKindBulkAlias, nil
	case "history":
		return governanceapp.AdminTaskKindBulkHistory, nil
	case "token":
		return governanceapp.AdminTaskKindBulkToken, nil
	case "publish":
		return governanceapp.AdminTaskKindBulkPublish, nil
	case "unpublish":
		return governanceapp.AdminTaskKindBulkUnpublish, nil
	case "delete":
		return governanceapp.AdminTaskKindBulkDelete, nil
	default:
		return "", errors.New("administrator bulk task action is invalid")
	}
}

func validAdminBulkTaskStatus(status string) bool {
	switch status {
	case governanceapp.AdminTaskStatusQueued,
		governanceapp.AdminTaskStatusRunning,
		governanceapp.AdminTaskStatusSucceeded,
		governanceapp.AdminTaskStatusFailed,
		governanceapp.AdminTaskStatusUncertain,
		governanceapp.AdminTaskStatusCanceled:
		return true
	default:
		return false
	}
}

func adminBulkTaskCount(values map[string]string, key string) (int64, error) {
	value, ok := values[key]
	if !ok {
		return 0, fmt.Errorf("administrator bulk task field %s is missing", key)
	}
	result, err := strconv.ParseInt(value, 10, 64)
	if err != nil || result < 0 {
		return 0, fmt.Errorf("administrator bulk task field %s is invalid", key)
	}
	return result, nil
}

func adminBulkTaskTime(values map[string]string, key string, required bool) (*time.Time, error) {
	raw := strings.TrimSpace(values[key])
	if raw == "" {
		if required {
			return nil, fmt.Errorf("administrator bulk task field %s is missing", key)
		}
		return nil, nil
	}
	milliseconds, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || milliseconds <= 0 {
		return nil, fmt.Errorf("administrator bulk task field %s is invalid", key)
	}
	value := time.UnixMilli(milliseconds).UTC()
	return &value, nil
}

func singleTaskSelect(source string) (string, error) {
	switch source {
	case governanceapp.AdminTaskSourceImport:
		return importSingleTaskSelect, nil
	case governanceapp.AdminTaskSourceAlias:
		return aliasAttemptTaskSelect, nil
	case governanceapp.AdminTaskSourceAliasSchedule:
		return aliasScheduleTaskSelect, nil
	case governanceapp.AdminTaskSourceToken:
		return tokenTaskSelect, nil
	case governanceapp.AdminTaskSourceFetch:
		return fetchTaskSelect, nil
	case governanceapp.AdminTaskSourceResourceHistory:
		return resourceHistoryTaskSelect, nil
	case governanceapp.AdminTaskSourceGmailValidate:
		return gmailValidationTaskSelect, nil
	case governanceapp.AdminTaskSourceGmailHistory:
		return gmailHistoryTaskSelect, nil
	case governanceapp.AdminTaskSourceICloudImport:
		return iCloudImportSingleTaskSelect, nil
	case governanceapp.AdminTaskSourceICloudOnboarding:
		return iCloudOnboardingSingleTaskSelect, nil
	case governanceapp.AdminTaskSourceICloudRefresh:
		return iCloudRefreshTaskSelect, nil
	case governanceapp.AdminTaskSourceICloudValidate:
		return iCloudValidationTaskSelect, nil
	default:
		return "", governanceapp.ErrInvalidAdminTaskQuery
	}
}

func adminTaskViews(rows []adminTaskRow) []governanceapp.AdminTaskView {
	items := make([]governanceapp.AdminTaskView, len(rows))
	for i := range rows {
		items[i] = adminTaskView(rows[i])
	}
	return items
}

func adminTaskView(row adminTaskRow) governanceapp.AdminTaskView {
	attempts := row.Attempts
	if attempts < 0 {
		attempts = 0
	}
	maxAttempts := row.MaxAttempts
	if maxAttempts < 1 {
		maxAttempts = 1
	}
	if attempts > maxAttempts {
		maxAttempts = attempts
	}
	var credentialRevision *uint64
	if row.CredentialRevision.Valid && row.CredentialRevision.Int64 >= 0 {
		value := uint64(row.CredentialRevision.Int64)
		credentialRevision = &value
	}
	queuedAt := row.QueuedAt.Time
	if queuedAt.IsZero() {
		queuedAt = row.UpdatedAt.Time
	}
	view := governanceapp.AdminTaskView{
		Ref:                governanceapp.AdminTaskRef{Source: row.Source, ID: row.SourceID},
		BizType:            row.BizType,
		BizID:              row.BizID,
		Kind:               row.Kind,
		Status:             row.Status,
		Attempts:           attempts,
		MaxAttempts:        maxAttempts,
		CredentialRevision: credentialRevision,
		QueuedAt:           queuedAt,
		StartedAt:          nullTimePointer(row.StartedAt),
		FinishedAt:         nullTimePointer(row.FinishedAt),
		UpdatedAt:          row.UpdatedAt.Time,
	}
	if row.ProgressTotal.Valid {
		view.Progress = &governanceapp.AdminTaskProgress{
			Total:        nonNegativeInt64(row.ProgressTotal.Int64),
			Processed:    nonNegativeInt64(row.ProgressProcessed.Int64),
			Succeeded:    nonNegativeInt64(row.ProgressSucceeded.Int64),
			Skipped:      nonNegativeInt64(row.ProgressSkipped.Int64),
			Failed:       nonNegativeInt64(row.ProgressFailed.Int64),
			ReasonCounts: make([]governanceapp.AdminTaskReasonCount, 0),
		}
		if len(row.ReasonBuckets) > 0 && string(row.ReasonBuckets) != "null" {
			view.Progress.ReasonCounts = safeReasonBuckets(row.ReasonBuckets)
		}
	}
	return view
}

func (r *AdminTaskViewRepo) attachImportReasonCounts(ctx context.Context, items []governanceapp.AdminTaskView, source, table string) error {
	ids := make([]uint64, 0)
	for i := range items {
		if items[i].Ref.Source == source && items[i].Progress != nil {
			ids = append(ids, items[i].Ref.ID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	rows := make([]adminTaskReasonRow, 0)
	if err := r.db.WithContext(ctx).Raw(fmt.Sprintf(`
SELECT item.import_id AS source_id,
       item.category AS reason,
       COUNT(*) AS count
FROM %s AS item
WHERE item.import_id IN ?
  AND item.outcome = 'skipped'
GROUP BY item.import_id, item.category
ORDER BY item.import_id, item.category`, table), ids).Scan(&rows).Error; err != nil {
		return fmt.Errorf("load safe import reason counts: %w", err)
	}
	byID := make(map[uint64]map[string]int64)
	for i := range rows {
		reason := safeReason(rows[i].Reason)
		if _, ok := byID[rows[i].SourceID]; !ok {
			byID[rows[i].SourceID] = make(map[string]int64)
		}
		byID[rows[i].SourceID][reason] += nonNegativeInt64(rows[i].Count)
	}
	for i := range items {
		if items[i].Ref.Source != source || items[i].Progress == nil {
			continue
		}
		items[i].Progress.ReasonCounts = reasonCountMap(byID[items[i].Ref.ID])
	}
	return nil
}

var safeTaskReasonPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

func safeReason(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return "skipped"
	}
	if !safeTaskReasonPattern.MatchString(value) {
		return "other"
	}
	return value
}

func safeReasonBuckets(raw json.RawMessage) []governanceapp.AdminTaskReasonCount {
	counts := make(map[string]int64)
	var object map[string]int64
	if err := json.Unmarshal(raw, &object); err == nil {
		for reason, count := range object {
			counts[safeReason(reason)] += nonNegativeInt64(count)
		}
		return reasonCountMap(counts)
	}
	var list []struct {
		Reason string `json:"reason"`
		Count  int64  `json:"count"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		return []governanceapp.AdminTaskReasonCount{}
	}
	for i := range list {
		counts[safeReason(list[i].Reason)] += nonNegativeInt64(list[i].Count)
	}
	return reasonCountMap(counts)
}

func reasonCountMap(counts map[string]int64) []governanceapp.AdminTaskReasonCount {
	if len(counts) == 0 {
		return []governanceapp.AdminTaskReasonCount{}
	}
	reasons := make([]string, 0, len(counts))
	for reason := range counts {
		reasons = append(reasons, reason)
	}
	sort.Strings(reasons)
	result := make([]governanceapp.AdminTaskReasonCount, 0, len(reasons))
	for _, reason := range reasons {
		result = append(result, governanceapp.AdminTaskReasonCount{Reason: reason, Count: counts[reason]})
	}
	return result
}

// adminTaskTime accepts native database timestamps and SQLite's string result
// for datetime aggregate expressions such as MIN/MAX.
type adminTaskTime struct {
	time.Time
	Valid bool
}

func (value *adminTaskTime) Scan(src any) error {
	if src == nil {
		value.Time = time.Time{}
		value.Valid = false
		return nil
	}
	switch raw := src.(type) {
	case time.Time:
		value.Time = raw
		value.Valid = true
		return nil
	case string:
		parsed, err := parseAdminTaskTime(raw)
		if err != nil {
			return err
		}
		value.Time = parsed
		value.Valid = true
		return nil
	case []byte:
		parsed, err := parseAdminTaskTime(string(raw))
		if err != nil {
			return err
		}
		value.Time = parsed
		value.Valid = true
		return nil
	default:
		return fmt.Errorf("unsupported administrator task timestamp type %T", src)
	}
}

func (value adminTaskTime) Value() (driver.Value, error) {
	if !value.Valid {
		return nil, nil
	}
	return value.Time, nil
}

func parseAdminTaskTime(raw string) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	for _, layout := range []string{
		time.RFC3339Nano,
		"2006-01-02 15:04:05.999999999-07:00",
		"2006-01-02 15:04:05.999999999",
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05.999999999",
		"2006-01-02T15:04:05",
	} {
		if parsed, err := time.ParseInLocation(layout, raw, time.UTC); err == nil {
			return parsed, nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid administrator task timestamp %q", raw)
}

func nullTimePointer(value adminTaskTime) *time.Time {
	if !value.Valid {
		return nil
	}
	result := value.Time
	return &result
}

func nonNegativeInt64(value int64) int64 {
	if value < 0 {
		return 0
	}
	return value
}

var _ governanceapp.AdminTaskViewRepository = (*AdminTaskViewRepo)(nil)
