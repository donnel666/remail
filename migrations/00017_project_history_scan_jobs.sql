-- +goose Up

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

-- Existing projects were already covered by the historical validation path.
-- Seed terminal planner markers so the recovery scan only picks up projects
-- created after this migration.
INSERT INTO mailmatch_project_history_scan_jobs(
    project_id, shard, status, finished_at
)
SELECT p.id, -1, 'succeeded', CURRENT_TIMESTAMP(3)
FROM projects AS p
WHERE EXISTS (
    SELECT 1
    FROM project_products AS pp
    WHERE pp.project_id = p.id
      AND pp.type = 'microsoft'
);

-- +goose Down

DROP TABLE IF EXISTS mailmatch_project_history_scan_jobs;
