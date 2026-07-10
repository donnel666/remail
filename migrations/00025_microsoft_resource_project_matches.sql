-- +goose Up

CREATE TABLE microsoft_resource_project_matches (
    resource_id BIGINT UNSIGNED NOT NULL,
    project_id BIGINT UNSIGNED NOT NULL,
    first_matched_at DATETIME(3) NOT NULL,
    last_matched_at DATETIME(3) NOT NULL,
    evidence_count INT UNSIGNED NOT NULL DEFAULT 1,
    last_scanned_at DATETIME(3) NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    PRIMARY KEY (resource_id, project_id),
    INDEX idx_ms_resource_project_matches_project (project_id, resource_id),
    INDEX idx_ms_resource_project_matches_scan (resource_id, last_scanned_at),
    CONSTRAINT fk_ms_resource_project_matches_resource
        FOREIGN KEY (resource_id) REFERENCES microsoft_resources(id) ON DELETE CASCADE,
    CONSTRAINT fk_ms_resource_project_matches_project
        FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE,
    CONSTRAINT chk_ms_resource_project_matches_count CHECK (evidence_count > 0),
    CONSTRAINT chk_ms_resource_project_matches_time CHECK (first_matched_at <= last_matched_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- +goose Down

DROP TABLE IF EXISTS microsoft_resource_project_matches;
