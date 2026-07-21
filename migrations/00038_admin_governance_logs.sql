-- +goose Up

ALTER TABLE operation_logs
    ADD INDEX idx_operation_logs_created (created_at, id);

INSERT INTO casbin_rule (ptype, v0, v1, v2, v3)
SELECT 'p', policy.role_name, 'governance:log', policy.action_name, 'allow'
FROM (
    SELECT 'role:admin' AS role_name, 'read' AS action_name
    UNION ALL SELECT 'role:super_admin', 'read'
    UNION ALL SELECT 'role:super_admin', 'operate'
) AS policy
WHERE NOT EXISTS (
    SELECT 1
    FROM casbin_rule existing
    WHERE existing.ptype = 'p'
      AND existing.v0 = policy.role_name
      AND existing.v1 = 'governance:log'
      AND existing.v2 = policy.action_name
      AND existing.v3 = 'allow'
);

-- +goose Down

DELETE FROM casbin_rule
WHERE ptype = 'p'
  AND v0 IN ('role:admin', 'role:super_admin')
  AND v1 = 'governance:log'
  AND v2 IN ('read', 'operate')
  AND v3 = 'allow';

ALTER TABLE operation_logs
    DROP INDEX idx_operation_logs_created;
