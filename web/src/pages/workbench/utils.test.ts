import { describe, expect, it } from "vitest";

import {
  compareDomainSuffixes,
  compareProjectNames,
  isPrivateDomainSelection,
} from "./utils";

describe("compareProjectNames", () => {
  it("places names containing Chinese characters after non-Chinese names", () => {
    expect(compareProjectNames("中文项目", "Zulu")).toBeGreaterThan(0);
    expect(compareProjectNames("Mail 中文", "Zulu")).toBeGreaterThan(0);
    expect(compareProjectNames("Alpha", "Beta")).toBeLessThan(0);
  });
});

describe("domain suffix selections", () => {
  it("places private full emails before displayed public suffixes", () => {
    const values = [
      "@net",
      "z@example.com",
      "a@example.com",
      "@com",
    ];
    expect(values.sort(compareDomainSuffixes)).toEqual([
      "a@example.com",
      "z@example.com",
      "@com",
      "@net",
    ]);
  });

  it("recognizes only full Domain emails as private", () => {
    expect(isPrivateDomainSelection("domain", "mail@example.com")).toBe(true);
    expect(isPrivateDomainSelection("domain", "com")).toBe(false);
    expect(isPrivateDomainSelection("microsoft", "outlook.com")).toBe(false);
  });
});
