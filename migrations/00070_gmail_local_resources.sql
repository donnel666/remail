-- +goose Up

CREATE TABLE gmail_resources (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    email VARCHAR(320) NOT NULL,
    identity VARCHAR(320) NOT NULL,
    password VARCHAR(512) NOT NULL,
    two_factor_secret VARCHAR(512) NOT NULL,
    app_password VARCHAR(128) NOT NULL,
    status VARCHAR(24) NOT NULL DEFAULT 'available' COMMENT 'available|disabled|leased|sold',
    last_safe_error VARCHAR(500) NOT NULL DEFAULT '',
    last_checked_at DATETIME(3) NULL,
    created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
    UNIQUE INDEX idx_gmail_resources_email (email),
    UNIQUE INDEX idx_gmail_resources_identity (identity),
    INDEX idx_gmail_resources_status_created (status, created_at, id),
    CONSTRAINT chk_gmail_resources_email CHECK (email <> ''),
    CONSTRAINT chk_gmail_resources_credentials CHECK (
        password <> '' AND two_factor_secret <> '' AND app_password <> ''
    ),
    CONSTRAINT chk_gmail_resources_status CHECK (
        status IN ('available', 'disabled', 'leased', 'sold')
    )
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

ALTER TABLE orders
    ADD COLUMN gmail_resource_id BIGINT UNSIGNED NULL AFTER gmail_session_id,
    ADD COLUMN gmail_cost_points_snapshot DECIMAL(18,6) NOT NULL DEFAULT 0 AFTER gmail_resource_id,
    ADD CONSTRAINT fk_orders_gmail_resource FOREIGN KEY (gmail_resource_id) REFERENCES gmail_resources(id) ON DELETE RESTRICT,
    DROP CHECK chk_orders_allocation_shape,
    ADD CONSTRAINT chk_orders_allocation_shape CHECK (
        (allocation_type IS NULL AND microsoft_alloc_id IS NULL AND domain_alloc_id IS NULL AND gmail_session_id IS NULL AND gmail_resource_id IS NULL)
        OR (allocation_type = 'microsoft' AND microsoft_alloc_id IS NOT NULL AND domain_alloc_id IS NULL AND gmail_session_id IS NULL AND gmail_resource_id IS NULL)
        OR (allocation_type = 'domain' AND domain_alloc_id IS NOT NULL AND microsoft_alloc_id IS NULL AND gmail_session_id IS NULL AND gmail_resource_id IS NULL)
        OR (allocation_type = 'gmail' AND gmail_session_id IS NOT NULL AND gmail_resource_id IS NULL AND microsoft_alloc_id IS NULL AND domain_alloc_id IS NULL)
        OR (allocation_type = 'gmail' AND gmail_resource_id IS NOT NULL AND gmail_session_id IS NULL AND microsoft_alloc_id IS NULL AND domain_alloc_id IS NULL)
    );

-- +goose Down

DROP TEMPORARY TABLE IF EXISTS gmail_resources_down_guard;
CREATE TEMPORARY TABLE gmail_resources_down_guard (
    resource_rows BIGINT NOT NULL,
    CONSTRAINT chk_gmail_resources_down_guard CHECK (resource_rows = 0)
);
INSERT INTO gmail_resources_down_guard (resource_rows)
SELECT
    (SELECT COUNT(*) FROM gmail_resources)
    + (SELECT COUNT(*) FROM orders WHERE gmail_resource_id IS NOT NULL);
DROP TEMPORARY TABLE gmail_resources_down_guard;

ALTER TABLE orders
    DROP CHECK chk_orders_allocation_shape,
    DROP FOREIGN KEY fk_orders_gmail_resource,
    DROP COLUMN gmail_cost_points_snapshot,
    DROP COLUMN gmail_resource_id,
    ADD CONSTRAINT chk_orders_allocation_shape CHECK (
        (allocation_type IS NULL AND microsoft_alloc_id IS NULL AND domain_alloc_id IS NULL AND gmail_session_id IS NULL)
        OR (allocation_type = 'microsoft' AND microsoft_alloc_id IS NOT NULL AND domain_alloc_id IS NULL AND gmail_session_id IS NULL)
        OR (allocation_type = 'domain' AND domain_alloc_id IS NOT NULL AND microsoft_alloc_id IS NULL AND gmail_session_id IS NULL)
        OR (allocation_type = 'gmail' AND gmail_session_id IS NOT NULL AND microsoft_alloc_id IS NULL AND domain_alloc_id IS NULL)
    );

DROP TABLE gmail_resources;
