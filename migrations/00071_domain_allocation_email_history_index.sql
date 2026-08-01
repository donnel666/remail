-- +goose Up

-- Domain restores retire and replace generated_mailboxes rows, so project
-- history is keyed by the delivered email rather than the mutable mailbox ID.
ALTER TABLE domain_allocations
    ADD INDEX idx_domain_alloc_email_project (email, project_id);

-- +goose Down

ALTER TABLE domain_allocations
    DROP INDEX idx_domain_alloc_email_project;
