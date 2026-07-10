-- +goose Up

ALTER TABLE orders
    ADD COLUMN failure_code VARCHAR(32) NOT NULL DEFAULT '' AFTER status;

UPDATE orders
SET failure_code = 'unknown'
WHERE status = 'failed';

ALTER TABLE orders
    ADD CONSTRAINT chk_orders_failure_code CHECK (
        (status = 'failed' AND failure_code IN (
            'unknown',
            'insufficient_inventory',
            'insufficient_balance',
            'allocation_failed',
            'service_token_failed',
            'activation_failed'
        ))
        OR (status <> 'failed' AND failure_code = '')
    );

-- +goose Down

ALTER TABLE orders
    DROP CHECK chk_orders_failure_code,
    DROP COLUMN failure_code;
