-- +goose Up

ALTER TABLE third_party_identities
    ADD UNIQUE INDEX idx_third_party_user_provider (user_id, provider);

-- +goose Down

ALTER TABLE third_party_identities
    DROP INDEX idx_third_party_user_provider;
