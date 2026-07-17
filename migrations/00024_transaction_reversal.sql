-- +goose Up

ALTER TABLE wallet_transactions
    ADD COLUMN reversal_of_no VARCHAR(64) NULL AFTER biz_id,
    ADD INDEX idx_wallet_transactions_reversal_of_no (reversal_of_no);

-- +goose Down

ALTER TABLE wallet_transactions
    DROP INDEX idx_wallet_transactions_reversal_of_no,
    DROP COLUMN reversal_of_no;
