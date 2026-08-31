import { beforeEach, describe, expect, it, vi } from "vitest";

const apiMocks = vi.hoisted(() => ({ GET: vi.fn(), POST: vi.fn() }));

vi.mock("@/lib/api-client", () => ({
  apiClient: apiMocks,
  csrfHeader: () => ({}),
  unwrap: async (result: { data?: unknown }) => result.data,
}));

import {
  adminRefundOrder,
  getOrderPickupCredentials,
  listOrders,
} from "./orders-api";

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

  it("supplies the required reason for an admin refund", async () => {
    apiMocks.POST.mockResolvedValueOnce({ data: { orderNo: "OR1" } });

    await adminRefundOrder("OR1");

    expect(apiMocks.POST).toHaveBeenCalledWith(
      "/v1/admin/orders/{orderNo}/refund",
      expect.objectContaining({
        body: { reason: "Admin console manual refund." },
      })
    );
  });

  it("loads pickup credentials for all selected orders in one request", async () => {
    apiMocks.POST.mockResolvedValueOnce({ data: [] });

    await getOrderPickupCredentials(["OR1", "OR2"]);

    expect(apiMocks.POST).toHaveBeenCalledTimes(1);
    expect(apiMocks.POST).toHaveBeenCalledWith(
      "/v1/orders/pickup-credentials",
      {
        body: { orderNos: ["OR1", "OR2"] },
        params: { header: {} },
      }
    );
  });
});
