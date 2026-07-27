-- +goose Up

ALTER TABLE system_guard
    ADD COLUMN domain_tld_index_fingerprint CHAR(64) NOT NULL DEFAULT '';

-- +goose Down

ALTER TABLE system_guard
    DROP COLUMN domain_tld_index_fingerprint;
