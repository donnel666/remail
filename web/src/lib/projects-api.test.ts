import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const apiMocks = vi.hoisted(() => ({
  GET: vi.fn(),
}));

vi.mock("./api-client", () => {
  class IamApiError extends Error {
    readonly status: number;
    readonly retryAfterSeconds?: number;

    constructor(
      status: number,
      body: { message?: string },
      response?: Response
    ) {
      super(body.message || "Request failed.");
      this.status = status;
      const retryAfter = Number.parseInt(
        response?.headers.get("Retry-After") ?? "",
        10
      );
      if (Number.isFinite(retryAfter) && retryAfter > 0) {
        this.retryAfterSeconds = retryAfter;
      }
    }
  }

  return {
    apiClient: apiMocks,
    csrfHeader: () => ({}),
    IamApiError,
    unwrap: async (result: { data?: unknown }) => result.data,
  };
});

import { IamApiError } from "./api-client";
import { getProjectInventory } from "./projects-api";

describe("project inventory API", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.useFakeTimers();
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("retries a preparing cache after Retry-After", async () => {
    const preparing = new IamApiError(
      503,
      { message: "Inventory is being prepared." },
      new Response(null, { status: 503, headers: { "Retry-After": "3" } })
    );
    const inventory = { projectId: 2, totalAvailable: 7, products: [] };
    apiMocks.GET.mockRejectedValueOnce(preparing).mockResolvedValueOnce({
      data: inventory,
    });

    const result = getProjectInventory(2);
    await vi.advanceTimersByTimeAsync(2_999);
    expect(apiMocks.GET).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(1);

    await expect(result).resolves.toEqual(inventory);
    expect(apiMocks.GET).toHaveBeenCalledTimes(2);
  });

  it("stops retrying a cache that never becomes ready", async () => {
    const preparing = new IamApiError(503, {
      message: "Inventory is being prepared.",
    });
    apiMocks.GET.mockRejectedValue(preparing);

    const result = expect(getProjectInventory(2)).rejects.toBe(preparing);
    await vi.runAllTimersAsync();

    await result;
    expect(apiMocks.GET).toHaveBeenCalledTimes(7);
  });
});
