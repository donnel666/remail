// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";

import { apiClient, csrfHeader } from "./api-client";

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
  for (const name of ["csrf_token", "csrf_token_points_v2"]) {
    document.cookie = `${name}=; Max-Age=0; path=/`;
  }
});

it.each([
  ["/v1/turnstile/config", 120_000],
  ["/v1/resources/imports", 600_000],
  ["/v1/admin/resources/imports", 600_000],
] as const)("keeps %s requests alive until %i ms", async (path, timeoutMs) => {
  vi.useFakeTimers();
  let observed: AbortSignal | undefined;
  vi.stubGlobal("fetch", vi.fn((request: Request) => {
    observed = request.signal;
    return new Promise((_resolve, reject) => {
      request.signal.addEventListener("abort", () => reject(new DOMException("Timeout", "AbortError")), { once: true });
    });
  }));
  const formData = new FormData();
  formData.append("file", "test upload");
  const request = path === "/v1/turnstile/config"
    ? apiClient.GET(path, { baseUrl: "http://localhost" })
    : apiClient.POST(path, {
      baseUrl: "http://localhost",
      body: formData as never,
      bodySerializer: (body) => body,
      params: { header: { "X-CSRF-Token": "csrf", "Idempotency-Key": "timeout-test" } },
    });
  const rejected = expect(request).rejects.toMatchObject({ name: "AbortError" });
  await vi.advanceTimersByTimeAsync(60_000);
  expect(observed?.aborted).toBe(false);
  await vi.advanceTimersByTimeAsync(timeoutMs - 60_001);
  expect(observed?.aborted).toBe(false);
  await vi.advanceTimersByTimeAsync(1);
  await rejected;
  expect(observed?.aborted).toBe(true);
});

describe("csrfHeader", () => {
  it("uses the csrf cookie and ignores the points-v2 namespace", () => {
    document.cookie = "csrf_token_points_v2=points-v2-csrf; path=/";
    document.cookie = "csrf_token=csrf; path=/";

    expect(csrfHeader()).toEqual({ "X-CSRF-Token": "csrf" });
  });
});
