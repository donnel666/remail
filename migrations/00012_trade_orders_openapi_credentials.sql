-- +goose Up

CREATE TABLE api_keys (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    user_id BIGINT UNSIGNED NOT NULL,
    name VARCHAR(120) NOT NULL DEFAULT '',
    key_prefix VARCHAR(32) NOT NULL,
    key_plain VARCHAR(255) NOT NULL,
    enabled TINYINT(1) NOT NULL DEFAULT 1,
    rate_limit_per_minute INT NOT NULL DEFAULT 60,
    concurrency_limit INT NOT NULL DEFAULT 5,
    active_requests INT NOT NULL DEFAULT 0,
    window_started_at DATETIME NULL,
    window_request_count INT NOT NULL DEFAULT 0,
    expire_at DATETIME NULL,
    last_used_at DATETIME NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    UNIQUE INDEX idx_api_keys_prefix (key_prefix),
    UNIQUE INDEX idx_api_keys_plain (key_plain),
    UNIQUE INDEX idx_api_keys_id_user (id, user_id),
    INDEX idx_api_keys_user_created (user_id, created_at, id),
    INDEX idx_api_keys_enabled_expire (enabled, expire_at),
    CONSTRAINT fk_api_keys_user FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE RESTRICT,
    CONSTRAINT chk_api_keys_prefix CHECK (key_prefix <> '' AND key_plain <> ''),
    CONSTRAINT chk_api_keys_limits CHECK (
        rate_limit_per_minute > 0
        AND concurrency_limit > 0
        AND active_requests >= 0
        AND active_requests <= concurrency_limit
        AND window_request_count >= 0
    )
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE orders (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    order_no VARCHAR(64) NOT NULL,
    user_id BIGINT UNSIGNED NOT NULL,
    project_id BIGINT UNSIGNED NOT NULL,
    project_product_id BIGINT UNSIGNED NOT NULL,
    product_type VARCHAR(32) NOT NULL COMMENT 'microsoft|domain',
    service_mode VARCHAR(32) NOT NULL COMMENT 'code|purchase',
    supply_policy VARCHAR(32) NOT NULL DEFAULT 'private_first' COMMENT 'private_first|public_only',
    status VARCHAR(32) NOT NULL DEFAULT 'pending_payment',
    pay_amount DECIMAL(18,2) NOT NULL,
    refund_amount DECIMAL(18,2) NOT NULL DEFAULT 0.00,
    debit_tx_id BIGINT UNSIGNED NULL,
    refund_tx_id BIGINT UNSIGNED NULL,
    allocation_type VARCHAR(32) NULL COMMENT 'microsoft|domain',
    microsoft_alloc_id BIGINT UNSIGNED NULL,
    domain_alloc_id BIGINT UNSIGNED NULL,
    delivery_email VARCHAR(255) NOT NULL DEFAULT '',
    receive_started_at DATETIME NULL,
    receive_until DATETIME NULL,
    activated_at DATETIME NULL,
    after_sale_until DATETIME NULL,
    client_channel VARCHAR(32) NOT NULL COMMENT 'console|api_key',
    api_key_id BIGINT UNSIGNED NULL,
    idempotency_key VARCHAR(128) NOT NULL,
    request_fingerprint CHAR(64) NOT NULL,
    idempotency_subject VARCHAR(80) GENERATED ALWAYS AS (
        CASE
            WHEN client_channel = 'api_key' THEN CONCAT('api_key:', COALESCE(api_key_id, 0))
            ELSE CONCAT('user:', user_id)
        END
    ) STORED,
    service_cleanup_status VARCHAR(32) NOT NULL DEFAULT 'none',
    archived_at DATETIME NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    version INT NOT NULL DEFAULT 1,
    UNIQUE INDEX idx_orders_order_no (order_no),
    UNIQUE INDEX idx_orders_idempotency (idempotency_subject, idempotency_key),
    INDEX idx_orders_user_created (user_id, created_at, id),
    INDEX idx_orders_status_created (status, created_at, id),
    INDEX idx_orders_project_created (project_id, created_at, id),
    INDEX idx_orders_delivery_status (delivery_email, status),
    INDEX idx_orders_api_key_created (api_key_id, created_at, id),
    INDEX idx_orders_api_key_user (api_key_id, user_id),
    CONSTRAINT fk_orders_user FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE RESTRICT,
    CONSTRAINT fk_orders_project FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE RESTRICT,
    CONSTRAINT fk_orders_product_project FOREIGN KEY (project_product_id, project_id) REFERENCES project_products(id, project_id) ON DELETE RESTRICT,
    CONSTRAINT fk_orders_debit_tx FOREIGN KEY (debit_tx_id) REFERENCES wallet_transactions(id) ON DELETE RESTRICT,
    CONSTRAINT fk_orders_refund_tx FOREIGN KEY (refund_tx_id) REFERENCES wallet_transactions(id) ON DELETE RESTRICT,
    CONSTRAINT fk_orders_ms_alloc FOREIGN KEY (microsoft_alloc_id) REFERENCES microsoft_allocations(id) ON DELETE RESTRICT,
    CONSTRAINT fk_orders_domain_alloc FOREIGN KEY (domain_alloc_id) REFERENCES domain_allocations(id) ON DELETE RESTRICT,
    CONSTRAINT fk_orders_api_key_user FOREIGN KEY (api_key_id, user_id) REFERENCES api_keys(id, user_id) ON DELETE RESTRICT,
    CONSTRAINT chk_orders_product_type CHECK (product_type IN ('microsoft', 'domain')),
    CONSTRAINT chk_orders_service_mode CHECK (service_mode IN ('code', 'purchase')),
    CONSTRAINT chk_orders_supply_policy CHECK (supply_policy IN ('private_first', 'public_only')),
    CONSTRAINT chk_orders_status CHECK (status IN ('pending_payment', 'paid', 'active', 'completed', 'refunded', 'failed', 'closed')),
    CONSTRAINT chk_orders_channel CHECK (
        (client_channel = 'console' AND api_key_id IS NULL)
        OR (client_channel = 'api_key' AND api_key_id IS NOT NULL)
    ),
    CONSTRAINT chk_orders_money CHECK (pay_amount > 0 AND refund_amount >= 0),
    CONSTRAINT chk_orders_debit_state CHECK (status NOT IN ('paid', 'active', 'completed', 'refunded') OR debit_tx_id IS NOT NULL),
    CONSTRAINT chk_orders_refund_state CHECK (
        (status NOT IN ('refunded', 'failed') AND refund_tx_id IS NULL AND refund_amount = 0.00)
        OR (status = 'refunded' AND refund_tx_id IS NOT NULL AND refund_amount > 0)
        OR (
            status = 'failed'
            AND (
                (debit_tx_id IS NULL AND refund_tx_id IS NULL AND refund_amount = 0.00)
                OR (debit_tx_id IS NOT NULL AND refund_tx_id IS NOT NULL AND refund_amount > 0)
            )
        )
    ),
    CONSTRAINT chk_orders_allocation_shape CHECK (
        (allocation_type IS NULL AND microsoft_alloc_id IS NULL AND domain_alloc_id IS NULL)
        OR (allocation_type = 'microsoft' AND microsoft_alloc_id IS NOT NULL AND domain_alloc_id IS NULL)
        OR (allocation_type = 'domain' AND domain_alloc_id IS NOT NULL AND microsoft_alloc_id IS NULL)
    ),
    CONSTRAINT chk_orders_active_allocation CHECK (status NOT IN ('active', 'completed') OR allocation_type IS NOT NULL),
    CONSTRAINT chk_orders_delivery CHECK (status NOT IN ('active', 'completed') OR delivery_email <> ''),
    CONSTRAINT chk_orders_cleanup CHECK (service_cleanup_status IN ('none', 'succeeded', 'partial_failure')),
    CONSTRAINT chk_orders_idempotency CHECK (idempotency_key <> '' AND request_fingerprint <> '')
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE order_events (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    event_no VARCHAR(64) NOT NULL,
    order_no VARCHAR(64) NOT NULL,
    event_type VARCHAR(64) NOT NULL,
    from_status VARCHAR(32) NULL,
    to_status VARCHAR(32) NULL,
    operator_type VARCHAR(32) NOT NULL DEFAULT 'system',
    reason VARCHAR(500) NOT NULL DEFAULT '',
    event_context JSON NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE INDEX idx_order_events_event_no (event_no),
    INDEX idx_order_events_order_created (order_no, created_at, id),
    CONSTRAINT fk_order_events_order FOREIGN KEY (order_no) REFERENCES orders(order_no) ON DELETE CASCADE,
    CONSTRAINT chk_order_events_operator CHECK (operator_type IN ('user', 'admin', 'system', 'openapi')),
    CONSTRAINT chk_order_events_type CHECK (event_no <> '' AND event_type <> '')
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE order_tokens (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    token_prefix VARCHAR(32) NOT NULL,
    token_plain VARCHAR(255) NOT NULL,
    order_no VARCHAR(64) NOT NULL,
    enabled TINYINT(1) NOT NULL DEFAULT 1,
    expire_at DATETIME NULL,
    disabled_at DATETIME NULL,
    disabled_reason VARCHAR(500) NOT NULL DEFAULT '',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    UNIQUE INDEX idx_order_tokens_prefix (token_prefix),
    UNIQUE INDEX idx_order_tokens_plain (token_plain),
    UNIQUE INDEX idx_order_tokens_order (order_no),
    INDEX idx_order_tokens_enabled_expire (enabled, expire_at),
    CONSTRAINT fk_order_tokens_order FOREIGN KEY (order_no) REFERENCES orders(order_no) ON DELETE CASCADE,
    CONSTRAINT chk_order_tokens_prefix CHECK (token_prefix <> '' AND token_plain <> ''),
    CONSTRAINT chk_order_tokens_disabled CHECK (
        (enabled = 1 AND disabled_at IS NULL AND disabled_reason = '')
        OR (enabled = 0 AND disabled_at IS NOT NULL)
    )
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE api_logs (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    principal_type VARCHAR(32) NOT NULL COMMENT 'api_key|order_token',
    principal_id BIGINT UNSIGNED NOT NULL,
    user_id BIGINT UNSIGNED NULL,
    path VARCHAR(255) NOT NULL,
    method VARCHAR(16) NOT NULL,
    idempotency_key VARCHAR(128) NOT NULL DEFAULT '',
    http_status INT NOT NULL,
    duration_ms INT NOT NULL,
    request_id VARCHAR(64) NOT NULL DEFAULT '',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    INDEX idx_api_logs_principal_created (principal_type, principal_id, created_at, id),
    INDEX idx_api_logs_user_created (user_id, created_at, id),
    INDEX idx_api_logs_request (request_id),
    CONSTRAINT fk_api_logs_user FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE SET NULL,
    CONSTRAINT chk_api_logs_principal CHECK (principal_type IN ('api_key', 'order_token') AND principal_id > 0),
    CONSTRAINT chk_api_logs_http CHECK (http_status >= 100 AND http_status <= 599 AND duration_ms >= 0),
    CONSTRAINT chk_api_logs_request CHECK (path <> '' AND method <> '')
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

INSERT INTO casbin_rule (ptype, v0, v1, v2, v3) VALUES
    ('p', 'role:admin', 'trade:order', 'read', 'allow'),
    ('p', 'role:admin', 'trade:order', 'operate', 'allow'),
    ('p', 'role:super_admin', 'trade:order', 'read', 'allow'),
    ('p', 'role:super_admin', 'trade:order', 'operate', 'allow');

-- +goose Down

DELETE FROM casbin_rule WHERE v1 = 'trade:order';
DROP TABLE IF EXISTS api_logs;
DROP TABLE IF EXISTS order_tokens;
DROP TABLE IF EXISTS order_events;
DROP TABLE IF EXISTS orders;
DROP TABLE IF EXISTS api_keys;
