-- +goose Up

ALTER TABLE microsoft_allocations
    DROP INDEX idx_ms_alloc_active_main,
    DROP INDEX idx_ms_alloc_active_alias,
    DROP INDEX idx_ms_alloc_active_dot,
    DROP INDEX idx_ms_alloc_active_plus,
    DROP COLUMN active_main_resource_id,
    DROP COLUMN active_explicit_alias_id,
    DROP COLUMN active_dot_project_id,
    DROP COLUMN active_dot_alias_id,
    DROP COLUMN active_plus_project_id,
    DROP COLUMN active_plus_alias_id,
    ADD COLUMN active_kind TINYINT UNSIGNED GENERATED ALWAYS AS (
        CASE
            WHEN status <> 'allocated' THEN NULL
            WHEN mailbox = 'main' THEN 1
            WHEN mailbox = 'alias' THEN 2
            WHEN mailbox = 'dot' THEN 3
            WHEN mailbox = 'plus' THEN 4
            ELSE NULL
        END
    ) STORED,
    ADD COLUMN active_project_id BIGINT UNSIGNED GENERATED ALWAYS AS (
        CASE
            WHEN status <> 'allocated' THEN NULL
            WHEN mailbox IN ('dot', 'plus') THEN project_id
            ELSE 0
        END
    ) STORED,
    ADD COLUMN active_entity_id BIGINT UNSIGNED GENERATED ALWAYS AS (
        CASE
            WHEN status <> 'allocated' THEN NULL
            WHEN mailbox = 'main' THEN resource_id
            WHEN mailbox = 'alias' THEN explicit_alias_id
            WHEN mailbox = 'dot' THEN dot_alias_id
            WHEN mailbox = 'plus' THEN plus_alias_id
            ELSE NULL
        END
    ) STORED,
    ADD UNIQUE INDEX idx_ms_alloc_active (
        active_kind,
        active_project_id,
        active_entity_id
    );

-- +goose Down

ALTER TABLE microsoft_allocations
    DROP INDEX idx_ms_alloc_active,
    DROP COLUMN active_kind,
    DROP COLUMN active_project_id,
    DROP COLUMN active_entity_id,
    ADD COLUMN active_main_resource_id BIGINT UNSIGNED GENERATED ALWAYS AS (
        CASE WHEN status = 'allocated' AND mailbox = 'main' THEN resource_id ELSE NULL END
    ) STORED,
    ADD COLUMN active_explicit_alias_id BIGINT UNSIGNED GENERATED ALWAYS AS (
        CASE WHEN status = 'allocated' AND mailbox = 'alias' THEN explicit_alias_id ELSE NULL END
    ) STORED,
    ADD COLUMN active_dot_project_id BIGINT UNSIGNED GENERATED ALWAYS AS (
        CASE WHEN status = 'allocated' AND mailbox = 'dot' THEN project_id ELSE NULL END
    ) STORED,
    ADD COLUMN active_dot_alias_id BIGINT UNSIGNED GENERATED ALWAYS AS (
        CASE WHEN status = 'allocated' AND mailbox = 'dot' THEN dot_alias_id ELSE NULL END
    ) STORED,
    ADD COLUMN active_plus_project_id BIGINT UNSIGNED GENERATED ALWAYS AS (
        CASE WHEN status = 'allocated' AND mailbox = 'plus' THEN project_id ELSE NULL END
    ) STORED,
    ADD COLUMN active_plus_alias_id BIGINT UNSIGNED GENERATED ALWAYS AS (
        CASE WHEN status = 'allocated' AND mailbox = 'plus' THEN plus_alias_id ELSE NULL END
    ) STORED,
    ADD UNIQUE INDEX idx_ms_alloc_active_main (active_main_resource_id),
    ADD UNIQUE INDEX idx_ms_alloc_active_alias (active_explicit_alias_id),
    ADD UNIQUE INDEX idx_ms_alloc_active_dot (active_dot_project_id, active_dot_alias_id),
    ADD UNIQUE INDEX idx_ms_alloc_active_plus (active_plus_project_id, active_plus_alias_id);
