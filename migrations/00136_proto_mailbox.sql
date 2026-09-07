-- +goose Up

-- Re-entry after committed DDL must not recreate an existing Proto table.
-- Keep shared CHECK validation out of retries once Proto is already admitted.
DROP PROCEDURE IF EXISTS create_proto_mailbox_00136;

-- +goose StatementBegin
CREATE PROCEDURE create_proto_mailbox_00136()
BEGIN
    DECLARE previous_lock_wait_timeout BIGINT;
    DECLARE previous_innodb_lock_wait_timeout BIGINT;
    DECLARE EXIT HANDLER FOR SQLEXCEPTION
    BEGIN
        SET SESSION lock_wait_timeout = previous_lock_wait_timeout;
        SET SESSION innodb_lock_wait_timeout = previous_innodb_lock_wait_timeout;
        RESIGNAL;
    END;

    SET previous_lock_wait_timeout = @@SESSION.lock_wait_timeout;
    SET previous_innodb_lock_wait_timeout = @@SESSION.innodb_lock_wait_timeout;
    SET SESSION lock_wait_timeout = 5;
    SET SESSION innodb_lock_wait_timeout = 5;

    IF NOT EXISTS (
        SELECT 1 FROM information_schema.check_constraints
        WHERE constraint_schema = DATABASE() AND constraint_name = 'chk_email_resources_type'
          AND LOWER(check_clause) LIKE '%proto%'
    ) THEN
        ALTER TABLE email_resources
            DROP CHECK chk_email_resources_type,
            ADD CONSTRAINT chk_email_resources_type CHECK (type IN ('microsoft', 'domain', 'gmail', 'icloud', 'proto'));
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM information_schema.check_constraints
        WHERE constraint_schema = DATABASE() AND constraint_name = 'chk_project_products_type'
          AND LOWER(check_clause) LIKE '%proto%'
    ) OR NOT EXISTS (
        SELECT 1 FROM information_schema.check_constraints
        WHERE constraint_schema = DATABASE() AND constraint_name = 'chk_project_products_weights'
          AND LOWER(check_clause) LIKE '%proto%'
    ) THEN
        ALTER TABLE project_products
            DROP CHECK chk_project_products_type,
            DROP CHECK chk_project_products_weights,
            ADD CONSTRAINT chk_project_products_type CHECK (
                type IN ('microsoft', 'domain', 'random', 'gmail', 'gmail_variant', 'icloud', 'proto')
            ),
            ADD CONSTRAINT chk_project_products_weights CHECK (
                main_weight >= 0
                AND dot_weight >= 0
                AND plus_weight >= 0
                AND (type NOT IN ('microsoft', 'gmail', 'gmail_variant', 'icloud', 'proto')
                     OR main_weight + dot_weight + plus_weight > 0)
                AND (type <> 'domain'
                     OR (main_weight = 0 AND dot_weight = 0 AND plus_weight = 0))
                AND (type <> 'random'
                     OR (main_weight = 1 AND dot_weight = 1 AND plus_weight = 1))
                AND (type <> 'gmail_variant'
                     OR (main_weight = 0 AND dot_weight = 0 AND plus_weight = 1))
                AND (type <> 'icloud'
                     OR (main_weight > 0 AND dot_weight = 0 AND plus_weight = 0))
                AND (type <> 'proto'
                     OR (main_weight > 0 AND dot_weight = 0 AND plus_weight = 0))
            );
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM information_schema.check_constraints
        WHERE constraint_schema = DATABASE() AND constraint_name = 'chk_orders_product_type'
          AND LOWER(check_clause) LIKE '%proto%'
    ) OR NOT EXISTS (
        SELECT 1 FROM information_schema.check_constraints
        WHERE constraint_schema = DATABASE() AND constraint_name = 'chk_orders_allocation_shape'
          AND LOWER(check_clause) LIKE '%proto%'
    ) THEN
        ALTER TABLE orders
            DROP CHECK chk_orders_product_type,
            DROP CHECK chk_orders_allocation_shape,
            ADD CONSTRAINT chk_orders_product_type CHECK (
                product_type IN ('microsoft', 'domain', 'random', 'gmail', 'gmail_variant', 'icloud', 'proto')
            ) NOT ENFORCED,
            ADD CONSTRAINT chk_orders_allocation_shape CHECK (
                allocation_type IS NULL
                OR allocation_type IN ('microsoft', 'domain', 'gmail', 'icloud', 'proto')
            ) NOT ENFORCED,
            ALGORITHM=INSTANT;
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM information_schema.check_constraints
        WHERE constraint_schema = DATABASE() AND constraint_name = 'chk_allocation_order_guards_type'
          AND LOWER(check_clause) LIKE '%proto%'
    ) THEN
        ALTER TABLE allocation_order_guards
            DROP CHECK chk_allocation_order_guards_type,
            ADD CONSTRAINT chk_allocation_order_guards_type CHECK (
                type IN ('microsoft', 'domain', 'gmail', 'icloud', 'proto')
            ) NOT ENFORCED,
            ALGORITHM=INSTANT;
    END IF;

    CREATE TABLE IF NOT EXISTS proto_resources (
        id BIGINT UNSIGNED PRIMARY KEY,
        resource_type VARCHAR(32) NOT NULL DEFAULT 'proto',
        owner_user_id BIGINT UNSIGNED NOT NULL,
        email_address VARCHAR(320) NOT NULL,
        password VARCHAR(512) NOT NULL COMMENT 'encrypted/secret; never expose in API or logs',
        for_sale TINYINT(1) NOT NULL DEFAULT 0,
        status VARCHAR(24) NOT NULL DEFAULT 'pending',
        version BIGINT UNSIGNED NOT NULL DEFAULT 1,
        validation_generation BIGINT UNSIGNED NOT NULL DEFAULT 1,
        credential_revision BIGINT UNSIGNED NOT NULL DEFAULT 1,
        credential_updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
        validation_request_id VARCHAR(64) NOT NULL DEFAULT '',
        validation_failures INT NOT NULL DEFAULT 0,
        last_safe_error VARCHAR(500) NOT NULL DEFAULT '',
        last_checked_at DATETIME NULL,
        last_allocated_at DATETIME NULL,
        created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
        updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
        UNIQUE INDEX idx_proto_resources_email (email_address),
        UNIQUE INDEX uq_proto_resources_id_owner (id, owner_user_id),
        INDEX idx_proto_resources_owner_status (owner_user_id, status),
        INDEX idx_proto_resources_sale_status (for_sale, status),
        CONSTRAINT fk_proto_resource_root FOREIGN KEY (id, resource_type) REFERENCES email_resources(id, type) ON DELETE CASCADE,
        CONSTRAINT fk_proto_resource_owner FOREIGN KEY (owner_user_id) REFERENCES users(id) ON DELETE RESTRICT,
        CONSTRAINT chk_proto_resource_type CHECK (resource_type = 'proto'),
        CONSTRAINT chk_proto_resource_status CHECK (status IN ('pending','validating','identifying','normal','abnormal','disabled','deleted')),
        CONSTRAINT chk_proto_resource_for_sale CHECK (for_sale = 0 OR status = 'normal'),
        CONSTRAINT chk_proto_resource_version CHECK (version > 0),
        CONSTRAINT chk_proto_resource_generations CHECK (validation_generation > 0 AND credential_revision > 0)
    ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

    CREATE TABLE IF NOT EXISTS proto_resource_imports (
        id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
        owner_user_id BIGINT UNSIGNED NOT NULL,
        operator_user_id BIGINT UNSIGNED NOT NULL,
        source_object_key VARCHAR(500) NOT NULL,
        failure_object_key VARCHAR(500) NOT NULL DEFAULT '',
        file_name VARCHAR(255) NOT NULL DEFAULT '',
        request_id VARCHAR(64) NOT NULL DEFAULT '',
        status VARCHAR(24) NOT NULL DEFAULT 'processing',
        error_strategy VARCHAR(16) NOT NULL DEFAULT 'skip',
        idempotency_key VARCHAR(128) NOT NULL,
        request_fingerprint CHAR(64) NOT NULL,
        generation BIGINT UNSIGNED NOT NULL DEFAULT 1,
        claim_token CHAR(36) NOT NULL DEFAULT '',
        accepted_count INT NOT NULL DEFAULT 0,
        imported_count INT NOT NULL DEFAULT 0,
        skipped_count INT NOT NULL DEFAULT 0,
        failed_count INT NOT NULL DEFAULT 0,
        dispatch_status VARCHAR(16) NOT NULL DEFAULT 'pending',
        dispatch_attempts INT NOT NULL DEFAULT 0,
        last_safe_error VARCHAR(500) NOT NULL DEFAULT '',
        started_at DATETIME NULL,
        finished_at DATETIME NULL,
        created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
        updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
        UNIQUE INDEX uq_proto_import_idempotency (operator_user_id, idempotency_key),
        INDEX idx_proto_import_status_created (status, created_at),
        CONSTRAINT fk_proto_import_owner FOREIGN KEY (owner_user_id) REFERENCES users(id) ON DELETE RESTRICT,
        CONSTRAINT fk_proto_import_operator FOREIGN KEY (operator_user_id) REFERENCES users(id) ON DELETE RESTRICT,
        CONSTRAINT chk_proto_import_status CHECK (status IN ('accepting','processing','imported','failed')),
        CONSTRAINT chk_proto_import_strategy CHECK (error_strategy IN ('skip','abort')),
        CONSTRAINT chk_proto_import_dispatch CHECK (dispatch_status IN ('pending','queued','running','done'))
    ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

    CREATE TABLE IF NOT EXISTS proto_resource_import_items (
        id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
        import_id BIGINT UNSIGNED NOT NULL,
        line_number INT NOT NULL,
        resource_id BIGINT UNSIGNED NULL,
        outcome VARCHAR(24) NOT NULL,
        category VARCHAR(64) NOT NULL DEFAULT '',
        last_safe_error VARCHAR(500) NOT NULL DEFAULT '',
        created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
        UNIQUE INDEX uq_proto_import_item_line (import_id, line_number),
        INDEX idx_proto_import_item_resource (resource_id),
        CONSTRAINT fk_proto_import_item_import FOREIGN KEY (import_id) REFERENCES proto_resource_imports(id) ON DELETE CASCADE,
        CONSTRAINT chk_proto_import_item_outcome CHECK (outcome IN ('imported','restored','skipped','failed'))
    ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

    CREATE TABLE IF NOT EXISTS proto_command_receipts (
        id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
        operator_user_id BIGINT UNSIGNED NOT NULL,
        resource_id BIGINT UNSIGNED NOT NULL DEFAULT 0,
        command VARCHAR(32) NOT NULL,
        idempotency_key VARCHAR(128) NOT NULL,
        request_fingerprint CHAR(64) NOT NULL,
        status VARCHAR(16) NOT NULL DEFAULT 'accepted',
        created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
        updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
        UNIQUE INDEX uq_proto_command_receipt (operator_user_id, resource_id, command, idempotency_key),
        INDEX idx_proto_command_receipt_resource (resource_id, command, created_at),
        CONSTRAINT fk_proto_command_receipt_operator FOREIGN KEY (operator_user_id) REFERENCES users(id) ON DELETE RESTRICT,
        CONSTRAINT chk_proto_command_receipt_status CHECK (status IN ('accepted', 'completed', 'failed'))
    ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

    CREATE TABLE IF NOT EXISTS proto_maintenance_runs (
        id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
        resource_id BIGINT UNSIGNED NOT NULL,
        validation_generation BIGINT UNSIGNED NOT NULL,
        kind VARCHAR(24) NOT NULL,
        status VARCHAR(24) NOT NULL DEFAULT 'queued',
        attempts INT NOT NULL DEFAULT 0,
        max_attempts INT NOT NULL DEFAULT 3,
        credential_revision BIGINT UNSIGNED NOT NULL,
        request_id VARCHAR(64) NOT NULL DEFAULT '',
        last_safe_error VARCHAR(500) NOT NULL DEFAULT '',
        queued_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
        started_at DATETIME NULL,
        finished_at DATETIME NULL,
        created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
        updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
        INDEX idx_proto_maintenance_resource (resource_id, validation_generation),
        INDEX idx_proto_maintenance_status (status, queued_at),
        CONSTRAINT fk_proto_maintenance_resource FOREIGN KEY (resource_id) REFERENCES proto_resources(id) ON DELETE CASCADE,
        CONSTRAINT chk_proto_maintenance_kind CHECK (kind IN ('validation','history')),
        CONSTRAINT chk_proto_maintenance_status CHECK (status IN ('queued','running','succeeded','failed','uncertain','canceled'))
    ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

    CREATE TABLE IF NOT EXISTS proto_allocations (
        id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
        order_no VARCHAR(64) NOT NULL,
        project_id BIGINT UNSIGNED NOT NULL,
        product_id BIGINT UNSIGNED NOT NULL,
        resource_id BIGINT UNSIGNED NOT NULL,
        owner_user_id BIGINT UNSIGNED NOT NULL,
        guard_type VARCHAR(32) NOT NULL DEFAULT 'proto',
        supply_scope VARCHAR(16) NOT NULL DEFAULT 'owned',
        service_mode VARCHAR(16) NOT NULL DEFAULT 'code',
        mailbox VARCHAR(16) NOT NULL DEFAULT 'main',
        email VARCHAR(255) NOT NULL,
        status VARCHAR(16) NOT NULL DEFAULT 'allocated',
        cost_points_snapshot DECIMAL(20,6) NOT NULL DEFAULT 0,
        created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
        released_at DATETIME NULL,
        UNIQUE INDEX uq_proto_allocation_order (order_no),
        active_resource_project_id BIGINT UNSIGNED GENERATED ALWAYS AS (
            CASE WHEN status = 'allocated' THEN resource_id ELSE NULL END
        ) STORED,
        active_project_id BIGINT UNSIGNED GENERATED ALWAYS AS (
            CASE WHEN status = 'allocated' THEN project_id ELSE NULL END
        ) STORED,
        UNIQUE INDEX uq_proto_allocation_active_project_resource (active_resource_project_id, active_project_id),
        INDEX idx_proto_allocation_resource_status (resource_id, status),
        INDEX idx_proto_allocation_project (project_id, status),
        INDEX idx_proto_allocation_guard (order_no, guard_type),
        INDEX idx_proto_allocation_product_project (product_id, project_id),
        CONSTRAINT fk_proto_allocation_resource FOREIGN KEY (resource_id) REFERENCES proto_resources(id) ON DELETE RESTRICT,
        CONSTRAINT fk_proto_allocation_resource_owner FOREIGN KEY (resource_id, owner_user_id) REFERENCES proto_resources(id, owner_user_id) ON DELETE RESTRICT,
        CONSTRAINT fk_proto_allocation_guard FOREIGN KEY (order_no, guard_type) REFERENCES allocation_order_guards(order_no, type) ON DELETE RESTRICT,
        CONSTRAINT fk_proto_allocation_project FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE RESTRICT,
        CONSTRAINT fk_proto_allocation_product_project FOREIGN KEY (product_id, project_id) REFERENCES project_products(id, project_id) ON DELETE RESTRICT,
        CONSTRAINT chk_proto_allocation_guard CHECK (guard_type = 'proto'),
        CONSTRAINT chk_proto_allocation_scope CHECK (supply_scope IN ('owned','public')),
        CONSTRAINT chk_proto_allocation_mode CHECK (service_mode IN ('code','purchase')),
        CONSTRAINT chk_proto_allocation_mailbox CHECK (mailbox = 'main'),
        CONSTRAINT chk_proto_allocation_status CHECK (status IN ('allocated','released'))
    ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

    SET SESSION lock_wait_timeout = previous_lock_wait_timeout;
    SET SESSION innodb_lock_wait_timeout = previous_innodb_lock_wait_timeout;
END;
-- +goose StatementEnd

CALL create_proto_mailbox_00136();
DROP PROCEDURE create_proto_mailbox_00136;

-- +goose Down
DROP TEMPORARY TABLE IF EXISTS proto_down_guard;
CREATE TEMPORARY TABLE proto_down_guard (
    unsafe_rows BIGINT NOT NULL,
    CONSTRAINT chk_proto_down_guard CHECK (unsafe_rows = 0)
);
INSERT INTO proto_down_guard (unsafe_rows)
SELECT
    (SELECT COUNT(*) FROM orders WHERE product_type = 'proto' OR allocation_type = 'proto')
    + (SELECT COUNT(*) FROM project_products WHERE type = 'proto')
    + (SELECT COUNT(*) FROM allocation_order_guards WHERE type = 'proto')
    + (SELECT COUNT(*) FROM proto_allocations)
    + (SELECT COUNT(*) FROM proto_maintenance_runs)
    + (SELECT COUNT(*) FROM proto_resource_import_items)
    + (SELECT COUNT(*) FROM proto_resource_imports)
    + (SELECT COUNT(*) FROM proto_command_receipts)
    + (SELECT COUNT(*) FROM proto_resources)
    + (SELECT COUNT(*) FROM email_resources WHERE type = 'proto');
DROP TEMPORARY TABLE proto_down_guard;

DROP TABLE IF EXISTS proto_maintenance_runs;
DROP TABLE IF EXISTS proto_allocations;
DROP TABLE IF EXISTS proto_resource_import_items;
DROP TABLE IF EXISTS proto_resource_imports;
DROP TABLE IF EXISTS proto_command_receipts;
DROP TABLE IF EXISTS proto_resources;
ALTER TABLE email_resources
    DROP CHECK chk_email_resources_type,
    ADD CONSTRAINT chk_email_resources_type CHECK (type IN ('microsoft', 'domain', 'gmail', 'icloud'));

ALTER TABLE allocation_order_guards
    DROP CHECK chk_allocation_order_guards_type,
    ADD CONSTRAINT chk_allocation_order_guards_type CHECK (
        type IN ('microsoft', 'domain', 'gmail', 'icloud')
    ) NOT ENFORCED,
    ALGORITHM=INSTANT;

ALTER TABLE orders
    DROP CHECK chk_orders_product_type,
    DROP CHECK chk_orders_allocation_shape,
    ADD CONSTRAINT chk_orders_product_type CHECK (
        product_type IN ('microsoft', 'domain', 'random', 'gmail', 'gmail_variant', 'icloud')
    ) NOT ENFORCED,
    ADD CONSTRAINT chk_orders_allocation_shape CHECK (
        allocation_type IS NULL
        OR allocation_type IN ('microsoft', 'domain', 'gmail', 'icloud')
    ) NOT ENFORCED,
    ALGORITHM=INSTANT;

ALTER TABLE project_products
    DROP CHECK chk_project_products_type,
    DROP CHECK chk_project_products_weights,
    ADD CONSTRAINT chk_project_products_type CHECK (
        type IN ('microsoft', 'domain', 'random', 'gmail', 'gmail_variant', 'icloud')
    ),
    ADD CONSTRAINT chk_project_products_weights CHECK (
        main_weight >= 0
        AND dot_weight >= 0
        AND plus_weight >= 0
        AND (type NOT IN ('microsoft', 'gmail', 'gmail_variant', 'icloud')
             OR main_weight + dot_weight + plus_weight > 0)
        AND (type <> 'domain'
             OR (main_weight = 0 AND dot_weight = 0 AND plus_weight = 0))
        AND (type <> 'random'
             OR (main_weight = 1 AND dot_weight = 1 AND plus_weight = 1))
        AND (type <> 'gmail_variant'
             OR (main_weight = 0 AND dot_weight = 0 AND plus_weight = 1))
        AND (type <> 'icloud'
             OR (main_weight > 0 AND dot_weight = 0 AND plus_weight = 0))
    );
