-- +goose Up

ALTER TABLE wallets
    ADD COLUMN total_recharged DECIMAL(18,6) NOT NULL DEFAULT 0.000000 AFTER supplier_frozen;

UPDATE wallets w
LEFT JOIN (
    SELECT wt.user_id, COALESCE(SUM(wt.amount), 0) AS total_recharged
    FROM wallet_transactions wt
    WHERE wt.transaction_type IN ('recharge', 'card_redeem')
      AND wt.reversal_of_no IS NULL
      AND NOT EXISTS (
          SELECT 1
          FROM wallet_transactions reversal
          WHERE reversal.reversal_of_no = wt.transaction_no
      )
    GROUP BY wt.user_id
) totals ON totals.user_id = w.user_id
SET w.total_recharged = COALESCE(totals.total_recharged, 0);

ALTER TABLE wallets
    ADD CONSTRAINT chk_wallets_total_recharged_nonnegative CHECK (total_recharged >= 0);

CREATE TABLE membership_upgrade_targets_00057 (
    user_id BIGINT UNSIGNED PRIMARY KEY,
    user_group_id BIGINT UNSIGNED NOT NULL
) ENGINE=InnoDB;

INSERT INTO membership_upgrade_targets_00057 (user_id, user_group_id)
SELECT user_id, user_group_id
FROM (
    SELECT
        u.id AS user_id,
        candidate.id AS user_group_id,
        ROW_NUMBER() OVER (
            PARTITION BY u.id
            ORDER BY candidate.topup_threshold DESC, candidate.id DESC
        ) AS row_rank
    FROM users u
    JOIN wallets w ON w.user_id = u.id
    JOIN user_groups current_group ON current_group.id = u.user_group_id
    JOIN user_groups candidate
      ON candidate.enabled = 1
     AND candidate.auto_upgrade_enabled = 1
     AND candidate.topup_threshold > current_group.topup_threshold
     AND candidate.topup_threshold <= w.total_recharged
) ranked
WHERE row_rank = 1;

UPDATE users u
JOIN membership_upgrade_targets_00057 target ON target.user_id = u.id
SET u.user_group_id = target.user_group_id,
    u.updated_at = CURRENT_TIMESTAMP;

DROP TABLE membership_upgrade_targets_00057;

-- +goose Down

ALTER TABLE wallets
    DROP CHECK chk_wallets_total_recharged_nonnegative,
    DROP COLUMN total_recharged;
