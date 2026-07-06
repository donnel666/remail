-- +goose Up

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
    ADD INDEX idx_domain_alloc_public (alloc_bucket, purpose, status, last_allocated_at, id);

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
    CONSTRAINT chk_domain_candidates_purpose CHECK (purpose = 'sale'),
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

-- +goose Down
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
