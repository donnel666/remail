-- +goose Up

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

-- +goose Down
DELETE FROM casbin_rule WHERE v1 = 'core:project';
DROP TABLE IF EXISTS project_accesses;
DROP TABLE IF EXISTS project_mail_rules;
DROP TABLE IF EXISTS project_products;
DROP TABLE IF EXISTS projects;
