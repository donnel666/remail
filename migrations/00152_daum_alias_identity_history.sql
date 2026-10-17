-- +goose Up
DROP PROCEDURE IF EXISTS preserve_daum_alias_identity_00152;
-- +goose StatementBegin
CREATE PROCEDURE preserve_daum_alias_identity_00152()
BEGIN
    IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = 'daum_explicit_aliases' AND column_name = 'active_email') THEN
        ALTER TABLE daum_explicit_aliases ADD COLUMN active_email VARCHAR(255) GENERATED ALWAYS AS (CASE WHEN status = 'normal' THEN email_address ELSE NULL END) VIRTUAL;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = 'daum_explicit_aliases' AND column_name = 'active_suffix') THEN
        ALTER TABLE daum_explicit_aliases ADD COLUMN active_suffix VARCHAR(255) GENERATED ALWAYS AS (CASE WHEN status = 'normal' THEN email_domain ELSE NULL END) VIRTUAL;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM information_schema.statistics WHERE table_schema = DATABASE() AND table_name = 'daum_explicit_aliases' AND index_name = 'uq_daum_alias_resource_email') THEN
        ALTER TABLE daum_explicit_aliases ADD UNIQUE INDEX uq_daum_alias_resource_email(resource_id, email_address);
    END IF;
    IF NOT EXISTS (SELECT 1 FROM information_schema.statistics WHERE table_schema = DATABASE() AND table_name = 'daum_explicit_aliases' AND index_name = 'uq_daum_alias_active_email') THEN
        ALTER TABLE daum_explicit_aliases ADD UNIQUE INDEX uq_daum_alias_active_email(active_email);
    END IF;
    IF NOT EXISTS (SELECT 1 FROM information_schema.statistics WHERE table_schema = DATABASE() AND table_name = 'daum_explicit_aliases' AND index_name = 'uq_daum_alias_active_suffix') THEN
        ALTER TABLE daum_explicit_aliases ADD UNIQUE INDEX uq_daum_alias_active_suffix(resource_id, active_suffix);
    END IF;
    IF EXISTS (SELECT 1 FROM information_schema.statistics WHERE table_schema = DATABASE() AND table_name = 'daum_explicit_aliases' AND index_name = 'uq_daum_alias_email') THEN
        ALTER TABLE daum_explicit_aliases DROP INDEX uq_daum_alias_email;
    END IF;
    IF EXISTS (SELECT 1 FROM information_schema.statistics WHERE table_schema = DATABASE() AND table_name = 'daum_explicit_aliases' AND index_name = 'uq_daum_alias_suffix') THEN
        ALTER TABLE daum_explicit_aliases DROP INDEX uq_daum_alias_suffix;
    END IF;
END;
-- +goose StatementEnd
CALL preserve_daum_alias_identity_00152();
DROP PROCEDURE preserve_daum_alias_identity_00152;

-- +goose Down
DROP PROCEDURE IF EXISTS rollback_daum_alias_identity_00152;
-- +goose StatementBegin
CREATE PROCEDURE rollback_daum_alias_identity_00152()
BEGIN
    IF EXISTS (SELECT 1 FROM daum_explicit_aliases WHERE status = 'disabled') THEN
        SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'Historical Daum alias identities exist; refusing destructive rollback';
    END IF;
    ALTER TABLE daum_explicit_aliases ADD UNIQUE INDEX uq_daum_alias_email(email_address), ADD UNIQUE INDEX uq_daum_alias_suffix(resource_id, email_domain);
    ALTER TABLE daum_explicit_aliases DROP INDEX uq_daum_alias_active_email, DROP INDEX uq_daum_alias_active_suffix, DROP INDEX uq_daum_alias_resource_email, DROP COLUMN active_email, DROP COLUMN active_suffix;
END;
-- +goose StatementEnd
CALL rollback_daum_alias_identity_00152();
DROP PROCEDURE rollback_daum_alias_identity_00152;
