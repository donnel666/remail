-- +goose Up

DROP TABLE IF EXISTS allocation_candidate_refresh_jobs;
DROP TABLE IF EXISTS domain_routing_candidates;
DROP TABLE IF EXISTS microsoft_routing_candidates;

-- +goose Down

-- Intentionally irreversible. Allocation reads directly from Core source tables;
-- rebuilding project-by-resource mirrors would recreate the removed write amplification.
