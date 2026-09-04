-- +goose Up

ALTER TABLE mailmatch_admin_resource_fetch_states
    DROP CHECK chk_mailmatch_admin_fetch_operation,
    MODIFY COLUMN operation_kind VARCHAR(32) NOT NULL DEFAULT 'resource_fetch'
        COMMENT 'resource_fetch|gmail_resource_fetch|icloud_resource_fetch',
    ADD CONSTRAINT chk_mailmatch_admin_fetch_operation
        CHECK (operation_kind IN ('resource_fetch', 'gmail_resource_fetch', 'icloud_resource_fetch'));

ALTER TABLE gmail_resources
    ADD COLUMN provider_cursor BIGINT UNSIGNED NOT NULL DEFAULT 0
        AFTER credential_updated_at,
    ADD COLUMN provider_spam_cursor BIGINT UNSIGNED NOT NULL DEFAULT 0
        AFTER provider_cursor;

-- All Gmail code orders now use the shared order lifecycle. Keep legacy rows
-- for audit, but make every unfinished local session inert before new code runs.
UPDATE gmail_code_sessions
SET status = 'unknown',
    completed_at = COALESCE(completed_at, CURRENT_TIMESTAMP(3)),
    next_poll_at = NULL,
    last_safe_error = 'Legacy Gmail code session retired; order uses shared lifecycle.',
    version = version + 1
WHERE source = 'local'
  AND status NOT IN ('completed', 'cancelled', 'failed', 'unknown');

UPDATE gmail_code_sessions
SET next_poll_at = NULL
WHERE source = 'local'
  AND next_poll_at IS NOT NULL;

-- SMSBower is retired from Gmail fulfillment. Preserve completed history for
-- audit, but remove its credentials and disable every route before new code runs.
UPDATE smsbower_config SET enabled = 0, api_key = '' WHERE id = 1;
UPDATE smsbower_project_routes SET enabled = 0;
UPDATE gmail_supply_routes SET enabled = 0 WHERE source = 'smsbower';
DELETE FROM system_settings WHERE `key` LIKE 'smsbower%';

-- +goose Down

-- Retired local sessions intentionally remain inert: their former runtime
-- state cannot be reconstructed after shared lifecycle processing begins.

DELETE FROM mailmatch_admin_resource_fetch_states
WHERE operation_kind = 'gmail_resource_fetch';

UPDATE gmail_allocations AS allocation
JOIN gmail_resources AS resource ON resource.id = allocation.resource_id
SET allocation.provider_cursor = resource.provider_cursor,
    allocation.provider_spam_cursor = resource.provider_spam_cursor
WHERE allocation.source = 'local';

ALTER TABLE gmail_resources
    DROP COLUMN provider_spam_cursor,
    DROP COLUMN provider_cursor;

ALTER TABLE mailmatch_admin_resource_fetch_states
    DROP CHECK chk_mailmatch_admin_fetch_operation,
    MODIFY COLUMN operation_kind VARCHAR(32) NOT NULL DEFAULT 'resource_fetch'
        COMMENT 'resource_fetch|icloud_resource_fetch',
    ADD CONSTRAINT chk_mailmatch_admin_fetch_operation
        CHECK (operation_kind IN ('resource_fetch', 'icloud_resource_fetch'));
