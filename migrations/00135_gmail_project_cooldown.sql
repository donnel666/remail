-- +goose Up

-- Project cooldowns now live exclusively in Redis. Old global cooldown rows
-- must stop blocking allocations in unrelated projects.
UPDATE gmail_resources SET status = 'available' WHERE status = 'cooldown';

-- +goose Down

-- The previous temporary global cooldown cannot be reconstructed.
