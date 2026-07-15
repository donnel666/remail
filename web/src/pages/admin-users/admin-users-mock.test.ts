import { describe, expect, it } from "vitest";

import {
  getMockAdminUser,
  getMockAdminUserInvitations,
  getMockAdminUserWallet,
  listMockAdminUsers,
} from "./admin-users-mock";

describe("admin user detail mock", () => {
  it("provides withdrawable balances for supplier-level roles and above", async () => {
    const [superAdmin, admin, supplier] = await Promise.all([
      getMockAdminUser(1),
      getMockAdminUser(2),
      getMockAdminUser(6),
    ]);
    const wallets = await Promise.all(
      [superAdmin, admin, supplier].map((user) =>
        getMockAdminUserWallet(user.id)
      )
    );

    expect([superAdmin.role, admin.role, supplier.role]).toEqual([
      "super_admin",
      "admin",
      "supplier",
    ]);
    expect(
      wallets.every((wallet) => Number(wallet.supplierAvailable) >= 0)
    ).toBe(true);
  });

  it("returns both inviter and direct invitees", async () => {
    const adminInvitations = await getMockAdminUserInvitations(2);
    const supplierInvitations = await getMockAdminUserInvitations(6);

    expect(adminInvitations.inviter?.id).toBe(1);
    expect(adminInvitations.invitees.length).toBeGreaterThan(0);
    expect(
      adminInvitations.invitees.every((member) => member.role === "supplier")
    ).toBe(true);
    expect(supplierInvitations.inviter?.role).toBe("admin");
    expect(supplierInvitations.invitees.length).toBeGreaterThan(0);
  });

  it("provides paged demo invitees for users without stored referrals", async () => {
    const invitations = await getMockAdminUserInvitations(83);

    expect(invitations.inviter).not.toBeNull();
    expect(invitations.invitees.length).toBeGreaterThan(10);
    expect(
      invitations.invitees.every((member) => member.id >= 100_000)
    ).toBe(true);
  });

  it("calculates each facet from the active filters outside its own dimension", async () => {
    const [allUsers, disabledUsers, disabledRegularUsers] = await Promise.all([
      listMockAdminUsers({}, 0, 1_000),
      listMockAdminUsers({ enabled: false }, 0, 1_000),
      listMockAdminUsers({ enabled: false, role: "user" }, 0, 1_000),
    ]);

    expect(disabledUsers.facets.status).toEqual(allUsers.facets.status);
    expect(disabledUsers.facets.role.all).toBe(disabledUsers.total);
    expect(disabledRegularUsers.facets.role.all).toBe(
      allUsers.facets.status.disabled
    );
    expect(disabledRegularUsers.facets.role.user).toBe(
      disabledRegularUsers.total
    );
    expect(disabledRegularUsers.facets.status.disabled).toBe(
      disabledRegularUsers.total
    );
    expect(disabledRegularUsers.facets.status.all).toBe(
      allUsers.facets.role.user
    );
  });

  it("applies search and cross-dimension filters to facet counts", async () => {
    const allUsers = await listMockAdminUsers({}, 0, 1_000);
    const target = allUsers.users.find((user) => user.role !== "super_admin");
    expect(target).toBeDefined();

    const searched = await listMockAdminUsers(
      {
        search: target!.email,
        userGroupId: target!.userGroup.id,
      },
      0,
      1_000
    );

    expect(searched.total).toBe(1);
    expect(searched.facets.role.all).toBe(1);
    expect(searched.facets.status.all).toBe(1);
    expect(
      searched.facets.group.reduce((sum, group) => sum + group.count, 0)
    ).toBe(1);
  });
});
