-- +goose Up

ALTER TABLE wallet_transactions
    DROP CHECK chk_wallet_transactions_type_direction,
    ADD CONSTRAINT chk_wallet_transactions_type_direction CHECK (
        (transaction_type IN ('debit', 'freeze', 'withdrawal') AND direction = 'out')
        OR (transaction_type IN ('recharge', 'refund', 'credit', 'card_redeem') AND direction = 'in')
        OR transaction_type IN ('manual_adjustment', 'transfer')
    );

-- +goose Down

-- Keep the broadened invariant during an application rollback. Outbound
-- transfer rows are immutable ledger facts; restoring the old CHECK would
-- make the downgrade fail as soon as one transfer has completed.
SELECT 1;
