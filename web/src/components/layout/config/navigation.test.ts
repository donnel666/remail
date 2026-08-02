import { describe, expect, it } from "vitest";

import {
  getSidebarRouteRequiredPermissions,
  getVisibleSidebarNavGroups,
} from "./navigation";

function visiblePaths(permissions: string[]) {
  return getVisibleSidebarNavGroups(permissions).flatMap((group) =>
    group.items.map((item) => item.path)
  );
}

describe("admin navigation permissions", () => {
  it("shows the personal finance center without admin permissions", () => {
    expect(visiblePaths([])).toContain("/finance");
    expect(getSidebarRouteRequiredPermissions("/finance")).toEqual([]);
  });

  it("requires every platform-dashboard permission", () => {
    expect(
      visiblePaths([
        "iam:user:read",
        "core:resource:read",
        "billing:wallet:read",
      ])
    ).toContain("/admin/dashboard");

    expect(
      visiblePaths(["iam:user:read", "core:resource:read"])
    ).not.toContain("/admin/dashboard");
  });

  it("keeps finance hidden without wallet read permission", () => {
    expect(visiblePaths([])).not.toContain("/admin/finance");
    expect(visiblePaths(["billing:wallet:read"])).toContain("/admin/finance");
  });

  it("guards the Gmail resource page with resource read permission", () => {
    expect(visiblePaths([])).not.toContain("/admin/gmail");
    expect(visiblePaths(["core:resource:read"])).toContain("/admin/gmail");
    expect(getSidebarRouteRequiredPermissions("/admin/gmail")).toEqual([
      "core:resource:read",
    ]);
  });

  it("guards system monitoring with diagnostics read permission", () => {
    expect(visiblePaths([])).not.toContain("/admin/monitoring");
    expect(visiblePaths(["governance:log:read"])).toContain("/admin/monitoring");
    expect(getSidebarRouteRequiredPermissions("/admin/monitoring")).toEqual([
      "governance:log:read",
    ]);
  });

  it("returns the full all-of requirement for route guards", () => {
    expect(getSidebarRouteRequiredPermissions("/admin/dashboard")).toEqual([
      "iam:user:read",
      "core:resource:read",
      "billing:wallet:read",
    ]);
    expect(getSidebarRouteRequiredPermissions("/admin/finance")).toEqual([
      "billing:wallet:read",
    ]);
  });
});
