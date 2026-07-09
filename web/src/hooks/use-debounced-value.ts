import { useCallback, useEffect, useState } from "react";

export function useDebouncedValue<T>(value: T, delayMs = 3000) {
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
