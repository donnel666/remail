-- +goose Up

ALTER TABLE icloud_resources
    ADD COLUMN family_id VARCHAR(128) NOT NULL DEFAULT '' AFTER family_invite_url,
    ADD COLUMN family_organizer_dsid VARCHAR(64) NOT NULL DEFAULT '' AFTER family_id,
    ADD COLUMN family_remote_member_count TINYINT UNSIGNED NOT NULL DEFAULT 0 AFTER family_organizer_dsid,
    ADD COLUMN family_sync_status VARCHAR(16) NOT NULL DEFAULT 'unknown'
        COMMENT 'unknown|ready|failed|inactive' AFTER family_remote_member_count,
    ADD COLUMN family_synced_at DATETIME(3) NULL AFTER family_sync_status,
    ADD COLUMN family_sync_error_category VARCHAR(64) NOT NULL DEFAULT '' AFTER family_synced_at,
    ADD INDEX idx_icloud_resources_family_capacity
        (account_role, country_code, family_sync_status, family_synced_at, id),
    ADD CONSTRAINT chk_icloud_resources_family_count
        CHECK (family_remote_member_count <= 5),
    ADD CONSTRAINT chk_icloud_resources_family_sync_status
        CHECK (family_sync_status IN ('unknown', 'ready', 'failed', 'inactive'));

ALTER TABLE icloud_account_onboarding_tasks
    ADD COLUMN family_reservation_confirmed TINYINT(1) NOT NULL DEFAULT 0 AFTER family_primary_resource_id,
    ADD INDEX idx_icloud_onboard_task_family_reservation
        (family_primary_resource_id, status, family_reservation_confirmed, id);

-- +goose Down

ALTER TABLE icloud_account_onboarding_tasks
    ADD INDEX fk_icloud_onboard_task_family_primary (family_primary_resource_id),
    DROP INDEX idx_icloud_onboard_task_family_reservation,
    DROP COLUMN family_reservation_confirmed;

ALTER TABLE icloud_resources
    DROP CHECK chk_icloud_resources_family_sync_status,
    DROP CHECK chk_icloud_resources_family_count,
    DROP INDEX idx_icloud_resources_family_capacity,
    DROP COLUMN family_sync_error_category,
    DROP COLUMN family_synced_at,
    DROP COLUMN family_sync_status,
    DROP COLUMN family_remote_member_count,
    DROP COLUMN family_organizer_dsid,
    DROP COLUMN family_id;
