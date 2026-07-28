import { describe, expect, it } from "vitest";

import {
  calculateMembershipProgress,
  formatPriceMultiplier,
  normalizePriceMultiplier,
  priceMultiplier,
  type MembershipGroup,
} from "./membership";

const group = (
  id: number,
  threshold: string,
  autoUpgradeEnabled = true,
): MembershipGroup => ({
  id,
  code: `vip${id}`,
  name: `VIP ${id}`,
  description: "",
  enabled: true,
  apiConcurrencyLimit: id * 5,
  priceDiscountRatio: "0.90",
  topupThreshold: threshold,
  autoUpgradeEnabled,
});

describe("membership price multiplier", () => {
  it("normalizes and formats valid ratios", () => {
    expect(normalizePriceMultiplier("0.900000")).toBe("0.900000");
    expect(priceMultiplier("0.900000")).toBe(0.9);
    expect(formatPriceMultiplier("0.900000")).toBe("0.9×");
    expect(formatPriceMultiplier("0")).toBe("0×");
  });

  it.each([undefined, "", "-0.1", "1.000001", "nope"])(
    "falls back to standard pricing for %s",
    (value) => {
      expect(normalizePriceMultiplier(value)).toBe("1");
      expect(formatPriceMultiplier(value)).toBe("1×");
    },
  );
});

describe("membership progress", () => {
  it("uses the next enabled automatic tier and the current tier threshold as its baseline", () => {
    const current = group(1, "100.00");
    const result = calculateMembershipProgress(
      current,
      [current, group(2, "500.00"), { ...group(3, "200.00"), enabled: false }],
      "250.00",
    );

    expect(result).toMatchObject({
      nextGroup: { id: 2 },
      hasHigherGroup: true,
      remaining: "250.00",
      percent: 37.5,
    });
    expect(calculateMembershipProgress(group(2, "500.00"), [current], "800.00"))
      .toEqual({ hasHigherGroup: false, nextGroup: null, percent: 100, remaining: "0.00" });
  });

  it("keeps unavailable totals distinct from zero and preserves ledger precision", () => {
    const current = group(1, "100.123456");
    const next = group(2, "100.123458");

    expect(calculateMembershipProgress(current, [current, next], undefined)).toEqual({
      hasHigherGroup: true,
      nextGroup: next,
      percent: null,
      remaining: null,
    });
    expect(calculateMembershipProgress(current, [current, next], "100.123457")).toMatchObject({
      percent: 50,
      remaining: "0.000001",
    });
    expect(calculateMembershipProgress(current, [current, next], "100.123458")).toMatchObject({
      percent: 100,
      remaining: "0.00",
    });
  });

  it("distinguishes a manual higher tier from the highest tier", () => {
    const current = group(1, "100.00");
    const manual = group(2, "500.00", false);

    expect(calculateMembershipProgress(current, [current, manual], "200.00")).toEqual({
      hasHigherGroup: true,
      nextGroup: null,
      percent: 100,
      remaining: "0.00",
    });
  });
});
