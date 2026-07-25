-- +goose Up

ALTER TABLE api_keys
    DROP CHECK chk_api_keys_limits;

ALTER TABLE api_keys
    DROP COLUMN rate_limit_per_minute;

ALTER TABLE api_keys
    ADD CONSTRAINT chk_api_keys_limits CHECK (
        (concurrency_limit IS NULL OR concurrency_limit > 0)
        AND (quota_limit IS NULL OR quota_limit > 0)
        AND quota_used >= 0
        AND (quota_limit IS NULL OR quota_used <= quota_limit)
    );

ALTER TABLE user_groups
    DROP COLUMN api_rpm_limit,
    DROP COLUMN api_quota_limit;

-- +goose Down

ALTER TABLE user_groups
    ADD COLUMN api_rpm_limit BIGINT UNSIGNED NOT NULL DEFAULT 60 AFTER enabled,
    ADD COLUMN api_quota_limit BIGINT UNSIGNED NOT NULL DEFAULT 10000 AFTER api_concurrency_limit;

ALTER TABLE api_keys
    DROP CHECK chk_api_keys_limits;

ALTER TABLE api_keys
    ADD COLUMN rate_limit_per_minute INT NULL AFTER deleted_at;

ALTER TABLE api_keys
    ADD CONSTRAINT chk_api_keys_limits CHECK (
        (rate_limit_per_minute IS NULL OR rate_limit_per_minute > 0)
        AND (concurrency_limit IS NULL OR concurrency_limit > 0)
        AND (quota_limit IS NULL OR quota_limit > 0)
        AND quota_used >= 0
        AND (quota_limit IS NULL OR quota_used <= quota_limit)
    );
