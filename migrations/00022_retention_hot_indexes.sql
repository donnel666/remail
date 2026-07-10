-- +goose Up

ALTER TABLE mailmatch_messages
    ADD INDEX idx_mailmatch_messages_retention (resource_type, received_at, id);

ALTER TABLE system_logs
    ADD INDEX idx_system_logs_created (created_at, id);

DROP TABLE api_logs;

-- +goose Down

CREATE TABLE api_logs (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    principal_type VARCHAR(32) NOT NULL COMMENT 'api_key|order_token',
    principal_id BIGINT UNSIGNED NOT NULL,
    user_id BIGINT UNSIGNED NULL,
    path VARCHAR(255) NOT NULL,
    method VARCHAR(16) NOT NULL,
    idempotency_key VARCHAR(128) NOT NULL DEFAULT '',
    http_status INT NOT NULL,
    duration_ms INT NOT NULL,
    request_id VARCHAR(64) NOT NULL DEFAULT '',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    INDEX idx_api_logs_principal_created (principal_type, principal_id, created_at, id),
    INDEX idx_api_logs_user_created (user_id, created_at, id),
    INDEX idx_api_logs_request (request_id),
    CONSTRAINT fk_api_logs_user FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE SET NULL,
    CONSTRAINT chk_api_logs_principal CHECK (principal_type IN ('api_key', 'order_token') AND principal_id > 0),
    CONSTRAINT chk_api_logs_http CHECK (http_status >= 100 AND http_status <= 599 AND duration_ms >= 0),
    CONSTRAINT chk_api_logs_request CHECK (path <> '' AND method <> '')
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

ALTER TABLE system_logs
    DROP INDEX idx_system_logs_created;

ALTER TABLE mailmatch_messages
    DROP INDEX idx_mailmatch_messages_retention;
