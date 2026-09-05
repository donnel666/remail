-- +goose Up

ALTER TABLE gmail_resources
    DROP CHECK chk_gmail_resources_status,
    MODIFY COLUMN status VARCHAR(24) NOT NULL DEFAULT 'available'
        COMMENT 'available|disabled|leased|sold|pending|validating|identifying|normal|cooldown|abnormal|deleted',
    ADD CONSTRAINT chk_gmail_resources_status CHECK (
        status IN ('available', 'disabled', 'leased', 'sold',
                   'pending', 'validating', 'identifying', 'normal', 'cooldown', 'abnormal', 'deleted')
    );

-- +goose Down

UPDATE gmail_resources SET status = 'available' WHERE status = 'cooldown';

ALTER TABLE gmail_resources
    DROP CHECK chk_gmail_resources_status,
    MODIFY COLUMN status VARCHAR(24) NOT NULL DEFAULT 'available'
        COMMENT 'available|disabled|leased|sold|pending|validating|identifying|normal|abnormal|deleted',
    ADD CONSTRAINT chk_gmail_resources_status CHECK (
        status IN ('available', 'disabled', 'leased', 'sold',
                   'pending', 'validating', 'identifying', 'normal', 'abnormal', 'deleted')
    );
