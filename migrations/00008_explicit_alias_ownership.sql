-- +goose Up
ALTER TABLE explicit_aliases
    ADD COLUMN owner_user_id BIGINT UNSIGNED NULL AFTER resource_id;

UPDATE explicit_aliases AS alias_row
JOIN (
    SELECT MIN(id) AS owner_user_id
    FROM users
    WHERE role = 'super_admin'
) AS deterministic_owner ON deterministic_owner.owner_user_id IS NOT NULL
SET alias_row.owner_user_id = deterministic_owner.owner_user_id
WHERE alias_row.owner_user_id IS NULL;

ALTER TABLE explicit_aliases
    MODIFY COLUMN owner_user_id BIGINT UNSIGNED NOT NULL,
    ADD INDEX idx_explicit_aliases_owner (owner_user_id),
    ADD CONSTRAINT fk_explicit_aliases_owner
        FOREIGN KEY (owner_user_id) REFERENCES users(id) ON DELETE RESTRICT;

-- +goose Down
ALTER TABLE explicit_aliases
    DROP FOREIGN KEY fk_explicit_aliases_owner,
    DROP INDEX idx_explicit_aliases_owner,
    DROP COLUMN owner_user_id;
