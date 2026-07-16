import { useCallback, useEffect, useState } from "react";

export const SHARED_SEARCH_DEBOUNCE_MS = 1_000;

export function useDebouncedValue<T>(
  value: T,
  delayMs = SHARED_SEARCH_DEBOUNCE_MS
) {
  const [debouncedValue, setDebouncedValue] = useState(value);

  useEffect(() => {
    const timer = globalThis.setTimeout(() => {
      setDebouncedValue(value);
    }, delayMs);

    return () => globalThis.clearTimeout(timer);
  }, [delayMs, value]);

  const flush = useCallback(
    (nextValue: T = value) => {
      setDebouncedValue(nextValue);
    },
    [value]
  );

  return [debouncedValue, flush] as const;
}
