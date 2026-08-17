-- +goose Up

ALTER TABLE icloud_account_onboarding_tasks
    ADD COLUMN icloud_activation_confirmed_at DATETIME(3) NULL AFTER finished_at;

CREATE TABLE icloud_apple_id_reservations (
    email_key VARCHAR(320) NOT NULL PRIMARY KEY
        COMMENT 'normalized Apple ID reserved across onboarding and Cookie imports',
    owner_kind VARCHAR(32) NOT NULL
        COMMENT 'onboarding|cookie_import',
    owner_id BIGINT UNSIGNED NOT NULL,
    created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),

    INDEX idx_icloud_apple_id_reservation_owner (owner_kind, owner_id),
    CONSTRAINT chk_icloud_apple_id_reservation_owner_kind
        CHECK (owner_kind IN ('onboarding', 'cookie_import'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

INSERT INTO icloud_apple_id_reservations (email_key, owner_kind, owner_id, created_at)
SELECT LOWER(TRIM(primary_email)), 'onboarding', import_id, created_at
FROM icloud_account_onboarding_tasks
WHERE task_kind = 'onboarding'
  AND status IN ('processing', 'waiting')
  AND import_id IS NOT NULL;

-- +goose Down

DROP TABLE icloud_apple_id_reservations;

ALTER TABLE icloud_account_onboarding_tasks
    DROP COLUMN icloud_activation_confirmed_at;
