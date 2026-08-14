-- +goose Up

ALTER TABLE icloud_maintenance_runs
    DROP CHECK chk_icloud_maintenance_kind,
    DROP INDEX uk_icloud_maintenance_resource_generation,
    ADD UNIQUE INDEX uk_icloud_maintenance_resource_generation
        (resource_id, kind, validation_generation),
    ADD CONSTRAINT chk_icloud_maintenance_kind
        CHECK (kind IN ('validation', 'alias'));

-- +goose Down

DELETE FROM icloud_maintenance_runs WHERE kind = 'alias';

ALTER TABLE icloud_maintenance_runs
    DROP CHECK chk_icloud_maintenance_kind,
    DROP INDEX uk_icloud_maintenance_resource_generation,
    ADD UNIQUE INDEX uk_icloud_maintenance_resource_generation
        (resource_id, validation_generation),
    ADD CONSTRAINT chk_icloud_maintenance_kind
        CHECK (kind = 'validation');
