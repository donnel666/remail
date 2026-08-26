-- +goose Up

ALTER TABLE system_keys
    ADD COLUMN purpose VARCHAR(32) NOT NULL DEFAULT 'icloud_forwarding' AFTER name,
    ADD CONSTRAINT chk_system_keys_purpose CHECK (purpose IN ('icloud_forwarding', 'smtp_submission'));

-- +goose Down

ALTER TABLE system_keys
    DROP CHECK chk_system_keys_purpose,
    DROP COLUMN purpose;
