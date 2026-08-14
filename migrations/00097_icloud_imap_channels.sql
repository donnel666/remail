-- +goose Up

-- Account health is IMAP-only. Apple web sessions are independent
-- provisioning credentials and live in this 1:N table.
ALTER TABLE icloud_resources
    ADD COLUMN imap_app_password VARCHAR(128) NOT NULL DEFAULT ''
        COMMENT 'Apple app-specific password; never expose through API or logs'
        AFTER primary_email,
    ADD COLUMN imap_uid_validity VARCHAR(64) NOT NULL DEFAULT ''
        AFTER imap_app_password,
    ADD COLUMN imap_last_uid BIGINT UNSIGNED NOT NULL DEFAULT 0
        AFTER imap_uid_validity,
    ADD COLUMN imap_last_sync_at DATETIME(3) NULL
        AFTER imap_last_uid,
    ADD COLUMN next_provision_at DATETIME(3) NULL
        AFTER next_validation_at,
    ADD INDEX idx_icloud_resources_provision (status, next_provision_at, id),
    ADD INDEX idx_icloud_resources_imap_sync (status, imap_last_sync_at, id);

CREATE TABLE icloud_resource_channels (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    resource_id BIGINT UNSIGNED NOT NULL,
    kind VARCHAR(24) NOT NULL COMMENT 'icloud_web|apple_account',

    host VARCHAR(255) NOT NULL,
    cookie TEXT NOT NULL,
    origin VARCHAR(255) NOT NULL DEFAULT '',
    referer VARCHAR(255) NOT NULL DEFAULT '',
    user_agent VARCHAR(500) NOT NULL DEFAULT '',

    dsid VARCHAR(191) NOT NULL DEFAULT '',
    client_id VARCHAR(191) NOT NULL DEFAULT '',
    client_build_number VARCHAR(64) NOT NULL DEFAULT '',
    client_mastering_number VARCHAR(64) NOT NULL DEFAULT '',

    scnt VARCHAR(191) NOT NULL DEFAULT '',
    session_id VARCHAR(191) NOT NULL DEFAULT '',
    api_key VARCHAR(191) NOT NULL DEFAULT '',
    data_access_token VARCHAR(191) NOT NULL DEFAULT '',
    manage_expires_at DATETIME(3) NULL,

    session_status VARCHAR(24) NOT NULL DEFAULT 'unchecked',
    session_failures TINYINT UNSIGNED NOT NULL DEFAULT 0,
    cooldown_until DATETIME(3) NULL,
    cooldown_stage TINYINT UNSIGNED NOT NULL DEFAULT 0,
    next_keepalive_at DATETIME(3) NULL,
    last_checked_at DATETIME(3) NULL,
    last_valid_at DATETIME(3) NULL,
    provision_window_at DATETIME(3) NULL,
    provision_window_count TINYINT UNSIGNED NOT NULL DEFAULT 0,
    created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3)
        ON UPDATE CURRENT_TIMESTAMP(3),

    UNIQUE INDEX uk_icloud_channels_resource_kind (resource_id, kind),
    INDEX idx_icloud_channels_dispatch
        (session_status, cooldown_until, next_keepalive_at, resource_id),
    CONSTRAINT fk_icloud_channels_resource
        FOREIGN KEY (resource_id) REFERENCES icloud_resources(id) ON DELETE CASCADE,
    CONSTRAINT chk_icloud_channels_kind
        CHECK (kind IN ('icloud_web', 'apple_account')),
    CONSTRAINT chk_icloud_channels_session
        CHECK (session_status IN ('unchecked', 'valid', 'invalid'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- Existing installations only have the legacy web API credential.
INSERT INTO icloud_resource_channels (
    resource_id, kind, host, cookie, origin, referer, user_agent,
    dsid, client_id, client_build_number, client_mastering_number,
    session_status, session_failures, next_keepalive_at,
    last_checked_at, last_valid_at, created_at, updated_at
)
SELECT
    id, 'icloud_web', host, cookie, origin, referer, user_agent,
    dsid, client_id, client_build_number, client_mastering_number,
    session_status, session_failures, next_keepalive_at,
    last_checked_at, last_valid_at, created_at, updated_at
FROM icloud_resources
WHERE TRIM(cookie) <> ''
ON DUPLICATE KEY UPDATE
    host = VALUES(host),
    cookie = VALUES(cookie),
    origin = VALUES(origin),
    referer = VALUES(referer),
    user_agent = VALUES(user_agent),
    dsid = VALUES(dsid),
    client_id = VALUES(client_id),
    client_build_number = VALUES(client_build_number),
    client_mastering_number = VALUES(client_mastering_number),
    session_status = VALUES(session_status),
    session_failures = VALUES(session_failures),
    next_keepalive_at = VALUES(next_keepalive_at),
    last_checked_at = VALUES(last_checked_at),
    last_valid_at = VALUES(last_valid_at),
    updated_at = VALUES(updated_at);

-- Alias maintenance is now provision-dispatch work, not a validation kind.
UPDATE icloud_maintenance_runs
SET kind = 'validation',
    status = CASE WHEN status IN ('queued', 'running') THEN 'canceled' ELSE status END,
    finished_at = CASE
        WHEN status IN ('queued', 'running') THEN UTC_TIMESTAMP(3)
        ELSE finished_at
    END,
    last_safe_error = CASE
        WHEN status IN ('queued', 'running') THEN 'Retired iCloud alias maintenance task.'
        ELSE last_safe_error
    END
WHERE kind = 'alias';

ALTER TABLE icloud_maintenance_runs
    DROP CHECK chk_icloud_maintenance_kind,
    ADD CONSTRAINT chk_icloud_maintenance_kind CHECK (kind = 'validation');

DROP TABLE icloud_alias_routes;

ALTER TABLE inbound_mails
    DROP INDEX idx_inbound_mails_icloud_scan,
    ALGORITHM=INPLACE,
    LOCK=NONE;

ALTER TABLE icloud_aliases
    DROP CHECK chk_icloud_aliases_required,
    DROP INDEX uk_icloud_aliases_resource_anonymous,
    DROP INDEX idx_icloud_aliases_forward,
    DROP INDEX idx_icloud_aliases_recipient_mail,
    DROP COLUMN forward_to_email,
    DROP COLUMN provider_domain,
    DROP COLUMN recipient_mail_id,
    DROP COLUMN recipient_probe_token,
    DROP COLUMN recipient_probe_started_at,
    DROP COLUMN recipient_probe_last_sent_at,
    MODIFY COLUMN anonymous_id VARCHAR(191) NOT NULL DEFAULT '',
    ADD CONSTRAINT chk_icloud_aliases_required CHECK (email <> '');

ALTER TABLE icloud_resources
    DROP CHECK chk_icloud_resources_required,
    DROP CHECK chk_icloud_resources_session,
    DROP CHECK chk_icloud_resources_probe,
    DROP INDEX uk_icloud_resources_dsid,
    DROP INDEX idx_icloud_resources_inventory,
    DROP INDEX idx_icloud_resources_keepalive,
    DROP COLUMN host,
    DROP COLUMN dsid,
    DROP COLUMN client_id,
    DROP COLUMN client_build_number,
    DROP COLUMN client_mastering_number,
    DROP COLUMN cookie,
    DROP COLUMN lang_code,
    DROP COLUMN origin,
    DROP COLUMN referer,
    DROP COLUMN user_agent,
    DROP COLUMN selected_forward_to,
    DROP COLUMN session_status,
    DROP COLUMN session_failures,
    DROP COLUMN delivery_probe_token,
    DROP COLUMN delivery_probe_alias,
    DROP COLUMN delivery_probe_started_at,
    DROP COLUMN delivery_probe_verified_at,
    DROP COLUMN next_keepalive_at,
    ADD INDEX idx_icloud_resources_inventory
        (for_sale, status, last_allocated_at, id);

-- +goose Down

ALTER TABLE inbound_mails
    ADD INDEX idx_inbound_mails_icloud_scan
        (mailbox_key, resource_type, status, created_at, id),
    ALGORITHM=INPLACE,
    LOCK=NONE;

ALTER TABLE icloud_resources
    DROP INDEX idx_icloud_resources_inventory,
    ADD COLUMN host VARCHAR(255) NOT NULL DEFAULT '' AFTER primary_email,
    ADD COLUMN dsid VARCHAR(191) NOT NULL DEFAULT '' AFTER host,
    ADD COLUMN client_id VARCHAR(191) NOT NULL DEFAULT '' AFTER dsid,
    ADD COLUMN client_build_number VARCHAR(64) NOT NULL DEFAULT '' AFTER client_id,
    ADD COLUMN client_mastering_number VARCHAR(64) NOT NULL DEFAULT '' AFTER client_build_number,
    ADD COLUMN cookie TEXT NOT NULL AFTER client_mastering_number,
    ADD COLUMN lang_code VARCHAR(16) NOT NULL DEFAULT 'zh-tw' AFTER cookie,
    ADD COLUMN origin VARCHAR(255) NOT NULL DEFAULT '' AFTER lang_code,
    ADD COLUMN referer VARCHAR(255) NOT NULL DEFAULT '' AFTER origin,
    ADD COLUMN user_agent VARCHAR(500) NOT NULL DEFAULT '' AFTER referer,
    ADD COLUMN selected_forward_to VARCHAR(320) NOT NULL DEFAULT '' AFTER user_agent,
    ADD COLUMN session_status VARCHAR(24) NOT NULL DEFAULT 'unchecked' AFTER status,
    ADD COLUMN session_failures TINYINT UNSIGNED NOT NULL DEFAULT 0 AFTER session_status,
    ADD COLUMN delivery_probe_token VARCHAR(128) NOT NULL DEFAULT '' AFTER next_validation_at,
    ADD COLUMN delivery_probe_alias VARCHAR(320) NOT NULL DEFAULT '' AFTER delivery_probe_token,
    ADD COLUMN delivery_probe_started_at DATETIME(3) NULL AFTER delivery_probe_alias,
    ADD COLUMN delivery_probe_verified_at DATETIME(3) NULL AFTER delivery_probe_started_at,
    ADD COLUMN next_keepalive_at DATETIME(3) NULL AFTER delivery_probe_verified_at;

-- New Apple-account-only rows cannot be represented by the old single web
-- credential schema. Keep rollback structurally valid without claiming the
-- synthesized values are usable provider credentials.
UPDATE icloud_resources AS ir
LEFT JOIN icloud_resource_channels AS ch
  ON ch.resource_id = ir.id AND ch.kind = 'icloud_web'
SET ir.host = COALESCE(NULLIF(ch.host, ''), CONCAT('retired-', ir.id, '-maildomainws.icloud.com')),
    ir.dsid = COALESCE(NULLIF(ch.dsid, ''), CONCAT('retired-', ir.id)),
    ir.client_id = COALESCE(NULLIF(ch.client_id, ''), CONCAT('retired-', ir.id)),
    ir.client_build_number = COALESCE(NULLIF(ch.client_build_number, ''), 'retired'),
    ir.client_mastering_number = COALESCE(NULLIF(ch.client_mastering_number, ''), 'retired'),
    ir.cookie = COALESCE(NULLIF(ch.cookie, ''), CONCAT('retired=', ir.id)),
    ir.origin = COALESCE(ch.origin, ''),
    ir.referer = COALESCE(ch.referer, ''),
    ir.user_agent = COALESCE(ch.user_agent, ''),
    ir.session_status = COALESCE(ch.session_status, 'unchecked'),
    ir.session_failures = COALESCE(ch.session_failures, 0),
    ir.next_keepalive_at = ch.next_keepalive_at,
    ir.last_checked_at = COALESCE(ch.last_checked_at, ir.last_checked_at),
    ir.last_valid_at = COALESCE(ch.last_valid_at, ir.last_valid_at);

ALTER TABLE icloud_resources
    ADD UNIQUE INDEX uk_icloud_resources_dsid (dsid),
    ADD INDEX idx_icloud_resources_inventory
        (for_sale, status, session_status, expire_at, last_allocated_at, id),
    ADD INDEX idx_icloud_resources_keepalive (session_status, next_keepalive_at, id),
    ADD CONSTRAINT chk_icloud_resources_session
        CHECK (session_status IN ('unchecked', 'valid', 'invalid')),
    ADD CONSTRAINT chk_icloud_resources_required
        CHECK (primary_email <> '' AND dsid <> '' AND host <> '' AND client_id <> ''
               AND client_build_number <> '' AND client_mastering_number <> '');

ALTER TABLE icloud_aliases
    DROP CHECK chk_icloud_aliases_required,
    ADD COLUMN forward_to_email VARCHAR(320) NOT NULL DEFAULT '' AFTER note,
    ADD COLUMN provider_domain VARCHAR(255) NOT NULL DEFAULT '' AFTER origin,
    ADD COLUMN recipient_mail_id VARCHAR(191) NOT NULL DEFAULT '' AFTER provider_domain,
    ADD COLUMN recipient_probe_token VARCHAR(128) NOT NULL DEFAULT '' AFTER recipient_mail_id,
    ADD COLUMN recipient_probe_started_at DATETIME(3) NULL AFTER recipient_probe_token,
    ADD COLUMN recipient_probe_last_sent_at DATETIME(3) NULL AFTER recipient_probe_started_at,
    MODIFY COLUMN anonymous_id VARCHAR(191) NOT NULL,
    ADD INDEX idx_icloud_aliases_forward (forward_to_email, status, id),
    ADD INDEX idx_icloud_aliases_recipient_mail (recipient_mail_id, resource_id, id);

UPDATE icloud_aliases
SET anonymous_id = CONCAT('retired-', id)
WHERE anonymous_id = '';

ALTER TABLE icloud_aliases
    ADD UNIQUE INDEX uk_icloud_aliases_resource_anonymous (resource_id, anonymous_id),
    ADD CONSTRAINT chk_icloud_aliases_required CHECK (anonymous_id <> '' AND email <> '');

CREATE TABLE icloud_alias_routes (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    resource_id BIGINT UNSIGNED NOT NULL,
    alias_id BIGINT UNSIGNED NOT NULL,
    forward_to_email VARCHAR(320) NOT NULL,
    recipient_mail_id VARCHAR(191) NOT NULL,
    first_seen_at DATETIME(3) NOT NULL,
    last_seen_at DATETIME(3) NOT NULL,
    UNIQUE INDEX uk_icloud_alias_routes_pair (forward_to_email, recipient_mail_id),
    INDEX idx_icloud_alias_routes_alias (alias_id, id),
    CONSTRAINT fk_icloud_alias_routes_resource
        FOREIGN KEY (resource_id) REFERENCES icloud_resources(id) ON DELETE CASCADE,
    CONSTRAINT fk_icloud_alias_routes_alias
        FOREIGN KEY (alias_id, resource_id)
        REFERENCES icloud_aliases(id, resource_id) ON DELETE CASCADE,
    CONSTRAINT chk_icloud_alias_routes_required
        CHECK (forward_to_email <> '' AND recipient_mail_id <> '')
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

ALTER TABLE icloud_maintenance_runs
    DROP CHECK chk_icloud_maintenance_kind,
    ADD CONSTRAINT chk_icloud_maintenance_kind
        CHECK (kind IN ('validation', 'alias'));

DROP TABLE icloud_resource_channels;

ALTER TABLE icloud_resources
    DROP INDEX idx_icloud_resources_imap_sync,
    DROP INDEX idx_icloud_resources_provision,
    DROP COLUMN next_provision_at,
    DROP COLUMN imap_last_sync_at,
    DROP COLUMN imap_last_uid,
    DROP COLUMN imap_uid_validity,
    DROP COLUMN imap_app_password;
