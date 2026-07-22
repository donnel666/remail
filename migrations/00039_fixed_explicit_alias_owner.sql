-- +goose Up

-- Product invariant: users.id=1 is the first activated super administrator.
-- Explicit aliases are platform inventory and must never inherit a resource
-- owner or accept a caller-selected user ID.
DROP PROCEDURE IF EXISTS assert_fixed_explicit_alias_owner_00039;

-- +goose StatementBegin
CREATE PROCEDURE assert_fixed_explicit_alias_owner_00039()
BEGIN
    IF EXISTS (SELECT 1 FROM explicit_aliases LIMIT 1)
       AND NOT EXISTS (
           SELECT 1
           FROM users
           WHERE id = 1
             AND role = 'super_admin'
       ) THEN
        SIGNAL SQLSTATE '45000'
            SET MESSAGE_TEXT = 'migration 00039 requires users.id=1 to be the super administrator before migrating explicit aliases';
    END IF;
END
-- +goose StatementEnd

CALL assert_fixed_explicit_alias_owner_00039();
DROP PROCEDURE assert_fixed_explicit_alias_owner_00039;

UPDATE explicit_aliases AS alias_row
JOIN users AS fixed_owner
  ON fixed_owner.id = 1
 AND fixed_owner.role = 'super_admin'
SET alias_row.owner_user_id = 1
WHERE alias_row.owner_user_id <> 1;

ALTER TABLE explicit_aliases
    ADD CONSTRAINT chk_explicit_aliases_owner_user_id
        CHECK (owner_user_id = 1);

-- +goose Down

DROP PROCEDURE IF EXISTS assert_fixed_explicit_alias_owner_00039;

ALTER TABLE explicit_aliases
    DROP CHECK chk_explicit_aliases_owner_user_id;
