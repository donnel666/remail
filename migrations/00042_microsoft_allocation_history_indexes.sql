-- +goose Up

-- Project inventory and allocation both exclude mailbox identities already
-- used by the same project. Keep those correlated history probes point-lookups
-- as the allocation history grows.
ALTER TABLE microsoft_allocations
    ADD INDEX idx_ms_alloc_resource_project_mailbox (resource_id, project_id, mailbox),
    ADD INDEX idx_ms_alloc_explicit_project_mailbox (explicit_alias_id, project_id, mailbox),
    ADD INDEX idx_ms_alloc_dot_project_mailbox (dot_alias_id, project_id, mailbox),
    ADD INDEX idx_ms_alloc_plus_project_mailbox (plus_alias_id, project_id, mailbox);

-- +goose Down

ALTER TABLE microsoft_allocations
    DROP INDEX idx_ms_alloc_resource_project_mailbox,
    DROP INDEX idx_ms_alloc_explicit_project_mailbox,
    DROP INDEX idx_ms_alloc_dot_project_mailbox,
    DROP INDEX idx_ms_alloc_plus_project_mailbox;
