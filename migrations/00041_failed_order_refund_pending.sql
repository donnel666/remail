-- +goose Up

ALTER TABLE orders
    DROP CHECK chk_orders_refund_state,
    ADD CONSTRAINT chk_orders_refund_state CHECK (
        (status NOT IN ('refunded', 'failed') AND refund_tx_id IS NULL AND refund_amount = 0.00)
        OR (status = 'refunded' AND refund_tx_id IS NOT NULL AND refund_amount >= 0)
        OR (
            status = 'failed'
            AND (
                (debit_tx_id IS NULL AND refund_tx_id IS NULL AND refund_amount = 0.00)
                OR (debit_tx_id IS NOT NULL AND refund_tx_id IS NULL AND refund_amount = 0.00)
                OR (debit_tx_id IS NOT NULL AND refund_tx_id IS NOT NULL AND refund_amount >= 0)
            )
        )
    );

-- +goose Down

-- Keep the narrowly broadened invariant during an application rollback. Rows
-- awaiting refund compensation are valid durable state; restoring the old
-- CHECK would make the downgrade fail exactly when one of those rows exists.
-- Older binaries already understand failed orders and can finish the refund.
SELECT 1;
