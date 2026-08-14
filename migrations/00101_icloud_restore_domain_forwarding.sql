-- +goose Up

ALTER TABLE icloud_resources
    DROP INDEX idx_icloud_resources_imap_sync,
    DROP COLUMN imap_last_sync_at,
    DROP COLUMN imap_last_uid,
    DROP COLUMN imap_uid_validity,
    DROP COLUMN imap_app_password,
    ADD COLUMN selected_forward_to VARCHAR(320) NOT NULL DEFAULT ''
        AFTER primary_email;

ALTER TABLE icloud_aliases
    DROP CHECK chk_icloud_aliases_required,
    ADD COLUMN forward_to_email VARCHAR(320) NOT NULL DEFAULT '' AFTER note,
    ADD COLUMN provider_domain VARCHAR(255) NOT NULL DEFAULT '' AFTER origin,
    ADD COLUMN recipient_mail_id VARCHAR(191) NOT NULL DEFAULT '' AFTER provider_domain;

UPDATE icloud_aliases
SET anonymous_id = CONCAT('retired-', id),
    status = 'missing'
WHERE anonymous_id = '';

ALTER TABLE icloud_aliases
    MODIFY COLUMN anonymous_id VARCHAR(191) NOT NULL,
    ADD UNIQUE INDEX uk_icloud_aliases_resource_anonymous
        (resource_id, anonymous_id),
    ADD INDEX idx_icloud_aliases_forward
        (forward_to_email, status, id),
    ADD INDEX idx_icloud_aliases_recipient_mail
        (recipient_mail_id, resource_id, id),
    ADD CONSTRAINT chk_icloud_aliases_required
        CHECK (anonymous_id <> '' AND email <> '');

CREATE TABLE icloud_alias_routes (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    resource_id BIGINT UNSIGNED NOT NULL,
    alias_id BIGINT UNSIGNED NOT NULL,
    forward_to_email VARCHAR(320) NOT NULL,
    recipient_mail_id VARCHAR(191) NOT NULL,
    first_seen_at DATETIME(3) NOT NULL,
    last_seen_at DATETIME(3) NOT NULL,

    UNIQUE INDEX uk_icloud_alias_routes_pair
        (forward_to_email, recipient_mail_id),
    INDEX idx_icloud_alias_routes_alias (alias_id, id),

    CONSTRAINT fk_icloud_alias_routes_resource
        FOREIGN KEY (resource_id) REFERENCES icloud_resources(id) ON DELETE CASCADE,
    CONSTRAINT fk_icloud_alias_routes_alias
        FOREIGN KEY (alias_id, resource_id)
        REFERENCES icloud_aliases(id, resource_id) ON DELETE CASCADE,
    CONSTRAINT chk_icloud_alias_routes_required
        CHECK (forward_to_email <> '' AND recipient_mail_id <> '')
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

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

DROP TABLE icloud_alias_routes;

ALTER TABLE icloud_aliases
    DROP CHECK chk_icloud_aliases_required,
    DROP INDEX uk_icloud_aliases_resource_anonymous,
    DROP INDEX idx_icloud_aliases_forward,
    DROP INDEX idx_icloud_aliases_recipient_mail,
    DROP COLUMN forward_to_email,
    DROP COLUMN provider_domain,
    DROP COLUMN recipient_mail_id,
    MODIFY COLUMN anonymous_id VARCHAR(191) NOT NULL DEFAULT '',
    ADD CONSTRAINT chk_icloud_aliases_required CHECK (email <> '');

ALTER TABLE icloud_resources
    DROP COLUMN selected_forward_to,
    ADD COLUMN imap_app_password VARCHAR(128) NOT NULL DEFAULT ''
        COMMENT 'Apple app-specific password; never expose through API or logs'
        AFTER primary_email,
    ADD COLUMN imap_uid_validity VARCHAR(64) NOT NULL DEFAULT ''
        AFTER imap_app_password,
    ADD COLUMN imap_last_uid BIGINT UNSIGNED NOT NULL DEFAULT 0
        AFTER imap_uid_validity,
    ADD COLUMN imap_last_sync_at DATETIME(3) NULL
        AFTER imap_last_uid,
    ADD INDEX idx_icloud_resources_imap_sync
        (status, imap_last_sync_at, id);
