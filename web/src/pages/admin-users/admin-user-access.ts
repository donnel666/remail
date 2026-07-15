import type { AdminUserRole } from "./admin-users-mock";

export interface AdminUserCapabilities {
  canAdjustBalance: boolean;
  canAssignSuperAdmin: boolean;
  canEditPermissions: boolean;
  canManageApiKeys: boolean;
  canOperateUsers: boolean;
  canWriteUsers: boolean;
}

export function getAdminUserCapabilities(
  permissions: readonly string[]
): AdminUserCapabilities {
  const permissionSet = new Set(permissions);
  return {
    canAdjustBalance: permissionSet.has("billing:wallet:operate"),
    canAssignSuperAdmin: permissionSet.has("iam:permission:sensitive"),
    canEditPermissions: permissionSet.has("iam:permission:write"),
    canManageApiKeys: permissionSet.has("iam:user:operate"),
    canOperateUsers: permissionSet.has("iam:user:operate"),
    canWriteUsers: permissionSet.has("iam:user:write"),
  };
}

export function canMutateAdminUser(
  role: AdminUserRole,
  capability: boolean
) {
  return capability && role !== "super_admin";
}
