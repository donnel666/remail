-- +goose Up

-- MySQL commits DDL statement by statement. Keep this migration resumable when
-- the schema changed but Goose could not record version 77.
DROP PROCEDURE IF EXISTS migrate_gmail_unified_allocations_00077;

-- +goose StatementBegin
CREATE PROCEDURE migrate_gmail_unified_allocations_00077()
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
    SET SESSION lock_wait_timeout = 20;
    SET SESSION innodb_lock_wait_timeout = 20;

    IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = 'projects' AND column_name = 'gmail_history_scan_status') THEN
        ALTER TABLE projects ADD COLUMN gmail_history_scan_status VARCHAR(32) NOT NULL DEFAULT 'normal'
            COMMENT 'pending|processing|normal|abnormal' AFTER history_scan_finished_at;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = 'projects' AND column_name = 'gmail_history_scan_generation') THEN
        ALTER TABLE projects ADD COLUMN gmail_history_scan_generation BIGINT UNSIGNED NOT NULL DEFAULT 0 AFTER gmail_history_scan_status;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = 'projects' AND column_name = 'gmail_history_scan_failures') THEN
        ALTER TABLE projects ADD COLUMN gmail_history_scan_failures TINYINT UNSIGNED NOT NULL DEFAULT 0 AFTER gmail_history_scan_generation;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = 'projects' AND column_name = 'gmail_history_scan_scanned_count') THEN
        ALTER TABLE projects ADD COLUMN gmail_history_scan_scanned_count INT UNSIGNED NOT NULL DEFAULT 0 AFTER gmail_history_scan_failures;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = 'projects' AND column_name = 'gmail_history_scan_matched_count') THEN
        ALTER TABLE projects ADD COLUMN gmail_history_scan_matched_count INT UNSIGNED NOT NULL DEFAULT 0 AFTER gmail_history_scan_scanned_count;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = 'projects' AND column_name = 'gmail_history_scan_skipped_count') THEN
        ALTER TABLE projects ADD COLUMN gmail_history_scan_skipped_count INT UNSIGNED NOT NULL DEFAULT 0 AFTER gmail_history_scan_matched_count;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = 'projects' AND column_name = 'gmail_history_scan_request_id') THEN
        ALTER TABLE projects ADD COLUMN gmail_history_scan_request_id VARCHAR(64) NOT NULL DEFAULT '' AFTER gmail_history_scan_skipped_count;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = 'projects' AND column_name = 'gmail_history_scan_last_safe_error') THEN
        ALTER TABLE projects ADD COLUMN gmail_history_scan_last_safe_error VARCHAR(500) NOT NULL DEFAULT '' AFTER gmail_history_scan_request_id;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = 'projects' AND column_name = 'gmail_history_scan_requested_at') THEN
        ALTER TABLE projects ADD COLUMN gmail_history_scan_requested_at DATETIME(3) NULL AFTER gmail_history_scan_last_safe_error;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = 'projects' AND column_name = 'gmail_history_scan_started_at') THEN
        ALTER TABLE projects ADD COLUMN gmail_history_scan_started_at DATETIME(3) NULL AFTER gmail_history_scan_requested_at;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = 'projects' AND column_name = 'gmail_history_scan_finished_at') THEN
        ALTER TABLE projects ADD COLUMN gmail_history_scan_finished_at DATETIME(3) NULL AFTER gmail_history_scan_started_at;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM information_schema.statistics WHERE table_schema = DATABASE() AND table_name = 'projects' AND index_name = 'idx_projects_gmail_history_scan_pending') THEN
        ALTER TABLE projects ADD INDEX idx_projects_gmail_history_scan_pending
            (gmail_history_scan_status, gmail_history_scan_requested_at, id);
    END IF;
    IF NOT EXISTS (SELECT 1 FROM information_schema.table_constraints WHERE constraint_schema = DATABASE() AND table_name = 'projects' AND constraint_name = 'chk_projects_gmail_history_scan_status') THEN
        ALTER TABLE projects ADD CONSTRAINT chk_projects_gmail_history_scan_status
            CHECK (gmail_history_scan_status IN ('pending', 'processing', 'normal', 'abnormal'));
    END IF;
    IF NOT EXISTS (SELECT 1 FROM information_schema.table_constraints WHERE constraint_schema = DATABASE() AND table_name = 'projects' AND constraint_name = 'chk_projects_gmail_history_scan_failures') THEN
        ALTER TABLE projects ADD CONSTRAINT chk_projects_gmail_history_scan_failures
            CHECK (gmail_history_scan_failures <= 3);
    END IF;

    IF NOT EXISTS (
        SELECT 1
        FROM information_schema.table_constraints AS tc
        JOIN information_schema.check_constraints AS cc
          ON cc.constraint_schema = tc.constraint_schema
         AND cc.constraint_name = tc.constraint_name
        WHERE tc.constraint_schema = DATABASE()
          AND tc.table_name = 'allocation_order_guards'
          AND tc.constraint_name = 'chk_allocation_order_guards_type'
          AND LOWER(cc.check_clause) LIKE '%gmail%'
          AND tc.enforced = 'NO'
    ) THEN
        IF EXISTS (
            SELECT 1 FROM information_schema.table_constraints
            WHERE constraint_schema = DATABASE()
              AND table_name = 'allocation_order_guards'
              AND constraint_name = 'chk_allocation_order_guards_type'
        ) THEN
            ALTER TABLE allocation_order_guards
                DROP CHECK chk_allocation_order_guards_type;
        END IF;
        ALTER TABLE allocation_order_guards
            ADD CONSTRAINT chk_allocation_order_guards_type
                CHECK (type IN ('microsoft', 'domain', 'gmail')) NOT ENFORCED,
            ALGORITHM=INSTANT;
    END IF;

    INSERT IGNORE INTO allocation_order_guards(order_no, type)
    SELECT order_no, 'gmail'
    FROM gmail_allocations;

    IF EXISTS (
        SELECT 1
        FROM gmail_allocations AS ga
        LEFT JOIN allocation_order_guards AS g
          ON g.order_no = ga.order_no AND g.type = 'gmail'
        WHERE g.order_no IS NULL
    ) THEN
        SIGNAL SQLSTATE '45000'
            SET MESSAGE_TEXT = 'migration 00077 found a conflicting Gmail allocation guard';
    END IF;

    IF EXISTS (
        SELECT 1
        FROM information_schema.table_constraints AS tc
        JOIN information_schema.check_constraints AS cc
          ON cc.constraint_schema = tc.constraint_schema
         AND cc.constraint_name = tc.constraint_name
        WHERE tc.constraint_schema = DATABASE()
          AND tc.table_name = 'gmail_resources'
          AND tc.constraint_name = 'chk_gmail_resources_status'
          AND (
              LOWER(cc.check_clause) NOT LIKE '%pending%'
              OR LOWER(cc.check_clause) NOT LIKE '%identifying%'
              OR LOWER(cc.check_clause) NOT LIKE '%available%'
              OR LOWER(cc.check_clause) NOT LIKE '%deleted%'
          )
    ) THEN
        ALTER TABLE gmail_resources DROP CHECK chk_gmail_resources_status;
    END IF;

    -- Keep healthy rows readable by the previous image during deploy rollback.
    -- A partially applied earlier attempt may already have renamed them.
    UPDATE gmail_resources SET status = 'available' WHERE status = 'normal';
    UPDATE gmail_resources AS gr
    SET gr.status = 'abnormal'
    WHERE gr.status IN ('leased', 'sold')
      AND NOT EXISTS (
          SELECT 1 FROM gmail_allocations AS ga WHERE ga.resource_id = gr.id
      );

    IF EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = DATABASE()
          AND table_name = 'gmail_resources'
          AND column_name = 'status'
          AND (
              is_nullable <> 'NO'
              OR column_default IS NULL
              OR column_default <> 'available'
              OR LOWER(column_comment) NOT LIKE '%identifying%'
          )
    ) THEN
        ALTER TABLE gmail_resources
            MODIFY COLUMN status VARCHAR(24) NOT NULL DEFAULT 'available'
                COMMENT 'available|disabled|leased|sold|pending|validating|identifying|normal|abnormal|deleted';
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM information_schema.table_constraints
        WHERE constraint_schema = DATABASE()
          AND table_name = 'gmail_resources'
          AND constraint_name = 'chk_gmail_resources_status'
    ) THEN
        ALTER TABLE gmail_resources
            ADD CONSTRAINT chk_gmail_resources_status CHECK (
                status IN ('available', 'disabled', 'leased', 'sold',
                           'pending', 'validating', 'identifying', 'normal', 'abnormal', 'deleted')
            );
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = DATABASE()
          AND table_name = 'gmail_resources'
          AND column_name = 'for_sale'
    ) THEN
        ALTER TABLE gmail_resources
            ADD COLUMN for_sale TINYINT(1) NOT NULL DEFAULT 0 AFTER app_password;
        -- Preserve the supply behavior of Gmail rows that existed before this
        -- migration. New imports remain private by default, like Microsoft.
        UPDATE gmail_resources SET for_sale = TRUE;
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = DATABASE()
          AND table_name = 'gmail_resources'
          AND column_name = 'alloc_bucket'
    ) THEN
        ALTER TABLE gmail_resources
            ADD COLUMN alloc_bucket SMALLINT UNSIGNED NOT NULL DEFAULT 0 AFTER status;
    END IF;
    UPDATE gmail_resources SET alloc_bucket = MOD(id, 2048)
    WHERE alloc_bucket <> MOD(id, 2048);

    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = DATABASE()
          AND table_name = 'gmail_resources'
          AND column_name = 'last_allocated_at'
    ) THEN
        ALTER TABLE gmail_resources
            ADD COLUMN last_allocated_at DATETIME(3) NULL AFTER alloc_bucket;
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = DATABASE()
          AND table_name = 'gmail_resources'
          AND column_name = 'credential_revision'
    ) THEN
        ALTER TABLE gmail_resources
            ADD COLUMN credential_revision BIGINT UNSIGNED NOT NULL DEFAULT 1 AFTER app_password;
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = DATABASE()
          AND table_name = 'gmail_resources'
          AND column_name = 'credential_updated_at'
    ) THEN
        ALTER TABLE gmail_resources
            ADD COLUMN credential_updated_at DATETIME(3) NULL AFTER credential_revision;
    END IF;
    UPDATE gmail_resources
    SET credential_revision = 1
    WHERE credential_revision < 1;
    UPDATE gmail_resources
    SET credential_updated_at = updated_at
    WHERE credential_updated_at IS NULL;
    IF EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = DATABASE()
          AND table_name = 'gmail_resources'
          AND column_name = 'credential_updated_at'
          AND (is_nullable <> 'NO' OR column_default IS NULL)
    ) THEN
        ALTER TABLE gmail_resources
            MODIFY COLUMN credential_updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3);
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = DATABASE()
          AND table_name = 'gmail_resources'
          AND column_name = 'validation_generation'
    ) THEN
        ALTER TABLE gmail_resources
            ADD COLUMN validation_generation BIGINT UNSIGNED NOT NULL DEFAULT 1 AFTER status;
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = DATABASE()
          AND table_name = 'gmail_resources'
          AND column_name = 'validation_failures'
    ) THEN
        ALTER TABLE gmail_resources
            ADD COLUMN validation_failures TINYINT UNSIGNED NOT NULL DEFAULT 0 AFTER validation_generation;
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = DATABASE()
          AND table_name = 'gmail_resources'
          AND column_name = 'validation_request_id'
    ) THEN
        ALTER TABLE gmail_resources
            ADD COLUMN validation_request_id VARCHAR(64) NOT NULL DEFAULT '' AFTER validation_failures;
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = DATABASE()
          AND table_name = 'gmail_resources'
          AND column_name = 'validation_command_hash'
    ) THEN
        ALTER TABLE gmail_resources
            ADD COLUMN validation_command_hash CHAR(64) NOT NULL DEFAULT '' AFTER validation_request_id;
    END IF;
    UPDATE gmail_resources
    SET validation_generation = 1
    WHERE validation_generation < 1;
    UPDATE gmail_resources
    SET validation_failures = 3
    WHERE validation_failures > 3;
    UPDATE gmail_resources
    SET status = 'pending', validation_generation = validation_generation + 1
    WHERE status = 'validating';

    IF NOT EXISTS (
        SELECT 1 FROM information_schema.statistics
        WHERE table_schema = DATABASE()
          AND table_name = 'gmail_resources'
          AND index_name = 'idx_gmail_resources_alloc_public'
    ) THEN
        ALTER TABLE gmail_resources
            ADD INDEX idx_gmail_resources_alloc_public
                (alloc_bucket, for_sale, status, last_allocated_at, id);
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.statistics
        WHERE table_schema = DATABASE()
          AND table_name = 'gmail_resources'
          AND index_name = 'idx_gmail_resources_alloc_owned'
    ) THEN
        ALTER TABLE gmail_resources
            ADD INDEX idx_gmail_resources_alloc_owned
                (owner_user_id, alloc_bucket, for_sale, status, last_allocated_at, id);
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.statistics
        WHERE table_schema = DATABASE()
          AND table_name = 'gmail_resources'
          AND index_name = 'idx_gmail_resources_validation_pending'
    ) THEN
        ALTER TABLE gmail_resources
            ADD INDEX idx_gmail_resources_validation_pending
                (status, updated_at, id);
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM information_schema.table_constraints
        WHERE constraint_schema = DATABASE()
          AND table_name = 'gmail_resources'
          AND constraint_name = 'chk_gmail_resources_validation_failures'
    ) THEN
        ALTER TABLE gmail_resources
            ADD CONSTRAINT chk_gmail_resources_validation_failures
                CHECK (validation_failures <= 3);
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.table_constraints
        WHERE constraint_schema = DATABASE()
          AND table_name = 'gmail_resources'
          AND constraint_name = 'chk_gmail_resources_validation_generation'
    ) THEN
        ALTER TABLE gmail_resources
            ADD CONSTRAINT chk_gmail_resources_validation_generation
                CHECK (validation_generation > 0);
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.table_constraints
        WHERE constraint_schema = DATABASE()
          AND table_name = 'gmail_resources'
          AND constraint_name = 'chk_gmail_resources_credential_revision'
    ) THEN
        ALTER TABLE gmail_resources
            ADD CONSTRAINT chk_gmail_resources_credential_revision
                CHECK (credential_revision > 0);
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = DATABASE()
          AND table_name = 'gmail_code_sessions'
          AND column_name = 'service_mode'
    ) THEN
        ALTER TABLE gmail_code_sessions
            ADD COLUMN service_mode VARCHAR(32) NOT NULL DEFAULT 'code'
                COMMENT 'code|purchase' AFTER provider_service_code;
    END IF;

    UPDATE gmail_code_sessions
    SET service_mode = 'code'
    WHERE service_mode IS NULL OR service_mode = '';

    IF EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = DATABASE()
          AND table_name = 'gmail_code_sessions'
          AND column_name = 'service_mode'
          AND (is_nullable <> 'NO' OR column_default IS NULL OR column_default <> 'code')
    ) THEN
        ALTER TABLE gmail_code_sessions
            MODIFY COLUMN service_mode VARCHAR(32) NOT NULL DEFAULT 'code'
                COMMENT 'code|purchase';
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = DATABASE()
          AND table_name = 'gmail_code_sessions'
          AND column_name = 'provider_cursor'
    ) THEN
        ALTER TABLE gmail_code_sessions
            ADD COLUMN provider_cursor BIGINT UNSIGNED NOT NULL DEFAULT 0
                AFTER pending_remote_action;
    END IF;

    UPDATE gmail_code_sessions
    SET provider_cursor = 0
    WHERE provider_cursor IS NULL;

    IF EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = DATABASE()
          AND table_name = 'gmail_code_sessions'
          AND column_name = 'provider_cursor'
          AND (is_nullable <> 'NO' OR column_default IS NULL OR column_default <> '0')
    ) THEN
        ALTER TABLE gmail_code_sessions
            MODIFY COLUMN provider_cursor BIGINT UNSIGNED NOT NULL DEFAULT 0;
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = DATABASE()
          AND table_name = 'gmail_code_sessions'
          AND column_name = 'provider_spam_cursor'
    ) THEN
        ALTER TABLE gmail_code_sessions
            ADD COLUMN provider_spam_cursor BIGINT UNSIGNED NOT NULL DEFAULT 0
                AFTER provider_cursor;
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM information_schema.table_constraints
        WHERE constraint_schema = DATABASE()
          AND table_name = 'gmail_code_sessions'
          AND constraint_name = 'chk_gmail_code_sessions_mode'
    ) THEN
        ALTER TABLE gmail_code_sessions
            ADD CONSTRAINT chk_gmail_code_sessions_mode
                CHECK (service_mode IN ('code', 'purchase'));
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = DATABASE()
          AND table_name = 'gmail_allocations'
          AND column_name = 'guard_type'
    ) THEN
        ALTER TABLE gmail_allocations
            ADD COLUMN guard_type VARCHAR(32) NOT NULL DEFAULT 'gmail' AFTER order_no;
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = DATABASE()
          AND table_name = 'gmail_allocations'
          AND column_name = 'project_id'
    ) THEN
        ALTER TABLE gmail_allocations
            ADD COLUMN project_id BIGINT UNSIGNED NULL AFTER guard_type;
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = DATABASE()
          AND table_name = 'gmail_allocations'
          AND column_name = 'product_id'
    ) THEN
        ALTER TABLE gmail_allocations
            ADD COLUMN product_id BIGINT UNSIGNED NULL AFTER project_id;
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = DATABASE()
          AND table_name = 'gmail_allocations'
          AND column_name = 'provider_cursor'
    ) THEN
        ALTER TABLE gmail_allocations
            ADD COLUMN provider_cursor BIGINT UNSIGNED NOT NULL DEFAULT 0
                AFTER source_ref;
    END IF;

    UPDATE gmail_allocations
    SET provider_cursor = 0
    WHERE provider_cursor IS NULL;

    IF EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = DATABASE()
          AND table_name = 'gmail_allocations'
          AND column_name = 'provider_cursor'
          AND (is_nullable <> 'NO' OR column_default IS NULL OR column_default <> '0')
    ) THEN
        ALTER TABLE gmail_allocations
            MODIFY COLUMN provider_cursor BIGINT UNSIGNED NOT NULL DEFAULT 0;
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = DATABASE()
          AND table_name = 'gmail_allocations'
          AND column_name = 'provider_spam_cursor'
    ) THEN
        ALTER TABLE gmail_allocations
            ADD COLUMN provider_spam_cursor BIGINT UNSIGNED NOT NULL DEFAULT 0
                AFTER provider_cursor;
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = DATABASE()
          AND table_name = 'gmail_allocations'
          AND column_name = 'supply_scope'
    ) THEN
        ALTER TABLE gmail_allocations
            ADD COLUMN supply_scope VARCHAR(16) NOT NULL DEFAULT 'public' AFTER resource_id;
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = DATABASE()
          AND table_name = 'gmail_allocations'
          AND column_name = 'mailbox'
    ) THEN
        ALTER TABLE gmail_allocations
            ADD COLUMN mailbox VARCHAR(16) NOT NULL DEFAULT 'main'
                COMMENT 'main|dot|plus' AFTER supply_scope;
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = DATABASE()
          AND table_name = 'gmail_allocations'
          AND column_name = 'status'
    ) THEN
        ALTER TABLE gmail_allocations
            ADD COLUMN status VARCHAR(32) NOT NULL DEFAULT 'allocated' AFTER email;
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = DATABASE()
          AND table_name = 'gmail_allocations'
          AND column_name = 'released_at'
    ) THEN
        ALTER TABLE gmail_allocations
            ADD COLUMN released_at DATETIME(3) NULL AFTER created_at;
    END IF;

    UPDATE gmail_allocations AS ga
    JOIN orders AS o ON o.order_no = ga.order_no
    SET ga.guard_type = 'gmail',
        ga.project_id = o.project_id,
        ga.product_id = o.project_product_id,
        ga.supply_scope = CASE
            WHEN ga.supply_scope IN ('owned', 'public') THEN ga.supply_scope
            ELSE 'public'
        END,
        ga.status = CASE
            WHEN ga.status IN ('allocated', 'released') THEN ga.status
            ELSE 'allocated'
        END,
        ga.mailbox = CASE
            WHEN ga.mailbox IN ('main', 'dot', 'plus') THEN ga.mailbox
            ELSE 'main'
        END;

    IF EXISTS (
        SELECT 1
        FROM gmail_allocations
        WHERE project_id IS NULL OR product_id IS NULL
    ) THEN
        SIGNAL SQLSTATE '45000'
            SET MESSAGE_TEXT = 'migration 00077 could not resolve Gmail allocation project/product ownership';
    END IF;

    -- Expand phase: nullable columns keep the previous image's INSERT shape
    -- valid. Existing and new code paths still populate both values.
    IF EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = DATABASE()
          AND table_name = 'gmail_allocations'
          AND column_name = 'project_id'
          AND is_nullable = 'NO'
    ) THEN
        ALTER TABLE gmail_allocations
            MODIFY COLUMN project_id BIGINT UNSIGNED NULL;
    END IF;

    IF EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = DATABASE()
          AND table_name = 'gmail_allocations'
          AND column_name = 'product_id'
          AND is_nullable = 'NO'
    ) THEN
        ALTER TABLE gmail_allocations
            MODIFY COLUMN product_id BIGINT UNSIGNED NULL;
    END IF;

    IF EXISTS (
        SELECT 1 FROM information_schema.statistics
        WHERE table_schema = DATABASE()
          AND table_name = 'gmail_allocations'
          AND index_name = 'idx_gmail_allocations_resource_project'
    ) AND NOT EXISTS (
        SELECT 1 FROM information_schema.statistics
        WHERE table_schema = DATABASE()
          AND table_name = 'gmail_allocations'
          AND index_name = 'idx_gmail_allocations_resource_project'
          AND seq_in_index = 3
          AND column_name = 'mailbox'
    ) THEN
        ALTER TABLE gmail_allocations
            DROP INDEX idx_gmail_allocations_resource_project;
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM information_schema.statistics
        WHERE table_schema = DATABASE()
          AND table_name = 'gmail_allocations'
          AND index_name = 'idx_gmail_allocations_guard_type'
    ) THEN
        ALTER TABLE gmail_allocations
            ADD INDEX idx_gmail_allocations_guard_type (order_no, guard_type);
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM information_schema.statistics
        WHERE table_schema = DATABASE()
          AND table_name = 'gmail_allocations'
          AND index_name = 'idx_gmail_allocations_product_project'
    ) THEN
        ALTER TABLE gmail_allocations
            ADD INDEX idx_gmail_allocations_product_project (product_id, project_id);
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM information_schema.statistics
        WHERE table_schema = DATABASE()
          AND table_name = 'gmail_allocations'
          AND index_name = 'idx_gmail_allocations_project_created'
    ) THEN
        ALTER TABLE gmail_allocations
            ADD INDEX idx_gmail_allocations_project_created (project_id, created_at, id);
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM information_schema.statistics
        WHERE table_schema = DATABASE()
          AND table_name = 'gmail_allocations'
          AND index_name = 'idx_gmail_allocations_resource_project'
    ) THEN
        ALTER TABLE gmail_allocations
            ADD INDEX idx_gmail_allocations_resource_project (resource_id, project_id, mailbox);
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM information_schema.statistics
        WHERE table_schema = DATABASE()
          AND table_name = 'gmail_allocations'
          AND index_name = 'idx_gmail_allocations_project_mailbox_email'
    ) THEN
        ALTER TABLE gmail_allocations
            ADD INDEX idx_gmail_allocations_project_mailbox_email (project_id, mailbox, email);
    END IF;

    IF EXISTS (
        SELECT 1 FROM information_schema.statistics
        WHERE table_schema = DATABASE()
          AND table_name = 'gmail_allocations'
          AND index_name = 'idx_gmail_allocations_active_resource'
    ) THEN
        ALTER TABLE gmail_allocations
            DROP INDEX idx_gmail_allocations_active_resource;
    END IF;

    IF EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = DATABASE()
          AND table_name = 'gmail_allocations'
          AND column_name = 'active_resource_id'
    ) THEN
        ALTER TABLE gmail_allocations
            DROP COLUMN active_resource_id;
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = DATABASE()
          AND table_name = 'gmail_allocations'
          AND column_name = 'active_main_resource_id'
    ) THEN
        ALTER TABLE gmail_allocations
            ADD COLUMN active_main_resource_id BIGINT UNSIGNED GENERATED ALWAYS AS (
                CASE
                    WHEN status = 'allocated' AND mailbox = 'main' AND resource_id IS NOT NULL THEN resource_id
                    ELSE NULL
                END
            ) STORED AFTER status;
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = DATABASE()
          AND table_name = 'gmail_allocations'
          AND column_name = 'active_alias_mailbox'
    ) THEN
        ALTER TABLE gmail_allocations
            ADD COLUMN active_alias_mailbox VARCHAR(16) GENERATED ALWAYS AS (
                CASE WHEN status = 'allocated' AND mailbox IN ('dot', 'plus') THEN mailbox ELSE NULL END
            ) STORED AFTER active_main_resource_id;
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = DATABASE()
          AND table_name = 'gmail_allocations'
          AND column_name = 'active_alias_project_id'
    ) THEN
        ALTER TABLE gmail_allocations
            ADD COLUMN active_alias_project_id BIGINT UNSIGNED GENERATED ALWAYS AS (
                CASE WHEN status = 'allocated' AND mailbox IN ('dot', 'plus') THEN project_id ELSE NULL END
            ) STORED AFTER active_alias_mailbox;
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = DATABASE()
          AND table_name = 'gmail_allocations'
          AND column_name = 'active_alias_email'
    ) THEN
        ALTER TABLE gmail_allocations
            ADD COLUMN active_alias_email VARCHAR(320) GENERATED ALWAYS AS (
                CASE WHEN status = 'allocated' AND mailbox IN ('dot', 'plus') THEN LOWER(TRIM(email)) ELSE NULL END
            ) STORED AFTER active_alias_project_id;
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM information_schema.statistics
        WHERE table_schema = DATABASE()
          AND table_name = 'gmail_allocations'
          AND index_name = 'idx_gmail_allocations_active_main'
    ) THEN
        ALTER TABLE gmail_allocations
            ADD UNIQUE INDEX idx_gmail_allocations_active_main (active_main_resource_id);
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM information_schema.statistics
        WHERE table_schema = DATABASE()
          AND table_name = 'gmail_allocations'
          AND index_name = 'idx_gmail_allocations_active_alias'
    ) THEN
        ALTER TABLE gmail_allocations
            ADD UNIQUE INDEX idx_gmail_allocations_active_alias
                (active_alias_mailbox, active_alias_project_id, active_alias_email);
    END IF;

    IF EXISTS (
        SELECT 1 FROM information_schema.statistics
        WHERE table_schema = DATABASE()
          AND table_name = 'gmail_allocations'
          AND index_name = 'idx_gmail_allocations_resource'
    ) THEN
        ALTER TABLE gmail_allocations DROP INDEX idx_gmail_allocations_resource;
    END IF;

    -- The previous image inserts Gmail allocations without creating a typed
    -- guard. Keep this FK for the later contract migration after rollback is
    -- no longer possible; new code still writes and validates the guard.
    IF EXISTS (
        SELECT 1 FROM information_schema.table_constraints
        WHERE constraint_schema = DATABASE()
          AND table_name = 'gmail_allocations'
          AND constraint_name = 'fk_gmail_allocations_guard'
    ) THEN
        ALTER TABLE gmail_allocations DROP FOREIGN KEY fk_gmail_allocations_guard;
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM information_schema.table_constraints
        WHERE constraint_schema = DATABASE()
          AND table_name = 'gmail_allocations'
          AND constraint_name = 'fk_gmail_allocations_project'
    ) THEN
        ALTER TABLE gmail_allocations
            ADD CONSTRAINT fk_gmail_allocations_project
                FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE RESTRICT;
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM information_schema.table_constraints
        WHERE constraint_schema = DATABASE()
          AND table_name = 'gmail_allocations'
          AND constraint_name = 'fk_gmail_allocations_product_project'
    ) THEN
        ALTER TABLE gmail_allocations
            ADD CONSTRAINT fk_gmail_allocations_product_project
                FOREIGN KEY (product_id, project_id)
                REFERENCES project_products(id, project_id) ON DELETE RESTRICT;
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM information_schema.table_constraints
        WHERE constraint_schema = DATABASE()
          AND table_name = 'gmail_allocations'
          AND constraint_name = 'chk_gmail_allocations_guard_type'
    ) THEN
        ALTER TABLE gmail_allocations
            ADD CONSTRAINT chk_gmail_allocations_guard_type CHECK (guard_type = 'gmail');
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM information_schema.table_constraints
        WHERE constraint_schema = DATABASE()
          AND table_name = 'gmail_allocations'
          AND constraint_name = 'chk_gmail_allocations_supply_scope'
    ) THEN
        ALTER TABLE gmail_allocations
            ADD CONSTRAINT chk_gmail_allocations_supply_scope
                CHECK (supply_scope IN ('owned', 'public'));
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM information_schema.table_constraints
        WHERE constraint_schema = DATABASE()
          AND table_name = 'gmail_allocations'
          AND constraint_name = 'chk_gmail_allocations_mailbox'
    ) THEN
        ALTER TABLE gmail_allocations
            ADD CONSTRAINT chk_gmail_allocations_mailbox
                CHECK (mailbox IN ('main', 'dot', 'plus'));
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM information_schema.table_constraints
        WHERE constraint_schema = DATABASE()
          AND table_name = 'gmail_allocations'
          AND constraint_name = 'chk_gmail_allocations_status'
    ) THEN
        ALTER TABLE gmail_allocations
            ADD CONSTRAINT chk_gmail_allocations_status
                CHECK (status IN ('allocated', 'released'));
    END IF;

    IF NOT EXISTS (
        SELECT 1
        FROM information_schema.table_constraints AS tc
        JOIN information_schema.check_constraints AS cc
          ON cc.constraint_schema = tc.constraint_schema
         AND cc.constraint_name = tc.constraint_name
        WHERE tc.constraint_schema = DATABASE()
          AND tc.table_name = 'mailmatch_messages'
          AND tc.constraint_name = 'chk_mailmatch_messages_resource_type'
          AND LOWER(cc.check_clause) LIKE '%gmail%'
    ) THEN
        IF EXISTS (
            SELECT 1 FROM information_schema.table_constraints
            WHERE constraint_schema = DATABASE()
              AND table_name = 'mailmatch_messages'
              AND constraint_name = 'chk_mailmatch_messages_resource_type'
        ) THEN
            ALTER TABLE mailmatch_messages
                DROP CHECK chk_mailmatch_messages_resource_type;
        END IF;
        ALTER TABLE mailmatch_messages
            ADD CONSTRAINT chk_mailmatch_messages_resource_type
                CHECK (resource_type IN ('microsoft', 'domain', 'gmail')) NOT ENFORCED,
            ALGORITHM=INSTANT;
    END IF;

    SET SESSION lock_wait_timeout = previous_lock_wait_timeout;
    SET SESSION innodb_lock_wait_timeout = previous_innodb_lock_wait_timeout;
END
-- +goose StatementEnd

CALL migrate_gmail_unified_allocations_00077();
DROP PROCEDURE migrate_gmail_unified_allocations_00077;

-- +goose Down

DROP TEMPORARY TABLE IF EXISTS gmail_unified_allocations_down_guard;
CREATE TEMPORARY TABLE gmail_unified_allocations_down_guard (
    unsafe_rows BIGINT NOT NULL,
    CONSTRAINT chk_gmail_unified_allocations_down_guard CHECK (unsafe_rows = 0)
);
INSERT INTO gmail_unified_allocations_down_guard(unsafe_rows)
SELECT
    (SELECT COUNT(*) FROM gmail_allocations WHERE status = 'released' OR provider_cursor <> 0 OR provider_spam_cursor <> 0)
    + (SELECT COUNT(*) FROM gmail_allocations WHERE mailbox <> 'main')
    + (SELECT COUNT(*) FROM gmail_allocations WHERE source = 'local' AND service_mode = 'code')
    + (SELECT COUNT(*) FROM gmail_code_sessions WHERE service_mode <> 'code' OR provider_cursor <> 0 OR provider_spam_cursor <> 0)
    + (SELECT COUNT(*) FROM gmail_code_sessions WHERE source = 'local')
    + (SELECT COUNT(*) FROM mailmatch_messages WHERE resource_type = 'gmail')
    + (SELECT COUNT(*) FROM gmail_resources WHERE status = 'identifying')
    + (SELECT COUNT(*) FROM projects WHERE gmail_history_scan_generation <> 0)
    + (SELECT COUNT(*) FROM (
        SELECT resource_id
        FROM gmail_allocations
        WHERE resource_id IS NOT NULL
        GROUP BY resource_id
        HAVING COUNT(*) > 1
    ) AS duplicated_resources);
DROP TEMPORARY TABLE gmail_unified_allocations_down_guard;

ALTER TABLE mailmatch_messages
    DROP CHECK chk_mailmatch_messages_resource_type,
    ADD CONSTRAINT chk_mailmatch_messages_resource_type
        CHECK (resource_type IN ('microsoft', 'domain')) NOT ENFORCED,
    ALGORITHM=INSTANT;

ALTER TABLE projects
    DROP CHECK chk_projects_gmail_history_scan_failures,
    DROP CHECK chk_projects_gmail_history_scan_status,
    DROP INDEX idx_projects_gmail_history_scan_pending,
    DROP COLUMN gmail_history_scan_finished_at,
    DROP COLUMN gmail_history_scan_started_at,
    DROP COLUMN gmail_history_scan_requested_at,
    DROP COLUMN gmail_history_scan_last_safe_error,
    DROP COLUMN gmail_history_scan_request_id,
    DROP COLUMN gmail_history_scan_skipped_count,
    DROP COLUMN gmail_history_scan_matched_count,
    DROP COLUMN gmail_history_scan_scanned_count,
    DROP COLUMN gmail_history_scan_failures,
    DROP COLUMN gmail_history_scan_generation,
    DROP COLUMN gmail_history_scan_status;

ALTER TABLE gmail_code_sessions
    DROP CHECK chk_gmail_code_sessions_mode,
    DROP COLUMN provider_spam_cursor,
    DROP COLUMN provider_cursor,
    DROP COLUMN service_mode;

ALTER TABLE gmail_allocations
    ADD UNIQUE INDEX idx_gmail_allocations_resource (resource_id);

ALTER TABLE gmail_allocations
    DROP FOREIGN KEY fk_gmail_allocations_project,
    DROP FOREIGN KEY fk_gmail_allocations_product_project,
    DROP CHECK chk_gmail_allocations_guard_type,
    DROP CHECK chk_gmail_allocations_supply_scope,
    DROP CHECK chk_gmail_allocations_mailbox,
    DROP CHECK chk_gmail_allocations_status,
    DROP INDEX idx_gmail_allocations_active_main,
    DROP INDEX idx_gmail_allocations_active_alias,
    DROP INDEX idx_gmail_allocations_guard_type,
    DROP INDEX idx_gmail_allocations_product_project,
    DROP INDEX idx_gmail_allocations_project_created,
    DROP INDEX idx_gmail_allocations_resource_project,
    DROP INDEX idx_gmail_allocations_project_mailbox_email,
    DROP COLUMN active_alias_email,
    DROP COLUMN active_alias_project_id,
    DROP COLUMN active_alias_mailbox,
    DROP COLUMN active_main_resource_id,
    DROP COLUMN released_at,
    DROP COLUMN status,
    DROP COLUMN mailbox,
    DROP COLUMN supply_scope,
    DROP COLUMN provider_spam_cursor,
    DROP COLUMN provider_cursor,
    DROP COLUMN product_id,
    DROP COLUMN project_id,
    DROP COLUMN guard_type;

ALTER TABLE gmail_resources
    DROP CHECK chk_gmail_resources_validation_failures,
    DROP CHECK chk_gmail_resources_validation_generation,
    DROP CHECK chk_gmail_resources_credential_revision,
    DROP INDEX idx_gmail_resources_alloc_public,
    DROP INDEX idx_gmail_resources_alloc_owned,
    DROP INDEX idx_gmail_resources_validation_pending,
    DROP COLUMN validation_command_hash,
    DROP COLUMN validation_request_id,
    DROP COLUMN validation_failures,
    DROP COLUMN validation_generation,
    DROP COLUMN credential_updated_at,
    DROP COLUMN credential_revision,
    DROP COLUMN last_allocated_at,
    DROP COLUMN alloc_bucket,
    DROP COLUMN for_sale;

ALTER TABLE gmail_resources
    DROP CHECK chk_gmail_resources_status,
    MODIFY COLUMN status VARCHAR(24) NOT NULL DEFAULT 'available'
        COMMENT 'available|disabled|leased|sold|pending|validating|normal|abnormal|deleted',
    ADD CONSTRAINT chk_gmail_resources_status CHECK (
        status IN ('available', 'disabled', 'leased', 'sold',
                   'pending', 'validating', 'normal', 'abnormal', 'deleted')
    );

DELETE FROM allocation_order_guards WHERE type = 'gmail';
ALTER TABLE allocation_order_guards
    DROP CHECK chk_allocation_order_guards_type,
    ADD CONSTRAINT chk_allocation_order_guards_type
        CHECK (type IN ('microsoft', 'domain')) NOT ENFORCED,
    ALGORITHM=INSTANT;
