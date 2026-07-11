-- +goose Up
ALTER TABLE resource_validation_jobs
    DROP INDEX idx_resource_validation_claim,
    ADD COLUMN dispatch_token CHAR(36) NOT NULL DEFAULT '' AFTER claim_token,
    ADD COLUMN dispatched_at DATETIME(3) NULL AFTER dispatch_token,
    ADD INDEX idx_resource_validation_dispatch (status, dispatched_at, id);

ALTER TABLE resource_validation_batches
    ADD COLUMN through_id BIGINT UNSIGNED NOT NULL DEFAULT 0 AFTER after_id;

UPDATE resource_validation_batches AS b
SET through_id = (
    SELECT COALESCE(MAX(er.id), 0)
    FROM email_resources AS er
    WHERE er.owner_user_id = b.owner_user_id
)
WHERE b.status = 'pending';

CREATE INDEX idx_email_resources_owner_type_id
    ON email_resources(owner_user_id, type, id);

-- +goose Down
DROP INDEX idx_email_resources_owner_type_id ON email_resources;

ALTER TABLE resource_validation_batches
    DROP COLUMN through_id;

ALTER TABLE resource_validation_jobs
    DROP INDEX idx_resource_validation_dispatch,
    DROP COLUMN dispatched_at,
    DROP COLUMN dispatch_token,
    ADD INDEX idx_resource_validation_claim (status, claim_token, id);
