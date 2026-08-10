-- +goose Up

-- Administrator-triggered full mailbox fetches own a separate lifecycle from
-- project-history identification. No legacy administrator task is migrated.
CREATE TABLE mailmatch_admin_resource_fetch_states (
    email_resource_id BIGINT UNSIGNED PRIMARY KEY,
    status VARCHAR(32) NOT NULL DEFAULT 'normal'
        COMMENT 'pending|processing|normal|abnormal',
    generation BIGINT UNSIGNED NOT NULL DEFAULT 0,
    failures TINYINT UNSIGNED NOT NULL DEFAULT 0,
    operation_kind VARCHAR(32) NOT NULL DEFAULT 'resource_fetch'
        COMMENT 'resource_fetch|icloud_resource_fetch',
    order_no VARCHAR(64) NOT NULL DEFAULT '',
    purpose VARCHAR(32) NOT NULL DEFAULT 'manual_fetch',
    operator_user_id BIGINT UNSIGNED NULL,
    expected_credential_revision BIGINT UNSIGNED NOT NULL DEFAULT 0,
    since_at DATETIME(3) NULL,
    until_at DATETIME(3) NULL,
    fetched_count INT UNSIGNED NOT NULL DEFAULT 0,
    stored_count INT UNSIGNED NOT NULL DEFAULT 0,
    matched_count INT UNSIGNED NOT NULL DEFAULT 0,
    request_id VARCHAR(64) NOT NULL DEFAULT '',
    path VARCHAR(255) NOT NULL DEFAULT '',
    idempotency_key VARCHAR(128) NOT NULL DEFAULT '',
    requested_at DATETIME(3) NULL,
    started_at DATETIME(3) NULL,
    finished_at DATETIME(3) NULL,
    last_success_at DATETIME NULL,
    last_received_at DATETIME NULL,
    cooldown_until DATETIME NULL,
    last_safe_error VARCHAR(500) NOT NULL DEFAULT '',
    created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3)
        ON UPDATE CURRENT_TIMESTAMP(3),

    INDEX idx_mailmatch_admin_fetch_pending
        (status, operation_kind, requested_at, email_resource_id),

    CONSTRAINT fk_mailmatch_admin_fetch_resource
        FOREIGN KEY (email_resource_id) REFERENCES email_resources(id) ON DELETE CASCADE,
    CONSTRAINT fk_mailmatch_admin_fetch_operator
        FOREIGN KEY (operator_user_id) REFERENCES users(id) ON DELETE SET NULL,
    CONSTRAINT chk_mailmatch_admin_fetch_status
        CHECK (status IN ('pending', 'processing', 'normal', 'abnormal')),
    CONSTRAINT chk_mailmatch_admin_fetch_failures CHECK (failures <= 3),
    CONSTRAINT chk_mailmatch_admin_fetch_operation
        CHECK (operation_kind IN ('resource_fetch', 'icloud_resource_fetch')),
    CONSTRAINT chk_mailmatch_admin_fetch_purpose CHECK (purpose = 'manual_fetch'),
    CONSTRAINT chk_mailmatch_admin_fetch_counts
        CHECK (stored_count <= fetched_count AND matched_count <= stored_count)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- Old administrator tasks used the shared history state and the retired
-- mailmatch:resource_fetch task type. End them instead of leaving processing
-- rows that can never be acknowledged by the new foreground worker.
UPDATE mailmatch_resource_fetch_states
SET status = 'abnormal',
    started_at = NULL,
    finished_at = UTC_TIMESTAMP(3),
    last_safe_error = 'Retired during administrator mail-fetch queue split.'
WHERE operation_kind IN ('resource_fetch', 'icloud_resource_fetch')
  AND status IN ('pending', 'processing');

-- History keeps its old durable state but receives a new task type. Release
-- already-dispatched rows so the new history dispatcher can enqueue them.
UPDATE mailmatch_resource_fetch_states
SET status = 'pending',
    generation = generation + 1,
    started_at = NULL,
    finished_at = NULL,
    last_safe_error = ''
WHERE operation_kind = 'resource_history'
  AND status = 'processing';

-- +goose Down

DROP TABLE IF EXISTS mailmatch_admin_resource_fetch_states;
