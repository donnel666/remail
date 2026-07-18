package infra

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	governanceapp "github.com/donnel666/remail/internal/governance/app"
	"gorm.io/gorm"
)

// AdminTaskViewRepo composes read-only task facts owned by their source
// contexts. It never updates source rows and deliberately does not select raw
// errors, payloads, filters, object keys, candidates, claims, leases or fencing
// tokens.
type AdminTaskViewRepo struct {
	db *gorm.DB
}

func NewAdminTaskViewRepo(db *gorm.DB) *AdminTaskViewRepo {
	return &AdminTaskViewRepo{db: db}
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
	QueuedAt           time.Time       `gorm:"column:queued_at"`
	StartedAt          sql.NullTime    `gorm:"column:started_at"`
	FinishedAt         sql.NullTime    `gorm:"column:finished_at"`
	UpdatedAt          time.Time       `gorm:"column:updated_at"`
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
    'microsoft_resource' AS biz_type,
    state.email_resource_id AS biz_id,
    CASE WHEN state.operation_kind = 'resource_history' THEN 'history' ELSE 'fetch' END AS kind,
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
    GREATEST(state.fetched_count - state.stored_count, 0) AS progress_skipped,
    0 AS progress_failed,
    NULL AS reason_buckets
FROM mailmatch_resource_fetch_states AS state
WHERE state.operation_kind IN ('resource_fetch', 'resource_history')`

// Redis-only bulk cursors are intentionally absent: this view exposes durable
// business facts only.
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
` + fetchTaskSelect

const domainResourceTaskUnion = emptyTaskSelect

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

func (r *AdminTaskViewRepo) ListForMicrosoftResource(ctx context.Context, filter governanceapp.AdminTaskListFilter) ([]governanceapp.AdminTaskView, int64, int64, error) {
	return r.listForResource(ctx, filter, microsoftResourceTaskUnion)
}

func (r *AdminTaskViewRepo) ListForDomainResource(ctx context.Context, filter governanceapp.AdminTaskListFilter) ([]governanceapp.AdminTaskView, int64, int64, error) {
	return r.listForResource(ctx, filter, domainResourceTaskUnion)
}

func (r *AdminTaskViewRepo) listForResource(ctx context.Context, filter governanceapp.AdminTaskListFilter, taskUnion string) ([]governanceapp.AdminTaskView, int64, int64, error) {
	if r == nil || r.db == nil {
		return nil, 0, 0, errors.New("administrator task database is unavailable")
	}
	outerWhere := `
FROM (` + taskUnion + `) AS normalized
WHERE normalized.resource_scope_id = ?
  AND (? = '' OR normalized.kind = ?)
  AND (? = '' OR normalized.status = ?)`
	args := []any{filter.BizID, filter.Kind, filter.Kind, filter.Status, filter.Status}

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
	if err := r.attachImportReasonCounts(ctx, items); err != nil {
		return nil, 0, 0, err
	}
	return items, aggregate.Total, aggregate.Succeeded, nil
}

func (r *AdminTaskViewRepo) FindByRef(ctx context.Context, ref governanceapp.AdminTaskRef) (*governanceapp.AdminTaskView, error) {
	if r == nil || r.db == nil {
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
		if err := r.attachImportReasonCounts(ctx, items); err != nil {
			return nil, err
		}
	}
	return &items[0], nil
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
	queuedAt := row.QueuedAt
	if queuedAt.IsZero() {
		queuedAt = row.UpdatedAt
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
		UpdatedAt:          row.UpdatedAt,
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

func (r *AdminTaskViewRepo) attachImportReasonCounts(ctx context.Context, items []governanceapp.AdminTaskView) error {
	ids := make([]uint64, 0)
	for i := range items {
		if items[i].Ref.Source == governanceapp.AdminTaskSourceImport && items[i].Progress != nil {
			ids = append(ids, items[i].Ref.ID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	rows := make([]adminTaskReasonRow, 0)
	if err := r.db.WithContext(ctx).Raw(`
SELECT item.import_id AS source_id,
       item.category AS reason,
       COUNT(*) AS count
FROM resource_import_items AS item
WHERE item.import_id IN ?
  AND item.outcome = 'skipped'
GROUP BY item.import_id, item.category
ORDER BY item.import_id, item.category`, ids).Scan(&rows).Error; err != nil {
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
		if items[i].Ref.Source != governanceapp.AdminTaskSourceImport || items[i].Progress == nil {
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

func nullTimePointer(value sql.NullTime) *time.Time {
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
