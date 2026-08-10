-- +goose Up

ALTER TABLE icloud_resources
    DROP FOREIGN KEY fk_icloud_resources_gmail,
    DROP INDEX idx_icloud_resources_gmail,
    DROP COLUMN provider_spam_cursor,
    DROP COLUMN provider_cursor,
    DROP COLUMN gmail_resource_id,
    ALGORITHM=INPLACE,
    LOCK=NONE;

ALTER TABLE icloud_aliases
    ADD INDEX idx_icloud_aliases_recipient_mail
        (recipient_mail_id, resource_id, id),
    ALGORITHM=INPLACE,
    LOCK=NONE;

ALTER TABLE inbound_mails
    ADD INDEX idx_inbound_mails_icloud_scan
        (mailbox_key, resource_type, status, created_at, id),
    ALGORITHM=INPLACE,
    LOCK=NONE;

-- +goose Down

ALTER TABLE inbound_mails
    DROP INDEX idx_inbound_mails_icloud_scan,
    ALGORITHM=INPLACE,
    LOCK=NONE;

ALTER TABLE icloud_aliases
    DROP INDEX idx_icloud_aliases_recipient_mail,
    ALGORITHM=INPLACE,
    LOCK=NONE;

-- The removed Gmail binding and provider cursors cannot be reconstructed from
-- an iCloud-only schema. Keep rollback honest instead of adding a NOT NULL
-- foreign key that fails as soon as an iCloud row exists.
