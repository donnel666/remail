-- +goose Up

-- MySQL commits this combined ALTER atomically, before Goose records the
-- migration version. request_count marks completed DDL and must survive retries.
DROP PROCEDURE IF EXISTS migrate_api_key_point_quota_00139;

-- +goose StatementBegin
CREATE PROCEDURE migrate_api_key_point_quota_00139()
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = DATABASE() AND table_name = 'api_keys' AND column_name = 'request_count'
    ) THEN
        ALTER TABLE api_keys
            DROP CHECK chk_api_keys_limits,
            CHANGE COLUMN quota_used request_count BIGINT UNSIGNED NOT NULL DEFAULT 0,
            ADD COLUMN quota_used DECIMAL(18,6) NOT NULL DEFAULT 0 AFTER quota_limit,
            ADD CONSTRAINT chk_api_keys_limits CHECK (
                (concurrency_limit IS NULL OR concurrency_limit > 0)
                AND (quota_limit IS NULL OR quota_limit > 0)
                AND quota_used >= 0
            );
    END IF;
END;
-- +goose StatementEnd

CALL migrate_api_key_point_quota_00139();
DROP PROCEDURE migrate_api_key_point_quota_00139;

-- A request count has no conversion rate to points. Rebuild net spending from
-- the actual debit/refund transactions, including keys deleted after ordering.
-- Existing limits keep their numeric values, now in points; a key whose past
-- spending exceeds its limit has zero remaining quota until the limit changes.
UPDATE api_keys AS k
LEFT JOIN (
    SELECT o.api_key_id,
           SUM(-COALESCE(d.amount, 0) - COALESCE(r.amount, 0)) AS points_used
    FROM orders AS o
    LEFT JOIN wallet_transactions AS d ON d.id = o.debit_tx_id
    LEFT JOIN wallet_transactions AS r ON r.id = o.refund_tx_id
    WHERE o.api_key_id IS NOT NULL
    GROUP BY o.api_key_id
) AS usage_by_key ON usage_by_key.api_key_id = k.id
SET k.quota_used = GREATEST(COALESCE(usage_by_key.points_used, 0), 0);

-- +goose Down
SIGNAL SQLSTATE '45000'
    SET MESSAGE_TEXT = 'API key point quotas cannot be rolled back to request quotas';
