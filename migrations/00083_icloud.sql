-- +goose Up

-- iCloud uses the existing project product windows. The one-month value is
-- stored on icloud_resources.expire_at and is not a product service period.
ALTER TABLE project_products
    DROP CHECK chk_project_products_type,
    DROP CHECK chk_project_products_weights,
    ADD CONSTRAINT chk_project_products_type CHECK (
        type IN ('microsoft', 'domain', 'random', 'gmail', 'icloud')
    ),
    ADD CONSTRAINT chk_project_products_weights CHECK (
        main_weight >= 0
        AND dot_weight >= 0
        AND plus_weight >= 0
        AND (type NOT IN ('microsoft', 'gmail', 'icloud')
             OR main_weight + dot_weight + plus_weight > 0)
        AND (type <> 'domain'
             OR (main_weight = 0 AND dot_weight = 0 AND plus_weight = 0))
        AND (type <> 'random'
             OR (main_weight = 1 AND dot_weight = 1 AND plus_weight = 1))
        AND (type <> 'icloud'
             OR (main_weight > 0 AND dot_weight = 0 AND plus_weight = 0))
    );

ALTER TABLE email_resources
    DROP CHECK chk_email_resources_type,
    ADD CONSTRAINT chk_email_resources_type CHECK (
        type IN ('microsoft', 'domain', 'gmail', 'icloud')
    ) NOT ENFORCED,
    ALGORITHM=INSTANT;

ALTER TABLE allocation_order_guards
    DROP CHECK chk_allocation_order_guards_type,
    ADD CONSTRAINT chk_allocation_order_guards_type CHECK (
        type IN ('microsoft', 'domain', 'gmail', 'icloud')
    ) NOT ENFORCED,
    ALGORITHM=INSTANT;

-- MySQL DDL autocommits. IF NOT EXISTS lets Goose resume 00083 after an
-- earlier deployment created these tables but failed before recording v83.
CREATE TABLE IF NOT EXISTS icloud_resources (
    id BIGINT UNSIGNED PRIMARY KEY,
    resource_type VARCHAR(32) NOT NULL DEFAULT 'icloud'
        COMMENT 'mirrors email_resources.type',

    -- Canonical import fields, in order:
    -- primary_email, host, dsid, client_id, client_build_number,
    -- client_mastering_number, cookie, Gmail -> gmail_resource_id.
    primary_email VARCHAR(320) NOT NULL,
    host VARCHAR(255) NOT NULL,
    dsid VARCHAR(191) NOT NULL,
    client_id VARCHAR(191) NOT NULL,
    client_build_number VARCHAR(64) NOT NULL,
    client_mastering_number VARCHAR(64) NOT NULL,
    cookie TEXT NOT NULL
        COMMENT 'plaintext session cookie; never expose through API or logs',
    gmail_resource_id BIGINT UNSIGNED NOT NULL
        COMMENT 'resolved from the imported local Gmail email',

    -- Optional/canonicalized request context. It is derived from host or a
    -- service default when the import does not come from a full cURL/HAR.
    lang_code VARCHAR(16) NOT NULL DEFAULT 'zh-tw',
    origin VARCHAR(255) NOT NULL DEFAULT '',
    referer VARCHAR(255) NOT NULL DEFAULT '',
    user_agent VARCHAR(500) NOT NULL DEFAULT '',
    selected_forward_to VARCHAR(320) NOT NULL DEFAULT '',

    -- Main-account lifetime; independent from product service windows and
    -- from the observed validity of the Apple session cookie.
    expire_at DATETIME(3) NOT NULL,

    for_sale TINYINT(1) NOT NULL DEFAULT 0,
    status VARCHAR(24) NOT NULL DEFAULT 'pending'
        COMMENT 'pending|validating|normal|abnormal|disabled|deleted',
    session_status VARCHAR(24) NOT NULL DEFAULT 'unchecked'
        COMMENT 'unchecked|valid|invalid',
    session_failures TINYINT UNSIGNED NOT NULL DEFAULT 0,

    credential_revision BIGINT UNSIGNED NOT NULL DEFAULT 1,
    credential_updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    validation_generation BIGINT UNSIGNED NOT NULL DEFAULT 1,
    validation_failures TINYINT UNSIGNED NOT NULL DEFAULT 0,

    -- The last complete Apple HME snapshot and the durable, single-alias
    -- provisioning/probe state. These values never contain a session Cookie.
    alias_count INT UNSIGNED NOT NULL DEFAULT 0,
    alias_provision_candidate VARCHAR(320) NOT NULL DEFAULT '',
    alias_provision_reconcile TINYINT(1) NOT NULL DEFAULT 0,
    next_validation_at DATETIME(3) NULL,
    delivery_probe_token VARCHAR(128) NOT NULL DEFAULT '',
    delivery_probe_alias VARCHAR(320) NOT NULL DEFAULT '',
    delivery_probe_started_at DATETIME(3) NULL,
    delivery_probe_verified_at DATETIME(3) NULL,

    next_keepalive_at DATETIME(3) NULL,
    last_checked_at DATETIME(3) NULL,
    last_valid_at DATETIME(3) NULL,
    last_alias_sync_at DATETIME(3) NULL,
    last_allocated_at DATETIME(3) NULL,
    last_safe_error VARCHAR(500) NOT NULL DEFAULT '',

    created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3)
        ON UPDATE CURRENT_TIMESTAMP(3),

    UNIQUE INDEX uk_icloud_resources_primary_email (primary_email),
    UNIQUE INDEX uk_icloud_resources_dsid (dsid),
    INDEX idx_icloud_resources_inventory
        (for_sale, status, session_status, expire_at, last_allocated_at, id),
    INDEX idx_icloud_resources_keepalive
        (session_status, next_keepalive_at, id),
    INDEX idx_icloud_resources_validation
        (status, next_validation_at, id),
    INDEX idx_icloud_resources_gmail
        (gmail_resource_id, status, id),

    CONSTRAINT fk_icloud_resources_root
        FOREIGN KEY (id, resource_type)
        REFERENCES email_resources(id, type) ON DELETE CASCADE,
    CONSTRAINT fk_icloud_resources_gmail
        FOREIGN KEY (gmail_resource_id)
        REFERENCES gmail_resources(id) ON DELETE RESTRICT,
    CONSTRAINT chk_icloud_resources_type
        CHECK (resource_type = 'icloud'),
    CONSTRAINT chk_icloud_resources_status
        CHECK (status IN ('pending', 'validating', 'normal', 'abnormal', 'disabled', 'deleted')),
    CONSTRAINT chk_icloud_resources_session
        CHECK (session_status IN ('unchecked', 'valid', 'invalid')),
    CONSTRAINT chk_icloud_resources_required
        CHECK (
            primary_email <> ''
            AND dsid <> ''
            AND host <> ''
            AND client_id <> ''
            AND client_build_number <> ''
            AND client_mastering_number <> ''
        ),
    CONSTRAINT chk_icloud_resources_counters
        CHECK (credential_revision > 0 AND validation_generation > 0
               AND validation_failures <= 3),
    CONSTRAINT chk_icloud_resources_provision
        CHECK (alias_provision_candidate <> '' OR alias_provision_reconcile = FALSE),
    CONSTRAINT chk_icloud_resources_probe
        CHECK (
            (delivery_probe_token = '' AND delivery_probe_alias = ''
             AND delivery_probe_started_at IS NULL AND delivery_probe_verified_at IS NULL)
            OR (delivery_probe_token <> '' AND delivery_probe_alias <> ''
                AND delivery_probe_started_at IS NOT NULL
                AND (delivery_probe_verified_at IS NULL
                     OR delivery_probe_verified_at >= delivery_probe_started_at))
        )
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS icloud_aliases (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    resource_id BIGINT UNSIGNED NOT NULL,
    anonymous_id VARCHAR(191) NOT NULL,
    email VARCHAR(320) NOT NULL,
    label VARCHAR(500) NOT NULL DEFAULT '',
    note VARCHAR(2000) NOT NULL DEFAULT '',
    forward_to_email VARCHAR(320) NOT NULL DEFAULT '',
    origin VARCHAR(64) NOT NULL DEFAULT '',
    provider_domain VARCHAR(255) NOT NULL DEFAULT '',
    recipient_mail_id VARCHAR(191) NOT NULL DEFAULT '',
    status VARCHAR(24) NOT NULL DEFAULT 'normal'
        COMMENT 'normal|disabled|missing|deleted',
    provider_created_at DATETIME(3) NULL,
    last_seen_at DATETIME(3) NULL,
    last_allocated_at DATETIME(3) NULL,
    created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3)
        ON UPDATE CURRENT_TIMESTAMP(3),

    UNIQUE INDEX uk_icloud_aliases_email (email),
    UNIQUE INDEX uk_icloud_aliases_resource_anonymous
        (resource_id, anonymous_id),
    UNIQUE INDEX uk_icloud_aliases_id_resource (id, resource_id),
    INDEX idx_icloud_aliases_inventory
        (resource_id, status, last_allocated_at, id),
    INDEX idx_icloud_aliases_forward
        (forward_to_email, status, id),

    CONSTRAINT fk_icloud_aliases_resource
        FOREIGN KEY (resource_id)
        REFERENCES icloud_resources(id) ON DELETE CASCADE,
    CONSTRAINT chk_icloud_aliases_status
        CHECK (status IN ('normal', 'disabled', 'missing', 'deleted')),
    CONSTRAINT chk_icloud_aliases_required
        CHECK (anonymous_id <> '' AND email <> '')
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- iCloud imports deliberately do not reuse resource_imports: that table and
-- its worker are the stable Microsoft import contract.  The source artifact
-- contains an Apple session Cookie, so it stays in private object storage and
-- only this durable, fenced row is sent through Asynq.
CREATE TABLE IF NOT EXISTS icloud_resource_imports (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    owner_user_id BIGINT UNSIGNED NOT NULL,
    operator_user_id BIGINT UNSIGNED NOT NULL,
    source_object_key VARCHAR(500) NOT NULL,
    failure_object_key VARCHAR(500) NOT NULL DEFAULT '',
    status VARCHAR(32) NOT NULL DEFAULT 'processing'
        COMMENT 'processing|imported|failed',
    error_strategy VARCHAR(16) NOT NULL DEFAULT 'skip'
        COMMENT 'skip|abort',
    imported_count INT UNSIGNED NOT NULL DEFAULT 0,
    accepted_count INT UNSIGNED NOT NULL DEFAULT 0,
    skipped_count INT UNSIGNED NOT NULL DEFAULT 0,
    last_safe_error VARCHAR(500) NOT NULL DEFAULT '',
    request_id VARCHAR(64) NOT NULL DEFAULT '',
    path VARCHAR(255) NOT NULL DEFAULT '',
    idempotency_key VARCHAR(128) NOT NULL DEFAULT '',
    request_fingerprint CHAR(64) NOT NULL DEFAULT '',
    dispatch_status VARCHAR(32) NOT NULL DEFAULT 'pending'
        COMMENT 'pending|queued|running|succeeded|failed',
    generation BIGINT UNSIGNED NOT NULL DEFAULT 1,
    attempts INT UNSIGNED NOT NULL DEFAULT 0,
    max_attempts INT UNSIGNED NOT NULL DEFAULT 3,
    claim_token CHAR(36) NOT NULL DEFAULT '',
    started_at DATETIME(3) NULL,
    finished_at DATETIME(3) NULL,
    created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3)
        ON UPDATE CURRENT_TIMESTAMP(3),
    idempotency_scope VARCHAR(320) GENERATED ALWAYS AS (
        CASE
            WHEN idempotency_key = '' THEN NULL
            ELSE CONCAT(operator_user_id, ':', idempotency_key)
        END
    ) STORED,

    UNIQUE INDEX uk_icloud_resource_imports_idempotency (idempotency_scope),
    INDEX idx_icloud_resource_imports_owner_created (owner_user_id, created_at),
    INDEX idx_icloud_resource_imports_dispatch
        (status, dispatch_status, generation, id),

    CONSTRAINT fk_icloud_resource_imports_owner
        FOREIGN KEY (owner_user_id) REFERENCES users(id) ON DELETE RESTRICT,
    CONSTRAINT fk_icloud_resource_imports_operator
        FOREIGN KEY (operator_user_id) REFERENCES users(id) ON DELETE RESTRICT,
    CONSTRAINT chk_icloud_resource_imports_status
        CHECK (status IN ('processing', 'imported', 'failed')),
    CONSTRAINT chk_icloud_resource_imports_error_strategy
        CHECK (error_strategy IN ('skip', 'abort')),
    CONSTRAINT chk_icloud_resource_imports_dispatch
        CHECK (dispatch_status IN ('pending', 'queued', 'running', 'succeeded', 'failed')),
    CONSTRAINT chk_icloud_resource_imports_attempts
        CHECK (generation > 0 AND attempts <= max_attempts AND max_attempts > 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS icloud_resource_import_items (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    import_id BIGINT UNSIGNED NOT NULL,
    resource_id BIGINT UNSIGNED NULL,
    line_number INT UNSIGNED NOT NULL,
    outcome VARCHAR(32) NOT NULL COMMENT 'imported|skipped',
    category VARCHAR(64) NOT NULL DEFAULT '',
    last_safe_error VARCHAR(500) NOT NULL DEFAULT '',
    created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),

    UNIQUE INDEX uk_icloud_resource_import_items_line (import_id, line_number),
    INDEX idx_icloud_resource_import_items_resource (resource_id, import_id),

    CONSTRAINT fk_icloud_resource_import_items_import
        FOREIGN KEY (import_id) REFERENCES icloud_resource_imports(id) ON DELETE CASCADE,
    CONSTRAINT fk_icloud_resource_import_items_resource
        FOREIGN KEY (resource_id) REFERENCES email_resources(id) ON DELETE SET NULL,
    CONSTRAINT chk_icloud_resource_import_items_line CHECK (line_number > 0),
    CONSTRAINT chk_icloud_resource_import_items_outcome
        CHECK (outcome IN ('imported', 'skipped'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS icloud_allocations (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    order_no VARCHAR(64) NOT NULL,
    guard_type VARCHAR(32) NOT NULL DEFAULT 'icloud',
    project_id BIGINT UNSIGNED NOT NULL,
    product_id BIGINT UNSIGNED NOT NULL,
    resource_id BIGINT UNSIGNED NOT NULL,
    alias_id BIGINT UNSIGNED NOT NULL,
    supply_scope VARCHAR(16) NOT NULL COMMENT 'owned|public',
    email VARCHAR(320) NOT NULL,
    status VARCHAR(32) NOT NULL DEFAULT 'allocated'
        COMMENT 'allocated|released',
    active_alias_id BIGINT UNSIGNED GENERATED ALWAYS AS (
        CASE WHEN status = 'allocated' THEN alias_id ELSE NULL END
    ) STORED,
    created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    released_at DATETIME(3) NULL,

    UNIQUE INDEX uk_icloud_allocations_order (order_no),
    UNIQUE INDEX uk_icloud_allocations_active_alias (active_alias_id),
    -- Project isolation history: released aliases may go to another project,
    -- but the same project does not receive the same alias twice.
    UNIQUE INDEX uk_icloud_allocations_alias_project (alias_id, project_id),
    INDEX idx_icloud_allocations_guard (order_no, guard_type),
    INDEX idx_icloud_allocations_product_project (product_id, project_id),
    INDEX idx_icloud_allocations_project_created (project_id, created_at, id),
    INDEX idx_icloud_allocations_resource_status (resource_id, status),

    CONSTRAINT fk_icloud_allocations_guard
        FOREIGN KEY (order_no, guard_type)
        REFERENCES allocation_order_guards(order_no, type) ON DELETE RESTRICT,
    CONSTRAINT fk_icloud_allocations_project
        FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE RESTRICT,
    CONSTRAINT fk_icloud_allocations_product_project
        FOREIGN KEY (product_id, project_id)
        REFERENCES project_products(id, project_id) ON DELETE RESTRICT,
    CONSTRAINT fk_icloud_allocations_resource
        FOREIGN KEY (resource_id)
        REFERENCES icloud_resources(id) ON DELETE RESTRICT,
    CONSTRAINT fk_icloud_allocations_alias_resource
        FOREIGN KEY (alias_id, resource_id)
        REFERENCES icloud_aliases(id, resource_id) ON DELETE RESTRICT,
    CONSTRAINT chk_icloud_allocations_guard
        CHECK (guard_type = 'icloud'),
    CONSTRAINT chk_icloud_allocations_scope
        CHECK (supply_scope IN ('owned', 'public')),
    CONSTRAINT chk_icloud_allocations_status
        CHECK (status IN ('allocated', 'released')),
    CONSTRAINT chk_icloud_allocations_email
        CHECK (email <> '')
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- Orders reference allocations only by their unique order_no. Type-specific
-- allocation IDs do not belong on orders.
ALTER TABLE orders
    DROP CHECK chk_orders_product_type,
    DROP CHECK chk_orders_allocation_shape,
    ADD CONSTRAINT chk_orders_product_type CHECK (
        product_type IN ('microsoft', 'domain', 'random', 'gmail', 'icloud')
    ) NOT ENFORCED,
    ADD CONSTRAINT chk_orders_allocation_shape CHECK (
        allocation_type IS NULL
        OR allocation_type IN ('microsoft', 'domain', 'gmail', 'icloud')
    ) NOT ENFORCED,
    ALGORITHM=INSTANT;

ALTER TABLE mailmatch_messages
    DROP CHECK chk_mailmatch_messages_resource_type,
    ADD CONSTRAINT chk_mailmatch_messages_resource_type CHECK (
        resource_type IN ('microsoft', 'domain', 'gmail', 'icloud')
    ) NOT ENFORCED,
    ALGORITHM=INSTANT;

-- +goose Down

DROP TEMPORARY TABLE IF EXISTS icloud_down_guard;
CREATE TEMPORARY TABLE icloud_down_guard (
    unsafe_rows BIGINT NOT NULL,
    CONSTRAINT chk_icloud_down_guard CHECK (unsafe_rows = 0)
);
INSERT INTO icloud_down_guard (unsafe_rows)
SELECT
    (SELECT COUNT(*) FROM orders
        WHERE product_type = 'icloud' OR allocation_type = 'icloud')
    + (SELECT COUNT(*) FROM project_products WHERE type = 'icloud')
    + (SELECT COUNT(*) FROM mailmatch_messages WHERE resource_type = 'icloud')
    + (SELECT COUNT(*) FROM icloud_allocations)
    + (SELECT COUNT(*) FROM icloud_aliases)
    + (SELECT COUNT(*) FROM icloud_resource_import_items)
    + (SELECT COUNT(*) FROM icloud_resource_imports)
    + (SELECT COUNT(*) FROM icloud_resources)
    + (SELECT COUNT(*) FROM email_resources WHERE type = 'icloud')
    + (SELECT COUNT(*) FROM allocation_order_guards WHERE type = 'icloud');
DROP TEMPORARY TABLE icloud_down_guard;

ALTER TABLE mailmatch_messages
    DROP CHECK chk_mailmatch_messages_resource_type,
    ADD CONSTRAINT chk_mailmatch_messages_resource_type CHECK (
        resource_type IN ('microsoft', 'domain', 'gmail')
    ) NOT ENFORCED,
    ALGORITHM=INSTANT;

ALTER TABLE orders
    DROP CHECK chk_orders_product_type,
    DROP CHECK chk_orders_allocation_shape,
    ADD CONSTRAINT chk_orders_product_type CHECK (
        product_type IN ('microsoft', 'domain', 'random', 'gmail')
    ) NOT ENFORCED,
    ADD CONSTRAINT chk_orders_allocation_shape CHECK (
        allocation_type IS NULL
        OR allocation_type IN ('microsoft', 'domain', 'gmail')
    ) NOT ENFORCED,
    ALGORITHM=INSTANT;

DROP TABLE icloud_allocations;
DROP TABLE icloud_aliases;
DROP TABLE icloud_resource_import_items;
DROP TABLE icloud_resource_imports;
DROP TABLE icloud_resources;
DELETE FROM email_resources WHERE type = 'icloud';
DELETE FROM allocation_order_guards WHERE type = 'icloud';

ALTER TABLE allocation_order_guards
    DROP CHECK chk_allocation_order_guards_type,
    ADD CONSTRAINT chk_allocation_order_guards_type CHECK (
        type IN ('microsoft', 'domain', 'gmail')
    ) NOT ENFORCED,
    ALGORITHM=INSTANT;

ALTER TABLE email_resources
    DROP CHECK chk_email_resources_type,
    ADD CONSTRAINT chk_email_resources_type CHECK (
        type IN ('microsoft', 'domain', 'gmail')
    ) NOT ENFORCED,
    ALGORITHM=INSTANT;

ALTER TABLE project_products
    DROP CHECK chk_project_products_type,
    DROP CHECK chk_project_products_weights,
    ADD CONSTRAINT chk_project_products_type CHECK (
        type IN ('microsoft', 'domain', 'random', 'gmail')
    ),
    ADD CONSTRAINT chk_project_products_weights CHECK (
        main_weight >= 0
        AND dot_weight >= 0
        AND plus_weight >= 0
        AND (type NOT IN ('microsoft', 'gmail')
             OR main_weight + dot_weight + plus_weight > 0)
        AND (type <> 'domain'
             OR (main_weight = 0 AND dot_weight = 0 AND plus_weight = 0))
        AND (type <> 'random'
             OR (main_weight = 1 AND dot_weight = 1 AND plus_weight = 1))
    );
