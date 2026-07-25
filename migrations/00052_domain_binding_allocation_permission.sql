-- +goose Up

ALTER TABLE domain_resources
    ADD COLUMN allow_new_bindings TINYINT(1) NOT NULL DEFAULT 0 AFTER purpose,
    ADD CONSTRAINT chk_domain_resources_allow_new_bindings
        CHECK (allow_new_bindings = 0 OR purpose = 'binding');

-- +goose Down

ALTER TABLE domain_resources
    DROP CHECK chk_domain_resources_allow_new_bindings,
    DROP COLUMN allow_new_bindings;
