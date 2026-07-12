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
});
