-- +goose Up

ALTER TABLE inbound_mails
    ADD COLUMN mailbox_key VARCHAR(320) NOT NULL DEFAULT '' AFTER recipient,
    ADD INDEX idx_inbound_mails_mailbox_created
        (resource_type, resource_id, mailbox_key, created_at, id),
    ALGORITHM=INPLACE,
    LOCK=NONE;

-- +goose Down

ALTER TABLE inbound_mails
    DROP INDEX idx_inbound_mails_mailbox_created,
    DROP COLUMN mailbox_key,
    ALGORITHM=INPLACE,
    LOCK=NONE;
