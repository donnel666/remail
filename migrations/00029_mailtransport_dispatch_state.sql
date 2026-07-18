-- +goose Up
ALTER TABLE inbound_mails
    ADD COLUMN process_generation BIGINT UNSIGNED NOT NULL DEFAULT 1 AFTER status,
    ADD COLUMN process_attempts TINYINT UNSIGNED NOT NULL DEFAULT 0 AFTER process_generation;

ALTER TABLE outbound_mails
    ADD COLUMN send_generation BIGINT UNSIGNED NOT NULL DEFAULT 1 AFTER status;

-- An in-flight status does not prove that its ephemeral Redis task survived
-- deployment. Release those rows and fence every pre-cutover task payload.
UPDATE inbound_mails
SET status = 'pending',
    process_generation = process_generation + 1,
    updated_at = CURRENT_TIMESTAMP
WHERE status = 'processing';

UPDATE outbound_mails
SET status = 'pending',
    send_generation = send_generation + 1,
    retries = LEAST(retries, 3),
    updated_at = CURRENT_TIMESTAMP
WHERE status = 'sending';

UPDATE outbound_mails
SET retries = LEAST(retries, 3)
WHERE retries > 3;

ALTER TABLE inbound_mails
    ADD CONSTRAINT chk_inbound_mails_process_attempts
        CHECK (process_attempts <= 3);

ALTER TABLE outbound_mails
    DROP CHECK chk_outbound_mails_retries,
    ADD CONSTRAINT chk_outbound_mails_retries
        CHECK (retries BETWEEN 0 AND 3);

-- Existing (status, created_at, id) indexes are the pending dispatch indexes.

-- +goose Down
ALTER TABLE outbound_mails
    DROP CHECK chk_outbound_mails_retries,
    ADD CONSTRAINT chk_outbound_mails_retries
        CHECK (retries >= 0),
    DROP COLUMN send_generation;

ALTER TABLE inbound_mails
    DROP CHECK chk_inbound_mails_process_attempts,
    DROP COLUMN process_attempts,
    DROP COLUMN process_generation;
