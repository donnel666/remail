-- +goose Up

CREATE INDEX idx_email_resources_owner_created_id
    ON email_resources(owner_user_id, created_at, id);

CREATE INDEX idx_email_resources_owner_type_created_id
    ON email_resources(owner_user_id, type, created_at, id);

CREATE INDEX idx_email_resources_type_created_id
    ON email_resources(type, created_at, id);

CREATE INDEX idx_email_resources_created_id
    ON email_resources(created_at, id);

DROP INDEX idx_email_resources_created ON email_resources;
DROP INDEX idx_email_resources_type_created ON email_resources;
DROP INDEX idx_email_resources_owner_type_created ON email_resources;
DROP INDEX idx_email_resources_owner_created ON email_resources;

-- +goose Down

CREATE INDEX idx_email_resources_owner_created
    ON email_resources(owner_user_id, created_at);

CREATE INDEX idx_email_resources_owner_type_created
    ON email_resources(owner_user_id, type, created_at);

CREATE INDEX idx_email_resources_type_created
    ON email_resources(type, created_at);

CREATE INDEX idx_email_resources_created
    ON email_resources(created_at);

DROP INDEX idx_email_resources_created_id ON email_resources;
DROP INDEX idx_email_resources_type_created_id ON email_resources;
DROP INDEX idx_email_resources_owner_type_created_id ON email_resources;
DROP INDEX idx_email_resources_owner_created_id ON email_resources;
