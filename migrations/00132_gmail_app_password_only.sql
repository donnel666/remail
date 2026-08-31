-- +goose Up

ALTER TABLE gmail_resources
    DROP CHECK chk_gmail_resources_credentials,
    ADD CONSTRAINT chk_gmail_resources_credentials CHECK (
        app_password <> ''
        OR status IN ('pending', 'validating', 'abnormal', 'disabled', 'deleted')
    );

-- +goose Down

DROP TEMPORARY TABLE IF EXISTS gmail_app_password_only_down_guard;
CREATE TEMPORARY TABLE gmail_app_password_only_down_guard (
    incompatible_rows BIGINT NOT NULL,
    CONSTRAINT chk_gmail_app_password_only_down_guard CHECK (incompatible_rows = 0)
);
INSERT INTO gmail_app_password_only_down_guard (incompatible_rows)
SELECT COUNT(*)
FROM gmail_resources
WHERE password = ''
   OR (
       (two_factor_secret = '' OR app_password = '')
       AND status NOT IN ('pending', 'validating', 'abnormal', 'disabled', 'deleted')
   );
DROP TEMPORARY TABLE gmail_app_password_only_down_guard;

ALTER TABLE gmail_resources
    DROP CHECK chk_gmail_resources_credentials,
    ADD CONSTRAINT chk_gmail_resources_credentials CHECK (
        password <> ''
        AND (
            (two_factor_secret <> '' AND app_password <> '')
            OR status IN ('pending', 'validating', 'abnormal', 'disabled', 'deleted')
        )
    );
