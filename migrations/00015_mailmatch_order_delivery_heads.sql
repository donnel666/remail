-- +goose Up

CREATE TABLE mailmatch_order_delivery_heads (
    order_id BIGINT UNSIGNED NOT NULL PRIMARY KEY,
    message_id BIGINT UNSIGNED NOT NULL,
    message_received_at DATETIME(3) NOT NULL,
    INDEX idx_mailmatch_delivery_heads_message (message_id),
    CONSTRAINT fk_mailmatch_delivery_heads_order FOREIGN KEY (order_id) REFERENCES orders(id) ON DELETE CASCADE,
    CONSTRAINT fk_mailmatch_delivery_heads_message FOREIGN KEY (message_id) REFERENCES mailmatch_messages(id) ON DELETE RESTRICT,
    CONSTRAINT chk_mailmatch_delivery_heads_required CHECK (order_id > 0 AND message_id > 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- +goose Down

DROP TABLE IF EXISTS mailmatch_order_delivery_heads;
