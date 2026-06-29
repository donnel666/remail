-- +goose Up
-- +goose StatementBegin
SELECT 'P1-I0 skeleton migration applied';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
SELECT 'P1-I0 skeleton migration rolled back';
-- +goose StatementEnd
