-- +goose Up

ALTER TABLE explicit_aliases
    ADD COLUMN email_domain VARCHAR(255)
        GENERATED ALWAYS AS (
            SUBSTRING_INDEX(LOWER(TRIM(email)), '@', -1)
        ) VIRTUAL,
    ADD COLUMN alloc_bucket SMALLINT UNSIGNED
        GENERATED ALWAYS AS (MOD(resource_id, 2048)) VIRTUAL,
    ALGORITHM=INSTANT;

-- +goose Down

ALTER TABLE explicit_aliases
    DROP COLUMN alloc_bucket,
    DROP COLUMN email_domain,
    ALGORITHM=INSTANT;
