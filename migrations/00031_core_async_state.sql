-- +goose Up

-- Administrator Microsoft bulk selection/cursor state is transient. Redis owns
-- the TTL lease and Asynq cursor; resource rows remain the only business facts.
DROP TABLE IF EXISTS admin_resource_bulk_commands;

-- Imports are durable business facts, but dispatch state is a fenced handoff:
-- pending -> queued (accepted by Redis) -> running (worker activation) ->
-- succeeded/failed. A generation invalidates every task from an older retry.
ALTER TABLE resource_imports
    ADD COLUMN generation BIGINT UNSIGNED NOT NULL DEFAULT 1 AFTER max_attempts;

UPDATE resource_imports
SET dispatch_status = 'pending'
WHERE dispatch_status IN ('legacy', 'queued');

ALTER TABLE resource_imports
    MODIFY COLUMN dispatch_status VARCHAR(32) NOT NULL DEFAULT 'pending',
    DROP CHECK chk_resource_imports_dispatch_status,
    DROP INDEX idx_resource_imports_dispatch,
    DROP COLUMN dispatch_token,
    DROP COLUMN dispatched_at,
    ADD CONSTRAINT chk_resource_imports_dispatch_status
        CHECK (dispatch_status IN ('pending', 'queued', 'running', 'succeeded', 'failed')),
    ADD INDEX idx_resource_imports_pending_generation (status, dispatch_status, generation, id);

-- +goose Down

UPDATE resource_imports
SET dispatch_status = 'queued'
WHERE dispatch_status = 'pending';

ALTER TABLE resource_imports
    DROP INDEX idx_resource_imports_pending_generation,
    DROP COLUMN generation,
    DROP CHECK chk_resource_imports_dispatch_status,
    MODIFY COLUMN dispatch_status VARCHAR(32) NOT NULL DEFAULT 'legacy',
    ADD COLUMN dispatch_token CHAR(36) NOT NULL DEFAULT '' AFTER claim_token,
    ADD COLUMN dispatched_at DATETIME(3) NULL AFTER dispatch_token,
    ADD INDEX idx_resource_imports_dispatch (dispatch_status, dispatched_at, id),
    ADD CONSTRAINT chk_resource_imports_dispatch_status
        CHECK (dispatch_status IN ('legacy', 'queued', 'running', 'succeeded', 'failed'));

-- Older down migrations still alter and finally drop this table. Recreate the
-- version introduced by 00009 so a full rollback remains executable.
CREATE TABLE admin_resource_bulk_commands (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    operator_user_id BIGINT UNSIGNED NOT NULL,
    action VARCHAR(32) NOT NULL COMMENT 'validate|publish|unpublish|delete',
    selection_mode VARCHAR(16) NOT NULL COMMENT 'ids|filter',
    selection_json JSON NOT NULL,
    selection_fingerprint CHAR(64) NOT NULL,
    idempotency_key VARCHAR(128) NOT NULL DEFAULT '',
    idempotency_scope VARCHAR(320) GENERATED ALWAYS AS (
        CASE
            WHEN idempotency_key = '' THEN NULL
            ELSE CONCAT(operator_user_id, ':', idempotency_key)
        END
    ) STORED,
    max_resource_id BIGINT UNSIGNED NOT NULL DEFAULT 0,
    checkpoint_resource_id BIGINT UNSIGNED NOT NULL DEFAULT 0,
    status VARCHAR(32) NOT NULL DEFAULT 'queued' COMMENT 'queued|running|succeeded|failed|canceled',
    matched_count INT UNSIGNED NOT NULL DEFAULT 0,
    processed_count INT UNSIGNED NOT NULL DEFAULT 0,
    affected_count INT UNSIGNED NOT NULL DEFAULT 0,
    skipped_count INT UNSIGNED NOT NULL DEFAULT 0,
    reason_buckets JSON NULL,
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
    UNIQUE INDEX idx_admin_resource_bulk_idempotency_scope (idempotency_scope),
    INDEX idx_admin_resource_bulk_dispatch (status, dispatched_at, id),
    CONSTRAINT fk_admin_resource_bulk_operator
        FOREIGN KEY (operator_user_id) REFERENCES users(id) ON DELETE RESTRICT,
    CONSTRAINT chk_admin_resource_bulk_action
        CHECK (action IN ('validate', 'publish', 'unpublish', 'delete')),
    CONSTRAINT chk_admin_resource_bulk_selection
        CHECK (selection_mode IN ('ids', 'filter')),
    CONSTRAINT chk_admin_resource_bulk_status
        CHECK (status IN ('queued', 'running', 'succeeded', 'failed', 'canceled')),
    CONSTRAINT chk_admin_resource_bulk_checkpoint
        CHECK (checkpoint_resource_id <= max_resource_id),
    CONSTRAINT chk_admin_resource_bulk_counts CHECK (
        processed_count <= matched_count
        AND affected_count + skipped_count <= processed_count
    ),
    CONSTRAINT chk_admin_resource_bulk_attempts
        CHECK (attempts <= max_attempts AND max_attempts > 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
