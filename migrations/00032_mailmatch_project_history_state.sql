-- +goose Up

-- Project history scan is mutable project state. Asynq carries only the
-- project generation; no planner/shard history is required for correctness.
ALTER TABLE projects
    ADD COLUMN history_scan_status VARCHAR(32) NOT NULL DEFAULT 'normal'
        COMMENT 'pending|processing|normal|abnormal' AFTER candidate_refresh_finished_at,
    ADD COLUMN history_scan_generation BIGINT UNSIGNED NOT NULL DEFAULT 0
        AFTER history_scan_status,
    ADD COLUMN history_scan_failures TINYINT UNSIGNED NOT NULL DEFAULT 0
        AFTER history_scan_generation,
    ADD COLUMN history_scan_scanned_count INT UNSIGNED NOT NULL DEFAULT 0
        AFTER history_scan_failures,
    ADD COLUMN history_scan_matched_count INT UNSIGNED NOT NULL DEFAULT 0
        AFTER history_scan_scanned_count,
    ADD COLUMN history_scan_skipped_count INT UNSIGNED NOT NULL DEFAULT 0
        AFTER history_scan_matched_count,
    ADD COLUMN history_scan_request_id VARCHAR(64) NOT NULL DEFAULT ''
        AFTER history_scan_skipped_count,
    ADD COLUMN history_scan_last_safe_error VARCHAR(500) NOT NULL DEFAULT ''
        AFTER history_scan_request_id,
    ADD COLUMN history_scan_requested_at DATETIME(3) NULL
        AFTER history_scan_last_safe_error,
    ADD COLUMN history_scan_started_at DATETIME(3) NULL
        AFTER history_scan_requested_at,
    ADD COLUMN history_scan_finished_at DATETIME(3) NULL
        AFTER history_scan_started_at,
    ADD INDEX idx_projects_history_scan_pending
        (history_scan_status, history_scan_requested_at, id),
    ADD CONSTRAINT chk_projects_history_scan_status
        CHECK (history_scan_status IN ('pending', 'processing', 'normal', 'abnormal')),
    ADD CONSTRAINT chk_projects_history_scan_failures
        CHECK (history_scan_failures <= 3);

-- One-time takeover only: old in-flight rows become a fresh pending project
-- generation. Hot dispatch never scans or revives processing rows.
UPDATE projects AS p
JOIN (
    SELECT project_id
    FROM mailmatch_project_history_scan_jobs
    WHERE status IN ('queued', 'running')
    GROUP BY project_id
) AS active ON active.project_id = p.id
SET p.history_scan_status = 'pending',
    p.history_scan_generation = 1,
    p.history_scan_failures = 0,
    p.history_scan_requested_at = CURRENT_TIMESTAMP(3),
    p.history_scan_started_at = NULL,
    p.history_scan_finished_at = NULL;

DROP TABLE IF EXISTS mailmatch_project_history_scan_jobs;

-- +goose Down

CREATE TABLE mailmatch_project_history_scan_jobs (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    project_id BIGINT UNSIGNED NOT NULL,
    shard SMALLINT NOT NULL COMMENT '-1=planner; 0..3=resource range',
    status VARCHAR(32) NOT NULL DEFAULT 'queued' COMMENT 'queued|running|succeeded',
    start_resource_id BIGINT UNSIGNED NOT NULL DEFAULT 0,
    checkpoint_resource_id BIGINT UNSIGNED NOT NULL DEFAULT 0,
    end_resource_id BIGINT UNSIGNED NOT NULL DEFAULT 0,
    attempts INT UNSIGNED NOT NULL DEFAULT 0,
    max_attempts INT UNSIGNED NOT NULL DEFAULT 3,
    scanned_count INT UNSIGNED NOT NULL DEFAULT 0,
    matched_count INT UNSIGNED NOT NULL DEFAULT 0,
    skipped_count INT UNSIGNED NOT NULL DEFAULT 0,
    claim_token CHAR(36) NOT NULL DEFAULT '',
    dispatch_token CHAR(36) NOT NULL DEFAULT '',
    dispatched_at DATETIME(3) NULL,
    last_safe_error VARCHAR(500) NOT NULL DEFAULT '',
    request_id VARCHAR(64) NOT NULL DEFAULT '',
    started_at DATETIME(3) NULL,
    finished_at DATETIME(3) NULL,
    created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
    UNIQUE INDEX idx_project_history_scan_shard (project_id, shard),
    INDEX idx_project_history_scan_dispatch (status, dispatched_at, updated_at, id),
    CONSTRAINT fk_project_history_scan_project
        FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE,
    CONSTRAINT chk_project_history_scan_status
        CHECK (status IN ('queued', 'running', 'succeeded')),
    CONSTRAINT chk_project_history_scan_shard
        CHECK (shard BETWEEN -1 AND 3),
    CONSTRAINT chk_project_history_scan_attempts
        CHECK (attempts <= max_attempts AND max_attempts > 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

INSERT INTO mailmatch_project_history_scan_jobs(
    project_id, shard, status,
    attempts, max_attempts,
    scanned_count, matched_count, skipped_count,
    request_id, created_at, updated_at
)
SELECT
    id, -1, 'queued',
    LEAST(history_scan_failures, 2), 3,
    history_scan_scanned_count, history_scan_matched_count, history_scan_skipped_count,
    history_scan_request_id,
    COALESCE(history_scan_requested_at, CURRENT_TIMESTAMP(3)), CURRENT_TIMESTAMP(3)
FROM projects
WHERE history_scan_status IN ('pending', 'processing');

ALTER TABLE projects
    DROP CHECK chk_projects_history_scan_failures,
    DROP CHECK chk_projects_history_scan_status,
    DROP INDEX idx_projects_history_scan_pending,
    DROP COLUMN history_scan_finished_at,
    DROP COLUMN history_scan_started_at,
    DROP COLUMN history_scan_requested_at,
    DROP COLUMN history_scan_last_safe_error,
    DROP COLUMN history_scan_request_id,
    DROP COLUMN history_scan_skipped_count,
    DROP COLUMN history_scan_matched_count,
    DROP COLUMN history_scan_scanned_count,
    DROP COLUMN history_scan_failures,
    DROP COLUMN history_scan_generation,
    DROP COLUMN history_scan_status;
