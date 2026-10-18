import { IamApiError } from "./api-client";
import { generateIdempotencyKey } from "./idempotency";

interface BulkAttempt {
  key: string;
  uncertain?: boolean;
  pending?: Promise<unknown>;
}

export class DaumBulkOutcomeUnconfirmedError extends Error {
  constructor(readonly cause?: unknown) {
    super("Daum bulk operation outcome is not confirmed. Retry to confirm.");
  }
}

export function isDaumBulkOutcomeUnconfirmed(error: unknown) {
  return error instanceof DaumBulkOutcomeUnconfirmedError || !(
    error instanceof IamApiError && error.status >= 400 && error.status < 500 && error.status !== 408
  );
}

// Both Daum pages keep their own session, scoped to the mounted page and current user.
// The caller normalizes its selection and only resolves after receiving a valid receipt.
export function createDaumBulkSession() {
  const attempts = new Map<string, BulkAttempt>();
  return {
    run<T>(signature: string, send: (key: string) => Promise<T>): Promise<T> {
      let attempt = attempts.get(signature);
      if (!attempt) {
        attempt = { key: generateIdempotencyKey() };
        attempts.set(signature, attempt);
      }
      if (attempt.pending) return attempt.pending as Promise<T>;
      const current = attempt;
      const request = Promise.resolve().then(async () => {
        try {
          const result = await send(current.key);
          // A confirmed receipt ends this operation; a later explicit click is a new one.
          attempts.delete(signature);
          return result;
        } catch (error) {
          if (isDaumBulkOutcomeUnconfirmed(error)) current.uncertain = true;
          else if (!current.uncertain) attempts.delete(signature);
          else {
            // Rejecting this retry cannot disprove acceptance of an earlier lost response.
            throw new DaumBulkOutcomeUnconfirmedError(error);
          }
          throw error;
        }
      });
      const pending = request.finally(() => { current.pending = undefined; });
      current.pending = pending;
      return pending;
    },
  };
}

export type DaumBulkSession = ReturnType<typeof createDaumBulkSession>;
