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

ALTER TABLE icloud_resources
    ADD COLUMN gmail_resource_id BIGINT UNSIGNED NOT NULL
        COMMENT 'resolved from the imported local Gmail email'
        AFTER cookie,
    ADD COLUMN provider_cursor BIGINT UNSIGNED NOT NULL DEFAULT 0
        AFTER gmail_resource_id,
    ADD COLUMN provider_spam_cursor BIGINT UNSIGNED NOT NULL DEFAULT 0
        AFTER provider_cursor,
    ADD INDEX idx_icloud_resources_gmail
        (gmail_resource_id, status, id),
    ADD CONSTRAINT fk_icloud_resources_gmail
        FOREIGN KEY (gmail_resource_id)
        REFERENCES gmail_resources(id) ON DELETE RESTRICT;
