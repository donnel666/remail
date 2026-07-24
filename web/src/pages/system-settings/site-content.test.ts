import { describe, expect, it } from "vitest";

import { parseSettingsList } from "@/lib/settings-api-mock";

describe("parseSettingsList", () => {
  it("accepts arrays and safely ignores invalid setting values", () => {
    expect(parseSettingsList<{ id: number }>('[{"id":1}]')).toEqual([{ id: 1 }]);
    expect(parseSettingsList("{}")).toEqual([]);
    expect(parseSettingsList("broken")).toEqual([]);
  });
});
