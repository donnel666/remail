-- +goose Up

ALTER TABLE mailmatch_messages
    DROP COLUMN raw_source,
    DROP COLUMN provider_payload;

-- +goose Down

ALTER TABLE mailmatch_messages
    ADD COLUMN raw_source MEDIUMTEXT NULL AFTER raw_body,
    ADD COLUMN provider_payload JSON NULL AFTER raw_source;
