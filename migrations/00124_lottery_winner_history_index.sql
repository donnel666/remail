-- +goose Up

ALTER TABLE lottery_payouts
    ADD INDEX idx_lottery_payouts_user_tier (user_id, tier);

-- +goose Down

-- MySQL may use the new composite index as the supporting index for the
-- existing user foreign key. Recreate that FK after removing the index so the
-- rollback does not fail with ER_DROP_INDEX_FK.
ALTER TABLE lottery_payouts
    DROP FOREIGN KEY fk_lottery_payouts_user;

ALTER TABLE lottery_payouts
    DROP INDEX idx_lottery_payouts_user_tier;

ALTER TABLE lottery_payouts
    ADD CONSTRAINT fk_lottery_payouts_user
        FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE RESTRICT;
