import { describe, expect, it } from "vitest";

import { compareProjectNames } from "./utils";

describe("compareProjectNames", () => {
  it("places names containing Chinese characters after non-Chinese names", () => {
    expect(compareProjectNames("中文项目", "Zulu")).toBeGreaterThan(0);
    expect(compareProjectNames("Mail 中文", "Zulu")).toBeGreaterThan(0);
    expect(compareProjectNames("Alpha", "Beta")).toBeLessThan(0);
  });
});
