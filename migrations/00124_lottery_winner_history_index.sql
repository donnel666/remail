-- +goose Up

ALTER TABLE lottery_payouts
    ADD INDEX idx_lottery_payouts_user_tier (user_id, tier);

-- +goose Down

ALTER TABLE lottery_payouts
    DROP INDEX idx_lottery_payouts_user_tier;
