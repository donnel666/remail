-- +goose Up

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

-- +goose Down

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
