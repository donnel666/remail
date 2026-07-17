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
});
