-- +goose Up

-- The root version is the optimistic concurrency boundary for all administrator
-- edits and explicit state commands. It is independent from MySQL timestamp
-- precision and therefore supports deterministic lost-update protection.
ALTER TABLE email_resources
    ADD COLUMN version BIGINT UNSIGNED NOT NULL DEFAULT 1 AFTER owner_user_id;

-- Administrator Microsoft resource management keeps credentials write-only,
-- but needs a monotonic revision and a timestamp that only changes when the
-- credential set changes. Existing rows are revision 1 because every current
-- Microsoft resource has a password value.
ALTER TABLE microsoft_resources
    ADD COLUMN credential_revision BIGINT UNSIGNED NOT NULL DEFAULT 1 AFTER refresh_token,
    ADD COLUMN credential_updated_at DATETIME(3) NULL AFTER credential_revision,
    ADD COLUMN token_last_refreshed_at DATETIME(3) NULL AFTER rt_expire_at,
    ADD COLUMN token_last_request_id VARCHAR(64) NOT NULL DEFAULT '' AFTER token_last_refreshed_at;

UPDATE microsoft_resources
SET credential_updated_at = updated_at
WHERE credential_updated_at IS NULL;

ALTER TABLE microsoft_resources
    MODIFY COLUMN credential_updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3);

UPDATE microsoft_resources
SET quality_score = LEAST(100, GREATEST(0, quality_score));

ALTER TABLE microsoft_resources
    ADD CONSTRAINT chk_microsoft_quality_score CHECK (quality_score BETWEEN 0 AND 100);

-- Resource imports become an administrator-owned durable command while the
-- selected owner remains the owner of imported resources. The generated key
-- permits multiple requests without an Idempotency-Key and enforces exact
-- replay when a key is supplied.
ALTER TABLE resource_imports
    ADD COLUMN operator_user_id BIGINT UNSIGNED NULL AFTER owner_user_id,
    ADD COLUMN long_lived TINYINT(1) NOT NULL DEFAULT 0 AFTER resource_type,
    ADD COLUMN error_strategy VARCHAR(16) NOT NULL DEFAULT 'skip' AFTER long_lived,
    ADD COLUMN accepted_count INT UNSIGNED NOT NULL DEFAULT 0 AFTER imported_count,
    ADD COLUMN skipped_count INT UNSIGNED NOT NULL DEFAULT 0 AFTER accepted_count,
    ADD COLUMN request_id VARCHAR(64) NOT NULL DEFAULT '' AFTER last_safe_error,
    ADD COLUMN path VARCHAR(255) NOT NULL DEFAULT '' AFTER request_id,
    ADD COLUMN idempotency_key VARCHAR(128) NOT NULL DEFAULT '' AFTER path,
    ADD COLUMN request_fingerprint CHAR(64) NOT NULL DEFAULT '' AFTER idempotency_key,
    ADD COLUMN dispatch_status VARCHAR(32) NOT NULL DEFAULT 'legacy' AFTER request_fingerprint,
    ADD COLUMN attempts INT UNSIGNED NOT NULL DEFAULT 0 AFTER dispatch_status,
    ADD COLUMN max_attempts INT UNSIGNED NOT NULL DEFAULT 3 AFTER attempts,
    ADD COLUMN claim_token CHAR(36) NOT NULL DEFAULT '' AFTER max_attempts,
    ADD COLUMN dispatch_token CHAR(36) NOT NULL DEFAULT '' AFTER claim_token,
    ADD COLUMN dispatched_at DATETIME(3) NULL AFTER dispatch_token,
    ADD COLUMN started_at DATETIME(3) NULL AFTER dispatched_at,
    ADD COLUMN finished_at DATETIME(3) NULL AFTER started_at,
    ADD COLUMN idempotency_scope VARCHAR(320) GENERATED ALWAYS AS (
        CASE
            WHEN idempotency_key = '' THEN NULL
            ELSE CONCAT(operator_user_id, ':', idempotency_key)
        END
    ) STORED AFTER request_fingerprint;

UPDATE resource_imports
SET operator_user_id = owner_user_id
WHERE operator_user_id IS NULL;

ALTER TABLE resource_imports
    ADD UNIQUE INDEX idx_resource_imports_idempotency_scope (idempotency_scope),
    ADD INDEX idx_resource_imports_dispatch (dispatch_status, dispatched_at, id),
    ADD CONSTRAINT fk_resource_imports_operator
        FOREIGN KEY (operator_user_id) REFERENCES users(id) ON DELETE RESTRICT,
    ADD CONSTRAINT chk_resource_imports_error_strategy
        CHECK (error_strategy IN ('skip', 'abort')),
    ADD CONSTRAINT chk_resource_imports_dispatch_status
        CHECK (dispatch_status IN ('legacy', 'queued', 'running', 'succeeded', 'failed')),
    ADD CONSTRAINT chk_resource_imports_attempts
        CHECK (attempts <= max_attempts AND max_attempts > 0);

CREATE TABLE resource_import_items (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    import_id BIGINT UNSIGNED NOT NULL,
    resource_id BIGINT UNSIGNED NULL,
    line_number INT UNSIGNED NOT NULL,
    outcome VARCHAR(32) NOT NULL COMMENT 'imported|restored|skipped',
    category VARCHAR(64) NOT NULL DEFAULT '',
    last_safe_error VARCHAR(500) NOT NULL DEFAULT '',
    created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    UNIQUE INDEX idx_resource_import_items_line (import_id, line_number),
    INDEX idx_resource_import_items_resource (resource_id, import_id),
    CONSTRAINT fk_resource_import_items_import
        FOREIGN KEY (import_id) REFERENCES resource_imports(id) ON DELETE CASCADE,
    CONSTRAINT fk_resource_import_items_resource
        FOREIGN KEY (resource_id) REFERENCES email_resources(id) ON DELETE SET NULL,
    CONSTRAINT chk_resource_import_items_line CHECK (line_number > 0),
    CONSTRAINT chk_resource_import_items_outcome
        CHECK (outcome IN ('imported', 'restored', 'skipped'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- Validation and inbound owner_user_id are historical snapshots. They must
-- continue to reference a real resource and user, but must not be forced to
-- equal the resource's current owner after an administrator transfer.
--
-- A safe Down keeps the split foreign keys instead of recreating the old
-- owner-equality constraint. Normalize either shape here so Down followed by
-- Up is repeatable without rewriting valid history rows.
SET @amr_fk_sql = IF(
    EXISTS (
        SELECT 1 FROM information_schema.referential_constraints
        WHERE constraint_schema = DATABASE()
          AND table_name = 'resource_validation_jobs'
          AND constraint_name = 'fk_resource_validation_jobs_resource_owner'
    ),
    'ALTER TABLE resource_validation_jobs DROP FOREIGN KEY fk_resource_validation_jobs_resource_owner',
    'ALTER TABLE resource_validation_jobs DROP FOREIGN KEY fk_resource_validation_jobs_resource, DROP FOREIGN KEY fk_resource_validation_jobs_owner'
);
PREPARE amr_fk_stmt FROM @amr_fk_sql;
EXECUTE amr_fk_stmt;
DEALLOCATE PREPARE amr_fk_stmt;

ALTER TABLE resource_validation_jobs
    ADD COLUMN expected_credential_revision BIGINT UNSIGNED NOT NULL DEFAULT 0 AFTER owner_user_id,
    ADD CONSTRAINT fk_resource_validation_jobs_resource
        FOREIGN KEY (resource_id, resource_type) REFERENCES email_resources(id, type) ON DELETE RESTRICT,
    ADD CONSTRAINT fk_resource_validation_jobs_owner
        FOREIGN KEY (owner_user_id) REFERENCES users(id) ON DELETE RESTRICT;

SET @amr_fk_sql = IF(
    EXISTS (
        SELECT 1 FROM information_schema.referential_constraints
        WHERE constraint_schema = DATABASE()
          AND table_name = 'inbound_mails'
          AND constraint_name = 'fk_inbound_mails_resource_owner'
    ),
    'ALTER TABLE inbound_mails DROP FOREIGN KEY fk_inbound_mails_resource_owner',
    'ALTER TABLE inbound_mails DROP FOREIGN KEY fk_inbound_mails_resource, DROP FOREIGN KEY fk_inbound_mails_owner'
);
PREPARE amr_fk_stmt FROM @amr_fk_sql;
EXECUTE amr_fk_stmt;
DEALLOCATE PREPARE amr_fk_stmt;

ALTER TABLE inbound_mails
    ADD COLUMN header_from VARCHAR(320) NOT NULL DEFAULT '' AFTER envelope_from,
    ADD COLUMN subject VARCHAR(500) NOT NULL DEFAULT '' AFTER recipient,
    ADD COLUMN body_preview VARCHAR(1000) NOT NULL DEFAULT '' AFTER subject,
    ADD COLUMN verification_code VARCHAR(64) NOT NULL DEFAULT '' AFTER body_preview,
    ADD COLUMN message_id_header VARCHAR(500) NOT NULL DEFAULT '' AFTER verification_code,
    ADD COLUMN received_at DATETIME(3) NULL AFTER message_id_header,
    ADD COLUMN parsed_at DATETIME(3) NULL AFTER received_at,
    ADD CONSTRAINT fk_inbound_mails_resource
        FOREIGN KEY (resource_id, resource_type) REFERENCES email_resources(id, type) ON DELETE RESTRICT,
    ADD CONSTRAINT fk_inbound_mails_owner
        FOREIGN KEY (owner_user_id) REFERENCES users(id) ON DELETE RESTRICT;

-- A binding is the current fact. Equality with the Core owner is maintained by
-- the same short application transaction; separate FKs allow both rows to be
-- updated atomically without an impossible intermediate composite-FK state.
SET @amr_fk_sql = IF(
    EXISTS (
        SELECT 1 FROM information_schema.referential_constraints
        WHERE constraint_schema = DATABASE()
          AND table_name = 'microsoft_binding_mailboxes'
          AND constraint_name = 'fk_microsoft_binding_resource_owner'
    ),
    'ALTER TABLE microsoft_binding_mailboxes DROP FOREIGN KEY fk_microsoft_binding_resource_owner',
    'DO 0'
);
PREPARE amr_fk_stmt FROM @amr_fk_sql;
EXECUTE amr_fk_stmt;
DEALLOCATE PREPARE amr_fk_stmt;

-- Explicit, dot and plus alias tabs page by resource and stable ID.
ALTER TABLE explicit_aliases
    ADD INDEX idx_explicit_aliases_resource_created_id (resource_id, created_at, id);

ALTER TABLE dot_aliases
    ADD INDEX idx_dot_aliases_resource_created_id (resource_id, created_at, id);

ALTER TABLE plus_aliases
    ADD INDEX idx_plus_aliases_resource_created_id (resource_id, created_at, id);

-- Existing mailmatch_fetch_jobs remain strictly order-scoped so old workers
-- can ignore the new feature. Administrator manual fetches use a separate
-- resource-scoped durable fact and never synthesize an Order/Allocation.
CREATE TABLE mailmatch_resource_fetch_jobs (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    resource_id BIGINT UNSIGNED NOT NULL,
    operator_user_id BIGINT UNSIGNED NOT NULL,
    expected_credential_revision BIGINT UNSIGNED NOT NULL,
    recipient VARCHAR(255) NOT NULL,
    status VARCHAR(32) NOT NULL DEFAULT 'queued' COMMENT 'queued|running|succeeded|failed|canceled',
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
    CONSTRAINT fk_mailmatch_resource_fetch_resource
        FOREIGN KEY (resource_id) REFERENCES microsoft_resources(id) ON DELETE RESTRICT,
    CONSTRAINT fk_mailmatch_resource_fetch_operator
        FOREIGN KEY (operator_user_id) REFERENCES users(id) ON DELETE RESTRICT,
    CONSTRAINT chk_mailmatch_resource_fetch_status
        CHECK (status IN ('queued', 'running', 'succeeded', 'failed', 'canceled')),
    CONSTRAINT chk_mailmatch_resource_fetch_attempts
        CHECK (attempts <= max_attempts AND max_attempts > 0),
    CONSTRAINT chk_mailmatch_resource_fetch_counts
        CHECK (stored_count <= fetched_count AND matched_count <= stored_count),
    CONSTRAINT chk_mailmatch_resource_fetch_recipient CHECK (recipient <> '')
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- Every accepted administrator request keeps a durable idempotency receipt,
-- including requests that reused an already-active resource job. This lets the
-- same key replay the exact task after it becomes terminal without weakening
-- the resource-level single-flight invariant.
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
    CONSTRAINT fk_mailmatch_resource_fetch_request_operator
        FOREIGN KEY (operator_user_id) REFERENCES users(id) ON DELETE RESTRICT,
    CONSTRAINT fk_mailmatch_resource_fetch_request_resource
        FOREIGN KEY (resource_id) REFERENCES microsoft_resources(id) ON DELETE RESTRICT,
    CONSTRAINT fk_mailmatch_resource_fetch_request_job
        FOREIGN KEY (job_id) REFERENCES mailmatch_resource_fetch_jobs(id) ON DELETE RESTRICT,
    CONSTRAINT chk_mailmatch_resource_fetch_request_key CHECK (idempotency_key <> '')
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- Refresh-token is a dedicated durable task. Microsoft/Graph/IMAP calls run in
-- workers; the HTTP transaction only creates or reuses this fact.
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

-- Idempotency receipts outlive a terminal refresh job so retries with the same
-- administrator key replay the exact durable task instead of creating another.
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

-- Alias expedite is a command receipt, not a second task fact. The canonical
-- schedule remains the only durable alias scheduler and all quota/fencing facts
-- remain in the existing schedule/attempt tables.
CREATE TABLE microsoft_alias_expedite_requests (
    operator_user_id BIGINT UNSIGNED NOT NULL,
    idempotency_key VARCHAR(128) NOT NULL,
    resource_id BIGINT UNSIGNED NOT NULL,
    reused BOOLEAN NOT NULL DEFAULT FALSE,
    created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    PRIMARY KEY (operator_user_id, idempotency_key),
    INDEX idx_microsoft_alias_expedite_resource (resource_id),
    CONSTRAINT fk_microsoft_alias_expedite_operator
        FOREIGN KEY (operator_user_id) REFERENCES users(id) ON DELETE RESTRICT,
    CONSTRAINT fk_microsoft_alias_expedite_resource
        FOREIGN KEY (resource_id) REFERENCES microsoft_resources(id) ON DELETE RESTRICT,
    CONSTRAINT chk_microsoft_alias_expedite_key CHECK (idempotency_key <> '')
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- Filter-mode publish/unpublish/delete and all validation batches use a
-- high-water mark and durable checkpoint. ids-mode commands may also record a
-- completed command here for idempotent replay without writing per-resource
-- operation logs.
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

INSERT INTO casbin_rule (ptype, v0, v1, v2, v3)
SELECT 'p', role_name, resource_name, action_name, 'allow'
FROM (
    SELECT 'role:admin' AS role_name
    UNION ALL SELECT 'role:super_admin'
) AS roles
CROSS JOIN (
    SELECT 'mailtransport:binding' AS resource_name, 'read' AS action_name
    UNION ALL SELECT 'mailtransport:binding', 'write'
    UNION ALL SELECT 'governance:task', 'read'
) AS permissions
WHERE NOT EXISTS (
    SELECT 1
    FROM casbin_rule existing
    WHERE existing.ptype = 'p'
      AND existing.v0 = roles.role_name
      AND existing.v1 = permissions.resource_name
      AND existing.v2 = permissions.action_name
      AND existing.v3 = 'allow'
);

-- +goose Down

DELETE FROM casbin_rule
WHERE ptype = 'p'
  AND v0 IN ('role:admin', 'role:super_admin')
  AND (
      (v1 = 'mailtransport:binding' AND v2 IN ('read', 'write'))
      OR (v1 = 'governance:task' AND v2 = 'read')
  );

DROP TABLE IF EXISTS admin_resource_bulk_commands;
DROP TABLE IF EXISTS microsoft_alias_expedite_requests;
DROP TABLE IF EXISTS microsoft_token_refresh_requests;
DROP TABLE IF EXISTS microsoft_token_refresh_jobs;
DROP TABLE IF EXISTS mailmatch_resource_fetch_requests;
DROP TABLE IF EXISTS mailmatch_resource_fetch_jobs;

ALTER TABLE plus_aliases
    DROP INDEX idx_plus_aliases_resource_created_id;

ALTER TABLE dot_aliases
    DROP INDEX idx_dot_aliases_resource_created_id;

ALTER TABLE explicit_aliases
    DROP INDEX idx_explicit_aliases_resource_created_id;

-- Do not recreate the legacy owner-equality foreign keys here. A resource may
-- have been transferred while validation and inbound owner_user_id values stay
-- as historical snapshots. The split resource/user foreign keys remain valid
-- for the old application image and preserve those facts without data repair.
ALTER TABLE inbound_mails
    DROP COLUMN parsed_at,
    DROP COLUMN received_at,
    DROP COLUMN message_id_header,
    DROP COLUMN verification_code,
    DROP COLUMN body_preview,
    DROP COLUMN subject,
    DROP COLUMN header_from;

ALTER TABLE resource_validation_jobs
    DROP COLUMN expected_credential_revision;

DROP TABLE IF EXISTS resource_import_items;

ALTER TABLE resource_imports
    DROP FOREIGN KEY fk_resource_imports_operator,
    DROP CHECK chk_resource_imports_error_strategy,
    DROP CHECK chk_resource_imports_attempts,
    DROP CHECK chk_resource_imports_dispatch_status,
    DROP INDEX idx_resource_imports_idempotency_scope,
    DROP INDEX idx_resource_imports_dispatch,
    DROP COLUMN idempotency_scope,
    DROP COLUMN finished_at,
    DROP COLUMN started_at,
    DROP COLUMN dispatched_at,
    DROP COLUMN dispatch_token,
    DROP COLUMN claim_token,
    DROP COLUMN max_attempts,
    DROP COLUMN attempts,
    DROP COLUMN dispatch_status,
    DROP COLUMN request_fingerprint,
    DROP COLUMN idempotency_key,
    DROP COLUMN path,
    DROP COLUMN request_id,
    DROP COLUMN skipped_count,
    DROP COLUMN accepted_count,
    DROP COLUMN error_strategy,
    DROP COLUMN long_lived,
    DROP COLUMN operator_user_id;

ALTER TABLE microsoft_resources
    DROP CHECK chk_microsoft_quality_score,
    DROP COLUMN token_last_request_id,
    DROP COLUMN token_last_refreshed_at,
    DROP COLUMN credential_updated_at,
    DROP COLUMN credential_revision;

ALTER TABLE email_resources
    DROP COLUMN version;
