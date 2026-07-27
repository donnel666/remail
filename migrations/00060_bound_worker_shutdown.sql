-- +goose Up

UPDATE system_settings
SET value = '5'
WHERE `key` = 'asynq_shutdown_timeout_seconds'
  AND value = '30';

-- +goose Down

-- The previous administrator-owned value cannot be reconstructed safely.
SELECT 1;
