-- +goose Up

ALTER TABLE microsoft_resources
    MODIFY COLUMN alloc_bucket SMALLINT UNSIGNED NOT NULL DEFAULT 0;

ALTER TABLE domain_resources
    MODIFY COLUMN alloc_bucket SMALLINT UNSIGNED NOT NULL DEFAULT 0;

ALTER TABLE microsoft_routing_candidates
    MODIFY COLUMN alloc_bucket SMALLINT UNSIGNED NOT NULL DEFAULT 0;

ALTER TABLE domain_routing_candidates
    MODIFY COLUMN alloc_bucket SMALLINT UNSIGNED NOT NULL DEFAULT 0;

ALTER TABLE generated_mailboxes
    ADD COLUMN alloc_bucket SMALLINT UNSIGNED NOT NULL DEFAULT 0 AFTER status;

UPDATE microsoft_resources
SET alloc_bucket = MOD(id, 2048)
WHERE alloc_bucket <> MOD(id, 2048);

UPDATE microsoft_routing_candidates
SET alloc_bucket = MOD(resource_id, 2048)
WHERE alloc_bucket <> MOD(resource_id, 2048);

UPDATE domain_resources
SET alloc_bucket = MOD(id, 512)
WHERE alloc_bucket <> MOD(id, 512);

UPDATE domain_routing_candidates
SET alloc_bucket = MOD(resource_id, 512)
WHERE alloc_bucket <> MOD(resource_id, 512);

UPDATE generated_mailboxes
SET alloc_bucket = MOD(CRC32(LOWER(TRIM(email))), 2048)
WHERE alloc_bucket <> MOD(CRC32(LOWER(TRIM(email))), 2048);

ALTER TABLE generated_mailboxes
    ADD INDEX idx_generated_mailboxes_bucket_reuse (alloc_bucket, status, last_allocated_at, id, resource_id);

-- +goose Down

UPDATE microsoft_resources
SET alloc_bucket = MOD(id, 64)
WHERE alloc_bucket <> MOD(id, 64);

UPDATE microsoft_routing_candidates
SET alloc_bucket = MOD(resource_id, 64)
WHERE alloc_bucket <> MOD(resource_id, 64);

UPDATE domain_resources
SET alloc_bucket = MOD(id, 64)
WHERE alloc_bucket <> MOD(id, 64);

UPDATE domain_routing_candidates
SET alloc_bucket = MOD(resource_id, 64)
WHERE alloc_bucket <> MOD(resource_id, 64);

ALTER TABLE microsoft_resources
    MODIFY COLUMN alloc_bucket TINYINT UNSIGNED NOT NULL DEFAULT 0;

ALTER TABLE domain_resources
    MODIFY COLUMN alloc_bucket TINYINT UNSIGNED NOT NULL DEFAULT 0;

ALTER TABLE microsoft_routing_candidates
    MODIFY COLUMN alloc_bucket TINYINT UNSIGNED NOT NULL DEFAULT 0;

ALTER TABLE domain_routing_candidates
    MODIFY COLUMN alloc_bucket TINYINT UNSIGNED NOT NULL DEFAULT 0;

ALTER TABLE generated_mailboxes
    DROP INDEX idx_generated_mailboxes_bucket_reuse,
    DROP COLUMN alloc_bucket;
