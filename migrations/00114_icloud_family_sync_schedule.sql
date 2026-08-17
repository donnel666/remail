-- +goose Up

ALTER TABLE icloud_resources
    ADD COLUMN family_next_sync_at DATETIME(3) NULL AFTER family_synced_at;

UPDATE icloud_resources
SET family_next_sync_at = CURRENT_TIMESTAMP(3),
    next_provision_at = CASE
        WHEN status = 'normal' AND (next_provision_at IS NULL OR next_provision_at > CURRENT_TIMESTAMP(3))
            THEN CURRENT_TIMESTAMP(3)
        ELSE next_provision_at
    END
WHERE account_role = 'primary';

-- +goose Down

ALTER TABLE icloud_resources
    DROP COLUMN family_next_sync_at;
