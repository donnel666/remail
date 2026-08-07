-- +goose Up

-- Allocation reads and locks the authoritative resource tables directly.
-- These project-by-resource mirrors only multiplied storage and refresh work.
DROP TABLE IF EXISTS microsoft_routing_candidates;
DROP TABLE IF EXISTS domain_routing_candidates;

ALTER TABLE projects
    DROP FOREIGN KEY fk_projects_candidate_refresh_operator,
    DROP CHECK chk_projects_candidate_refresh_status,
    DROP CHECK chk_projects_candidate_refresh_failures,
    DROP INDEX idx_projects_candidate_refresh_pending,
    DROP COLUMN candidate_refresh_finished_at,
    DROP COLUMN candidate_refresh_started_at,
    DROP COLUMN candidate_refresh_requested_at,
    DROP COLUMN candidate_refresh_last_safe_error,
    DROP COLUMN candidate_refresh_path,
    DROP COLUMN candidate_refresh_request_id,
    DROP COLUMN candidate_refresh_operator_user_id,
    DROP COLUMN candidate_refresh_affected,
    DROP COLUMN candidate_refresh_failures,
    DROP COLUMN candidate_refresh_generation,
    DROP COLUMN candidate_refresh_status;

-- +goose Down

ALTER TABLE projects
    ADD COLUMN candidate_refresh_status VARCHAR(32) NOT NULL DEFAULT 'normal'
        COMMENT 'pending|processing|normal|abnormal' AFTER loose_match,
    ADD COLUMN candidate_refresh_generation BIGINT UNSIGNED NOT NULL DEFAULT 0
        AFTER candidate_refresh_status,
    ADD COLUMN candidate_refresh_failures TINYINT UNSIGNED NOT NULL DEFAULT 0
        AFTER candidate_refresh_generation,
    ADD COLUMN candidate_refresh_affected INT UNSIGNED NOT NULL DEFAULT 0
        AFTER candidate_refresh_failures,
    ADD COLUMN candidate_refresh_operator_user_id BIGINT UNSIGNED NULL
        AFTER candidate_refresh_affected,
    ADD COLUMN candidate_refresh_request_id VARCHAR(64) NOT NULL DEFAULT ''
        AFTER candidate_refresh_operator_user_id,
    ADD COLUMN candidate_refresh_path VARCHAR(255) NOT NULL DEFAULT ''
        AFTER candidate_refresh_request_id,
    ADD COLUMN candidate_refresh_last_safe_error VARCHAR(500) NOT NULL DEFAULT ''
        AFTER candidate_refresh_path,
    ADD COLUMN candidate_refresh_requested_at DATETIME NULL
        AFTER candidate_refresh_last_safe_error,
    ADD COLUMN candidate_refresh_started_at DATETIME NULL
        AFTER candidate_refresh_requested_at,
    ADD COLUMN candidate_refresh_finished_at DATETIME NULL
        AFTER candidate_refresh_started_at,
    ADD INDEX idx_projects_candidate_refresh_pending
        (candidate_refresh_status, candidate_refresh_requested_at, id),
    ADD CONSTRAINT fk_projects_candidate_refresh_operator
        FOREIGN KEY (candidate_refresh_operator_user_id) REFERENCES users(id) ON DELETE SET NULL,
    ADD CONSTRAINT chk_projects_candidate_refresh_status
        CHECK (candidate_refresh_status IN ('pending', 'processing', 'normal', 'abnormal')),
    ADD CONSTRAINT chk_projects_candidate_refresh_failures
        CHECK (candidate_refresh_failures <= 3);

CREATE TABLE microsoft_routing_candidates (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    project_id BIGINT UNSIGNED NOT NULL,
    resource_id BIGINT UNSIGNED NOT NULL,
    email_address VARCHAR(255) NOT NULL,
    domain_suffix VARCHAR(255) NOT NULL DEFAULT '',
    for_sale TINYINT(1) NOT NULL DEFAULT 1,
    quality_score INT NOT NULL DEFAULT 0,
    status VARCHAR(32) NOT NULL DEFAULT 'normal' COMMENT 'normal|abnormal|disabled',
    alloc_bucket SMALLINT UNSIGNED NOT NULL DEFAULT 0,
    last_allocated_at DATETIME NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    UNIQUE INDEX idx_ms_candidates_project_resource (project_id, resource_id),
    INDEX idx_ms_candidates_project_bucket
        (project_id, alloc_bucket, status, for_sale, last_allocated_at, quality_score, resource_id),
    INDEX idx_ms_candidates_resource (resource_id),
    CONSTRAINT fk_ms_candidates_project
        FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE,
    CONSTRAINT fk_ms_candidates_resource
        FOREIGN KEY (resource_id) REFERENCES microsoft_resources(id) ON DELETE CASCADE,
    CONSTRAINT chk_ms_candidates_status
        CHECK (status IN ('normal', 'abnormal', 'disabled'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE domain_routing_candidates (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    project_id BIGINT UNSIGNED NOT NULL,
    resource_id BIGINT UNSIGNED NOT NULL,
    domain VARCHAR(255) NOT NULL,
    domain_tld VARCHAR(64) NOT NULL DEFAULT '',
    purpose VARCHAR(32) NOT NULL DEFAULT 'sale',
    status VARCHAR(32) NOT NULL DEFAULT 'normal' COMMENT 'normal|abnormal|disabled',
    alloc_bucket SMALLINT UNSIGNED NOT NULL DEFAULT 0,
    last_allocated_at DATETIME NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    UNIQUE INDEX idx_domain_candidates_project_resource (project_id, resource_id),
    INDEX idx_domain_candidates_project_bucket
        (project_id, alloc_bucket, status, purpose, last_allocated_at, resource_id),
    INDEX idx_domain_candidates_resource (resource_id),
    CONSTRAINT fk_domain_candidates_project
        FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE,
    CONSTRAINT fk_domain_candidates_resource
        FOREIGN KEY (resource_id) REFERENCES domain_resources(id) ON DELETE CASCADE,
    CONSTRAINT chk_domain_candidates_purpose
        CHECK (purpose IN ('sale', 'not_sale')),
    CONSTRAINT chk_domain_candidates_status
        CHECK (status IN ('normal', 'abnormal', 'disabled'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
