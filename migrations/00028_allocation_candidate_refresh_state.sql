-- +goose Up

-- Candidate refresh is one piece of mutable state per project, not a durable
-- job history. Redis/Asynq only carries the current project generation.
CREATE TABLE IF NOT EXISTS microsoft_routing_candidates (
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

CREATE TABLE IF NOT EXISTS domain_routing_candidates (
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

-- Some installations were created from the squashed initial migration after
-- it had already dropped this legacy table. Recreate only the migration input
-- shape when absent; it is dropped below in the same Up migration.
CREATE TABLE IF NOT EXISTS allocation_candidate_refresh_jobs (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    project_id BIGINT UNSIGNED NOT NULL,
    operator_user_id BIGINT UNSIGNED NOT NULL,
    status VARCHAR(32) NOT NULL DEFAULT 'pending',
    affected INT NOT NULL DEFAULT 0,
    attempts INT NOT NULL DEFAULT 0,
    max_attempts INT NOT NULL DEFAULT 1,
    last_safe_error VARCHAR(500) NOT NULL DEFAULT '',
    request_id VARCHAR(64) NOT NULL DEFAULT '',
    path VARCHAR(255) NOT NULL DEFAULT '',
    started_at DATETIME NULL,
    finished_at DATETIME NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    INDEX idx_alloc_refresh_status_updated (status, updated_at, id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

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

-- Legacy attempts mixed execution and infrastructure retries, so active work
-- starts a fresh business-failure budget in the project state.
UPDATE projects AS project
JOIN allocation_candidate_refresh_jobs AS job
  ON job.project_id = project.id
 AND job.status IN ('pending', 'queued', 'running')
SET project.candidate_refresh_status = 'pending',
    project.candidate_refresh_generation = 1,
    project.candidate_refresh_failures = 0,
    project.candidate_refresh_affected = GREATEST(job.affected, 0),
    project.candidate_refresh_operator_user_id = job.operator_user_id,
    project.candidate_refresh_request_id = job.request_id,
    project.candidate_refresh_path = job.path,
    project.candidate_refresh_last_safe_error = job.last_safe_error,
    project.candidate_refresh_requested_at = job.created_at,
    project.candidate_refresh_started_at = NULL,
    project.candidate_refresh_finished_at = NULL;

DROP TABLE IF EXISTS allocation_candidate_refresh_jobs;

-- +goose Down

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

-- A deleted operator is represented as NULL by the new schema and cannot be
-- safely forged into the legacy non-null foreign key.
INSERT INTO allocation_candidate_refresh_jobs(
    project_id, operator_user_id, status, affected, attempts, max_attempts,
    last_safe_error, request_id, path, created_at, updated_at
)
SELECT
    id, candidate_refresh_operator_user_id, 'pending', candidate_refresh_affected, 0, 1,
    candidate_refresh_last_safe_error, candidate_refresh_request_id, candidate_refresh_path,
    COALESCE(candidate_refresh_requested_at, CURRENT_TIMESTAMP), CURRENT_TIMESTAMP
FROM projects
WHERE candidate_refresh_status IN ('pending', 'processing')
  AND candidate_refresh_operator_user_id IS NOT NULL;

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
