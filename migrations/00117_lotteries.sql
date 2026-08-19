-- +goose Up

CREATE TABLE lotteries (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    public_token VARCHAR(64) NOT NULL,
    created_by_user_id BIGINT UNSIGNED NOT NULL,
    funding_user_id BIGINT UNSIGNED NOT NULL,
    title VARCHAR(120) NOT NULL,
    total_amount DECIMAL(18,6) NOT NULL,
    min_payout DECIMAL(18,6) NOT NULL,
    max_payout DECIMAL(18,6) NOT NULL,
    tier_weights JSON NOT NULL,
    min_account_age_days INT UNSIGNED NOT NULL DEFAULT 0,
    draw_at DATETIME(3) NULL,
    participant_target INT UNSIGNED NULL,
    participant_count INT UNSIGNED NOT NULL DEFAULT 0,
    max_participants INT UNSIGNED NOT NULL,
    status VARCHAR(16) NOT NULL,
    triggered_by VARCHAR(16) NOT NULL DEFAULT '',
    target_reached_at DATETIME(3) NULL,
    fund_transaction_no VARCHAR(64) NOT NULL DEFAULT '',
    algorithm_version VARCHAR(32) NOT NULL,
    refund_amount DECIMAL(18,6) NOT NULL DEFAULT 0,
    idempotency_key VARCHAR(128) NOT NULL,
    funding_error VARCHAR(255) NOT NULL DEFAULT '',
    settled_at DATETIME(3) NULL,
    created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),

    UNIQUE INDEX uk_lotteries_public_token (public_token),
    UNIQUE INDEX uk_lotteries_creator_idempotency (created_by_user_id, idempotency_key),
    INDEX idx_lotteries_status_draw (status, draw_at, id),
    INDEX idx_lotteries_status_target (status, participant_count, participant_target, id),
    INDEX idx_lotteries_funding (status, id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE lottery_entries (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    lottery_id BIGINT UNSIGNED NOT NULL,
    user_id BIGINT UNSIGNED NOT NULL,
    registered_at DATETIME(3) NOT NULL,
    created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),

    UNIQUE INDEX uk_lottery_entries_user (lottery_id, user_id),
    INDEX idx_lottery_entries_lottery (lottery_id, id),
    INDEX idx_lottery_entries_user (user_id, id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE lottery_payouts (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    lottery_id BIGINT UNSIGNED NOT NULL,
    user_id BIGINT UNSIGNED NOT NULL,
    tier VARCHAR(16) NOT NULL,
    amount DECIMAL(18,6) NOT NULL,
    billing_transaction_no VARCHAR(64) NOT NULL DEFAULT '',
    mail_queued_at DATETIME(3) NULL,
    created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),

    UNIQUE INDEX uk_lottery_payouts_user (lottery_id, user_id),
    INDEX idx_lottery_payouts_lottery (lottery_id, id),
    INDEX idx_lottery_payouts_mail (mail_queued_at, id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- +goose Down

DROP TABLE lottery_payouts;
DROP TABLE lottery_entries;
DROP TABLE lotteries;
