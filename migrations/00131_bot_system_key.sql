-- +goose Up

ALTER TABLE system_keys
    DROP CHECK chk_system_keys_purpose;

ALTER TABLE system_keys
    ADD COLUMN platform VARCHAR(32) NULL AFTER purpose,
    ADD COLUMN subject_namespace VARCHAR(50) NULL AFTER platform,
    ADD COLUMN allowed_group_ids JSON NULL AFTER subject_namespace,
    ADD CONSTRAINT chk_system_keys_purpose CHECK (
        purpose IN ('icloud_forwarding', 'smtp_submission', 'bot')
    ),
    ADD CONSTRAINT chk_system_keys_bot_scope CHECK (
        (purpose = 'bot' AND platform IS NOT NULL AND platform <> ''
            AND subject_namespace IS NOT NULL AND subject_namespace <> ''
            AND allowed_group_ids IS NOT NULL
            AND JSON_TYPE(allowed_group_ids) = 'ARRAY'
            AND JSON_LENGTH(allowed_group_ids) > 0)
        OR
        (purpose <> 'bot' AND platform IS NULL AND subject_namespace IS NULL
            AND allowed_group_ids IS NULL)
    );

-- +goose Down

ALTER TABLE system_keys
    DROP CHECK chk_system_keys_bot_scope,
    DROP CHECK chk_system_keys_purpose;

DELETE FROM system_keys WHERE purpose = 'bot';

ALTER TABLE system_keys
    DROP COLUMN allowed_group_ids,
    DROP COLUMN subject_namespace,
    DROP COLUMN platform,
    ADD CONSTRAINT chk_system_keys_purpose CHECK (
        purpose IN ('icloud_forwarding', 'smtp_submission')
    );
