import { describe, expect, it } from "vitest";

import { formatPoints, formatPointsValue, normalizePointValue } from "./points";

describe("points formatting", () => {
  it("keeps six-decimal point values readable without a currency symbol", () => {
    expect(formatPointsValue("0.010000")).toBe("0.01");
    expect(formatPointsValue("6.400000")).toBe("6.4");
    expect(formatPoints("10000.000000", "积分")).toBe("10,000 积分");
    expect(formatPointsValue("")).toBe("—");
    expect(normalizePointValue(0.01)).toBe("0.01");
  });
});
