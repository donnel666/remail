-- +goose Up

ALTER TABLE users
    ADD COLUMN status VARCHAR(32) NOT NULL DEFAULT 'active' AFTER nickname;

UPDATE users
SET status = CASE WHEN enabled = TRUE THEN 'active' ELSE 'disabled' END;

ALTER TABLE users
    ADD INDEX idx_users_status (status),
    ADD CONSTRAINT chk_users_status CHECK (status IN ('active', 'disabled', 'deleted')),
    DROP COLUMN enabled;

-- +goose Down

ALTER TABLE users
    ADD COLUMN enabled TINYINT(1) NOT NULL DEFAULT 1 AFTER nickname;

UPDATE users
SET enabled = (status = 'active');

ALTER TABLE users
    DROP CHECK chk_users_status,
    DROP INDEX idx_users_status,
    DROP COLUMN status;
