-- +goose Up

-- The administrator Microsoft Orders Tab pages allocation history by one
-- resource in stable newest-first order. The existing resource/status index
-- protects active-allocation guards but cannot satisfy this history scan.
ALTER TABLE microsoft_allocations
    ADD INDEX idx_ms_alloc_resource_created_id (resource_id, created_at, id);

-- +goose Down

ALTER TABLE microsoft_allocations
    DROP INDEX idx_ms_alloc_resource_created_id;
