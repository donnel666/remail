-- +goose Up

-- System settings are an administrator resource. Keep the sensitive action in
-- the catalog for future per-setting guards, while normal CRUD is available to
-- both administrator roles like the existing admin configuration resources.
INSERT INTO casbin_rule (ptype, v0, v1, v2, v3)
SELECT 'p', policy.role_name, 'system:settings', policy.action_name, 'allow'
FROM (
    SELECT 'role:admin' AS role_name, 'read' AS action_name
    UNION ALL SELECT 'role:admin', 'write'
    UNION ALL SELECT 'role:super_admin', 'read'
    UNION ALL SELECT 'role:super_admin', 'write'
    UNION ALL SELECT 'role:super_admin', 'sensitive'
) AS policy
WHERE NOT EXISTS (
    SELECT 1
    FROM casbin_rule existing
    WHERE existing.ptype = 'p'
      AND existing.v0 = policy.role_name
      AND existing.v1 = 'system:settings'
      AND existing.v2 = policy.action_name
      AND existing.v3 = 'allow'
);

-- +goose Down

DELETE FROM casbin_rule
WHERE ptype = 'p'
  AND v1 = 'system:settings'
  AND v0 IN ('role:admin', 'role:super_admin');
