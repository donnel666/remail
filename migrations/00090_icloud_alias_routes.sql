-- +goose Up

ALTER TABLE icloud_aliases
    ADD COLUMN recipient_probe_token VARCHAR(128) NOT NULL DEFAULT '' AFTER recipient_mail_id,
    ADD COLUMN recipient_probe_started_at DATETIME(3) NULL AFTER recipient_probe_token,
    ADD COLUMN recipient_probe_last_sent_at DATETIME(3) NULL AFTER recipient_probe_started_at,
    ALGORITHM=INSTANT;

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

-- +goose Down

DROP TABLE icloud_alias_routes;

ALTER TABLE icloud_aliases
    DROP COLUMN recipient_probe_last_sent_at,
    DROP COLUMN recipient_probe_started_at,
    DROP COLUMN recipient_probe_token,
    ALGORITHM=INSTANT;
