-- +goose Up

-- Re-entry after committed DDL must not recreate an existing Daum table.
-- Keep shared CHECK validation out of retries once Daum is already admitted.
DROP PROCEDURE IF EXISTS create_daum_mailbox_00150;

-- +goose StatementBegin
CREATE PROCEDURE create_daum_mailbox_00150()
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
          AND LOWER(check_clause) LIKE '%daum%'
    ) OR EXISTS (
        SELECT 1 FROM information_schema.table_constraints
        WHERE constraint_schema = DATABASE() AND table_name = 'email_resources'
          AND constraint_name = 'chk_email_resources_type' AND enforced = 'YES'
    ) THEN
        -- Preserve the Gmail/iCloud policy (00072/00083): metadata only, without
        -- validating every legacy root row when adding another resource type.
        ALTER TABLE email_resources
            DROP CHECK chk_email_resources_type,
            ADD CONSTRAINT chk_email_resources_type CHECK (type IN ('microsoft', 'domain', 'gmail', 'icloud', 'proto', 'daum')) NOT ENFORCED,
            ALGORITHM=INSTANT;
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM information_schema.check_constraints
        WHERE constraint_schema = DATABASE() AND constraint_name = 'chk_project_products_type'
          AND LOWER(check_clause) LIKE '%daum%'
    ) OR NOT EXISTS (
        SELECT 1 FROM information_schema.check_constraints
        WHERE constraint_schema = DATABASE() AND constraint_name = 'chk_project_products_weights'
          AND LOWER(check_clause) LIKE '%daum%'
    ) THEN
        ALTER TABLE project_products
            DROP CHECK chk_project_products_type,
            DROP CHECK chk_project_products_weights,
            ADD CONSTRAINT chk_project_products_type CHECK (
                type IN ('microsoft', 'domain', 'random', 'gmail', 'gmail_variant', 'icloud', 'proto', 'daum')
            ),
            ADD CONSTRAINT chk_project_products_weights CHECK (
                main_weight >= 0
                AND dot_weight >= 0
                AND plus_weight >= 0
                AND (type NOT IN ('microsoft', 'gmail', 'gmail_variant', 'icloud', 'proto', 'daum')
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
                AND (type <> 'daum'
                     OR (main_weight > 0 AND dot_weight = 0 AND plus_weight = 0))
            );
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM information_schema.check_constraints
        WHERE constraint_schema = DATABASE() AND constraint_name = 'chk_orders_product_type'
          AND LOWER(check_clause) LIKE '%daum%'
    ) OR NOT EXISTS (
        SELECT 1 FROM information_schema.check_constraints
        WHERE constraint_schema = DATABASE() AND constraint_name = 'chk_orders_allocation_shape'
          AND LOWER(check_clause) LIKE '%daum%'
    ) THEN
        ALTER TABLE orders
            DROP CHECK chk_orders_product_type,
            DROP CHECK chk_orders_allocation_shape,
            ADD CONSTRAINT chk_orders_product_type CHECK (
                product_type IN ('microsoft', 'domain', 'random', 'gmail', 'gmail_variant', 'icloud', 'proto', 'daum')
            ) NOT ENFORCED,
            ADD CONSTRAINT chk_orders_allocation_shape CHECK (
                allocation_type IS NULL
                OR allocation_type IN ('microsoft', 'domain', 'gmail', 'icloud', 'proto', 'daum')
            ) NOT ENFORCED,
            ALGORITHM=INSTANT;
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM information_schema.check_constraints
        WHERE constraint_schema = DATABASE() AND constraint_name = 'chk_allocation_order_guards_type'
          AND LOWER(check_clause) LIKE '%daum%'
    ) THEN
        ALTER TABLE allocation_order_guards
            DROP CHECK chk_allocation_order_guards_type,
            ADD CONSTRAINT chk_allocation_order_guards_type CHECK (
                type IN ('microsoft', 'domain', 'gmail', 'icloud', 'proto', 'daum')
            ) NOT ENFORCED,
            ALGORITHM=INSTANT;
    END IF;

    CREATE TABLE IF NOT EXISTS daum_resources (
        id BIGINT UNSIGNED PRIMARY KEY,
        resource_type VARCHAR(32) NOT NULL DEFAULT 'daum',
        owner_user_id BIGINT UNSIGNED NOT NULL,
        email_address VARCHAR(320) NOT NULL,
        password VARCHAR(512) NOT NULL DEFAULT '' COMMENT 'login password; never expose in API or logs',
        app_password VARCHAR(512) NOT NULL COMMENT 'APP password; never expose in API or logs',
        protocol VARCHAR(8) NOT NULL DEFAULT 'imap',
        email_domain VARCHAR(255) NOT NULL DEFAULT 'daum.net',
        long_lived TINYINT(1) NOT NULL DEFAULT 0,
        quality_score INT NOT NULL DEFAULT 0,
        alloc_bucket SMALLINT UNSIGNED NOT NULL DEFAULT 0,
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
        UNIQUE INDEX idx_daum_resources_email (email_address),
        UNIQUE INDEX uq_daum_resources_id_owner (id, owner_user_id),
        INDEX idx_daum_resources_owner_status (owner_user_id, status),
        INDEX idx_daum_resources_sale_status (for_sale, status),
        INDEX idx_daum_dispatch (status, updated_at, id),
        INDEX idx_daum_suffix (email_domain, status, for_sale, id),
        INDEX idx_daum_candidates (status, for_sale, alloc_bucket, last_allocated_at, quality_score, id),
        CONSTRAINT fk_daum_resource_root FOREIGN KEY (id, resource_type) REFERENCES email_resources(id, type) ON DELETE CASCADE,
        CONSTRAINT fk_daum_resource_owner FOREIGN KEY (owner_user_id) REFERENCES users(id) ON DELETE RESTRICT,
        CONSTRAINT chk_daum_resource_type CHECK (resource_type = 'daum'),
        CONSTRAINT chk_daum_resource_status CHECK (status IN ('pending','validating','identifying','normal','abnormal','disabled','deleted')),
        CONSTRAINT chk_daum_resource_protocol CHECK (protocol IN ('imap','pop3')),
        CONSTRAINT chk_daum_quality_score CHECK (quality_score BETWEEN 0 AND 100),
        CONSTRAINT chk_daum_alloc_bucket CHECK (alloc_bucket < 256),
        CONSTRAINT chk_daum_main_domain CHECK (email_domain = 'daum.net' AND SUBSTRING_INDEX(email_address, '@', -1) = 'daum.net'),
        CONSTRAINT chk_daum_resource_version CHECK (version > 0),
        CONSTRAINT chk_daum_resource_generations CHECK (validation_generation > 0 AND credential_revision > 0)
    ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

    CREATE TABLE IF NOT EXISTS daum_resource_imports (
        id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
        owner_user_id BIGINT UNSIGNED NOT NULL,
        operator_user_id BIGINT UNSIGNED NOT NULL,
        source_object_key VARCHAR(500) NOT NULL,
        failure_object_key VARCHAR(500) NOT NULL DEFAULT '',
        file_name VARCHAR(255) NOT NULL DEFAULT '',
        request_id VARCHAR(64) NOT NULL DEFAULT '',
        status VARCHAR(24) NOT NULL DEFAULT 'processing',
        error_strategy VARCHAR(16) NOT NULL DEFAULT 'skip',
        protocol VARCHAR(8) NOT NULL DEFAULT 'imap',
        long_lived TINYINT(1) NOT NULL DEFAULT 0,
        attempts INT UNSIGNED NOT NULL DEFAULT 0,
        max_attempts INT UNSIGNED NOT NULL DEFAULT 3,
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
        UNIQUE INDEX uq_daum_import_idempotency (operator_user_id, idempotency_key),
        INDEX idx_daum_import_status_created (status, created_at),
        INDEX idx_daum_import_dispatch (status, dispatch_status, generation, id),
        CONSTRAINT fk_daum_import_owner FOREIGN KEY (owner_user_id) REFERENCES users(id) ON DELETE RESTRICT,
        CONSTRAINT fk_daum_import_operator FOREIGN KEY (operator_user_id) REFERENCES users(id) ON DELETE RESTRICT,
        CONSTRAINT chk_daum_import_status CHECK (status IN ('accepting','processing','imported','failed')),
        CONSTRAINT chk_daum_import_strategy CHECK (error_strategy IN ('skip','abort')),
        CONSTRAINT chk_daum_import_dispatch CHECK (dispatch_status IN ('pending','queued','running','done','succeeded','failed'))
    ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

    CREATE TABLE IF NOT EXISTS daum_resource_import_items (
        id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
        import_id BIGINT UNSIGNED NOT NULL,
        line_number INT NOT NULL,
        resource_id BIGINT UNSIGNED NULL,
        outcome VARCHAR(24) NOT NULL,
        category VARCHAR(64) NOT NULL DEFAULT '',
        last_safe_error VARCHAR(500) NOT NULL DEFAULT '',
        created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
        UNIQUE INDEX uq_daum_import_item_line (import_id, line_number),
        INDEX idx_daum_import_item_resource (resource_id),
        CONSTRAINT chk_daum_import_line_number CHECK (line_number > 0),
        CONSTRAINT fk_daum_import_item_resource FOREIGN KEY (resource_id) REFERENCES daum_resources(id) ON DELETE RESTRICT,
        CONSTRAINT fk_daum_import_item_import FOREIGN KEY (import_id) REFERENCES daum_resource_imports(id) ON DELETE CASCADE,
        CONSTRAINT chk_daum_import_item_outcome CHECK (outcome IN ('imported','restored','skipped','failed'))
    ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

    CREATE TABLE IF NOT EXISTS daum_command_receipts (
        id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
        operator_user_id BIGINT UNSIGNED NOT NULL,
        resource_id BIGINT UNSIGNED NOT NULL DEFAULT 0,
        command VARCHAR(32) NOT NULL,
        idempotency_key VARCHAR(128) NOT NULL,
        request_fingerprint CHAR(64) NOT NULL,
        status VARCHAR(16) NOT NULL DEFAULT 'processing',
        reservation_token CHAR(36) NOT NULL DEFAULT '',
        result_json JSON NULL,
        created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
        updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
        UNIQUE INDEX uq_daum_command_receipt (operator_user_id, idempotency_key),
        INDEX idx_daum_command_receipt_resource (resource_id, command, created_at),
        CONSTRAINT fk_daum_command_receipt_operator FOREIGN KEY (operator_user_id) REFERENCES users(id) ON DELETE RESTRICT,
        CONSTRAINT chk_daum_command_receipt_status CHECK (status IN ('processing', 'succeeded', 'failed'))
    ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

    CREATE TABLE IF NOT EXISTS daum_maintenance_runs (
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
        UNIQUE INDEX uq_daum_maintenance_generation (resource_id, validation_generation, kind),
        INDEX idx_daum_maintenance_resource (resource_id, validation_generation),
        INDEX idx_daum_maintenance_status (status, queued_at),
        CONSTRAINT fk_daum_maintenance_resource FOREIGN KEY (resource_id) REFERENCES daum_resources(id) ON DELETE CASCADE,
        CONSTRAINT chk_daum_maintenance_kind CHECK (kind IN ('validation','history')),
        CONSTRAINT chk_daum_maintenance_status CHECK (status IN ('queued','running','succeeded','failed','uncertain','canceled'))
    ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

    CREATE TABLE IF NOT EXISTS daum_explicit_aliases (
        id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
        resource_id BIGINT UNSIGNED NOT NULL,
        owner_user_id BIGINT UNSIGNED NOT NULL DEFAULT 1,
        email_address VARCHAR(255) NOT NULL,
        email_domain VARCHAR(255) NOT NULL,
        status VARCHAR(24) NOT NULL DEFAULT 'normal',
        version BIGINT UNSIGNED NOT NULL DEFAULT 1,
        created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
        updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
        UNIQUE INDEX uq_daum_alias_email (email_address),
        UNIQUE INDEX uq_daum_alias_id_resource (id, resource_id),
        UNIQUE INDEX uq_daum_alias_suffix (resource_id, email_domain),
        INDEX idx_daum_alias_suffix (email_domain, status, resource_id),
        CONSTRAINT fk_daum_alias_resource FOREIGN KEY (resource_id) REFERENCES daum_resources(id) ON DELETE RESTRICT,
        CONSTRAINT fk_daum_alias_owner FOREIGN KEY (owner_user_id) REFERENCES users(id) ON DELETE RESTRICT,
        CONSTRAINT chk_daum_alias_owner CHECK (owner_user_id = 1),
        CONSTRAINT chk_daum_alias_suffix CHECK (email_domain IN ('hanmail.net','hanmail2.net') AND SUBSTRING_INDEX(email_address, '@', -1) = email_domain),
        CONSTRAINT chk_daum_alias_status CHECK (status IN ('normal','disabled'))
    ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

    CREATE TABLE IF NOT EXISTS daum_binding_mailboxes (
        id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
        resource_id BIGINT UNSIGNED NOT NULL,
        resource_type VARCHAR(32) NOT NULL DEFAULT 'daum',
        owner_user_id BIGINT UNSIGNED NOT NULL,
        account_email VARCHAR(255) NOT NULL,
        binding_address VARCHAR(320) NOT NULL,
        pickup_url TEXT NOT NULL COMMENT 'secret remail capability; write-only',
        domain_resource_id BIGINT UNSIGNED NULL,
        mailbox_resource_id BIGINT UNSIGNED NULL,
        order_no VARCHAR(64) NOT NULL DEFAULT '',
        status VARCHAR(24) NOT NULL DEFAULT 'unlinked',
        last_safe_error VARCHAR(500) NOT NULL DEFAULT '',
        created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
        updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
        UNIQUE INDEX uq_daum_binding_resource (resource_id),
        INDEX idx_daum_binding_address (binding_address),
        INDEX idx_daum_binding_domain (domain_resource_id, status),
        CONSTRAINT fk_daum_binding_resource FOREIGN KEY (resource_id) REFERENCES daum_resources(id) ON DELETE RESTRICT,
        CONSTRAINT fk_daum_binding_owner FOREIGN KEY (owner_user_id) REFERENCES users(id) ON DELETE RESTRICT,
        CONSTRAINT fk_daum_binding_domain FOREIGN KEY (domain_resource_id) REFERENCES domain_resources(id) ON DELETE RESTRICT,
        CONSTRAINT fk_daum_binding_mailbox FOREIGN KEY (mailbox_resource_id) REFERENCES email_resources(id) ON DELETE RESTRICT,
        CONSTRAINT chk_daum_binding_type CHECK (resource_type = 'daum'),
        CONSTRAINT chk_daum_binding_status CHECK (status IN ('linked','unlinked','conflict','unavailable'))
    ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

    CREATE TABLE IF NOT EXISTS project_daum_suffix_blacklists (
        project_id BIGINT UNSIGNED NOT NULL,
        suffix VARCHAR(255) NOT NULL,
        PRIMARY KEY (project_id, suffix),
        CONSTRAINT fk_project_daum_suffix_blacklists_project FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE,
        CONSTRAINT chk_project_daum_suffix CHECK (suffix IN ('daum.net','hanmail.net','hanmail2.net'))
    ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

    CREATE TABLE IF NOT EXISTS daum_project_history_scan_states (
        project_id BIGINT UNSIGNED PRIMARY KEY,
        generation BIGINT UNSIGNED NOT NULL DEFAULT 1,
        status VARCHAR(24) NOT NULL DEFAULT 'pending',
        after_id BIGINT UNSIGNED NOT NULL DEFAULT 0,
        through_id BIGINT UNSIGNED NOT NULL DEFAULT 0,
        failures TINYINT UNSIGNED NOT NULL DEFAULT 0,
        scanned_count INT UNSIGNED NOT NULL DEFAULT 0,
        matched_count INT UNSIGNED NOT NULL DEFAULT 0,
        skipped_count INT UNSIGNED NOT NULL DEFAULT 0,
        request_id VARCHAR(64) NOT NULL DEFAULT '',
        last_safe_error VARCHAR(500) NOT NULL DEFAULT '',
        requested_at DATETIME(3) NULL,
        started_at DATETIME(3) NULL,
        finished_at DATETIME(3) NULL,
        created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
        updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
        INDEX idx_daum_project_history_dispatch (status, requested_at, project_id),
        CONSTRAINT fk_daum_project_history_project FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE,
        CONSTRAINT chk_daum_project_history_status CHECK (status IN ('pending','processing','normal','abnormal','uncertain')),
        CONSTRAINT chk_daum_project_history_generation CHECK (generation > 0)
    ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;


    CREATE TABLE IF NOT EXISTS daum_allocations (
        id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
        order_no VARCHAR(64) NOT NULL,
        project_id BIGINT UNSIGNED NOT NULL,
        product_id BIGINT UNSIGNED NOT NULL,
        resource_id BIGINT UNSIGNED NOT NULL,
        owner_user_id BIGINT UNSIGNED NOT NULL,
        guard_type VARCHAR(32) NOT NULL DEFAULT 'daum',
        supply_scope VARCHAR(16) NOT NULL DEFAULT 'owned',
        service_mode VARCHAR(16) NOT NULL DEFAULT 'code',
        mailbox VARCHAR(16) NOT NULL DEFAULT 'main',
        explicit_alias_id BIGINT UNSIGNED NULL,
        email VARCHAR(255) NOT NULL,
        status VARCHAR(16) NOT NULL DEFAULT 'allocated',
        cost_points_snapshot DECIMAL(20,6) NOT NULL DEFAULT 0,
        created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
        released_at DATETIME NULL,
        UNIQUE INDEX uq_daum_allocation_order (order_no),
        active_project_id BIGINT UNSIGNED GENERATED ALWAYS AS (CASE WHEN status = 'allocated' THEN project_id ELSE NULL END) STORED,
        active_email VARCHAR(255) GENERATED ALWAYS AS (CASE WHEN status = 'allocated' THEN email ELSE NULL END) STORED,
        UNIQUE INDEX uq_daum_allocation_active_project_email (active_project_id, active_email),
        INDEX idx_daum_allocation_history (resource_id, project_id, email, id),
        INDEX idx_daum_allocation_resource_status (resource_id, status),
        INDEX idx_daum_allocation_project (project_id, status),
        INDEX idx_daum_allocation_guard (order_no, guard_type),
        INDEX idx_daum_allocation_product_project (product_id, project_id),
        CONSTRAINT fk_daum_allocation_resource FOREIGN KEY (resource_id) REFERENCES daum_resources(id) ON DELETE RESTRICT,
        CONSTRAINT fk_daum_allocation_owner FOREIGN KEY (owner_user_id) REFERENCES users(id) ON DELETE RESTRICT,
        CONSTRAINT fk_daum_allocation_alias FOREIGN KEY (explicit_alias_id, resource_id) REFERENCES daum_explicit_aliases(id, resource_id) ON DELETE RESTRICT,
        CONSTRAINT fk_daum_allocation_guard FOREIGN KEY (order_no, guard_type) REFERENCES allocation_order_guards(order_no, type) ON DELETE RESTRICT,
        CONSTRAINT fk_daum_allocation_project FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE RESTRICT,
        CONSTRAINT fk_daum_allocation_product_project FOREIGN KEY (product_id, project_id) REFERENCES project_products(id, project_id) ON DELETE RESTRICT,
        CONSTRAINT chk_daum_allocation_guard CHECK (guard_type = 'daum'),
        CONSTRAINT chk_daum_allocation_scope CHECK (supply_scope IN ('owned','public')),
        CONSTRAINT chk_daum_allocation_mode CHECK (service_mode IN ('code','purchase')),
        CONSTRAINT chk_daum_allocation_mailbox CHECK ((mailbox = 'main' AND explicit_alias_id IS NULL) OR (mailbox = 'alias' AND explicit_alias_id IS NOT NULL)),
        CONSTRAINT chk_daum_allocation_status CHECK (status IN ('allocated','released'))
    ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

    IF NOT EXISTS (SELECT 1 FROM information_schema.check_constraints WHERE constraint_schema = DATABASE() AND constraint_name = 'chk_mailmatch_messages_resource_type' AND LOWER(check_clause) LIKE '%daum%') THEN
        ALTER TABLE mailmatch_messages DROP CHECK chk_mailmatch_messages_resource_type,
            ADD CONSTRAINT chk_mailmatch_messages_resource_type CHECK (resource_type IN ('microsoft','domain','gmail','icloud','proto','daum')) NOT ENFORCED, ALGORITHM=INSTANT;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM information_schema.check_constraints WHERE constraint_schema = DATABASE() AND constraint_name = 'chk_mailmatch_admin_fetch_operation' AND LOWER(check_clause) LIKE '%daum_resource_fetch%') THEN
        ALTER TABLE mailmatch_admin_resource_fetch_states DROP CHECK chk_mailmatch_admin_fetch_operation,
            ADD CONSTRAINT chk_mailmatch_admin_fetch_operation CHECK (operation_kind IN ('resource_fetch','gmail_resource_fetch','icloud_resource_fetch','proto_resource_fetch','daum_resource_fetch'));
    END IF;

    SET SESSION lock_wait_timeout = previous_lock_wait_timeout;
    SET SESSION innodb_lock_wait_timeout = previous_innodb_lock_wait_timeout;
END;
-- +goose StatementEnd

CALL create_daum_mailbox_00150();
DROP PROCEDURE create_daum_mailbox_00150;

-- +goose Down

-- Business facts and secrets must be explicitly archived before removing Daum.
DROP PROCEDURE IF EXISTS refuse_daum_rollback_00150;
-- +goose StatementBegin
CREATE PROCEDURE refuse_daum_rollback_00150()
BEGIN
    SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'Daum rollback requires an explicit data-preserving migration; automatic removal is disabled';
END;
-- +goose StatementEnd
CALL refuse_daum_rollback_00150();
