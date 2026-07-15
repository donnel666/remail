import { describe, expect, it } from "vitest";

import { getDashboardData } from "./dashboard-mock";

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

const range = {
  from: localISOString(2026, 6, 1),
  to: localISOString(2026, 6, 7, 23, 59, 59, 999),
};

describe("getDashboardData", () => {
  it("returns stable mock data for the same time range", async () => {
    const [first, second] = await Promise.all([
      getDashboardData(range),
      getDashboardData(range),
    ]);

    expect(first).toEqual(second);
  });

  it("ranks users by successful code receipts without changing project analytics", async () => {
    const data = await getDashboardData({ ...range, username: "donnel" });

    for (const ranking of [data.todayCodeRanking, data.historicalCodeRanking]) {
      expect(ranking).toHaveLength(10);
      expect(ranking.map((item) => item.count)).toEqual(
        [...ranking.map((item) => item.count)].sort((left, right) => right - left),
      );
    }

    expect(data.todayCodeRanking.reduce((sum, item) => sum + item.count, 0)).toBeLessThanOrEqual(
      data.stats.todayCodeReceipts,
    );
    expect(data.historicalCodeRanking.some((item) => item.isCurrentUser)).toBe(false);
    expect(data.historicalCurrentUserRank).toEqual(
      expect.objectContaining({ isCurrentUser: true, name: "donnel" }),
    );
    expect(data.historicalCurrentUserRank.rank).toBeGreaterThan(10);
    expect(data.todayCurrentUserRank.rank).toBeGreaterThan(10);
    expect(data.projectCodeRanking).toHaveLength(10);
    expect(data.projectCodeRanking.map((item) => item.name)).toContain("Microsoft");
    expect(data.projectCodeRanking.every((item) => !item.isCurrentUser)).toBe(true);
    expect(data.projectCodeRanking.reduce((sum, item) => sum + item.count, 0)).toBe(
      data.stats.totalCodeReceipts,
    );

    expect(data.stats.totalCodeReceipts).toBeGreaterThan(0);
    expect(data.projectSeries).toHaveLength(6);
    expect(
      data.projectSeries.every(
        (series) => series.receivedCodes.length === data.trend.length,
      ),
    ).toBe(true);
  });

  it("derives user-facing quality metrics from the trend", async () => {
    const data = await getDashboardData(range);
    const codeOrders = data.trend.reduce((sum, point) => sum + point.codeOrders, 0);
    const receivedCodes = data.trend.reduce(
      (sum, point) => sum + point.receivedCodes,
      0,
    );
    const weightedSeconds = data.trend.reduce(
      (sum, point) => sum + point.averageCodeReceiptSeconds * point.receivedCodes,
      0,
    );

    expect(data.stats.codeSuccessRate).toBe(
      Number(((receivedCodes / codeOrders) * 100).toFixed(1)),
    );
    expect(data.stats.averageCodeReceiptSeconds).toBe(
      Math.round(weightedSeconds / receivedCodes),
    );
    expect(data.codeRatio + data.purchaseRatio).toBe(100);
  });

  it("respects the selected time inside a single day", async () => {
    const data = await getDashboardData({
      from: localISOString(2026, 6, 14, 10, 15),
      to: localISOString(2026, 6, 14, 11, 20),
    });

    expect(data.trend.map((point) => point.label)).toEqual(["10:00", "11:00"]);
  });

  it("keeps today and all-time ranking semantics independent of the chart range", async () => {
    const [first, second] = await Promise.all([
      getDashboardData({
        from: localISOString(2026, 5, 1),
        to: localISOString(2026, 5, 7, 23, 59, 59),
        username: "donnel",
      }),
      getDashboardData({
        from: localISOString(2026, 6, 1),
        to: localISOString(2026, 6, 7, 23, 59, 59),
        username: "donnel",
      }),
    ]);

    expect(first.stats.todayOrders).toBe(second.stats.todayOrders);
    expect(first.stats.todayCodeReceipts).toBe(second.stats.todayCodeReceipts);
    expect(first.stats.historicalSpend).toBe(second.stats.historicalSpend);
    expect(first.todayCodeRanking).toEqual(second.todayCodeRanking);
    expect(first.historicalCodeRanking).toEqual(second.historicalCodeRanking);
  });

  it("bounds long ranges and gives cross-year points unique labels", async () => {
    const data = await getDashboardData({
      from: localISOString(2000, 0, 1),
      to: localISOString(2026, 6, 1, 23, 59, 59),
    });

    expect(data.trend.length).toBeLessThanOrEqual(366);
    expect(new Set(data.trend.map((point) => point.label)).size).toBe(
      data.trend.length,
    );
  });
});
