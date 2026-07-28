import { useCallback, useEffect, useMemo, useRef, useState } from "react";

export const DEFAULT_BLOCK_PAGE_SIZE = 1_000;
const PREFETCH_THRESHOLD = 0.8;

export interface BlockPageResult<T, M = undefined> {
  items: T[];
  meta?: M;
  nextAfterId?: number | null;
  total?: number;
}

export interface BlockLoadCursor {
  afterId?: number;
}

interface UseBlockPagedListOptions<T, M = undefined> {
  activePage: number;
  blockSize?: number;
  filterKey: string;
  loadBlock: (
    offset: number,
    limit: number,
    cursor: BlockLoadCursor | undefined,
    signal: AbortSignal
  ) => Promise<BlockPageResult<T, M>>;
  onError?: (error: unknown) => void;
  onLoaded?: (response: BlockPageResult<T, M>) => void;
  pageSize: number;
}

interface CachedBlock<T> {
  items: T[];
  nextAfterId?: number | null;
}

function blockOffsetForIndex(index: number, blockSize: number) {
  return Math.floor(Math.max(index, 0) / blockSize) * blockSize;
}

export function useBlockPagedList<T, M = undefined>({
  activePage,
  blockSize = DEFAULT_BLOCK_PAGE_SIZE,
  filterKey,
  loadBlock,
  onError,
  onLoaded,
  pageSize,
}: UseBlockPagedListOptions<T, M>) {
  const cacheRef = useRef(new Map<number, CachedBlock<T>>());
  const pendingRef = useRef(new Map<number, Promise<void>>());
  const controllersRef = useRef(new Map<number, AbortController>());
  const loadSeqRef = useRef(0);
  const onErrorRef = useRef(onError);
  const onLoadedRef = useRef(onLoaded);
  onErrorRef.current = onError;
  onLoadedRef.current = onLoaded;
  const [loading, setLoading] = useState(true);
  const [loadedFilterKey, setLoadedFilterKey] = useState<string | null>(null);
  const [total, setTotalState] = useState(0);
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
      const controller = new AbortController();
      controllersRef.current.set(offset, controller);

      const request = loadBlock(offset, blockSize, cursor, controller.signal)
        .then((response) => {
          if (loadSeqRef.current !== seq) return;
          cacheRef.current.set(offset, {
            items: response.items,
            nextAfterId: response.nextAfterId ?? null,
          });
          const observedTotal =
            offset + response.items.length + (response.nextAfterId ? 1 : 0);
          setTotalState((current) =>
            response.total ?? Math.max(current, observedTotal)
          );
          if (foreground) setLoadedFilterKey(filterKey);
          onLoadedRef.current?.(response);
          bumpVersion();
        })
        .catch((error) => {
          if (loadSeqRef.current === seq) onErrorRef.current?.(error);
        })
        .finally(() => {
          if (pendingRef.current.get(offset) === request) {
            pendingRef.current.delete(offset);
          }
          if (controllersRef.current.get(offset) === controller) {
            controllersRef.current.delete(offset);
          }
          if (foreground && loadSeqRef.current === seq) setLoading(false);
        });

      pendingRef.current.set(offset, request);
      await request;
    },
    [blockSize, bumpVersion, filterKey, loadBlock]
  );

  const cancelPending = useCallback(() => {
    loadSeqRef.current += 1;
    controllersRef.current.forEach((controller) => controller.abort());
    controllersRef.current.clear();
    pendingRef.current.clear();
  }, []);

  const clear = useCallback(() => {
    cancelPending();
    cacheRef.current.clear();
    setLoadedFilterKey(null);
    setTotalState(0);
    setLoading(true);
    bumpVersion();
  }, [bumpVersion, cancelPending]);

  const refresh = useCallback(async () => {
    clear();
    await loadBlockAt(currentBlockOffset, true);
  }, [clear, currentBlockOffset, loadBlockAt]);

  useEffect(() => {
    clear();
  }, [clear, filterKey]);

  useEffect(() => cancelPending, [cancelPending]);

  useEffect(() => {
    void loadBlockAt(currentBlockOffset, true);
  }, [currentBlockOffset, filterKey, loadBlockAt]);

  useEffect(() => {
    const block = cacheRef.current.get(currentBlockOffset);
    if (!block) return;

    const pageEnd = pageStart + pageSize;
    const prefetchAt = currentBlockOffset + blockSize * PREFETCH_THRESHOLD;
    const nextBlockOffset = currentBlockOffset + blockSize;
    if (pageEnd >= prefetchAt && block.nextAfterId) {
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
    setTotalState((value) => Math.max(value + delta, 0));
  }, []);

  const setTotal = useCallback((value: number) => {
    setTotalState(Math.max(value, 0));
  }, []);

  return {
    adjustTotal,
    loadedItems,
    loaded: loadedFilterKey === filterKey,
    loading: loading && !currentBlock,
    pagedItems,
    refresh,
    setTotal,
    total,
    updateLoadedItems,
  };
}
