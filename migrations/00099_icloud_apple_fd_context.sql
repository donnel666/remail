-- +goose Up

ALTER TABLE icloud_resource_channels
    ADD COLUMN fd_client_info TEXT NULL AFTER user_agent;

UPDATE icloud_resource_channels
SET fd_client_info = ''
WHERE fd_client_info IS NULL;

ALTER TABLE icloud_resource_channels
    MODIFY COLUMN fd_client_info TEXT NOT NULL DEFAULT ('');

-- +goose Down

ALTER TABLE icloud_resource_channels
    DROP COLUMN fd_client_info;
