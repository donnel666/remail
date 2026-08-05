-- +goose Up

CREATE TABLE aftersale_inbound_receipts (
    inbound_mail_id BIGINT UNSIGNED PRIMARY KEY,
    ticket_no VARCHAR(64) NOT NULL DEFAULT '',
    outcome VARCHAR(16) NOT NULL COMMENT 'replied|ignored',
    created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    INDEX idx_aftersale_inbound_receipts_ticket (ticket_no, inbound_mail_id),
    CONSTRAINT chk_aftersale_inbound_receipts_outcome CHECK (outcome IN ('replied', 'ignored'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- +goose Down

DROP TABLE IF EXISTS aftersale_inbound_receipts;
