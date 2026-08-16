-- +goose Up

CREATE TABLE kitesim_upstream_settings (
    id TINYINT UNSIGNED PRIMARY KEY,
    account_id BIGINT UNSIGNED NULL,
	card_profile JSON NULL COMMENT 'Write-only card and billing profile; never returned by APIs',
    card_brand VARCHAR(24) NOT NULL DEFAULT '',
    card_last4 CHAR(4) NOT NULL DEFAULT '',
	card_expiry_month TINYINT UNSIGNED NOT NULL DEFAULT 0,
	card_expiry_year SMALLINT UNSIGNED NOT NULL DEFAULT 0,
	card_revision BIGINT UNSIGNED NOT NULL DEFAULT 0,
    balance DECIMAL(18,6) NOT NULL DEFAULT 0,
    balance_updated_at DATETIME(3) NULL,
    refresh_status VARCHAR(16) NOT NULL DEFAULT 'idle',
    refresh_queued_at DATETIME(3) NULL,
    refresh_started_at DATETIME(3) NULL,
    refresh_finished_at DATETIME(3) NULL,
    refresh_attempts INT UNSIGNED NOT NULL DEFAULT 0,
    last_safe_error VARCHAR(500) NOT NULL DEFAULT '',
    created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3)
        ON UPDATE CURRENT_TIMESTAMP(3),

    UNIQUE INDEX uk_kitesim_upstream_account (account_id),
    CONSTRAINT fk_kitesim_upstream_account
        FOREIGN KEY (account_id) REFERENCES kitesim_accounts(id) ON DELETE SET NULL,
    CONSTRAINT chk_kitesim_upstream_singleton CHECK (id = 1),
    CONSTRAINT chk_kitesim_upstream_refresh_status
        CHECK (refresh_status IN ('idle', 'queued', 'running', 'succeeded', 'failed')),
    CONSTRAINT chk_kitesim_upstream_card_expiry CHECK (
        (card_last4 = '' AND card_expiry_month = 0 AND card_expiry_year = 0)
        OR (card_last4 <> '' AND card_expiry_month BETWEEN 1 AND 12 AND card_expiry_year >= 2000)
    )
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

INSERT INTO kitesim_upstream_settings (id) VALUES (1);

CREATE TABLE kitesim_products (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    country_code VARCHAR(16) NOT NULL,
    package_id VARCHAR(64) NOT NULL,
    duration_type INT NOT NULL DEFAULT 0,
    duration_value INT NOT NULL DEFAULT 0,
    currency VARCHAR(16) NOT NULL DEFAULT 'USD',
    buy_price DECIMAL(18,6) NOT NULL DEFAULT 0,
    original_price DECIMAL(18,6) NOT NULL DEFAULT 0,
    auto_renew_price DECIMAL(18,6) NOT NULL DEFAULT 0,
    active TINYINT(1) NOT NULL DEFAULT 1,
    raw_payload JSON NOT NULL,
    last_seen_at DATETIME(3) NOT NULL,
    created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3)
        ON UPDATE CURRENT_TIMESTAMP(3),

    UNIQUE INDEX uk_kitesim_products_country_package (country_code, package_id),
    INDEX idx_kitesim_products_active_country_price (active, country_code, buy_price, id),
    CONSTRAINT chk_kitesim_products_identity CHECK (country_code <> '' AND package_id <> ''),
    CONSTRAINT chk_kitesim_products_prices CHECK (
        buy_price >= 0 AND original_price >= 0 AND auto_renew_price >= 0
    )
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE kitesim_operations (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    kind VARCHAR(16) NOT NULL,
    account_id BIGINT UNSIGNED NOT NULL,
    phone_id BIGINT UNSIGNED NULL,
    country_code VARCHAR(16) NOT NULL DEFAULT '',
    package_id VARCHAR(64) NOT NULL DEFAULT '',
    requested_count INT UNSIGNED NOT NULL DEFAULT 0,
	completed_count INT UNSIGNED NOT NULL DEFAULT 0,
	amount DECIMAL(18,6) NOT NULL DEFAULT 0,
	currency VARCHAR(16) NOT NULL DEFAULT '',
	card_revision BIGINT UNSIGNED NOT NULL DEFAULT 0,
	status VARCHAR(16) NOT NULL DEFAULT 'queued',
    attempts INT UNSIGNED NOT NULL DEFAULT 0,
    provider_order_nos JSON NULL,
	secret_payload JSON NULL COMMENT 'temporary write-only CVC payload, cleared when the worker starts',
	last_safe_error VARCHAR(500) NOT NULL DEFAULT '',
	operator_user_id BIGINT UNSIGNED NOT NULL,
	idempotency_key VARCHAR(128) NOT NULL,
	request_fingerprint CHAR(64) NOT NULL,
	request_id VARCHAR(64) NOT NULL DEFAULT '',
	path VARCHAR(500) NOT NULL DEFAULT '',
	reconcile_requested_at DATETIME(3) NULL,
	last_reconciled_at DATETIME(3) NULL,
	reconcile_attempts INT UNSIGNED NOT NULL DEFAULT 0,
	resolution_source VARCHAR(16) NOT NULL DEFAULT '',
	resolution_note VARCHAR(500) NOT NULL DEFAULT '',
	resolved_by_user_id BIGINT UNSIGNED NULL,
	resolved_at DATETIME(3) NULL,
    queued_at DATETIME(3) NOT NULL,
    started_at DATETIME(3) NULL,
    finished_at DATETIME(3) NULL,
    created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3)
        ON UPDATE CURRENT_TIMESTAMP(3),
	active_scope VARCHAR(96) GENERATED ALWAYS AS (
		CASE
			WHEN status IN ('queued', 'running', 'uncertain', 'requires_action')
				THEN CONCAT('account:', account_id)
			ELSE NULL
		END
	) STORED,
	active_recharge_scope VARCHAR(16) GENERATED ALWAYS AS (
		CASE
			WHEN kind = 'recharge'
				AND status IN ('queued', 'running', 'uncertain', 'requires_action')
				THEN 'recharge'
			ELSE NULL
		END
	) STORED,

	UNIQUE INDEX uk_kitesim_operations_active_scope (active_scope),
	UNIQUE INDEX uk_kitesim_operations_active_recharge_scope (active_recharge_scope),
	UNIQUE INDEX uk_kitesim_operations_idempotency (operator_user_id, idempotency_key),
	INDEX idx_kitesim_operations_dispatch (status, queued_at, id),
	INDEX idx_kitesim_operations_reconcile (status, reconcile_requested_at, last_reconciled_at, id),
	INDEX idx_kitesim_operations_recent (created_at, id),
    INDEX idx_kitesim_operations_account_recent (account_id, created_at, id),
    INDEX idx_kitesim_operations_phone_recent (phone_id, created_at, id),
    CONSTRAINT fk_kitesim_operations_account
        FOREIGN KEY (account_id) REFERENCES kitesim_accounts(id) ON DELETE RESTRICT,
    CONSTRAINT fk_kitesim_operations_phone
        FOREIGN KEY (phone_id) REFERENCES kitesim_phones(id) ON DELETE SET NULL,
    CONSTRAINT chk_kitesim_operations_kind CHECK (kind IN ('purchase', 'recharge', 'renew')),
    CONSTRAINT chk_kitesim_operations_status
        CHECK (status IN ('queued', 'running', 'succeeded', 'failed', 'uncertain', 'requires_action')),
	CONSTRAINT chk_kitesim_operations_counts CHECK (completed_count <= requested_count),
	CONSTRAINT chk_kitesim_operations_amount CHECK (amount >= 0),
	CONSTRAINT chk_kitesim_operations_idempotency CHECK (idempotency_key <> '' AND request_fingerprint <> ''),
	CONSTRAINT chk_kitesim_operations_resolution CHECK (
		resolution_source IN ('', 'query', 'manual')
	)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- +goose Down

DROP TABLE kitesim_operations;
DROP TABLE kitesim_products;
DROP TABLE kitesim_upstream_settings;
