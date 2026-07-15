import { describe, expect, it } from "vitest";

import { getAdminDomainCapabilities } from "./admin-domain-access";

describe("admin domain operation access", () => {
  it("keeps write and operate capabilities independent", () => {
    expect(getAdminDomainCapabilities(["core:resource:read"])).toEqual({
      canOperateDomains: false,
      canWriteDomains: false,
    });
    expect(getAdminDomainCapabilities(["core:resource:write"])).toEqual({
      canOperateDomains: false,
      canWriteDomains: true,
    });
    expect(getAdminDomainCapabilities(["core:resource:operate"])).toEqual({
      canOperateDomains: true,
      canWriteDomains: false,
    });
    expect(
      getAdminDomainCapabilities([
        "core:resource:read",
        "core:resource:write",
        "core:resource:operate",
      ])
    ).toEqual({
      canOperateDomains: true,
      canWriteDomains: true,
    });
  });
});
