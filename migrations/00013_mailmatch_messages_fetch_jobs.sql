-- +goose Up

CREATE TABLE mailmatch_messages (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    email_resource_id BIGINT UNSIGNED NOT NULL,
    resource_type VARCHAR(32) NOT NULL COMMENT 'microsoft|domain',
    recipient VARCHAR(255) NOT NULL,
    recipients_json JSON NULL,
    sender VARCHAR(255) NOT NULL DEFAULT '',
    subject VARCHAR(500) NOT NULL DEFAULT '',
    raw_body MEDIUMTEXT NULL,
    raw_source MEDIUMTEXT NULL,
    provider_payload JSON NULL,
    body_preview VARCHAR(1000) NOT NULL DEFAULT '',
    verification_code VARCHAR(64) NOT NULL DEFAULT '',
    message_id_header VARCHAR(500) NOT NULL DEFAULT '',
    provider_message_id VARCHAR(500) NOT NULL DEFAULT '',
    dedupe_key CHAR(64) NOT NULL,
    protocol VARCHAR(32) NOT NULL DEFAULT '',
    folder VARCHAR(64) NOT NULL DEFAULT '',
    status VARCHAR(32) NOT NULL DEFAULT 'received',
    match_diagnostic VARCHAR(500) NOT NULL DEFAULT '',
    received_at DATETIME NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    UNIQUE INDEX idx_mailmatch_messages_resource_dedupe (email_resource_id, dedupe_key),
    INDEX idx_mailmatch_messages_resource_type_time (email_resource_id, resource_type, received_at, id),
    INDEX idx_mailmatch_messages_scope_window (email_resource_id, recipient, received_at, id),
    INDEX idx_mailmatch_messages_recipient_status_time (recipient, status, received_at, id),
    INDEX idx_mailmatch_messages_status_time (status, received_at, id),
    CONSTRAINT fk_mailmatch_messages_resource FOREIGN KEY (email_resource_id, resource_type) REFERENCES email_resources(id, type) ON DELETE RESTRICT,
    CONSTRAINT chk_mailmatch_messages_resource_type CHECK (resource_type IN ('microsoft', 'domain')),
    CONSTRAINT chk_mailmatch_messages_status CHECK (status IN ('received', 'matched', 'ignored')),
    CONSTRAINT chk_mailmatch_messages_required CHECK (recipient <> '' AND dedupe_key <> '')
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE mailmatch_fetch_jobs (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    order_no VARCHAR(64) NOT NULL,
    purpose VARCHAR(32) NOT NULL DEFAULT 'order_fetch' COMMENT 'order_fetch|manual_fetch|auto_refresh|aftersale_check|inbound_consume',
    allocation_type VARCHAR(32) NOT NULL COMMENT 'microsoft|domain',
    allocation_id BIGINT UNSIGNED NOT NULL,
    project_id BIGINT UNSIGNED NOT NULL,
    email_resource_id BIGINT UNSIGNED NOT NULL,
    recipient VARCHAR(255) NOT NULL,
    status VARCHAR(32) NOT NULL DEFAULT 'pending',
    attempts INT NOT NULL DEFAULT 0,
    max_attempts INT NOT NULL DEFAULT 3,
    since_at DATETIME NULL,
    until_at DATETIME NULL,
    fetched_count INT NOT NULL DEFAULT 0,
    stored_count INT NOT NULL DEFAULT 0,
    matched_count INT NOT NULL DEFAULT 0,
    last_safe_error VARCHAR(500) NOT NULL DEFAULT '',
    request_id VARCHAR(64) NOT NULL DEFAULT '',
    started_at DATETIME NULL,
    finished_at DATETIME NULL,
    active_order_no VARCHAR(64) GENERATED ALWAYS AS (
        CASE WHEN status IN ('pending', 'queued', 'running') THEN order_no ELSE NULL END
    ) STORED,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    UNIQUE INDEX idx_mailmatch_fetch_jobs_active_order (active_order_no),
    INDEX idx_mailmatch_fetch_jobs_status_updated (status, updated_at, id),
    INDEX idx_mailmatch_fetch_jobs_order_created (order_no, created_at, id),
    INDEX idx_mailmatch_fetch_jobs_resource_created (email_resource_id, created_at, id),
    CONSTRAINT chk_mailmatch_fetch_jobs_purpose CHECK (purpose IN ('order_fetch', 'manual_fetch', 'auto_refresh', 'aftersale_check', 'inbound_consume')),
    CONSTRAINT chk_mailmatch_fetch_jobs_allocation_type CHECK (allocation_type IN ('microsoft', 'domain')),
    CONSTRAINT chk_mailmatch_fetch_jobs_status CHECK (status IN ('pending', 'queued', 'running', 'succeeded', 'failed', 'skipped')),
    CONSTRAINT chk_mailmatch_fetch_jobs_attempts CHECK (attempts >= 0 AND max_attempts > 0 AND attempts <= max_attempts),
    CONSTRAINT chk_mailmatch_fetch_jobs_counts CHECK (fetched_count >= 0 AND stored_count >= 0 AND matched_count >= 0),
    CONSTRAINT chk_mailmatch_fetch_jobs_required CHECK (order_no <> '' AND allocation_id > 0 AND recipient <> '')
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

ALTER TABLE mailmatch_fetch_jobs
    ADD CONSTRAINT fk_mailmatch_fetch_jobs_order FOREIGN KEY (order_no) REFERENCES orders(order_no) ON DELETE RESTRICT;

ALTER TABLE mailmatch_fetch_jobs
    ADD CONSTRAINT fk_mailmatch_fetch_jobs_project FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE RESTRICT;

ALTER TABLE mailmatch_fetch_jobs
    ADD CONSTRAINT fk_mailmatch_fetch_jobs_resource FOREIGN KEY (email_resource_id, allocation_type) REFERENCES email_resources(id, type) ON DELETE RESTRICT;

CREATE TABLE mailmatch_order_fetch_states (
    order_no VARCHAR(64) PRIMARY KEY,
    last_job_id BIGINT UNSIGNED NULL,
    last_status VARCHAR(32) NOT NULL DEFAULT '',
    last_submitted_at DATETIME NULL,
    last_success_at DATETIME NULL,
    last_received_at DATETIME NULL,
    cooldown_until DATETIME NULL,
    last_safe_error VARCHAR(500) NOT NULL DEFAULT '',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    INDEX idx_mailmatch_fetch_states_cooldown (cooldown_until),
    CONSTRAINT fk_mailmatch_fetch_states_order FOREIGN KEY (order_no) REFERENCES orders(order_no) ON DELETE CASCADE,
    CONSTRAINT fk_mailmatch_fetch_states_job FOREIGN KEY (last_job_id) REFERENCES mailmatch_fetch_jobs(id) ON DELETE SET NULL,
    CONSTRAINT chk_mailmatch_fetch_states_status CHECK (last_status IN ('', 'pending', 'queued', 'running', 'succeeded', 'failed', 'skipped', 'cooldown'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

INSERT INTO casbin_rule (ptype, v0, v1, v2, v3) VALUES
    ('p', 'role:admin', 'mailmatch:message', 'read', 'allow'),
    ('p', 'role:admin', 'mailmatch:message', 'operate', 'allow'),
    ('p', 'role:super_admin', 'mailmatch:message', 'read', 'allow'),
    ('p', 'role:super_admin', 'mailmatch:message', 'operate', 'allow');

-- +goose Down

DELETE FROM casbin_rule WHERE v1 = 'mailmatch:message';
DROP TABLE IF EXISTS mailmatch_order_fetch_states;
DROP TABLE IF EXISTS mailmatch_fetch_jobs;
DROP TABLE IF EXISTS mailmatch_messages;
