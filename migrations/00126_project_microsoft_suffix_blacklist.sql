-- +goose Up

CREATE TABLE project_microsoft_suffix_blacklists (
    project_id BIGINT UNSIGNED NOT NULL,
    suffix VARCHAR(255) NOT NULL,
    PRIMARY KEY (project_id, suffix),
    CONSTRAINT fk_project_microsoft_suffix_blacklists_project
        FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE
);

-- +goose Down

DROP TABLE project_microsoft_suffix_blacklists;
