-- +goose Up

ALTER TABLE orders
    ADD INDEX idx_orders_status_receive_until (status, receive_until),
    ADD INDEX idx_orders_status_after_sale (status, after_sale_until);

-- +goose Down

ALTER TABLE orders
    DROP INDEX idx_orders_status_after_sale,
    DROP INDEX idx_orders_status_receive_until;
