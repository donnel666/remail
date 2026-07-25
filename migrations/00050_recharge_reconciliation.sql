-- +goose Up

ALTER TABLE recharges
    ADD COLUMN gateway_trade_no VARCHAR(64) NULL AFTER status,
    ADD COLUMN gateway_config_hash CHAR(64) NOT NULL DEFAULT '' AFTER gateway_trade_no,
    ADD COLUMN failure_reason VARCHAR(64) NOT NULL DEFAULT '' AFTER gateway_config_hash,
    ADD COLUMN query_attempts INT NOT NULL DEFAULT 0 AFTER failure_reason,
    ADD COLUMN last_queried_at DATETIME NULL AFTER query_attempts,
    ADD COLUMN query_generation INT NOT NULL DEFAULT 0 AFTER last_queried_at,
    ADD COLUMN query_lease_until DATETIME NULL AFTER query_generation,
    ADD COLUMN gateway_config_snapshot LONGTEXT NULL AFTER query_lease_until,
    ADD COLUMN paid_at DATETIME NULL AFTER gateway_config_snapshot,
    ADD COLUMN reconciled_at DATETIME NULL AFTER paid_at;

UPDATE recharges AS stale
JOIN recharges AS newer
  ON newer.user_id = stale.user_id
 AND newer.status IN ('paying', 'callback', 'reconciled')
 AND stale.status IN ('paying', 'callback', 'reconciled')
 AND (newer.created_at > stale.created_at OR (newer.created_at = stale.created_at AND newer.id > stale.id))
SET stale.status = 'failed',
    stale.failure_reason = 'migration_duplicate_pending',
    stale.reconciled_at = UTC_TIMESTAMP(),
    stale.updated_at = UTC_TIMESTAMP();

ALTER TABLE recharges
    ADD COLUMN pending_user_id BIGINT UNSIGNED GENERATED ALWAYS AS (
        CASE WHEN status IN ('paying', 'callback', 'reconciled') THEN user_id ELSE NULL END
    ) STORED AFTER reconciled_at,
    ADD UNIQUE INDEX idx_recharges_gateway_trade_no (gateway_trade_no),
    ADD UNIQUE INDEX idx_recharges_one_pending_per_user (pending_user_id),
    ADD INDEX idx_recharges_reconcile_due (status, last_queried_at, created_at, id),
    ADD CONSTRAINT chk_recharges_query_attempts CHECK (query_attempts >= 0),
    ADD CONSTRAINT chk_recharges_query_generation CHECK (query_generation >= 0);

-- +goose Down

ALTER TABLE recharges
    DROP CONSTRAINT chk_recharges_query_generation,
    DROP CONSTRAINT chk_recharges_query_attempts,
    DROP INDEX idx_recharges_reconcile_due,
    DROP INDEX idx_recharges_one_pending_per_user,
    DROP INDEX idx_recharges_gateway_trade_no,
    DROP COLUMN pending_user_id,
    DROP COLUMN reconciled_at,
    DROP COLUMN paid_at,
    DROP COLUMN gateway_config_snapshot,
    DROP COLUMN query_lease_until,
    DROP COLUMN query_generation,
    DROP COLUMN last_queried_at,
    DROP COLUMN query_attempts,
    DROP COLUMN failure_reason,
    DROP COLUMN gateway_config_hash,
    DROP COLUMN gateway_trade_no;
