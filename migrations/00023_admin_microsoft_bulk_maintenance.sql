-- +goose Up

ALTER TABLE admin_resource_bulk_commands
    DROP CHECK chk_admin_resource_bulk_action,
    ADD CONSTRAINT chk_admin_resource_bulk_action
        CHECK (action IN ('validate', 'alias', 'history', 'token', 'publish', 'unpublish', 'delete'));

-- +goose Down

DELETE FROM admin_resource_bulk_commands
WHERE action IN ('alias', 'history', 'token');

ALTER TABLE admin_resource_bulk_commands
    DROP CHECK chk_admin_resource_bulk_action,
    ADD CONSTRAINT chk_admin_resource_bulk_action
        CHECK (action IN ('validate', 'publish', 'unpublish', 'delete'));
