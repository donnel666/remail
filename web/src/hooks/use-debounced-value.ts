import { useCallback, useEffect, useRef, useState } from "react";

export const SHARED_SEARCH_DEBOUNCE_MS = 1_000;

export function useDebouncedValue<T>(
  value: T,
  delayMs = SHARED_SEARCH_DEBOUNCE_MS
) {
  const [debouncedValue, setDebouncedValue] = useState(value);
  const valueRef = useRef(value);
  valueRef.current = value;

  useEffect(() => {
    const timer = globalThis.setTimeout(() => {
      setDebouncedValue(value);
    }, delayMs);

    return () => globalThis.clearTimeout(timer);
  }, [delayMs, value]);

  // Stable identity: callers put flush in useCallback/useEffect deps.
  const flush = useCallback((nextValue: T = valueRef.current) => {
    setDebouncedValue(nextValue);
  }, []);

  return [debouncedValue, flush] as const;
}
