-- +goose Up

ALTER TABLE gmail_resources
    DROP FOREIGN KEY fk_gmail_resources_root,
    DROP CHECK chk_gmail_resources_status;

ALTER TABLE email_resources
    ADD UNIQUE INDEX idx_email_resources_id_owner (id, owner_user_id);

ALTER TABLE gmail_resources
    MODIFY COLUMN status VARCHAR(24) NOT NULL DEFAULT 'available'
        COMMENT 'available|disabled|leased|sold|pending|validating|normal|abnormal|deleted',
    ADD CONSTRAINT chk_gmail_resources_status CHECK (
        status IN ('available', 'disabled', 'leased', 'sold',
                   'pending', 'validating', 'normal', 'abnormal', 'deleted')
    ),
    ADD CONSTRAINT fk_gmail_resources_root_type
        FOREIGN KEY (id, resource_type)
        REFERENCES email_resources(id, type) ON DELETE CASCADE,
    ADD CONSTRAINT fk_gmail_resources_root_owner
        FOREIGN KEY (id, owner_user_id)
        REFERENCES email_resources(id, owner_user_id)
        ON UPDATE CASCADE ON DELETE CASCADE;

-- +goose Down

DROP TEMPORARY TABLE IF EXISTS gmail_deleted_restore_down_guard;
CREATE TEMPORARY TABLE gmail_deleted_restore_down_guard (
    deleted_rows BIGINT NOT NULL,
    CONSTRAINT chk_gmail_deleted_restore_down_guard CHECK (deleted_rows = 0)
);
INSERT INTO gmail_deleted_restore_down_guard (deleted_rows)
SELECT COUNT(*) FROM gmail_resources WHERE status = 'deleted';
DROP TEMPORARY TABLE gmail_deleted_restore_down_guard;

ALTER TABLE gmail_resources
    DROP FOREIGN KEY fk_gmail_resources_root_type,
    DROP FOREIGN KEY fk_gmail_resources_root_owner,
    DROP CHECK chk_gmail_resources_status;

ALTER TABLE gmail_resources
    MODIFY COLUMN status VARCHAR(24) NOT NULL DEFAULT 'available'
        COMMENT 'available|disabled|leased|sold|pending|validating|normal|abnormal',
    ADD CONSTRAINT chk_gmail_resources_status CHECK (
        status IN ('available', 'disabled', 'leased', 'sold',
                   'pending', 'validating', 'normal', 'abnormal')
    ),
    ADD CONSTRAINT fk_gmail_resources_root
        FOREIGN KEY (id, resource_type, owner_user_id)
        REFERENCES email_resources(id, type, owner_user_id) ON DELETE CASCADE;

ALTER TABLE email_resources
    DROP INDEX idx_email_resources_id_owner;
