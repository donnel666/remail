-- +goose Up
ALTER TABLE resource_validation_jobs
    ADD COLUMN claim_token CHAR(36) NOT NULL DEFAULT '' AFTER max_attempts,
    ADD INDEX idx_resource_validation_claim (status, claim_token, id);

-- +goose Down
ALTER TABLE resource_validation_jobs
    DROP INDEX idx_resource_validation_claim,
    DROP COLUMN claim_token;
