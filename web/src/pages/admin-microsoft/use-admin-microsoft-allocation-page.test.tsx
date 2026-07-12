// @vitest-environment jsdom

import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type {
  AdminMicrosoftAllocation,
  AdminMicrosoftAllocationListResponse,
} from "./admin-microsoft-types";

const apiMocks = vi.hoisted(() => ({ listAllocations: vi.fn() }));

vi.mock("../../lib/admin-microsoft-api", () => ({
  listAdminMicrosoftAllocations: apiMocks.listAllocations,
}));

import { useAdminMicrosoftAllocationPage } from "./use-admin-microsoft-allocation-page";

function allocation(id: number): AdminMicrosoftAllocation {
  return {
    type: "microsoft",
    id,
    orderNo: `ORDER-${id}`,
    projectId: 1,
    projectName: "Project",
    projectLogoUrl: null,
    resourceId: 55,
    mailbox: "main",
    supplyScope: "owned",
    deliveryEmail: `mail-${id}@example.com`,
    status: "allocated",
    serviceMode: "code",
    orderStatus: "active",
    payAmount: "1.00",
    buyerEmail: "buyer@example.com",
    verificationCode: null,
    createdAt: "2026-07-11T08:00:00Z",
    receiveUntil: null,
  };
}

function page(
  items: AdminMicrosoftAllocation[],
  total: number,
  offset: number,
  limit: number
): AdminMicrosoftAllocationListResponse {
  return { items, total, offset, limit };
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => {
    resolve = done;
  });
  return { promise, resolve };
}

describe("admin Microsoft allocation page loader", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    window.localStorage.setItem("page-size", "10");
  });

  afterEach(() => {
    cleanup();
    window.localStorage.clear();
  });

  it("issues exactly one request for each selected page", async () => {
    const rows = Array.from({ length: 25 }, (_, index) => allocation(index + 1));
    const secondPage = deferred<AdminMicrosoftAllocationListResponse>();
    apiMocks.listAllocations
      .mockResolvedValueOnce(page(rows.slice(0, 10), 125, 0, 10))
      .mockReturnValueOnce(secondPage.promise);

    const { result } = renderHook(() => useAdminMicrosoftAllocationPage(55));

    await waitFor(() => expect(result.current.loading).toBe(false));
    expect(apiMocks.listAllocations).toHaveBeenNthCalledWith(
      1,
      55,
      0,
      10,
      expect.any(AbortSignal)
    );

    act(() => result.current.setPage(2));
    await waitFor(() => expect(apiMocks.listAllocations).toHaveBeenCalledTimes(2));
    act(() => secondPage.resolve(page(rows.slice(10, 20), 125, 10, 10)));
    await waitFor(() => expect(result.current.loading).toBe(false));
    expect(result.current.items.map((item) => item.id)).toEqual(
      rows.slice(10, 20).map((item) => item.id)
    );
  });

  it("clamps a page after the server total shrinks", async () => {
    const rows = Array.from({ length: 35 }, (_, index) => allocation(index + 1));
    apiMocks.listAllocations
      .mockResolvedValueOnce(page(rows.slice(0, 10), 35, 0, 10))
      .mockResolvedValueOnce(page([], 15, 30, 10))
      .mockResolvedValueOnce(page(rows.slice(10, 15), 15, 10, 10));

    const { result } = renderHook(() => useAdminMicrosoftAllocationPage(55));
    await waitFor(() => expect(result.current.loading).toBe(false));

    act(() => result.current.setPage(4));
    await waitFor(() => expect(result.current.page).toBe(2));
    await waitFor(() => expect(apiMocks.listAllocations).toHaveBeenCalledTimes(3));
    await waitFor(() => expect(result.current.loading).toBe(false));
    expect(result.current.total).toBe(15);
  });

  it("surfaces a backend error without inventing aggregate fallback data", async () => {
    const loadError = new Error("allocation page unavailable");
    apiMocks.listAllocations.mockRejectedValueOnce(loadError);

    const { result } = renderHook(() => useAdminMicrosoftAllocationPage(55));
    await waitFor(() => expect(result.current.loading).toBe(false));
    expect(result.current.error).toBe(loadError);
    expect(result.current.items).toEqual([]);
    expect(result.current.total).toBe(0);
  });

  it("aborts stale requests when the resource changes or the tab unmounts", async () => {
    apiMocks.listAllocations.mockImplementation(
      (
        _resourceId: number,
        _offset: number,
        _limit: number,
        signal: AbortSignal
      ) =>
        new Promise<AdminMicrosoftAllocationListResponse>((_resolve, reject) => {
          signal.addEventListener(
            "abort",
            () => reject(new DOMException("Aborted", "AbortError")),
            { once: true }
          );
        })
    );

    const { rerender, unmount } = renderHook(
      ({ resourceId }) => useAdminMicrosoftAllocationPage(resourceId),
      { initialProps: { resourceId: 55 } }
    );
    await waitFor(() => expect(apiMocks.listAllocations).toHaveBeenCalledTimes(1));
    const firstSignal = apiMocks.listAllocations.mock.calls[0]?.[3] as AbortSignal;

    rerender({ resourceId: 66 });
    await waitFor(() => expect(apiMocks.listAllocations).toHaveBeenCalledTimes(2));
    expect(firstSignal.aborted).toBe(true);
    const secondSignal = apiMocks.listAllocations.mock.calls[1]?.[3] as AbortSignal;

    unmount();
    expect(secondSignal.aborted).toBe(true);
  });
});
