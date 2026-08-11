-- +goose Up

ALTER TABLE icloud_resource_imports
    ADD COLUMN resource_expire_at DATETIME(3) NULL AFTER error_strategy;

UPDATE icloud_resource_imports
SET resource_expire_at = DATE_ADD(created_at, INTERVAL 1 MONTH)
WHERE resource_expire_at IS NULL;

ALTER TABLE icloud_resource_imports
    MODIFY COLUMN resource_expire_at DATETIME(3) NOT NULL;

-- +goose Down

ALTER TABLE icloud_resource_imports
    DROP COLUMN resource_expire_at;
