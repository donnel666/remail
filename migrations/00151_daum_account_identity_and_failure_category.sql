-- +goose Up
DROP PROCEDURE IF EXISTS correct_daum_identity_00151;
-- +goose StatementBegin
CREATE PROCEDURE correct_daum_identity_00151()
BEGIN
    IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = 'daum_resources' AND column_name = 'kakao_email') THEN
        ALTER TABLE daum_resources ADD COLUMN kakao_email VARCHAR(320) NOT NULL DEFAULT '', ALGORITHM=INSTANT;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = 'daum_resources' AND column_name = 'failure_category') THEN
        ALTER TABLE daum_resources ADD COLUMN failure_category VARCHAR(64) NOT NULL DEFAULT '', ALGORITHM=INSTANT;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM information_schema.table_constraints WHERE constraint_schema = DATABASE() AND table_name = 'daum_resources' AND constraint_name = 'chk_daum_failure_category') THEN
        ALTER TABLE daum_resources ADD CONSTRAINT chk_daum_failure_category CHECK (failure_category IN ('','invalid_credentials','identity_mismatch','account_disabled'));
    END IF;
END;
-- +goose StatementEnd
CALL correct_daum_identity_00151();
DROP PROCEDURE correct_daum_identity_00151;

-- +goose Down
DROP PROCEDURE IF EXISTS rollback_daum_identity_00151;
-- +goose StatementBegin
CREATE PROCEDURE rollback_daum_identity_00151()
BEGIN
    IF EXISTS (SELECT 1 FROM daum_resources WHERE kakao_email <> '' OR failure_category <> '') THEN
        SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'Daum account identity or failure facts exist; refusing destructive rollback';
    END IF;
    ALTER TABLE daum_resources DROP CHECK chk_daum_failure_category, DROP COLUMN failure_category, DROP COLUMN kakao_email;
END;
-- +goose StatementEnd
CALL rollback_daum_identity_00151();
DROP PROCEDURE rollback_daum_identity_00151;
