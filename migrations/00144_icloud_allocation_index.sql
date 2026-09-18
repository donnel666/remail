-- +goose Up

ALTER TABLE icloud_resources
    ADD INDEX idx_icloud_alloc_bucket (alloc_bucket, status, last_allocated_at, id, for_sale),
    ALGORITHM=INPLACE,
    LOCK=NONE;

-- +goose Down

ALTER TABLE icloud_resources
    DROP INDEX idx_icloud_alloc_bucket,
    ALGORITHM=INPLACE,
    LOCK=NONE;
