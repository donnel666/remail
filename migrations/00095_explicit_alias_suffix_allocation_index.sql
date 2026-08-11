-- +goose Up

ALTER TABLE explicit_aliases
    ADD INDEX idx_explicit_aliases_suffix_bucket (
        email_domain,
        alloc_bucket,
        status,
        resource_id
    ),
    ALGORITHM=INPLACE,
    LOCK=NONE;

-- +goose Down

ALTER TABLE explicit_aliases
    DROP INDEX idx_explicit_aliases_suffix_bucket,
    ALGORITHM=INPLACE,
    LOCK=NONE;
