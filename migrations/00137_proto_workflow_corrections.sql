-- +goose Up

-- MySQL commits DDL individually. Like migrations 00077/00086, inspect each
-- completed step so an interrupted run can resume before Goose records it.
DROP PROCEDURE IF EXISTS correct_proto_workflow_00137;

-- +goose StatementBegin
CREATE PROCEDURE correct_proto_workflow_00137()
BEGIN
    DECLARE previous_lock_wait_timeout BIGINT;
    DECLARE previous_innodb_lock_wait_timeout BIGINT;
    DECLARE receipt_key_columns TEXT;

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

    -- Validate legacy facts before changing any business table. Never discard
    -- conflicting receipts or orphaned import evidence to force an upgrade.
    IF EXISTS (
        SELECT 1 FROM proto_command_receipts
        GROUP BY operator_user_id, idempotency_key HAVING COUNT(*) > 1
    ) THEN
        SIGNAL SQLSTATE '45000'
            SET MESSAGE_TEXT = 'migration 00137: conflicting Proto command keys; reconcile before retry';
    END IF;
    IF EXISTS (
        SELECT 1 FROM proto_resource_import_items AS item
        LEFT JOIN proto_resources AS resource ON resource.id = item.resource_id
        WHERE item.line_number <= 0 OR (item.resource_id IS NOT NULL AND resource.id IS NULL)
    ) THEN
        SIGNAL SQLSTATE '45000'
            SET MESSAGE_TEXT = 'migration 00137: invalid Proto import references; reconcile before retry';
    END IF;
    IF EXISTS (
        SELECT 1 FROM proto_resources
        WHERE CHAR_LENGTH(SUBSTRING_INDEX(email_address, '@', -1)) > 255
    ) THEN
        SIGNAL SQLSTATE '45000'
            SET MESSAGE_TEXT = 'migration 00137: oversized Proto email domain; reconcile before retry';
    END IF;

    IF EXISTS (SELECT 1 FROM information_schema.table_constraints WHERE constraint_schema = DATABASE() AND table_name = 'proto_resources' AND constraint_name = 'chk_proto_resource_for_sale') THEN
        ALTER TABLE proto_resources DROP CHECK chk_proto_resource_for_sale;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = 'proto_resources' AND column_name = 'email_domain') THEN
        ALTER TABLE proto_resources ADD COLUMN email_domain VARCHAR(255) NOT NULL DEFAULT '', ALGORITHM=INSTANT;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = 'proto_resources' AND column_name = 'long_lived') THEN
        ALTER TABLE proto_resources ADD COLUMN long_lived TINYINT(1) NOT NULL DEFAULT 0, ALGORITHM=INSTANT;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = 'proto_resources' AND column_name = 'quality_score') THEN
        ALTER TABLE proto_resources ADD COLUMN quality_score INT NOT NULL DEFAULT 0, ALGORITHM=INSTANT;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = 'proto_resources' AND column_name = 'alloc_bucket') THEN
        ALTER TABLE proto_resources ADD COLUMN alloc_bucket SMALLINT UNSIGNED NOT NULL DEFAULT 0, ALGORITHM=INSTANT;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM information_schema.table_constraints WHERE constraint_schema = DATABASE() AND table_name = 'proto_resources' AND constraint_name = 'chk_proto_quality_score') THEN
        ALTER TABLE proto_resources ADD CONSTRAINT chk_proto_quality_score CHECK (quality_score BETWEEN 0 AND 100);
    END IF;
    IF NOT EXISTS (SELECT 1 FROM information_schema.table_constraints WHERE constraint_schema = DATABASE() AND table_name = 'proto_resources' AND constraint_name = 'chk_proto_alloc_bucket') THEN
        ALTER TABLE proto_resources ADD CONSTRAINT chk_proto_alloc_bucket CHECK (alloc_bucket < 2048);
    END IF;
    IF NOT EXISTS (SELECT 1 FROM information_schema.statistics WHERE table_schema = DATABASE() AND table_name = 'proto_resources' AND index_name = 'idx_proto_dispatch') THEN
        ALTER TABLE proto_resources ADD INDEX idx_proto_dispatch (status, updated_at, id), ALGORITHM=INPLACE, LOCK=NONE;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM information_schema.statistics WHERE table_schema = DATABASE() AND table_name = 'proto_resources' AND index_name = 'idx_proto_suffix') THEN
        ALTER TABLE proto_resources ADD INDEX idx_proto_suffix (email_domain, status, for_sale, id), ALGORITHM=INPLACE, LOCK=NONE;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM information_schema.statistics WHERE table_schema = DATABASE() AND table_name = 'proto_resources' AND index_name = 'idx_proto_candidates') THEN
        ALTER TABLE proto_resources ADD INDEX idx_proto_candidates (status, for_sale, alloc_bucket, last_allocated_at, quality_score, id), ALGORITHM=INPLACE, LOCK=NONE;
    END IF;

    UPDATE proto_resources
    SET email_domain = LOWER(SUBSTRING_INDEX(email_address, '@', -1)),
        alloc_bucket = MOD(id, 2048)
    WHERE email_domain <> LOWER(SUBSTRING_INDEX(email_address, '@', -1))
       OR alloc_bucket <> MOD(id, 2048);

    IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = 'proto_resource_imports' AND column_name = 'long_lived') THEN
        ALTER TABLE proto_resource_imports ADD COLUMN long_lived TINYINT(1) NOT NULL DEFAULT 0, ALGORITHM=INSTANT;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = 'proto_resource_imports' AND column_name = 'attempts') THEN
        ALTER TABLE proto_resource_imports ADD COLUMN attempts INT UNSIGNED NOT NULL DEFAULT 0, ALGORITHM=INSTANT;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = 'proto_resource_imports' AND column_name = 'max_attempts') THEN
        ALTER TABLE proto_resource_imports ADD COLUMN max_attempts INT UNSIGNED NOT NULL DEFAULT 3, ALGORITHM=INSTANT;
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.check_constraints
        WHERE constraint_schema = DATABASE() AND constraint_name = 'chk_proto_import_dispatch'
          AND LOWER(check_clause) LIKE '%succeeded%' AND LOWER(check_clause) LIKE '%failed%'
    ) THEN
        IF EXISTS (SELECT 1 FROM information_schema.table_constraints WHERE constraint_schema = DATABASE() AND table_name = 'proto_resource_imports' AND constraint_name = 'chk_proto_import_dispatch') THEN
            ALTER TABLE proto_resource_imports DROP CHECK chk_proto_import_dispatch;
        END IF;
        ALTER TABLE proto_resource_imports ADD CONSTRAINT chk_proto_import_dispatch
            CHECK (dispatch_status IN ('pending','queued','running','done','succeeded','failed'));
    END IF;
    IF NOT EXISTS (SELECT 1 FROM information_schema.statistics WHERE table_schema = DATABASE() AND table_name = 'proto_resource_imports' AND index_name = 'idx_proto_import_dispatch') THEN
        ALTER TABLE proto_resource_imports ADD INDEX idx_proto_import_dispatch (status, dispatch_status, generation, id), ALGORITHM=INPLACE, LOCK=NONE;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM information_schema.table_constraints WHERE constraint_schema = DATABASE() AND table_name = 'proto_resource_import_items' AND constraint_name = 'chk_proto_import_line_number') THEN
        ALTER TABLE proto_resource_import_items ADD CONSTRAINT chk_proto_import_line_number CHECK (line_number > 0);
    END IF;
    IF NOT EXISTS (SELECT 1 FROM information_schema.table_constraints WHERE constraint_schema = DATABASE() AND table_name = 'proto_resource_import_items' AND constraint_name = 'fk_proto_import_item_resource') THEN
        ALTER TABLE proto_resource_import_items ADD CONSTRAINT fk_proto_import_item_resource FOREIGN KEY (resource_id) REFERENCES proto_resources(id) ON DELETE RESTRICT;
    END IF;

    IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = 'proto_command_receipts' AND column_name = 'reservation_token') THEN
        ALTER TABLE proto_command_receipts ADD COLUMN reservation_token CHAR(36) NOT NULL DEFAULT '', ALGORITHM=INSTANT;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = 'proto_command_receipts' AND column_name = 'result_json') THEN
        ALTER TABLE proto_command_receipts ADD COLUMN result_json JSON NULL, ALGORITHM=INSTANT;
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.check_constraints
        WHERE constraint_schema = DATABASE() AND constraint_name = 'chk_proto_command_receipt_status'
          AND LOWER(check_clause) LIKE '%processing%' AND LOWER(check_clause) LIKE '%succeeded%'
    ) THEN
        IF EXISTS (SELECT 1 FROM information_schema.table_constraints WHERE constraint_schema = DATABASE() AND table_name = 'proto_command_receipts' AND constraint_name = 'chk_proto_command_receipt_status') THEN
            ALTER TABLE proto_command_receipts DROP CHECK chk_proto_command_receipt_status;
        END IF;
        ALTER TABLE proto_command_receipts
            ADD CONSTRAINT chk_proto_command_receipt_status CHECK (status IN ('accepted','completed','failed','processing','succeeded'));
    END IF;
    ALTER TABLE proto_command_receipts ALTER COLUMN status SET DEFAULT 'processing';

    -- An accepted legacy receipt did not prove its business transaction ran.
    UPDATE proto_command_receipts
    SET status = CASE status WHEN 'completed' THEN 'succeeded' ELSE 'failed' END
    WHERE status IN ('accepted', 'completed');

    SELECT GROUP_CONCAT(column_name ORDER BY seq_in_index SEPARATOR ',')
    INTO receipt_key_columns
    FROM information_schema.statistics
    WHERE table_schema = DATABASE() AND table_name = 'proto_command_receipts'
      AND index_name = 'uq_proto_command_receipt';
    IF receipt_key_columns IS NULL THEN
        ALTER TABLE proto_command_receipts ADD UNIQUE INDEX uq_proto_command_receipt (operator_user_id, idempotency_key);
    ELSEIF receipt_key_columns <> 'operator_user_id,idempotency_key' THEN
        ALTER TABLE proto_command_receipts
            DROP INDEX uq_proto_command_receipt,
            ADD UNIQUE INDEX uq_proto_command_receipt (operator_user_id, idempotency_key);
    END IF;

    -- Allocation owner is a historical snapshot, independent of a later transfer.
    IF EXISTS (SELECT 1 FROM information_schema.table_constraints WHERE constraint_schema = DATABASE() AND table_name = 'proto_allocations' AND constraint_name = 'fk_proto_allocation_resource_owner') THEN
        ALTER TABLE proto_allocations DROP FOREIGN KEY fk_proto_allocation_resource_owner;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM information_schema.table_constraints WHERE constraint_schema = DATABASE() AND table_name = 'proto_allocations' AND constraint_name = 'fk_proto_allocation_owner') THEN
        ALTER TABLE proto_allocations ADD CONSTRAINT fk_proto_allocation_owner FOREIGN KEY (owner_user_id) REFERENCES users(id) ON DELETE RESTRICT;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM information_schema.statistics WHERE table_schema = DATABASE() AND table_name = 'proto_allocations' AND index_name = 'idx_proto_allocation_history') THEN
        ALTER TABLE proto_allocations ADD INDEX idx_proto_allocation_history (resource_id, project_id, email, id), ALGORITHM=INPLACE, LOCK=NONE;
    END IF;

    CREATE TABLE IF NOT EXISTS proto_project_history_scan_states (
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
        INDEX idx_proto_project_history_dispatch (status, requested_at, project_id),
        CONSTRAINT fk_proto_project_history_project FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE,
        CONSTRAINT chk_proto_project_history_status CHECK (status IN ('pending','processing','normal','abnormal','uncertain')),
        CONSTRAINT chk_proto_project_history_generation CHECK (generation > 0)
    ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

    IF NOT EXISTS (
        SELECT 1 FROM information_schema.check_constraints
        WHERE constraint_schema = DATABASE() AND constraint_name = 'chk_mailmatch_messages_resource_type'
          AND LOWER(check_clause) LIKE '%proto%'
    ) THEN
        IF EXISTS (SELECT 1 FROM information_schema.table_constraints WHERE constraint_schema = DATABASE() AND table_name = 'mailmatch_messages' AND constraint_name = 'chk_mailmatch_messages_resource_type') THEN
            ALTER TABLE mailmatch_messages DROP CHECK chk_mailmatch_messages_resource_type, ALGORITHM=INSTANT;
        END IF;
        ALTER TABLE mailmatch_messages
            ADD CONSTRAINT chk_mailmatch_messages_resource_type CHECK (
                resource_type IN ('microsoft', 'domain', 'gmail', 'icloud', 'proto')
            ) NOT ENFORCED, ALGORITHM=INSTANT;
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.check_constraints
        WHERE constraint_schema = DATABASE() AND constraint_name = 'chk_mailmatch_admin_fetch_operation'
          AND LOWER(check_clause) LIKE '%proto_resource_fetch%'
    ) THEN
        IF EXISTS (SELECT 1 FROM information_schema.table_constraints WHERE constraint_schema = DATABASE() AND table_name = 'mailmatch_admin_resource_fetch_states' AND constraint_name = 'chk_mailmatch_admin_fetch_operation') THEN
            ALTER TABLE mailmatch_admin_resource_fetch_states DROP CHECK chk_mailmatch_admin_fetch_operation;
        END IF;
        ALTER TABLE mailmatch_admin_resource_fetch_states
            MODIFY COLUMN operation_kind VARCHAR(32) NOT NULL DEFAULT 'resource_fetch'
                COMMENT 'resource_fetch|gmail_resource_fetch|icloud_resource_fetch|proto_resource_fetch',
            ADD CONSTRAINT chk_mailmatch_admin_fetch_operation CHECK (
                operation_kind IN ('resource_fetch', 'gmail_resource_fetch', 'icloud_resource_fetch', 'proto_resource_fetch')
            );
    END IF;

    SET SESSION lock_wait_timeout = previous_lock_wait_timeout;
    SET SESSION innodb_lock_wait_timeout = previous_innodb_lock_wait_timeout;
END;
-- +goose StatementEnd

CALL correct_proto_workflow_00137();
DROP PROCEDURE correct_proto_workflow_00137;

-- +goose Down

-- Do not discard Proto credentials, history, or task facts during a rollback.
DROP TEMPORARY TABLE IF EXISTS proto_workflow_down_guard;
CREATE TEMPORARY TABLE proto_workflow_down_guard (
    unsafe_rows BIGINT NOT NULL,
    CONSTRAINT chk_proto_workflow_down_guard CHECK (unsafe_rows = 0)
);
INSERT INTO proto_workflow_down_guard (unsafe_rows)
SELECT (SELECT COUNT(*) FROM proto_resources)
     + (SELECT COUNT(*) FROM proto_resource_imports)
     + (SELECT COUNT(*) FROM proto_command_receipts)
     + (SELECT COUNT(*) FROM proto_project_history_scan_states)
     + (SELECT COUNT(*) FROM mailmatch_messages WHERE resource_type = 'proto')
     + (SELECT COUNT(*) FROM mailmatch_admin_resource_fetch_states WHERE operation_kind = 'proto_resource_fetch');
DROP TEMPORARY TABLE proto_workflow_down_guard;

ALTER TABLE mailmatch_admin_resource_fetch_states
    DROP CHECK chk_mailmatch_admin_fetch_operation,
    MODIFY COLUMN operation_kind VARCHAR(32) NOT NULL DEFAULT 'resource_fetch'
        COMMENT 'resource_fetch|gmail_resource_fetch|icloud_resource_fetch',
    ADD CONSTRAINT chk_mailmatch_admin_fetch_operation CHECK (
        operation_kind IN ('resource_fetch', 'gmail_resource_fetch', 'icloud_resource_fetch')
    );

ALTER TABLE mailmatch_messages
    DROP CHECK chk_mailmatch_messages_resource_type,
    ADD CONSTRAINT chk_mailmatch_messages_resource_type CHECK (
        resource_type IN ('microsoft', 'domain', 'gmail', 'icloud')
    ) NOT ENFORCED,
    ALGORITHM=INSTANT;

DROP TABLE proto_project_history_scan_states;
ALTER TABLE proto_allocations
    DROP INDEX idx_proto_allocation_history,
    DROP FOREIGN KEY fk_proto_allocation_owner,
    ADD CONSTRAINT fk_proto_allocation_resource_owner FOREIGN KEY (resource_id, owner_user_id) REFERENCES proto_resources(id, owner_user_id) ON DELETE RESTRICT;

ALTER TABLE proto_command_receipts
    DROP CHECK chk_proto_command_receipt_status,
    DROP INDEX uq_proto_command_receipt,
    DROP COLUMN result_json,
    DROP COLUMN reservation_token,
    MODIFY COLUMN status VARCHAR(16) NOT NULL DEFAULT 'accepted',
    ADD UNIQUE INDEX uq_proto_command_receipt (operator_user_id, resource_id, command, idempotency_key),
    ADD CONSTRAINT chk_proto_command_receipt_status CHECK (status IN ('accepted','completed','failed'));

ALTER TABLE proto_resource_import_items
    DROP FOREIGN KEY fk_proto_import_item_resource,
    DROP CHECK chk_proto_import_line_number;

ALTER TABLE proto_resource_imports
    DROP INDEX idx_proto_import_dispatch,
    DROP CHECK chk_proto_import_dispatch,
    DROP COLUMN attempts,
    DROP COLUMN max_attempts,
    DROP COLUMN long_lived,
    ADD CONSTRAINT chk_proto_import_dispatch CHECK (dispatch_status IN ('pending','queued','running','done'));

ALTER TABLE proto_resources
    DROP INDEX idx_proto_candidates,
    DROP INDEX idx_proto_suffix,
    DROP INDEX idx_proto_dispatch,
    DROP CHECK chk_proto_alloc_bucket,
    DROP CHECK chk_proto_quality_score,
    DROP COLUMN alloc_bucket,
    DROP COLUMN quality_score,
    DROP COLUMN long_lived,
    DROP COLUMN email_domain,
    ADD CONSTRAINT chk_proto_resource_for_sale CHECK (for_sale = 0 OR status = 'normal');
