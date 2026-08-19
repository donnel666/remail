import { describe, expect, it, vi } from "vitest";
// @ts-expect-error -- this source-contract check runs in Node without Node types.
import { readFileSync } from "node:fs";

import type { ProjectItem } from "@/lib/projects-api";

import { loadAllProjects } from "./upstream-settings-values";

const upstreamsSource = readFileSync(new URL("./upstreams.tsx", import.meta.url), "utf8");

function project(id: number): ProjectItem {
  return {
    id,
    name: `Project ${id}`,
    targetPlatform: "test",
    status: "listed",
    accessType: "public",
    looseMatch: false,
    productCount: 1,
    mailRuleCount: 0,
    supportsDotAlias: false,
    supportsPlusAlias: false,
    createdAt: "2026-08-03T00:00:00Z",
    updatedAt: "2026-08-03T00:00:00Z",
  };
}

describe("loadAllProjects", () => {
  it("loads every page with the project API maximum page size", async () => {
    const projects = Array.from({ length: 205 }, (_, index) => project(index + 1));
    const fetchPage = vi.fn(async (offset: number, limit: number) => ({
      items: projects.slice(offset, offset + limit),
      total: projects.length,
      offset,
      limit,
    }));

    await expect(loadAllProjects(fetchPage)).resolves.toHaveLength(205);
    expect(fetchPage.mock.calls).toEqual([[0, 100], [100, 100], [200, 100]]);
  });
});

describe("Kitesim upstream money commands", () => {
  it("prefills the script-compatible billing profile", () => {
    expect(upstreamsSource).toContain('firstName: "noreal"');
    expect(upstreamsSource).toContain('lastName: "name"');
    expect(upstreamsSource).toContain('phone: "6505438765"');
    expect(upstreamsSource).toContain('address: "1295 Charleston Rd"');
  });

  it("passes the selected purchase price as the command ceiling", () => {
    expect(upstreamsSource).toContain("selectedProduct.buyPrice");
    expect(upstreamsSource).toContain("purchaseKitesimNumbers(");
  });

  it("preserves an edited system account while background refreshes run", () => {
    expect(upstreamsSource).toContain("if (!accountEditedRef.current) setAccountId(next.accountId || 0);");
    expect(upstreamsSource).toContain("accountEditedRef.current = nextAccountId !== (upstream?.accountId ?? 0);");
    expect(upstreamsSource).toContain("accountEditedRef.current = false;");
  });
});
