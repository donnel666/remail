-- +goose Up

-- Production can be changed manually before this migration is deployed. Keep
-- Up resumable so Goose can record version 86 without repeating completed DDL.
DROP PROCEDURE IF EXISTS drop_legacy_order_allocation_ids_00086;

-- +goose StatementBegin
CREATE PROCEDURE drop_legacy_order_allocation_ids_00086()
BEGIN
    DECLARE previous_lock_wait_timeout BIGINT;
    DECLARE ddl_clauses TEXT DEFAULT '';

    DECLARE EXIT HANDLER FOR SQLEXCEPTION
    BEGIN
        SET SESSION lock_wait_timeout = previous_lock_wait_timeout;
        RESIGNAL;
    END;

    SET previous_lock_wait_timeout = @@SESSION.lock_wait_timeout;
    SET SESSION lock_wait_timeout = 2;

    IF EXISTS (
        SELECT 1
        FROM information_schema.columns
        WHERE table_schema = DATABASE()
          AND table_name = 'orders'
          AND column_name = 'microsoft_alloc_id'
    ) THEN
        SET @legacy_order_allocation_mismatch_00086 = 0;
        SET @legacy_order_allocation_check_00086_sql = '
            SELECT EXISTS (
                SELECT 1
                FROM orders o
                LEFT JOIN microsoft_allocations by_id
                  ON by_id.id = o.microsoft_alloc_id
                LEFT JOIN microsoft_allocations by_order
                  ON by_order.order_no = o.order_no
                WHERE (
                    o.microsoft_alloc_id IS NOT NULL
                    AND (
                        NOT (o.allocation_type <=> ''microsoft'')
                        OR by_id.id IS NULL
                        OR by_id.order_no <> o.order_no
                    )
                ) OR (
                    o.allocation_type = ''microsoft''
                    AND by_order.id IS NULL
                )
                LIMIT 1
            ) INTO @legacy_order_allocation_mismatch_00086';
        PREPARE legacy_order_allocation_check_00086_stmt
            FROM @legacy_order_allocation_check_00086_sql;
        EXECUTE legacy_order_allocation_check_00086_stmt;
        DEALLOCATE PREPARE legacy_order_allocation_check_00086_stmt;

        IF @legacy_order_allocation_mismatch_00086 <> 0 THEN
            SIGNAL SQLSTATE '45000'
                SET MESSAGE_TEXT = 'migration 00086 found inconsistent Microsoft order allocation linkage';
        END IF;
    END IF;

    IF EXISTS (
        SELECT 1
        FROM information_schema.columns
        WHERE table_schema = DATABASE()
          AND table_name = 'orders'
          AND column_name = 'domain_alloc_id'
    ) THEN
        SET @legacy_order_allocation_mismatch_00086 = 0;
        SET @legacy_order_allocation_check_00086_sql = '
            SELECT EXISTS (
                SELECT 1
                FROM orders o
                LEFT JOIN domain_allocations by_id
                  ON by_id.id = o.domain_alloc_id
                LEFT JOIN domain_allocations by_order
                  ON by_order.order_no = o.order_no
                WHERE (
                    o.domain_alloc_id IS NOT NULL
                    AND (
                        NOT (o.allocation_type <=> ''domain'')
                        OR by_id.id IS NULL
                        OR by_id.order_no <> o.order_no
                    )
                ) OR (
                    o.allocation_type = ''domain''
                    AND by_order.id IS NULL
                )
                LIMIT 1
            ) INTO @legacy_order_allocation_mismatch_00086';
        PREPARE legacy_order_allocation_check_00086_stmt
            FROM @legacy_order_allocation_check_00086_sql;
        EXECUTE legacy_order_allocation_check_00086_stmt;
        DEALLOCATE PREPARE legacy_order_allocation_check_00086_stmt;

        IF @legacy_order_allocation_mismatch_00086 <> 0 THEN
            SIGNAL SQLSTATE '45000'
                SET MESSAGE_TEXT = 'migration 00086 found inconsistent Domain order allocation linkage';
        END IF;
    END IF;

    IF EXISTS (
        SELECT 1
        FROM information_schema.table_constraints
        WHERE constraint_schema = DATABASE()
          AND table_name = 'orders'
          AND constraint_name = 'fk_orders_ms_alloc'
    ) THEN
        SET ddl_clauses = 'DROP FOREIGN KEY fk_orders_ms_alloc';
    END IF;

    IF EXISTS (
        SELECT 1
        FROM information_schema.table_constraints
        WHERE constraint_schema = DATABASE()
          AND table_name = 'orders'
          AND constraint_name = 'fk_orders_domain_alloc'
    ) THEN
        SET ddl_clauses = CONCAT_WS(', ', NULLIF(ddl_clauses, ''),
            'DROP FOREIGN KEY fk_orders_domain_alloc');
    END IF;

    IF EXISTS (
        SELECT 1
        FROM information_schema.statistics
        WHERE table_schema = DATABASE()
          AND table_name = 'orders'
          AND index_name = 'fk_orders_ms_alloc'
    ) THEN
        SET ddl_clauses = CONCAT_WS(', ', NULLIF(ddl_clauses, ''),
            'DROP INDEX fk_orders_ms_alloc');
    END IF;

    IF EXISTS (
        SELECT 1
        FROM information_schema.statistics
        WHERE table_schema = DATABASE()
          AND table_name = 'orders'
          AND index_name = 'fk_orders_domain_alloc'
    ) THEN
        SET ddl_clauses = CONCAT_WS(', ', NULLIF(ddl_clauses, ''),
            'DROP INDEX fk_orders_domain_alloc');
    END IF;

    IF ddl_clauses <> '' THEN
        SET @drop_legacy_order_keys_00086_sql = CONCAT(
            'ALTER TABLE orders ', ddl_clauses,
            ', ALGORITHM=INPLACE, LOCK=NONE'
        );
        PREPARE drop_legacy_order_keys_00086_stmt
            FROM @drop_legacy_order_keys_00086_sql;
        EXECUTE drop_legacy_order_keys_00086_stmt;
        DEALLOCATE PREPARE drop_legacy_order_keys_00086_stmt;
    END IF;

    SET ddl_clauses = '';

    IF EXISTS (
        SELECT 1
        FROM information_schema.columns
        WHERE table_schema = DATABASE()
          AND table_name = 'orders'
          AND column_name = 'microsoft_alloc_id'
    ) THEN
        SET ddl_clauses = 'DROP COLUMN microsoft_alloc_id';
    END IF;

    IF EXISTS (
        SELECT 1
        FROM information_schema.columns
        WHERE table_schema = DATABASE()
          AND table_name = 'orders'
          AND column_name = 'domain_alloc_id'
    ) THEN
        SET ddl_clauses = CONCAT_WS(', ', NULLIF(ddl_clauses, ''),
            'DROP COLUMN domain_alloc_id');
    END IF;

    IF ddl_clauses <> '' THEN
        SET @drop_legacy_order_columns_00086_sql = CONCAT(
            'ALTER TABLE orders ', ddl_clauses, ', ALGORITHM=INSTANT'
        );
        PREPARE drop_legacy_order_columns_00086_stmt
            FROM @drop_legacy_order_columns_00086_sql;
        EXECUTE drop_legacy_order_columns_00086_stmt;
        DEALLOCATE PREPARE drop_legacy_order_columns_00086_stmt;
    END IF;

    IF EXISTS (
        SELECT 1
        FROM information_schema.columns
        WHERE table_schema = DATABASE()
          AND table_name = 'orders'
          AND column_name IN ('microsoft_alloc_id', 'domain_alloc_id')
    ) THEN
        SIGNAL SQLSTATE '45000'
            SET MESSAGE_TEXT = 'migration 00086 did not remove legacy order allocation IDs';
    END IF;

    SET SESSION lock_wait_timeout = previous_lock_wait_timeout;
END;
-- +goose StatementEnd

CALL drop_legacy_order_allocation_ids_00086();
DROP PROCEDURE drop_legacy_order_allocation_ids_00086;

-- +goose Down

-- Restoring IDs on a populated table would require a large backfill and could
-- make an old binary observe a partial mapping. Permit only empty-schema
-- rollbacks used by migration verification.
DROP TEMPORARY TABLE IF EXISTS legacy_order_allocation_ids_down_guard;
CREATE TEMPORARY TABLE legacy_order_allocation_ids_down_guard (
    unsafe_rows TINYINT UNSIGNED NOT NULL,
    CONSTRAINT chk_legacy_order_allocation_ids_down_guard
        CHECK (unsafe_rows = 0)
);
INSERT INTO legacy_order_allocation_ids_down_guard(unsafe_rows)
SELECT EXISTS(SELECT 1 FROM orders LIMIT 1);
DROP TEMPORARY TABLE legacy_order_allocation_ids_down_guard;

ALTER TABLE orders
    ADD COLUMN microsoft_alloc_id BIGINT UNSIGNED NULL AFTER allocation_type,
    ADD COLUMN domain_alloc_id BIGINT UNSIGNED NULL AFTER microsoft_alloc_id,
    ALGORITHM=INSTANT;

-- The table is guaranteed empty above, so foreign-key validation cannot copy
-- or lock live order rows during this schema-only rollback.
ALTER TABLE orders
    ADD INDEX fk_orders_ms_alloc (microsoft_alloc_id),
    ADD INDEX fk_orders_domain_alloc (domain_alloc_id),
    ADD CONSTRAINT fk_orders_ms_alloc
        FOREIGN KEY (microsoft_alloc_id)
        REFERENCES microsoft_allocations(id) ON DELETE RESTRICT,
    ADD CONSTRAINT fk_orders_domain_alloc
        FOREIGN KEY (domain_alloc_id)
        REFERENCES domain_allocations(id) ON DELETE RESTRICT;
