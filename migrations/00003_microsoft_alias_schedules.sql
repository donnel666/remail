-- +goose Up
CREATE TABLE microsoft_alias_schedules (
    resource_id BIGINT UNSIGNED PRIMARY KEY,
    status VARCHAR(32) NOT NULL DEFAULT 'pending' COMMENT 'pending|queued|running|paused',
    next_run_at DATETIME(3) NOT NULL,
    attempts INT UNSIGNED NOT NULL DEFAULT 0,
    claim_token CHAR(32) NOT NULL DEFAULT '',
    last_safe_error VARCHAR(500) NOT NULL DEFAULT '',
    last_run_at DATETIME(3) NULL,
    created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
    INDEX idx_microsoft_alias_schedules_due (status, next_run_at, resource_id),
    INDEX idx_microsoft_alias_schedules_stale (status, updated_at, resource_id),
    CONSTRAINT fk_microsoft_alias_schedules_resource
        FOREIGN KEY (resource_id) REFERENCES microsoft_resources(id) ON DELETE CASCADE,
    CONSTRAINT chk_microsoft_alias_schedules_status CHECK (status IN ('pending', 'queued', 'running', 'paused'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE microsoft_alias_attempts (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    resource_id BIGINT UNSIGNED NOT NULL,
    candidate VARCHAR(255) NOT NULL,
    status VARCHAR(32) NOT NULL COMMENT 'running|succeeded|failed|uncertain',
    quota_at DATETIME(3) NOT NULL,
    category VARCHAR(64) NOT NULL DEFAULT '',
    last_safe_error VARCHAR(500) NOT NULL DEFAULT '',
    completed_at DATETIME(3) NULL,
    created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
    UNIQUE INDEX idx_microsoft_alias_attempts_candidate (resource_id, candidate),
    INDEX idx_microsoft_alias_attempts_quota (resource_id, quota_at),
    INDEX idx_microsoft_alias_attempts_status (resource_id, status, id),
    CONSTRAINT fk_microsoft_alias_attempts_resource
        FOREIGN KEY (resource_id) REFERENCES microsoft_resources(id) ON DELETE CASCADE,
    CONSTRAINT chk_microsoft_alias_attempts_status
        CHECK (status IN ('running', 'succeeded', 'failed', 'uncertain'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

INSERT INTO microsoft_alias_attempts (
    resource_id,
    candidate,
    status,
    quota_at,
    completed_at,
    created_at,
    updated_at
)
SELECT
    resource_id,
    email,
    'succeeded',
    created_at,
    created_at,
    created_at,
    updated_at
FROM explicit_aliases;

-- +goose Down
DROP TABLE IF EXISTS microsoft_alias_attempts;
DROP TABLE IF EXISTS microsoft_alias_schedules;
