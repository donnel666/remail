-- +goose Up

ALTER TABLE icloud_resources
    ADD COLUMN alloc_bucket SMALLINT UNSIGNED
        GENERATED ALWAYS AS (MOD(CRC32(CAST(id AS CHAR)), 256)) VIRTUAL,
    ALGORITHM=INSTANT;

-- +goose Down

ALTER TABLE icloud_resources
    DROP COLUMN alloc_bucket,
    ALGORITHM=INSTANT;
