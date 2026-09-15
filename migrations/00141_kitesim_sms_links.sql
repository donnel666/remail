-- +goose Up
ALTER TABLE kitesim_phones
    ADD COLUMN sms_link_token_hash CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NULL,
    ADD UNIQUE INDEX uk_kitesim_phones_sms_link_token (sms_link_token_hash);

-- +goose Down
ALTER TABLE kitesim_phones
    DROP INDEX uk_kitesim_phones_sms_link_token,
    DROP COLUMN sms_link_token_hash;
