-- +goose Up

CREATE TABLE proxy_servers (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    server_ip VARCHAR(255) NOT NULL COMMENT 'canonical proxy server IP; legacy hostnames remain supported',
    name VARCHAR(255) NOT NULL DEFAULT '',
    source_type VARCHAR(16) NOT NULL DEFAULT 'vendor' COMMENT 'self_hosted|vendor',
    capacity_weight INT UNSIGNED NOT NULL DEFAULT 1,
    admin_status VARCHAR(16) NOT NULL DEFAULT 'online' COMMENT 'online|draining|offline',
    health_status VARCHAR(16) NOT NULL DEFAULT 'healthy' COMMENT 'healthy|unhealthy',
    health_failures INT UNSIGNED NOT NULL DEFAULT 0,
    health_generation BIGINT UNSIGNED NOT NULL DEFAULT 1,
    last_health_error VARCHAR(500) NOT NULL DEFAULT '',
    last_health_checked_at DATETIME NULL,
    next_health_check_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    inventory_status VARCHAR(16) NOT NULL DEFAULT 'healthy' COMMENT 'healthy|degraded',
    last_failover_logged_at DATETIME NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    UNIQUE INDEX idx_proxy_servers_ip (server_ip),
    INDEX idx_proxy_servers_select (admin_status, health_status, id),
    INDEX idx_proxy_servers_health_due (admin_status, next_health_check_at, id),
    CONSTRAINT chk_proxy_servers_ip CHECK (server_ip <> ''),
    CONSTRAINT chk_proxy_servers_source CHECK (source_type IN ('self_hosted', 'vendor')),
    CONSTRAINT chk_proxy_servers_weight CHECK (capacity_weight > 0),
    CONSTRAINT chk_proxy_servers_admin CHECK (admin_status IN ('online', 'draining', 'offline')),
    CONSTRAINT chk_proxy_servers_health CHECK (health_status IN ('healthy', 'unhealthy')),
    CONSTRAINT chk_proxy_servers_generation CHECK (health_generation > 0),
    CONSTRAINT chk_proxy_servers_inventory CHECK (inventory_status IN ('healthy', 'degraded'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

ALTER TABLE proxies
    ADD COLUMN proxy_server_id BIGINT UNSIGNED NULL AFTER id,
    ADD COLUMN last_assigned_at DATETIME NULL AFTER last_used_at,
    ADD COLUMN latency_sort_ms INT GENERATED ALWAYS AS (
        CASE WHEN latency_ms > 0 THEN latency_ms ELSE 2147483647 END
    ) STORED AFTER latency_ms;

-- Existing url_host values were produced with url.URL.Hostname(). Canonicalize
-- literal IPv4/IPv6 spellings while retaining legacy DNS hosts for compatibility.
INSERT INTO proxy_servers(server_ip, name, source_type)
SELECT server_key, server_key, 'vendor'
FROM (
    SELECT CASE
        WHEN TRIM(url_host) = '' THEN CONCAT('legacy-proxy-', id)
        WHEN INET6_ATON(TRIM(url_host)) IS NOT NULL
            THEN LOWER(INET6_NTOA(INET6_ATON(TRIM(url_host))))
        ELSE LOWER(TRIM(url_host))
    END AS server_key
    FROM proxies
) AS existing_servers
GROUP BY server_key;

UPDATE proxies AS p
JOIN proxy_servers AS s
  ON s.server_ip = CASE
      WHEN TRIM(p.url_host) = '' THEN CONCAT('legacy-proxy-', p.id)
      WHEN INET6_ATON(TRIM(p.url_host)) IS NOT NULL
          THEN LOWER(INET6_NTOA(INET6_ATON(TRIM(p.url_host))))
      ELSE LOWER(TRIM(p.url_host))
  END
SET p.proxy_server_id = s.id;

-- Preserve historical Resource allocations without breaking their sticky
-- bindings. New allocations can then fairly prefer genuinely unused routes.
UPDATE proxies AS p
JOIN (
    SELECT
        proxy_id,
        MAX(GREATEST(created_at, COALESCE(last_used_at, created_at))) AS assigned_at
    FROM proxy_bindings
    GROUP BY proxy_id
) AS historical_bindings ON historical_bindings.proxy_id = p.id
SET p.last_assigned_at = historical_bindings.assigned_at;

ALTER TABLE proxies
    MODIFY COLUMN proxy_server_id BIGINT UNSIGNED NOT NULL,
    ADD INDEX idx_proxies_resource_server_select
        (proxy_server_id, pool, status, ip_version, errors, last_assigned_at, latency_sort_ms, id, expire_at),
    ADD INDEX idx_proxies_system_server_select
        (proxy_server_id, pool, status, ip_version, errors, last_used_at, latency_sort_ms, id, expire_at),
    ADD CONSTRAINT fk_proxies_proxy_server
        FOREIGN KEY (proxy_server_id) REFERENCES proxy_servers(id) ON DELETE RESTRICT;

-- +goose Down

ALTER TABLE proxies
    DROP FOREIGN KEY fk_proxies_proxy_server,
    DROP INDEX idx_proxies_resource_server_select,
    DROP INDEX idx_proxies_system_server_select,
    DROP COLUMN latency_sort_ms,
    DROP COLUMN last_assigned_at,
    DROP COLUMN proxy_server_id;

DROP TABLE proxy_servers;
