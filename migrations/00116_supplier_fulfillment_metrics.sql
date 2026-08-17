-- +goose Up

ALTER TABLE microsoft_allocations
    ADD COLUMN supplier_user_id BIGINT UNSIGNED NULL AFTER supply_scope,
    ADD INDEX idx_ms_alloc_supplier_scope_order (supplier_user_id, supply_scope, order_no);

ALTER TABLE domain_allocations
    ADD COLUMN supplier_user_id BIGINT UNSIGNED NULL AFTER supply_scope,
    ADD INDEX idx_domain_alloc_supplier_scope_order (supplier_user_id, supply_scope, order_no);

ALTER TABLE gmail_allocations
    ADD COLUMN supplier_user_id BIGINT UNSIGNED NULL AFTER supply_scope,
    ADD INDEX idx_gmail_alloc_supplier_scope_order (supplier_user_id, supply_scope, order_no);

ALTER TABLE icloud_allocations
    ADD COLUMN supplier_user_id BIGINT UNSIGNED NULL AFTER supply_scope,
    ADD INDEX idx_icloud_alloc_supplier_scope_order (supplier_user_id, supply_scope, order_no);

UPDATE microsoft_allocations AS allocation
JOIN email_resources AS resource ON resource.id = allocation.resource_id
SET allocation.supplier_user_id = resource.owner_user_id
WHERE allocation.supply_scope = 'public';

UPDATE domain_allocations AS allocation
JOIN email_resources AS resource ON resource.id = allocation.resource_id
SET allocation.supplier_user_id = resource.owner_user_id
WHERE allocation.supply_scope = 'public';

UPDATE gmail_allocations AS allocation
JOIN email_resources AS resource ON resource.id = allocation.resource_id
SET allocation.supplier_user_id = resource.owner_user_id
WHERE allocation.source = 'local'
  AND allocation.supply_scope = 'public';

UPDATE icloud_allocations AS allocation
JOIN email_resources AS resource ON resource.id = allocation.resource_id
SET allocation.supplier_user_id = resource.owner_user_id
WHERE allocation.supply_scope = 'public';

ALTER TABLE orders
    ADD INDEX idx_orders_created_order (created_at, order_no);

-- +goose Down

ALTER TABLE orders
    DROP INDEX idx_orders_created_order;

ALTER TABLE icloud_allocations
    DROP INDEX idx_icloud_alloc_supplier_scope_order,
    DROP COLUMN supplier_user_id;

ALTER TABLE gmail_allocations
    DROP INDEX idx_gmail_alloc_supplier_scope_order,
    DROP COLUMN supplier_user_id;

ALTER TABLE domain_allocations
    DROP INDEX idx_domain_alloc_supplier_scope_order,
    DROP COLUMN supplier_user_id;

ALTER TABLE microsoft_allocations
    DROP INDEX idx_ms_alloc_supplier_scope_order,
    DROP COLUMN supplier_user_id;
