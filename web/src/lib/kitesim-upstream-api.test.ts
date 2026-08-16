import { beforeEach, describe, expect, it, vi } from "vitest";

const apiMocks = vi.hoisted(() => ({
  GET: vi.fn(),
  POST: vi.fn(),
  PUT: vi.fn(),
}));
const idempotencyMocks = vi.hoisted(() => ({
  generate: vi.fn(),
}));

vi.mock("./api-client", () => ({
  IamApiError: class IamApiError extends Error {
    readonly status: number;

    constructor(status: number, body: { message?: string } = {}) {
      super(body.message || "Request failed.");
      this.status = status;
    }
  },
  apiClient: apiMocks,
  csrfHeader: () => ({ "X-CSRF-Token": "kitesim-csrf" }),
  unwrap: async (result: { data?: unknown }) => result.data,
}));
vi.mock("./idempotency", () => ({
  generateIdempotencyKey: idempotencyMocks.generate,
}));

import {
  listKitesimProducts,
  purchaseKitesimNumbers,
  rechargeKitesimAccount,
  renewKitesimPhone,
  updateKitesimUpstream,
} from "./kitesim-upstream-api";
import { IamApiError } from "./api-client";

describe("Kitesim upstream API adapter", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    idempotencyMocks.generate.mockReturnValue("kitesim-command-1");
  });

  it("keeps stored card data separate from the one-shot recharge CVC", async () => {
    apiMocks.PUT.mockResolvedValueOnce({ data: {} });
    apiMocks.POST.mockResolvedValueOnce({ data: { id: 9 } });

    const card = {
      number: "4111111111111111",
      expiryMonth: 12,
      expiryYear: 2030,
      holder: "Test User",
      billingEmail: "test@example.com",
      firstName: "Test",
      lastName: "User",
      phone: "+10000000000",
      country: "US",
      city: "New York",
      address: "1 Test Street",
    };
    await updateKitesimUpstream({ accountId: 7, card, clearCard: false });
    await rechargeKitesimAccount("10", "123");

    expect(apiMocks.PUT).toHaveBeenCalledWith("/v1/admin/kitesim/upstream", {
      body: { accountId: 7, card, clearCard: false },
      params: { header: { "X-CSRF-Token": "kitesim-csrf" } },
    });
    expect(apiMocks.POST).toHaveBeenCalledWith("/v1/admin/kitesim/upstream/recharges", {
      body: { amount: "10", cvc: "123" },
      params: {
        header: {
          "X-CSRF-Token": "kitesim-csrf",
          "Idempotency-Key": "kitesim-command-1",
        },
      },
    });
    expect(idempotencyMocks.generate).toHaveBeenCalledTimes(1);
  });

  it("sends the selected purchase price as the idempotent command ceiling", async () => {
    apiMocks.POST.mockResolvedValueOnce({ data: { id: 8 } });

    await purchaseKitesimNumbers(3, 10, "1.25");

    expect(apiMocks.POST).toHaveBeenCalledWith("/v1/admin/kitesim/upstream/purchases", {
      body: { productId: 3, count: 10, maxUnitPrice: "1.25" },
      params: {
        header: {
          "X-CSRF-Token": "kitesim-csrf",
          "Idempotency-Key": "kitesim-command-1",
        },
      },
    });
    expect(idempotencyMocks.generate).toHaveBeenCalledTimes(1);
  });

  it("uses the resource product catalog and queues renewal for the selected phone", async () => {
    apiMocks.GET.mockResolvedValueOnce({ data: { items: [{ id: 3 }] } });
    apiMocks.POST.mockResolvedValueOnce({ data: { id: 10 } });

    await expect(listKitesimProducts()).resolves.toEqual([{ id: 3 }]);
    await renewKitesimPhone(42, 3, "2.50");

    expect(apiMocks.GET).toHaveBeenCalledWith("/v1/admin/kitesim/products", { signal: undefined });
    expect(apiMocks.POST).toHaveBeenCalledWith("/v1/admin/kitesim/phones/{phoneId}/renewals", {
      body: { productId: 3, maxUnitPrice: "2.50" },
      params: {
        header: {
          "X-CSRF-Token": "kitesim-csrf",
          "Idempotency-Key": "kitesim-command-1",
        },
        path: { phoneId: 42 },
      },
    });
    expect(idempotencyMocks.generate).toHaveBeenCalledTimes(1);
  });

  it("reuses a command key after an ambiguous failure and clears it after success", async () => {
    idempotencyMocks.generate
      .mockReturnValueOnce("kitesim-command-retry")
      .mockReturnValueOnce("kitesim-command-next");
    apiMocks.POST
      .mockRejectedValueOnce(new TypeError("network failed"))
      .mockResolvedValueOnce({ data: { id: 11 } })
      .mockResolvedValueOnce({ data: { id: 12 } });

    await expect(purchaseKitesimNumbers(3, 10, "01.250")).rejects.toThrow("network failed");
    await expect(purchaseKitesimNumbers(3, 10, "1.25")).resolves.toEqual({ id: 11 });

    expect(apiMocks.POST.mock.calls[0][1].params.header["Idempotency-Key"]).toBe("kitesim-command-retry");
    expect(apiMocks.POST.mock.calls[1][1].params.header["Idempotency-Key"]).toBe("kitesim-command-retry");
    expect(idempotencyMocks.generate).toHaveBeenCalledTimes(1);

    await expect(purchaseKitesimNumbers(3, 10, "1.25")).resolves.toEqual({ id: 12 });
    expect(apiMocks.POST.mock.calls[2][1].params.header["Idempotency-Key"]).toBe("kitesim-command-next");
    expect(idempotencyMocks.generate).toHaveBeenCalledTimes(2);
  });

  it("reuses the recharge key when equivalent amount strings retry after a network failure", async () => {
    idempotencyMocks.generate.mockReturnValue("kitesim-recharge-retry");
    apiMocks.POST
      .mockRejectedValueOnce(new TypeError("network failed"))
      .mockRejectedValueOnce(new TypeError("network failed again"))
      .mockResolvedValueOnce({ data: { id: 14 } });

    await expect(rechargeKitesimAccount("10", "123")).rejects.toThrow("network failed");
    await expect(rechargeKitesimAccount("10.0", "123")).rejects.toThrow("network failed again");
    await expect(rechargeKitesimAccount("010.00", "123")).resolves.toEqual({ id: 14 });

    expect(apiMocks.POST.mock.calls[0][1].params.header["Idempotency-Key"]).toBe("kitesim-recharge-retry");
    expect(apiMocks.POST.mock.calls[1][1].params.header["Idempotency-Key"]).toBe("kitesim-recharge-retry");
    expect(apiMocks.POST.mock.calls[2][1].params.header["Idempotency-Key"]).toBe("kitesim-recharge-retry");
    expect(apiMocks.POST.mock.calls[2][1].body).toEqual({ amount: "010.00", cvc: "123" });
    expect(idempotencyMocks.generate).toHaveBeenCalledTimes(1);
  });

  it("reuses the renewal key for equivalent unit price ceilings after a network failure", async () => {
    idempotencyMocks.generate.mockReturnValue("kitesim-renewal-retry");
    apiMocks.POST
      .mockRejectedValueOnce(new TypeError("network failed"))
      .mockResolvedValueOnce({ data: { id: 15 } });

    await expect(renewKitesimPhone(42, 3, "02.500")).rejects.toThrow("network failed");
    await expect(renewKitesimPhone(42, 3, "2.5")).resolves.toEqual({ id: 15 });

    expect(apiMocks.POST.mock.calls[0][1].params.header["Idempotency-Key"]).toBe("kitesim-renewal-retry");
    expect(apiMocks.POST.mock.calls[1][1].params.header["Idempotency-Key"]).toBe("kitesim-renewal-retry");
    expect(idempotencyMocks.generate).toHaveBeenCalledTimes(1);
  });

  it("clears a command key after a deterministic client error", async () => {
    idempotencyMocks.generate
      .mockReturnValueOnce("kitesim-command-conflict")
      .mockReturnValueOnce("kitesim-command-after-conflict");
    apiMocks.POST
      .mockRejectedValueOnce(new IamApiError(409, { message: "price changed" }))
      .mockResolvedValueOnce({ data: { id: 13 } });

    await expect(renewKitesimPhone(42, 3, "2.50")).rejects.toMatchObject({ status: 409 });
    await expect(renewKitesimPhone(42, 3, "2.50")).resolves.toEqual({ id: 13 });

    expect(apiMocks.POST.mock.calls[0][1].params.header["Idempotency-Key"]).toBe("kitesim-command-conflict");
    expect(apiMocks.POST.mock.calls[1][1].params.header["Idempotency-Key"]).toBe("kitesim-command-after-conflict");
    expect(idempotencyMocks.generate).toHaveBeenCalledTimes(2);
  });
});
