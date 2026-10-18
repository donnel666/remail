import { apiClient as client, csrfHeader, turnstileHeader, unwrap } from "./api-client";
import { generateIdempotencyKey } from "./idempotency";

export type DaumResourceStatus = "pending" | "validating" | "identifying" | "normal" | "abnormal" | "disabled";
export type DaumImportErrorStrategy = "skip" | "abort";
export interface DaumResourceItem {
  id: number;
  version: number;
  type: "daum";
  email: string;
  suffix: "daum.net";
  status: DaumResourceStatus;
  forSale: boolean;
  longLived: boolean;
  qualityScore: number;
  lastSafeError?: string;
  createdAt: string;
  updatedAt: string;
}
export interface DaumListFilter {
  search?: string;
  suffix?: string;
  status?: DaumResourceStatus;
  forSale?: boolean;
  longLived?: boolean;
  createdFrom?: string;
  createdTo?: string;
}
export interface DaumFacets {
  status: Record<DaumResourceStatus | "all", number>;
  forSale: { all: number; yes: number; no: number };
  longLived: { all: number; yes: number; no: number };
  suffixes: Array<{ key: "daum.net"; count: number }>;
}
export interface DaumListResponse {
  items: DaumResourceItem[];
  total?: number;
  nextAfterId?: number | null;
  facets?: DaumFacets;
}
export interface DaumImportResponse {
  importId: number;
  status: "processing" | "imported" | "failed";
  accepted: number;
  imported: number;
  skipped: number;
  failed: number;
  lastSafeError?: string;
}
export interface DaumBulkResponse {
  taskId: string;
  status: string;
  requested: number;
  processed: number;
  affected: number;
  skipped: number;
  reasonCounts: Array<{ reason: string; count: number }>;
}
type DaumSelection = { mode: "ids"; resourceIds: number[] } | { mode: "filter"; filter: ReturnType<typeof listFilter> };

function headers(idempotencyKey = generateIdempotencyKey()) {
  return { ...csrfHeader(), "Idempotency-Key": idempotencyKey };
}
function listFilter(filter: DaumListFilter) {
  const suffix = filter.suffix?.trim().toLowerCase().replace(/^@/, "");
  if (suffix && suffix !== "daum.net") throw new Error("Invalid Daum resource request.");
  return {
    search: filter.search?.trim() || undefined,
    suffix: suffix ? "daum.net" as const : undefined,
    status: filter.status,
    forSale: filter.forSale,
    longLived: filter.longLived,
    createdFrom: filter.createdFrom,
    createdTo: filter.createdTo,
  };
}
function idsSelection(ids: number[]): DaumSelection {
  return { mode: "ids", resourceIds: [...new Set(ids)].filter((id) => Number.isInteger(id) && id > 0) };
}
function readableStatus(status: string): status is DaumResourceStatus {
  return ["pending", "validating", "identifying", "normal", "abnormal", "disabled"].includes(status);
}

export async function listOwnedDaumResources(filter: DaumListFilter = {}, offset = 0, limit = 20, afterId?: number, options: { includeFacets?: boolean; includeTotal?: boolean; signal?: AbortSignal } = {}): Promise<DaumListResponse> {
  const response = await unwrap(await client.GET("/v1/daum/resources", {
    params: { query: { ...listFilter(filter), offset, limit, afterId, includeFacets: options.includeFacets, includeTotal: options.includeTotal } },
    signal: options.signal,
  }));
  const items: DaumResourceItem[] = [];
  for (const item of response.items) {
    if (item.type !== "daum" || !item.email.toLowerCase().endsWith("@daum.net") || !readableStatus(item.status)) continue;
    items.push({
      id: item.id, version: item.version, type: "daum", email: item.email, suffix: "daum.net",
      status: item.status, forSale: item.forSale, longLived: item.longLived,
      qualityScore: item.qualityScore, createdAt: item.createdAt, updatedAt: item.updatedAt,
      ...(item.lastSafeError ? { lastSafeError: item.lastSafeError } : {}),
    });
  }
  return {
    items,
    total: options.includeTotal === false ? undefined : response.total,
    nextAfterId: response.nextAfterId,
    facets: response.facets ? {
      status: {
        all: response.facets.status.all, pending: response.facets.status.pending, validating: response.facets.status.validating,
        identifying: response.facets.status.identifying, normal: response.facets.status.normal,
        abnormal: response.facets.status.abnormal, disabled: response.facets.status.disabled,
      },
      forSale: response.facets.forSale,
      longLived: response.facets.longLived,
      suffixes: response.facets.suffixes.filter((item) => item.key.replace(/^@/, "") === "daum.net").map((item) => ({ key: "daum.net", count: item.count })),
    } : undefined,
  };
}

export async function importDaumResources(file: File, longLived: boolean, turnstileToken: string, errorStrategy: DaumImportErrorStrategy = "skip", signal?: AbortSignal, idempotencyKey?: string): Promise<DaumImportResponse> {
  const body = new FormData();
  body.append("file", file);
  body.append("longLived", String(longLived));
  body.append("errorStrategy", errorStrategy);
  return unwrap(await client.POST("/v1/daum/resources/imports", {
    body: body as never, bodySerializer: (value) => value,
    params: { header: { ...headers(idempotencyKey), ...turnstileHeader(turnstileToken) } }, signal,
  }));
}
export async function getDaumImport(importId: number, signal?: AbortSignal): Promise<DaumImportResponse> {
  return unwrap(await client.GET("/v1/daum/resources/imports/{importId}", { params: { path: { importId } }, signal }));
}
export async function waitForDaumImport(importId: number, options: { signal?: AbortSignal } = {}): Promise<DaumImportResponse> {
  for (let attempt = 0; attempt < 120; attempt += 1) {
    const response = await getDaumImport(importId, options.signal);
    if (response.status !== "processing") return response;
    await abortableDelay(1000, options.signal);
  }
  const response = await getDaumImport(importId, options.signal);
  if (response.status === "processing") throw new Error("The Daum resource import is still processing.");
  return response;
}
export async function validateDaumResource(resourceId: number, signal?: AbortSignal) {
  return unwrap(await client.POST("/v1/daum/resources/{resourceId}/validate", { params: { path: { resourceId }, header: headers() }, signal }));
}
export async function publishDaumResource(resourceId: number, version: number) {
  return unwrap(await client.POST("/v1/daum/resources/{resourceId}/publish", { params: { path: { resourceId }, header: headers() }, body: { version } }));
}
export async function deleteDaumResource(resourceId: number, version: number) {
  await unwrap(await client.DELETE("/v1/daum/resources/{resourceId}", { params: { path: { resourceId }, header: headers() }, body: { version } }));
}
async function bulk(command: "validate" | "publish" | "delete", selection: DaumSelection): Promise<DaumBulkResponse> {
  return unwrap(await client.POST("/v1/daum/resources/bulk/{command}", { params: { path: { command }, header: headers() }, body: { selection } }));
}
export async function getDaumBulkTask(taskId: string): Promise<DaumBulkResponse> {
  return unwrap(await client.GET("/v1/daum/resources/bulk-tasks/{taskId}", { params: { path: { taskId } } }));
}
async function waitForBulkTask(accepted: DaumBulkResponse): Promise<DaumBulkResponse> {
  let current = accepted;
  for (let attempt = 0; attempt < 300; attempt += 1) {
    if (current.status !== "queued" && current.status !== "running") return current;
    try {
      current = { ...await getDaumBulkTask(accepted.taskId), taskId: accepted.taskId };
    } catch {
      // Keep the accepted receipt when only its status request failed.
      return current;
    }
    if (attempt < 299 && (current.status === "queued" || current.status === "running")) await abortableDelay(1000);
  }
  return current;
}
export const validateDaumResourcesBatch = (ids: number[]) => bulk("validate", idsSelection(ids));
export const validateDaumResourcesByFilter = (filter: DaumListFilter) => bulk("validate", { mode: "filter", filter: listFilter(filter) });
export const publishDaumResourcesBatch = async (ids: number[]) => waitForBulkTask(await bulk("publish", idsSelection(ids)));
export const publishDaumResourcesByFilter = async (filter: DaumListFilter) => waitForBulkTask(await bulk("publish", { mode: "filter", filter: listFilter(filter) }));
export const deleteDaumResourcesBatch = async (ids: number[]) => waitForBulkTask(await bulk("delete", idsSelection(ids)));
export const deleteDaumResourcesByFilter = async (filter: DaumListFilter) => waitForBulkTask(await bulk("delete", { mode: "filter", filter: listFilter(filter) }));

function abortableDelay(ms: number, signal?: AbortSignal) {
  return new Promise<void>((resolve, reject) => {
    if (signal?.aborted) { reject(new DOMException("The operation was aborted.", "AbortError")); return; }
    const cleanup = () => signal?.removeEventListener("abort", onAbort);
    const timer = globalThis.setTimeout(() => { cleanup(); resolve(); }, ms);
    const onAbort = () => { globalThis.clearTimeout(timer); cleanup(); reject(new DOMException("The operation was aborted.", "AbortError")); };
    signal?.addEventListener("abort", onAbort, { once: true });
  });
}
