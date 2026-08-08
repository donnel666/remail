-- +goose Up

CREATE TABLE gmail_maintenance_runs (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    resource_id BIGINT UNSIGNED NOT NULL,
    validation_generation BIGINT UNSIGNED NOT NULL,
    kind VARCHAR(24) NOT NULL COMMENT 'validation|history',
    status VARCHAR(24) NOT NULL DEFAULT 'queued'
        COMMENT 'queued|running|succeeded|failed|canceled',
    attempts INT UNSIGNED NOT NULL DEFAULT 0,
    max_attempts INT UNSIGNED NOT NULL DEFAULT 3,
    credential_revision BIGINT UNSIGNED NOT NULL,
    queued_at DATETIME(3) NOT NULL,
    started_at DATETIME(3) NULL,
    finished_at DATETIME(3) NULL,
    last_safe_error VARCHAR(500) NOT NULL DEFAULT '',
    created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3)
        ON UPDATE CURRENT_TIMESTAMP(3),

    INDEX idx_gmail_maintenance_resource_updated
        (resource_id, updated_at, id),
    INDEX idx_gmail_maintenance_resource_generation
        (resource_id, validation_generation, kind, id),
    INDEX idx_gmail_maintenance_dispatch
        (kind, status, updated_at, id),

    CONSTRAINT fk_gmail_maintenance_resource
        FOREIGN KEY (resource_id) REFERENCES gmail_resources(id) ON DELETE CASCADE,
    CONSTRAINT chk_gmail_maintenance_kind
        CHECK (kind IN ('validation', 'history')),
    CONSTRAINT chk_gmail_maintenance_status
        CHECK (status IN ('queued', 'running', 'succeeded', 'failed', 'canceled')),
    CONSTRAINT chk_gmail_maintenance_attempts
        CHECK (max_attempts > 0 AND attempts <= max_attempts)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

INSERT INTO gmail_maintenance_runs(
    resource_id, validation_generation, kind, status, attempts, max_attempts,
    credential_revision, queued_at, started_at, finished_at, last_safe_error,
    created_at, updated_at
)
SELECT
    resource.id,
    resource.validation_generation,
    'validation',
    CASE resource.status
        WHEN 'pending' THEN 'queued'
        WHEN 'validating' THEN 'running'
        WHEN 'abnormal' THEN 'failed'
        WHEN 'disabled' THEN 'canceled'
        WHEN 'deleted' THEN 'canceled'
        ELSE 'succeeded'
    END,
    CASE
        WHEN resource.status = 'pending' THEN LEAST(resource.validation_failures, 3)
        ELSE LEAST(resource.validation_failures + 1, 3)
    END,
    3,
    resource.credential_revision,
    CASE
        WHEN resource.status IN ('pending', 'validating') THEN resource.updated_at
        ELSE COALESCE(resource.last_checked_at, resource.updated_at)
    END,
    CASE
        WHEN resource.status = 'validating' THEN resource.updated_at
        WHEN resource.status NOT IN ('pending', 'validating') AND resource.last_checked_at IS NOT NULL
            THEN resource.last_checked_at
        ELSE NULL
    END,
    CASE
        WHEN resource.status NOT IN ('pending', 'validating') THEN resource.updated_at
        ELSE NULL
    END,
    resource.last_safe_error,
    resource.created_at,
    resource.updated_at
FROM gmail_resources AS resource
WHERE resource.validation_generation > 0;

INSERT INTO gmail_maintenance_runs(
    resource_id, validation_generation, kind, status, attempts, max_attempts,
    credential_revision, queued_at, started_at, finished_at, last_safe_error,
    created_at, updated_at
)
SELECT
    resource.id,
    resource.validation_generation,
    'history',
    CASE resource.status
        WHEN 'identifying' THEN 'queued'
        WHEN 'disabled' THEN 'canceled'
        WHEN 'deleted' THEN 'canceled'
        ELSE 'succeeded'
    END,
    CASE WHEN resource.status = 'identifying' THEN 0 ELSE 1 END,
    3,
    resource.credential_revision,
    resource.last_checked_at,
    CASE WHEN resource.status = 'identifying' THEN NULL ELSE resource.last_checked_at END,
    CASE WHEN resource.status = 'identifying' THEN NULL ELSE resource.updated_at END,
    CASE WHEN resource.status = 'identifying' THEN '' ELSE resource.last_safe_error END,
    resource.created_at,
    resource.updated_at
FROM gmail_resources AS resource
WHERE resource.last_checked_at IS NOT NULL
  AND resource.status IN ('identifying', 'normal', 'available', 'leased', 'sold', 'disabled', 'deleted');

-- +goose Down

DROP TABLE IF EXISTS gmail_maintenance_runs;
