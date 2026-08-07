-- +goose Up

-- All HME aliases of one iCloud account are forwarded into the same Gmail
-- mailbox, so realtime pickup advances one shared Inbox/Spam cursor per root.
ALTER TABLE icloud_resources
    ADD COLUMN provider_cursor BIGINT UNSIGNED NOT NULL DEFAULT 0
        AFTER gmail_resource_id,
    ADD COLUMN provider_spam_cursor BIGINT UNSIGNED NOT NULL DEFAULT 0
        AFTER provider_cursor;

-- +goose Down

ALTER TABLE icloud_resources
    DROP COLUMN provider_spam_cursor,
    DROP COLUMN provider_cursor;
