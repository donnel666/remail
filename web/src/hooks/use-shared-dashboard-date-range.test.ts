// @vitest-environment jsdom

import { act, renderHook } from "@testing-library/react";
import { afterAll, beforeAll, describe, expect, it, vi } from "vitest";

import {
  DASHBOARD_MAX_RANGE_DAYS,
  createDefaultDashboardDateRange,
  normalizeDashboardDateRange,
  useSharedDashboardDateRange,
} from "./use-shared-dashboard-date-range";

describe("dashboard date range", () => {
  const now = new Date("2026-07-15T12:30:00+08:00");

  beforeAll(() => {
    vi.useFakeTimers();
    vi.setSystemTime(now);
  });

  afterAll(() => {
    vi.useRealTimers();
  });

  it("uses the same bounded 30-day default for every dashboard", () => {
    const [from, to] = createDefaultDashboardDateRange(now);
    const expectedFrom = new Date(now);
    expectedFrom.setDate(expectedFrom.getDate() - 29);
    expectedFrom.setHours(0, 0, 0, 0);

    expect(from).toEqual(expectedFrom);
    expect(to).toEqual(now);
  });

  it("orders dates and excludes future time", () => {
    const range = normalizeDashboardDateRange(
      [
        new Date("2026-07-16T00:00:00+08:00"),
        new Date("2026-07-15T10:00:00+08:00"),
      ],
      now,
    );

    expect(range).toEqual([
      new Date("2026-07-15T10:00:00+08:00"),
      now,
    ]);
  });

  it("bounds very long custom ranges without changing their end", () => {
    const [from, to] = normalizeDashboardDateRange(
      [
        new Date("2000-01-01T00:00:00+08:00"),
        new Date("2026-07-01T23:59:59+08:00"),
      ],
      now,
    );

    const earliestAllowed = new Date(to);
    earliestAllowed.setDate(
      earliestAllowed.getDate() - (DASHBOARD_MAX_RANGE_DAYS - 1),
    );
    expect(from).toEqual(earliestAllowed);
    expect(to).toEqual(new Date("2026-07-01T23:59:59+08:00"));
  });

  it("keeps the selected value when one dashboard unmounts and another mounts", () => {
    const selected = [
      new Date("2026-07-01T09:00:00+08:00"),
      new Date("2026-07-07T18:00:00+08:00"),
    ];
    const firstDashboard = renderHook(() => useSharedDashboardDateRange());

    act(() => firstDashboard.result.current[1](selected));
    firstDashboard.unmount();

    const secondDashboard = renderHook(() => useSharedDashboardDateRange());
    expect(secondDashboard.result.current[0]).toEqual(selected);
    secondDashboard.unmount();
  });

  it("updates another mounted dashboard when the selected value changes", () => {
    const selected = [
      new Date("2026-07-08T08:30:00+08:00"),
      new Date("2026-07-12T20:15:00+08:00"),
    ];
    const firstDashboard = renderHook(() => useSharedDashboardDateRange());
    const secondDashboard = renderHook(() => useSharedDashboardDateRange());

    act(() => firstDashboard.result.current[1](selected));

    expect(firstDashboard.result.current[0]).toEqual(selected);
    expect(secondDashboard.result.current[0]).toEqual(selected);
    firstDashboard.unmount();
    secondDashboard.unmount();
  });

  it("refreshes the cached value after another browser tab changes it", () => {
    const selected = [
      new Date("2026-06-01T00:00:00+08:00"),
      new Date("2026-06-30T23:59:00+08:00"),
    ];
    const serialized = JSON.stringify(
      selected.map((date) => date.toISOString()),
    );
    const dashboard = renderHook(() => useSharedDashboardDateRange());
    dashboard.unmount();

    window.localStorage.setItem("dashboard-date-range", serialized);
    act(() => {
      window.dispatchEvent(
        new StorageEvent("storage", {
          key: "dashboard-date-range",
          newValue: serialized,
        }),
      );
    });

    const remountedDashboard = renderHook(() =>
      useSharedDashboardDateRange(),
    );
    expect(remountedDashboard.result.current[0]).toEqual(selected);
    remountedDashboard.unmount();
  });
});
