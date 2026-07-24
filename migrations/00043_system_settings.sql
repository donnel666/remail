-- +goose Up

-- Generic administrator-managed options. Values remain opaque text so adding
-- a setting (including JSON or Markdown/HTML content) never needs a schema
-- migration.
CREATE TABLE system_settings (
    `key` VARCHAR(191) NOT NULL,
    `value` LONGTEXT NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    PRIMARY KEY (`key`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- +goose Down

DROP TABLE IF EXISTS system_settings;
