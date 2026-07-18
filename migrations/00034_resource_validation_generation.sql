-- +goose Up

ALTER TABLE microsoft_resources
    ADD COLUMN validation_generation BIGINT UNSIGNED NOT NULL DEFAULT 1 AFTER status,
    ADD COLUMN validation_failures TINYINT UNSIGNED NOT NULL DEFAULT 0 AFTER validation_generation,
    ADD CONSTRAINT chk_microsoft_validation_failures CHECK (validation_failures <= 3);

ALTER TABLE domain_resources
    ADD COLUMN validation_generation BIGINT UNSIGNED NOT NULL DEFAULT 1 AFTER status,
    ADD COLUMN validation_failures TINYINT UNSIGNED NOT NULL DEFAULT 0 AFTER validation_generation,
    ADD CONSTRAINT chk_domain_validation_failures CHECK (validation_failures <= 3);

-- Tasks from the previous application image do not carry a generation. Fence
-- them and release only the assignments that were in flight at deployment.
UPDATE microsoft_resources
SET status = 'pending', validation_generation = validation_generation + 1
WHERE status = 'validating';

UPDATE domain_resources
SET status = 'pending', validation_generation = validation_generation + 1
WHERE status = 'validating';

-- +goose Down

ALTER TABLE microsoft_resources
    DROP CHECK chk_microsoft_validation_failures,
    DROP COLUMN validation_failures,
    DROP COLUMN validation_generation;

ALTER TABLE domain_resources
    DROP CHECK chk_domain_validation_failures,
    DROP COLUMN validation_failures,
    DROP COLUMN validation_generation;
