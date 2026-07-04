-- +goose Up

CREATE TABLE email_resources (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    type VARCHAR(32) NOT NULL COMMENT 'microsoft|domain',
    owner_user_id BIGINT UNSIGNED NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    INDEX idx_email_resources_owner_created (owner_user_id, created_at),
    INDEX idx_email_resources_owner_type_created (owner_user_id, type, created_at),
    INDEX idx_email_resources_type_created (type, created_at),
    INDEX idx_email_resources_created (created_at),
    UNIQUE INDEX idx_email_resources_id_type (id, type),
    UNIQUE INDEX idx_email_resources_id_type_owner (id, type, owner_user_id),
    CONSTRAINT fk_email_resources_owner FOREIGN KEY (owner_user_id) REFERENCES users(id) ON DELETE RESTRICT,
    CONSTRAINT chk_email_resources_type CHECK (type IN ('microsoft', 'domain'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE resource_imports (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    owner_user_id BIGINT UNSIGNED NOT NULL,
    resource_type VARCHAR(32) NOT NULL COMMENT 'microsoft',
    source_object_key VARCHAR(500) NOT NULL COMMENT 'private MinIO object key for original import file',
    failure_object_key VARCHAR(500) NOT NULL DEFAULT '' COMMENT 'private MinIO object key for safe failure detail file',
    status VARCHAR(32) NOT NULL DEFAULT 'processing' COMMENT 'processing|imported|failed',
    imported_count INT NOT NULL DEFAULT 0,
    last_safe_error VARCHAR(500) NOT NULL DEFAULT '',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    INDEX idx_resource_imports_owner_created (owner_user_id, created_at),
    INDEX idx_resource_imports_status_created (status, created_at),
    CONSTRAINT fk_resource_imports_owner FOREIGN KEY (owner_user_id) REFERENCES users(id) ON DELETE RESTRICT,
    CONSTRAINT chk_resource_imports_type CHECK (resource_type IN ('microsoft')),
    CONSTRAINT chk_resource_imports_status CHECK (status IN ('processing', 'imported', 'failed'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE microsoft_resources (
    id BIGINT UNSIGNED PRIMARY KEY,
    resource_type VARCHAR(32) NOT NULL DEFAULT 'microsoft' COMMENT 'mirrors email_resources.type for DB-level traceability',
    email_address VARCHAR(255) NOT NULL,
    email_domain VARCHAR(255) NOT NULL DEFAULT '',
    password VARCHAR(512) NOT NULL COMMENT 'original value, never in API response or logs',
    client_id VARCHAR(255) NOT NULL DEFAULT '',
    refresh_token VARCHAR(1024) NOT NULL DEFAULT '' COMMENT 'original value, never in API response or logs',
    long_lived TINYINT(1) NOT NULL DEFAULT 0,
    graph_available TINYINT(1) NOT NULL DEFAULT 0 COMMENT 'whether Microsoft Graph mail fetch is available after validation',
    rt_expire_at DATETIME NULL,
    for_sale TINYINT(1) NOT NULL DEFAULT 0,
    status VARCHAR(32) NOT NULL DEFAULT 'pending' COMMENT 'pending|normal|abnormal|disabled|deleted',
    quality_score INT NOT NULL DEFAULT 0,
    last_safe_error VARCHAR(500) NOT NULL DEFAULT '' COMMENT 'sanitized diagnostic summary',
    last_allocated_at DATETIME NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    UNIQUE INDEX idx_microsoft_email (email_address),
    INDEX idx_microsoft_status (status),
    INDEX idx_microsoft_long_lived (long_lived),
    INDEX idx_microsoft_graph_available (graph_available),
    INDEX idx_microsoft_for_sale (for_sale, status),
    INDEX idx_microsoft_bulk_domain (email_domain, for_sale, status, long_lived, graph_available),
    CONSTRAINT fk_microsoft_resource_type FOREIGN KEY (id, resource_type) REFERENCES email_resources(id, type) ON DELETE CASCADE,
    CONSTRAINT chk_microsoft_resource_type CHECK (resource_type = 'microsoft'),
    CONSTRAINT chk_microsoft_status CHECK (status IN ('pending', 'normal', 'abnormal', 'disabled', 'deleted'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;


CREATE TABLE explicit_aliases (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    resource_id BIGINT UNSIGNED NOT NULL,
    email VARCHAR(255) NOT NULL,
    status VARCHAR(32) NOT NULL DEFAULT 'normal' COMMENT 'normal|disabled',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    UNIQUE INDEX idx_explicit_aliases_resource_email (resource_id, email),
    INDEX idx_explicit_aliases_status (status),
    CONSTRAINT fk_explicit_aliases_resource FOREIGN KEY (resource_id) REFERENCES microsoft_resources(id) ON DELETE CASCADE,
    CONSTRAINT chk_explicit_aliases_status CHECK (status IN ('normal', 'disabled'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE dot_aliases (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    resource_id BIGINT UNSIGNED NOT NULL,
    email VARCHAR(255) NOT NULL,
    status VARCHAR(32) NOT NULL DEFAULT 'normal' COMMENT 'normal|disabled',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE INDEX idx_dot_aliases_resource_email (resource_id, email),
    CONSTRAINT fk_dot_aliases_resource FOREIGN KEY (resource_id) REFERENCES microsoft_resources(id) ON DELETE CASCADE,
    CONSTRAINT chk_dot_aliases_status CHECK (status IN ('normal', 'disabled'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE plus_aliases (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    resource_id BIGINT UNSIGNED NOT NULL,
    email VARCHAR(255) NOT NULL,
    status VARCHAR(32) NOT NULL DEFAULT 'normal' COMMENT 'normal|disabled',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE INDEX idx_plus_aliases_resource_email (resource_id, email),
    CONSTRAINT fk_plus_aliases_resource FOREIGN KEY (resource_id) REFERENCES microsoft_resources(id) ON DELETE CASCADE,
    CONSTRAINT chk_plus_aliases_status CHECK (status IN ('normal', 'disabled'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE mail_servers (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    owner_user_id BIGINT UNSIGNED NOT NULL,
    name VARCHAR(255) NOT NULL DEFAULT '',
    server_address VARCHAR(255) NOT NULL,
    mx_record VARCHAR(255) NOT NULL DEFAULT '',
    spf_record VARCHAR(512) NOT NULL DEFAULT '',
    dkim_record VARCHAR(512) NOT NULL DEFAULT '',
    dmarc_record VARCHAR(512) NOT NULL DEFAULT '',
    ptr_record VARCHAR(255) NOT NULL DEFAULT '',
    status VARCHAR(32) NOT NULL DEFAULT 'online' COMMENT 'online|offline|disabled',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    INDEX idx_mail_servers_owner_created (owner_user_id, created_at),
    INDEX idx_mail_servers_created (created_at),
    UNIQUE INDEX idx_mail_servers_id_owner (id, owner_user_id),
    UNIQUE INDEX idx_mail_servers_owner_address_mx (owner_user_id, server_address, mx_record),
    INDEX idx_mail_servers_status (status),
    CONSTRAINT fk_mail_servers_owner FOREIGN KEY (owner_user_id) REFERENCES users(id) ON DELETE RESTRICT,
    CONSTRAINT chk_mail_servers_status CHECK (status IN ('online', 'offline', 'disabled'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE domain_resources (
    id BIGINT UNSIGNED PRIMARY KEY,
    resource_type VARCHAR(32) NOT NULL DEFAULT 'domain' COMMENT 'mirrors email_resources.type for DB-level traceability',
    owner_user_id BIGINT UNSIGNED NOT NULL,
    domain VARCHAR(255) NOT NULL,
    domain_tld VARCHAR(64) NOT NULL DEFAULT '',
    mail_server_id BIGINT UNSIGNED NOT NULL,
    purpose VARCHAR(32) NOT NULL DEFAULT 'not_sale' COMMENT 'not_sale|sale|binding',
    status VARCHAR(32) NOT NULL DEFAULT 'abnormal' COMMENT 'normal|abnormal|disabled|deleted',
    last_allocated_at DATETIME NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    UNIQUE INDEX idx_domain_resources_domain (domain),
    INDEX idx_domain_resources_owner_created (owner_user_id, created_at),
    INDEX idx_domain_resources_purpose_status (purpose, status),
    INDEX idx_domain_resources_server (mail_server_id),
    UNIQUE INDEX idx_domain_resources_id_owner (id, owner_user_id),
    INDEX idx_domain_resources_owner_tld_private (owner_user_id, domain_tld, purpose, status),
    CONSTRAINT fk_domain_resources_resource_owner FOREIGN KEY (id, resource_type, owner_user_id) REFERENCES email_resources(id, type, owner_user_id) ON DELETE CASCADE,
    CONSTRAINT fk_domain_resources_server_owner FOREIGN KEY (mail_server_id, owner_user_id) REFERENCES mail_servers(id, owner_user_id) ON DELETE RESTRICT,
    CONSTRAINT chk_domain_resources_type CHECK (resource_type = 'domain'),
    CONSTRAINT chk_domain_resources_purpose CHECK (purpose IN ('not_sale', 'sale', 'binding')),
    CONSTRAINT chk_domain_resources_status CHECK (status IN ('normal', 'abnormal', 'disabled', 'deleted'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;


CREATE TABLE generated_mailboxes (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    resource_id BIGINT UNSIGNED NOT NULL,
    owner_user_id BIGINT UNSIGNED NOT NULL,
    email VARCHAR(255) NOT NULL,
    status VARCHAR(32) NOT NULL DEFAULT 'normal' COMMENT 'normal|disabled',
    last_allocated_at DATETIME NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE INDEX idx_generated_mailboxes_resource_email (resource_id, email),
    INDEX idx_generated_mailboxes_resource_created (resource_id, owner_user_id, created_at),
    INDEX idx_generated_mailboxes_status (status),
    CONSTRAINT fk_generated_mailboxes_resource_owner FOREIGN KEY (resource_id, owner_user_id) REFERENCES domain_resources(id, owner_user_id) ON DELETE CASCADE,
    CONSTRAINT fk_generated_mailboxes_owner FOREIGN KEY (owner_user_id) REFERENCES users(id) ON DELETE RESTRICT,
    CONSTRAINT chk_generated_mailboxes_status CHECK (status IN ('normal', 'disabled'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- +goose Down
DROP TABLE IF EXISTS generated_mailboxes;
DROP TABLE IF EXISTS domain_resources;
DROP TABLE IF EXISTS dot_aliases;
DROP TABLE IF EXISTS plus_aliases;
DROP TABLE IF EXISTS explicit_aliases;
DROP TABLE IF EXISTS microsoft_resources;
DROP TABLE IF EXISTS resource_imports;
DROP TABLE IF EXISTS mail_servers;
DROP TABLE IF EXISTS email_resources;
