-- +goose Up

ALTER TABLE icloud_resource_channels
    ADD COLUMN setup_cookie TEXT NOT NULL DEFAULT ('') AFTER cookie;

UPDATE icloud_resource_channels
SET setup_cookie = cookie
WHERE kind = 'icloud_web' AND TRIM(setup_cookie) = '';

-- +goose Down

ALTER TABLE icloud_resource_channels
    DROP COLUMN setup_cookie;
