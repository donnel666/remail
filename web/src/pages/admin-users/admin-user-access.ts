import type { AdminUserRole } from "./admin-users-api";

export interface AdminUserCapabilities {
  canAdjustBalance: boolean;
  canAssignSuperAdmin: boolean;
  canEditSuperAdminGroup: boolean;
  canEditPermissions: boolean;
  canManageApiKeys: boolean;
  canOperateUsers: boolean;
  canReadMetrics: boolean;
  canWriteUsers: boolean;
}

export function getAdminUserCapabilities(
  permissions: readonly string[],
  operatorRole: AdminUserRole | undefined
): AdminUserCapabilities {
  const permissionSet = new Set(permissions);
  return {
    canAdjustBalance: permissionSet.has("billing:wallet:operate"),
    canAssignSuperAdmin: permissionSet.has("iam:permission:sensitive"),
    canEditSuperAdminGroup:
      operatorRole === "super_admin" &&
      permissionSet.has("iam:user:write") &&
      permissionSet.has("iam:permission:sensitive"),
    canEditPermissions: permissionSet.has("iam:permission:write"),
    canManageApiKeys: permissionSet.has("iam:user:operate"),
    canOperateUsers: permissionSet.has("iam:user:operate"),
    canReadMetrics: permissionSet.has("billing:wallet:read"),
    canWriteUsers: permissionSet.has("iam:user:write"),
  };
}

export function canMutateAdminUser(
  role: AdminUserRole,
  capability: boolean
) {
  return capability && role !== "super_admin";
}

export function canEditAdminUserGroup(
  role: AdminUserRole,
  capabilities: AdminUserCapabilities
) {
  return (
    capabilities.canWriteUsers &&
    (role !== "super_admin" || capabilities.canEditSuperAdminGroup)
  );
}
