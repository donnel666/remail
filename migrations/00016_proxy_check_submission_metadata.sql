-- +goose Up
ALTER TABLE proxies
    ADD COLUMN check_operator_user_id BIGINT UNSIGNED NOT NULL DEFAULT 0 AFTER last_safe_error,
    ADD COLUMN check_request_id VARCHAR(64) NOT NULL DEFAULT '' AFTER check_operator_user_id,
    ADD COLUMN check_path VARCHAR(255) NOT NULL DEFAULT '' AFTER check_request_id;

-- +goose Down
ALTER TABLE proxies
    DROP COLUMN check_path,
    DROP COLUMN check_request_id,
    DROP COLUMN check_operator_user_id;
