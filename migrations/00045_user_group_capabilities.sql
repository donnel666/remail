-- +goose Up

ALTER TABLE user_groups
    ADD COLUMN api_rpm_limit BIGINT UNSIGNED NOT NULL DEFAULT 60 AFTER enabled,
    ADD COLUMN api_concurrency_limit BIGINT UNSIGNED NOT NULL DEFAULT 3 AFTER api_rpm_limit,
    ADD COLUMN api_quota_limit BIGINT UNSIGNED NOT NULL DEFAULT 10000 AFTER api_concurrency_limit,
    ADD COLUMN price_discount_ratio DECIMAL(7,6) NOT NULL DEFAULT 1.000000 AFTER api_quota_limit,
    ADD COLUMN topup_threshold DECIMAL(18,6) NOT NULL DEFAULT 0.000000 AFTER price_discount_ratio,
    ADD COLUMN auto_upgrade_enabled TINYINT(1) NOT NULL DEFAULT 0 AFTER topup_threshold,
    ADD CONSTRAINT chk_user_groups_price_discount_ratio
        CHECK (price_discount_ratio >= 0 AND price_discount_ratio <= 1),
    ADD CONSTRAINT chk_user_groups_topup_threshold
        CHECK (topup_threshold >= 0);

-- +goose Down

ALTER TABLE user_groups
    DROP CHECK chk_user_groups_price_discount_ratio,
    DROP CHECK chk_user_groups_topup_threshold,
    DROP COLUMN auto_upgrade_enabled,
    DROP COLUMN topup_threshold,
    DROP COLUMN price_discount_ratio,
    DROP COLUMN api_quota_limit,
    DROP COLUMN api_concurrency_limit,
    DROP COLUMN api_rpm_limit;
