-- +goose Up

ALTER TABLE lotteries
    CHANGE COLUMN refund_amount unused_amount DECIMAL(18,6) NOT NULL DEFAULT 0,
    ADD COLUMN request_fingerprint CHAR(64) NOT NULL DEFAULT '' AFTER idempotency_key,
    ADD CONSTRAINT fk_lotteries_creator FOREIGN KEY (created_by_user_id) REFERENCES users(id) ON DELETE RESTRICT,
    ADD CONSTRAINT fk_lotteries_legacy_funding_user FOREIGN KEY (funding_user_id) REFERENCES users(id) ON DELETE RESTRICT,
    ADD CONSTRAINT chk_lotteries_amounts CHECK (
        total_amount > 0 AND min_payout > 0 AND max_payout > min_payout
    ),
    ADD CONSTRAINT chk_lotteries_participants CHECK (
        participant_target IS NOT NULL
        AND participant_target > 0
        AND max_participants > 0
        AND participant_count <= participant_target
        AND participant_target <= max_participants
    ),
    ADD CONSTRAINT chk_lotteries_status CHECK (
        status IN ('funding', 'open', 'settling', 'completed', 'cancelled')
    ),
    ADD CONSTRAINT chk_lotteries_trigger CHECK (
        triggered_by IN ('', 'time', 'participants')
    );

ALTER TABLE lottery_entries
    ADD CONSTRAINT fk_lottery_entries_lottery FOREIGN KEY (lottery_id) REFERENCES lotteries(id) ON DELETE CASCADE,
    ADD CONSTRAINT fk_lottery_entries_user FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE RESTRICT;

ALTER TABLE lottery_payouts
    ADD CONSTRAINT fk_lottery_payouts_lottery FOREIGN KEY (lottery_id) REFERENCES lotteries(id) ON DELETE CASCADE,
    ADD CONSTRAINT fk_lottery_payouts_user FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE RESTRICT,
    ADD CONSTRAINT chk_lottery_payouts_amount CHECK (amount > 0),
    ADD CONSTRAINT chk_lottery_payouts_tier CHECK (tier IN ('consolation', 'normal', 'lucky'));

CREATE TABLE billing_lottery_settlements (
    lottery_id BIGINT UNSIGNED PRIMARY KEY,
    request_fingerprint CHAR(64) NOT NULL,
    response_json JSON NULL,
    created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),

    CONSTRAINT chk_billing_lottery_settlement_fingerprint CHECK (request_fingerprint <> '')
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- +goose Down

DROP TABLE billing_lottery_settlements;

ALTER TABLE lottery_payouts
    DROP CONSTRAINT chk_lottery_payouts_tier,
    DROP CONSTRAINT chk_lottery_payouts_amount,
    DROP FOREIGN KEY fk_lottery_payouts_user,
    DROP FOREIGN KEY fk_lottery_payouts_lottery;

ALTER TABLE lottery_entries
    DROP FOREIGN KEY fk_lottery_entries_user,
    DROP FOREIGN KEY fk_lottery_entries_lottery;

ALTER TABLE lotteries
    DROP CONSTRAINT chk_lotteries_trigger,
    DROP CONSTRAINT chk_lotteries_status,
    DROP CONSTRAINT chk_lotteries_participants,
    DROP CONSTRAINT chk_lotteries_amounts,
    DROP FOREIGN KEY fk_lotteries_legacy_funding_user,
    DROP FOREIGN KEY fk_lotteries_creator,
    DROP COLUMN request_fingerprint,
    CHANGE COLUMN unused_amount refund_amount DECIMAL(18,6) NOT NULL DEFAULT 0;
