-- +goose Up

ALTER TABLE referral_rewards
    ADD COLUMN expires_at DATETIME NULL AFTER transferred_at;

-- +goose Down

ALTER TABLE referral_rewards
    DROP COLUMN expires_at;
