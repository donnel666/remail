import { useCallback, useEffect, useState } from "react";

import { useSharedPageSize } from "../../hooks/use-shared-page-size";
import { listAdminDaumAllocations } from "../../lib/admin-daum-api";

import type { AdminDaumAllocation } from "./admin-daum-types";

export function useAdminDaumAllocationPage(
  resourceId: number
) {
  const [pageSize, setPageSize] = useSharedPageSize();
  const [pagination, setPagination] = useState({ page: 1, resourceId });
  const [items, setItems] = useState<AdminDaumAllocation[]>([]);
  const [total, setTotal] = useState(0);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const page = pagination.resourceId === resourceId ? pagination.page : 1;
  const setPage = useCallback(
    (nextPage: number) => {
      setPagination({ page: Math.max(1, nextPage), resourceId });
    },
    [resourceId]
  );

  useEffect(() => setPage(1), [pageSize, setPage]);

  useEffect(() => {
    setItems([]);
    setTotal(0);
    setError(null);
  }, [resourceId]);

  useEffect(() => {
    const controller = new AbortController();
    setLoading(true);
    setError(null);

    void listAdminDaumAllocations(
      resourceId,
      (page - 1) * pageSize,
      pageSize,
      controller.signal
    )
      .then((result) => {
        if (controller.signal.aborted) return;
        setTotal(result.total);
        const lastPage = Math.max(1, Math.ceil(result.total / pageSize));
        if (page > lastPage) {
          setPage(lastPage);
          return;
        }
        setItems(result.items);
      })
      .catch((loadError: unknown) => {
        if (controller.signal.aborted) return;
        setError(loadError);
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false);
      });

    return () => controller.abort();
  }, [page, pageSize, resourceId, setPage]);

  return {
    error,
    items,
    loading,
    page,
    pageSize,
    setPage,
    setPageSize,
    total,
  };
}
