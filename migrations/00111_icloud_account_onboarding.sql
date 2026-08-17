-- +goose Up

ALTER TABLE kitesim_phones
    ADD COLUMN sms_cooldown_until DATETIME(3) NULL AFTER deleted_at,
    ADD COLUMN sms_cooldown_stage TINYINT UNSIGNED NOT NULL DEFAULT 0 AFTER sms_cooldown_until,
    ADD COLUMN sms_consecutive_failures INT UNSIGNED NOT NULL DEFAULT 0 AFTER sms_cooldown_stage,
    ADD COLUMN sms_blacklisted_until DATETIME(3) NULL AFTER sms_consecutive_failures,
    ADD COLUMN sms_last_used_at DATETIME(3) NULL AFTER sms_blacklisted_until,
    ADD INDEX idx_kitesim_phones_sms_available
        (deleted_at, disabled_at, status, sms_blacklisted_until, sms_cooldown_until, id);

CREATE TABLE kitesim_phone_bindings (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    phone_id BIGINT UNSIGNED NOT NULL,
    consumer_type VARCHAR(32) NOT NULL,
    consumer_key VARCHAR(320) NOT NULL,
    source VARCHAR(16) NOT NULL COMMENT 'automatic|matched',
    created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),

    UNIQUE INDEX uk_kitesim_phone_bindings_consumer (consumer_type, consumer_key),
    INDEX idx_kitesim_phone_bindings_phone (phone_id, created_at, id),
    CONSTRAINT fk_kitesim_phone_bindings_phone
        FOREIGN KEY (phone_id) REFERENCES kitesim_phones(id) ON DELETE RESTRICT,
    CONSTRAINT chk_kitesim_phone_bindings_required
        CHECK (consumer_type <> '' AND consumer_key <> ''),
    CONSTRAINT chk_kitesim_phone_bindings_source
        CHECK (source IN ('automatic', 'matched'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE kitesim_phone_usage_events (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    phone_id BIGINT UNSIGNED NOT NULL,
    purpose VARCHAR(64) NOT NULL,
    result VARCHAR(32) NOT NULL DEFAULT 'reserved'
        COMMENT 'reserved|sent|send_failed|infrastructure_failed',
    created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    resolved_at DATETIME(3) NULL,

    INDEX idx_kitesim_phone_usage_window (phone_id, created_at, id),
    CONSTRAINT fk_kitesim_phone_usage_phone
        FOREIGN KEY (phone_id) REFERENCES kitesim_phones(id) ON DELETE RESTRICT,
    CONSTRAINT chk_kitesim_phone_usage_purpose CHECK (purpose <> ''),
    CONSTRAINT chk_kitesim_phone_usage_result
        CHECK (result IN ('reserved', 'sent', 'send_failed', 'infrastructure_failed'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

ALTER TABLE icloud_resources
    ADD COLUMN account_role VARCHAR(16) NOT NULL DEFAULT 'unknown' AFTER primary_email,
    ADD COLUMN family_primary_resource_id BIGINT UNSIGNED NULL AFTER account_role,
    ADD COLUMN region VARCHAR(64) NOT NULL DEFAULT '' AFTER family_primary_resource_id,
    ADD COLUMN country_code VARCHAR(16) NOT NULL DEFAULT '' AFTER region,
    ADD COLUMN icloud_opened TINYINT(1) NOT NULL DEFAULT 0 AFTER country_code,
    ADD COLUMN bound_phone_number VARCHAR(32) NOT NULL DEFAULT '' AFTER icloud_opened,
    ADD COLUMN bound_phone_country_code VARCHAR(16) NOT NULL DEFAULT '' AFTER bound_phone_number,
    ADD COLUMN bound_phone_source VARCHAR(16) NOT NULL DEFAULT '' AFTER bound_phone_country_code,
    ADD COLUMN kitesim_phone_id BIGINT UNSIGNED NULL AFTER bound_phone_source,
    ADD COLUMN family_invite_url VARCHAR(2048) NOT NULL DEFAULT '' AFTER kitesim_phone_id,
    ADD INDEX idx_icloud_resources_family (account_role, region, id),
    ADD INDEX idx_icloud_resources_phone (kitesim_phone_id, id),
    ADD CONSTRAINT fk_icloud_resources_kitesim_phone
        FOREIGN KEY (kitesim_phone_id) REFERENCES kitesim_phones(id) ON DELETE RESTRICT,
    ADD CONSTRAINT fk_icloud_resources_family_primary
        FOREIGN KEY (family_primary_resource_id) REFERENCES icloud_resources(id) ON DELETE RESTRICT,
    ADD CONSTRAINT chk_icloud_resources_account_role
        CHECK (account_role IN ('unknown', 'primary', 'child')),
    ADD CONSTRAINT chk_icloud_resources_phone_source
        CHECK (bound_phone_source IN ('', 'kitesim', 'manual'));

CREATE TABLE icloud_resource_credentials (
    resource_id BIGINT UNSIGNED PRIMARY KEY,
    apple_password TEXT NOT NULL
        COMMENT 'write-only Apple password; never returned by APIs or written to logs',
    security_answers JSON NOT NULL
        COMMENT 'write-only security questions and answers; never returned by APIs or written to logs',
    birthday DATE NOT NULL,
    created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3)
        ON UPDATE CURRENT_TIMESTAMP(3),

    CONSTRAINT fk_icloud_resource_credentials_resource
        FOREIGN KEY (resource_id) REFERENCES icloud_resources(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE icloud_account_onboarding_imports (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    owner_user_id BIGINT UNSIGNED NOT NULL,
    operator_user_id BIGINT UNSIGNED NOT NULL,
    status VARCHAR(16) NOT NULL DEFAULT 'processing'
        COMMENT 'processing|completed|partial|failed',
    accepted_count INT UNSIGNED NOT NULL DEFAULT 0,
    completed_count INT UNSIGNED NOT NULL DEFAULT 0,
    failed_count INT UNSIGNED NOT NULL DEFAULT 0,
    waiting_count INT UNSIGNED NOT NULL DEFAULT 0,
    resource_expire_at DATETIME(3) NOT NULL,
    last_safe_error VARCHAR(500) NOT NULL DEFAULT '',
    request_id VARCHAR(64) NOT NULL DEFAULT '',
    path VARCHAR(255) NOT NULL DEFAULT '',
    idempotency_key VARCHAR(128) NOT NULL,
    request_fingerprint CHAR(64) NOT NULL,
    created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3)
        ON UPDATE CURRENT_TIMESTAMP(3),

    UNIQUE INDEX uk_icloud_onboard_import_idempotency (operator_user_id, idempotency_key),
    INDEX idx_icloud_onboard_import_owner (owner_user_id, created_at, id),
    CONSTRAINT fk_icloud_onboard_import_owner
        FOREIGN KEY (owner_user_id) REFERENCES users(id) ON DELETE RESTRICT,
    CONSTRAINT fk_icloud_onboard_import_operator
        FOREIGN KEY (operator_user_id) REFERENCES users(id) ON DELETE RESTRICT,
    CONSTRAINT chk_icloud_onboard_import_status
        CHECK (status IN ('processing', 'completed', 'partial', 'failed')),
    CONSTRAINT chk_icloud_onboard_import_idempotency
        CHECK (idempotency_key <> '' AND request_fingerprint <> '')
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE icloud_account_onboarding_tasks (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    import_id BIGINT UNSIGNED NULL,
    resource_id BIGINT UNSIGNED NULL,
    task_kind VARCHAR(16) NOT NULL DEFAULT 'onboarding'
        COMMENT 'onboarding|refresh',
    line_number INT UNSIGNED NOT NULL,
    primary_email VARCHAR(320) NOT NULL,
    account_role VARCHAR(16) NOT NULL,
    family_primary_resource_id BIGINT UNSIGNED NULL,
    region VARCHAR(64) NOT NULL,
    country_code VARCHAR(16) NOT NULL DEFAULT '',
    icloud_opened TINYINT(1) NOT NULL,
    family_invite_url VARCHAR(2048) NOT NULL DEFAULT '',
    bound_phone_number VARCHAR(32) NOT NULL DEFAULT '',
    bound_phone_country_code VARCHAR(16) NOT NULL DEFAULT '',
    bound_phone_source VARCHAR(16) NOT NULL DEFAULT '',
    kitesim_phone_id BIGINT UNSIGNED NULL,
    secret_payload JSON NULL
        COMMENT 'temporary write-only Apple credentials; cleared after terminal completion',
    session_payload JSON NULL
        COMMENT 'temporary write-only Apple browser session; cleared after terminal completion',
    manual_verification_code VARCHAR(64) NOT NULL DEFAULT ''
        COMMENT 'temporary write-only SMS code; cleared after one verification attempt',
    pending_sms_purpose VARCHAR(64) NOT NULL DEFAULT '',
    sms_sent_at DATETIME(3) NULL,
    sms_poll_deadline DATETIME(3) NULL,
    forward_preparation_id BIGINT UNSIGNED NULL,
    status VARCHAR(16) NOT NULL DEFAULT 'processing'
        COMMENT 'processing|waiting|completed|failed',
    stage VARCHAR(48) NOT NULL DEFAULT 'accepted',
    dispatch_status VARCHAR(16) NOT NULL DEFAULT 'pending'
        COMMENT 'pending|queued|running|waiting|succeeded|failed',
    generation BIGINT UNSIGNED NOT NULL DEFAULT 1,
    expected_credential_revision BIGINT UNSIGNED NOT NULL DEFAULT 0,
    claim_token CHAR(36) NOT NULL DEFAULT '',
    attempts INT UNSIGNED NOT NULL DEFAULT 0,
    max_attempts INT UNSIGNED NOT NULL DEFAULT 5,
    stage_attempts INT UNSIGNED NOT NULL DEFAULT 0,
    next_attempt_at DATETIME(3) NULL,
    last_error_category VARCHAR(64) NOT NULL DEFAULT '',
    last_safe_error VARCHAR(500) NOT NULL DEFAULT '',
    started_at DATETIME(3) NULL,
    finished_at DATETIME(3) NULL,
    created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3)
        ON UPDATE CURRENT_TIMESTAMP(3),
    active_email VARCHAR(320) GENERATED ALWAYS AS (
        CASE WHEN status IN ('processing', 'waiting') THEN primary_email ELSE NULL END
    ) STORED,
    active_refresh_resource_id BIGINT UNSIGNED GENERATED ALWAYS AS (
        CASE WHEN task_kind = 'refresh' AND status IN ('processing', 'waiting') THEN resource_id ELSE NULL END
    ) STORED,

    UNIQUE INDEX uk_icloud_onboard_task_line (import_id, line_number),
    UNIQUE INDEX uk_icloud_onboard_task_active_email (active_email),
    UNIQUE INDEX uk_icloud_onboard_task_active_refresh (active_refresh_resource_id),
    INDEX idx_icloud_onboard_task_dispatch
        (status, dispatch_status, next_attempt_at, generation, id),
    INDEX idx_icloud_onboard_task_phone (kitesim_phone_id, status, id),
    CONSTRAINT fk_icloud_onboard_task_import
        FOREIGN KEY (import_id) REFERENCES icloud_account_onboarding_imports(id) ON DELETE CASCADE,
    CONSTRAINT fk_icloud_onboard_task_resource
        FOREIGN KEY (resource_id) REFERENCES icloud_resources(id) ON DELETE RESTRICT,
    CONSTRAINT fk_icloud_onboard_task_family_primary
        FOREIGN KEY (family_primary_resource_id) REFERENCES icloud_resources(id) ON DELETE RESTRICT,
    CONSTRAINT fk_icloud_onboard_task_phone
        FOREIGN KEY (kitesim_phone_id) REFERENCES kitesim_phones(id) ON DELETE RESTRICT,
    CONSTRAINT fk_icloud_onboard_task_preparation
        FOREIGN KEY (forward_preparation_id) REFERENCES icloud_import_preparations(id) ON DELETE RESTRICT,
    CONSTRAINT chk_icloud_onboard_task_role
        CHECK (account_role IN ('primary', 'child')),
    CONSTRAINT chk_icloud_onboard_task_kind
        CHECK (task_kind IN ('onboarding', 'refresh')),
    CONSTRAINT chk_icloud_onboard_task_phone_source
        CHECK (bound_phone_source IN ('', 'kitesim', 'manual')),
    CONSTRAINT chk_icloud_onboard_task_status
        CHECK (status IN ('processing', 'waiting', 'completed', 'failed')),
    CONSTRAINT chk_icloud_onboard_task_dispatch
        CHECK (dispatch_status IN ('pending', 'queued', 'running', 'waiting', 'succeeded', 'failed')),
    CONSTRAINT chk_icloud_onboard_task_attempts
        CHECK (generation > 0 AND attempts <= max_attempts AND max_attempts > 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- +goose Down

DROP TABLE icloud_account_onboarding_tasks;
DROP TABLE icloud_account_onboarding_imports;
DROP TABLE icloud_resource_credentials;

ALTER TABLE icloud_resources
    DROP FOREIGN KEY fk_icloud_resources_kitesim_phone,
    DROP FOREIGN KEY fk_icloud_resources_family_primary,
    DROP CHECK chk_icloud_resources_account_role,
    DROP CHECK chk_icloud_resources_phone_source,
    DROP INDEX idx_icloud_resources_family,
    DROP INDEX idx_icloud_resources_phone,
    DROP COLUMN family_invite_url,
    DROP COLUMN kitesim_phone_id,
    DROP COLUMN bound_phone_source,
    DROP COLUMN bound_phone_country_code,
    DROP COLUMN bound_phone_number,
    DROP COLUMN icloud_opened,
    DROP COLUMN country_code,
    DROP COLUMN region,
    DROP COLUMN family_primary_resource_id,
    DROP COLUMN account_role;

DROP TABLE kitesim_phone_usage_events;
DROP TABLE kitesim_phone_bindings;

ALTER TABLE kitesim_phones
    DROP INDEX idx_kitesim_phones_sms_available,
    DROP COLUMN sms_last_used_at,
    DROP COLUMN sms_blacklisted_until,
    DROP COLUMN sms_consecutive_failures,
    DROP COLUMN sms_cooldown_stage,
    DROP COLUMN sms_cooldown_until;
