-- +goose Up

ALTER TABLE gmail_resources
	ADD COLUMN binding_email VARCHAR(320) NOT NULL DEFAULT '' AFTER password,
    DROP CHECK chk_gmail_resources_credentials,
    ADD CONSTRAINT chk_gmail_resources_credentials CHECK (
        password <> ''
        AND (
            (two_factor_secret <> '' AND app_password <> '')
            OR status IN ('pending', 'validating', 'abnormal', 'disabled', 'deleted')
        )
    );

-- +goose Down

DROP TEMPORARY TABLE IF EXISTS gmail_pending_credentials_down_guard;
CREATE TEMPORARY TABLE gmail_pending_credentials_down_guard (
    incomplete_rows BIGINT NOT NULL,
    CONSTRAINT chk_gmail_pending_credentials_down_guard CHECK (incomplete_rows = 0)
);
INSERT INTO gmail_pending_credentials_down_guard (incomplete_rows)
SELECT COUNT(*)
FROM gmail_resources
WHERE two_factor_secret = '' OR app_password = '';
DROP TEMPORARY TABLE gmail_pending_credentials_down_guard;

ALTER TABLE gmail_resources
    DROP CHECK chk_gmail_resources_credentials,
    ADD CONSTRAINT chk_gmail_resources_credentials CHECK (
        password <> '' AND two_factor_secret <> '' AND app_password <> ''
    ),
	DROP COLUMN binding_email;
