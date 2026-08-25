-- +goose Up

-- Provider trade identifiers are only unique within a payment method. Keep
-- the historical index name so existing operational checks remain valid.
ALTER TABLE recharges
    DROP INDEX idx_recharges_gateway_trade_no,
    ADD UNIQUE INDEX idx_recharges_gateway_trade_no (payment_method, gateway_trade_no);

-- +goose Down

-- Restoring the global key is only valid when no trade number is shared by
-- different payment methods. Fail before any DDL so a rollback cannot leave a
-- partially altered index.
DROP TEMPORARY TABLE IF EXISTS recharge_gateway_trade_scope_down_guard;
CREATE TEMPORARY TABLE recharge_gateway_trade_scope_down_guard (
    unsafe_rows BIGINT NOT NULL,
    CONSTRAINT chk_recharge_gateway_trade_scope_down_guard CHECK (unsafe_rows = 0)
);
INSERT INTO recharge_gateway_trade_scope_down_guard (unsafe_rows)
SELECT COUNT(*)
FROM (
    SELECT gateway_trade_no
    FROM recharges
    WHERE gateway_trade_no IS NOT NULL
    GROUP BY gateway_trade_no
    HAVING COUNT(*) > 1
) AS duplicated_trade_numbers;
DROP TEMPORARY TABLE recharge_gateway_trade_scope_down_guard;

ALTER TABLE recharges
    DROP INDEX idx_recharges_gateway_trade_no,
    ADD UNIQUE INDEX idx_recharges_gateway_trade_no (gateway_trade_no);
