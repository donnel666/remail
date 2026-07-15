import { describe, expect, it } from "vitest";

import en from "./locales/en.json";
import zh from "./locales/zh.json";

function interpolationNames(value: string) {
  return Array.from(value.matchAll(/{{\s*([^},\s]+)[^}]*}}/g), (match) => match[1]).sort();
}

describe("locale contracts", () => {
  it("keeps the English and Chinese key sets aligned", () => {
    expect(Object.keys(en).sort()).toEqual(Object.keys(zh).sort());
  });

  it("keeps interpolation variables aligned", () => {
    for (const key of Object.keys(en) as Array<keyof typeof en>) {
      expect(interpolationNames(en[key]), key).toEqual(interpolationNames(zh[key]));
    }
  });
});
