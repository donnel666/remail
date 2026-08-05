import { describe, expect, it, vi } from "vitest";

import type { ProjectItem } from "@/lib/projects-api";

import { loadAllProjects } from "./upstream-settings-values";

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
