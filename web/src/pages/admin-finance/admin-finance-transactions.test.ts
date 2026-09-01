import { beforeEach, describe, expect, it, vi } from "vitest";

const apiMocks = vi.hoisted(() => ({ GET: vi.fn() }));

vi.mock("@/lib/api-client", () => ({
  apiClient: apiMocks,
  csrfHeader: () => ({}),
  unwrap: async (result: { data?: unknown }) => result.data,
}));

import { listFinanceTransactions } from "./admin-finance-api";

describe("admin finance transaction filters", () => {
  beforeEach(() => vi.clearAllMocks());

  it.each(["all", "referral_cashback", "activity"] as const)(
    "sends the %s business category and returns server-side facets",
    async (category) => {
      const totals = { all: 10, referral_cashback: 2, activity: 4 } as const;
      apiMocks.GET.mockResolvedValue({
        data: {
          items: [],
          total: totals[category],
          offset: 0,
          limit: 20,
          facets: {
            all: 10,
            recharge: 2,
            spend: 1,
            refund: 1,
            referralCashback: 2,
            activity: 4,
          },
        },
      });

      await expect(
        listFinanceTransactions({ category, search: "alice" })
      ).resolves.toMatchObject({
        total: totals[category],
        facets: { all: 10, recharge: 2, referralCashback: 2, activity: 4 },
      });
      expect(apiMocks.GET).toHaveBeenCalledWith("/v1/admin/transactions", {
        params: {
          query: {
            search: "alice",
            type: undefined,
            category,
            direction: undefined,
            createdFrom: undefined,
            createdTo: undefined,
            offset: 0,
            limit: 20,
          },
        },
      });
    }
  );
});
