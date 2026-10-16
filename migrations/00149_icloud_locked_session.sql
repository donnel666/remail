-- +goose Up
ALTER TABLE icloud_resource_channels
    DROP CHECK chk_icloud_channels_session,
    ADD CONSTRAINT chk_icloud_channels_session CHECK (session_status IN ('unchecked', 'valid', 'invalid', 'locked'));

-- +goose Down
ALTER TABLE icloud_resource_channels
    DROP CHECK chk_icloud_channels_session,
    ADD CONSTRAINT chk_icloud_channels_session CHECK (session_status IN ('unchecked', 'valid', 'invalid'));
