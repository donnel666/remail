import { describe, expect, it } from "vitest";

import {
  canEditAdminUserGroup,
  canMutateAdminUser,
  getAdminUserCapabilities,
} from "./admin-user-access";

describe("admin user operation access", () => {
  it("maps the existing permission keys to independent UI capabilities", () => {
    expect(getAdminUserCapabilities(["iam:user:read"], "admin")).toEqual({
      canAdjustBalance: false,
      canAssignSuperAdmin: false,
      canEditSuperAdminGroup: false,
      canEditPermissions: false,
      canManageApiKeys: false,
      canOperateUsers: false,
      canReadMetrics: false,
      canWriteUsers: false,
    });

    expect(
      getAdminUserCapabilities([
        "iam:user:write",
        "iam:user:operate",
        "iam:permission:write",
        "iam:permission:sensitive",
        "billing:wallet:read",
        "billing:wallet:operate",
      ], "admin")
    ).toEqual({
      canAdjustBalance: true,
      canAssignSuperAdmin: true,
      canEditSuperAdminGroup: false,
      canEditPermissions: true,
      canManageApiKeys: true,
      canOperateUsers: true,
      canReadMetrics: true,
      canWriteUsers: true,
    });
  });

  it("protects super administrator identity and account mutations", () => {
    expect(canMutateAdminUser("super_admin", true)).toBe(false);
    expect(canMutateAdminUser("admin", true)).toBe(true);
    expect(canMutateAdminUser("user", false)).toBe(false);
  });

  it("requires the super administrator role and permissions to change a protected user's group", () => {
    const ordinary = getAdminUserCapabilities(["iam:user:write"], "admin");
    const sensitiveOnly = getAdminUserCapabilities(["iam:permission:sensitive"], "super_admin");
    const permissions = ["iam:user:write", "iam:permission:sensitive"];
    const sensitive = getAdminUserCapabilities(permissions, "super_admin");
    expect(canEditAdminUserGroup("user", ordinary)).toBe(true);
    expect(canEditAdminUserGroup("super_admin", ordinary)).toBe(false);
    expect(canEditAdminUserGroup("super_admin", sensitiveOnly)).toBe(false);
    expect(canEditAdminUserGroup("super_admin", sensitive)).toBe(true);
    expect(canMutateAdminUser("super_admin", sensitive.canWriteUsers)).toBe(false);
    for (const role of ["admin", "supplier", "user", undefined] as const) {
      const capabilities = getAdminUserCapabilities(permissions, role);
      expect(capabilities.canAssignSuperAdmin).toBe(true);
      expect(canEditAdminUserGroup("super_admin", capabilities)).toBe(false);
      expect(canEditAdminUserGroup("user", capabilities)).toBe(true);
    }
  });
});
