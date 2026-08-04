import { describe, expect, it, vi } from "vitest";

import type { ProjectItem } from "@/lib/projects-api";

import {
  buildSMSBowerSettingsUpdates,
  loadAllProjects,
} from "./upstream-settings-values";

const form = {
  smsbower_api_key: "",
  smsbower_balance_warning_threshold: 5,
  smsbower_code_enabled: true,
  smsbower_enabled: true,
  smsbower_min_margin_percent: 20,
  smsbower_points_per_unit: 2,
  smsbower_purchase_enabled: false,
  smsbower_sync_interval_minutes: 10,
};

describe("buildSMSBowerSettingsUpdates", () => {
  it("keeps the stored API key when the password field is blank", () => {
    expect(buildSMSBowerSettingsUpdates(form, true)).not.toContainEqual(
      expect.objectContaining({ key: "smsbower_api_key" }),
    );
  });

  it("trims and saves a newly entered API key", () => {
    expect(buildSMSBowerSettingsUpdates({ ...form, smsbower_api_key: "  secret  " }, true))
      .toContainEqual({ key: "smsbower_api_key", value: "secret" });
  });

  it("saves the global code and purchase participation switches", () => {
    expect(buildSMSBowerSettingsUpdates(form, false)).toEqual(expect.arrayContaining([
      { key: "smsbower_code_enabled", value: "true" },
      { key: "smsbower_purchase_enabled", value: "false" },
    ]));
  });
});

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
