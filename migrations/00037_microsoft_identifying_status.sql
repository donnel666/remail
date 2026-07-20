-- +goose Up

ALTER TABLE microsoft_resources
    DROP CHECK chk_microsoft_status,
    MODIFY COLUMN status VARCHAR(32) NOT NULL DEFAULT 'pending'
        COMMENT 'pending|validating|identifying|normal|abnormal|disabled|deleted',
    ADD CONSTRAINT chk_microsoft_status
        CHECK (status IN ('pending', 'validating', 'identifying', 'normal', 'abnormal', 'disabled', 'deleted'));

-- +goose Down

UPDATE microsoft_resources
SET status = 'pending', validation_generation = validation_generation + 1
WHERE status = 'identifying';

ALTER TABLE microsoft_resources
    DROP CHECK chk_microsoft_status,
    MODIFY COLUMN status VARCHAR(32) NOT NULL DEFAULT 'pending'
        COMMENT 'pending|validating|normal|abnormal|disabled|deleted',
    ADD CONSTRAINT chk_microsoft_status
        CHECK (status IN ('pending', 'validating', 'normal', 'abnormal', 'disabled', 'deleted'));
