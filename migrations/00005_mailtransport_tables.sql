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
    owner_user_id BIGINT UNSIGNED NOT NULL,
    source_object_key VARCHAR(500) NOT NULL COMMENT 'private MinIO object key for original RFC822 message',
    status VARCHAR(32) NOT NULL DEFAULT 'pending' COMMENT 'pending|processing|stored|failed',
    failure_reason VARCHAR(500) NOT NULL DEFAULT '' COMMENT 'sanitized diagnostic summary',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    INDEX idx_inbound_mails_status_created (status, created_at, id),
    INDEX idx_inbound_mails_status_updated (status, updated_at),
    INDEX idx_inbound_mails_resource_created (resource_id, created_at),
    INDEX idx_inbound_mails_recipient_created (recipient, created_at),
    INDEX idx_inbound_mails_object_key (source_object_key),
    CONSTRAINT fk_inbound_mails_resource_owner FOREIGN KEY (resource_id, owner_user_id) REFERENCES domain_resources(id, owner_user_id) ON DELETE RESTRICT,
    CONSTRAINT chk_inbound_mails_status CHECK (status IN ('pending', 'processing', 'stored', 'failed'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

ALTER TABLE generated_mailboxes
    ADD INDEX idx_generated_mailboxes_email_status (email, status);

-- +goose Down
ALTER TABLE generated_mailboxes
    DROP INDEX idx_generated_mailboxes_email_status;

DROP TABLE IF EXISTS inbound_mails;
DROP TABLE IF EXISTS outbound_mails;
