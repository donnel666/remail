import { describe, expect, it } from "vitest";

import { getAdminDashboardData } from "./admin-dashboard-mock";

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
  to: localISOString(2026, 6, 7, 23, 59, 59),
};

function roundMoney(value: number) {
  return Math.round(value * 100) / 100;
}

describe("getAdminDashboardData", () => {
  it("returns stable asynchronous data for the same date range", async () => {
    const firstPromise = getAdminDashboardData(range);
    expect(firstPromise).toBeInstanceOf(Promise);

    const [first, second] = await Promise.all([
      firstPromise,
      getAdminDashboardData(range),
    ]);

    expect(first).toEqual(second);
    expect(first.trend).toHaveLength(7);
  });

  it("derives platform finance and order totals from every trend point", async () => {
    const data = await getAdminDashboardData(range);
    const sum = (
      field:
        | "rechargeAmount"
        | "spendAmount"
        | "refundAmount"
        | "platformRevenue"
        | "withdrawAmount",
    ) =>
      roundMoney(data.trend.reduce((total, point) => total + point[field], 0));

    expect(data.stats.rechargeAmount).toBe(sum("rechargeAmount"));
    expect(data.stats.spendAmount).toBe(sum("spendAmount"));
    expect(data.stats.refundAmount).toBe(sum("refundAmount"));
    expect(data.stats.platformRevenue).toBe(sum("platformRevenue"));
    expect(data.stats.withdrawAmount).toBe(sum("withdrawAmount"));
    expect(data.stats.totalOrders).toBe(
      data.trend.reduce((total, point) => total + point.orders, 0),
    );
    expect(data.stats.successfulCodeReceipts).toBe(
      data.trend.reduce(
        (total, point) => total + point.successfulCodeReceipts,
        0,
      ),
    );
    expect(data.stats.newUsers).toBe(
      data.trend.reduce((total, point) => total + point.newUsers, 0),
    );
  });

  it("keeps resource snapshots valid and calculates weighted success rates", async () => {
    const data = await getAdminDashboardData(range);
    const lastPoint = data.trend[data.trend.length - 1]!;

    for (const point of data.trend) {
      expect(point.microsoftAvailableEmails).toBeLessThanOrEqual(
        point.microsoftTotalEmails,
      );
      expect(point.domainAvailableMailboxes).toBeLessThanOrEqual(
        point.domainTotalMailboxes,
      );
      expect(point.microsoftReceivedCodes).toBeLessThanOrEqual(
        point.microsoftCodeOrders,
      );
      expect(point.domainReceivedCodes).toBeLessThanOrEqual(
        point.domainCodeOrders,
      );
      expect(point.activeUsers).toBeLessThanOrEqual(point.totalUsers);
    }

    expect(data.stats.microsoftTotalEmails).toBe(lastPoint.microsoftTotalEmails);
    expect(data.stats.microsoftAvailableEmails).toBe(
      lastPoint.microsoftAvailableEmails,
    );
    expect(data.stats.domainTotalMailboxes).toBe(lastPoint.domainTotalMailboxes);
    expect(data.stats.domainAvailableMailboxes).toBe(
      lastPoint.domainAvailableMailboxes,
    );
    expect(data.stats.totalUsers).toBe(lastPoint.totalUsers);
    expect(data.stats.activeUsers).toBe(lastPoint.activeUsers);

    const microsoftOrders = data.trend.reduce(
      (total, point) => total + point.microsoftCodeOrders,
      0,
    );
    const microsoftCodes = data.trend.reduce(
      (total, point) => total + point.microsoftReceivedCodes,
      0,
    );
    const domainOrders = data.trend.reduce(
      (total, point) => total + point.domainCodeOrders,
      0,
    );
    const domainCodes = data.trend.reduce(
      (total, point) => total + point.domainReceivedCodes,
      0,
    );

    expect(data.stats.microsoftCodeSuccessRate).toBe(
      Number(((microsoftCodes / microsoftOrders) * 100).toFixed(1)),
    );
    expect(data.stats.domainCodeSuccessRate).toBe(
      Number(((domainCodes / domainOrders) * 100).toFixed(1)),
    );
    expect(data.stats.microsoftAverageCodeReceiptSeconds).toBe(
      Math.round(
        data.trend.reduce(
          (total, point) =>
            total +
            point.microsoftAverageCodeReceiptSeconds * point.microsoftReceivedCodes,
          0,
        ) / microsoftCodes,
      ),
    );
    expect(data.stats.domainAverageCodeReceiptSeconds).toBe(
      Math.round(
        data.trend.reduce(
          (total, point) =>
            total + point.domainAverageCodeReceiptSeconds * point.domainReceivedCodes,
          0,
        ) / domainCodes,
      ),
    );
    expect(data.stats.successfulCodeReceipts).toBe(
      data.stats.microsoftCodeReceipts + data.stats.domainCodeReceipts,
    );
  });

  it("returns project code and low-inventory rankings", async () => {
    const data = await getAdminDashboardData(range);

    expect(data.projectCodeRanking).toHaveLength(10);
    expect(data.projectCodeRanking.map((item) => item.rank)).toEqual([
      1, 2, 3, 4, 5, 6, 7, 8, 9, 10,
    ]);
    expect(data.projectCodeRanking.map((item) => item.count)).toEqual(
      [...data.projectCodeRanking.map((item) => item.count)].sort(
        (left, right) => right - left,
      ),
    );
    expect(data.projectCodeRanking.every((item) => item.count <= item.orders)).toBe(true);

    expect(
      data.projectCodeRanking.reduce((total, item) => total + item.count, 0),
    ).toBe(data.stats.successfulCodeReceipts);

    expect(data.projectInventoryRanking).toHaveLength(10);
    expect(data.projectInventoryRanking.map((item) => item.rank)).toEqual([
      1, 2, 3, 4, 5, 6, 7, 8, 9, 10,
    ]);
    expect(data.projectInventoryRanking.map((item) => item.available)).toEqual(
      [...data.projectInventoryRanking.map((item) => item.available)].sort(
        (left, right) => left - right,
      ),
    );
    expect(data.projectInventoryRanking[0]?.consumed).toBe(
      data.projectCodeRanking[0]?.count,
    );
  });

  it("uses hourly points for a single-day platform view", async () => {
    const data = await getAdminDashboardData({
      from: localISOString(2026, 6, 14),
      to: localISOString(2026, 6, 14, 23, 59, 59),
    });

    expect(data.trend).toHaveLength(24);
    expect(data.trend[0]?.label).toBe("00:00");
    expect(data.trend[23]?.label).toBe("23:00");
  });

  it("respects a partial single-day range", async () => {
    const data = await getAdminDashboardData({
      from: localISOString(2026, 6, 14, 10, 15),
      to: localISOString(2026, 6, 14, 11, 20),
    });

    expect(data.trend.map((point) => point.label)).toEqual(["10:00", "11:00"]);
  });

  it("bounds long ranges and keeps cross-year labels unique", async () => {
    const data = await getAdminDashboardData({
      from: localISOString(2000, 0, 1),
      to: localISOString(2026, 6, 1, 23, 59, 59),
    });

    expect(data.trend.length).toBeLessThanOrEqual(366);
    expect(new Set(data.trend.map((point) => point.label)).size).toBe(
      data.trend.length,
    );
  });
});
