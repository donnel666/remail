-- +goose Up

-- iCloud onboarding is resource-first.  The resource row contains both the
-- account fields and the small amount of durable workflow state needed to
-- rebuild a Redis task after a worker restart.  No per-account onboarding
-- table is required.
ALTER TABLE icloud_resources
    ADD COLUMN import_id BIGINT UNSIGNED NULL,
    ADD COLUMN resource_id BIGINT UNSIGNED NULL,
    ADD COLUMN task_kind VARCHAR(16) NOT NULL DEFAULT '',
    ADD COLUMN line_number INT UNSIGNED NOT NULL DEFAULT 0,
    ADD COLUMN family_reservation_confirmed TINYINT(1) NOT NULL DEFAULT 0,
    ADD COLUMN secret_payload JSON NULL,
    ADD COLUMN session_payload JSON NULL,
    ADD COLUMN manual_verification_code VARCHAR(64) NOT NULL DEFAULT '',
    ADD COLUMN pending_sms_purpose VARCHAR(64) NOT NULL DEFAULT '',
    ADD COLUMN sms_sent_at DATETIME(3) NULL,
    ADD COLUMN sms_poll_deadline DATETIME(3) NULL,
    ADD COLUMN forward_preparation_id BIGINT UNSIGNED NULL,
    ADD COLUMN onboarding_status VARCHAR(16) NOT NULL DEFAULT '',
    ADD COLUMN stage VARCHAR(48) NOT NULL DEFAULT '',
    ADD COLUMN dispatch_status VARCHAR(16) NOT NULL DEFAULT '',
    ADD COLUMN generation BIGINT UNSIGNED NOT NULL DEFAULT 0,
    ADD COLUMN expected_credential_revision BIGINT UNSIGNED NOT NULL DEFAULT 0,
    ADD COLUMN claim_token CHAR(36) NOT NULL DEFAULT '',
    ADD COLUMN attempts INT UNSIGNED NOT NULL DEFAULT 0,
    ADD COLUMN max_attempts INT UNSIGNED NOT NULL DEFAULT 0,
    ADD COLUMN stage_attempts INT UNSIGNED NOT NULL DEFAULT 0,
    ADD COLUMN next_attempt_at DATETIME(3) NULL,
    ADD COLUMN last_error_category VARCHAR(64) NOT NULL DEFAULT '',
    ADD COLUMN started_at DATETIME(3) NULL,
    ADD COLUMN finished_at DATETIME(3) NULL,
    ADD COLUMN icloud_activation_confirmed_at DATETIME(3) NULL,
    ADD COLUMN onboarding_operator_user_id BIGINT UNSIGNED NULL,
    ADD COLUMN onboarding_request_id VARCHAR(64) NOT NULL DEFAULT '',
    ADD COLUMN onboarding_idempotency_key VARCHAR(128) NOT NULL DEFAULT '',
    ADD COLUMN onboarding_request_fingerprint CHAR(64) NOT NULL DEFAULT '',
    ALGORITHM=INSTANT;

-- Index creation is online so the resource inventory remains usable while the
-- new workflow columns are indexed.
ALTER TABLE icloud_resources
    ADD INDEX idx_icloud_resources_workflow_dispatch
        (onboarding_status, dispatch_status, next_attempt_at, id),
    ADD INDEX idx_icloud_resources_workflow_import
        (import_id, line_number, id),
    ADD INDEX idx_icloud_resources_workflow_phone
        (kitesim_phone_id, onboarding_status, id),
    ALGORITHM=INPLACE, LOCK=NONE;

-- Preserve active work when upgrading an installation that already used the
-- temporary onboarding tables. Rows without a resource were never sellable
-- and are intentionally discarded with their transient task record.
UPDATE icloud_resources AS resource
JOIN icloud_account_onboarding_tasks AS task ON task.resource_id = resource.id
LEFT JOIN icloud_account_onboarding_tasks AS newer
    ON newer.resource_id = task.resource_id
   AND (newer.updated_at > task.updated_at
        OR (newer.updated_at = task.updated_at AND newer.id > task.id))
LEFT JOIN icloud_account_onboarding_imports AS batch ON batch.id = task.import_id
SET resource.import_id = task.import_id,
    resource.resource_id = resource.id,
    resource.task_kind = task.task_kind,
    resource.line_number = task.line_number,
    resource.account_role = CASE
        WHEN task.task_kind = 'onboarding' THEN task.account_role
        ELSE resource.account_role
    END,
    resource.family_primary_resource_id = COALESCE(task.family_primary_resource_id, resource.family_primary_resource_id),
    resource.family_reservation_confirmed = task.family_reservation_confirmed,
    resource.region = COALESCE(NULLIF(task.region, ''), resource.region),
    resource.country_code = COALESCE(NULLIF(task.country_code, ''), resource.country_code),
    resource.icloud_opened = CASE
        WHEN task.task_kind = 'onboarding' THEN task.icloud_opened
        ELSE resource.icloud_opened
    END,
    resource.bound_phone_number = COALESCE(NULLIF(task.bound_phone_number, ''), resource.bound_phone_number),
    resource.bound_phone_country_code = COALESCE(NULLIF(task.bound_phone_country_code, ''), resource.bound_phone_country_code),
    resource.bound_phone_source = COALESCE(NULLIF(task.bound_phone_source, ''), resource.bound_phone_source),
    resource.kitesim_phone_id = COALESCE(task.kitesim_phone_id, resource.kitesim_phone_id),
    resource.family_invite_url = COALESCE(NULLIF(task.family_invite_url, ''), resource.family_invite_url),
    resource.secret_payload = task.secret_payload,
    resource.session_payload = task.session_payload,
    resource.manual_verification_code = task.manual_verification_code,
    resource.pending_sms_purpose = task.pending_sms_purpose,
    resource.sms_sent_at = task.sms_sent_at,
    resource.sms_poll_deadline = task.sms_poll_deadline,
    resource.forward_preparation_id = task.forward_preparation_id,
    resource.onboarding_status = task.status,
    resource.stage = task.stage,
    resource.dispatch_status = task.dispatch_status,
    resource.generation = task.generation,
    resource.expected_credential_revision = task.expected_credential_revision,
    resource.claim_token = task.claim_token,
    resource.attempts = task.attempts,
    resource.max_attempts = task.max_attempts,
    resource.stage_attempts = task.stage_attempts,
    resource.next_attempt_at = task.next_attempt_at,
    resource.last_error_category = task.last_error_category,
    resource.last_safe_error = CASE
        WHEN task.task_kind = 'onboarding'
            THEN COALESCE(NULLIF(task.last_safe_error, ''), resource.last_safe_error)
        ELSE resource.last_safe_error
    END,
    resource.started_at = task.started_at,
    resource.finished_at = task.finished_at,
    resource.icloud_activation_confirmed_at = task.icloud_activation_confirmed_at,
    resource.onboarding_operator_user_id = COALESCE(batch.operator_user_id, resource.onboarding_operator_user_id),
    resource.onboarding_request_id = COALESCE(batch.request_id, resource.onboarding_request_id),
    resource.onboarding_idempotency_key = COALESCE(batch.idempotency_key, resource.onboarding_idempotency_key),
    resource.onboarding_request_fingerprint = COALESCE(batch.request_fingerprint, resource.onboarding_request_fingerprint),
    resource.expire_at = CASE
        WHEN task.task_kind = 'onboarding' AND batch.resource_expire_at IS NOT NULL
            THEN batch.resource_expire_at
        ELSE resource.expire_at
    END,
    resource.for_sale = CASE
        WHEN task.task_kind = 'onboarding' THEN 0
        ELSE resource.for_sale
    END,
    resource.status = CASE
        WHEN task.task_kind = 'onboarding' AND task.status IN ('processing', 'waiting') AND resource.status <> 'disabled' THEN 'pending'
        ELSE resource.status
    END,
    resource.next_validation_at = CASE
        WHEN task.task_kind = 'onboarding' AND task.status IN ('processing', 'waiting') THEN NULL
        ELSE resource.next_validation_at
    END,
    resource.next_provision_at = CASE
        WHEN task.task_kind = 'onboarding' AND task.status IN ('processing', 'waiting') THEN NULL
        ELSE resource.next_provision_at
    END
WHERE newer.id IS NULL
;

DELETE reservation
FROM icloud_apple_id_reservations AS reservation
LEFT JOIN icloud_resources AS resource
    ON resource.import_id = reservation.owner_id
   AND resource.task_kind = 'onboarding'
   AND LOWER(TRIM(resource.primary_email)) = reservation.email_key
WHERE reservation.owner_kind = 'onboarding' AND resource.id IS NULL;

DROP TABLE icloud_account_onboarding_tasks;
DROP TABLE icloud_account_onboarding_imports;

-- +goose Down

-- Irreversible: the source task/import tables were folded into resource rows
-- and dropped. Failing here prevents Goose from recording a partial rollback
-- that older migrations cannot safely continue.
SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'migration 00116_icloud_resource_workflow is irreversible';
