-- +goose Up

ALTER TABLE lotteries
    ADD COLUMN lottery_type VARCHAR(16) NOT NULL DEFAULT 'fixed' AFTER title,
    ADD COLUMN starting_amount DECIMAL(18,6) NOT NULL DEFAULT 0 AFTER lottery_type,
    ADD COLUMN pool_increment_amount DECIMAL(18,6) NOT NULL DEFAULT 0 AFTER total_amount;

UPDATE lotteries
SET starting_amount = total_amount
WHERE starting_amount = 0;

ALTER TABLE lotteries
    ADD CONSTRAINT chk_lotteries_pool CHECK (
        lottery_type IN ('fixed', 'growing')
        AND starting_amount > 0
        AND pool_increment_amount >= 0
        AND (
            (lottery_type = 'fixed' AND pool_increment_amount = 0 AND total_amount = starting_amount)
            OR (lottery_type = 'growing' AND pool_increment_amount > 0 AND total_amount >= starting_amount)
        )
    );

-- +goose Down

ALTER TABLE lotteries
    DROP CHECK chk_lotteries_pool,
    DROP COLUMN pool_increment_amount,
    DROP COLUMN starting_amount,
    DROP COLUMN lottery_type;
