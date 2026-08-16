-- +goose Up

ALTER TABLE kitesim_accounts
    ADD COLUMN deleted_at DATETIME(3) NULL AFTER sync_attempts,
    ADD INDEX idx_kitesim_accounts_deleted (deleted_at, id);

ALTER TABLE kitesim_phones
    ADD COLUMN disabled_at DATETIME(3) NULL AFTER refund_time,
    ADD COLUMN deleted_at DATETIME(3) NULL AFTER disabled_at,
    ADD INDEX idx_kitesim_phones_lifecycle (account_id, deleted_at, disabled_at, id);

-- +goose Down

ALTER TABLE kitesim_accounts
    DROP INDEX idx_kitesim_accounts_deleted,
    DROP COLUMN deleted_at;

ALTER TABLE kitesim_phones
    DROP INDEX idx_kitesim_phones_lifecycle,
    DROP COLUMN deleted_at,
    DROP COLUMN disabled_at;
