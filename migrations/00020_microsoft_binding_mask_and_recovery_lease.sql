-- +goose Up

ALTER TABLE microsoft_binding_mailboxes
    DROP INDEX idx_microsoft_binding_active_address,
    MODIFY COLUMN active_binding_address VARCHAR(320)
        GENERATED ALWAYS AS (
            CASE
                WHEN status IN ('pending', 'code_sent', 'verified', 'timeout', 'failed')
                  AND LOCATE('*', SUBSTRING_INDEX(binding_address, '@', 1)) = 0
                THEN binding_address
                ELSE NULL
            END
        ) STORED,
    ADD UNIQUE INDEX idx_microsoft_binding_active_address (active_binding_address);

UPDATE microsoft_binding_mailboxes
SET binding_address = LOWER(TRIM(bound_display)),
    bound_display = ''
WHERE LOCATE('*', SUBSTRING_INDEX(LOWER(TRIM(bound_display)), '@', 1)) > 0
  AND LENGTH(LOWER(TRIM(bound_display))) - LENGTH(REPLACE(LOWER(TRIM(bound_display)), '@', '')) = 1
  AND SUBSTRING_INDEX(LOWER(TRIM(bound_display)), '@', 1) <> ''
  AND SUBSTRING_INDEX(LOWER(TRIM(bound_display)), '@', -1) <> ''
  AND SUBSTRING_INDEX(LOWER(TRIM(bound_display)), '@', 1) NOT REGEXP '[[:space:]]';

CREATE TABLE microsoft_binding_recovery_leases (
    normalized_mask VARCHAR(320) PRIMARY KEY,
    claim_token CHAR(32) NOT NULL,
    lease_until DATETIME(6) NOT NULL,
    resource_id BIGINT UNSIGNED NOT NULL,
    sent_at DATETIME(6) NULL,
    created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
    INDEX idx_microsoft_binding_recovery_lease_until (lease_until),
    INDEX idx_microsoft_binding_recovery_resource (resource_id),
    CONSTRAINT fk_microsoft_binding_recovery_resource
        FOREIGN KEY (resource_id) REFERENCES microsoft_resources(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- +goose Down

DROP TABLE microsoft_binding_recovery_leases;

-- The old schema made every active binding_address unique and could not
-- represent duplicate Microsoft masks. Preserve the masks as legacy display
-- evidence and expire those rows before restoring that uniqueness rule.
UPDATE microsoft_binding_mailboxes
SET bound_display = binding_address,
    status = 'expired'
WHERE LOCATE('*', SUBSTRING_INDEX(binding_address, '@', 1)) > 0;

ALTER TABLE microsoft_binding_mailboxes
    DROP INDEX idx_microsoft_binding_active_address,
    MODIFY COLUMN active_binding_address VARCHAR(320)
        GENERATED ALWAYS AS (
            CASE
                WHEN status IN ('pending', 'code_sent', 'verified', 'timeout', 'failed')
                THEN binding_address
                ELSE NULL
            END
        ) STORED,
    ADD UNIQUE INDEX idx_microsoft_binding_active_address (active_binding_address);
