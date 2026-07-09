-- +goose Up

CREATE TABLE mailmatch_order_snapshots (
    order_no VARCHAR(64) PRIMARY KEY,
    sender VARCHAR(255) NOT NULL DEFAULT '',
    recipient VARCHAR(255) NOT NULL,
    received_at DATETIME NOT NULL,
    subject VARCHAR(500) NOT NULL DEFAULT '',
    body MEDIUMTEXT NULL,
    verification_code VARCHAR(64) NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    INDEX idx_mailmatch_order_snapshots_received (received_at, order_no),
    CONSTRAINT fk_mailmatch_order_snapshots_order FOREIGN KEY (order_no) REFERENCES orders(order_no) ON DELETE CASCADE,
    CONSTRAINT chk_mailmatch_order_snapshots_required CHECK (order_no <> '' AND recipient <> '' AND verification_code <> '')
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- +goose Down

DROP TABLE IF EXISTS mailmatch_order_snapshots;
