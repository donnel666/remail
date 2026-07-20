import { describe, expect, it } from "vitest";

import {
  clearCheckoutAttempt,
  loadCheckoutAttempt,
  saveCheckoutAttempt,
} from "./checkout-attempt";

function memoryStorage() {
  const values = new Map<string, string>();
  return {
    getItem: (key: string) => values.get(key) ?? null,
    removeItem: (key: string) => values.delete(key),
    setItem: (key: string, value: string) => values.set(key, value),
  };
}

describe("checkout attempt persistence", () => {
  it("reuses the same key after reload and clears it after completion", () => {
    const storage = memoryStorage();
    const first = loadCheckoutAttempt("same-request", storage);
    saveCheckoutAttempt(first, storage);

    expect(loadCheckoutAttempt("same-request", storage)).toEqual(first);
    expect(loadCheckoutAttempt("changed-request", storage).key).not.toBe(
      first.key,
    );

    clearCheckoutAttempt(first, storage);
    expect(loadCheckoutAttempt("same-request", storage).key).not.toBe(
      first.key,
    );
  });

  it("falls back safely when session storage is unavailable", () => {
    const unavailableStorage = {
      getItem: () => {
        throw new Error("storage unavailable");
      },
      removeItem: () => {
        throw new Error("storage unavailable");
      },
      setItem: () => {
        throw new Error("storage unavailable");
      },
    };
    const attempt = loadCheckoutAttempt("request", unavailableStorage);

    expect(attempt.signature).toBe("request");
    expect(attempt.key).toBeTruthy();
    expect(() => saveCheckoutAttempt(attempt, unavailableStorage)).not.toThrow();
    expect(() => clearCheckoutAttempt(attempt, unavailableStorage)).not.toThrow();
  });
});
