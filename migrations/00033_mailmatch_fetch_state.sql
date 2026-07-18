-- +goose Up

-- Mail fetching is one mutable state per email resource. Asynq carries only
-- resource ID + generation; old task history and idempotency receipt tables
-- are intentionally removed.
ALTER TABLE mailmatch_resource_fetch_states
    DROP FOREIGN KEY fk_mm_resource_fetch_states_job,
    DROP CHECK chk_mm_resource_fetch_states_status;

UPDATE mailmatch_resource_fetch_states
SET last_status = CASE last_status
    WHEN 'pending' THEN 'pending'
    WHEN 'queued' THEN 'pending'
    WHEN 'running' THEN 'pending'
    WHEN 'failed' THEN 'abnormal'
    ELSE 'normal'
END;

ALTER TABLE mailmatch_resource_fetch_states
    DROP COLUMN last_job_id,
    CHANGE COLUMN last_status status VARCHAR(32) NOT NULL DEFAULT 'normal'
        COMMENT 'pending|processing|normal|abnormal',
    CHANGE COLUMN last_submitted_at requested_at DATETIME(3) NULL,
    ADD COLUMN generation BIGINT UNSIGNED NOT NULL DEFAULT 0 AFTER status,
    ADD COLUMN failures TINYINT UNSIGNED NOT NULL DEFAULT 0 AFTER generation,
    ADD COLUMN operation_kind VARCHAR(32) NOT NULL DEFAULT 'order_fetch'
        COMMENT 'order_fetch|resource_fetch|resource_history' AFTER failures,
    ADD COLUMN order_no VARCHAR(64) NOT NULL DEFAULT '' AFTER operation_kind,
    ADD COLUMN purpose VARCHAR(32) NOT NULL DEFAULT 'order_fetch' AFTER order_no,
    ADD COLUMN operator_user_id BIGINT UNSIGNED NULL AFTER purpose,
    ADD COLUMN expected_credential_revision BIGINT UNSIGNED NOT NULL DEFAULT 0
        AFTER operator_user_id,
    ADD COLUMN since_at DATETIME(3) NULL AFTER expected_credential_revision,
    ADD COLUMN until_at DATETIME(3) NULL AFTER since_at,
    ADD COLUMN fetched_count INT UNSIGNED NOT NULL DEFAULT 0 AFTER until_at,
    ADD COLUMN stored_count INT UNSIGNED NOT NULL DEFAULT 0 AFTER fetched_count,
    ADD COLUMN matched_count INT UNSIGNED NOT NULL DEFAULT 0 AFTER stored_count,
    ADD COLUMN request_id VARCHAR(64) NOT NULL DEFAULT '' AFTER matched_count,
    ADD COLUMN path VARCHAR(255) NOT NULL DEFAULT '' AFTER request_id,
    ADD COLUMN idempotency_key VARCHAR(128) NOT NULL DEFAULT '' AFTER path,
    ADD COLUMN started_at DATETIME(3) NULL AFTER requested_at,
    ADD COLUMN finished_at DATETIME(3) NULL AFTER started_at,
    ADD INDEX idx_mailmatch_fetch_state_pending
        (status, operation_kind, requested_at, email_resource_id),
    ADD CONSTRAINT fk_mailmatch_fetch_state_operator
        FOREIGN KEY (operator_user_id) REFERENCES users(id) ON DELETE SET NULL;

-- One-time takeover of old in-flight order fetches. Hot dispatch scans only
-- the indexed pending state and never revives processing rows.
INSERT INTO mailmatch_resource_fetch_states(
    email_resource_id, status, generation, failures, operation_kind,
    order_no, purpose, since_at, until_at,
    fetched_count, stored_count, matched_count,
    request_id, requested_at, started_at, finished_at,
    cooldown_until, last_safe_error, created_at, updated_at
)
SELECT
    j.email_resource_id, 'pending', 1, 0, 'order_fetch',
    j.order_no, j.purpose, j.since_at, j.until_at,
    GREATEST(j.fetched_count, j.stored_count, j.matched_count),
    GREATEST(j.stored_count, j.matched_count),
    j.matched_count,
    j.request_id, j.created_at, NULL, NULL,
    NULL, j.last_safe_error, j.created_at, j.updated_at
FROM mailmatch_fetch_jobs AS j
WHERE j.status IN ('pending', 'queued', 'running')
ON DUPLICATE KEY UPDATE
    status = 'pending',
    generation = mailmatch_resource_fetch_states.generation + 1,
    failures = 0,
    operation_kind = 'order_fetch',
    order_no = VALUES(order_no),
    purpose = VALUES(purpose),
    operator_user_id = NULL,
    expected_credential_revision = 0,
    since_at = VALUES(since_at),
    until_at = VALUES(until_at),
    fetched_count = VALUES(fetched_count),
    stored_count = VALUES(stored_count),
    matched_count = VALUES(matched_count),
    request_id = VALUES(request_id),
    path = '',
    idempotency_key = '',
    requested_at = VALUES(requested_at),
    started_at = NULL,
    finished_at = NULL,
    last_safe_error = VALUES(last_safe_error);

-- Administrator fetch/history requests use the same resource state. If an
-- old admin task and order fetch were both active, the explicit admin request
-- wins the new generation and fences the older worker.
INSERT INTO mailmatch_resource_fetch_states(
    email_resource_id, status, generation, failures, operation_kind,
    operator_user_id, expected_credential_revision,
    since_at, until_at, fetched_count, stored_count, matched_count,
    request_id, path, idempotency_key, requested_at, started_at, finished_at,
    last_safe_error, created_at, updated_at
)
SELECT
    j.resource_id, 'pending', 1, 0,
    CASE WHEN j.since_at IS NULL AND j.until_at IS NULL
        THEN 'resource_history' ELSE 'resource_fetch' END,
    j.operator_user_id, j.expected_credential_revision,
    j.since_at, j.until_at,
    GREATEST(j.fetched_count, j.stored_count, j.matched_count),
    GREATEST(j.stored_count, j.matched_count),
    j.matched_count,
    j.request_id, j.path, COALESCE((
        SELECT request.idempotency_key
        FROM mailmatch_resource_fetch_requests AS request
        WHERE request.job_id = j.id
          AND request.operator_user_id = j.operator_user_id
        ORDER BY request.created_at DESC, request.idempotency_key DESC
        LIMIT 1
    ), ''), j.created_at, NULL, NULL,
    j.last_safe_error, j.created_at, j.updated_at
FROM mailmatch_resource_fetch_jobs AS j
WHERE j.status IN ('queued', 'running')
ON DUPLICATE KEY UPDATE
    status = 'pending',
    generation = mailmatch_resource_fetch_states.generation + 1,
    failures = 0,
    operation_kind = VALUES(operation_kind),
    order_no = '',
    operator_user_id = VALUES(operator_user_id),
    expected_credential_revision = VALUES(expected_credential_revision),
    since_at = VALUES(since_at),
    until_at = VALUES(until_at),
    fetched_count = VALUES(fetched_count),
    stored_count = VALUES(stored_count),
    matched_count = VALUES(matched_count),
    request_id = VALUES(request_id),
    path = VALUES(path),
    idempotency_key = VALUES(idempotency_key),
    requested_at = VALUES(requested_at),
    started_at = NULL,
    finished_at = NULL,
    last_safe_error = VALUES(last_safe_error);

-- Older rows only required non-negative counters. Normalize them before the
-- stricter relationship constraint is installed.
UPDATE mailmatch_resource_fetch_states
SET fetched_count = GREATEST(fetched_count, stored_count, matched_count),
    stored_count = GREATEST(stored_count, matched_count);

ALTER TABLE mailmatch_resource_fetch_states
    ADD CONSTRAINT chk_mailmatch_fetch_state_status
        CHECK (status IN ('pending', 'processing', 'normal', 'abnormal')),
    ADD CONSTRAINT chk_mailmatch_fetch_state_failures CHECK (failures <= 3),
    ADD CONSTRAINT chk_mailmatch_fetch_state_operation
        CHECK (operation_kind IN ('order_fetch', 'resource_fetch', 'resource_history')),
    ADD CONSTRAINT chk_mailmatch_fetch_state_purpose
        CHECK (purpose IN ('order_fetch', 'manual_fetch', 'auto_refresh', 'aftersale_check', 'inbound_consume')),
    ADD CONSTRAINT chk_mailmatch_fetch_state_counts
        CHECK (stored_count <= fetched_count AND matched_count <= stored_count);

DROP TABLE IF EXISTS mailmatch_resource_fetch_requests;
DROP TABLE IF EXISTS mailmatch_resource_fetch_jobs;
DROP TABLE IF EXISTS mailmatch_fetch_jobs;

-- +goose Down

CREATE TABLE mailmatch_fetch_jobs (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    order_no VARCHAR(64) NOT NULL,
    purpose VARCHAR(32) NOT NULL DEFAULT 'order_fetch',
    allocation_type VARCHAR(32) NOT NULL,
    allocation_id BIGINT UNSIGNED NOT NULL,
    project_id BIGINT UNSIGNED NOT NULL,
    email_resource_id BIGINT UNSIGNED NOT NULL,
    recipient VARCHAR(255) NOT NULL,
    status VARCHAR(32) NOT NULL DEFAULT 'pending',
    attempts INT NOT NULL DEFAULT 0,
    max_attempts INT NOT NULL DEFAULT 3,
    since_at DATETIME NULL,
    until_at DATETIME NULL,
    fetched_count INT NOT NULL DEFAULT 0,
    stored_count INT NOT NULL DEFAULT 0,
    matched_count INT NOT NULL DEFAULT 0,
    last_safe_error VARCHAR(500) NOT NULL DEFAULT '',
    request_id VARCHAR(64) NOT NULL DEFAULT '',
    started_at DATETIME NULL,
    finished_at DATETIME NULL,
    active_resource_id BIGINT UNSIGNED GENERATED ALWAYS AS (
        CASE WHEN status IN ('pending', 'queued', 'running') THEN email_resource_id ELSE NULL END
    ) STORED,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    UNIQUE INDEX idx_mailmatch_fetch_jobs_active_resource (active_resource_id),
    INDEX idx_mailmatch_fetch_jobs_status_updated (status, updated_at, id),
    INDEX idx_mailmatch_fetch_jobs_order_created (order_no, created_at, id),
    INDEX idx_mailmatch_fetch_jobs_resource_created (email_resource_id, created_at, id),
    CONSTRAINT fk_mailmatch_fetch_jobs_order FOREIGN KEY (order_no) REFERENCES orders(order_no) ON DELETE RESTRICT,
    CONSTRAINT fk_mailmatch_fetch_jobs_project FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE RESTRICT,
    CONSTRAINT fk_mailmatch_fetch_jobs_resource FOREIGN KEY (email_resource_id, allocation_type) REFERENCES email_resources(id, type) ON DELETE RESTRICT,
    CONSTRAINT chk_mailmatch_fetch_jobs_purpose CHECK (purpose IN ('order_fetch', 'manual_fetch', 'auto_refresh', 'aftersale_check', 'inbound_consume')),
    CONSTRAINT chk_mailmatch_fetch_jobs_allocation_type CHECK (allocation_type IN ('microsoft', 'domain')),
    CONSTRAINT chk_mailmatch_fetch_jobs_status CHECK (status IN ('pending', 'queued', 'running', 'succeeded', 'failed', 'skipped')),
    CONSTRAINT chk_mailmatch_fetch_jobs_attempts CHECK (attempts >= 0 AND max_attempts > 0 AND attempts <= max_attempts),
    CONSTRAINT chk_mailmatch_fetch_jobs_counts CHECK (fetched_count >= 0 AND stored_count >= 0 AND matched_count >= 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE mailmatch_resource_fetch_jobs (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    resource_id BIGINT UNSIGNED NOT NULL,
    operator_user_id BIGINT UNSIGNED NOT NULL,
    expected_credential_revision BIGINT UNSIGNED NOT NULL,
    recipient VARCHAR(255) NOT NULL,
    status VARCHAR(32) NOT NULL DEFAULT 'queued',
    active_resource_id BIGINT UNSIGNED GENERATED ALWAYS AS (
        CASE WHEN status IN ('queued', 'running') THEN resource_id ELSE NULL END
    ) STORED,
    attempts INT UNSIGNED NOT NULL DEFAULT 0,
    max_attempts INT UNSIGNED NOT NULL DEFAULT 3,
    fetched_count INT UNSIGNED NOT NULL DEFAULT 0,
    stored_count INT UNSIGNED NOT NULL DEFAULT 0,
    matched_count INT UNSIGNED NOT NULL DEFAULT 0,
    since_at DATETIME(3) NULL,
    until_at DATETIME(3) NULL,
    claim_token CHAR(36) NOT NULL DEFAULT '',
    dispatch_token CHAR(36) NOT NULL DEFAULT '',
    last_safe_error VARCHAR(500) NOT NULL DEFAULT '',
    request_id VARCHAR(64) NOT NULL DEFAULT '',
    path VARCHAR(255) NOT NULL DEFAULT '',
    dispatched_at DATETIME(3) NULL,
    started_at DATETIME(3) NULL,
    finished_at DATETIME(3) NULL,
    created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
    UNIQUE INDEX idx_mailmatch_resource_fetch_active (active_resource_id),
    INDEX idx_mailmatch_resource_fetch_dispatch (status, dispatched_at, id),
    INDEX idx_mailmatch_resource_fetch_resource_created (resource_id, created_at, id),
    CONSTRAINT fk_mailmatch_resource_fetch_resource FOREIGN KEY (resource_id) REFERENCES microsoft_resources(id) ON DELETE RESTRICT,
    CONSTRAINT fk_mailmatch_resource_fetch_operator FOREIGN KEY (operator_user_id) REFERENCES users(id) ON DELETE RESTRICT,
    CONSTRAINT chk_mailmatch_resource_fetch_status CHECK (status IN ('queued', 'running', 'succeeded', 'failed', 'canceled')),
    CONSTRAINT chk_mailmatch_resource_fetch_attempts CHECK (attempts <= max_attempts AND max_attempts > 0),
    CONSTRAINT chk_mailmatch_resource_fetch_counts CHECK (stored_count <= fetched_count AND matched_count <= stored_count)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE mailmatch_resource_fetch_requests (
    operator_user_id BIGINT UNSIGNED NOT NULL,
    idempotency_key VARCHAR(128) NOT NULL,
    resource_id BIGINT UNSIGNED NOT NULL,
    job_id BIGINT UNSIGNED NOT NULL,
    reused BOOLEAN NOT NULL DEFAULT FALSE,
    created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    PRIMARY KEY (operator_user_id, idempotency_key),
    INDEX idx_mailmatch_resource_fetch_request_job (job_id),
    INDEX idx_mailmatch_resource_fetch_request_resource (resource_id),
    CONSTRAINT fk_mailmatch_resource_fetch_request_operator FOREIGN KEY (operator_user_id) REFERENCES users(id) ON DELETE RESTRICT,
    CONSTRAINT fk_mailmatch_resource_fetch_request_resource FOREIGN KEY (resource_id) REFERENCES microsoft_resources(id) ON DELETE RESTRICT,
    CONSTRAINT fk_mailmatch_resource_fetch_request_job FOREIGN KEY (job_id) REFERENCES mailmatch_resource_fetch_jobs(id) ON DELETE RESTRICT
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

ALTER TABLE mailmatch_resource_fetch_states
    DROP FOREIGN KEY fk_mailmatch_fetch_state_operator,
    DROP CHECK chk_mailmatch_fetch_state_counts,
    DROP CHECK chk_mailmatch_fetch_state_purpose,
    DROP CHECK chk_mailmatch_fetch_state_operation,
    DROP CHECK chk_mailmatch_fetch_state_failures,
    DROP CHECK chk_mailmatch_fetch_state_status,
    DROP INDEX idx_mailmatch_fetch_state_pending;

UPDATE mailmatch_resource_fetch_states
SET status = CASE status
    WHEN 'pending' THEN 'pending'
    WHEN 'processing' THEN 'running'
    WHEN 'abnormal' THEN 'failed'
    ELSE 'succeeded'
END;

ALTER TABLE mailmatch_resource_fetch_states
    ADD COLUMN last_job_id BIGINT UNSIGNED NULL AFTER email_resource_id,
    CHANGE COLUMN status last_status VARCHAR(32) NOT NULL DEFAULT '',
    CHANGE COLUMN requested_at last_submitted_at DATETIME NULL,
    DROP COLUMN generation,
    DROP COLUMN failures,
    DROP COLUMN operation_kind,
    DROP COLUMN order_no,
    DROP COLUMN purpose,
    DROP COLUMN operator_user_id,
    DROP COLUMN expected_credential_revision,
    DROP COLUMN since_at,
    DROP COLUMN until_at,
    DROP COLUMN fetched_count,
    DROP COLUMN stored_count,
    DROP COLUMN matched_count,
    DROP COLUMN request_id,
    DROP COLUMN path,
    DROP COLUMN idempotency_key,
    DROP COLUMN started_at,
    DROP COLUMN finished_at,
    ADD CONSTRAINT fk_mm_resource_fetch_states_job
        FOREIGN KEY (last_job_id) REFERENCES mailmatch_fetch_jobs(id) ON DELETE SET NULL,
    ADD CONSTRAINT chk_mm_resource_fetch_states_status
        CHECK (last_status IN ('', 'pending', 'queued', 'running', 'succeeded', 'failed', 'skipped', 'cooldown'));
