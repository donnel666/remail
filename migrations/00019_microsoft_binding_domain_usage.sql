-- +goose Up

ALTER TABLE microsoft_binding_mailboxes
    ADD COLUMN active_binding_domain VARCHAR(255)
        GENERATED ALWAYS AS (
            CASE
                WHEN status IN ('pending', 'code_sent', 'verified', 'timeout', 'failed')
                THEN LOWER(SUBSTRING_INDEX(binding_address, '@', -1))
                ELSE NULL
            END
        ) STORED AFTER active_binding_address,
    ADD INDEX idx_microsoft_binding_active_domain (active_binding_domain);

-- +goose Down

ALTER TABLE microsoft_binding_mailboxes
    DROP INDEX idx_microsoft_binding_active_domain,
    DROP COLUMN active_binding_domain;
