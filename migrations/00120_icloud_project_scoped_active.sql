-- +goose Up

-- (alias_id, project_id) already prevents an alias from returning to the same
-- project. Removing the stricter global active key permits different projects
-- to use the alias concurrently without adding another redundant index.
DROP PROCEDURE IF EXISTS migrate_icloud_project_scoped_active_00120;
-- +goose StatementBegin
CREATE PROCEDURE migrate_icloud_project_scoped_active_00120()
BEGIN
    IF EXISTS (
        SELECT 1
        FROM information_schema.statistics
        WHERE table_schema = DATABASE()
          AND table_name = 'icloud_allocations'
          AND index_name = 'uk_icloud_allocations_active_alias'
    ) THEN
        ALTER TABLE icloud_allocations
            DROP INDEX uk_icloud_allocations_active_alias,
            ALGORITHM=INPLACE,
            LOCK=NONE;
    END IF;
END
-- +goose StatementEnd
CALL migrate_icloud_project_scoped_active_00120();
DROP PROCEDURE migrate_icloud_project_scoped_active_00120;

-- +goose Down

-- Restoring the old global key is unsafe once one alias is active in more
-- than one project. Fail before any DDL instead of leaving a half-rolled-back
-- table.
DROP TEMPORARY TABLE IF EXISTS icloud_project_scoped_active_down_guard;
CREATE TEMPORARY TABLE icloud_project_scoped_active_down_guard (
    unsafe_rows BIGINT NOT NULL,
    CONSTRAINT chk_icloud_project_scoped_active_down_guard CHECK (unsafe_rows = 0)
);
INSERT INTO icloud_project_scoped_active_down_guard (unsafe_rows)
SELECT COUNT(*)
FROM (
    SELECT alias_id
    FROM icloud_allocations
    WHERE status = 'allocated'
    GROUP BY alias_id
    HAVING COUNT(*) > 1
) AS duplicated_aliases;
DROP TEMPORARY TABLE icloud_project_scoped_active_down_guard;

DROP PROCEDURE IF EXISTS rollback_icloud_project_scoped_active_00120;
-- +goose StatementBegin
CREATE PROCEDURE rollback_icloud_project_scoped_active_00120()
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM information_schema.statistics
        WHERE table_schema = DATABASE()
          AND table_name = 'icloud_allocations'
          AND index_name = 'uk_icloud_allocations_active_alias'
    ) THEN
        ALTER TABLE icloud_allocations
            ADD UNIQUE INDEX uk_icloud_allocations_active_alias (active_alias_id),
            ALGORITHM=INPLACE,
            LOCK=NONE;
    END IF;
END
-- +goose StatementEnd
CALL rollback_icloud_project_scoped_active_00120();
DROP PROCEDURE rollback_icloud_project_scoped_active_00120;
