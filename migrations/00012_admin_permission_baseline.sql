-- +goose Up

-- Keep the role baseline aligned with the permission contract returned by
-- /v1/me. This migration is intentionally additive so databases that already
-- ran the original consolidated migration receive the newer actions too.
INSERT INTO casbin_rule (ptype, v0, v1, v2, v3)
SELECT 'p', policy.role_name, policy.resource_name, policy.action_name, 'allow'
FROM (
    SELECT 'role:admin' AS role_name, 'trade:order' AS resource_name, 'write' AS action_name
    UNION ALL SELECT 'role:admin', 'billing:wallet', 'operate'
    UNION ALL SELECT 'role:super_admin', 'iam:permission', 'sensitive'
    UNION ALL SELECT 'role:super_admin', 'trade:order', 'write'
    UNION ALL SELECT 'role:super_admin', 'billing:wallet', 'operate'
    UNION ALL SELECT 'role:super_admin', 'billing:wallet', 'sensitive'
    UNION ALL SELECT 'role:super_admin', 'billing:card', 'sensitive'
) AS policy
WHERE NOT EXISTS (
    SELECT 1
    FROM casbin_rule existing
    WHERE existing.ptype = 'p'
      AND existing.v0 = policy.role_name
      AND existing.v1 = policy.resource_name
      AND existing.v2 = policy.action_name
      AND existing.v3 = 'allow'
);

-- +goose Down

-- Intentionally retain additive role permissions on rollback. The Up migration
-- skips policies that already existed, so deleting by value here could remove
-- administrator policies that were created manually before this migration.
SELECT 1;
