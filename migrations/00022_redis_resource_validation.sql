-- +goose Up
-- Validation batch cursors, per-resource attempts and retry state live only in
-- Redis/Asynq. The database owns resource health and assignment state only.
DROP TABLE IF EXISTS resource_validation_batches;
DROP TABLE IF EXISTS resource_validation_jobs;

ALTER TABLE microsoft_resources
    DROP CHECK chk_microsoft_status,
    MODIFY COLUMN status VARCHAR(32) NOT NULL DEFAULT 'pending'
        COMMENT 'pending|validating|normal|abnormal|disabled|deleted',
    ADD CONSTRAINT chk_microsoft_status
        CHECK (status IN ('pending', 'validating', 'normal', 'abnormal', 'disabled', 'deleted'));

ALTER TABLE domain_resources
    DROP CHECK chk_domain_resources_status,
    MODIFY COLUMN status VARCHAR(32) NOT NULL DEFAULT 'abnormal'
        COMMENT 'pending|validating|normal|abnormal|disabled|deleted',
    ADD CONSTRAINT chk_domain_resources_status
        CHECK (status IN ('pending', 'validating', 'normal', 'abnormal', 'disabled', 'deleted'));

CREATE INDEX idx_domain_resources_status ON domain_resources(status);

-- +goose Down
DROP INDEX idx_domain_resources_status ON domain_resources;

UPDATE microsoft_resources
SET status = 'pending'
WHERE status = 'validating';

UPDATE domain_resources
SET status = 'abnormal'
WHERE status IN ('pending', 'validating');

ALTER TABLE microsoft_resources
    DROP CHECK chk_microsoft_status,
    MODIFY COLUMN status VARCHAR(32) NOT NULL DEFAULT 'pending'
        COMMENT 'pending|normal|abnormal|disabled|deleted',
    ADD CONSTRAINT chk_microsoft_status
        CHECK (status IN ('pending', 'normal', 'abnormal', 'disabled', 'deleted'));

ALTER TABLE domain_resources
    DROP CHECK chk_domain_resources_status,
    MODIFY COLUMN status VARCHAR(32) NOT NULL DEFAULT 'abnormal'
        COMMENT 'normal|abnormal|disabled|deleted',
    ADD CONSTRAINT chk_domain_resources_status
        CHECK (status IN ('normal', 'abnormal', 'disabled', 'deleted'));

CREATE TABLE resource_validation_jobs (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    resource_id BIGINT UNSIGNED NOT NULL,
    resource_type VARCHAR(32) NOT NULL COMMENT 'microsoft|domain',
    owner_user_id BIGINT UNSIGNED NOT NULL,
    expected_credential_revision BIGINT UNSIGNED NOT NULL DEFAULT 0,
    status VARCHAR(32) NOT NULL DEFAULT 'queued' COMMENT 'queued|running|succeeded|failed',
    active_resource_id BIGINT UNSIGNED GENERATED ALWAYS AS (CASE WHEN status IN ('queued', 'running') THEN resource_id ELSE NULL END) STORED,
    attempts INT NOT NULL DEFAULT 0,
    max_attempts INT NOT NULL DEFAULT 3,
    claim_token CHAR(36) NOT NULL DEFAULT '',
    dispatch_token CHAR(36) NOT NULL DEFAULT '',
    dispatched_at DATETIME(3) NULL,
    last_safe_error VARCHAR(500) NOT NULL DEFAULT '',
    request_id VARCHAR(64) NOT NULL DEFAULT '',
    path VARCHAR(255) NOT NULL DEFAULT '',
    started_at DATETIME NULL,
    finished_at DATETIME NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    INDEX idx_resource_validation_jobs_status_updated (status, updated_at, id),
    INDEX idx_resource_validation_jobs_resource_created (resource_id, created_at),
    INDEX idx_resource_validation_jobs_owner_created (owner_user_id, created_at),
    INDEX idx_resource_validation_jobs_request_id (request_id),
    INDEX idx_resource_validation_dispatch (status, dispatched_at, id),
    UNIQUE INDEX idx_resource_validation_jobs_active_resource (active_resource_id),
    CONSTRAINT fk_resource_validation_jobs_resource
        FOREIGN KEY (resource_id, resource_type) REFERENCES email_resources(id, type) ON DELETE RESTRICT,
    CONSTRAINT fk_resource_validation_jobs_owner
        FOREIGN KEY (owner_user_id) REFERENCES users(id) ON DELETE RESTRICT,
    CONSTRAINT chk_resource_validation_jobs_type CHECK (resource_type IN ('microsoft', 'domain')),
    CONSTRAINT chk_resource_validation_jobs_status CHECK (status IN ('queued', 'running', 'succeeded', 'failed')),
    CONSTRAINT chk_resource_validation_jobs_attempts CHECK (attempts >= 0 AND max_attempts > 0 AND attempts <= max_attempts)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
