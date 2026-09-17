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

it("keeps requests alive after 60 seconds and aborts at 120 seconds", async () => {
  vi.useFakeTimers();
  let observed: AbortSignal | undefined;
  vi.stubGlobal("fetch", vi.fn((request: Request) => {
    observed = request.signal;
    return new Promise((_resolve, reject) => {
      request.signal.addEventListener("abort", () => reject(new DOMException("Timeout", "AbortError")), { once: true });
    });
  }));
  const request = apiClient.GET("/v1/turnstile/config", { baseUrl: "http://localhost" });
  const rejected = expect(request).rejects.toMatchObject({ name: "AbortError" });
  await vi.advanceTimersByTimeAsync(60_000);
  expect(observed?.aborted).toBe(false);
  await vi.advanceTimersByTimeAsync(59_999);
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
