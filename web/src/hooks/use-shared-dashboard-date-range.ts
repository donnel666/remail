import { useCallback, useEffect, useState } from "react";

import {
  normalizeDateRangeValue,
  type DateRangeValue,
} from "@/pages/resources/date-range-filter";

const DASHBOARD_DATE_RANGE_STORAGE_KEY = "dashboard-date-range";

// Keep custom ranges bounded so every dashboard can render them without
// producing an unbounded number of chart points. The selected range remains
// identical when users move between the console, administrator and finance
// dashboards.
export const DASHBOARD_MAX_RANGE_DAYS = 366;

type RangeListener = (range: DateRangeValue) => void;

const rangeListeners = new Set<RangeListener>();
let currentRange: DateRangeValue | null = null;
let storageListenerInstalled = false;

function cloneRange(range: DateRangeValue): DateRangeValue {
  return range.map((date) => new Date(date.getTime()));
}

export function createDefaultDashboardDateRange(
  now = new Date(),
): DateRangeValue {
  const end = new Date(now.getTime());
  const start = new Date(end.getTime() - 24 * 60 * 60 * 1000);
  return [start, end];
}

export function normalizeDashboardDateRange(
  value: DateRangeValue,
  now = new Date(),
): DateRangeValue {
  const normalized = normalizeDateRangeValue(value);
  if (normalized.length !== 2) return createDefaultDashboardDateRange(now);

  let [from, to] = cloneRange(normalized);
  if (to.getTime() > now.getTime()) to = new Date(now.getTime());
  if (from.getTime() > to.getTime()) from = new Date(to.getTime());

  const earliestAllowed = new Date(to.getTime());
  earliestAllowed.setDate(
    earliestAllowed.getDate() - (DASHBOARD_MAX_RANGE_DAYS - 1),
  );
  if (from.getTime() < earliestAllowed.getTime()) from = earliestAllowed;

  return [from, to];
}

function readStoredRange(): DateRangeValue {
  if (typeof window === "undefined") return createDefaultDashboardDateRange();

  try {
    const stored = window.localStorage.getItem(DASHBOARD_DATE_RANGE_STORAGE_KEY);
    if (!stored) return createDefaultDashboardDateRange();
    const parsed: unknown = JSON.parse(stored);
    if (!Array.isArray(parsed)) return createDefaultDashboardDateRange();
    return normalizeDashboardDateRange(
      parsed.map((value) => new Date(String(value))),
    );
  } catch {
    return createDefaultDashboardDateRange();
  }
}

function getCurrentRange() {
  currentRange ??= readStoredRange();
  return cloneRange(currentRange);
}

function notifyRangeListeners(range: DateRangeValue) {
  for (const listener of rangeListeners) listener(cloneRange(range));
}

function ensureStorageListener() {
  if (storageListenerInstalled || typeof window === "undefined") return;

  window.addEventListener("storage", (event) => {
    if (event.key !== DASHBOARD_DATE_RANGE_STORAGE_KEY) return;
    currentRange = readStoredRange();
    notifyRangeListeners(currentRange);
  });
  storageListenerInstalled = true;
}

function storeRange(range: DateRangeValue) {
  if (typeof window === "undefined") return;

  try {
    window.localStorage.setItem(
      DASHBOARD_DATE_RANGE_STORAGE_KEY,
      JSON.stringify(range.map((date) => date.toISOString())),
    );
  } catch {
    // Storage can be unavailable in privacy modes. The in-memory value still
    // keeps dashboard navigation synchronized for the current tab.
  }
}

function publishRange(range: DateRangeValue) {
  currentRange = cloneRange(range);
  storeRange(range);
  notifyRangeListeners(currentRange);
}

export function useSharedDashboardDateRange() {
  const [range, setRangeState] = useState<DateRangeValue>(getCurrentRange);

  const setRange = useCallback((nextRange: DateRangeValue) => {
    publishRange(normalizeDashboardDateRange(nextRange));
  }, []);

  useEffect(() => {
    ensureStorageListener();
    const listener: RangeListener = (nextRange) => setRangeState(nextRange);
    rangeListeners.add(listener);
    return () => {
      rangeListeners.delete(listener);
    };
  }, []);

  return [range, setRange] as const;
}
