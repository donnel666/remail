-- +goose Up

ALTER TABLE explicit_aliases
    ADD INDEX idx_explicit_aliases_email_resource (email, resource_id);

ALTER TABLE dot_aliases
    ADD INDEX idx_dot_aliases_email_resource (email, resource_id);

ALTER TABLE plus_aliases
    ADD INDEX idx_plus_aliases_email_resource (email, resource_id);

-- +goose Down

ALTER TABLE plus_aliases
    DROP INDEX idx_plus_aliases_email_resource;

ALTER TABLE dot_aliases
    DROP INDEX idx_dot_aliases_email_resource;

ALTER TABLE explicit_aliases
    DROP INDEX idx_explicit_aliases_email_resource;
