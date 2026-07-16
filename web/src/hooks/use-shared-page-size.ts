import { useSyncExternalStore } from "react";

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

let currentPageSize = readStoredPageSize();
const subscribers = new Set<() => void>();
let listeningForStorage = false;

function emitPageSizeChange() {
  for (const subscriber of subscribers) subscriber();
}

function handleStorage(event: StorageEvent) {
  if (event.key !== PAGE_SIZE_STORAGE_KEY) return;
  const nextPageSize = normalizePageSize(event.newValue);
  if (nextPageSize === currentPageSize) return;
  currentPageSize = nextPageSize;
  emitPageSizeChange();
}

function subscribe(subscriber: () => void) {
  const firstSubscriber = subscribers.size === 0;
  subscribers.add(subscriber);
  if (firstSubscriber) {
    const storedPageSize = readStoredPageSize();
    if (storedPageSize !== currentPageSize) {
      currentPageSize = storedPageSize;
      emitPageSizeChange();
    }
  }
  if (!listeningForStorage && typeof window !== "undefined") {
    window.addEventListener("storage", handleStorage);
    listeningForStorage = true;
  }
  return () => {
    subscribers.delete(subscriber);
    if (subscribers.size === 0 && listeningForStorage) {
      window.removeEventListener("storage", handleStorage);
      listeningForStorage = false;
    }
  };
}

function getPageSize() {
  return currentPageSize;
}

function setSharedPageSize(nextPageSize: number) {
  const normalized = normalizePageSize(nextPageSize);
  writeStoredPageSize(normalized);
  if (normalized === currentPageSize) return;
  currentPageSize = normalized;
  emitPageSizeChange();
}

export function useSharedPageSize() {
  const pageSize = useSyncExternalStore(subscribe, getPageSize, getPageSize);
  return [pageSize, setSharedPageSize] as const;
}
