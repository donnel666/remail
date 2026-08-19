-- +goose Up

INSERT INTO casbin_rule (ptype, v0, v1, v2, v3)
SELECT 'p', policy.role_name, 'lottery:lottery', policy.action_name, 'allow'
FROM (
    SELECT 'role:admin' AS role_name, 'read' AS action_name
    UNION ALL SELECT 'role:admin', 'write'
    UNION ALL SELECT 'role:admin', 'operate'
    UNION ALL SELECT 'role:super_admin', 'read'
    UNION ALL SELECT 'role:super_admin', 'write'
    UNION ALL SELECT 'role:super_admin', 'operate'
) AS policy
WHERE NOT EXISTS (
    SELECT 1
    FROM casbin_rule existing
    WHERE existing.ptype = 'p'
      AND existing.v0 = policy.role_name
      AND existing.v1 = 'lottery:lottery'
      AND existing.v2 = policy.action_name
      AND existing.v3 = 'allow'
);

-- +goose Down

-- Keep additive role permissions on rollback for the same reason as the
-- existing permission-baseline migrations: manually granted policies may
-- share these values.
SELECT 1;
