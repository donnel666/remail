-- +goose Up

ALTER TABLE domain_resources
    ADD COLUMN last_safe_error VARCHAR(500) NOT NULL DEFAULT '' COMMENT 'sanitized diagnostic summary' AFTER status;

CREATE TABLE resource_validation_jobs (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    resource_id BIGINT UNSIGNED NOT NULL,
    resource_type VARCHAR(32) NOT NULL COMMENT 'microsoft|domain',
    owner_user_id BIGINT UNSIGNED NOT NULL,
    status VARCHAR(32) NOT NULL DEFAULT 'queued' COMMENT 'queued|running|succeeded|failed',
    active_resource_id BIGINT UNSIGNED GENERATED ALWAYS AS (CASE WHEN status IN ('queued', 'running') THEN resource_id ELSE NULL END) STORED,
    attempts INT NOT NULL DEFAULT 0,
    max_attempts INT NOT NULL DEFAULT 3,
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
    UNIQUE INDEX idx_resource_validation_jobs_active_resource (active_resource_id),
    CONSTRAINT fk_resource_validation_jobs_resource_owner FOREIGN KEY (resource_id, resource_type, owner_user_id) REFERENCES email_resources(id, type, owner_user_id) ON DELETE RESTRICT,
    CONSTRAINT chk_resource_validation_jobs_type CHECK (resource_type IN ('microsoft', 'domain')),
    CONSTRAINT chk_resource_validation_jobs_status CHECK (status IN ('queued', 'running', 'succeeded', 'failed')),
    CONSTRAINT chk_resource_validation_jobs_attempts CHECK (attempts >= 0 AND max_attempts > 0 AND attempts <= max_attempts)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- +goose Down
DROP TABLE IF EXISTS resource_validation_jobs;
ALTER TABLE domain_resources
    DROP COLUMN last_safe_error;
