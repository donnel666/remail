-- +goose Up

CREATE TABLE kitesim_sync_runs (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    account_id BIGINT UNSIGNED NOT NULL,
    status VARCHAR(16) NOT NULL,
    attempts INT UNSIGNED NOT NULL DEFAULT 0,
    last_safe_error VARCHAR(500) NOT NULL DEFAULT '',
    queued_at DATETIME(3) NOT NULL,
    started_at DATETIME(3) NULL,
    finished_at DATETIME(3) NULL,
    created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3)
        ON UPDATE CURRENT_TIMESTAMP(3),
    active_account_id BIGINT UNSIGNED GENERATED ALWAYS AS (
        CASE WHEN status IN ('queued', 'running') THEN account_id ELSE NULL END
    ) STORED,

    UNIQUE INDEX uk_kitesim_sync_runs_active_account (active_account_id),
    INDEX idx_kitesim_sync_runs_account_recent (account_id, updated_at, id),
    INDEX idx_kitesim_sync_runs_dispatch (status, updated_at, id),
    CONSTRAINT fk_kitesim_sync_runs_account
        FOREIGN KEY (account_id) REFERENCES kitesim_accounts(id) ON DELETE RESTRICT,
    CONSTRAINT chk_kitesim_sync_runs_status
        CHECK (status IN ('queued', 'running', 'succeeded', 'failed'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

INSERT INTO kitesim_sync_runs (
    account_id,
    status,
    attempts,
    last_safe_error,
    queued_at,
    started_at,
    finished_at,
    created_at,
    updated_at
)
SELECT
    id,
    sync_status,
    sync_attempts,
    last_safe_error,
    COALESCE(sync_queued_at, created_at),
    sync_started_at,
    sync_finished_at,
    COALESCE(sync_queued_at, created_at),
    COALESCE(sync_finished_at, sync_started_at, sync_queued_at, updated_at)
FROM kitesim_accounts
WHERE sync_status <> 'idle';

-- +goose Down

DROP TABLE kitesim_sync_runs;
