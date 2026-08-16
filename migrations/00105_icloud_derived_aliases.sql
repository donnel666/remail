-- +goose Up

CREATE TABLE icloud_dot_aliases (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    resource_id BIGINT UNSIGNED NOT NULL,
    alias_id BIGINT UNSIGNED NOT NULL,
    email VARCHAR(320) NOT NULL,
    status VARCHAR(24) NOT NULL DEFAULT 'normal' COMMENT 'normal|disabled',
    created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),

    UNIQUE INDEX uk_icloud_dot_aliases_resource_email (resource_id, email),
    INDEX idx_icloud_dot_aliases_alias (alias_id, status, id),
    INDEX idx_icloud_dot_aliases_email_resource (email, resource_id),

    CONSTRAINT fk_icloud_dot_aliases_alias
        FOREIGN KEY (alias_id, resource_id)
        REFERENCES icloud_aliases(id, resource_id) ON DELETE CASCADE,
    CONSTRAINT chk_icloud_dot_aliases_status
        CHECK (status IN ('normal', 'disabled'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE icloud_plus_aliases (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    resource_id BIGINT UNSIGNED NOT NULL,
    alias_id BIGINT UNSIGNED NOT NULL,
    email VARCHAR(320) NOT NULL,
    status VARCHAR(24) NOT NULL DEFAULT 'normal' COMMENT 'normal|disabled',
    created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),

    UNIQUE INDEX uk_icloud_plus_aliases_resource_email (resource_id, email),
    INDEX idx_icloud_plus_aliases_alias (alias_id, status, id),
    INDEX idx_icloud_plus_aliases_email_resource (email, resource_id),

    CONSTRAINT fk_icloud_plus_aliases_alias
        FOREIGN KEY (alias_id, resource_id)
        REFERENCES icloud_aliases(id, resource_id) ON DELETE CASCADE,
    CONSTRAINT chk_icloud_plus_aliases_status
        CHECK (status IN ('normal', 'disabled'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- +goose Down

DROP TABLE icloud_plus_aliases;
DROP TABLE icloud_dot_aliases;
