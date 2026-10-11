-- +goose Up
DROP PROCEDURE IF EXISTS migrate_supplier_settlements_00147;
-- +goose StatementBegin
CREATE PROCEDURE migrate_supplier_settlements_00147()
BEGIN
    IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = 'orders' AND column_name = 'supplier_amount') THEN
        ALTER TABLE orders
            ADD COLUMN supplier_amount DECIMAL(18,6) NULL,
            ADD COLUMN supplier_user_id BIGINT UNSIGNED NULL,
            ADD CONSTRAINT chk_orders_supplier_amount CHECK (supplier_amount IS NULL OR supplier_amount >= 0),
            ADD CONSTRAINT fk_orders_supplier FOREIGN KEY (supplier_user_id) REFERENCES users(id) ON DELETE RESTRICT;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = 'allocation_order_guards' AND column_name = 'owner_user_id') THEN
        ALTER TABLE allocation_order_guards ADD COLUMN owner_user_id BIGINT UNSIGNED NULL;
    END IF;
END;
-- +goose StatementEnd
CALL migrate_supplier_settlements_00147();
DROP PROCEDURE migrate_supplier_settlements_00147;

-- NULL order snapshots deliberately exclude all pre-migration orders.
CREATE TABLE IF NOT EXISTS supplier_settlements (
    order_no VARCHAR(64) NOT NULL PRIMARY KEY,
    supplier_user_id BIGINT UNSIGNED NOT NULL,
    amount DECIMAL(18,6) NOT NULL,
    status VARCHAR(16) NOT NULL,
    available_at DATETIME(3) NOT NULL,
    credited_at DATETIME(3) NULL,
    cancelled_at DATETIME(3) NULL,
    created_at DATETIME(3) NOT NULL,
    updated_at DATETIME(3) NOT NULL,
    INDEX idx_supplier_settlements_due (status, available_at, order_no),
    INDEX idx_supplier_settlements_supplier (supplier_user_id, created_at),
    CONSTRAINT fk_supplier_settlements_order FOREIGN KEY (order_no) REFERENCES orders(order_no) ON DELETE RESTRICT,
    CONSTRAINT fk_supplier_settlements_user FOREIGN KEY (supplier_user_id) REFERENCES users(id) ON DELETE RESTRICT,
    CONSTRAINT chk_supplier_settlements_amount CHECK (amount > 0),
    CONSTRAINT chk_supplier_settlements_status CHECK (status IN ('frozen', 'credited', 'cancelled'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- +goose Down
SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'Supplier settlement snapshots and ledger facts must not be discarded';
