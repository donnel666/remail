-- +goose Up

CREATE TABLE kitesim_accounts (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    account VARCHAR(320) NOT NULL,
	password VARCHAR(512) NOT NULL COMMENT 'Write-only platform password; never returned by APIs or written to logs',
	token TEXT NULL COMMENT 'Write-only long-lived token; never returned by APIs or written to logs',
    token_updated_at DATETIME(3) NULL,
    last_safe_error VARCHAR(500) NOT NULL DEFAULT '',
    last_synced_at DATETIME(3) NULL,
    sync_status VARCHAR(16) NOT NULL DEFAULT 'idle',
    sync_queued_at DATETIME(3) NULL,
    sync_started_at DATETIME(3) NULL,
    sync_finished_at DATETIME(3) NULL,
    sync_attempts INT UNSIGNED NOT NULL DEFAULT 0,
    created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3)
        ON UPDATE CURRENT_TIMESTAMP(3),

    UNIQUE INDEX uk_kitesim_accounts_account (account),
    INDEX idx_kitesim_accounts_synced (last_synced_at, id),

    CONSTRAINT chk_kitesim_accounts_account CHECK (account <> '' AND OCTET_LENGTH(password) > 0),
    CONSTRAINT chk_kitesim_accounts_sync_status
        CHECK (sync_status IN ('idle', 'queued', 'running', 'succeeded', 'failed'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE kitesim_phones (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    account_id BIGINT UNSIGNED NOT NULL,
    provider_order_id VARCHAR(128) NOT NULL,
    order_no VARCHAR(64) NOT NULL,
    phone_code VARCHAR(16) NOT NULL DEFAULT '',
    phone_number VARCHAR(32) NOT NULL,
    country_code VARCHAR(16) NOT NULL DEFAULT '',
    status TINYINT UNSIGNED NOT NULL,
    order_status INT NOT NULL DEFAULT 0,
    package_id VARCHAR(64) NOT NULL DEFAULT '',
    duration_type INT NOT NULL DEFAULT 0,
    duration_value INT NOT NULL DEFAULT 0,
    auto_renew TINYINT(1) NOT NULL DEFAULT 0,
    currency VARCHAR(16) NOT NULL DEFAULT '',
    original_amount DECIMAL(18,6) NOT NULL DEFAULT 0,
    paid_amount DECIMAL(18,6) NOT NULL DEFAULT 0,
    auto_renew_price DECIMAL(18,6) NOT NULL DEFAULT 0,
    create_time VARCHAR(32) NOT NULL DEFAULT '',
    provider_created_at DATETIME(3) NULL,
    payment_time VARCHAR(32) NOT NULL DEFAULT '',
    expire_time VARCHAR(32) NOT NULL DEFAULT '',
    latest_renewal_time VARCHAR(32) NOT NULL DEFAULT '',
    next_renewal_date VARCHAR(32) NOT NULL DEFAULT '',
    refund_time VARCHAR(32) NOT NULL DEFAULT '',
    raw_payload JSON NOT NULL,
    created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3)
        ON UPDATE CURRENT_TIMESTAMP(3),

    UNIQUE INDEX uk_kitesim_phones_account_order (account_id, provider_order_id),
    INDEX idx_kitesim_phones_order_no (order_no),
    INDEX idx_kitesim_phones_phone (phone_number),
    INDEX idx_kitesim_phones_provider_created (provider_created_at, account_id, id),
    INDEX idx_kitesim_phones_status_account (status, account_id, id),

    CONSTRAINT fk_kitesim_phones_account
        FOREIGN KEY (account_id) REFERENCES kitesim_accounts(id) ON DELETE CASCADE,
    CONSTRAINT chk_kitesim_phones_status CHECK (status BETWEEN 0 AND 4),
    CONSTRAINT chk_kitesim_phones_number CHECK (phone_number <> '')
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- +goose Down

DROP TABLE kitesim_phones;
DROP TABLE kitesim_accounts;
