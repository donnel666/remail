-- +goose Up

ALTER TABLE microsoft_resources
    ADD COLUMN token_refresh_status VARCHAR(32) NOT NULL DEFAULT 'normal'
        COMMENT 'pending|processing|normal|abnormal' AFTER token_last_request_id,
    ADD COLUMN token_refresh_generation BIGINT UNSIGNED NOT NULL DEFAULT 0
        AFTER token_refresh_status,
    ADD COLUMN token_refresh_failures TINYINT UNSIGNED NOT NULL DEFAULT 0
        AFTER token_refresh_generation,
    ADD COLUMN token_refresh_expected_credential_revision BIGINT UNSIGNED NOT NULL DEFAULT 0
        AFTER token_refresh_failures,
    ADD COLUMN token_refresh_operator_user_id BIGINT UNSIGNED NULL
        AFTER token_refresh_expected_credential_revision,
    ADD COLUMN token_refresh_idempotency_key VARCHAR(128) NOT NULL DEFAULT ''
        AFTER token_refresh_operator_user_id,
    ADD COLUMN token_refresh_request_id VARCHAR(64) NOT NULL DEFAULT ''
        AFTER token_refresh_idempotency_key,
    ADD COLUMN token_refresh_path VARCHAR(255) NOT NULL DEFAULT ''
        AFTER token_refresh_request_id,
    ADD COLUMN token_refresh_last_safe_error VARCHAR(500) NOT NULL DEFAULT ''
        AFTER token_refresh_path,
    ADD COLUMN token_refresh_requested_at DATETIME(3) NULL
        AFTER token_refresh_last_safe_error,
    ADD COLUMN token_refresh_started_at DATETIME(3) NULL
        AFTER token_refresh_requested_at,
    ADD COLUMN token_refresh_finished_at DATETIME(3) NULL
        AFTER token_refresh_started_at,
    ADD COLUMN token_refresh_idempotency_scope VARCHAR(320) GENERATED ALWAYS AS (
        CASE
            WHEN token_refresh_operator_user_id IS NULL OR token_refresh_idempotency_key = '' THEN NULL
            ELSE CONCAT(token_refresh_operator_user_id, ':', token_refresh_idempotency_key)
        END
    ) STORED AFTER token_refresh_finished_at,
    ADD UNIQUE INDEX idx_microsoft_token_refresh_idempotency (token_refresh_idempotency_scope),
    ADD INDEX idx_microsoft_token_refresh_pending (token_refresh_status, token_refresh_requested_at, id),
    ADD CONSTRAINT chk_microsoft_token_refresh_state_status
        CHECK (token_refresh_status IN ('pending', 'processing', 'normal', 'abnormal')),
    ADD CONSTRAINT chk_microsoft_token_refresh_state_failures
        CHECK (token_refresh_failures <= 3);

-- Keep only the latest visible state per resource. Active Redis work is not
-- migrated: queued/running rows become pending and receive a fresh generation.
UPDATE microsoft_resources AS resource
JOIN (
    SELECT job.*
    FROM microsoft_token_refresh_jobs AS job
    JOIN (
        SELECT resource_id, MAX(id) AS id
        FROM microsoft_token_refresh_jobs
        GROUP BY resource_id
    ) AS latest ON latest.id = job.id
) AS job ON job.resource_id = resource.id
SET resource.token_refresh_status = CASE job.status
        WHEN 'queued' THEN 'pending'
        WHEN 'running' THEN 'pending'
        WHEN 'failed' THEN 'abnormal'
        ELSE 'normal'
    END,
    resource.token_refresh_generation = 1,
    resource.token_refresh_failures = CASE
        WHEN job.status = 'failed' THEN LEAST(job.attempts, 3)
        ELSE 0
    END,
    resource.token_refresh_expected_credential_revision = job.expected_credential_revision,
    resource.token_refresh_operator_user_id = job.operator_user_id,
    resource.token_refresh_idempotency_key = COALESCE((
        SELECT request.idempotency_key
        FROM microsoft_token_refresh_requests AS request
        WHERE request.job_id = job.id
          AND request.operator_user_id = job.operator_user_id
        ORDER BY request.created_at DESC, request.idempotency_key DESC
        LIMIT 1
    ), ''),
    resource.token_refresh_request_id = job.request_id,
    resource.token_refresh_path = job.path,
    resource.token_refresh_last_safe_error = job.last_safe_error,
    resource.token_refresh_requested_at = job.created_at,
    resource.token_refresh_started_at = CASE WHEN job.status = 'running' THEN NULL ELSE job.started_at END,
    resource.token_refresh_finished_at = job.finished_at;

ALTER TABLE microsoft_alias_schedules
    ADD COLUMN generation BIGINT UNSIGNED NOT NULL DEFAULT 1 AFTER resource_id;

UPDATE microsoft_alias_schedules
SET status = 'pending',
    generation = generation + 1,
    claim_token = '',
    next_run_at = LEAST(next_run_at, CURRENT_TIMESTAMP(3)),
    updated_at = CURRENT_TIMESTAMP(3)
WHERE status IN ('queued', 'running');

DROP TABLE microsoft_token_refresh_requests;
DROP TABLE microsoft_token_refresh_jobs;

-- +goose Down

CREATE TABLE microsoft_token_refresh_jobs (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    resource_id BIGINT UNSIGNED NOT NULL,
    operator_user_id BIGINT UNSIGNED NOT NULL,
    expected_credential_revision BIGINT UNSIGNED NOT NULL,
    status VARCHAR(32) NOT NULL DEFAULT 'queued' COMMENT 'queued|running|succeeded|failed|canceled',
    active_resource_id BIGINT UNSIGNED GENERATED ALWAYS AS (
        CASE WHEN status IN ('queued', 'running') THEN resource_id ELSE NULL END
    ) STORED,
    attempts INT UNSIGNED NOT NULL DEFAULT 0,
    max_attempts INT UNSIGNED NOT NULL DEFAULT 3,
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
    UNIQUE INDEX idx_microsoft_token_refresh_active_resource (active_resource_id),
    INDEX idx_microsoft_token_refresh_dispatch (status, dispatched_at, id),
    INDEX idx_microsoft_token_refresh_resource_created (resource_id, created_at, id),
    CONSTRAINT fk_microsoft_token_refresh_resource
        FOREIGN KEY (resource_id) REFERENCES microsoft_resources(id) ON DELETE RESTRICT,
    CONSTRAINT fk_microsoft_token_refresh_operator
        FOREIGN KEY (operator_user_id) REFERENCES users(id) ON DELETE RESTRICT,
    CONSTRAINT chk_microsoft_token_refresh_status
        CHECK (status IN ('queued', 'running', 'succeeded', 'failed', 'canceled')),
    CONSTRAINT chk_microsoft_token_refresh_attempts
        CHECK (attempts <= max_attempts AND max_attempts > 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE microsoft_token_refresh_requests (
    operator_user_id BIGINT UNSIGNED NOT NULL,
    idempotency_key VARCHAR(128) NOT NULL,
    resource_id BIGINT UNSIGNED NOT NULL,
    job_id BIGINT UNSIGNED NOT NULL,
    reused BOOLEAN NOT NULL DEFAULT FALSE,
    created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    PRIMARY KEY (operator_user_id, idempotency_key),
    INDEX idx_microsoft_token_refresh_request_job (job_id),
    INDEX idx_microsoft_token_refresh_request_resource (resource_id),
    CONSTRAINT fk_microsoft_token_refresh_request_operator
        FOREIGN KEY (operator_user_id) REFERENCES users(id) ON DELETE RESTRICT,
    CONSTRAINT fk_microsoft_token_refresh_request_resource
        FOREIGN KEY (resource_id) REFERENCES microsoft_resources(id) ON DELETE RESTRICT,
    CONSTRAINT fk_microsoft_token_refresh_request_job
        FOREIGN KEY (job_id) REFERENCES microsoft_token_refresh_jobs(id) ON DELETE RESTRICT,
    CONSTRAINT chk_microsoft_token_refresh_request_key CHECK (idempotency_key <> '')
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

ALTER TABLE microsoft_alias_schedules
    DROP COLUMN generation;

ALTER TABLE microsoft_resources
    DROP CHECK chk_microsoft_token_refresh_state_status,
    DROP CHECK chk_microsoft_token_refresh_state_failures,
    DROP INDEX idx_microsoft_token_refresh_idempotency,
    DROP INDEX idx_microsoft_token_refresh_pending,
    DROP COLUMN token_refresh_idempotency_scope,
    DROP COLUMN token_refresh_finished_at,
    DROP COLUMN token_refresh_started_at,
    DROP COLUMN token_refresh_requested_at,
    DROP COLUMN token_refresh_last_safe_error,
    DROP COLUMN token_refresh_path,
    DROP COLUMN token_refresh_request_id,
    DROP COLUMN token_refresh_idempotency_key,
    DROP COLUMN token_refresh_operator_user_id,
    DROP COLUMN token_refresh_expected_credential_revision,
    DROP COLUMN token_refresh_failures,
    DROP COLUMN token_refresh_generation,
    DROP COLUMN token_refresh_status;
