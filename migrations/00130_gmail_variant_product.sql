-- +goose Up

ALTER TABLE project_products
    DROP CHECK chk_project_products_type,
    DROP CHECK chk_project_products_weights,
    ADD CONSTRAINT chk_project_products_type CHECK (
        type IN ('microsoft', 'domain', 'random', 'gmail', 'gmail_variant', 'icloud')
    ),
    ADD CONSTRAINT chk_project_products_weights CHECK (
        main_weight >= 0
        AND dot_weight >= 0
        AND plus_weight >= 0
        AND (type NOT IN ('microsoft', 'gmail', 'gmail_variant', 'icloud')
             OR main_weight + dot_weight + plus_weight > 0)
        AND (type <> 'domain'
             OR (main_weight = 0 AND dot_weight = 0 AND plus_weight = 0))
        AND (type <> 'random'
             OR (main_weight = 1 AND dot_weight = 1 AND plus_weight = 1))
        AND (type <> 'gmail_variant'
             OR (main_weight = 0 AND dot_weight = 0 AND plus_weight = 1))
        AND (type <> 'icloud'
             OR (main_weight > 0 AND dot_weight = 0 AND plus_weight = 0))
    );

-- The product SKU still uses Gmail allocations, so allocation_type is unchanged.
ALTER TABLE orders
    DROP CHECK chk_orders_product_type,
    ADD CONSTRAINT chk_orders_product_type CHECK (
        product_type IN ('microsoft', 'domain', 'random', 'gmail', 'gmail_variant', 'icloud')
    ) NOT ENFORCED,
    ALGORITHM=INSTANT;

-- +goose Down

DROP TEMPORARY TABLE IF EXISTS gmail_variant_down_guard;
CREATE TEMPORARY TABLE gmail_variant_down_guard (
    unsafe_rows BIGINT NOT NULL,
    CONSTRAINT chk_gmail_variant_down_guard CHECK (unsafe_rows = 0)
);
INSERT INTO gmail_variant_down_guard (unsafe_rows)
SELECT
    (SELECT COUNT(*) FROM orders WHERE product_type = 'gmail_variant')
    + (SELECT COUNT(*) FROM project_products WHERE type = 'gmail_variant');
DROP TEMPORARY TABLE gmail_variant_down_guard;

ALTER TABLE orders
    DROP CHECK chk_orders_product_type,
    ADD CONSTRAINT chk_orders_product_type CHECK (
        product_type IN ('microsoft', 'domain', 'random', 'gmail', 'icloud')
    ) NOT ENFORCED,
    ALGORITHM=INSTANT;

ALTER TABLE project_products
    DROP CHECK chk_project_products_type,
    DROP CHECK chk_project_products_weights,
    ADD CONSTRAINT chk_project_products_type CHECK (
        type IN ('microsoft', 'domain', 'random', 'gmail', 'icloud')
    ),
    ADD CONSTRAINT chk_project_products_weights CHECK (
        main_weight >= 0
        AND dot_weight >= 0
        AND plus_weight >= 0
        AND (type NOT IN ('microsoft', 'gmail', 'icloud')
             OR main_weight + dot_weight + plus_weight > 0)
        AND (type <> 'domain'
             OR (main_weight = 0 AND dot_weight = 0 AND plus_weight = 0))
        AND (type <> 'random'
             OR (main_weight = 1 AND dot_weight = 1 AND plus_weight = 1))
        AND (type <> 'icloud'
             OR (main_weight > 0 AND dot_weight = 0 AND plus_weight = 0))
    );
