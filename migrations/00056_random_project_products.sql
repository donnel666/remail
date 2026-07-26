-- +goose Up

ALTER TABLE project_products
    DROP CHECK chk_project_products_type,
    DROP CHECK chk_project_products_weights,
    ADD CONSTRAINT chk_project_products_type CHECK (type IN ('microsoft', 'domain', 'random')),
    ADD CONSTRAINT chk_project_products_weights CHECK (
        main_weight >= 0
        AND dot_weight >= 0
        AND plus_weight >= 0
        AND (type <> 'microsoft' OR main_weight + dot_weight + plus_weight > 0)
        AND (type <> 'domain' OR (main_weight = 0 AND dot_weight = 0 AND plus_weight = 0))
        AND (type <> 'random' OR (main_weight = 1 AND dot_weight = 1 AND plus_weight = 1))
    );

ALTER TABLE orders
    ADD COLUMN random_microsoft_pay_amount DECIMAL(18,6) NULL AFTER pay_amount,
    ADD COLUMN random_domain_pay_amount DECIMAL(18,6) NULL AFTER random_microsoft_pay_amount,
    DROP CHECK chk_orders_product_type,
    ADD CONSTRAINT chk_orders_product_type CHECK (product_type IN ('microsoft', 'domain', 'random')),
    ADD CONSTRAINT chk_orders_random_prices CHECK (
        (
            product_type = 'random'
            AND random_microsoft_pay_amount IS NOT NULL
            AND random_microsoft_pay_amount >= 0
            AND random_domain_pay_amount IS NOT NULL
            AND random_domain_pay_amount >= 0
        )
        OR (
            product_type <> 'random'
            AND random_microsoft_pay_amount IS NULL
            AND random_domain_pay_amount IS NULL
        )
    );

-- +goose Down

DROP TEMPORARY TABLE IF EXISTS random_product_down_guard;

CREATE TEMPORARY TABLE random_product_down_guard (
    random_rows BIGINT NOT NULL,
    CONSTRAINT chk_random_product_down_guard CHECK (random_rows = 0)
);

INSERT INTO random_product_down_guard (random_rows)
SELECT
    (SELECT COUNT(*) FROM orders WHERE product_type = 'random')
    + (SELECT COUNT(*) FROM project_products WHERE type = 'random');

DROP TEMPORARY TABLE random_product_down_guard;

ALTER TABLE project_products
    DROP CHECK chk_project_products_type,
    DROP CHECK chk_project_products_weights,
    ADD CONSTRAINT chk_project_products_type CHECK (type IN ('microsoft', 'domain')),
    ADD CONSTRAINT chk_project_products_weights CHECK (
        main_weight >= 0
        AND dot_weight >= 0
        AND plus_weight >= 0
        AND (type <> 'microsoft' OR main_weight + dot_weight + plus_weight > 0)
        AND (type <> 'domain' OR (main_weight = 0 AND dot_weight = 0 AND plus_weight = 0))
    );

ALTER TABLE orders
    DROP CHECK chk_orders_product_type,
    DROP CHECK chk_orders_random_prices,
    ADD CONSTRAINT chk_orders_product_type CHECK (product_type IN ('microsoft', 'domain')),
    DROP COLUMN random_domain_pay_amount,
    DROP COLUMN random_microsoft_pay_amount;
