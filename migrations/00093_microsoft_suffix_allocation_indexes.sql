-- +goose Up

ALTER TABLE microsoft_resources
    ADD INDEX idx_microsoft_suffix_bucket (email_domain, alloc_bucket),
    ALGORITHM=INPLACE,
    LOCK=NONE;

-- +goose Down

ALTER TABLE microsoft_resources
    DROP INDEX idx_microsoft_suffix_bucket,
    ALGORITHM=INPLACE,
    LOCK=NONE;
