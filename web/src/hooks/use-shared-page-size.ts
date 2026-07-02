import { useCallback, useEffect, useState } from "react";

const DEFAULT_PAGE_SIZE = 10;
const PAGE_SIZE_STORAGE_KEY = "page-size";
const PAGE_SIZE_OPTIONS = [10, 20, 50, 100];

function normalizePageSize(value: unknown): number {
  const parsed =
    typeof value === "number" ? value : Number.parseInt(String(value), 10);
  return PAGE_SIZE_OPTIONS.includes(parsed) ? parsed : DEFAULT_PAGE_SIZE;
}

function readStoredPageSize(): number {
  if (typeof window === "undefined") return DEFAULT_PAGE_SIZE;
  try {
    return normalizePageSize(window.localStorage.getItem(PAGE_SIZE_STORAGE_KEY));
  } catch {
    return DEFAULT_PAGE_SIZE;
  }
}

function writeStoredPageSize(pageSize: number) {
  if (typeof window === "undefined") return;
  try {
    window.localStorage.setItem(PAGE_SIZE_STORAGE_KEY, String(pageSize));
  } catch {
    // Ignore storage failures so pagination still works in restricted browsers.
  }
}

export function useSharedPageSize() {
  const [pageSize, setPageSizeState] = useState(readStoredPageSize);

  const setPageSize = useCallback((nextPageSize: number) => {
    const normalized = normalizePageSize(nextPageSize);
    setPageSizeState(normalized);
    writeStoredPageSize(normalized);
  }, []);

  useEffect(() => {
    const handleStorage = (event: StorageEvent) => {
      if (event.key !== PAGE_SIZE_STORAGE_KEY) return;
      setPageSizeState(normalizePageSize(event.newValue));
    };

    window.addEventListener("storage", handleStorage);
    return () => {
      window.removeEventListener("storage", handleStorage);
    };
  }, []);

  return [pageSize, setPageSize] as const;
}
