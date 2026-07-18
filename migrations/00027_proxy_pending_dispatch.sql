-- +goose Up
ALTER TABLE proxies
    DROP CHECK chk_proxies_status,
    MODIFY COLUMN status VARCHAR(32) NOT NULL DEFAULT 'pending' COMMENT 'pending|checking|normal|abnormal|disabled|expired',
    ADD COLUMN check_generation BIGINT UNSIGNED NOT NULL DEFAULT 1 AFTER check_path,
    ADD INDEX idx_proxies_check_dispatch (status, updated_at, id);

-- A checking row is not proof that a Redis task still exists. Releasing all
-- in-flight rows makes the business table authoritative after this cutover.
UPDATE proxies
SET status = 'pending',
    check_generation = check_generation + 1,
    updated_at = CURRENT_TIMESTAMP
WHERE status = 'checking';

ALTER TABLE proxies
    ADD CONSTRAINT chk_proxies_status
        CHECK (status IN ('pending', 'checking', 'normal', 'abnormal', 'disabled', 'expired'));

DROP TABLE IF EXISTS proxy_check_job_items;
DROP TABLE IF EXISTS proxy_check_jobs;

-- +goose Down
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

-- Recreate one legacy task for every unfinished business row so rolling the
-- application back cannot turn pending into checking without a task.
INSERT INTO proxy_check_jobs(
    kind, batch_mode, status, proxy_id, filter_json,
    operator_user_id, request_id, path, last_safe_error,
    created_at, updated_at
)
SELECT
    'single', '', 'pending', id, '{}',
    check_operator_user_id, check_request_id, check_path, '',
    CURRENT_TIMESTAMP, CURRENT_TIMESTAMP
FROM proxies
WHERE status IN ('pending', 'checking');

-- The legacy schema has no pending proxy status; the recreated jobs above are
-- now the authoritative pending work.
UPDATE proxies
SET status = 'checking',
    updated_at = CURRENT_TIMESTAMP
WHERE status = 'pending';

ALTER TABLE proxies
    DROP CHECK chk_proxies_status,
    MODIFY COLUMN status VARCHAR(32) NOT NULL DEFAULT 'checking' COMMENT 'checking|normal|abnormal|disabled|expired',
    DROP INDEX idx_proxies_check_dispatch,
    DROP COLUMN check_generation,
    ADD CONSTRAINT chk_proxies_status
        CHECK (status IN ('checking', 'normal', 'abnormal', 'disabled', 'expired'));
