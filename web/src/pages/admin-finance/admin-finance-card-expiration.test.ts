import { beforeEach, describe, expect, it, vi } from "vitest";

const apiMocks = vi.hoisted(() => ({ PATCH: vi.fn() }));

vi.mock("@/lib/api-client", () => ({
  apiClient: apiMocks,
  csrfHeader: () => ({ "X-CSRF-Token": "finance-csrf" }),
  unwrap: async (result: { data?: unknown }) => result.data,
}));

vi.mock("@/lib/idempotency", () => ({
  generateIdempotencyKey: vi.fn(),
}));

import {
  setFinanceCardKeysExpireAt,
  setFinanceCardKeysExpireAtByFilter,
} from "./admin-finance-api";

describe("card key bulk expiration API", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    apiMocks.PATCH.mockResolvedValue({
      data: { requested: 1, affected: 1, skipped: 0 },
    });
  });

  it("sends ids and filter selections to the expiration endpoint", async () => {
    const expireAt = "2026-09-01T00:00:00.000Z";

    await setFinanceCardKeysExpireAt(["CARD-A", "CARD-A", ""], expireAt);
    await setFinanceCardKeysExpireAtByFilter(
      { ownerRole: "supplier", search: "owner@example.com", status: "enabled" },
      expireAt
    );

    expect(apiMocks.PATCH).toHaveBeenNthCalledWith(
      1,
      "/v1/admin/cards/expiration",
      {
        body: {
          expireAt,
          selection: { mode: "ids", cardKeys: ["CARD-A"] },
        },
        params: { header: { "X-CSRF-Token": "finance-csrf" } },
      }
    );
    expect(apiMocks.PATCH).toHaveBeenNthCalledWith(
      2,
      "/v1/admin/cards/expiration",
      {
        body: {
          expireAt,
          selection: {
            mode: "filter",
            filter: {
              ownerGroupId: undefined,
              ownerRole: "supplier",
              search: "owner@example.com",
              status: "enabled",
            },
          },
        },
        params: { header: { "X-CSRF-Token": "finance-csrf" } },
      }
    );
  });
});
