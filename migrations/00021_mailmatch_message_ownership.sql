-- +goose Up

ALTER TABLE mailmatch_messages
    ADD COLUMN matched_order_id BIGINT UNSIGNED NULL AFTER resource_type,
    ADD INDEX idx_mailmatch_messages_order_time (matched_order_id, received_at, id),
    ADD CONSTRAINT fk_mailmatch_messages_order
        FOREIGN KEY (matched_order_id) REFERENCES orders(id) ON DELETE SET NULL;

UPDATE mailmatch_messages AS m
JOIN mailmatch_order_delivery_heads AS h ON h.message_id = m.id
SET m.matched_order_id = h.order_id
WHERE m.matched_order_id IS NULL;

ALTER TABLE mailmatch_order_delivery_heads
    DROP FOREIGN KEY fk_mailmatch_delivery_heads_message,
    DROP CHECK chk_mailmatch_delivery_heads_required,
    MODIFY COLUMN message_id BIGINT UNSIGNED NULL;

ALTER TABLE mailmatch_order_delivery_heads
    ADD CONSTRAINT fk_mailmatch_delivery_heads_message
        FOREIGN KEY (message_id) REFERENCES mailmatch_messages(id) ON DELETE SET NULL;

-- +goose Down

DELETE FROM mailmatch_order_delivery_heads
WHERE message_id IS NULL;

ALTER TABLE mailmatch_order_delivery_heads
    DROP FOREIGN KEY fk_mailmatch_delivery_heads_message,
    MODIFY COLUMN message_id BIGINT UNSIGNED NOT NULL;

ALTER TABLE mailmatch_order_delivery_heads
    ADD CONSTRAINT fk_mailmatch_delivery_heads_message
        FOREIGN KEY (message_id) REFERENCES mailmatch_messages(id) ON DELETE RESTRICT,
    ADD CONSTRAINT chk_mailmatch_delivery_heads_required CHECK (
        order_id > 0 AND message_id > 0
    );

ALTER TABLE mailmatch_messages
    DROP FOREIGN KEY fk_mailmatch_messages_order,
    DROP INDEX idx_mailmatch_messages_order_time,
    DROP COLUMN matched_order_id;
