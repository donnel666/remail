import { describe, expect, it } from "vitest";

import { compareDomainSuffixes, compareProjectNames } from "./utils";

describe("compareProjectNames", () => {
  it("places names containing Chinese characters after non-Chinese names", () => {
    expect(compareProjectNames("中文项目", "Zulu")).toBeGreaterThan(0);
    expect(compareProjectNames("Mail 中文", "Zulu")).toBeGreaterThan(0);
    expect(compareProjectNames("Alpha", "Beta")).toBeLessThan(0);
  });
});

describe("domain suffix selections", () => {
  it("sorts displayed public suffixes", () => {
    const values = ["@net", "@com.cn", "@com"];
    expect(values.sort(compareDomainSuffixes)).toEqual([
      "@com",
      "@com.cn",
      "@net",
    ]);
  });
});
