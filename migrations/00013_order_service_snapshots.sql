-- +goose Up

-- Product configuration is mutable and its enabled flag controls only new
-- purchases. Persist the service windows on each order so an accepted order
-- can be retried, allocated, activated, and completed after its product is
-- delisted or reconfigured.
ALTER TABLE orders
    ADD COLUMN code_window_minutes INT NOT NULL DEFAULT 0 AFTER refund_amount,
    ADD COLUMN activation_window_minutes INT NOT NULL DEFAULT 0 AFTER code_window_minutes,
    ADD COLUMN warranty_minutes INT NOT NULL DEFAULT 0 AFTER activation_window_minutes;

-- Existing orders predate these columns. project_products rows are retained
-- by the product-history update, so populate their durable fulfilment terms
-- before the application begins relying on the snapshots.
UPDATE orders AS o
JOIN project_products AS pp
  ON pp.id = o.project_product_id
 AND pp.project_id = o.project_id
SET o.code_window_minutes = pp.code_window_minutes,
    o.activation_window_minutes = pp.activation_window_minutes,
    o.warranty_minutes = pp.warranty_minutes
WHERE o.code_window_minutes = 0
  AND o.activation_window_minutes = 0
  AND o.warranty_minutes = 0;

-- +goose Down

ALTER TABLE orders
    DROP COLUMN warranty_minutes,
    DROP COLUMN activation_window_minutes,
    DROP COLUMN code_window_minutes;
