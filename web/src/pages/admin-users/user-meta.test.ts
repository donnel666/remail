import { describe, expect, it } from "vitest";

import { formatMoney } from "./format-money";

describe("formatMoney", () => {
  it.each<[string, string]>([
    ["0.000001", "0.000001"],
    ["10", "10.00"],
    ["1234.5", "1,234.50"],
    ["0.024", "0.024"],
    ["10.00", "10.00"],
    ["0.50", "0.50"],
    ["-0.000001", "-0.000001"],
  ])("formats the exact decimal string %s", (value, expected) => {
    expect(formatMoney(value)).toBe(expected);
  });

  it("rounds computed numbers to ledger precision without float noise", () => {
    const result = formatMoney(0.1 + 0.2);
    expect(result).toBe("0.30");
    expect(result).not.toMatch(/000000000/);
  });
});
