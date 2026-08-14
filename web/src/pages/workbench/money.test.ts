import { describe, expect, it } from "vitest";

import {
  calculateDiscountedLedgerTotal,
  formatCompactNumber,
  formatMoney,
  formatMoneyExact,
} from "./money";

describe("calculateDiscountedLedgerTotal", () => {
  it("rounds each discounted order to ledger precision before totaling a batch", () => {
    expect(calculateDiscountedLedgerTotal(0.000003, 0.5, 2)).toBe(0.000004);
    expect(calculateDiscountedLedgerTotal(0.000005, 0.5, 2)).toBe(0.000004);
  });
});

describe("formatCompactNumber", () => {
  it.each<[number, string]>([
    [38, "38"],
    [1_000, "1K"],
    [100_000, "100K"],
    [1_000_000, "1M"],
  ])("formats %s compactly", (value, expected) => {
    expect(formatCompactNumber(value)).toBe(expected);
  });
});

describe("formatMoney", () => {
  it.each<[number, string]>([
    [0, "0"],
    [0.008, "0.008"],
    [0.01, "0.01"],
    [1, "1"],
    [1.2, "1.2"],
    [1.2346, "1.2346"],
    [1_000, "1K"],
    [100_000, "100K"],
    [1_000_000, "1M"],
    [1_000_000_000, "1B"],
  ])("formats %s for display", (value, expected) => {
    expect(formatMoney(value)).toBe(expected);
  });

  it("falls back to zero for a non-finite value", () => {
    expect(formatMoney(Number.NaN)).toBe("0");
  });
});

describe("formatMoneyExact", () => {
  it("keeps the unabridged value for compact amount tooltips", () => {
    expect(formatMoneyExact(1_234.567)).toBe("1234.567");
  });
});
