-- +goose Up

-- DDL commits independently; allow retries after a partially completed migration.
DROP PROCEDURE IF EXISTS add_microsoft_enterprise_00153;
-- +goose StatementBegin
CREATE PROCEDURE add_microsoft_enterprise_00153()
BEGIN
    IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = 'resource_imports' AND column_name = 'enterprise') THEN
        ALTER TABLE resource_imports ADD COLUMN enterprise BOOLEAN NOT NULL DEFAULT FALSE, ALGORITHM=INSTANT;
    END IF;
    IF EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = 'microsoft_resources' AND column_name = 'refresh_token' AND character_maximum_length < 8192) THEN
        ALTER TABLE microsoft_resources MODIFY COLUMN refresh_token VARCHAR(8192) NOT NULL DEFAULT '';
    END IF;
    IF NOT EXISTS (SELECT 1 FROM information_schema.check_constraints WHERE constraint_schema = DATABASE() AND constraint_name = 'chk_project_products_type' AND LOWER(check_clause) LIKE '%microsoft_enterprise%') THEN
        ALTER TABLE project_products
            DROP CHECK chk_project_products_type,
            DROP CHECK chk_project_products_weights,
            ADD CONSTRAINT chk_project_products_type CHECK (type IN ('microsoft', 'domain', 'random', 'gmail', 'gmail_variant', 'icloud', 'proto', 'daum', 'microsoft_enterprise')),
            ADD CONSTRAINT chk_project_products_weights CHECK (
                main_weight >= 0
                AND dot_weight >= 0
                AND plus_weight >= 0
                AND (type NOT IN ('microsoft', 'gmail', 'gmail_variant', 'icloud', 'proto', 'daum', 'microsoft_enterprise') OR main_weight + dot_weight + plus_weight > 0)
                AND (type <> 'domain' OR (main_weight = 0 AND dot_weight = 0 AND plus_weight = 0))
                AND (type <> 'random' OR (main_weight = 1 AND dot_weight = 1 AND plus_weight = 1))
                AND (type <> 'gmail_variant' OR (main_weight = 0 AND dot_weight = 0 AND plus_weight = 1))
                AND (type NOT IN ('icloud', 'proto', 'daum', 'microsoft_enterprise') OR (main_weight > 0 AND dot_weight = 0 AND plus_weight = 0))
            );
    END IF;
    IF NOT EXISTS (SELECT 1 FROM information_schema.check_constraints WHERE constraint_schema = DATABASE() AND constraint_name = 'chk_orders_product_type' AND LOWER(check_clause) LIKE '%microsoft_enterprise%') THEN
        ALTER TABLE orders
            DROP CHECK chk_orders_product_type,
            ADD CONSTRAINT chk_orders_product_type CHECK (product_type IN ('microsoft', 'domain', 'random', 'gmail', 'gmail_variant', 'icloud', 'proto', 'daum', 'microsoft_enterprise')) NOT ENFORCED,
            ALGORITHM=INSTANT;
    END IF;
END;
-- +goose StatementEnd
CALL add_microsoft_enterprise_00153();
DROP PROCEDURE add_microsoft_enterprise_00153;

-- +goose Down

-- Refuse rollback while enterprise products, imports, or longer credentials exist.
DROP TEMPORARY TABLE IF EXISTS microsoft_enterprise_down_guard;
CREATE TEMPORARY TABLE microsoft_enterprise_down_guard (
    unsafe_rows BIGINT NOT NULL CHECK (unsafe_rows = 0)
);
INSERT INTO microsoft_enterprise_down_guard
SELECT (SELECT COUNT(*) FROM project_products WHERE type = 'microsoft_enterprise')
     + (SELECT COUNT(*) FROM orders WHERE product_type = 'microsoft_enterprise')
     + (SELECT COUNT(*) FROM resource_imports WHERE enterprise = TRUE)
     + (SELECT COUNT(*) FROM microsoft_resources WHERE CHAR_LENGTH(refresh_token) > 1024);
DROP TEMPORARY TABLE microsoft_enterprise_down_guard;

ALTER TABLE orders
    DROP CHECK chk_orders_product_type,
    ADD CONSTRAINT chk_orders_product_type CHECK (product_type IN ('microsoft', 'domain', 'random', 'gmail', 'gmail_variant', 'icloud', 'proto', 'daum')) NOT ENFORCED,
    ALGORITHM=INSTANT;

ALTER TABLE project_products
    DROP CHECK chk_project_products_type,
    DROP CHECK chk_project_products_weights,
    ADD CONSTRAINT chk_project_products_type CHECK (type IN ('microsoft', 'domain', 'random', 'gmail', 'gmail_variant', 'icloud', 'proto', 'daum')),
    ADD CONSTRAINT chk_project_products_weights CHECK (
        main_weight >= 0
        AND dot_weight >= 0
        AND plus_weight >= 0
        AND (type NOT IN ('microsoft', 'gmail', 'gmail_variant', 'icloud', 'proto', 'daum') OR main_weight + dot_weight + plus_weight > 0)
        AND (type <> 'domain' OR (main_weight = 0 AND dot_weight = 0 AND plus_weight = 0))
        AND (type <> 'random' OR (main_weight = 1 AND dot_weight = 1 AND plus_weight = 1))
        AND (type <> 'gmail_variant' OR (main_weight = 0 AND dot_weight = 0 AND plus_weight = 1))
        AND (type NOT IN ('icloud', 'proto', 'daum') OR (main_weight > 0 AND dot_weight = 0 AND plus_weight = 0))
    );

ALTER TABLE microsoft_resources MODIFY COLUMN refresh_token VARCHAR(1024) NOT NULL DEFAULT '';
ALTER TABLE resource_imports DROP COLUMN enterprise, ALGORITHM=INSTANT;
