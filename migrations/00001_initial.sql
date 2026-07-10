-- +goose Up
SELECT 'P1-I0 skeleton migration applied';


CREATE TABLE user_groups (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    code VARCHAR(64) NOT NULL,
    name VARCHAR(100) NOT NULL,
    description VARCHAR(500) NOT NULL DEFAULT '',
    enabled TINYINT(1) NOT NULL DEFAULT 1,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    UNIQUE INDEX idx_user_groups_code (code),
    CONSTRAINT chk_user_groups_code CHECK (code <> ''),
    CONSTRAINT chk_user_groups_name CHECK (name <> '')
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

INSERT INTO user_groups (id, code, name, description, enabled)
VALUES (1, 'normal', '普通用户', '默认权益分组', 1);

CREATE TABLE users (
    id          BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    email       VARCHAR(255) NOT NULL,
    password_hash VARCHAR(255) NOT NULL,
    nickname    VARCHAR(100) NOT NULL DEFAULT '',
    enabled     TINYINT(1) NOT NULL DEFAULT 1,
    role        VARCHAR(32) NOT NULL DEFAULT 'user' COMMENT 'RBAC role: user|supplier|admin|super_admin',
    user_group_id BIGINT UNSIGNED NOT NULL DEFAULT 1,
    token_version INT NOT NULL DEFAULT 0 COMMENT 'increment to invalidate all sessions',
    last_login_at DATETIME NULL,
    created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    UNIQUE INDEX idx_users_email (email),
    INDEX idx_users_role (role),
    INDEX idx_users_user_group (user_group_id),
    CONSTRAINT fk_users_user_group FOREIGN KEY (user_group_id) REFERENCES user_groups(id) ON DELETE RESTRICT,
    CONSTRAINT chk_users_role CHECK (role IN ('user', 'supplier', 'admin', 'super_admin'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE system_guard (
    id INT PRIMARY KEY,
    label VARCHAR(64) NOT NULL DEFAULT ''
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

INSERT INTO system_guard (id, label) VALUES (1, 'activation-guard');

CREATE TABLE invites (
    code VARCHAR(64) PRIMARY KEY,
    enabled TINYINT(1) NOT NULL DEFAULT 1,
    max_use INT NOT NULL,
    used INT NOT NULL DEFAULT 0,
    expire_at DATETIME NULL,
    created_by_user_id BIGINT UNSIGNED NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    INDEX idx_invites_enabled_expire (enabled, expire_at),
    INDEX idx_invites_created_by (created_by_user_id),
    CONSTRAINT fk_invites_created_by FOREIGN KEY (created_by_user_id) REFERENCES users(id) ON DELETE SET NULL,
    CONSTRAINT chk_invites_max_use CHECK (max_use > 0),
    CONSTRAINT chk_invites_used CHECK (used >= 0 AND used <= max_use)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE invite_uses (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    invite_code VARCHAR(64) NOT NULL,
    user_id BIGINT UNSIGNED NOT NULL,
    used_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE INDEX idx_invite_uses_invite_user (invite_code, user_id),
    INDEX idx_invite_uses_user (user_id),
    CONSTRAINT fk_invite_uses_invite FOREIGN KEY (invite_code) REFERENCES invites(code) ON DELETE RESTRICT,
    CONSTRAINT fk_invite_uses_user FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE third_party_identities (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    user_id BIGINT UNSIGNED NOT NULL,
    provider VARCHAR(50) NOT NULL,
    provider_user_id VARCHAR(255) NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE INDEX idx_third_party_provider_user (provider, provider_user_id),
    INDEX idx_third_party_user (user_id),
    CONSTRAINT fk_third_party_user FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE user_login_devices (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    user_id BIGINT UNSIGNED NOT NULL,
    fingerprint VARCHAR(128) NOT NULL,
    last_login_at DATETIME NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    UNIQUE INDEX idx_user_login_devices_user_fingerprint (user_id, fingerprint),
    CONSTRAINT fk_user_login_devices_user FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE casbin_rule (
    id    BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    ptype VARCHAR(100) NOT NULL DEFAULT '',
    v0    VARCHAR(255) NOT NULL DEFAULT '',
    v1    VARCHAR(255) NOT NULL DEFAULT '',
    v2    VARCHAR(255) NOT NULL DEFAULT '',
    v3    VARCHAR(255) NOT NULL DEFAULT '',
    v4    VARCHAR(255) NOT NULL DEFAULT '',
    v5    VARCHAR(255) NOT NULL DEFAULT '',
    INDEX idx_casbin_ptype (ptype)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

INSERT INTO casbin_rule (ptype, v0, v1, v2, v3) VALUES
    ('p', 'role:admin', 'iam:user', 'read', 'allow'),
    ('p', 'role:admin', 'iam:user', 'write', 'allow'),
    ('p', 'role:admin', 'iam:user', 'operate', 'allow'),
    ('p', 'role:admin', 'iam:user_group', 'read', 'allow'),
    ('p', 'role:admin', 'iam:user_group', 'write', 'allow'),
    ('p', 'role:admin', 'iam:permission', 'read', 'allow'),
    ('p', 'role:admin', 'iam:permission', 'write', 'allow'),
    ('p', 'role:admin', 'iam:invite', 'read', 'allow'),
    ('p', 'role:admin', 'iam:invite', 'write', 'allow'),
    ('p', 'role:admin', 'iam:invite', 'operate', 'allow'),
    ('p', 'role:admin', 'iam:supplier_application', 'read', 'allow'),
    ('p', 'role:admin', 'iam:supplier_application', 'operate', 'allow'),
    ('p', 'role:admin', 'core:resource', 'read', 'allow'),
    ('p', 'role:admin', 'core:resource', 'write', 'allow'),
    ('p', 'role:admin', 'core:resource', 'operate', 'allow'),
    ('p', 'role:super_admin', 'iam:user', 'read', 'allow'),
    ('p', 'role:super_admin', 'iam:user', 'write', 'allow'),
    ('p', 'role:super_admin', 'iam:user', 'operate', 'allow'),
    ('p', 'role:super_admin', 'iam:user_group', 'read', 'allow'),
    ('p', 'role:super_admin', 'iam:user_group', 'write', 'allow'),
    ('p', 'role:super_admin', 'iam:permission', 'read', 'allow'),
    ('p', 'role:super_admin', 'iam:permission', 'write', 'allow'),
    ('p', 'role:super_admin', 'iam:invite', 'read', 'allow'),
    ('p', 'role:super_admin', 'iam:invite', 'write', 'allow'),
    ('p', 'role:super_admin', 'iam:invite', 'operate', 'allow'),
    ('p', 'role:super_admin', 'iam:supplier_application', 'read', 'allow'),
    ('p', 'role:super_admin', 'iam:supplier_application', 'operate', 'allow'),
    ('p', 'role:super_admin', 'core:resource', 'read', 'allow'),
    ('p', 'role:super_admin', 'core:resource', 'write', 'allow'),
    ('p', 'role:super_admin', 'core:resource', 'operate', 'allow');

CREATE TABLE supplier_applications (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    applicant_user_id BIGINT UNSIGNED NOT NULL,
    reason VARCHAR(1000) NOT NULL,
    status VARCHAR(32) NOT NULL DEFAULT 'reviewing' COMMENT 'reviewing|approved|rejected|canceled',
    reviewing_applicant_user_id BIGINT UNSIGNED GENERATED ALWAYS AS (CASE WHEN status = 'reviewing' THEN applicant_user_id ELSE NULL END) STORED,
    review_reason VARCHAR(500) NOT NULL DEFAULT '',
    reviewed_by BIGINT UNSIGNED NULL,
    reviewed_at DATETIME NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    INDEX idx_supplier_applications_applicant_created (applicant_user_id, created_at),
    INDEX idx_supplier_applications_status_created (status, created_at),
    UNIQUE INDEX idx_supplier_applications_reviewing_applicant (reviewing_applicant_user_id),
    CONSTRAINT fk_supplier_applications_applicant FOREIGN KEY (applicant_user_id) REFERENCES users(id) ON DELETE RESTRICT,
    CONSTRAINT fk_supplier_applications_reviewer FOREIGN KEY (reviewed_by) REFERENCES users(id) ON DELETE SET NULL,
    CONSTRAINT chk_supplier_applications_status CHECK (status IN ('reviewing', 'approved', 'rejected', 'canceled'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE operation_logs (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    operator_user_id BIGINT UNSIGNED NOT NULL,
    operation_type VARCHAR(100) NOT NULL,
    resource_type VARCHAR(100) NOT NULL,
    resource_id VARCHAR(100) NOT NULL,
    path VARCHAR(255) NOT NULL,
    result VARCHAR(32) NOT NULL,
    safe_summary VARCHAR(500) NOT NULL,
    request_id VARCHAR(64) NOT NULL DEFAULT '',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    INDEX idx_operation_logs_operator_created (operator_user_id, created_at),
    INDEX idx_operation_logs_resource_created (resource_type, resource_id, created_at),
    INDEX idx_operation_logs_request_id (request_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;


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


-- SystemLog belongs to BC-GOVERNANCE. This base table is introduced here only
-- because proxy diagnostics is the first staged consumer.
CREATE TABLE system_logs (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    level VARCHAR(32) NOT NULL,
    module VARCHAR(100) NOT NULL,
    event_type VARCHAR(100) NOT NULL,
    request_id VARCHAR(64) NOT NULL DEFAULT '',
    biz_type VARCHAR(100) NOT NULL DEFAULT '',
    biz_id VARCHAR(100) NOT NULL DEFAULT '',
    message VARCHAR(500) NOT NULL,
    detail VARCHAR(1000) NOT NULL DEFAULT '',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    INDEX idx_system_logs_module_created (module, created_at),
    INDEX idx_system_logs_event_created (event_type, created_at),
    INDEX idx_system_logs_biz_created (biz_type, biz_id, created_at),
    INDEX idx_system_logs_request_id (request_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE proxies (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    pool VARCHAR(16) NOT NULL COMMENT 'resource|system',
    url VARCHAR(1024) NOT NULL COMMENT 'original proxy URL; authorized admin list may return it; logs/errors/diagnostics must stay redacted',
    url_hash CHAR(64) NOT NULL COMMENT 'sha256(url) for unique constraint without indexing the secret URL',
    url_host VARCHAR(255) NOT NULL DEFAULT '' COMMENT 'lowercase proxy host for indexed search',
    expire_at DATETIME NULL COMMENT 'NULL means no expiration',
    ip_version VARCHAR(8) NOT NULL DEFAULT '' COMMENT 'ipv4|ipv6, detected by system',
    outbound_ip VARCHAR(64) NOT NULL DEFAULT '',
    country VARCHAR(64) NOT NULL DEFAULT 'UNKNOWN',
    latency_ms INT NOT NULL DEFAULT 0,
    status VARCHAR(32) NOT NULL DEFAULT 'checking' COMMENT 'checking|normal|abnormal|disabled|expired',
    errors INT NOT NULL DEFAULT 0,
    last_safe_error VARCHAR(500) NOT NULL DEFAULT '',
    last_checked_at DATETIME NULL,
    last_used_at DATETIME NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    UNIQUE INDEX idx_proxies_pool_url_hash (pool, url_hash),
    INDEX idx_proxies_url_host (url_host),
    INDEX idx_proxies_pool_status_expire (pool, status, expire_at),
    INDEX idx_proxies_select_health (pool, status, ip_version, errors, expire_at, last_used_at),
    INDEX idx_proxies_country_status (country, status),
    INDEX idx_proxies_ip_status (ip_version, status),
    INDEX idx_proxies_expire (expire_at),
    INDEX idx_proxies_created (created_at),
    CONSTRAINT chk_proxies_pool CHECK (pool IN ('resource', 'system')),
    CONSTRAINT chk_proxies_ip_version CHECK (ip_version IN ('', 'ipv4', 'ipv6')),
    CONSTRAINT chk_proxies_status CHECK (status IN ('checking', 'normal', 'abnormal', 'disabled', 'expired')),
    CONSTRAINT chk_proxies_errors CHECK (errors >= 0),
    CONSTRAINT chk_proxies_latency CHECK (latency_ms >= 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE proxy_bindings (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    bind_key VARCHAR(255) NOT NULL COMMENT 'current business key, Microsoft email address',
    proxy_id BIGINT UNSIGNED NOT NULL,
    ip_version VARCHAR(8) NOT NULL COMMENT 'ipv4|ipv6',
    expire_at DATETIME NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_used_at DATETIME NULL,
    UNIQUE INDEX idx_proxy_bindings_key_ip (bind_key, ip_version),
    INDEX idx_proxy_bindings_proxy_expire (proxy_id, expire_at),
    INDEX idx_proxy_bindings_expire_proxy (expire_at, proxy_id),
    INDEX idx_proxy_bindings_expire (expire_at),
    CONSTRAINT fk_proxy_bindings_proxy FOREIGN KEY (proxy_id) REFERENCES proxies(id) ON DELETE CASCADE,
    CONSTRAINT chk_proxy_bindings_ip CHECK (ip_version IN ('ipv4', 'ipv6'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE proxy_check_jobs (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    kind VARCHAR(16) NOT NULL COMMENT 'single|batch',
    batch_mode VARCHAR(16) NOT NULL DEFAULT '' COMMENT 'ids|filter for batch jobs',
    status VARCHAR(32) NOT NULL DEFAULT 'pending' COMMENT 'pending|queued|running|succeeded|failed',
    proxy_id BIGINT UNSIGNED NOT NULL DEFAULT 0,
    filter_json TEXT NOT NULL,
    operator_user_id BIGINT UNSIGNED NOT NULL DEFAULT 0,
    request_id VARCHAR(64) NOT NULL DEFAULT '',
    path VARCHAR(255) NOT NULL DEFAULT '',
    last_safe_error VARCHAR(500) NOT NULL DEFAULT '',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    INDEX idx_proxy_check_jobs_status_created (status, created_at),
    INDEX idx_proxy_check_jobs_proxy_created (proxy_id, created_at),
    INDEX idx_proxy_check_jobs_request_id (request_id),
    CONSTRAINT chk_proxy_check_jobs_kind CHECK (kind IN ('single', 'batch')),
    CONSTRAINT chk_proxy_check_jobs_batch_mode CHECK (batch_mode IN ('', 'ids', 'filter')),
    CONSTRAINT chk_proxy_check_jobs_status CHECK (status IN ('pending', 'queued', 'running', 'succeeded', 'failed'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE proxy_check_job_items (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    job_id BIGINT UNSIGNED NOT NULL,
    proxy_id BIGINT UNSIGNED NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE INDEX idx_proxy_check_job_items_job_proxy (job_id, proxy_id),
    INDEX idx_proxy_check_job_items_proxy (proxy_id),
    CONSTRAINT fk_proxy_check_job_items_job FOREIGN KEY (job_id) REFERENCES proxy_check_jobs(id) ON DELETE CASCADE,
    CONSTRAINT fk_proxy_check_job_items_proxy FOREIGN KEY (proxy_id) REFERENCES proxies(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

INSERT INTO casbin_rule (ptype, v0, v1, v2, v3)
VALUES
    ('p', 'role:admin', 'proxy:proxy', 'read', 'allow'),
    ('p', 'role:admin', 'proxy:proxy', 'write', 'allow'),
    ('p', 'role:admin', 'proxy:proxy', 'operate', 'allow'),
    ('p', 'role:super_admin', 'proxy:proxy', 'read', 'allow'),
    ('p', 'role:super_admin', 'proxy:proxy', 'write', 'allow'),
    ('p', 'role:super_admin', 'proxy:proxy', 'operate', 'allow');


CREATE TABLE outbound_mails (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    idempotency_key CHAR(64) NOT NULL,
    request_hash CHAR(64) NOT NULL,
    purpose VARCHAR(64) NOT NULL COMMENT 'verification_code|system_notification|security_notice',
    sender VARCHAR(320) NOT NULL,
    recipient VARCHAR(320) NOT NULL,
    subject VARCHAR(500) NOT NULL,
    text_body MEDIUMTEXT NOT NULL,
    html_body MEDIUMTEXT NOT NULL,
    status VARCHAR(32) NOT NULL DEFAULT 'pending' COMMENT 'pending|sending|sent|failed',
    retries INT NOT NULL DEFAULT 0,
    failure_reason VARCHAR(500) NOT NULL DEFAULT '',
    sent_at DATETIME NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    UNIQUE INDEX idx_outbound_mails_idempotency_key (idempotency_key),
    INDEX idx_outbound_mails_status_created (status, created_at, id),
    INDEX idx_outbound_mails_status_updated (status, updated_at),
    CONSTRAINT chk_outbound_mails_purpose CHECK (purpose IN ('verification_code', 'system_notification', 'security_notice')),
    CONSTRAINT chk_outbound_mails_status CHECK (status IN ('pending', 'sending', 'sent', 'failed')),
    CONSTRAINT chk_outbound_mails_retries CHECK (retries >= 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE inbound_mails (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    envelope_from VARCHAR(320) NOT NULL,
    recipient VARCHAR(320) NOT NULL,
    resource_id BIGINT UNSIGNED NOT NULL,
    resource_type VARCHAR(32) NOT NULL COMMENT 'microsoft|domain',
    owner_user_id BIGINT UNSIGNED NOT NULL,
    source_object_key VARCHAR(500) NOT NULL COMMENT 'private MinIO object key for original RFC822 message',
    status VARCHAR(32) NOT NULL DEFAULT 'pending' COMMENT 'pending|processing|stored|failed',
    failure_reason VARCHAR(500) NOT NULL DEFAULT '' COMMENT 'sanitized diagnostic summary',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    INDEX idx_inbound_mails_status_created (status, created_at, id),
    INDEX idx_inbound_mails_status_updated (status, updated_at),
    INDEX idx_inbound_mails_resource_created (resource_type, resource_id, created_at),
    INDEX idx_inbound_mails_recipient_created (recipient, created_at),
    INDEX idx_inbound_mails_object_key (source_object_key),
    CONSTRAINT fk_inbound_mails_resource_owner FOREIGN KEY (resource_id, resource_type, owner_user_id) REFERENCES email_resources(id, type, owner_user_id) ON DELETE RESTRICT,
    CONSTRAINT chk_inbound_mails_resource_type CHECK (resource_type IN ('microsoft', 'domain')),
    CONSTRAINT chk_inbound_mails_status CHECK (status IN ('pending', 'processing', 'stored', 'failed'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE microsoft_binding_mailboxes (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    resource_id BIGINT UNSIGNED NOT NULL,
    resource_type VARCHAR(32) NOT NULL DEFAULT 'microsoft' COMMENT 'mirrors email_resources.type for DB-level traceability',
    owner_user_id BIGINT UNSIGNED NOT NULL,
    account_email VARCHAR(255) NOT NULL,
    binding_address VARCHAR(320) NOT NULL,
    purpose VARCHAR(64) NOT NULL DEFAULT 'validation',
    status VARCHAR(32) NOT NULL DEFAULT 'pending' COMMENT 'pending|code_sent|verified|timeout|failed|expired',
    active_binding_address VARCHAR(320) GENERATED ALWAYS AS (CASE WHEN status IN ('pending', 'code_sent', 'verified', 'timeout', 'failed') THEN binding_address ELSE NULL END) STORED,
    code_msg_id VARCHAR(255) NOT NULL DEFAULT '',
    bound_display VARCHAR(255) NOT NULL DEFAULT '',
    category VARCHAR(64) NOT NULL DEFAULT '',
    last_safe_error VARCHAR(500) NOT NULL DEFAULT '',
    selected_at DATETIME NULL,
    code_sent_at DATETIME NULL,
    verified_at DATETIME NULL,
    expires_at DATETIME NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    UNIQUE INDEX idx_microsoft_binding_resource (resource_id),
    UNIQUE INDEX idx_microsoft_binding_active_address (active_binding_address),
    INDEX idx_microsoft_binding_address_status (binding_address, status),
    INDEX idx_microsoft_binding_owner_created (owner_user_id, created_at),
    CONSTRAINT fk_microsoft_binding_resource_owner FOREIGN KEY (resource_id, resource_type, owner_user_id) REFERENCES email_resources(id, type, owner_user_id) ON DELETE CASCADE,
    CONSTRAINT fk_microsoft_binding_resource FOREIGN KEY (resource_id) REFERENCES microsoft_resources(id) ON DELETE CASCADE,
    CONSTRAINT fk_microsoft_binding_owner FOREIGN KEY (owner_user_id) REFERENCES users(id) ON DELETE RESTRICT,
    CONSTRAINT chk_microsoft_binding_resource_type CHECK (resource_type = 'microsoft'),
    CONSTRAINT chk_microsoft_binding_status CHECK (status IN ('pending', 'code_sent', 'verified', 'timeout', 'failed', 'expired'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

ALTER TABLE generated_mailboxes
    ADD INDEX idx_generated_mailboxes_email_status (email, status);


ALTER TABLE domain_resources
    ADD COLUMN last_safe_error VARCHAR(500) NOT NULL DEFAULT '' COMMENT 'sanitized diagnostic summary' AFTER status;

CREATE TABLE resource_validation_jobs (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    resource_id BIGINT UNSIGNED NOT NULL,
    resource_type VARCHAR(32) NOT NULL COMMENT 'microsoft|domain',
    owner_user_id BIGINT UNSIGNED NOT NULL,
    status VARCHAR(32) NOT NULL DEFAULT 'queued' COMMENT 'queued|running|succeeded|failed',
    active_resource_id BIGINT UNSIGNED GENERATED ALWAYS AS (CASE WHEN status IN ('queued', 'running') THEN resource_id ELSE NULL END) STORED,
    attempts INT NOT NULL DEFAULT 0,
    max_attempts INT NOT NULL DEFAULT 3,
    last_safe_error VARCHAR(500) NOT NULL DEFAULT '',
    request_id VARCHAR(64) NOT NULL DEFAULT '',
    path VARCHAR(255) NOT NULL DEFAULT '',
    started_at DATETIME NULL,
    finished_at DATETIME NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    INDEX idx_resource_validation_jobs_status_updated (status, updated_at, id),
    INDEX idx_resource_validation_jobs_resource_created (resource_id, created_at),
    INDEX idx_resource_validation_jobs_owner_created (owner_user_id, created_at),
    INDEX idx_resource_validation_jobs_request_id (request_id),
    UNIQUE INDEX idx_resource_validation_jobs_active_resource (active_resource_id),
    CONSTRAINT fk_resource_validation_jobs_resource_owner FOREIGN KEY (resource_id, resource_type, owner_user_id) REFERENCES email_resources(id, type, owner_user_id) ON DELETE RESTRICT,
    CONSTRAINT chk_resource_validation_jobs_type CHECK (resource_type IN ('microsoft', 'domain')),
    CONSTRAINT chk_resource_validation_jobs_status CHECK (status IN ('queued', 'running', 'succeeded', 'failed')),
    CONSTRAINT chk_resource_validation_jobs_attempts CHECK (attempts >= 0 AND max_attempts > 0 AND attempts <= max_attempts)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;


CREATE TABLE projects (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    name VARCHAR(120) NOT NULL,
    target_platform VARCHAR(120) NOT NULL,
    logo_url VARCHAR(500) NOT NULL DEFAULT '',
    description VARCHAR(1000) NOT NULL DEFAULT '',
    status VARCHAR(32) NOT NULL DEFAULT 'reviewing' COMMENT 'reviewing|listed|delisted',
    access_type VARCHAR(32) NOT NULL DEFAULT 'public' COMMENT 'public|private',
    applicant_user_id BIGINT UNSIGNED NULL,
    review_reason VARCHAR(500) NOT NULL DEFAULT '',
    loose_match TINYINT(1) NOT NULL DEFAULT 1,
    listed_name VARCHAR(120) GENERATED ALWAYS AS (CASE WHEN status = 'listed' THEN name ELSE NULL END) STORED,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    UNIQUE INDEX idx_projects_listed_name (listed_name),
	    INDEX idx_projects_status_created (status, created_at),
	    INDEX idx_projects_access_status_created (access_type, status, created_at),
	    INDEX idx_projects_applicant_created (applicant_user_id, created_at),
	    INDEX idx_projects_status_updated (status, updated_at, id),
	    INDEX idx_projects_access_status_updated (access_type, status, updated_at, id),
	    INDEX idx_projects_applicant_updated (applicant_user_id, updated_at, id),
	    INDEX idx_projects_target_platform (target_platform),
    FULLTEXT INDEX idx_projects_search (name, target_platform),
    CONSTRAINT fk_projects_applicant FOREIGN KEY (applicant_user_id) REFERENCES users(id) ON DELETE SET NULL,
    CONSTRAINT chk_projects_status CHECK (status IN ('reviewing', 'listed', 'delisted')),
    CONSTRAINT chk_projects_access_type CHECK (access_type IN ('public', 'private'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE project_products (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    project_id BIGINT UNSIGNED NOT NULL,
    type VARCHAR(32) NOT NULL COMMENT 'microsoft|domain',
    status VARCHAR(32) NOT NULL DEFAULT 'enabled' COMMENT 'enabled|disabled',
    code_enabled TINYINT(1) NOT NULL DEFAULT 1,
    purchase_enabled TINYINT(1) NOT NULL DEFAULT 0,
    code_price DECIMAL(18,6) NOT NULL DEFAULT 0,
    purchase_price DECIMAL(18,6) NOT NULL DEFAULT 0,
    code_supplier_price DECIMAL(18,6) NOT NULL DEFAULT 0,
    purchase_supplier_price DECIMAL(18,6) NOT NULL DEFAULT 0,
    code_window_minutes INT NOT NULL DEFAULT 10,
    activation_window_minutes INT NOT NULL DEFAULT 60,
    warranty_minutes INT NOT NULL DEFAULT 60,
    main_weight INT NOT NULL DEFAULT 1,
    dot_weight INT NOT NULL DEFAULT 0,
    plus_weight INT NOT NULL DEFAULT 0,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    UNIQUE INDEX idx_project_products_project_type (project_id, type),
    INDEX idx_project_products_type_status (type, status),
    CONSTRAINT fk_project_products_project FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE,
    CONSTRAINT chk_project_products_type CHECK (type IN ('microsoft', 'domain')),
    CONSTRAINT chk_project_products_status CHECK (status IN ('enabled', 'disabled')),
    CONSTRAINT chk_project_products_service CHECK (code_enabled = 1 OR purchase_enabled = 1),
    CONSTRAINT chk_project_products_money CHECK (
        code_price >= 0
        AND purchase_price >= 0
        AND code_supplier_price >= 0
        AND purchase_supplier_price >= 0
    ),
    CONSTRAINT chk_project_products_windows CHECK (
        code_window_minutes >= 0
        AND activation_window_minutes >= 0
        AND warranty_minutes >= 0
        AND (code_enabled = 0 OR code_window_minutes > 0)
        AND (purchase_enabled = 0 OR (activation_window_minutes > 0 AND warranty_minutes > 0))
    ),
    CONSTRAINT chk_project_products_weights CHECK (
        main_weight >= 0
        AND dot_weight >= 0
        AND plus_weight >= 0
        AND (type <> 'microsoft' OR main_weight + dot_weight + plus_weight > 0)
        AND (type <> 'domain' OR (main_weight = 0 AND dot_weight = 0 AND plus_weight = 0))
    )
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE project_mail_rules (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    project_id BIGINT UNSIGNED NOT NULL,
    rule_type VARCHAR(32) NOT NULL COMMENT 'sender|recipient|subject|body',
    pattern VARCHAR(500) NOT NULL,
    enabled TINYINT(1) NOT NULL DEFAULT 1,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
	    INDEX idx_project_mail_rules_project_type (project_id, rule_type, enabled),
	    CONSTRAINT fk_project_mail_rules_project FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE,
	    CONSTRAINT chk_project_mail_rules_type CHECK (rule_type IN ('sender', 'recipient', 'subject', 'body')),
	    CONSTRAINT chk_project_mail_rules_recipient_pattern CHECK (rule_type <> 'recipient' OR pattern IN ('exact', 'dot', 'plus'))
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE project_accesses (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    project_id BIGINT UNSIGNED NOT NULL,
    user_id BIGINT UNSIGNED NOT NULL,
    granted_by BIGINT UNSIGNED NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
	    UNIQUE INDEX idx_project_accesses_project_user (project_id, user_id),
	    INDEX idx_project_accesses_user_created (user_id, created_at),
	    INDEX idx_project_accesses_project_created (project_id, created_at, id),
    CONSTRAINT fk_project_accesses_project FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE,
    CONSTRAINT fk_project_accesses_user FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE,
    CONSTRAINT fk_project_accesses_granted_by FOREIGN KEY (granted_by) REFERENCES users(id) ON DELETE RESTRICT
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

INSERT INTO casbin_rule (ptype, v0, v1, v2, v3) VALUES
    ('p', 'role:admin', 'core:project', 'read', 'allow'),
    ('p', 'role:admin', 'core:project', 'write', 'allow'),
    ('p', 'role:admin', 'core:project', 'operate', 'allow'),
    ('p', 'role:super_admin', 'core:project', 'read', 'allow'),
    ('p', 'role:super_admin', 'core:project', 'write', 'allow'),
    ('p', 'role:super_admin', 'core:project', 'operate', 'allow');


ALTER TABLE users
    ADD FULLTEXT INDEX idx_users_search (email, nickname);


ALTER TABLE microsoft_resources
    ADD COLUMN plus_daily_limit INT NOT NULL DEFAULT 10000,
    ADD COLUMN alloc_bucket TINYINT UNSIGNED NOT NULL DEFAULT 0,
    ADD INDEX idx_microsoft_inventory_public (for_sale, status, id, plus_daily_limit),
    ADD INDEX idx_microsoft_alloc_public (alloc_bucket, for_sale, status, last_allocated_at, quality_score, id),
    ADD INDEX idx_microsoft_alloc_owned (alloc_bucket, status, for_sale, last_allocated_at, id);

UPDATE microsoft_resources SET alloc_bucket = MOD(id, 64);

ALTER TABLE domain_resources
    ADD COLUMN mailbox_daily_limit INT NOT NULL DEFAULT 10000,
    ADD COLUMN alloc_bucket TINYINT UNSIGNED NOT NULL DEFAULT 0,
    ADD INDEX idx_domain_inventory_public (purpose, status, id, mailbox_daily_limit),
    ADD INDEX idx_domain_alloc_public (alloc_bucket, purpose, status, last_allocated_at, id),
    ADD INDEX idx_domain_alloc_owned (alloc_bucket, owner_user_id, purpose, status, last_allocated_at, id);

UPDATE domain_resources SET alloc_bucket = MOD(id, 64);

ALTER TABLE project_products
    ADD UNIQUE INDEX idx_project_products_id_project (id, project_id);

ALTER TABLE explicit_aliases
    ADD UNIQUE INDEX idx_explicit_aliases_id_resource (id, resource_id),
    ADD INDEX idx_explicit_aliases_alloc_reuse (resource_id, status, id);

ALTER TABLE dot_aliases
    ADD UNIQUE INDEX idx_dot_aliases_id_resource (id, resource_id),
    ADD INDEX idx_dot_aliases_alloc_reuse (resource_id, status, id);

ALTER TABLE plus_aliases
    ADD UNIQUE INDEX idx_plus_aliases_id_resource (id, resource_id),
    ADD INDEX idx_plus_aliases_alloc_reuse (resource_id, status, id);

ALTER TABLE generated_mailboxes
    ADD UNIQUE INDEX idx_generated_mailboxes_id_resource (id, resource_id),
    ADD INDEX idx_generated_mailboxes_alloc_reuse (resource_id, status, last_allocated_at, id);

CREATE TABLE microsoft_routing_candidates (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    project_id BIGINT UNSIGNED NOT NULL,
    resource_id BIGINT UNSIGNED NOT NULL,
    email_address VARCHAR(255) NOT NULL,
    domain_suffix VARCHAR(255) NOT NULL DEFAULT '',
    for_sale TINYINT(1) NOT NULL DEFAULT 1,
    quality_score INT NOT NULL DEFAULT 0,
    status VARCHAR(32) NOT NULL DEFAULT 'normal' COMMENT 'normal|abnormal|disabled',
    alloc_bucket TINYINT UNSIGNED NOT NULL DEFAULT 0,
    last_allocated_at DATETIME NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    UNIQUE INDEX idx_ms_candidates_project_resource (project_id, resource_id),
    INDEX idx_ms_candidates_project_bucket (project_id, alloc_bucket, status, for_sale, last_allocated_at, quality_score, resource_id),
    INDEX idx_ms_candidates_resource (resource_id),
    CONSTRAINT fk_ms_candidates_project FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE,
    CONSTRAINT fk_ms_candidates_resource FOREIGN KEY (resource_id) REFERENCES microsoft_resources(id) ON DELETE CASCADE,
    CONSTRAINT chk_ms_candidates_status CHECK (status IN ('normal', 'abnormal', 'disabled'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE domain_routing_candidates (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    project_id BIGINT UNSIGNED NOT NULL,
    resource_id BIGINT UNSIGNED NOT NULL,
    domain VARCHAR(255) NOT NULL,
    domain_tld VARCHAR(64) NOT NULL DEFAULT '',
    purpose VARCHAR(32) NOT NULL DEFAULT 'sale',
    status VARCHAR(32) NOT NULL DEFAULT 'normal' COMMENT 'normal|abnormal|disabled',
    alloc_bucket TINYINT UNSIGNED NOT NULL DEFAULT 0,
    last_allocated_at DATETIME NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    UNIQUE INDEX idx_domain_candidates_project_resource (project_id, resource_id),
    INDEX idx_domain_candidates_project_bucket (project_id, alloc_bucket, status, purpose, last_allocated_at, resource_id),
    INDEX idx_domain_candidates_resource (resource_id),
    CONSTRAINT fk_domain_candidates_project FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE,
    CONSTRAINT fk_domain_candidates_resource FOREIGN KEY (resource_id) REFERENCES domain_resources(id) ON DELETE CASCADE,
    CONSTRAINT chk_domain_candidates_purpose CHECK (purpose IN ('sale', 'not_sale')),
    CONSTRAINT chk_domain_candidates_status CHECK (status IN ('normal', 'abnormal', 'disabled'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE allocation_order_guards (
    order_no VARCHAR(64) PRIMARY KEY,
    type VARCHAR(32) NOT NULL COMMENT 'microsoft|domain',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE INDEX idx_allocation_order_guards_order_type (order_no, type),
    CONSTRAINT chk_allocation_order_guards_type CHECK (type IN ('microsoft', 'domain'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE allocation_daily_usages (
    usage_date DATE NOT NULL,
    resource_type VARCHAR(32) NOT NULL COMMENT 'microsoft|domain',
    resource_id BIGINT UNSIGNED NOT NULL,
    usage_kind VARCHAR(32) NOT NULL COMMENT 'plus|domain_mailbox',
    used_count INT NOT NULL DEFAULT 0,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    PRIMARY KEY (usage_date, resource_type, resource_id, usage_kind),
    INDEX idx_allocation_daily_usages_resource (resource_type, resource_id, usage_kind, usage_date),
    CONSTRAINT chk_allocation_daily_usages_type CHECK (resource_type IN ('microsoft', 'domain')),
    CONSTRAINT chk_allocation_daily_usages_kind CHECK (usage_kind IN ('plus', 'domain_mailbox')),
    CONSTRAINT chk_allocation_daily_usages_count CHECK (used_count >= 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE microsoft_allocations (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    order_no VARCHAR(64) NOT NULL,
    guard_type VARCHAR(32) NOT NULL DEFAULT 'microsoft',
    project_id BIGINT UNSIGNED NOT NULL,
    product_id BIGINT UNSIGNED NOT NULL,
    resource_id BIGINT UNSIGNED NOT NULL,
    supply_scope VARCHAR(16) NOT NULL COMMENT 'owned|public',
    mailbox VARCHAR(32) NOT NULL COMMENT 'main|alias|dot|plus',
    explicit_alias_id BIGINT UNSIGNED NULL,
    dot_alias_id BIGINT UNSIGNED NULL,
    plus_alias_id BIGINT UNSIGNED NULL,
    email VARCHAR(255) NOT NULL DEFAULT '',
    status VARCHAR(32) NOT NULL DEFAULT 'allocated' COMMENT 'allocated|released',
    active_main_resource_id BIGINT UNSIGNED GENERATED ALWAYS AS (
        CASE WHEN status = 'allocated' AND mailbox = 'main' THEN resource_id ELSE NULL END
    ) STORED,
    active_explicit_alias_id BIGINT UNSIGNED GENERATED ALWAYS AS (
        CASE WHEN status = 'allocated' AND mailbox = 'alias' THEN explicit_alias_id ELSE NULL END
    ) STORED,
    active_dot_project_id BIGINT UNSIGNED GENERATED ALWAYS AS (
        CASE WHEN status = 'allocated' AND mailbox = 'dot' THEN project_id ELSE NULL END
    ) STORED,
    active_dot_alias_id BIGINT UNSIGNED GENERATED ALWAYS AS (
        CASE WHEN status = 'allocated' AND mailbox = 'dot' THEN dot_alias_id ELSE NULL END
    ) STORED,
    active_plus_project_id BIGINT UNSIGNED GENERATED ALWAYS AS (
        CASE WHEN status = 'allocated' AND mailbox = 'plus' THEN project_id ELSE NULL END
    ) STORED,
    active_plus_alias_id BIGINT UNSIGNED GENERATED ALWAYS AS (
        CASE WHEN status = 'allocated' AND mailbox = 'plus' THEN plus_alias_id ELSE NULL END
    ) STORED,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    released_at DATETIME NULL,
    UNIQUE INDEX idx_ms_alloc_order (order_no),
    UNIQUE INDEX idx_ms_alloc_active_main (active_main_resource_id),
    UNIQUE INDEX idx_ms_alloc_active_alias (active_explicit_alias_id),
    UNIQUE INDEX idx_ms_alloc_active_dot (active_dot_project_id, active_dot_alias_id),
    UNIQUE INDEX idx_ms_alloc_active_plus (active_plus_project_id, active_plus_alias_id),
    INDEX idx_ms_alloc_guard_type (order_no, guard_type),
    INDEX idx_ms_alloc_product_project (product_id, project_id),
    INDEX idx_ms_alloc_explicit_alias_resource (explicit_alias_id, resource_id),
    INDEX idx_ms_alloc_dot_alias_resource (dot_alias_id, resource_id),
    INDEX idx_ms_alloc_plus_alias_resource (plus_alias_id, resource_id),
    INDEX idx_ms_alloc_project_created (project_id, created_at, id),
    INDEX idx_ms_alloc_resource_status (resource_id, status),
    INDEX idx_ms_alloc_resource_mailbox_created (resource_id, mailbox, created_at),
    INDEX idx_ms_alloc_email_status (email, status),
    CONSTRAINT fk_ms_alloc_guard FOREIGN KEY (order_no, guard_type) REFERENCES allocation_order_guards(order_no, type) ON DELETE RESTRICT,
    CONSTRAINT fk_ms_alloc_project FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE RESTRICT,
    CONSTRAINT fk_ms_alloc_product_project FOREIGN KEY (product_id, project_id) REFERENCES project_products(id, project_id) ON DELETE RESTRICT,
    CONSTRAINT fk_ms_alloc_resource FOREIGN KEY (resource_id) REFERENCES microsoft_resources(id) ON DELETE RESTRICT,
    CONSTRAINT fk_ms_alloc_explicit_alias_resource FOREIGN KEY (explicit_alias_id, resource_id) REFERENCES explicit_aliases(id, resource_id) ON DELETE RESTRICT,
    CONSTRAINT fk_ms_alloc_dot_alias_resource FOREIGN KEY (dot_alias_id, resource_id) REFERENCES dot_aliases(id, resource_id) ON DELETE RESTRICT,
    CONSTRAINT fk_ms_alloc_plus_alias_resource FOREIGN KEY (plus_alias_id, resource_id) REFERENCES plus_aliases(id, resource_id) ON DELETE RESTRICT,
    CONSTRAINT chk_ms_alloc_guard_type CHECK (guard_type = 'microsoft'),
    CONSTRAINT chk_ms_alloc_supply_scope CHECK (supply_scope IN ('owned', 'public')),
    CONSTRAINT chk_ms_alloc_mailbox CHECK (mailbox IN ('main', 'alias', 'dot', 'plus')),
    CONSTRAINT chk_ms_alloc_status CHECK (status IN ('allocated', 'released')),
    CONSTRAINT chk_ms_alloc_email CHECK (email <> ''),
    CONSTRAINT chk_ms_alloc_alias_shape CHECK (
        (mailbox = 'main' AND explicit_alias_id IS NULL AND dot_alias_id IS NULL AND plus_alias_id IS NULL)
        OR (mailbox = 'alias' AND explicit_alias_id IS NOT NULL AND dot_alias_id IS NULL AND plus_alias_id IS NULL)
        OR (mailbox = 'dot' AND explicit_alias_id IS NULL AND dot_alias_id IS NOT NULL AND plus_alias_id IS NULL)
        OR (mailbox = 'plus' AND explicit_alias_id IS NULL AND dot_alias_id IS NULL AND plus_alias_id IS NOT NULL)
    )
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE domain_allocations (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    order_no VARCHAR(64) NOT NULL,
    guard_type VARCHAR(32) NOT NULL DEFAULT 'domain',
    project_id BIGINT UNSIGNED NOT NULL,
    product_id BIGINT UNSIGNED NOT NULL,
    resource_id BIGINT UNSIGNED NOT NULL,
    supply_scope VARCHAR(16) NOT NULL COMMENT 'owned|public',
    mailbox_id BIGINT UNSIGNED NOT NULL,
    email VARCHAR(255) NOT NULL,
    status VARCHAR(32) NOT NULL DEFAULT 'allocated' COMMENT 'allocated|released',
    active_project_id BIGINT UNSIGNED GENERATED ALWAYS AS (
        CASE WHEN status = 'allocated' THEN project_id ELSE NULL END
    ) STORED,
    active_mailbox_id BIGINT UNSIGNED GENERATED ALWAYS AS (
        CASE WHEN status = 'allocated' THEN mailbox_id ELSE NULL END
    ) STORED,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    released_at DATETIME NULL,
    UNIQUE INDEX idx_domain_alloc_order (order_no),
    UNIQUE INDEX idx_domain_alloc_active_mailbox (active_project_id, active_mailbox_id),
    INDEX idx_domain_alloc_guard_type (order_no, guard_type),
    INDEX idx_domain_alloc_product_project (product_id, project_id),
    INDEX idx_domain_alloc_mailbox_resource (mailbox_id, resource_id),
    INDEX idx_domain_alloc_project_created (project_id, created_at, id),
    INDEX idx_domain_alloc_resource_status (resource_id, status),
    INDEX idx_domain_alloc_resource_created (resource_id, created_at),
    INDEX idx_domain_alloc_email_status (email, status),
    CONSTRAINT fk_domain_alloc_guard FOREIGN KEY (order_no, guard_type) REFERENCES allocation_order_guards(order_no, type) ON DELETE RESTRICT,
    CONSTRAINT fk_domain_alloc_project FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE RESTRICT,
    CONSTRAINT fk_domain_alloc_product_project FOREIGN KEY (product_id, project_id) REFERENCES project_products(id, project_id) ON DELETE RESTRICT,
    CONSTRAINT fk_domain_alloc_resource FOREIGN KEY (resource_id) REFERENCES domain_resources(id) ON DELETE RESTRICT,
    CONSTRAINT fk_domain_alloc_mailbox_resource FOREIGN KEY (mailbox_id, resource_id) REFERENCES generated_mailboxes(id, resource_id) ON DELETE RESTRICT,
    CONSTRAINT chk_domain_alloc_guard_type CHECK (guard_type = 'domain'),
    CONSTRAINT chk_domain_alloc_supply_scope CHECK (supply_scope IN ('owned', 'public')),
    CONSTRAINT chk_domain_alloc_status CHECK (status IN ('allocated', 'released')),
    CONSTRAINT chk_domain_alloc_email CHECK (email <> '')
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE allocation_candidate_refresh_jobs (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    project_id BIGINT UNSIGNED NOT NULL,
    operator_user_id BIGINT UNSIGNED NOT NULL,
    status VARCHAR(32) NOT NULL DEFAULT 'pending' COMMENT 'pending|queued|running|succeeded|failed',
    affected INT NOT NULL DEFAULT 0,
    attempts INT NOT NULL DEFAULT 0,
    max_attempts INT NOT NULL DEFAULT 1,
    active_project_id BIGINT UNSIGNED GENERATED ALWAYS AS (
        CASE WHEN status IN ('pending', 'queued', 'running') THEN project_id ELSE NULL END
    ) STORED,
    last_safe_error VARCHAR(500) NOT NULL DEFAULT '',
    request_id VARCHAR(64) NOT NULL DEFAULT '',
    path VARCHAR(255) NOT NULL DEFAULT '',
    started_at DATETIME NULL,
    finished_at DATETIME NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    UNIQUE INDEX idx_alloc_refresh_active_project (active_project_id),
    INDEX idx_alloc_refresh_status_updated (status, updated_at, id),
    INDEX idx_alloc_refresh_project_created (project_id, created_at),
    INDEX idx_alloc_refresh_request_id (request_id),
    CONSTRAINT fk_alloc_refresh_project FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE RESTRICT,
    CONSTRAINT fk_alloc_refresh_operator FOREIGN KEY (operator_user_id) REFERENCES users(id) ON DELETE RESTRICT,
    CONSTRAINT chk_alloc_refresh_status CHECK (status IN ('pending', 'queued', 'running', 'succeeded', 'failed')),
    CONSTRAINT chk_alloc_refresh_attempts CHECK (attempts >= 0 AND max_attempts = 1 AND attempts <= max_attempts),
    CONSTRAINT chk_alloc_refresh_affected CHECK (affected >= 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

INSERT INTO casbin_rule (ptype, v0, v1, v2, v3) VALUES
    ('p', 'role:admin', 'alloc:allocation', 'read', 'allow'),
    ('p', 'role:admin', 'alloc:allocation', 'operate', 'allow'),
    ('p', 'role:super_admin', 'alloc:allocation', 'read', 'allow'),
    ('p', 'role:super_admin', 'alloc:allocation', 'operate', 'allow');


CREATE TABLE wallets (
    user_id BIGINT UNSIGNED PRIMARY KEY,
    consumer_balance DECIMAL(18,2) NOT NULL DEFAULT 0.00,
    supplier_available DECIMAL(18,2) NOT NULL DEFAULT 0.00,
    supplier_frozen DECIMAL(18,2) NOT NULL DEFAULT 0.00,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    CONSTRAINT fk_wallets_user FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE RESTRICT,
    CONSTRAINT chk_wallets_consumer_nonnegative CHECK (consumer_balance >= 0),
    CONSTRAINT chk_wallets_supplier_available_nonnegative CHECK (supplier_available >= 0),
    CONSTRAINT chk_wallets_supplier_frozen_nonnegative CHECK (supplier_frozen >= 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE wallet_transactions (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    transaction_no VARCHAR(64) NOT NULL,
    user_id BIGINT UNSIGNED NOT NULL,
    transaction_type VARCHAR(32) NOT NULL,
    balance_bucket VARCHAR(32) NOT NULL,
    direction VARCHAR(8) NOT NULL,
    amount DECIMAL(18,2) NOT NULL,
    balance_before DECIMAL(18,2) NOT NULL,
    balance_after DECIMAL(18,2) NOT NULL,
    biz_type VARCHAR(32) NOT NULL,
    biz_id VARCHAR(128) NOT NULL,
    idempotency_key VARCHAR(128) NOT NULL DEFAULT '',
    request_id VARCHAR(64) NOT NULL DEFAULT '',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE INDEX idx_wallet_transactions_no (transaction_no),
    INDEX idx_wallet_transactions_user_created (user_id, created_at, id),
    INDEX idx_wallet_transactions_biz (biz_type, biz_id),
    INDEX idx_wallet_transactions_idempotency (idempotency_key),
    CONSTRAINT fk_wallet_transactions_user FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE RESTRICT,
    CONSTRAINT chk_wallet_transactions_type CHECK (transaction_type IN ('recharge', 'debit', 'refund', 'freeze', 'credit', 'withdrawal', 'manual_adjustment', 'card_redeem', 'transfer')),
    CONSTRAINT chk_wallet_transactions_bucket CHECK (balance_bucket IN ('consumer', 'supplier_available', 'supplier_frozen')),
    CONSTRAINT chk_wallet_transactions_direction CHECK (direction IN ('in', 'out')),
    CONSTRAINT chk_wallet_transactions_type_direction CHECK (
        (transaction_type IN ('debit', 'freeze', 'withdrawal') AND direction = 'out')
        OR (transaction_type IN ('recharge', 'refund', 'credit', 'card_redeem', 'transfer') AND direction = 'in')
        OR transaction_type = 'manual_adjustment'
    ),
    CONSTRAINT chk_wallet_transactions_amount CHECK (
        (direction = 'in' AND amount >= 0)
        OR (direction = 'out' AND amount <= 0)
    ),
    CONSTRAINT chk_wallet_transactions_balance CHECK (
        balance_before >= 0
        AND balance_after >= 0
        AND balance_after = balance_before + amount
    ),
    CONSTRAINT chk_wallet_transactions_biz CHECK (biz_type <> '' AND biz_id <> '')
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE idempotency_keys (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    owner_user_id BIGINT UNSIGNED NOT NULL,
    idempotency_key VARCHAR(128) NOT NULL,
    operation VARCHAR(64) NOT NULL,
    request_fingerprint CHAR(64) NOT NULL,
    status VARCHAR(32) NOT NULL DEFAULT 'succeeded',
    response_json JSON NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    UNIQUE INDEX idx_idempotency_owner_key_operation (owner_user_id, idempotency_key, operation),
    INDEX idx_idempotency_created (created_at),
    CONSTRAINT fk_idempotency_user FOREIGN KEY (owner_user_id) REFERENCES users(id) ON DELETE RESTRICT,
    CONSTRAINT chk_idempotency_status CHECK (status IN ('processing', 'succeeded', 'failed')),
    CONSTRAINT chk_idempotency_key_nonempty CHECK (idempotency_key <> '' AND operation <> '' AND request_fingerprint <> '')
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE recharges (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    recharge_no VARCHAR(64) NOT NULL,
    user_id BIGINT UNSIGNED NOT NULL,
    payment_method VARCHAR(32) NOT NULL,
    recharge_quota DECIMAL(18,2) NOT NULL,
    payment_amount DECIMAL(18,2) NOT NULL,
    status VARCHAR(32) NOT NULL DEFAULT 'paying',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    UNIQUE INDEX idx_recharges_no (recharge_no),
    INDEX idx_recharges_user_created (user_id, created_at, id),
    INDEX idx_recharges_status_created (status, created_at),
    CONSTRAINT fk_recharges_user FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE RESTRICT,
    CONSTRAINT chk_recharges_status CHECK (status IN ('paying', 'callback', 'reconciled', 'credited', 'failed')),
    CONSTRAINT chk_recharges_amount CHECK (recharge_quota > 0 AND payment_amount > 0),
    CONSTRAINT chk_recharges_payment_method CHECK (payment_method <> '')
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE card_keys (
    card_key VARCHAR(128) PRIMARY KEY,
    amount DECIMAL(18,2) NOT NULL,
    status VARCHAR(32) NOT NULL DEFAULT 'enabled',
    max_redemptions INT NOT NULL DEFAULT 1,
    redeemed_count INT NOT NULL DEFAULT 0,
    expire_at DATETIME NULL,
    created_by_user_id BIGINT UNSIGNED NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    INDEX idx_card_keys_status_expire (status, expire_at),
    INDEX idx_card_keys_created_by (created_by_user_id, created_at),
    CONSTRAINT fk_card_keys_created_by FOREIGN KEY (created_by_user_id) REFERENCES users(id) ON DELETE SET NULL,
    CONSTRAINT chk_card_keys_status CHECK (status IN ('enabled', 'disabled')),
    CONSTRAINT chk_card_keys_amount CHECK (amount > 0),
    CONSTRAINT chk_card_keys_redemptions CHECK (max_redemptions > 0 AND redeemed_count >= 0 AND redeemed_count <= max_redemptions),
    CONSTRAINT chk_card_keys_key_nonempty CHECK (card_key <> '')
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE card_key_redemptions (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    card_key VARCHAR(128) NOT NULL,
    user_id BIGINT UNSIGNED NOT NULL,
    transaction_id BIGINT UNSIGNED NOT NULL,
    request_id VARCHAR(64) NOT NULL DEFAULT '',
    redeemed_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE INDEX idx_card_redemptions_card_user (card_key, user_id),
    INDEX idx_card_redemptions_user_redeemed (user_id, redeemed_at, id),
    INDEX idx_card_redemptions_transaction (transaction_id),
    CONSTRAINT fk_card_redemptions_card FOREIGN KEY (card_key) REFERENCES card_keys(card_key) ON DELETE RESTRICT,
    CONSTRAINT fk_card_redemptions_user FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE RESTRICT,
    CONSTRAINT fk_card_redemptions_transaction FOREIGN KEY (transaction_id) REFERENCES wallet_transactions(id) ON DELETE RESTRICT
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

INSERT INTO casbin_rule (ptype, v0, v1, v2, v3) VALUES
    ('p', 'role:admin', 'billing:wallet', 'read', 'allow'),
    ('p', 'role:admin', 'billing:wallet', 'write', 'allow'),
    ('p', 'role:admin', 'billing:card', 'read', 'allow'),
    ('p', 'role:admin', 'billing:card', 'write', 'allow'),
    ('p', 'role:admin', 'billing:card', 'operate', 'allow'),
    ('p', 'role:super_admin', 'billing:wallet', 'read', 'allow'),
    ('p', 'role:super_admin', 'billing:wallet', 'write', 'allow'),
    ('p', 'role:super_admin', 'billing:card', 'read', 'allow'),
    ('p', 'role:super_admin', 'billing:card', 'write', 'allow'),
    ('p', 'role:super_admin', 'billing:card', 'operate', 'allow');


ALTER TABLE invites
    ADD COLUMN invite_kind VARCHAR(32) NOT NULL DEFAULT 'admin' AFTER code,
    ADD COLUMN referral_owner_user_id BIGINT UNSIGNED NULL AFTER created_by_user_id,
    ADD CONSTRAINT chk_invites_kind CHECK (invite_kind IN ('admin', 'referral'));

CREATE INDEX idx_invites_kind_created ON invites(invite_kind, created_at);
CREATE UNIQUE INDEX idx_invites_referral_owner ON invites(referral_owner_user_id);
CREATE UNIQUE INDEX idx_invites_code_referral_owner ON invites(code, referral_owner_user_id);

ALTER TABLE invites
    ADD CONSTRAINT fk_invites_referral_owner FOREIGN KEY (referral_owner_user_id) REFERENCES users(id) ON DELETE RESTRICT,
    ADD CONSTRAINT chk_invites_referral_owner CHECK (
        (invite_kind = 'admin' AND referral_owner_user_id IS NULL)
        OR
        (invite_kind = 'referral' AND referral_owner_user_id IS NOT NULL)
    );

CREATE TABLE referral_rewards (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    inviter_user_id BIGINT UNSIGNED NOT NULL,
    invitee_user_id BIGINT UNSIGNED NOT NULL,
    invite_code VARCHAR(64) NOT NULL,
    source_transaction_id BIGINT UNSIGNED NOT NULL,
    transfer_transaction_id BIGINT UNSIGNED NULL,
    source_amount DECIMAL(18,2) NOT NULL,
    reward_amount DECIMAL(18,2) NOT NULL,
    status VARCHAR(32) NOT NULL DEFAULT 'available',
    transferred_at DATETIME NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE INDEX idx_referral_rewards_invitee (invitee_user_id),
    UNIQUE INDEX idx_referral_rewards_source_transaction (source_transaction_id),
    INDEX idx_referral_rewards_transfer_transaction (transfer_transaction_id),
    INDEX idx_referral_rewards_inviter_created (inviter_user_id, created_at, id),
    INDEX idx_referral_rewards_inviter_status (inviter_user_id, status, id),
    INDEX idx_referral_rewards_invite_code (invite_code),
    CONSTRAINT fk_referral_rewards_inviter FOREIGN KEY (inviter_user_id) REFERENCES users(id) ON DELETE RESTRICT,
    CONSTRAINT fk_referral_rewards_invitee FOREIGN KEY (invitee_user_id) REFERENCES users(id) ON DELETE RESTRICT,
    CONSTRAINT fk_referral_rewards_invite FOREIGN KEY (invite_code) REFERENCES invites(code) ON DELETE RESTRICT,
    CONSTRAINT fk_referral_rewards_invite_owner FOREIGN KEY (invite_code, inviter_user_id) REFERENCES invites(code, referral_owner_user_id) ON DELETE RESTRICT,
    CONSTRAINT fk_referral_rewards_invite_use FOREIGN KEY (invite_code, invitee_user_id) REFERENCES invite_uses(invite_code, user_id) ON DELETE RESTRICT,
    CONSTRAINT fk_referral_rewards_source_transaction FOREIGN KEY (source_transaction_id) REFERENCES wallet_transactions(id) ON DELETE RESTRICT,
    CONSTRAINT fk_referral_rewards_transfer_transaction FOREIGN KEY (transfer_transaction_id) REFERENCES wallet_transactions(id) ON DELETE RESTRICT,
    CONSTRAINT chk_referral_rewards_users CHECK (inviter_user_id <> invitee_user_id),
    CONSTRAINT chk_referral_rewards_amount CHECK (source_amount > 0 AND reward_amount > 0),
    CONSTRAINT chk_referral_rewards_status CHECK (status IN ('available', 'transferred')),
    CONSTRAINT chk_referral_rewards_transfer_state CHECK (
        (status = 'available' AND transfer_transaction_id IS NULL AND transferred_at IS NULL)
        OR
        (status = 'transferred' AND transfer_transaction_id IS NOT NULL AND transferred_at IS NOT NULL)
    )
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;


CREATE TABLE api_keys (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    user_id BIGINT UNSIGNED NOT NULL,
    name VARCHAR(120) NOT NULL DEFAULT '',
    key_prefix VARCHAR(32) NOT NULL,
    key_plain VARCHAR(255) NOT NULL,
    enabled TINYINT(1) NOT NULL DEFAULT 1,
    deleted_at DATETIME NULL,
    rate_limit_per_minute INT NULL,
    concurrency_limit INT NOT NULL DEFAULT 5,
    quota_limit BIGINT UNSIGNED NULL,
    quota_used BIGINT UNSIGNED NOT NULL DEFAULT 0,
    active_requests INT NOT NULL DEFAULT 0,
    window_started_at DATETIME NULL,
    window_request_count INT NOT NULL DEFAULT 0,
    expire_at DATETIME NULL,
    last_used_at DATETIME NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    UNIQUE INDEX idx_api_keys_prefix (key_prefix),
    UNIQUE INDEX idx_api_keys_plain (key_plain),
    UNIQUE INDEX idx_api_keys_id_user (id, user_id),
    INDEX idx_api_keys_user_deleted_created (user_id, deleted_at, created_at, id),
    INDEX idx_api_keys_enabled_expire (enabled, expire_at),
    CONSTRAINT fk_api_keys_user FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE RESTRICT,
    CONSTRAINT chk_api_keys_prefix CHECK (key_prefix <> '' AND key_plain <> ''),
    CONSTRAINT chk_api_keys_limits CHECK (
        (rate_limit_per_minute IS NULL OR rate_limit_per_minute > 0)
        AND concurrency_limit > 0
        AND (quota_limit IS NULL OR quota_limit > 0)
        AND quota_used >= 0
        AND (quota_limit IS NULL OR quota_used <= quota_limit)
        AND active_requests >= 0
        AND active_requests <= concurrency_limit
        AND window_request_count >= 0
    )
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE orders (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    order_no VARCHAR(64) NOT NULL,
    user_id BIGINT UNSIGNED NOT NULL,
    project_id BIGINT UNSIGNED NOT NULL,
    project_product_id BIGINT UNSIGNED NOT NULL,
    product_type VARCHAR(32) NOT NULL COMMENT 'microsoft|domain',
    service_mode VARCHAR(32) NOT NULL COMMENT 'code|purchase',
    supply_policy VARCHAR(32) NOT NULL DEFAULT 'private_first' COMMENT 'private_first|public_only',
    status VARCHAR(32) NOT NULL DEFAULT 'pending_payment',
    pay_amount DECIMAL(18,2) NOT NULL,
    refund_amount DECIMAL(18,2) NOT NULL DEFAULT 0.00,
    debit_tx_id BIGINT UNSIGNED NULL,
    refund_tx_id BIGINT UNSIGNED NULL,
    allocation_type VARCHAR(32) NULL COMMENT 'microsoft|domain',
    microsoft_alloc_id BIGINT UNSIGNED NULL,
    domain_alloc_id BIGINT UNSIGNED NULL,
    delivery_email VARCHAR(255) NOT NULL DEFAULT '',
    receive_started_at DATETIME NULL,
    receive_until DATETIME NULL,
    activated_at DATETIME NULL,
    after_sale_until DATETIME NULL,
    client_channel VARCHAR(32) NOT NULL COMMENT 'console|api_key',
    api_key_id BIGINT UNSIGNED NULL,
    idempotency_key VARCHAR(128) NOT NULL,
    request_fingerprint CHAR(64) NOT NULL,
    idempotency_subject VARCHAR(80) GENERATED ALWAYS AS (
        CASE
            WHEN client_channel = 'api_key' THEN CONCAT('api_key:', COALESCE(api_key_id, 0))
            ELSE CONCAT('user:', user_id)
        END
    ) STORED,
    service_cleanup_status VARCHAR(32) NOT NULL DEFAULT 'none',
    archived_at DATETIME NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    version INT NOT NULL DEFAULT 1,
    UNIQUE INDEX idx_orders_order_no (order_no),
    UNIQUE INDEX idx_orders_idempotency (idempotency_subject, idempotency_key),
    INDEX idx_orders_user_created (user_id, created_at, id),
    INDEX idx_orders_status_created (status, created_at, id),
    INDEX idx_orders_project_created (project_id, created_at, id),
    INDEX idx_orders_delivery_status (delivery_email, status),
    INDEX idx_orders_api_key_created (api_key_id, created_at, id),
    INDEX idx_orders_api_key_user (api_key_id, user_id),
    CONSTRAINT fk_orders_user FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE RESTRICT,
    CONSTRAINT fk_orders_project FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE RESTRICT,
    CONSTRAINT fk_orders_product_project FOREIGN KEY (project_product_id, project_id) REFERENCES project_products(id, project_id) ON DELETE RESTRICT,
    CONSTRAINT fk_orders_debit_tx FOREIGN KEY (debit_tx_id) REFERENCES wallet_transactions(id) ON DELETE RESTRICT,
    CONSTRAINT fk_orders_refund_tx FOREIGN KEY (refund_tx_id) REFERENCES wallet_transactions(id) ON DELETE RESTRICT,
    CONSTRAINT fk_orders_ms_alloc FOREIGN KEY (microsoft_alloc_id) REFERENCES microsoft_allocations(id) ON DELETE RESTRICT,
    CONSTRAINT fk_orders_domain_alloc FOREIGN KEY (domain_alloc_id) REFERENCES domain_allocations(id) ON DELETE RESTRICT,
    CONSTRAINT fk_orders_api_key_user FOREIGN KEY (api_key_id, user_id) REFERENCES api_keys(id, user_id) ON DELETE RESTRICT,
    CONSTRAINT chk_orders_product_type CHECK (product_type IN ('microsoft', 'domain')),
    CONSTRAINT chk_orders_service_mode CHECK (service_mode IN ('code', 'purchase')),
    CONSTRAINT chk_orders_supply_policy CHECK (supply_policy IN ('private_first', 'public_only')),
    CONSTRAINT chk_orders_status CHECK (status IN ('pending_payment', 'paid', 'active', 'completed', 'refunded', 'failed', 'closed')),
    CONSTRAINT chk_orders_channel CHECK (
        (client_channel = 'console' AND api_key_id IS NULL)
        OR (client_channel = 'api_key' AND api_key_id IS NOT NULL)
    ),
    CONSTRAINT chk_orders_money CHECK (pay_amount >= 0 AND refund_amount >= 0),
    CONSTRAINT chk_orders_debit_state CHECK (status NOT IN ('paid', 'active', 'completed', 'refunded') OR debit_tx_id IS NOT NULL),
    CONSTRAINT chk_orders_refund_state CHECK (
        (status NOT IN ('refunded', 'failed') AND refund_tx_id IS NULL AND refund_amount = 0.00)
        OR (status = 'refunded' AND refund_tx_id IS NOT NULL AND refund_amount >= 0)
        OR (
            status = 'failed'
            AND (
                (debit_tx_id IS NULL AND refund_tx_id IS NULL AND refund_amount = 0.00)
                OR (debit_tx_id IS NOT NULL AND refund_tx_id IS NOT NULL AND refund_amount >= 0)
            )
        )
    ),
    CONSTRAINT chk_orders_allocation_shape CHECK (
        (allocation_type IS NULL AND microsoft_alloc_id IS NULL AND domain_alloc_id IS NULL)
        OR (allocation_type = 'microsoft' AND microsoft_alloc_id IS NOT NULL AND domain_alloc_id IS NULL)
        OR (allocation_type = 'domain' AND domain_alloc_id IS NOT NULL AND microsoft_alloc_id IS NULL)
    ),
    CONSTRAINT chk_orders_active_allocation CHECK (status NOT IN ('active', 'completed') OR allocation_type IS NOT NULL),
    CONSTRAINT chk_orders_delivery CHECK (status NOT IN ('active', 'completed') OR delivery_email <> ''),
    CONSTRAINT chk_orders_cleanup CHECK (service_cleanup_status IN ('none', 'succeeded', 'partial_failure')),
    CONSTRAINT chk_orders_idempotency CHECK (idempotency_key <> '' AND request_fingerprint <> '')
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE order_events (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    event_no VARCHAR(64) NOT NULL,
    order_no VARCHAR(64) NOT NULL,
    event_type VARCHAR(64) NOT NULL,
    from_status VARCHAR(32) NULL,
    to_status VARCHAR(32) NULL,
    operator_type VARCHAR(32) NOT NULL DEFAULT 'system',
    reason VARCHAR(500) NOT NULL DEFAULT '',
    event_context JSON NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE INDEX idx_order_events_event_no (event_no),
    INDEX idx_order_events_order_created (order_no, created_at, id),
    CONSTRAINT fk_order_events_order FOREIGN KEY (order_no) REFERENCES orders(order_no) ON DELETE CASCADE,
    CONSTRAINT chk_order_events_operator CHECK (operator_type IN ('user', 'admin', 'system', 'openapi')),
    CONSTRAINT chk_order_events_type CHECK (event_no <> '' AND event_type <> '')
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE order_tokens (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    token_prefix VARCHAR(32) NOT NULL,
    token_plain VARCHAR(255) NOT NULL,
    order_no VARCHAR(64) NOT NULL,
    enabled TINYINT(1) NOT NULL DEFAULT 1,
    expire_at DATETIME NULL,
    disabled_at DATETIME NULL,
    disabled_reason VARCHAR(500) NOT NULL DEFAULT '',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    UNIQUE INDEX idx_order_tokens_prefix (token_prefix),
    UNIQUE INDEX idx_order_tokens_plain (token_plain),
    UNIQUE INDEX idx_order_tokens_order (order_no),
    INDEX idx_order_tokens_enabled_expire (enabled, expire_at),
    CONSTRAINT fk_order_tokens_order FOREIGN KEY (order_no) REFERENCES orders(order_no) ON DELETE CASCADE,
    CONSTRAINT chk_order_tokens_prefix CHECK (token_prefix <> '' AND token_plain <> ''),
    CONSTRAINT chk_order_tokens_disabled CHECK (
        (enabled = 1 AND disabled_at IS NULL AND disabled_reason = '')
        OR (enabled = 0 AND disabled_at IS NOT NULL)
    )
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

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

INSERT INTO casbin_rule (ptype, v0, v1, v2, v3) VALUES
    ('p', 'role:admin', 'trade:order', 'read', 'allow'),
    ('p', 'role:admin', 'trade:order', 'operate', 'allow'),
    ('p', 'role:super_admin', 'trade:order', 'read', 'allow'),
    ('p', 'role:super_admin', 'trade:order', 'operate', 'allow');


CREATE TABLE mailmatch_messages (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    email_resource_id BIGINT UNSIGNED NOT NULL,
    resource_type VARCHAR(32) NOT NULL COMMENT 'microsoft|domain',
    recipient VARCHAR(255) NOT NULL,
    recipients_json JSON NULL,
    sender VARCHAR(255) NOT NULL DEFAULT '',
    subject VARCHAR(500) NOT NULL DEFAULT '',
    raw_body MEDIUMTEXT NULL,
    raw_source MEDIUMTEXT NULL,
    provider_payload JSON NULL,
    body_preview VARCHAR(1000) NOT NULL DEFAULT '',
    verification_code VARCHAR(64) NOT NULL DEFAULT '',
    message_id_header VARCHAR(500) NOT NULL DEFAULT '',
    provider_message_id VARCHAR(500) NOT NULL DEFAULT '',
    dedupe_key CHAR(64) NOT NULL,
    protocol VARCHAR(32) NOT NULL DEFAULT '',
    folder VARCHAR(64) NOT NULL DEFAULT '',
    status VARCHAR(32) NOT NULL DEFAULT 'received',
    match_diagnostic VARCHAR(500) NOT NULL DEFAULT '',
    received_at DATETIME NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    UNIQUE INDEX idx_mailmatch_messages_resource_dedupe (email_resource_id, dedupe_key),
    INDEX idx_mailmatch_messages_resource_type_time (email_resource_id, resource_type, received_at, id),
    INDEX idx_mailmatch_messages_scope_window (email_resource_id, recipient, received_at, id),
    INDEX idx_mailmatch_messages_recipient_status_time (recipient, status, received_at, id),
    INDEX idx_mailmatch_messages_status_time (status, received_at, id),
    CONSTRAINT fk_mailmatch_messages_resource FOREIGN KEY (email_resource_id, resource_type) REFERENCES email_resources(id, type) ON DELETE RESTRICT,
    CONSTRAINT chk_mailmatch_messages_resource_type CHECK (resource_type IN ('microsoft', 'domain')),
    CONSTRAINT chk_mailmatch_messages_status CHECK (status IN ('received', 'matched', 'ignored')),
    CONSTRAINT chk_mailmatch_messages_required CHECK (recipient <> '' AND dedupe_key <> '')
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE mailmatch_fetch_jobs (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    order_no VARCHAR(64) NOT NULL,
    purpose VARCHAR(32) NOT NULL DEFAULT 'order_fetch' COMMENT 'order_fetch|manual_fetch|auto_refresh|aftersale_check|inbound_consume',
    allocation_type VARCHAR(32) NOT NULL COMMENT 'microsoft|domain',
    allocation_id BIGINT UNSIGNED NOT NULL,
    project_id BIGINT UNSIGNED NOT NULL,
    email_resource_id BIGINT UNSIGNED NOT NULL,
    recipient VARCHAR(255) NOT NULL,
    status VARCHAR(32) NOT NULL DEFAULT 'pending',
    attempts INT NOT NULL DEFAULT 0,
    max_attempts INT NOT NULL DEFAULT 3,
    since_at DATETIME NULL,
    until_at DATETIME NULL,
    fetched_count INT NOT NULL DEFAULT 0,
    stored_count INT NOT NULL DEFAULT 0,
    matched_count INT NOT NULL DEFAULT 0,
    last_safe_error VARCHAR(500) NOT NULL DEFAULT '',
    request_id VARCHAR(64) NOT NULL DEFAULT '',
    started_at DATETIME NULL,
    finished_at DATETIME NULL,
    active_order_no VARCHAR(64) GENERATED ALWAYS AS (
        CASE WHEN status IN ('pending', 'queued', 'running') THEN order_no ELSE NULL END
    ) STORED,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    UNIQUE INDEX idx_mailmatch_fetch_jobs_active_order (active_order_no),
    INDEX idx_mailmatch_fetch_jobs_status_updated (status, updated_at, id),
    INDEX idx_mailmatch_fetch_jobs_order_created (order_no, created_at, id),
    INDEX idx_mailmatch_fetch_jobs_resource_created (email_resource_id, created_at, id),
    CONSTRAINT chk_mailmatch_fetch_jobs_purpose CHECK (purpose IN ('order_fetch', 'manual_fetch', 'auto_refresh', 'aftersale_check', 'inbound_consume')),
    CONSTRAINT chk_mailmatch_fetch_jobs_allocation_type CHECK (allocation_type IN ('microsoft', 'domain')),
    CONSTRAINT chk_mailmatch_fetch_jobs_status CHECK (status IN ('pending', 'queued', 'running', 'succeeded', 'failed', 'skipped')),
    CONSTRAINT chk_mailmatch_fetch_jobs_attempts CHECK (attempts >= 0 AND max_attempts > 0 AND attempts <= max_attempts),
    CONSTRAINT chk_mailmatch_fetch_jobs_counts CHECK (fetched_count >= 0 AND stored_count >= 0 AND matched_count >= 0),
    CONSTRAINT chk_mailmatch_fetch_jobs_required CHECK (order_no <> '' AND allocation_id > 0 AND recipient <> '')
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

ALTER TABLE mailmatch_fetch_jobs
    ADD CONSTRAINT fk_mailmatch_fetch_jobs_order FOREIGN KEY (order_no) REFERENCES orders(order_no) ON DELETE RESTRICT;

ALTER TABLE mailmatch_fetch_jobs
    ADD CONSTRAINT fk_mailmatch_fetch_jobs_project FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE RESTRICT;

ALTER TABLE mailmatch_fetch_jobs
    ADD CONSTRAINT fk_mailmatch_fetch_jobs_resource FOREIGN KEY (email_resource_id, allocation_type) REFERENCES email_resources(id, type) ON DELETE RESTRICT;

CREATE TABLE mailmatch_order_fetch_states (
    order_no VARCHAR(64) PRIMARY KEY,
    last_job_id BIGINT UNSIGNED NULL,
    last_status VARCHAR(32) NOT NULL DEFAULT '',
    last_submitted_at DATETIME NULL,
    last_success_at DATETIME NULL,
    last_received_at DATETIME NULL,
    cooldown_until DATETIME NULL,
    last_safe_error VARCHAR(500) NOT NULL DEFAULT '',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    INDEX idx_mailmatch_fetch_states_cooldown (cooldown_until),
    CONSTRAINT fk_mailmatch_fetch_states_order FOREIGN KEY (order_no) REFERENCES orders(order_no) ON DELETE CASCADE,
    CONSTRAINT fk_mailmatch_fetch_states_job FOREIGN KEY (last_job_id) REFERENCES mailmatch_fetch_jobs(id) ON DELETE SET NULL,
    CONSTRAINT chk_mailmatch_fetch_states_status CHECK (last_status IN ('', 'pending', 'queued', 'running', 'succeeded', 'failed', 'skipped', 'cooldown'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

INSERT INTO casbin_rule (ptype, v0, v1, v2, v3) VALUES
    ('p', 'role:admin', 'mailmatch:message', 'read', 'allow'),
    ('p', 'role:admin', 'mailmatch:message', 'operate', 'allow'),
    ('p', 'role:super_admin', 'mailmatch:message', 'read', 'allow'),
    ('p', 'role:super_admin', 'mailmatch:message', 'operate', 'allow');


CREATE INDEX idx_email_resources_owner_created_id
    ON email_resources(owner_user_id, created_at, id);

CREATE INDEX idx_email_resources_owner_type_created_id
    ON email_resources(owner_user_id, type, created_at, id);

CREATE INDEX idx_email_resources_type_created_id
    ON email_resources(type, created_at, id);

CREATE INDEX idx_email_resources_created_id
    ON email_resources(created_at, id);

DROP INDEX idx_email_resources_created ON email_resources;
DROP INDEX idx_email_resources_type_created ON email_resources;
DROP INDEX idx_email_resources_owner_type_created ON email_resources;
DROP INDEX idx_email_resources_owner_created ON email_resources;


CREATE TABLE mailmatch_order_delivery_heads (
    order_id BIGINT UNSIGNED NOT NULL PRIMARY KEY,
    message_id BIGINT UNSIGNED NOT NULL,
    message_received_at DATETIME(3) NOT NULL,
    INDEX idx_mailmatch_delivery_heads_message (message_id),
    CONSTRAINT fk_mailmatch_delivery_heads_order FOREIGN KEY (order_id) REFERENCES orders(id) ON DELETE CASCADE,
    CONSTRAINT fk_mailmatch_delivery_heads_message FOREIGN KEY (message_id) REFERENCES mailmatch_messages(id) ON DELETE RESTRICT,
    CONSTRAINT chk_mailmatch_delivery_heads_required CHECK (order_id > 0 AND message_id > 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;


ALTER TABLE orders
    ADD INDEX idx_orders_status_receive_until (status, receive_until),
    ADD INDEX idx_orders_status_after_sale (status, after_sale_until);


DROP TABLE IF EXISTS mailmatch_order_fetch_states;

ALTER TABLE mailmatch_fetch_jobs
    DROP INDEX idx_mailmatch_fetch_jobs_active_order,
    DROP COLUMN active_order_no,
    ADD COLUMN active_resource_id BIGINT UNSIGNED GENERATED ALWAYS AS (
        CASE WHEN status IN ('pending', 'queued', 'running') THEN email_resource_id ELSE NULL END
    ) STORED,
    ADD UNIQUE INDEX idx_mailmatch_fetch_jobs_active_resource (active_resource_id);

CREATE TABLE mailmatch_resource_fetch_states (
    email_resource_id BIGINT UNSIGNED PRIMARY KEY,
    last_job_id BIGINT UNSIGNED NULL,
    last_status VARCHAR(32) NOT NULL DEFAULT '',
    last_submitted_at DATETIME NULL,
    last_success_at DATETIME NULL,
    last_received_at DATETIME NULL,
    cooldown_until DATETIME NULL,
    last_safe_error VARCHAR(500) NOT NULL DEFAULT '',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    INDEX idx_mailmatch_resource_fetch_states_cooldown (cooldown_until),
    CONSTRAINT fk_mm_resource_fetch_states_resource FOREIGN KEY (email_resource_id) REFERENCES email_resources(id) ON DELETE CASCADE,
    CONSTRAINT fk_mm_resource_fetch_states_job FOREIGN KEY (last_job_id) REFERENCES mailmatch_fetch_jobs(id) ON DELETE SET NULL,
    CONSTRAINT chk_mm_resource_fetch_states_status CHECK (last_status IN ('', 'pending', 'queued', 'running', 'succeeded', 'failed', 'skipped', 'cooldown'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;


ALTER TABLE mailmatch_messages
    DROP COLUMN raw_source,
    DROP COLUMN provider_payload;


ALTER TABLE wallets
    ADD COLUMN total_spend DECIMAL(18,2) NOT NULL DEFAULT 0.00 AFTER supplier_frozen,
    ADD COLUMN spend_count BIGINT UNSIGNED NOT NULL DEFAULT 0 AFTER total_spend;

UPDATE wallets w
LEFT JOIN (
    SELECT user_id, COALESCE(SUM(-amount), 0) AS total_spend, COUNT(*) AS spend_count
    FROM wallet_transactions
    WHERE balance_bucket = 'consumer' AND direction = 'out'
    GROUP BY user_id
) t ON t.user_id = w.user_id
SET w.total_spend = COALESCE(t.total_spend, 0),
    w.spend_count = COALESCE(t.spend_count, 0);


ALTER TABLE orders
    ADD COLUMN failure_code VARCHAR(32) NOT NULL DEFAULT '' AFTER status;

UPDATE orders
SET failure_code = 'unknown'
WHERE status = 'failed';

ALTER TABLE orders
    ADD CONSTRAINT chk_orders_failure_code CHECK (
        (status = 'failed' AND failure_code IN (
            'unknown',
            'insufficient_inventory',
            'insufficient_balance',
            'allocation_failed',
            'service_token_failed',
            'activation_failed'
        ))
        OR (status <> 'failed' AND failure_code = '')
    );


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


ALTER TABLE mailmatch_messages
    ADD INDEX idx_mailmatch_messages_retention (resource_type, received_at, id);

ALTER TABLE system_logs
    ADD INDEX idx_system_logs_created (created_at, id);

DROP TABLE api_logs;


DROP TABLE IF EXISTS allocation_candidate_refresh_jobs;
DROP TABLE IF EXISTS domain_routing_candidates;
DROP TABLE IF EXISTS microsoft_routing_candidates;


ALTER TABLE microsoft_allocations
    DROP INDEX idx_ms_alloc_active_main,
    DROP INDEX idx_ms_alloc_active_alias,
    DROP INDEX idx_ms_alloc_active_dot,
    DROP INDEX idx_ms_alloc_active_plus,
    DROP COLUMN active_main_resource_id,
    DROP COLUMN active_explicit_alias_id,
    DROP COLUMN active_dot_project_id,
    DROP COLUMN active_dot_alias_id,
    DROP COLUMN active_plus_project_id,
    DROP COLUMN active_plus_alias_id,
    ADD COLUMN active_kind TINYINT UNSIGNED GENERATED ALWAYS AS (
        CASE
            WHEN status <> 'allocated' THEN NULL
            WHEN mailbox = 'main' THEN 1
            WHEN mailbox = 'alias' THEN 2
            WHEN mailbox = 'dot' THEN 3
            WHEN mailbox = 'plus' THEN 4
            ELSE NULL
        END
    ) STORED,
    ADD COLUMN active_project_id BIGINT UNSIGNED GENERATED ALWAYS AS (
        CASE
            WHEN status <> 'allocated' THEN NULL
            WHEN mailbox IN ('dot', 'plus') THEN project_id
            ELSE 0
        END
    ) STORED,
    ADD COLUMN active_entity_id BIGINT UNSIGNED GENERATED ALWAYS AS (
        CASE
            WHEN status <> 'allocated' THEN NULL
            WHEN mailbox = 'main' THEN resource_id
            WHEN mailbox = 'alias' THEN explicit_alias_id
            WHEN mailbox = 'dot' THEN dot_alias_id
            WHEN mailbox = 'plus' THEN plus_alias_id
            ELSE NULL
        END
    ) STORED,
    ADD UNIQUE INDEX idx_ms_alloc_active (
        active_kind,
        active_project_id,
        active_entity_id
    );


CREATE TABLE microsoft_resource_project_matches (
    resource_id BIGINT UNSIGNED NOT NULL,
    project_id BIGINT UNSIGNED NOT NULL,
    first_matched_at DATETIME(3) NOT NULL,
    last_matched_at DATETIME(3) NOT NULL,
    evidence_count INT UNSIGNED NOT NULL DEFAULT 1,
    last_scanned_at DATETIME(3) NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    PRIMARY KEY (resource_id, project_id),
    INDEX idx_ms_resource_project_matches_project (project_id, resource_id),
    INDEX idx_ms_resource_project_matches_scan (resource_id, last_scanned_at),
    CONSTRAINT fk_ms_resource_project_matches_resource
        FOREIGN KEY (resource_id) REFERENCES microsoft_resources(id) ON DELETE CASCADE,
    CONSTRAINT fk_ms_resource_project_matches_project
        FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE,
    CONSTRAINT chk_ms_resource_project_matches_count CHECK (evidence_count > 0),
    CONSTRAINT chk_ms_resource_project_matches_time CHECK (first_matched_at <= last_matched_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;


ALTER TABLE api_keys
    DROP CHECK chk_api_keys_limits,
    DROP COLUMN active_requests,
    DROP COLUMN window_started_at,
    DROP COLUMN window_request_count;

ALTER TABLE api_keys
    ADD CONSTRAINT chk_api_keys_limits CHECK (
        (rate_limit_per_minute IS NULL OR rate_limit_per_minute > 0)
        AND concurrency_limit > 0
        AND (quota_limit IS NULL OR quota_limit > 0)
        AND quota_used >= 0
        AND (quota_limit IS NULL OR quota_used <= quota_limit)
    );

-- +goose Down

ALTER TABLE api_keys
    DROP CHECK chk_api_keys_limits,
    ADD COLUMN active_requests INT NOT NULL DEFAULT 0 AFTER quota_used,
    ADD COLUMN window_started_at DATETIME NULL AFTER active_requests,
    ADD COLUMN window_request_count INT NOT NULL DEFAULT 0 AFTER window_started_at;

ALTER TABLE api_keys
    ADD CONSTRAINT chk_api_keys_limits CHECK (
        (rate_limit_per_minute IS NULL OR rate_limit_per_minute > 0)
        AND concurrency_limit > 0
        AND (quota_limit IS NULL OR quota_limit > 0)
        AND quota_used >= 0
        AND (quota_limit IS NULL OR quota_used <= quota_limit)
        AND active_requests >= 0
        AND active_requests <= concurrency_limit
        AND window_request_count >= 0
    );

DROP TABLE IF EXISTS microsoft_resource_project_matches;

ALTER TABLE microsoft_allocations
    DROP INDEX idx_ms_alloc_active,
    DROP COLUMN active_kind,
    DROP COLUMN active_project_id,
    DROP COLUMN active_entity_id,
    ADD COLUMN active_main_resource_id BIGINT UNSIGNED GENERATED ALWAYS AS (
        CASE WHEN status = 'allocated' AND mailbox = 'main' THEN resource_id ELSE NULL END
    ) STORED,
    ADD COLUMN active_explicit_alias_id BIGINT UNSIGNED GENERATED ALWAYS AS (
        CASE WHEN status = 'allocated' AND mailbox = 'alias' THEN explicit_alias_id ELSE NULL END
    ) STORED,
    ADD COLUMN active_dot_project_id BIGINT UNSIGNED GENERATED ALWAYS AS (
        CASE WHEN status = 'allocated' AND mailbox = 'dot' THEN project_id ELSE NULL END
    ) STORED,
    ADD COLUMN active_dot_alias_id BIGINT UNSIGNED GENERATED ALWAYS AS (
        CASE WHEN status = 'allocated' AND mailbox = 'dot' THEN dot_alias_id ELSE NULL END
    ) STORED,
    ADD COLUMN active_plus_project_id BIGINT UNSIGNED GENERATED ALWAYS AS (
        CASE WHEN status = 'allocated' AND mailbox = 'plus' THEN project_id ELSE NULL END
    ) STORED,
    ADD COLUMN active_plus_alias_id BIGINT UNSIGNED GENERATED ALWAYS AS (
        CASE WHEN status = 'allocated' AND mailbox = 'plus' THEN plus_alias_id ELSE NULL END
    ) STORED,
    ADD UNIQUE INDEX idx_ms_alloc_active_main (active_main_resource_id),
    ADD UNIQUE INDEX idx_ms_alloc_active_alias (active_explicit_alias_id),
    ADD UNIQUE INDEX idx_ms_alloc_active_dot (active_dot_project_id, active_dot_alias_id),
    ADD UNIQUE INDEX idx_ms_alloc_active_plus (active_plus_project_id, active_plus_alias_id);

-- Intentionally irreversible. Allocation reads directly from Core source tables;
-- rebuilding project-by-resource mirrors would recreate the removed write amplification.

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

ALTER TABLE orders
    DROP CHECK chk_orders_failure_code,
    DROP COLUMN failure_code;

ALTER TABLE wallets
    DROP COLUMN spend_count,
    DROP COLUMN total_spend;

ALTER TABLE mailmatch_messages
    ADD COLUMN raw_source MEDIUMTEXT NULL AFTER raw_body,
    ADD COLUMN provider_payload JSON NULL AFTER raw_source;

DROP TABLE IF EXISTS mailmatch_resource_fetch_states;

ALTER TABLE mailmatch_fetch_jobs
    DROP INDEX idx_mailmatch_fetch_jobs_active_resource,
    DROP COLUMN active_resource_id,
    ADD COLUMN active_order_no VARCHAR(64) GENERATED ALWAYS AS (
        CASE WHEN status IN ('pending', 'queued', 'running') THEN order_no ELSE NULL END
    ) STORED,
    ADD UNIQUE INDEX idx_mailmatch_fetch_jobs_active_order (active_order_no);

CREATE TABLE mailmatch_order_fetch_states (
    order_no VARCHAR(64) PRIMARY KEY,
    last_job_id BIGINT UNSIGNED NULL,
    last_status VARCHAR(32) NOT NULL DEFAULT '',
    last_submitted_at DATETIME NULL,
    last_success_at DATETIME NULL,
    last_received_at DATETIME NULL,
    cooldown_until DATETIME NULL,
    last_safe_error VARCHAR(500) NOT NULL DEFAULT '',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    INDEX idx_mailmatch_fetch_states_cooldown (cooldown_until),
    CONSTRAINT fk_mailmatch_fetch_states_order FOREIGN KEY (order_no) REFERENCES orders(order_no) ON DELETE CASCADE,
    CONSTRAINT fk_mailmatch_fetch_states_job FOREIGN KEY (last_job_id) REFERENCES mailmatch_fetch_jobs(id) ON DELETE SET NULL,
    CONSTRAINT chk_mailmatch_fetch_states_status CHECK (last_status IN ('', 'pending', 'queued', 'running', 'succeeded', 'failed', 'skipped', 'cooldown'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

ALTER TABLE orders
    DROP INDEX idx_orders_status_after_sale,
    DROP INDEX idx_orders_status_receive_until;

DROP TABLE IF EXISTS mailmatch_order_delivery_heads;

CREATE INDEX idx_email_resources_owner_created
    ON email_resources(owner_user_id, created_at);

CREATE INDEX idx_email_resources_owner_type_created
    ON email_resources(owner_user_id, type, created_at);

CREATE INDEX idx_email_resources_type_created
    ON email_resources(type, created_at);

CREATE INDEX idx_email_resources_created
    ON email_resources(created_at);

DROP INDEX idx_email_resources_created_id ON email_resources;
DROP INDEX idx_email_resources_type_created_id ON email_resources;
DROP INDEX idx_email_resources_owner_type_created_id ON email_resources;
DROP INDEX idx_email_resources_owner_created_id ON email_resources;

DELETE FROM casbin_rule WHERE v1 = 'mailmatch:message';
DROP TABLE IF EXISTS mailmatch_order_fetch_states;
DROP TABLE IF EXISTS mailmatch_fetch_jobs;
DROP TABLE IF EXISTS mailmatch_messages;

DELETE FROM casbin_rule WHERE v1 = 'trade:order';
DROP TABLE IF EXISTS api_logs;
DROP TABLE IF EXISTS order_tokens;
DROP TABLE IF EXISTS order_events;
DROP TABLE IF EXISTS orders;
DROP TABLE IF EXISTS api_keys;

DROP TABLE IF EXISTS referral_rewards;

ALTER TABLE invites
    DROP FOREIGN KEY fk_invites_referral_owner,
    DROP CHECK chk_invites_referral_owner,
    DROP CHECK chk_invites_kind;

DROP INDEX idx_invites_referral_owner ON invites;
DROP INDEX idx_invites_code_referral_owner ON invites;
DROP INDEX idx_invites_kind_created ON invites;

ALTER TABLE invites
    DROP COLUMN referral_owner_user_id,
    DROP COLUMN invite_kind;
DELETE FROM casbin_rule
WHERE ptype = 'p'
  AND v1 IN ('billing:wallet', 'billing:card');

DROP TABLE IF EXISTS card_key_redemptions;
DROP TABLE IF EXISTS card_keys;
DROP TABLE IF EXISTS recharges;
DROP TABLE IF EXISTS idempotency_keys;
DROP TABLE IF EXISTS wallet_transactions;
DROP TABLE IF EXISTS wallets;
DELETE FROM casbin_rule WHERE v1 = 'alloc:allocation';
DROP TABLE IF EXISTS allocation_candidate_refresh_jobs;
DROP TABLE IF EXISTS domain_allocations;
DROP TABLE IF EXISTS microsoft_allocations;
DROP TABLE IF EXISTS allocation_daily_usages;
DROP TABLE IF EXISTS allocation_order_guards;
DROP TABLE IF EXISTS domain_routing_candidates;
DROP TABLE IF EXISTS microsoft_routing_candidates;
ALTER TABLE generated_mailboxes
    DROP INDEX idx_generated_mailboxes_id_resource,
    DROP INDEX idx_generated_mailboxes_alloc_reuse;
ALTER TABLE plus_aliases
    DROP INDEX idx_plus_aliases_id_resource,
    DROP INDEX idx_plus_aliases_alloc_reuse;
ALTER TABLE dot_aliases
    DROP INDEX idx_dot_aliases_id_resource,
    DROP INDEX idx_dot_aliases_alloc_reuse;
ALTER TABLE explicit_aliases
    DROP INDEX idx_explicit_aliases_id_resource,
    DROP INDEX idx_explicit_aliases_alloc_reuse;
ALTER TABLE project_products
    DROP INDEX idx_project_products_id_project;
ALTER TABLE domain_resources
    DROP INDEX idx_domain_inventory_public,
    DROP INDEX idx_domain_alloc_public,
    DROP COLUMN mailbox_daily_limit,
    DROP COLUMN alloc_bucket;
ALTER TABLE microsoft_resources
    DROP INDEX idx_microsoft_inventory_public,
    DROP INDEX idx_microsoft_alloc_owned,
    DROP INDEX idx_microsoft_alloc_public,
    DROP COLUMN plus_daily_limit,
    DROP COLUMN alloc_bucket;

ALTER TABLE users
    DROP INDEX idx_users_search;
DELETE FROM casbin_rule WHERE v1 = 'core:project';
DROP TABLE IF EXISTS project_accesses;
DROP TABLE IF EXISTS project_mail_rules;
DROP TABLE IF EXISTS project_products;
DROP TABLE IF EXISTS projects;
DROP TABLE IF EXISTS resource_validation_jobs;
ALTER TABLE domain_resources
    DROP COLUMN last_safe_error;
ALTER TABLE generated_mailboxes
    DROP INDEX idx_generated_mailboxes_email_status;

DROP TABLE IF EXISTS inbound_mails;
DROP TABLE IF EXISTS microsoft_binding_mailboxes;
DROP TABLE IF EXISTS outbound_mails;
DELETE FROM casbin_rule
WHERE ptype = 'p'
  AND v0 IN ('role:admin', 'role:super_admin')
  AND v1 = 'proxy:proxy';

DROP TABLE IF EXISTS proxy_check_job_items;
DROP TABLE IF EXISTS proxy_bindings;
DROP TABLE IF EXISTS proxy_check_jobs;
DROP TABLE IF EXISTS proxies;
DROP TABLE IF EXISTS system_logs;
DROP TABLE IF EXISTS generated_mailboxes;
DROP TABLE IF EXISTS domain_resources;
DROP TABLE IF EXISTS dot_aliases;
DROP TABLE IF EXISTS plus_aliases;
DROP TABLE IF EXISTS explicit_aliases;
DROP TABLE IF EXISTS microsoft_resources;
DROP TABLE IF EXISTS resource_imports;
DROP TABLE IF EXISTS mail_servers;
DROP TABLE IF EXISTS email_resources;
DROP TABLE IF EXISTS operation_logs;
DROP TABLE IF EXISTS supplier_applications;
DROP TABLE IF EXISTS casbin_rule;
DROP TABLE IF EXISTS user_login_devices;
DROP TABLE IF EXISTS third_party_identities;
DROP TABLE IF EXISTS invite_uses;
DROP TABLE IF EXISTS invites;
DROP TABLE IF EXISTS system_guard;
DROP TABLE IF EXISTS users;
DROP TABLE IF EXISTS user_groups;
SELECT 'P1-I0 skeleton migration rolled back';
