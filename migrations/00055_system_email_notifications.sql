-- +goose Up

ALTER TABLE wallets
    ADD COLUMN balance_warning_level TINYINT UNSIGNED NOT NULL DEFAULT 4 AFTER spend_count,
    ADD COLUMN balance_warning_cycle BIGINT UNSIGNED NOT NULL DEFAULT 0 AFTER balance_warning_level,
    ADD CONSTRAINT chk_wallets_balance_warning_level CHECK (balance_warning_level <= 4),
    ADD INDEX idx_wallets_balance_warning_dispatch (consumer_balance, balance_warning_level, updated_at);

UPDATE wallets SET balance_warning_level = 0 WHERE consumer_balance > 3;

-- +goose Down

ALTER TABLE wallets
    DROP INDEX idx_wallets_balance_warning_dispatch,
    DROP CHECK chk_wallets_balance_warning_level,
    DROP COLUMN balance_warning_cycle,
    DROP COLUMN balance_warning_level;
