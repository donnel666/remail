-- +goose Up

-- All running application instances now use the project-scoped active key.
-- The non-unique legacy lookup only supported the previous application image.
DROP PROCEDURE IF EXISTS drop_microsoft_active_legacy_lookup_00122;
-- +goose StatementBegin
CREATE PROCEDURE drop_microsoft_active_legacy_lookup_00122()
BEGIN
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
END
-- +goose StatementEnd
CALL drop_microsoft_active_legacy_lookup_00122();
DROP PROCEDURE drop_microsoft_active_legacy_lookup_00122;

-- +goose Down

DROP PROCEDURE IF EXISTS restore_microsoft_active_legacy_lookup_00122;
-- +goose StatementBegin
CREATE PROCEDURE restore_microsoft_active_legacy_lookup_00122()
BEGIN
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
END
-- +goose StatementEnd
CALL restore_microsoft_active_legacy_lookup_00122();
DROP PROCEDURE restore_microsoft_active_legacy_lookup_00122;
