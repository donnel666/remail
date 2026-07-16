-- +goose Up

-- Migration 20 already moved every valid masked display into binding_address.
-- This final guard handles a deployment that wrote one more legacy row between
-- the two migrations.
UPDATE microsoft_binding_mailboxes
SET binding_address = LOWER(TRIM(bound_display))
WHERE TRIM(bound_display) <> ''
  AND LOCATE('*', SUBSTRING_INDEX(LOWER(TRIM(bound_display)), '@', 1)) > 0
  AND LENGTH(LOWER(TRIM(bound_display))) - LENGTH(REPLACE(LOWER(TRIM(bound_display)), '@', '')) = 1
  AND SUBSTRING_INDEX(LOWER(TRIM(bound_display)), '@', 1) <> ''
  AND SUBSTRING_INDEX(LOWER(TRIM(bound_display)), '@', -1) <> ''
  AND SUBSTRING_INDEX(LOWER(TRIM(bound_display)), '@', 1) NOT REGEXP '[[:space:]]';

-- blocked_resource_signature used to contain bound_display. Rewrite the
-- snapshots before dropping the column so paused schedules do not wake merely
-- because the storage shape changed.
UPDATE microsoft_alias_schedules AS schedule
JOIN microsoft_resources AS resource ON resource.id = schedule.resource_id
LEFT JOIN microsoft_binding_mailboxes AS binding
  ON binding.resource_id = resource.id
 AND binding.status <> 'expired'
SET schedule.blocked_resource_signature = SHA2(CONCAT_WS(
        CHAR(0),
        resource.status,
        resource.email_address,
        resource.password,
        resource.client_id,
        resource.refresh_token,
        COALESCE(binding.account_email, ''),
        COALESCE(binding.binding_address, ''),
        COALESCE(binding.status, '')
    ), 256)
WHERE schedule.blocked_resource_signature <> '';

ALTER TABLE microsoft_binding_mailboxes
    DROP COLUMN bound_display;

-- +goose Down

ALTER TABLE microsoft_binding_mailboxes
    ADD COLUMN bound_display VARCHAR(320) NOT NULL DEFAULT '' AFTER code_msg_id;

UPDATE microsoft_alias_schedules AS schedule
JOIN microsoft_resources AS resource ON resource.id = schedule.resource_id
LEFT JOIN microsoft_binding_mailboxes AS binding
  ON binding.resource_id = resource.id
 AND binding.status <> 'expired'
SET schedule.blocked_resource_signature = SHA2(CONCAT_WS(
        CHAR(0),
        resource.status,
        resource.email_address,
        resource.password,
        resource.client_id,
        resource.refresh_token,
        COALESCE(binding.account_email, ''),
        COALESCE(binding.binding_address, ''),
        COALESCE(binding.status, ''),
        ''
    ), 256)
WHERE schedule.blocked_resource_signature <> '';
