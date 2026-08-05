-- +goose Up

CREATE TABLE smsbower_config (
    id TINYINT UNSIGNED NOT NULL PRIMARY KEY,
    enabled TINYINT(1) NOT NULL DEFAULT 0,
    api_key VARCHAR(512) NOT NULL DEFAULT '',
    strategy VARCHAR(32) NOT NULL DEFAULT 'local_first' COMMENT 'local_first|upstream_first',
    sync_interval_minutes INT UNSIGNED NOT NULL DEFAULT 5,
    balance_warning_threshold DECIMAL(18,6) NOT NULL DEFAULT 0,
    points_per_unit DECIMAL(18,6) NOT NULL DEFAULT 1,
    min_margin_rate DECIMAL(18,6) NOT NULL DEFAULT 0.10,
    created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
    CONSTRAINT chk_smsbower_config_singleton CHECK (id = 1),
    CONSTRAINT chk_smsbower_config_strategy CHECK (strategy IN ('local_first', 'upstream_first')),
    CONSTRAINT chk_smsbower_config_sync_interval CHECK (sync_interval_minutes BETWEEN 1 AND 1440),
    CONSTRAINT chk_smsbower_config_threshold CHECK (balance_warning_threshold >= 0),
    CONSTRAINT chk_smsbower_config_points CHECK (points_per_unit > 0),
    CONSTRAINT chk_smsbower_config_margin CHECK (min_margin_rate >= 0 AND min_margin_rate < 1)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

INSERT INTO smsbower_config(
    id, enabled, api_key, strategy, sync_interval_minutes,
    balance_warning_threshold, points_per_unit, min_margin_rate
)
SELECT
    1,
	CASE
		WHEN LOWER(COALESCE((SELECT value FROM system_settings WHERE `key` = 'smsbower_enabled'), 'false')) = 'true'
		 AND LOWER(COALESCE((SELECT value FROM system_settings WHERE `key` = 'smsbower_code_enabled'), 'true')) = 'true'
		THEN 1 ELSE 0
	END,
    COALESCE((SELECT value FROM system_settings WHERE `key` = 'smsbower_api_key'), ''),
    'local_first',
    CAST(COALESCE((SELECT value FROM system_settings WHERE `key` = 'smsbower_sync_interval_minutes'), '5') AS UNSIGNED),
    CAST(COALESCE((SELECT value FROM system_settings WHERE `key` = 'smsbower_balance_warning_threshold'), '0') AS DECIMAL(18,6)),
    CAST(COALESCE((SELECT value FROM system_settings WHERE `key` = 'smsbower_points_per_unit'), '1') AS DECIMAL(18,6)),
    CAST(COALESCE((SELECT value FROM system_settings WHERE `key` = 'smsbower_min_margin_rate'), '0.10') AS DECIMAL(18,6));

CREATE TABLE smsbower_project_routes (
    project_id BIGINT UNSIGNED NOT NULL PRIMARY KEY,
    service_code VARCHAR(64) NOT NULL,
    enabled TINYINT(1) NOT NULL DEFAULT 1,
    created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
    INDEX idx_smsbower_project_routes_service (service_code, enabled, project_id),
    CONSTRAINT fk_smsbower_project_routes_project
        FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE,
    CONSTRAINT fk_smsbower_project_routes_service
        FOREIGN KEY (service_code) REFERENCES smsbower_services(code) ON DELETE RESTRICT,
    CONSTRAINT chk_smsbower_project_routes_service CHECK (service_code <> '')
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

INSERT INTO smsbower_project_routes(project_id, service_code, enabled, created_at, updated_at)
SELECT project_id, provider_service_code, (enabled AND code_enabled), created_at, updated_at
FROM gmail_supply_routes
WHERE source = 'smsbower';

CREATE TABLE smsbower_orders (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    order_no VARCHAR(64) NOT NULL,
    project_id BIGINT UNSIGNED NOT NULL,
    product_id BIGINT UNSIGNED NOT NULL,
    service_code VARCHAR(64) NOT NULL,
    remote_mail_id BIGINT UNSIGNED NULL,
    email VARCHAR(320) NOT NULL DEFAULT '',
    status VARCHAR(32) NOT NULL DEFAULT 'pending'
        COMMENT 'pending|provisioning|active|completing|completed|cancelling|cancelled|failed|unknown',
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
    UNIQUE INDEX idx_smsbower_orders_order (order_no),
    UNIQUE INDEX idx_smsbower_orders_remote_mail (remote_mail_id),
    INDEX idx_smsbower_orders_due (status, next_poll_at, id),
    INDEX idx_smsbower_orders_service_created (service_code, created_at, id),
    INDEX idx_smsbower_orders_project_created (project_id, created_at, id),
    CONSTRAINT fk_smsbower_orders_order
        FOREIGN KEY (order_no) REFERENCES orders(order_no) ON DELETE RESTRICT,
    CONSTRAINT fk_smsbower_orders_project
        FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE RESTRICT,
    CONSTRAINT fk_smsbower_orders_product_project
        FOREIGN KEY (product_id, project_id) REFERENCES project_products(id, project_id) ON DELETE RESTRICT,
    CONSTRAINT fk_smsbower_orders_service
        FOREIGN KEY (service_code) REFERENCES smsbower_services(code) ON DELETE RESTRICT,
    CONSTRAINT chk_smsbower_orders_status CHECK (
        status IN ('pending', 'provisioning', 'active', 'completing', 'completed', 'cancelling', 'cancelled', 'failed', 'unknown')
    ),
    CONSTRAINT chk_smsbower_orders_count CHECK (received_count <= 3),
    CONSTRAINT chk_smsbower_orders_cost CHECK (
        upstream_price_snapshot >= 0
        AND points_per_unit_snapshot > 0
        AND cost_points_snapshot >= 0
        AND max_price_snapshot >= 0
    ),
    CONSTRAINT chk_smsbower_orders_action CHECK (
        pending_remote_action IN ('', 'wait_next', 'complete', 'cancel')
    ),
    CONSTRAINT chk_smsbower_orders_version CHECK (version > 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

INSERT INTO smsbower_orders(
    id, order_no, project_id, product_id, service_code, remote_mail_id, email,
    status, received_count, codes_json, upstream_price_snapshot,
    points_per_unit_snapshot, cost_points_snapshot, max_price_snapshot,
    pending_remote_action, next_poll_at, last_safe_error, version,
    started_at, expires_at, completed_at, created_at, updated_at
)
SELECT
    s.id, s.order_no, o.project_id, o.project_product_id, s.provider_service_code,
    CASE WHEN s.source_ref REGEXP '^[1-9][0-9]*$' THEN CAST(s.source_ref AS UNSIGNED) ELSE NULL END,
    s.email, s.status, s.received_count, s.codes_json, s.upstream_price_snapshot,
    CASE
        WHEN s.points_per_unit_snapshot > 0 THEN s.points_per_unit_snapshot
        ELSE 1
    END,
    s.cost_points_snapshot, s.max_price_snapshot,
    s.pending_remote_action, s.next_poll_at, s.last_safe_error, s.version,
    s.started_at, s.expires_at, s.completed_at, s.created_at, s.updated_at
FROM gmail_code_sessions AS s
JOIN orders AS o ON o.order_no = s.order_no
WHERE s.source = 'smsbower';

DELETE ga
FROM gmail_allocations AS ga
WHERE ga.source = 'smsbower';

DELETE g
FROM allocation_order_guards AS g
WHERE g.type = 'gmail'
  AND NOT EXISTS (
      SELECT 1 FROM gmail_allocations AS ga
      WHERE ga.order_no = g.order_no AND ga.guard_type = 'gmail'
  )
  AND NOT EXISTS (
      SELECT 1 FROM smsbower_orders AS so
      WHERE so.order_no = g.order_no
  );

DELETE FROM gmail_code_sessions WHERE source = 'smsbower';
DELETE FROM gmail_supply_routes WHERE source = 'smsbower';

DELETE FROM system_settings
WHERE `key` IN (
    'smsbower_enabled', 'smsbower_code_enabled', 'smsbower_purchase_enabled',
    'smsbower_api_key', 'smsbower_sync_interval_minutes',
    'smsbower_balance_warning_threshold', 'smsbower_points_per_unit',
    'smsbower_min_margin_rate'
);

-- +goose Down

INSERT INTO system_settings(`key`, value) VALUES
	('smsbower_enabled', (SELECT IF(enabled, 'true', 'false') FROM smsbower_config WHERE id = 1)),
	('smsbower_code_enabled', (SELECT IF(enabled, 'true', 'false') FROM smsbower_config WHERE id = 1)),
    ('smsbower_purchase_enabled', 'false'),
    ('smsbower_api_key', (SELECT api_key FROM smsbower_config WHERE id = 1)),
    ('smsbower_sync_interval_minutes', (SELECT CAST(sync_interval_minutes AS CHAR) FROM smsbower_config WHERE id = 1)),
    ('smsbower_balance_warning_threshold', (SELECT CAST(balance_warning_threshold AS CHAR) FROM smsbower_config WHERE id = 1)),
    ('smsbower_points_per_unit', (SELECT CAST(points_per_unit AS CHAR) FROM smsbower_config WHERE id = 1)),
    ('smsbower_min_margin_rate', (SELECT CAST(min_margin_rate AS CHAR) FROM smsbower_config WHERE id = 1))
ON DUPLICATE KEY UPDATE value = VALUES(value);

INSERT INTO gmail_supply_routes(
    project_id, source, provider_service_code, enabled, code_enabled,
    purchase_enabled, created_at, updated_at
)
SELECT project_id, 'smsbower', service_code, enabled, enabled, 0, created_at, updated_at
FROM smsbower_project_routes
ON DUPLICATE KEY UPDATE
    provider_service_code = VALUES(provider_service_code),
    enabled = VALUES(enabled),
	code_enabled = VALUES(code_enabled),
    purchase_enabled = 0,
    updated_at = VALUES(updated_at);

INSERT INTO gmail_code_sessions(
    id, order_no, source, source_ref, provider_service_code, service_mode,
    email, status, received_count, codes_json, upstream_price_snapshot,
    points_per_unit_snapshot, cost_points_snapshot, max_price_snapshot,
    pending_remote_action, provider_cursor, provider_spam_cursor, next_poll_at,
    last_safe_error, version, started_at, expires_at, completed_at, created_at, updated_at
)
SELECT
    id, order_no, 'smsbower', COALESCE(CAST(remote_mail_id AS CHAR), ''), service_code, 'code',
    email, status, received_count, codes_json, upstream_price_snapshot,
    points_per_unit_snapshot, cost_points_snapshot, max_price_snapshot,
    pending_remote_action, 0, 0, next_poll_at, last_safe_error, version,
    started_at, expires_at, completed_at, created_at, updated_at
FROM smsbower_orders
ON DUPLICATE KEY UPDATE
    source_ref = VALUES(source_ref),
    provider_service_code = VALUES(provider_service_code),
    email = VALUES(email),
    status = VALUES(status),
    received_count = VALUES(received_count),
    codes_json = VALUES(codes_json),
    pending_remote_action = VALUES(pending_remote_action),
    next_poll_at = VALUES(next_poll_at),
    last_safe_error = VALUES(last_safe_error),
    version = VALUES(version),
    started_at = VALUES(started_at),
    expires_at = VALUES(expires_at),
    completed_at = VALUES(completed_at),
    updated_at = VALUES(updated_at);

DROP TABLE smsbower_orders;
DROP TABLE smsbower_project_routes;
DROP TABLE smsbower_config;
