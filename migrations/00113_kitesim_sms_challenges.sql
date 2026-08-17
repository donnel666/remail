-- +goose Up

CREATE TABLE kitesim_sms_challenges (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    phone_id BIGINT UNSIGNED NOT NULL,
    usage_event_id BIGINT UNSIGNED NOT NULL,
    owner_key VARCHAR(160) NULL,
    purpose VARCHAR(64) NOT NULL,
    status VARCHAR(32) NOT NULL DEFAULT 'reserved'
        COMMENT 'reserved|sent|completed|canceled|expired|send_failed|infrastructure_failed',
    active_phone_id BIGINT UNSIGNED NULL,
    sent_at DATETIME(3) NULL,
    expires_at DATETIME(3) NOT NULL,
    finished_at DATETIME(3) NULL,
    message_fingerprint CHAR(64) NULL,
    message_caller VARCHAR(255) NULL,
    message_content TEXT NULL,
    message_time VARCHAR(64) NULL,
    message_received_at DATETIME(3) NULL,
    created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3)
        ON UPDATE CURRENT_TIMESTAMP(3),

    UNIQUE INDEX uk_kitesim_sms_challenge_usage (usage_event_id),
    UNIQUE INDEX uk_kitesim_sms_challenge_owner (owner_key),
    UNIQUE INDEX uk_kitesim_sms_challenge_active_phone (active_phone_id),
    UNIQUE INDEX uk_kitesim_sms_challenge_message (message_fingerprint),
    INDEX idx_kitesim_sms_challenge_phone (phone_id, created_at, id),
    INDEX idx_kitesim_sms_challenge_expiry (status, expires_at, id),
    CONSTRAINT fk_kitesim_sms_challenge_phone
        FOREIGN KEY (phone_id) REFERENCES kitesim_phones(id) ON DELETE RESTRICT,
    CONSTRAINT fk_kitesim_sms_challenge_active_phone
        FOREIGN KEY (active_phone_id) REFERENCES kitesim_phones(id) ON DELETE RESTRICT,
    CONSTRAINT fk_kitesim_sms_challenge_usage
        FOREIGN KEY (usage_event_id) REFERENCES kitesim_phone_usage_events(id) ON DELETE RESTRICT,
    CONSTRAINT chk_kitesim_sms_challenge_purpose CHECK (purpose <> ''),
    CONSTRAINT chk_kitesim_sms_challenge_status CHECK (
        status IN ('reserved', 'sent', 'completed', 'canceled', 'expired', 'send_failed', 'infrastructure_failed')
    ),
    CONSTRAINT chk_kitesim_sms_challenge_active CHECK (
        (status IN ('reserved', 'sent') AND active_phone_id = phone_id AND finished_at IS NULL)
        OR
        (status IN ('completed', 'canceled', 'expired', 'send_failed', 'infrastructure_failed') AND active_phone_id IS NULL AND finished_at IS NOT NULL)
    )
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- +goose Down

DROP TABLE kitesim_sms_challenges;
