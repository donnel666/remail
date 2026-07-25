-- +goose Up

ALTER TABLE recharges
    DROP INDEX idx_recharges_one_pending_per_user,
    DROP COLUMN pending_user_id;

-- +goose Down

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
    ADD UNIQUE INDEX idx_recharges_one_pending_per_user (pending_user_id);
