-- +goose Up
ALTER TABLE microsoft_alias_schedules
    ADD COLUMN failure_streak INT UNSIGNED NOT NULL DEFAULT 0 AFTER attempts,
    ADD COLUMN blocked_resource_signature CHAR(64) NOT NULL DEFAULT '' AFTER failure_streak,
    ADD COLUMN blocked_resource_updated_at DATETIME NULL AFTER blocked_resource_signature,
    ADD COLUMN blocked_last_allocated_at DATETIME NULL AFTER blocked_resource_updated_at;

ALTER TABLE microsoft_alias_attempts
    ADD COLUMN was_attempted TINYINT(1) NOT NULL DEFAULT 0 AFTER last_safe_error,
    ADD COLUMN uncertain_since DATETIME(3) NULL AFTER was_attempted,
    ADD COLUMN negative_confirmations INT UNSIGNED NOT NULL DEFAULT 0 AFTER uncertain_since,
    ADD COLUMN last_negative_confirmation_at DATETIME(3) NULL AFTER negative_confirmations;

UPDATE microsoft_alias_attempts
SET was_attempted = TRUE;

UPDATE microsoft_alias_attempts
SET uncertain_since = COALESCE(uncertain_since, updated_at, created_at)
WHERE status IN ('running', 'uncertain');

-- +goose Down
ALTER TABLE microsoft_alias_attempts
    DROP COLUMN last_negative_confirmation_at,
    DROP COLUMN negative_confirmations,
    DROP COLUMN uncertain_since,
    DROP COLUMN was_attempted;

ALTER TABLE microsoft_alias_schedules
    DROP COLUMN blocked_last_allocated_at,
    DROP COLUMN blocked_resource_updated_at,
    DROP COLUMN blocked_resource_signature,
    DROP COLUMN failure_streak;
