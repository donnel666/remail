-- +goose Up

ALTER TABLE icloud_maintenance_runs
    DROP CHECK chk_icloud_maintenance_kind,
    ADD CONSTRAINT chk_icloud_maintenance_kind CHECK (kind IN ('validation', 'alias', 'liveness')),
    ADD COLUMN probe_recipient VARCHAR(320) NOT NULL DEFAULT '',
    ADD COLUMN probe_sender VARCHAR(320) NOT NULL DEFAULT '',
    ADD COLUMN probe_sent_at DATETIME(3) NULL,
    ADD COLUMN next_check_at DATETIME(3) NULL,
    ADD INDEX idx_icloud_liveness_due (kind, status, next_check_at, id);

-- +goose Down

DELETE FROM icloud_maintenance_runs WHERE kind = 'liveness';

ALTER TABLE icloud_maintenance_runs
    DROP CHECK chk_icloud_maintenance_kind,
    ADD CONSTRAINT chk_icloud_maintenance_kind CHECK (kind IN ('validation', 'alias')),
    DROP INDEX idx_icloud_liveness_due,
    DROP COLUMN probe_recipient,
    DROP COLUMN probe_sender,
    DROP COLUMN probe_sent_at,
    DROP COLUMN next_check_at;
