-- +goose Up

CREATE TABLE daily_checkins (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    user_id BIGINT UNSIGNED NOT NULL,
    business_date DATE NOT NULL,
    reward_amount DECIMAL(18,6) NOT NULL DEFAULT 0,
    wallet_transaction_id BIGINT UNSIGNED NULL,
    checked_in_at DATETIME(3) NOT NULL,
    created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    UNIQUE INDEX idx_daily_checkins_user_date (user_id, business_date),
    CONSTRAINT fk_daily_checkins_user FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE RESTRICT,
    CONSTRAINT fk_daily_checkins_transaction FOREIGN KEY (wallet_transaction_id) REFERENCES wallet_transactions(id) ON DELETE RESTRICT,
    CONSTRAINT chk_daily_checkins_reward CHECK (reward_amount >= 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE leaderboard_settlements (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    business_date DATE NOT NULL,
    period_start DATETIME(3) NOT NULL,
    period_end DATETIME(3) NOT NULL,
    rules_snapshot JSON NOT NULL,
    status VARCHAR(32) NOT NULL DEFAULT 'completed',
    settled_at DATETIME(3) NOT NULL,
    created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    UNIQUE INDEX idx_leaderboard_settlements_date (business_date),
    CONSTRAINT chk_leaderboard_settlements_period CHECK (period_end > period_start),
    CONSTRAINT chk_leaderboard_settlements_status CHECK (status = 'completed')
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE leaderboard_rewards (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    settlement_id BIGINT UNSIGNED NOT NULL,
    user_id BIGINT UNSIGNED NOT NULL,
    rank_no INT UNSIGNED NOT NULL,
    score INT UNSIGNED NOT NULL,
    reward_amount DECIMAL(18,6) NOT NULL,
    wallet_transaction_id BIGINT UNSIGNED NOT NULL,
    created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    UNIQUE INDEX idx_leaderboard_rewards_user (settlement_id, user_id),
    UNIQUE INDEX idx_leaderboard_rewards_rank (settlement_id, rank_no),
    CONSTRAINT fk_leaderboard_rewards_settlement FOREIGN KEY (settlement_id) REFERENCES leaderboard_settlements(id) ON DELETE RESTRICT,
    CONSTRAINT fk_leaderboard_rewards_user FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE RESTRICT,
    CONSTRAINT fk_leaderboard_rewards_transaction FOREIGN KEY (wallet_transaction_id) REFERENCES wallet_transactions(id) ON DELETE RESTRICT,
    CONSTRAINT chk_leaderboard_rewards_values CHECK (rank_no > 0 AND score > 0 AND reward_amount > 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

ALTER TABLE mailmatch_order_delivery_heads
    ADD INDEX idx_mailmatch_delivery_heads_received (message_received_at, order_id);

ALTER TABLE orders
    ADD INDEX idx_orders_activated (activated_at, user_id);

-- +goose Down

ALTER TABLE orders DROP INDEX idx_orders_activated;
ALTER TABLE mailmatch_order_delivery_heads DROP INDEX idx_mailmatch_delivery_heads_received;
DROP TABLE IF EXISTS leaderboard_rewards;
DROP TABLE IF EXISTS leaderboard_settlements;
DROP TABLE IF EXISTS daily_checkins;
