-- +goose Up

ALTER TABLE api_keys
    DROP CHECK chk_api_keys_limits,
    DROP COLUMN active_requests,
    DROP COLUMN window_started_at,
    DROP COLUMN window_request_count;

ALTER TABLE api_keys
    ADD CONSTRAINT chk_api_keys_limits CHECK (
        (rate_limit_per_minute IS NULL OR rate_limit_per_minute > 0)
        AND concurrency_limit > 0
        AND (quota_limit IS NULL OR quota_limit > 0)
        AND quota_used >= 0
        AND (quota_limit IS NULL OR quota_used <= quota_limit)
    );

-- +goose Down

ALTER TABLE api_keys
    DROP CHECK chk_api_keys_limits,
    ADD COLUMN active_requests INT NOT NULL DEFAULT 0 AFTER quota_used,
    ADD COLUMN window_started_at DATETIME NULL AFTER active_requests,
    ADD COLUMN window_request_count INT NOT NULL DEFAULT 0 AFTER window_started_at;

ALTER TABLE api_keys
    ADD CONSTRAINT chk_api_keys_limits CHECK (
        (rate_limit_per_minute IS NULL OR rate_limit_per_minute > 0)
        AND concurrency_limit > 0
        AND (quota_limit IS NULL OR quota_limit > 0)
        AND quota_used >= 0
        AND (quota_limit IS NULL OR quota_used <= quota_limit)
        AND active_requests >= 0
        AND active_requests <= concurrency_limit
        AND window_request_count >= 0
    );
