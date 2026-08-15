-- +goose Up

CREATE TABLE IF NOT EXISTS icloud_import_preparations (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    operator_user_id BIGINT UNSIGNED NOT NULL,
    domain_resource_id BIGINT UNSIGNED NOT NULL,
    forward_to_email VARCHAR(320) NOT NULL,
    verification_message_id BIGINT UNSIGNED NULL,
    verification_code VARCHAR(64) NOT NULL DEFAULT '',
    verified_at DATETIME(3) NULL,
    expires_at DATETIME(3) NOT NULL,
    consumed_at DATETIME(3) NULL,
    created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3)
        ON UPDATE CURRENT_TIMESTAMP(3),

    UNIQUE INDEX uk_icloud_import_preparations_email (forward_to_email),
    INDEX idx_icloud_import_preparations_operator_expiry
        (operator_user_id, expires_at, id),
    INDEX idx_icloud_import_preparations_message (verification_message_id),

    CONSTRAINT fk_icloud_import_preparations_operator
        FOREIGN KEY (operator_user_id) REFERENCES users(id) ON DELETE RESTRICT,
    CONSTRAINT fk_icloud_import_preparations_domain
        FOREIGN KEY (domain_resource_id) REFERENCES domain_resources(id) ON DELETE RESTRICT
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

ALTER TABLE icloud_resources
    ADD COLUMN required_forward_to VARCHAR(320) NOT NULL DEFAULT ''
        AFTER selected_forward_to;

ALTER TABLE icloud_resource_imports
    ADD COLUMN preparation_id BIGINT UNSIGNED NULL AFTER resource_expire_at,
    ADD COLUMN forward_to_email VARCHAR(320) NOT NULL DEFAULT '' AFTER preparation_id,
    ADD UNIQUE INDEX uk_icloud_resource_imports_preparation (preparation_id),
    ADD CONSTRAINT fk_icloud_resource_imports_preparation
        FOREIGN KEY (preparation_id)
        REFERENCES icloud_import_preparations(id) ON DELETE RESTRICT;

-- +goose Down

ALTER TABLE icloud_resource_imports
    DROP FOREIGN KEY fk_icloud_resource_imports_preparation,
    DROP INDEX uk_icloud_resource_imports_preparation,
    DROP COLUMN forward_to_email,
    DROP COLUMN preparation_id;

ALTER TABLE icloud_resources
    DROP COLUMN required_forward_to;

DROP TABLE icloud_import_preparations;
