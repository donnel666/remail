-- +goose Up

ALTER TABLE project_products
    DROP CHECK chk_project_products_type,
    DROP CHECK chk_project_products_weights,
    ADD CONSTRAINT chk_project_products_type CHECK (type IN ('microsoft', 'domain', 'random', 'gmail')),
    ADD CONSTRAINT chk_project_products_weights CHECK (
        main_weight >= 0
        AND dot_weight >= 0
        AND plus_weight >= 0
        AND (type NOT IN ('microsoft', 'gmail') OR main_weight + dot_weight + plus_weight > 0)
        AND (type <> 'domain' OR (main_weight = 0 AND dot_weight = 0 AND plus_weight = 0))
        AND (type <> 'random' OR (main_weight = 1 AND dot_weight = 1 AND plus_weight = 1))
    ),
    ADD CONSTRAINT chk_project_products_gmail CHECK (
        type <> 'gmail'
        OR code_enabled = 0
        OR code_window_minutes = 1440
    );

ALTER TABLE orders
    DROP CHECK chk_orders_product_type,
    ADD CONSTRAINT chk_orders_product_type CHECK (product_type IN ('microsoft', 'domain', 'random', 'gmail'));

CREATE TABLE smsbower_account_state (
    id TINYINT UNSIGNED NOT NULL PRIMARY KEY,
    balance DECIMAL(18,6) NOT NULL DEFAULT 0,
    health_status VARCHAR(24) NOT NULL DEFAULT 'disabled' COMMENT 'disabled|healthy|degraded|unavailable',
    consecutive_failures INT UNSIGNED NOT NULL DEFAULT 0,
    last_safe_error VARCHAR(500) NOT NULL DEFAULT '',
    balance_alert_active TINYINT(1) NOT NULL DEFAULT 0,
    failure_alert_active TINYINT(1) NOT NULL DEFAULT 0,
    generation BIGINT UNSIGNED NOT NULL DEFAULT 1,
    last_synced_at DATETIME(3) NULL,
    last_success_at DATETIME(3) NULL,
    created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
    CONSTRAINT chk_smsbower_account_singleton CHECK (id = 1),
    CONSTRAINT chk_smsbower_account_balance CHECK (balance >= 0),
    CONSTRAINT chk_smsbower_account_health CHECK (health_status IN ('disabled', 'healthy', 'degraded', 'unavailable')),
    CONSTRAINT chk_smsbower_account_generation CHECK (generation > 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

INSERT INTO smsbower_account_state (id) VALUES (1);

CREATE TABLE smsbower_services (
    code VARCHAR(64) NOT NULL PRIMARY KEY,
    name VARCHAR(191) NOT NULL,
    gmail_price DECIMAL(18,6) NOT NULL DEFAULT 0,
    gmail_stock INT UNSIGNED NOT NULL DEFAULT 0,
    previous_price DECIMAL(18,6) NULL,
    last_notified_price DECIMAL(18,6) NULL,
    active TINYINT(1) NOT NULL DEFAULT 1,
    price_changed_at DATETIME(3) NULL,
    last_seen_at DATETIME(3) NOT NULL,
    created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
    INDEX idx_smsbower_services_active_name (active, name),
    INDEX idx_smsbower_services_price_changed (price_changed_at, code),
    CONSTRAINT chk_smsbower_services_code CHECK (code <> ''),
    CONSTRAINT chk_smsbower_services_price CHECK (gmail_price >= 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE gmail_supply_routes (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    project_id BIGINT UNSIGNED NOT NULL,
    source VARCHAR(24) NOT NULL DEFAULT 'smsbower' COMMENT 'smsbower|local',
    provider_service_code VARCHAR(64) NOT NULL DEFAULT '',
    enabled TINYINT(1) NOT NULL DEFAULT 1,
    code_enabled TINYINT(1) NOT NULL DEFAULT 1,
    purchase_enabled TINYINT(1) NOT NULL DEFAULT 0,
    created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
    UNIQUE INDEX idx_gmail_supply_routes_project_source (project_id, source),
    INDEX idx_gmail_supply_routes_provider (source, provider_service_code, enabled, code_enabled, purchase_enabled),
    CONSTRAINT fk_gmail_supply_routes_project FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE,
    CONSTRAINT chk_gmail_supply_routes_source CHECK (source IN ('smsbower', 'local')),
    CONSTRAINT chk_gmail_supply_routes_provider CHECK (
        (source = 'smsbower' AND provider_service_code <> '')
        OR (source = 'local' AND provider_service_code = '')
    ),
    CONSTRAINT chk_gmail_supply_routes_modes CHECK (
        code_enabled IN (0, 1) AND purchase_enabled IN (0, 1)
    )
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE gmail_code_sessions (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    order_no VARCHAR(64) NOT NULL,
    source VARCHAR(24) NOT NULL DEFAULT 'smsbower' COMMENT 'smsbower|local',
    source_ref VARCHAR(191) NOT NULL DEFAULT '',
    provider_service_code VARCHAR(64) NOT NULL DEFAULT '',
    email VARCHAR(320) NOT NULL DEFAULT '',
    status VARCHAR(32) NOT NULL DEFAULT 'pending' COMMENT 'pending|provisioning|active|completing|completed|cancelling|cancelled|failed|unknown',
    received_count TINYINT UNSIGNED NOT NULL DEFAULT 0,
    codes_json JSON NOT NULL,
    upstream_price_snapshot DECIMAL(18,6) NOT NULL DEFAULT 0,
    points_per_unit_snapshot DECIMAL(18,6) NOT NULL DEFAULT 0,
    cost_points_snapshot DECIMAL(18,6) NOT NULL DEFAULT 0,
    max_price_snapshot DECIMAL(18,6) NOT NULL DEFAULT 0,
    pending_remote_action VARCHAR(24) NOT NULL DEFAULT '' COMMENT '|wait_next|complete|cancel',
    next_poll_at DATETIME(3) NULL,
    last_safe_error VARCHAR(500) NOT NULL DEFAULT '',
    version INT UNSIGNED NOT NULL DEFAULT 1,
    started_at DATETIME(3) NULL,
    expires_at DATETIME(3) NULL,
    completed_at DATETIME(3) NULL,
    created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
    UNIQUE INDEX idx_gmail_code_sessions_order (order_no),
    INDEX idx_gmail_code_sessions_due (status, next_poll_at, id),
    INDEX idx_gmail_code_sessions_expiry (status, expires_at, id),
    INDEX idx_gmail_code_sessions_retention (status, completed_at, id),
    INDEX idx_gmail_code_sessions_source_created (source, created_at, id),
    CONSTRAINT chk_gmail_code_sessions_source CHECK (source IN ('smsbower', 'local')),
    CONSTRAINT chk_gmail_code_sessions_status CHECK (status IN ('pending', 'provisioning', 'active', 'completing', 'completed', 'cancelling', 'cancelled', 'failed', 'unknown')),
    CONSTRAINT chk_gmail_code_sessions_count CHECK (received_count <= 3),
    CONSTRAINT chk_gmail_code_sessions_cost CHECK (
        upstream_price_snapshot >= 0
        AND points_per_unit_snapshot >= 0
        AND cost_points_snapshot >= 0
        AND max_price_snapshot >= 0
    ),
    CONSTRAINT chk_gmail_code_sessions_action CHECK (pending_remote_action IN ('', 'wait_next', 'complete', 'cancel')),
    CONSTRAINT chk_gmail_code_sessions_version CHECK (version > 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

ALTER TABLE orders
    ADD COLUMN gmail_session_id BIGINT UNSIGNED NULL AFTER domain_alloc_id,
    ADD CONSTRAINT fk_orders_gmail_session FOREIGN KEY (gmail_session_id) REFERENCES gmail_code_sessions(id) ON DELETE RESTRICT,
    DROP CHECK chk_orders_allocation_shape,
    ADD CONSTRAINT chk_orders_allocation_shape CHECK (
        (allocation_type IS NULL AND microsoft_alloc_id IS NULL AND domain_alloc_id IS NULL AND gmail_session_id IS NULL)
        OR (allocation_type = 'microsoft' AND microsoft_alloc_id IS NOT NULL AND domain_alloc_id IS NULL AND gmail_session_id IS NULL)
        OR (allocation_type = 'domain' AND domain_alloc_id IS NOT NULL AND microsoft_alloc_id IS NULL AND gmail_session_id IS NULL)
        OR (allocation_type = 'gmail' AND gmail_session_id IS NOT NULL AND microsoft_alloc_id IS NULL AND domain_alloc_id IS NULL)
    );

-- +goose Down

DROP TEMPORARY TABLE IF EXISTS gmail_upstream_down_guard;
CREATE TEMPORARY TABLE gmail_upstream_down_guard (
    gmail_rows BIGINT NOT NULL,
    CONSTRAINT chk_gmail_upstream_down_guard CHECK (gmail_rows = 0)
);
INSERT INTO gmail_upstream_down_guard (gmail_rows)
SELECT
    (SELECT COUNT(*) FROM orders WHERE product_type = 'gmail')
    + (SELECT COUNT(*) FROM project_products WHERE type = 'gmail')
    + (SELECT COUNT(*) FROM gmail_code_sessions)
    + (SELECT COUNT(*) FROM gmail_supply_routes);
DROP TEMPORARY TABLE gmail_upstream_down_guard;

ALTER TABLE orders
    DROP CHECK chk_orders_allocation_shape,
    DROP FOREIGN KEY fk_orders_gmail_session,
    DROP COLUMN gmail_session_id,
    ADD CONSTRAINT chk_orders_allocation_shape CHECK (
        (allocation_type IS NULL AND microsoft_alloc_id IS NULL AND domain_alloc_id IS NULL)
        OR (allocation_type = 'microsoft' AND microsoft_alloc_id IS NOT NULL AND domain_alloc_id IS NULL)
        OR (allocation_type = 'domain' AND domain_alloc_id IS NOT NULL AND microsoft_alloc_id IS NULL)
    ),
    DROP CHECK chk_orders_product_type,
    ADD CONSTRAINT chk_orders_product_type CHECK (product_type IN ('microsoft', 'domain', 'random'));

DROP TABLE gmail_code_sessions;
DROP TABLE gmail_supply_routes;
DROP TABLE smsbower_services;
DROP TABLE smsbower_account_state;

ALTER TABLE project_products
    DROP CHECK chk_project_products_gmail,
    DROP CHECK chk_project_products_type,
    DROP CHECK chk_project_products_weights,
    ADD CONSTRAINT chk_project_products_type CHECK (type IN ('microsoft', 'domain', 'random')),
    ADD CONSTRAINT chk_project_products_weights CHECK (
        main_weight >= 0
        AND dot_weight >= 0
        AND plus_weight >= 0
        AND (type <> 'microsoft' OR main_weight + dot_weight + plus_weight > 0)
        AND (type <> 'domain' OR (main_weight = 0 AND dot_weight = 0 AND plus_weight = 0))
        AND (type <> 'random' OR (main_weight = 1 AND dot_weight = 1 AND plus_weight = 1))
    );
