-- +goose Up

ALTER TABLE wallets
    ADD COLUMN total_spend DECIMAL(18,2) NOT NULL DEFAULT 0.00 AFTER supplier_frozen,
    ADD COLUMN spend_count BIGINT UNSIGNED NOT NULL DEFAULT 0 AFTER total_spend;

UPDATE wallets w
LEFT JOIN (
    SELECT user_id, COALESCE(SUM(-amount), 0) AS total_spend, COUNT(*) AS spend_count
    FROM wallet_transactions
    WHERE balance_bucket = 'consumer' AND direction = 'out'
    GROUP BY user_id
) t ON t.user_id = w.user_id
SET w.total_spend = COALESCE(t.total_spend, 0),
    w.spend_count = COALESCE(t.spend_count, 0);

-- +goose Down

ALTER TABLE wallets
    DROP COLUMN spend_count,
    DROP COLUMN total_spend;
