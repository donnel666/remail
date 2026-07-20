-- +goose Up

ALTER TABLE api_keys
    MODIFY COLUMN concurrency_limit INT NULL DEFAULT NULL;

-- +goose Down

UPDATE api_keys
SET concurrency_limit = 5
WHERE concurrency_limit IS NULL;

ALTER TABLE api_keys
    MODIFY COLUMN concurrency_limit INT NOT NULL DEFAULT 5;
