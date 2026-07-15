import { describe, expect, it } from "vitest";

import {
  canMutateAdminUser,
  getAdminUserCapabilities,
} from "./admin-user-access";

describe("admin user operation access", () => {
  it("maps the existing permission keys to independent UI capabilities", () => {
    expect(getAdminUserCapabilities(["iam:user:read"])).toEqual({
      canAdjustBalance: false,
      canAssignSuperAdmin: false,
      canEditPermissions: false,
      canManageApiKeys: false,
      canOperateUsers: false,
      canWriteUsers: false,
    });

    expect(
      getAdminUserCapabilities([
        "iam:user:write",
        "iam:user:operate",
        "iam:permission:write",
        "iam:permission:sensitive",
        "billing:wallet:operate",
      ])
    ).toEqual({
      canAdjustBalance: true,
      canAssignSuperAdmin: true,
      canEditPermissions: true,
      canManageApiKeys: true,
      canOperateUsers: true,
      canWriteUsers: true,
    });
  });

  it("protects super administrators from every mutation capability", () => {
    expect(canMutateAdminUser("super_admin", true)).toBe(false);
    expect(canMutateAdminUser("admin", true)).toBe(true);
    expect(canMutateAdminUser("user", false)).toBe(false);
  });
});
