-- +goose Up

-- Synchronous administrator Microsoft-resource commands use one receipt
-- namespace per operator. A key therefore cannot be reused for another
-- operation, resource, version, selection, or request body. The receipt is
-- created and completed in the same short transaction as the Core aggregate,
-- validation fact, and OperationLog; credentials are represented only by the
-- request fingerprint and are never persisted in result_json.
CREATE TABLE admin_resource_command_receipts (
    operator_user_id BIGINT UNSIGNED NOT NULL,
    idempotency_key VARCHAR(128) NOT NULL,
    operation VARCHAR(64) NOT NULL,
    subject VARCHAR(255) NOT NULL,
    request_fingerprint CHAR(64) NOT NULL,
    reservation_token CHAR(36) NOT NULL,
    status VARCHAR(16) NOT NULL DEFAULT 'processing' COMMENT 'processing|succeeded',
    result_json JSON NULL,
    created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
    PRIMARY KEY (operator_user_id, idempotency_key),
    CONSTRAINT fk_admin_resource_command_operator
        FOREIGN KEY (operator_user_id) REFERENCES users(id) ON DELETE RESTRICT,
    CONSTRAINT chk_admin_resource_command_key
        CHECK (idempotency_key <> '' AND operation <> '' AND subject <> '' AND request_fingerprint <> '' AND reservation_token <> ''),
    CONSTRAINT chk_admin_resource_command_status
        CHECK (status IN ('processing', 'succeeded')),
    CONSTRAINT chk_admin_resource_command_result
        CHECK ((status = 'processing' AND result_json IS NULL) OR (status = 'succeeded' AND result_json IS NOT NULL))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- +goose Down

DROP TABLE IF EXISTS admin_resource_command_receipts;
