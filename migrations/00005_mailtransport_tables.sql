-- +goose Up

CREATE TABLE outbound_mails (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    idempotency_key CHAR(64) NOT NULL,
    request_hash CHAR(64) NOT NULL,
    purpose VARCHAR(64) NOT NULL COMMENT 'verification_code|system_notification|security_notice',
    sender VARCHAR(320) NOT NULL,
    recipient VARCHAR(320) NOT NULL,
    subject VARCHAR(500) NOT NULL,
    text_body MEDIUMTEXT NOT NULL,
    html_body MEDIUMTEXT NOT NULL,
    status VARCHAR(32) NOT NULL DEFAULT 'pending' COMMENT 'pending|sending|sent|failed',
    retries INT NOT NULL DEFAULT 0,
    failure_reason VARCHAR(500) NOT NULL DEFAULT '',
    sent_at DATETIME NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    UNIQUE INDEX idx_outbound_mails_idempotency_key (idempotency_key),
    INDEX idx_outbound_mails_status_created (status, created_at, id),
    INDEX idx_outbound_mails_status_updated (status, updated_at),
    CONSTRAINT chk_outbound_mails_purpose CHECK (purpose IN ('verification_code', 'system_notification', 'security_notice')),
    CONSTRAINT chk_outbound_mails_status CHECK (status IN ('pending', 'sending', 'sent', 'failed')),
    CONSTRAINT chk_outbound_mails_retries CHECK (retries >= 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE inbound_mails (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    envelope_from VARCHAR(320) NOT NULL,
    recipient VARCHAR(320) NOT NULL,
    resource_id BIGINT UNSIGNED NOT NULL,
    resource_type VARCHAR(32) NOT NULL COMMENT 'microsoft|domain',
    owner_user_id BIGINT UNSIGNED NOT NULL,
    source_object_key VARCHAR(500) NOT NULL COMMENT 'private MinIO object key for original RFC822 message',
    status VARCHAR(32) NOT NULL DEFAULT 'pending' COMMENT 'pending|processing|stored|failed',
    failure_reason VARCHAR(500) NOT NULL DEFAULT '' COMMENT 'sanitized diagnostic summary',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    INDEX idx_inbound_mails_status_created (status, created_at, id),
    INDEX idx_inbound_mails_status_updated (status, updated_at),
    INDEX idx_inbound_mails_resource_created (resource_type, resource_id, created_at),
    INDEX idx_inbound_mails_recipient_created (recipient, created_at),
    INDEX idx_inbound_mails_object_key (source_object_key),
    CONSTRAINT fk_inbound_mails_resource_owner FOREIGN KEY (resource_id, resource_type, owner_user_id) REFERENCES email_resources(id, type, owner_user_id) ON DELETE RESTRICT,
    CONSTRAINT chk_inbound_mails_resource_type CHECK (resource_type IN ('microsoft', 'domain')),
    CONSTRAINT chk_inbound_mails_status CHECK (status IN ('pending', 'processing', 'stored', 'failed'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE microsoft_binding_mailboxes (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    resource_id BIGINT UNSIGNED NOT NULL,
    resource_type VARCHAR(32) NOT NULL DEFAULT 'microsoft' COMMENT 'mirrors email_resources.type for DB-level traceability',
    owner_user_id BIGINT UNSIGNED NOT NULL,
    account_email VARCHAR(255) NOT NULL,
    binding_address VARCHAR(320) NOT NULL,
    purpose VARCHAR(64) NOT NULL DEFAULT 'validation',
    status VARCHAR(32) NOT NULL DEFAULT 'pending' COMMENT 'pending|code_sent|verified|timeout|failed|expired',
    active_binding_address VARCHAR(320) GENERATED ALWAYS AS (CASE WHEN status IN ('pending', 'code_sent', 'verified', 'timeout', 'failed') THEN binding_address ELSE NULL END) STORED,
    code_msg_id VARCHAR(255) NOT NULL DEFAULT '',
    bound_display VARCHAR(255) NOT NULL DEFAULT '',
    category VARCHAR(64) NOT NULL DEFAULT '',
    last_safe_error VARCHAR(500) NOT NULL DEFAULT '',
    selected_at DATETIME NULL,
    code_sent_at DATETIME NULL,
    verified_at DATETIME NULL,
    expires_at DATETIME NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    UNIQUE INDEX idx_microsoft_binding_resource (resource_id),
    UNIQUE INDEX idx_microsoft_binding_active_address (active_binding_address),
    INDEX idx_microsoft_binding_address_status (binding_address, status),
    INDEX idx_microsoft_binding_owner_created (owner_user_id, created_at),
    CONSTRAINT fk_microsoft_binding_resource_owner FOREIGN KEY (resource_id, resource_type, owner_user_id) REFERENCES email_resources(id, type, owner_user_id) ON DELETE CASCADE,
    CONSTRAINT fk_microsoft_binding_resource FOREIGN KEY (resource_id) REFERENCES microsoft_resources(id) ON DELETE CASCADE,
    CONSTRAINT fk_microsoft_binding_owner FOREIGN KEY (owner_user_id) REFERENCES users(id) ON DELETE RESTRICT,
    CONSTRAINT chk_microsoft_binding_resource_type CHECK (resource_type = 'microsoft'),
    CONSTRAINT chk_microsoft_binding_status CHECK (status IN ('pending', 'code_sent', 'verified', 'timeout', 'failed', 'expired'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

ALTER TABLE generated_mailboxes
    ADD INDEX idx_generated_mailboxes_email_status (email, status);

-- +goose Down
ALTER TABLE generated_mailboxes
    DROP INDEX idx_generated_mailboxes_email_status;

DROP TABLE IF EXISTS inbound_mails;
DROP TABLE IF EXISTS microsoft_binding_mailboxes;
DROP TABLE IF EXISTS outbound_mails;
