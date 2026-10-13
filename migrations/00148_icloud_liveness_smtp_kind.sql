-- +goose Up

ALTER TABLE icloud_maintenance_runs
    DROP CHECK chk_icloud_maintenance_kind,
    ADD CONSTRAINT chk_icloud_maintenance_kind CHECK (kind IN ('validation', 'alias', 'liveness', 'liveness_smtp'));

-- +goose Down

-- Retain SMTP probe history; rollback must fail while rows still use this mode.
ALTER TABLE icloud_maintenance_runs
    DROP CHECK chk_icloud_maintenance_kind,
    ADD CONSTRAINT chk_icloud_maintenance_kind CHECK (kind IN ('validation', 'alias', 'liveness'));
