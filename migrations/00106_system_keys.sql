-- +goose Up

CREATE TABLE system_keys (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    name VARCHAR(120) NOT NULL,
    key_prefix VARCHAR(20) NOT NULL,
    key_hash CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    last_used_at DATETIME(3) NULL,
    deleted_at DATETIME(3) NULL,
    created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),

    UNIQUE INDEX uk_system_keys_hash (key_hash),
    INDEX idx_system_keys_active_created (deleted_at, created_at, id),

    CONSTRAINT chk_system_keys_name CHECK (name <> '' AND key_prefix <> '' AND key_hash <> '')
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

ALTER TABLE icloud_import_preparations
    MODIFY COLUMN operator_user_id BIGINT UNSIGNED NULL,
    ADD COLUMN system_key_id BIGINT UNSIGNED NULL AFTER operator_user_id,
    ADD INDEX idx_icloud_import_preparations_system_key_expiry
        (system_key_id, expires_at, id),
    ADD CONSTRAINT fk_icloud_import_preparations_system_key
        FOREIGN KEY (system_key_id) REFERENCES system_keys(id) ON DELETE RESTRICT,
    ADD CONSTRAINT chk_icloud_import_preparations_owner CHECK (
        (operator_user_id IS NOT NULL AND system_key_id IS NULL)
        OR (operator_user_id IS NULL AND system_key_id IS NOT NULL)
    );

-- +goose Down

DELETE FROM icloud_import_preparations WHERE system_key_id IS NOT NULL;

ALTER TABLE icloud_import_preparations
    DROP CHECK chk_icloud_import_preparations_owner,
    DROP FOREIGN KEY fk_icloud_import_preparations_system_key,
    DROP INDEX idx_icloud_import_preparations_system_key_expiry,
    DROP COLUMN system_key_id,
    MODIFY COLUMN operator_user_id BIGINT UNSIGNED NOT NULL;

DROP TABLE system_keys;
