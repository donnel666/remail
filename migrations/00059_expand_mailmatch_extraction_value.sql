-- +goose Up

ALTER TABLE mailmatch_messages
    MODIFY COLUMN verification_code VARCHAR(4096) NOT NULL DEFAULT '';

ALTER TABLE mailmatch_message_projections
    MODIFY COLUMN verification_code VARCHAR(4096) NOT NULL DEFAULT '';

-- +goose Down

UPDATE mailmatch_message_projections
SET verification_code = LEFT(verification_code, 64)
WHERE CHAR_LENGTH(verification_code) > 64;

UPDATE mailmatch_messages
SET verification_code = LEFT(verification_code, 64)
WHERE CHAR_LENGTH(verification_code) > 64;

ALTER TABLE mailmatch_message_projections
    MODIFY COLUMN verification_code VARCHAR(64) NOT NULL DEFAULT '';

ALTER TABLE mailmatch_messages
    MODIFY COLUMN verification_code VARCHAR(64) NOT NULL DEFAULT '';
