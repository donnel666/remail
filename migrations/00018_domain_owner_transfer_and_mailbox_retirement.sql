-- +goose Up

-- Domain ownership is edited atomically with its MailServer and generated
-- mailbox ownership. The original composite owner foreign keys made that
-- transition impossible because MySQL checks each statement immediately.
ALTER TABLE generated_mailboxes
    DROP FOREIGN KEY fk_generated_mailboxes_resource_owner;

ALTER TABLE domain_resources
    DROP FOREIGN KEY fk_domain_resources_resource_owner,
    DROP FOREIGN KEY fk_domain_resources_server_owner;

ALTER TABLE domain_resources
    ADD CONSTRAINT fk_domain_resources_resource_type
        FOREIGN KEY (id, resource_type) REFERENCES email_resources(id, type) ON DELETE CASCADE,
    ADD CONSTRAINT fk_domain_resources_owner
        FOREIGN KEY (owner_user_id) REFERENCES users(id) ON DELETE RESTRICT,
    ADD CONSTRAINT fk_domain_resources_server
        FOREIGN KEY (mail_server_id) REFERENCES mail_servers(id) ON DELETE RESTRICT;

ALTER TABLE generated_mailboxes
    ADD CONSTRAINT fk_generated_mailboxes_resource
        FOREIGN KEY (resource_id) REFERENCES domain_resources(id) ON DELETE CASCADE,
    DROP CHECK chk_generated_mailboxes_status,
    ADD CONSTRAINT chk_generated_mailboxes_status
        CHECK (status IN ('normal', 'disabled', 'retired'));

-- +goose Down

UPDATE generated_mailboxes
SET status = 'disabled'
WHERE status = 'retired';

ALTER TABLE generated_mailboxes
    DROP FOREIGN KEY fk_generated_mailboxes_resource,
    DROP CHECK chk_generated_mailboxes_status;

ALTER TABLE domain_resources
    DROP FOREIGN KEY fk_domain_resources_resource_type,
    DROP FOREIGN KEY fk_domain_resources_owner,
    DROP FOREIGN KEY fk_domain_resources_server;

ALTER TABLE domain_resources
    ADD CONSTRAINT fk_domain_resources_resource_owner
        FOREIGN KEY (id, resource_type, owner_user_id) REFERENCES email_resources(id, type, owner_user_id) ON DELETE CASCADE,
    ADD CONSTRAINT fk_domain_resources_server_owner
        FOREIGN KEY (mail_server_id, owner_user_id) REFERENCES mail_servers(id, owner_user_id) ON DELETE RESTRICT;

ALTER TABLE generated_mailboxes
    ADD CONSTRAINT fk_generated_mailboxes_resource_owner
        FOREIGN KEY (resource_id, owner_user_id) REFERENCES domain_resources(id, owner_user_id) ON DELETE CASCADE,
    ADD CONSTRAINT chk_generated_mailboxes_status
        CHECK (status IN ('normal', 'disabled'));
