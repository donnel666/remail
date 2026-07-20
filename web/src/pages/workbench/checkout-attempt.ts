import { generateIdempotencyKey } from "@/lib/idempotency";

const storageKey = "remail.workbench.checkoutAttempt";

export interface CheckoutAttempt {
  key: string;
  signature: string;
}

type CheckoutStorage = Pick<Storage, "getItem" | "setItem" | "removeItem">;

export function loadCheckoutAttempt(
  signature: string,
  storage?: CheckoutStorage,
) {
  try {
    const target = storage ?? globalThis.sessionStorage;
    const parsed = JSON.parse(target?.getItem(storageKey) ?? "null") as
      | Partial<CheckoutAttempt>
      | null;
    if (
      parsed?.signature === signature &&
      typeof parsed.key === "string" &&
      parsed.key.trim()
    ) {
      return parsed as CheckoutAttempt;
    }
  } catch {
    // Invalid or unavailable storage gets a fresh retry identity below.
  }
  return { key: generateIdempotencyKey(), signature };
}

export function saveCheckoutAttempt(
  attempt: CheckoutAttempt,
  storage?: CheckoutStorage,
) {
  try {
    const target = storage ?? globalThis.sessionStorage;
    target?.setItem(storageKey, JSON.stringify(attempt));
  } catch {
    // The in-memory ref still protects retries within the current page.
  }
}

export function clearCheckoutAttempt(
  attempt: CheckoutAttempt,
  storage?: CheckoutStorage,
) {
  let target = storage;
  try {
    target ??= globalThis.sessionStorage;
    const stored = JSON.parse(target?.getItem(storageKey) ?? "null") as
      | Partial<CheckoutAttempt>
      | null;
    if (stored?.key === attempt.key) target?.removeItem(storageKey);
  } catch {
    try {
      target?.removeItem(storageKey);
    } catch {
      // Storage can remain unavailable; the caller still clears its in-memory ref.
    }
  }
}
