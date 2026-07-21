import { beforeEach, describe, expect, it, vi } from "vitest";

const apiMocks = vi.hoisted(() => ({
  GET: vi.fn(),
  DELETE: vi.fn(),
}));

vi.mock("@/lib/api-client", () => ({
  apiClient: apiMocks,
  csrfHeader: () => ({ "X-CSRF-Token": "logs-csrf" }),
  unwrap: async (result: { data?: unknown }) => result.data,
}));

import { listLogs, purgeLogs } from "./admin-logs-api";

describe("admin log API adapter", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("lists system logs with server-side filters and pagination", async () => {
    apiMocks.GET.mockResolvedValueOnce({
      data: {
        items: [
          {
            id: 11,
            createdAt: "2026-07-20T08:00:00Z",
            category: "system",
            requestId: "req-11",
            level: "warning",
            module: "core",
            eventType: "resource.validation_failed",
            bizType: "resource",
            bizId: "61",
            message: "Resource validation failed.",
            detail: "category=authorization",
          },
        ],
        total: 1,
        facets: { system: 8, operation: 5 },
        offset: 20,
        limit: 20,
      },
    });

    await expect(
      listLogs(
        {
          category: "system",
          level: "warning",
          search: " authorization ",
          from: "2026-07-01T00:00:00Z",
          to: "2026-07-31T23:59:59Z",
        },
        20,
        20
      )
    ).resolves.toMatchObject({ total: 1, facets: { system: 8, operation: 5 } });

    expect(apiMocks.GET).toHaveBeenCalledWith("/v1/admin/logs/system", {
      params: {
        query: {
          level: "warning",
          search: "authorization",
          from: "2026-07-01T00:00:00Z",
          to: "2026-07-31T23:59:59Z",
          offset: 20,
          limit: 20,
        },
      },
    });
  });

  it("uses the operation endpoint and omits the all-result sentinel", async () => {
    apiMocks.GET.mockResolvedValueOnce({
      data: {
        items: [],
        total: 0,
        facets: { system: 8, operation: 5 },
        offset: 0,
        limit: 50,
      },
    });

    await listLogs({ category: "operation", result: "all" }, 0, 50);

    expect(apiMocks.GET).toHaveBeenCalledWith("/v1/admin/logs/operations", {
      params: {
        query: {
          result: undefined,
          search: undefined,
          from: undefined,
          to: undefined,
          offset: 0,
          limit: 50,
        },
      },
    });
  });

  it("cleans only the selected stream with CSRF protection", async () => {
    apiMocks.DELETE
      .mockResolvedValueOnce({ data: { removed: 12 } })
      .mockResolvedValueOnce({ data: { removed: 4 } });
    const before = "2030-01-01T00:00:00Z";

    await expect(purgeLogs("system", before)).resolves.toBe(12);
    await expect(purgeLogs("operation", before)).resolves.toBe(4);

    const options = {
      params: {
        query: { before },
        header: { "X-CSRF-Token": "logs-csrf" },
      },
    };
    expect(apiMocks.DELETE).toHaveBeenNthCalledWith(
      1,
      "/v1/admin/logs/system",
      options
    );
    expect(apiMocks.DELETE).toHaveBeenNthCalledWith(
      2,
      "/v1/admin/logs/operations",
      options
    );
  });
});
