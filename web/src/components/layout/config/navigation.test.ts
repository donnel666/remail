// @ts-expect-error -- This source-contract check runs in Vitest's Node process.
import { readFileSync } from "node:fs";
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
  it("keeps Proto resource management only in the authorized admin menu", () => {
    expect(visiblePaths([])).not.toContain("/proto");
    expect(visiblePaths([])).not.toContain("/admin/proto");
    expect(visiblePaths([])).toEqual(expect.arrayContaining(["/microsoft", "/dashboard", "/orders"]));
    expect(visiblePaths(["core:resource:read"])).not.toContain("/proto");
    expect(visiblePaths(["core:resource:read"])).toContain("/admin/proto");
    expect(getSidebarRouteRequiredPermissions("/admin/proto")).toEqual(["core:resource:read"]);
  });

  it("does not register or preload the unopened user Proto page", () => {
    const appSource = readFileSync(new URL("../../../App.tsx", import.meta.url), "utf8");
    expect(appSource).not.toContain('import("./pages/ProtoEmails")');
    expect(appSource).not.toContain("protoEmails");
    expect(appSource).not.toMatch(/path:\s*["']\/proto["']/);
    expect(appSource).toMatch(/notFoundComponent:\s*NotFoundPage/);
    expect(appSource).toMatch(/path:\s*["']\/admin\/proto["']/);
  });

  it("labels the admin Proto entry proto.me in both supported languages", () => {
    const entry = getVisibleSidebarNavGroups(["core:resource:read"])
      .flatMap((group) => group.items)
      .find((item) => item.path === "/admin/proto");
    expect(entry?.labelKey).toBe("Admin Proto Emails");
    for (const locale of ["en", "zh"]) {
      const labels = JSON.parse(readFileSync(new URL(`../../../i18n/locales/${locale}.json`, import.meta.url), "utf8"));
      expect(labels[entry!.labelKey]).toBe("proto.me");
    }
  });

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

  it("guards the Kitesim page with resource read permission", () => {
    expect(visiblePaths([])).not.toContain("/admin/kitesim");
    expect(visiblePaths(["core:resource:read"])).toContain("/admin/kitesim");
    expect(getSidebarRouteRequiredPermissions("/admin/kitesim")).toEqual([
      "core:resource:read",
    ]);
  });

  it("guards the iCloud resource page with resource read permission", () => {
    expect(visiblePaths([])).not.toContain("/admin/icloud");
    expect(visiblePaths(["core:resource:read"])).toContain("/admin/icloud");
    expect(getSidebarRouteRequiredPermissions("/admin/icloud")).toEqual([
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
