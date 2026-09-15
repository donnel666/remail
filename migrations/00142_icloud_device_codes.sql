-- +goose Up
ALTER TABLE icloud_resources
    ADD COLUMN device_code_api TEXT NULL,
    ADD COLUMN device_bind_status VARCHAR(16) NOT NULL DEFAULT '',
    ADD COLUMN device_account_id VARCHAR(128) NOT NULL DEFAULT '';

-- Binding also precedes resource creation in the standalone validator. This
-- durable queue lets all entrypoints share one device-status polling task.
CREATE TABLE icloud_device_bindings (
    email VARCHAR(320) NOT NULL PRIMARY KEY,
    resource_id BIGINT UNSIGNED NULL,
    phone_id BIGINT UNSIGNED NOT NULL,
    phone VARCHAR(32) NOT NULL,
    password TEXT NULL,
    identity_hash CHAR(64) NOT NULL,
    generation BIGINT UNSIGNED NOT NULL,
    status VARCHAR(16) NOT NULL,
    remote_id VARCHAR(128) NOT NULL DEFAULT '',
    code_api TEXT NULL,
    callback_token VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NULL,
    challenge_id BIGINT UNSIGNED NOT NULL DEFAULT 0,
    submit_at DATETIME(3) NOT NULL,
    submitted_at DATETIME(3) NULL,
    deadline_at DATETIME(3) NOT NULL,
    last_error VARCHAR(500) NOT NULL DEFAULT '',
    created_at DATETIME(3) NOT NULL,
    updated_at DATETIME(3) NOT NULL,
    UNIQUE INDEX uk_icloud_device_callback (callback_token),
    INDEX idx_icloud_device_dispatch (status, submit_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- +goose Down
DROP TABLE icloud_device_bindings;
ALTER TABLE icloud_resources
    DROP COLUMN device_code_api,
    DROP COLUMN device_bind_status,
    DROP COLUMN device_account_id;
