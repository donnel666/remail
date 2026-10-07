-- +goose Up
ALTER TABLE icloud_resource_credentials
    MODIFY COLUMN birthday DATE NULL DEFAULT NULL;

-- +goose Down
-- A rollback fails while accounts without a birthday remain; never invent dates.
ALTER TABLE icloud_resource_credentials
    MODIFY COLUMN birthday DATE NOT NULL;
