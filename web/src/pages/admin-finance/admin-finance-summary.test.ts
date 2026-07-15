import { describe, expect, it } from "vitest";

import {
  creditMockFinanceUserWallet,
  getMockFinanceSummary,
  listMockFinanceUserBalances,
  reverseMockFinanceTransaction,
} from "./admin-finance-mock";

function localISOString(
  year: number,
  monthIndex: number,
  day: number,
  hour = 0,
  minute = 0,
  second = 0,
  millisecond = 0,
) {
  return new Date(
    year,
    monthIndex,
    day,
    hour,
    minute,
    second,
    millisecond,
  ).toISOString();
}

function total(
  trend: Awaited<ReturnType<typeof getMockFinanceSummary>>["trend"],
  field: "accountRevenue" | "platformRevenue" | "recharge" | "refund" | "spend" | "withdraw",
) {
  return trend.reduce((sum, point) => sum + point[field], 0);
}

describe("getMockFinanceSummary", () => {
  it("uses only the selected hours for a single-day range", async () => {
    const summary = await getMockFinanceSummary({
      createdFrom: localISOString(2026, 6, 14, 10, 15),
      createdTo: localISOString(2026, 6, 14, 11, 20),
    });

    expect(summary.trend.map((point) => point.label)).toEqual(["10:00", "11:00"]);
  });

  it("derives every summary card from the returned trend", async () => {
    const summary = await getMockFinanceSummary({
      createdFrom: localISOString(2026, 6, 1),
      createdTo: localISOString(2026, 6, 7, 23, 59, 59),
    });

    expect(Number(summary.rechargeAmount)).toBeCloseTo(total(summary.trend, "recharge"), 2);
    expect(Number(summary.spendAmount)).toBeCloseTo(total(summary.trend, "spend"), 2);
    expect(Number(summary.withdrawAmount)).toBeCloseTo(total(summary.trend, "withdraw"), 2);
    expect(Number(summary.refundAmount)).toBeCloseTo(total(summary.trend, "refund"), 2);
    expect(Number(summary.platformRevenue)).toBeCloseTo(
      total(summary.trend, "platformRevenue"),
      2,
    );
    expect(Number(summary.accountRevenue)).toBeCloseTo(
      total(summary.trend, "accountRevenue"),
      2,
    );
  });

  it("bounds long ranges and keeps cross-year labels unique", async () => {
    const summary = await getMockFinanceSummary({
      createdFrom: localISOString(2000, 0, 1),
      createdTo: localISOString(2026, 6, 1, 23, 59, 59),
    });

    expect(summary.trend.length).toBeLessThanOrEqual(366);
    expect(new Set(summary.trend.map((point) => point.label)).size).toBe(
      summary.trend.length,
    );
  });

  it("keeps manual adjustments distinct and reverses the current bucket", async () => {
    const users = await listMockFinanceUserBalances({}, 0, 1);
    const user = users.items[0]!;
    const originalBalance = user.consumerBalance;

    const adjustment = await creditMockFinanceUserWallet(
      user.userId,
      "10.00",
      "summary-invariant-test",
    );
    expect(adjustment.transaction.transactionType).toBe("manual_adjustment");

    const reversed = await reverseMockFinanceTransaction(
      adjustment.transaction.id,
    );
    expect(reversed.reversal.balanceBefore).toBe(
      adjustment.wallet.consumerBalance,
    );

    const balancesAfter = await listMockFinanceUserBalances({}, 0, 1);
    expect(balancesAfter.items[0]?.consumerBalance).toBe(originalBalance);
  });
});
