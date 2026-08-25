-- +goose Up

-- Orders created before reconciliation snapshots were introduced cannot be
-- queried safely: the provider, merchant scope, and credentials at creation
-- time are unknowable. Quarantine only still-pending rows; credited/failed
-- history is left untouched. The explicit reason makes these rows visible to
-- operators for manual payment verification.
UPDATE recharges
SET status = 'failed',
    failure_reason = 'migration_missing_gateway_snapshot',
    query_lease_until = NULL,
    reconciled_at = COALESCE(reconciled_at, UTC_TIMESTAMP()),
    updated_at = UTC_TIMESTAMP()
WHERE status IN ('paying', 'callback', 'reconciled')
  AND (
      gateway_config_snapshot IS NULL
      OR TRIM(gateway_config_snapshot) = ''
      OR TRIM(gateway_config_snapshot) IN ('{}', 'null')
      OR CASE
           WHEN JSON_VALID(gateway_config_snapshot) = 0 THEN 1
           WHEN JSON_TYPE(gateway_config_snapshot) <> 'OBJECT' THEN 1
           WHEN NOT (
               (
                   COALESCE(JSON_TYPE(JSON_EXTRACT(gateway_config_snapshot, '$.GatewayURL')), '') = 'STRING'
                   AND COALESCE(TRIM(JSON_UNQUOTE(JSON_EXTRACT(gateway_config_snapshot, '$.GatewayURL'))), '') <> ''
               )
               OR (
                   COALESCE(JSON_TYPE(JSON_EXTRACT(gateway_config_snapshot, '$.EpusdtGatewayURL')), '') = 'STRING'
                   AND COALESCE(TRIM(JSON_UNQUOTE(JSON_EXTRACT(gateway_config_snapshot, '$.EpusdtGatewayURL'))), '') <> ''
               )
           ) THEN 1
           ELSE 0
         END = 1
  );

-- +goose Down

-- The previous status/configuration is not recoverable from this migration;
-- do not reactivate orders with an unknown gateway configuration.
SELECT 'recharge snapshot quarantine is intentionally irreversible';
