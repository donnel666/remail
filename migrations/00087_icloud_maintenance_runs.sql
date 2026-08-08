-- +goose Up

ALTER TABLE mailmatch_resource_fetch_states
    DROP CHECK chk_mailmatch_fetch_state_operation,
    ADD CONSTRAINT chk_mailmatch_fetch_state_operation
        CHECK (operation_kind IN ('order_fetch', 'resource_fetch', 'resource_history', 'icloud_resource_fetch'));

CREATE TABLE icloud_maintenance_runs (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    resource_id BIGINT UNSIGNED NOT NULL,
    validation_generation BIGINT UNSIGNED NOT NULL,
    kind VARCHAR(24) NOT NULL COMMENT 'validation|alias',
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

    UNIQUE INDEX uk_icloud_maintenance_resource_generation
        (resource_id, validation_generation),
    INDEX idx_icloud_maintenance_resource_updated
        (resource_id, updated_at, id),
    INDEX idx_icloud_maintenance_status
        (status, updated_at, id),

    CONSTRAINT fk_icloud_maintenance_resource
        FOREIGN KEY (resource_id) REFERENCES icloud_resources(id) ON DELETE CASCADE,
    CONSTRAINT chk_icloud_maintenance_kind
        CHECK (kind IN ('validation', 'alias')),
    CONSTRAINT chk_icloud_maintenance_status
        CHECK (status IN ('queued', 'running', 'succeeded', 'failed', 'canceled')),
    CONSTRAINT chk_icloud_maintenance_attempts
        CHECK (max_attempts > 0 AND attempts <= max_attempts)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

INSERT INTO icloud_maintenance_runs(
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
        WHEN 'deleted' THEN 'canceled'
        ELSE 'succeeded'
    END,
    LEAST(resource.validation_failures, 3),
    3,
    resource.credential_revision,
    CASE
        WHEN resource.status IN ('pending', 'validating') THEN resource.updated_at
        ELSE COALESCE(resource.last_checked_at, resource.updated_at)
    END,
    CASE
        WHEN resource.status = 'validating' THEN resource.updated_at
        WHEN resource.last_checked_at IS NOT NULL THEN resource.last_checked_at
        ELSE NULL
    END,
    CASE
        WHEN resource.status NOT IN ('pending', 'validating') THEN resource.last_checked_at
        ELSE NULL
    END,
    resource.last_safe_error,
    resource.created_at,
    resource.updated_at
FROM icloud_resources AS resource
WHERE resource.validation_generation > 0;

-- +goose Down

DROP TABLE IF EXISTS icloud_maintenance_runs;

UPDATE mailmatch_resource_fetch_states
SET operation_kind = 'order_fetch'
WHERE operation_kind = 'icloud_resource_fetch';

ALTER TABLE mailmatch_resource_fetch_states
    DROP CHECK chk_mailmatch_fetch_state_operation,
    ADD CONSTRAINT chk_mailmatch_fetch_state_operation
        CHECK (operation_kind IN ('order_fetch', 'resource_fetch', 'resource_history'));
