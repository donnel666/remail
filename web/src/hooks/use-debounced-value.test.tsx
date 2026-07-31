// @vitest-environment jsdom

import { act, renderHook } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import {
  SHARED_SEARCH_DEBOUNCE_MS,
  useDebouncedValue,
} from "./use-debounced-value";

afterEach(() => vi.useRealTimers());

describe("shared search debounce", () => {
  it("uses the global one-second delay by default", () => {
    vi.useFakeTimers();
    const { result, rerender } = renderHook(
      ({ value }) => useDebouncedValue(value),
      { initialProps: { value: "first" } }
    );

    rerender({ value: "second" });
    act(() => vi.advanceTimersByTime(SHARED_SEARCH_DEBOUNCE_MS - 1));
    expect(result.current[0]).toBe("first");
    act(() => vi.advanceTimersByTime(1));
    expect(result.current[0]).toBe("second");
    expect(SHARED_SEARCH_DEBOUNCE_MS).toBe(1_000);
  });

  it("keeps flush stable across value changes and flushes the latest value", () => {
    vi.useFakeTimers();
    const { result, rerender } = renderHook(
      ({ value }) => useDebouncedValue(value),
      { initialProps: { value: "first" } }
    );
    const flush = result.current[1];

    rerender({ value: "second" });
    expect(result.current[1]).toBe(flush);

    act(() => flush());
    expect(result.current[0]).toBe("second");
  });
});
