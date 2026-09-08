-- +goose Up

CREATE TABLE IF NOT EXISTS proto_sessions (
    resource_id BIGINT UNSIGNED PRIMARY KEY,
    credential_revision BIGINT UNSIGNED NOT NULL,
    version BIGINT UNSIGNED NOT NULL DEFAULT 1,
    payload LONGBLOB NOT NULL COMMENT 'AES-GCM encrypted Proto tokens and unlocked private keys; never expose',
    lease_token CHAR(36) NOT NULL DEFAULT '',
    lease_expires_at DATETIME(3) NULL,
    created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
    CONSTRAINT fk_proto_session_resource FOREIGN KEY (resource_id) REFERENCES proto_resources(id) ON DELETE CASCADE,
    CONSTRAINT chk_proto_session_revisions CHECK (credential_revision > 0 AND version > 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- Resume only explicit placeholder observations. New generations leave the
-- original maintenance rows intact as audit facts and make this re-entry safe.
UPDATE email_resources AS root
JOIN proto_resources AS resource ON resource.id = root.id AND root.type = 'proto'
SET root.version = root.version + 1,
    root.updated_at = CURRENT_TIMESTAMP,
    resource.version = resource.version + 1,
    resource.validation_generation = resource.validation_generation + 1,
    resource.status = 'pending',
    resource.last_safe_error = '',
    resource.validation_failures = 0,
    resource.updated_at = CURRENT_TIMESTAMP
WHERE (resource.status = 'pending' AND resource.last_safe_error = 'proto_validation_todo')
   OR (resource.status = 'identifying' AND resource.last_safe_error = 'proto_history_todo');

UPDATE proto_project_history_scan_states
SET generation = generation + 1, status = 'pending',
    after_id = 0, through_id = 0, failures = 0,
    scanned_count = 0, matched_count = 0, skipped_count = 0,
    last_safe_error = '', requested_at = CURRENT_TIMESTAMP(3),
    started_at = NULL, finished_at = NULL, updated_at = CURRENT_TIMESTAMP(3)
WHERE status = 'uncertain' AND last_safe_error = 'proto_project_history_todo';

-- +goose Down

-- Keep durable session keys during an image rollback. Operators must explicitly
-- invalidate every Proto session before removing its persistence table.
DROP TEMPORARY TABLE IF EXISTS proto_sessions_down_guard;
CREATE TEMPORARY TABLE proto_sessions_down_guard (
    unsafe_rows BIGINT NOT NULL,
    CONSTRAINT chk_proto_sessions_down_guard CHECK (unsafe_rows = 0)
);
INSERT INTO proto_sessions_down_guard SELECT COUNT(*) FROM proto_sessions;
DROP TEMPORARY TABLE proto_sessions_down_guard;
DROP TABLE proto_sessions;
