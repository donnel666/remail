-- +goose Up

-- Allocation anti-joins materialize the small pending/processing set. Keep
-- email_resource_id adjacent so the materialized lookup stays index-only.
ALTER TABLE mailmatch_resource_fetch_states
    ADD INDEX idx_mailmatch_fetch_state_blocking
        (status, operation_kind, email_resource_id),
    ALGORITHM=INPLACE,
    LOCK=NONE;

-- +goose Down

ALTER TABLE mailmatch_resource_fetch_states
    DROP INDEX idx_mailmatch_fetch_state_blocking,
    ALGORITHM=INPLACE,
    LOCK=NONE;
