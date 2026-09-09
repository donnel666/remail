import { describe, expect, it } from "vitest";

import { formatPoints, formatPointsValue, normalizePointValue, sumPointValues } from "./points";

describe("points formatting", () => {
  it("keeps small values precise and abbreviates large values without a unit", () => {
    expect(formatPointsValue("0.010000")).toBe("0.01");
    expect(formatPointsValue("6.400000")).toBe("6.4");
    expect(formatPointsValue("999999999999.999999")).toBe("999,999,999,999.999999");
    expect(formatPoints("999.999999")).toBe("999.999999");
    expect(formatPoints("1000.000000")).toBe("1K");
    expect(formatPoints("1000000.000000")).toBe("1M");
    expect(formatPoints("1000000000.000000")).toBe("1B");
    expect(formatPointsValue("")).toBe("—");
    expect(normalizePointValue(0.01)).toBe("0.01");
  });

  it("sums point amounts without losing six-decimal precision", () => {
    expect(sumPointValues(["999999999999.999999", "0.000001"])).toBe("1000000000000.000000");
    expect(sumPointValues(["0.1", "0.2"])).toBe("0.300000");
    expect(sumPointValues(["-0.008", "0.005"])).toBe("-0.003000");
    expect(sumPointValues([])).toBe("0.000000");
    expect(sumPointValues(["invalid"])).toBe("");
  });
});
