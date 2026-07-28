// @vitest-environment jsdom

import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import {
  useBlockPagedList,
  type BlockPageResult,
} from "./use-block-paged-list";

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => {
    resolve = done;
  });
  return { promise, resolve };
}

describe("useBlockPagedList", () => {
  afterEach(() => {
    cleanup();
  });

  it("ignores an older response that finishes after a refresh", async () => {
    type Response = BlockPageResult<number, { revision: string }>;

    const oldRequest = deferred<Response>();
    const refreshedRequest = deferred<Response>();
    const loadBlock = vi
      .fn()
      .mockReturnValueOnce(oldRequest.promise)
      .mockReturnValueOnce(refreshedRequest.promise);
    const onLoaded = vi.fn();

    const { result } = renderHook(() =>
      useBlockPagedList<number, { revision: string }>({
        activePage: 1,
        blockSize: 10,
        filterKey: "same-filter",
        loadBlock,
        onLoaded,
        pageSize: 5,
      })
    );

    await waitFor(() => expect(loadBlock).toHaveBeenCalledTimes(1));

    let refreshPromise!: Promise<void>;
    act(() => {
      refreshPromise = result.current.refresh();
    });
    await waitFor(() => expect(loadBlock).toHaveBeenCalledTimes(2));

    await act(async () => {
      refreshedRequest.resolve({
        items: [20, 21],
        meta: { revision: "new" },
        total: 2,
      });
      await refreshPromise;
    });

    expect(onLoaded).toHaveBeenCalledTimes(1);
    expect(onLoaded).toHaveBeenLastCalledWith(
      expect.objectContaining({ meta: { revision: "new" } })
    );
    expect(result.current.pagedItems).toEqual([20, 21]);
    expect(result.current.total).toBe(2);

    await act(async () => {
      oldRequest.resolve({
        items: [10, 11, 12],
        meta: { revision: "old" },
        total: 3,
      });
      await oldRequest.promise;
    });

    expect(onLoaded).toHaveBeenCalledTimes(1);
    expect(result.current.pagedItems).toEqual([20, 21]);
    expect(result.current.total).toBe(2);
  });

  it("aborts the stale request when filters change", async () => {
    let staleSignal!: AbortSignal;
    const loadBlock = vi
      .fn()
      .mockImplementationOnce(
        (_offset, _limit, _cursor, signal: AbortSignal) => {
          staleSignal = signal;
          return new Promise<BlockPageResult<number>>((_resolve, reject) => {
            signal.addEventListener(
              "abort",
              () => reject(new DOMException("Aborted", "AbortError")),
              { once: true }
            );
          });
        }
      )
      .mockResolvedValueOnce({ items: [2], total: 1 });

    const { rerender } = renderHook(
      ({ filterKey }) =>
        useBlockPagedList<number>({
          activePage: 1,
          blockSize: 10,
          filterKey,
          loadBlock,
          pageSize: 5,
        }),
      { initialProps: { filterKey: "old" } }
    );

    await waitFor(() => expect(loadBlock).toHaveBeenCalledTimes(1));
    rerender({ filterKey: "new" });

    await waitFor(() => expect(loadBlock).toHaveBeenCalledTimes(2));
    expect(staleSignal.aborted).toBe(true);
  });

  it("estimates total without a count and accepts the later exact total", async () => {
    const loadBlock = vi.fn().mockResolvedValue({
      items: [1, 2, 3],
      nextAfterId: 3,
    });
    const { result } = renderHook(() =>
      useBlockPagedList<number>({
        activePage: 1,
        blockSize: 3,
        filterKey: "count-free",
        loadBlock,
        pageSize: 2,
      })
    );

    await waitFor(() => expect(result.current.total).toBe(4));
    act(() => result.current.setTotal(5_000_000));
    expect(result.current.total).toBe(5_000_000);
  });

  it("does not retry a failed block when callback identities change", async () => {
    const loadBlock = vi.fn().mockRejectedValue(new Error("database timeout"));
    const onError = vi.fn();
    const { result, rerender } = renderHook(
      ({ revision }) =>
        useBlockPagedList<number>({
          activePage: 1,
          filterKey: "failed-filter",
          loadBlock,
          onError: (error) => onError(revision, error),
          pageSize: 5,
        }),
      { initialProps: { revision: 1 } }
    );

    await waitFor(() => expect(onError).toHaveBeenCalledTimes(1));
    await waitFor(() => expect(result.current.loading).toBe(false));
    rerender({ revision: 2 });
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 0));
    });

    expect(loadBlock).toHaveBeenCalledTimes(1);
    expect(onError).toHaveBeenCalledTimes(1);
  });

  it("aborts pending requests when unmounted", async () => {
    let signal!: AbortSignal;
    const loadBlock = vi.fn(
      (_offset, _limit, _cursor, nextSignal: AbortSignal) => {
        signal = nextSignal;
        return new Promise<BlockPageResult<number>>(() => undefined);
      }
    );
    const { unmount } = renderHook(() =>
      useBlockPagedList<number>({
        activePage: 1,
        filterKey: "unmount",
        loadBlock,
        pageSize: 5,
      })
    );

    await waitFor(() => expect(loadBlock).toHaveBeenCalledTimes(1));
    unmount();
    expect(signal.aborted).toBe(true);
  });
});
