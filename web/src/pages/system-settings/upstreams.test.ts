import { describe, expect, it } from "vitest";

import { buildSMSBowerSettingsUpdates } from "./upstream-settings-values";

const form = {
  smsbower_api_key: "",
  smsbower_balance_warning_threshold: 5,
  smsbower_enabled: true,
  smsbower_min_margin_percent: 20,
  smsbower_points_per_unit: 2,
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
});
