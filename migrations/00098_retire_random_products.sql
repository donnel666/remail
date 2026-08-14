-- +goose Up

-- Historical orders and allocations retain foreign keys to these rows, so
-- retirement is a logical disable rather than a destructive delete.
UPDATE project_products
SET status = 'disabled', updated_at = CURRENT_TIMESTAMP
WHERE type = 'random' AND status <> 'disabled';

ALTER TABLE project_products
    ADD CONSTRAINT chk_project_products_random_retired CHECK (
        type <> 'random' OR status = 'disabled'
    );

-- +goose Down

ALTER TABLE project_products
    DROP CHECK chk_project_products_random_retired;

-- Intentionally do not re-enable retired products: their previous enabled
-- state cannot be reconstructed without risking accidental resale.
