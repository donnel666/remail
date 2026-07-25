-- +goose Up

ALTER TABLE domain_resources
    DROP INDEX idx_domain_inventory_public,
    DROP INDEX idx_domain_resources_owner_tld_private,
    ADD INDEX idx_domain_inventory_public (purpose, status, domain_tld, mailbox_daily_limit, id),
    ADD INDEX idx_domain_alloc_tld_public (domain_tld, purpose, status, last_allocated_at, id),
    ADD INDEX idx_domain_resources_owner_tld_private (owner_user_id, domain_tld, purpose, status, last_allocated_at, id);

-- +goose Down

ALTER TABLE domain_resources
    DROP INDEX idx_domain_inventory_public,
    DROP INDEX idx_domain_alloc_tld_public,
    DROP INDEX idx_domain_resources_owner_tld_private,
    ADD INDEX idx_domain_inventory_public (purpose, status, id, mailbox_daily_limit),
    ADD INDEX idx_domain_resources_owner_tld_private (owner_user_id, domain_tld, purpose, status);
