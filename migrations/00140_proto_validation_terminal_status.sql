-- +goose Up

-- Reconcile only stopped validations for the current credentials/generation.
-- Retrying resources and authoritative normal/disabled/deleted states stay put.
UPDATE proto_resources AS resource
JOIN email_resources AS root ON root.id = resource.id AND root.type = 'proto'
SET resource.status = 'abnormal',
    resource.version = resource.version + 1,
    resource.updated_at = CURRENT_TIMESTAMP(3),
    root.version = root.version + 1,
    root.updated_at = CURRENT_TIMESTAMP(3)
WHERE resource.status = 'pending'
  AND EXISTS (
      SELECT 1 FROM proto_maintenance_runs AS validation_run
      WHERE validation_run.resource_id = resource.id
        AND validation_run.validation_generation = resource.validation_generation
        AND validation_run.credential_revision = resource.credential_revision
        AND validation_run.kind = 'validation'
        AND validation_run.status IN ('failed', 'uncertain')
  );

-- +goose Down

-- Keep confirmed terminal states and their history on an image rollback.
-- An explicit validation command can start a new generation.
