// @vitest-environment jsdom

import { beforeEach, describe, expect, it, vi } from "vitest";

const apiMocks = vi.hoisted(() => ({
  GET: vi.fn(),
  POST: vi.fn(),
  PATCH: vi.fn(),
  DELETE: vi.fn(),
}));
const idempotencyMock = vi.hoisted(() => vi.fn());

vi.mock("@/lib/api-client", () => ({
  apiClient: apiMocks,
  csrfHeader: () => ({ "X-CSRF-Token": "user-csrf" }),
  unwrap: async (result: { data?: unknown }) => result.data,
}));

vi.mock("@/lib/idempotency", () => ({
  generateIdempotencyKey: idempotencyMock,
}));

import {
  adjustAdminUsersWalletByIds,
  getAdminUserDashboardStats,
  getAdminUserRealtimeUsage,
  listAdminUsers,
  setAdminUsersEnabledByFilter,
  setAdminUsersEnabledByIds,
} from "./admin-users-api";

const COMMAND_HEADER = {
  header: {
    "X-CSRF-Token": "user-csrf",
    "Idempotency-Key": "user-command-1",
  },
};

describe("admin user bulk API adapter", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    idempotencyMock.mockReturnValue("user-command-1");
  });

  it("sends enable as an ids command, dedupes ids, and drops requested from the result", async () => {
    apiMocks.POST.mockResolvedValueOnce({
      data: { requested: 2, affected: 2, skipped: 0 },
    });

    await expect(
      setAdminUsersEnabledByIds([2, 2, 3, -1, 0], true)
    ).resolves.toEqual({ affected: 2, skipped: 0 });

    expect(apiMocks.POST).toHaveBeenCalledWith("/v1/admin/users/enable", {
      body: { selection: { mode: "ids", userIds: [2, 3] } },
      params: COMMAND_HEADER,
    });
  });

  it("maps the disable action to the disable endpoint with a normalized filter", async () => {
    apiMocks.POST.mockResolvedValueOnce({
      data: { requested: 5, affected: 4, skipped: 1 },
    });

    await expect(
      setAdminUsersEnabledByFilter(
        { search: " ann ", role: "user", enabled: true },
        false
      )
    ).resolves.toEqual({ affected: 4, skipped: 1 });

    expect(apiMocks.POST).toHaveBeenCalledWith("/v1/admin/users/disable", {
      body: {
        selection: {
          mode: "filter",
          filter: {
            search: "ann",
            role: "user",
            enabled: true,
            userGroupId: undefined,
            createdFrom: undefined,
            createdTo: undefined,
          },
        },
      },
      params: COMMAND_HEADER,
    });
  });

  it("sends a bulk balance adjustment as a signed decimal string", async () => {
    apiMocks.POST.mockResolvedValueOnce({
      data: { requested: 1, affected: 1, skipped: 0 },
    });

    await adjustAdminUsersWalletByIds([5], -50, "manual audit");

    expect(apiMocks.POST).toHaveBeenCalledWith("/v1/admin/wallets/adjust", {
      body: {
        selection: { mode: "ids", userIds: [5] },
        amount: "-50.000000",
        reason: "manual audit",
      },
      params: COMMAND_HEADER,
    });
  });

  it("loads dashboard and real-time usage for the selected user", async () => {
    apiMocks.GET
      .mockResolvedValueOnce({ data: { walletBalance: 2.48 } })
      .mockResolvedValueOnce({
        data: { activeRequests: 3, requestsPerMinute: 12 },
      });

    await getAdminUserDashboardStats(42, {
      createdFrom: "2026-07-01T00:00:00.000Z",
      createdTo: "2026-07-02T00:00:00.000Z",
    });
    await getAdminUserRealtimeUsage(42);

    expect(apiMocks.GET).toHaveBeenNthCalledWith(
      1,
      "/v1/admin/users/{userId}/dashboard",
      {
        params: {
          path: { userId: 42 },
          query: {
            createdFrom: "2026-07-01T00:00:00.000Z",
            createdTo: "2026-07-02T00:00:00.000Z",
          },
        },
      }
    );
    expect(apiMocks.GET).toHaveBeenNthCalledWith(
      2,
      "/v1/admin/users/{userId}/apikeys/realtime-usage",
      { params: { path: { userId: 42 } } }
    );
  });

  it("preserves the read-only QQ number in an admin user detail model", async () => {
    apiMocks.GET
      .mockResolvedValueOnce({
        data: {
          users: [{
            id: 42,
            email: "bound@example.com",
            nickname: "Bound",
            role: "user",
            userGroup: {
              id: 1,
              code: "normal",
              name: "Normal",
              description: "",
              enabled: true,
              apiConcurrencyLimit: 3,
              priceDiscountRatio: "1",
              topupThreshold: "0",
              autoUpgradeEnabled: false,
            },
            qqNumber: "123456789",
            hasLocalPassword: true,
            enabled: true,
            createdAt: "2026-09-01T00:00:00Z",
            updatedAt: "2026-09-01T00:00:00Z",
          }],
          total: 1,
          offset: 0,
          limit: 20,
        },
      })
      .mockResolvedValueOnce({ data: { balances: [] } });

    const result = await listAdminUsers({}, 0, 20);

    expect(result.users[0].qqNumber).toBe("123456789");
  });
});
