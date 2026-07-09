import { useCallback, useEffect, useMemo, useRef, useState } from "react";

export const DEFAULT_BLOCK_PAGE_SIZE = 10_000;
const PREFETCH_THRESHOLD = 0.8;

export interface BlockPageResult<T> {
  items: T[];
  nextAfterId?: number | null;
  total: number;
}

export interface BlockLoadCursor {
  afterId?: number;
}

interface UseBlockPagedListOptions<T> {
  activePage: number;
  blockSize?: number;
  filterKey: string;
  loadBlock: (
    offset: number,
    limit: number,
    cursor?: BlockLoadCursor
  ) => Promise<BlockPageResult<T>>;
  onError?: (error: unknown) => void;
  pageSize: number;
}

interface CachedBlock<T> {
  items: T[];
  nextAfterId?: number | null;
  total: number;
}

function blockOffsetForIndex(index: number, blockSize: number) {
  return Math.floor(Math.max(index, 0) / blockSize) * blockSize;
}

export function useBlockPagedList<T>({
  activePage,
  blockSize = DEFAULT_BLOCK_PAGE_SIZE,
  filterKey,
  loadBlock,
  onError,
  pageSize,
}: UseBlockPagedListOptions<T>) {
  const cacheRef = useRef(new Map<number, CachedBlock<T>>());
  const pendingRef = useRef(new Map<number, Promise<void>>());
  const loadSeqRef = useRef(0);
  const [loading, setLoading] = useState(true);
  const [total, setTotal] = useState(0);
  const [version, setVersion] = useState(0);

  const pageStart = Math.max(activePage - 1, 0) * pageSize;
  const currentBlockOffset = blockOffsetForIndex(pageStart, blockSize);

  const bumpVersion = useCallback(() => {
    setVersion((value) => value + 1);
  }, []);

  const loadBlockAt = useCallback(
    async (offset: number, foreground: boolean) => {
      if (cacheRef.current.has(offset)) return;

      const pending = pendingRef.current.get(offset);
      if (pending) {
        if (foreground) await pending;
        return;
      }

      const seq = loadSeqRef.current;
      if (foreground) setLoading(true);
      const previousBlock = cacheRef.current.get(offset - blockSize);
      const cursor =
        offset > 0 && previousBlock?.nextAfterId
          ? { afterId: previousBlock.nextAfterId }
          : undefined;

      const request = loadBlock(offset, blockSize, cursor)
        .then((response) => {
          if (loadSeqRef.current !== seq) return;
          cacheRef.current.set(offset, {
            items: response.items,
            nextAfterId: response.nextAfterId ?? null,
            total: response.total,
          });
          setTotal(response.total);
          bumpVersion();
        })
        .catch((error) => {
          if (loadSeqRef.current === seq) onError?.(error);
        })
        .finally(() => {
          if (pendingRef.current.get(offset) === request) {
            pendingRef.current.delete(offset);
          }
          if (foreground && loadSeqRef.current === seq) setLoading(false);
        });

      pendingRef.current.set(offset, request);
      await request;
    },
    [blockSize, bumpVersion, loadBlock, onError]
  );

  const clear = useCallback(() => {
    loadSeqRef.current += 1;
    cacheRef.current.clear();
    pendingRef.current.clear();
    setTotal(0);
    setLoading(true);
    bumpVersion();
  }, [bumpVersion]);

  const refresh = useCallback(async () => {
    clear();
    await loadBlockAt(currentBlockOffset, true);
  }, [clear, currentBlockOffset, loadBlockAt]);

  useEffect(() => {
    clear();
  }, [clear, filterKey]);

  useEffect(() => {
    void loadBlockAt(currentBlockOffset, true);
  }, [currentBlockOffset, filterKey, loadBlockAt]);

  useEffect(() => {
    const block = cacheRef.current.get(currentBlockOffset);
    if (!block) return;

    const pageEnd = pageStart + pageSize;
    const prefetchAt = currentBlockOffset + blockSize * PREFETCH_THRESHOLD;
    const nextBlockOffset = currentBlockOffset + blockSize;
    if (pageEnd >= prefetchAt && nextBlockOffset < block.total) {
      void loadBlockAt(nextBlockOffset, false);
    }
  }, [blockSize, currentBlockOffset, loadBlockAt, pageSize, pageStart, version]);

  const currentBlock = cacheRef.current.get(currentBlockOffset);
  const items = currentBlock?.items ?? [];
  const localStart = pageStart - currentBlockOffset;
  const pagedItems = useMemo(
    () => items.slice(localStart, localStart + pageSize),
    [items, localStart, pageSize, version]
  );

  const loadedItems = useMemo(
    () => Array.from(cacheRef.current.values()).flatMap((block) => block.items),
    [version]
  );

  const updateLoadedItems = useCallback(
    (updater: (items: T[]) => T[]) => {
      for (const [offset, block] of cacheRef.current.entries()) {
        cacheRef.current.set(offset, {
          ...block,
          items: updater(block.items),
        });
      }
      bumpVersion();
    },
    [bumpVersion]
  );

  const adjustTotal = useCallback((delta: number) => {
    const nextTotalByOffset = new Map<number, CachedBlock<T>>();
    for (const [offset, block] of cacheRef.current.entries()) {
      nextTotalByOffset.set(offset, {
        ...block,
        total: Math.max(block.total + delta, 0),
      });
    }
    cacheRef.current = nextTotalByOffset;
    setTotal((value) => Math.max(value + delta, 0));
    bumpVersion();
  }, [bumpVersion]);

  return {
    adjustTotal,
    loadedItems,
    loading: loading && !currentBlock,
    pagedItems,
    refresh,
    total,
    updateLoadedItems,
  };
}
