import { beforeEach, describe, expect, it, vi } from "vitest";

const apiMocks = vi.hoisted(() => ({ GET: vi.fn() }));

vi.mock("@/lib/api-client", () => ({
  apiClient: apiMocks,
  csrfHeader: () => ({}),
  unwrap: async (result: { data?: unknown }) => result.data,
}));

import { listOrders } from "./orders-api";

describe("order API adapter", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("passes the selected project to the order list request", async () => {
    apiMocks.GET.mockResolvedValueOnce({ data: { items: [] } });

    await listOrders({ projectId: 11 });

    expect(apiMocks.GET).toHaveBeenCalledWith("/v1/orders", {
      params: {
        query: {
          scope: "mine",
          afterId: undefined,
          offset: undefined,
          limit: 100,
          search: undefined,
          serviceMode: undefined,
          status: undefined,
          projectId: 11,
          domain: undefined,
          createdFrom: undefined,
          createdTo: undefined,
        },
      },
    });
  });
});
