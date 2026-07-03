-- +goose Up

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
    url VARCHAR(1024) NOT NULL COMMENT 'original proxy URL, never expose in normal lists or logs',
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

-- +goose Down
DELETE FROM casbin_rule
WHERE ptype = 'p'
  AND v0 IN ('role:admin', 'role:super_admin')
  AND v1 = 'proxy:proxy';

DROP TABLE IF EXISTS proxy_check_job_items;
DROP TABLE IF EXISTS proxy_bindings;
DROP TABLE IF EXISTS proxy_check_jobs;
DROP TABLE IF EXISTS proxies;
DROP TABLE IF EXISTS system_logs;
