-- +goose Up

ALTER TABLE users
    ADD FULLTEXT INDEX idx_users_search (email, nickname);

-- +goose Down

ALTER TABLE users
    DROP INDEX idx_users_search;
