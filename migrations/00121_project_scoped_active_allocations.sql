-- +goose Up

-- Legacy Gmail rows omitted ownership. Backfill before the online NOT NULL
-- rebuild so the project key cannot silently bypass uniqueness through NULL.
UPDATE gmail_allocations AS ga
JOIN orders AS o ON o.order_no = ga.order_no
SET ga.project_id = o.project_id,
    ga.product_id = o.project_product_id
WHERE ga.project_id IS NULL OR ga.product_id IS NULL;

DROP TEMPORARY TABLE IF EXISTS gmail_project_scope_00121_guard;
CREATE TEMPORARY TABLE gmail_project_scope_00121_guard (
    unsafe_rows BIGINT NOT NULL,
    CONSTRAINT chk_gmail_project_scope_00121_guard CHECK (unsafe_rows = 0)
);
INSERT INTO gmail_project_scope_00121_guard (unsafe_rows)
SELECT COUNT(*)
FROM gmail_allocations
WHERE project_id IS NULL OR product_id IS NULL;
DROP TEMPORARY TABLE gmail_project_scope_00121_guard;

-- Every ALTER explicitly rejects algorithms that would copy a table or block
-- concurrent DML. The legacy Microsoft lookup keeps the previous image's
-- active_project_id=0 queries indexed until all instances run project queries.
DROP PROCEDURE IF EXISTS migrate_project_scoped_active_00121;
-- +goose StatementBegin
CREATE PROCEDURE migrate_project_scoped_active_00121()
BEGIN
    IF EXISTS (
        SELECT 1
        FROM information_schema.columns
        WHERE table_schema = DATABASE()
          AND table_name = 'gmail_allocations'
          AND column_name IN ('project_id', 'product_id')
          AND is_nullable = 'YES'
    ) THEN
        ALTER TABLE gmail_allocations
            MODIFY COLUMN project_id BIGINT UNSIGNED NOT NULL,
            MODIFY COLUMN product_id BIGINT UNSIGNED NOT NULL,
            ALGORITHM=INPLACE,
            LOCK=NONE;
    END IF;

    IF NOT EXISTS (
        SELECT 1
        FROM information_schema.statistics
        WHERE table_schema = DATABASE()
          AND table_name = 'gmail_allocations'
          AND index_name = 'idx_gmail_allocations_active_main_project'
    ) THEN
        ALTER TABLE gmail_allocations
            ADD UNIQUE INDEX idx_gmail_allocations_active_main_project
                (project_id, active_main_resource_id),
            ALGORITHM=INPLACE,
            LOCK=NONE;
    END IF;
    IF EXISTS (
        SELECT 1
        FROM information_schema.statistics
        WHERE table_schema = DATABASE()
          AND table_name = 'gmail_allocations'
          AND index_name = 'idx_gmail_allocations_active_main'
    ) THEN
        ALTER TABLE gmail_allocations
            DROP INDEX idx_gmail_allocations_active_main,
            ALGORITHM=INPLACE,
            LOCK=NONE;
    END IF;

    IF NOT EXISTS (
        SELECT 1
        FROM information_schema.statistics
        WHERE table_schema = DATABASE()
          AND table_name = 'microsoft_allocations'
          AND index_name = 'idx_ms_alloc_active_project'
    ) AND NOT EXISTS (
        SELECT 1
        FROM information_schema.statistics
        WHERE table_schema = DATABASE()
          AND table_name = 'microsoft_allocations'
          AND index_name = 'idx_ms_alloc_active_legacy_lookup'
    ) THEN
        ALTER TABLE microsoft_allocations
            ADD UNIQUE INDEX idx_ms_alloc_active_project
                (active_kind, project_id, active_entity_id),
            ADD INDEX idx_ms_alloc_active_legacy_lookup
                (active_kind, active_project_id, active_entity_id),
            ALGORITHM=INPLACE,
            LOCK=NONE;
    ELSE
        IF NOT EXISTS (
            SELECT 1
            FROM information_schema.statistics
            WHERE table_schema = DATABASE()
              AND table_name = 'microsoft_allocations'
              AND index_name = 'idx_ms_alloc_active_project'
        ) THEN
            ALTER TABLE microsoft_allocations
                ADD UNIQUE INDEX idx_ms_alloc_active_project
                    (active_kind, project_id, active_entity_id),
                ALGORITHM=INPLACE,
                LOCK=NONE;
        END IF;
        IF NOT EXISTS (
            SELECT 1
            FROM information_schema.statistics
            WHERE table_schema = DATABASE()
              AND table_name = 'microsoft_allocations'
              AND index_name = 'idx_ms_alloc_active_legacy_lookup'
        ) THEN
            ALTER TABLE microsoft_allocations
                ADD INDEX idx_ms_alloc_active_legacy_lookup
                    (active_kind, active_project_id, active_entity_id),
                ALGORITHM=INPLACE,
                LOCK=NONE;
        END IF;
    END IF;
    IF EXISTS (
        SELECT 1
        FROM information_schema.statistics
        WHERE table_schema = DATABASE()
          AND table_name = 'microsoft_allocations'
          AND index_name = 'idx_ms_alloc_active'
    ) THEN
        ALTER TABLE microsoft_allocations
            DROP INDEX idx_ms_alloc_active,
            ALGORITHM=INPLACE,
            LOCK=NONE;
    END IF;
END
-- +goose StatementEnd
CALL migrate_project_scoped_active_00121();
DROP PROCEDURE migrate_project_scoped_active_00121;

-- +goose Down

-- Both old keys are global for main/explicit alias identities. Check all
-- affected tables before the first ALTER so a failed rollback leaves schema
-- version and indexes unchanged.
DROP TEMPORARY TABLE IF EXISTS project_scoped_active_down_guard;
CREATE TEMPORARY TABLE project_scoped_active_down_guard (
    unsafe_rows BIGINT NOT NULL,
    CONSTRAINT chk_project_scoped_active_down_guard CHECK (unsafe_rows = 0)
);
INSERT INTO project_scoped_active_down_guard (unsafe_rows)
SELECT
    (SELECT COUNT(*)
     FROM (
         SELECT resource_id
         FROM gmail_allocations
         WHERE status = 'allocated'
           AND mailbox = 'main'
           AND resource_id IS NOT NULL
         GROUP BY resource_id
         HAVING COUNT(*) > 1
     ) AS duplicated_gmail_main)
    + (SELECT COUNT(*)
       FROM (
           SELECT active_kind, active_entity_id
           FROM microsoft_allocations
           WHERE status = 'allocated'
             AND mailbox IN ('main', 'alias')
             AND active_entity_id IS NOT NULL
           GROUP BY active_kind, active_entity_id
           HAVING COUNT(*) > 1
       ) AS duplicated_microsoft_global);
DROP TEMPORARY TABLE project_scoped_active_down_guard;

DROP PROCEDURE IF EXISTS rollback_project_scoped_active_00121;
-- +goose StatementBegin
CREATE PROCEDURE rollback_project_scoped_active_00121()
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM information_schema.statistics
        WHERE table_schema = DATABASE()
          AND table_name = 'microsoft_allocations'
          AND index_name = 'idx_ms_alloc_active'
    ) THEN
        ALTER TABLE microsoft_allocations
            ADD UNIQUE INDEX idx_ms_alloc_active
                (active_kind, active_project_id, active_entity_id),
            ALGORITHM=INPLACE,
            LOCK=NONE;
    END IF;
    IF EXISTS (
        SELECT 1
        FROM information_schema.statistics
        WHERE table_schema = DATABASE()
          AND table_name = 'microsoft_allocations'
          AND index_name = 'idx_ms_alloc_active_project'
    ) AND EXISTS (
        SELECT 1
        FROM information_schema.statistics
        WHERE table_schema = DATABASE()
          AND table_name = 'microsoft_allocations'
          AND index_name = 'idx_ms_alloc_active_legacy_lookup'
    ) THEN
        ALTER TABLE microsoft_allocations
            DROP INDEX idx_ms_alloc_active_project,
            DROP INDEX idx_ms_alloc_active_legacy_lookup,
            ALGORITHM=INPLACE,
            LOCK=NONE;
    ELSE
        IF EXISTS (
            SELECT 1
            FROM information_schema.statistics
            WHERE table_schema = DATABASE()
              AND table_name = 'microsoft_allocations'
              AND index_name = 'idx_ms_alloc_active_project'
        ) THEN
            ALTER TABLE microsoft_allocations
                DROP INDEX idx_ms_alloc_active_project,
                ALGORITHM=INPLACE,
                LOCK=NONE;
        END IF;
        IF EXISTS (
            SELECT 1
            FROM information_schema.statistics
            WHERE table_schema = DATABASE()
              AND table_name = 'microsoft_allocations'
              AND index_name = 'idx_ms_alloc_active_legacy_lookup'
        ) THEN
            ALTER TABLE microsoft_allocations
                DROP INDEX idx_ms_alloc_active_legacy_lookup,
                ALGORITHM=INPLACE,
                LOCK=NONE;
        END IF;
    END IF;

    IF NOT EXISTS (
        SELECT 1
        FROM information_schema.statistics
        WHERE table_schema = DATABASE()
          AND table_name = 'gmail_allocations'
          AND index_name = 'idx_gmail_allocations_active_main'
    ) THEN
        ALTER TABLE gmail_allocations
            ADD UNIQUE INDEX idx_gmail_allocations_active_main
                (active_main_resource_id),
            ALGORITHM=INPLACE,
            LOCK=NONE;
    END IF;
    IF EXISTS (
        SELECT 1
        FROM information_schema.statistics
        WHERE table_schema = DATABASE()
          AND table_name = 'gmail_allocations'
          AND index_name = 'idx_gmail_allocations_active_main_project'
    ) THEN
        ALTER TABLE gmail_allocations
            DROP INDEX idx_gmail_allocations_active_main_project,
            ALGORITHM=INPLACE,
            LOCK=NONE;
    END IF;

    IF EXISTS (
        SELECT 1
        FROM information_schema.columns
        WHERE table_schema = DATABASE()
          AND table_name = 'gmail_allocations'
          AND column_name IN ('project_id', 'product_id')
          AND is_nullable = 'NO'
    ) THEN
        ALTER TABLE gmail_allocations
            MODIFY COLUMN project_id BIGINT UNSIGNED NULL,
            MODIFY COLUMN product_id BIGINT UNSIGNED NULL,
            ALGORITHM=INPLACE,
            LOCK=NONE;
    END IF;
END
-- +goose StatementEnd
CALL rollback_project_scoped_active_00121();
DROP PROCEDURE rollback_project_scoped_active_00121;
