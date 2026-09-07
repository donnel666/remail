import type { components } from "./openapi/schema";
import { apiClient as client, csrfHeader, turnstileHeader, unwrap } from "./api-client";
import { generateIdempotencyKey } from "./idempotency";
import type { AdminProtoFacets, AdminProtoListFilter, AdminProtoResourceSelection, AdminProtoBulkResponse } from "@/pages/admin-proto/admin-proto-types";

export type ProtoResource = components["schemas"]["ProtoResource"];
export type ProtoResourceStatus = components["schemas"]["ProtoResourceStatus"];
export type ProtoImportResponse = components["schemas"]["ProtoImportResponse"];
export type ProtoImportItemsResponse = components["schemas"]["ProtoImportItemsResponse"];
export type ImportErrorStrategy = "skip" | "abort";
export type ResourceListFilter = AdminProtoListFilter;
export type ResourceBulkFilter = ResourceListFilter;
export type ResourceListResponse = Omit<components["schemas"]["ProtoResourceListResponse"], "facets" | "total"> & {
  facets?: AdminProtoFacets;
  total?: number;
  nextAfterId?: number | null;
};

export function protoCommandHeaders() {
  return { ...csrfHeader(), "Idempotency-Key": generateIdempotencyKey() };
}
export function protoFilter(filter: ResourceListFilter) {
  return { ...filter, search: filter.search?.trim() || undefined, status: filter.status === "all" ? undefined : filter.status, suffix: filter.suffix?.replace(/^@/, "") || undefined };
}
export function protoPageLimit(limit: number) {
  return Math.max(1, Math.min(100, Number.isFinite(limit) ? Math.trunc(limit) : 20));
}
export function protoIdsSelection(resourceIds: number[]): AdminProtoResourceSelection {
  return { mode: "ids", resourceIds: Array.from(new Set(resourceIds)).filter((id) => Number.isInteger(id) && id > 0) };
}
export async function listOwnedProtoResources(filter: ResourceListFilter = {}, offset = 0, limit = 20, afterId?: number, options: { includeFacets?: boolean; includeTotal?: boolean; signal?: AbortSignal } = {}): Promise<ResourceListResponse> {
  const response = await unwrap(await client.GET("/v1/proto/resources", {
    params: { query: { ...protoFilter(filter), offset, limit: protoPageLimit(limit), afterId, includeFacets: options.includeFacets, includeTotal: options.includeTotal } }, signal: options.signal,
  }));
  return { ...response, total: options.includeTotal === false ? undefined : response.total };
}
export const listProtoResources = listOwnedProtoResources;

export async function importProtoResources(file: File | string, longLivedOrStrategy: boolean | ImportErrorStrategy = true, turnstileToken = "", errorStrategy: ImportErrorStrategy = "skip", signal?: AbortSignal): Promise<ProtoImportResponse> {
  const formData = new FormData();
  formData.append("file", typeof file === "string" ? new File([file], "proto-resources.txt", { type: "text/plain" }) : file);
  formData.append("longLived", String(typeof longLivedOrStrategy === "boolean" ? longLivedOrStrategy : true));
  formData.append("errorStrategy", typeof longLivedOrStrategy === "string" ? longLivedOrStrategy : errorStrategy);
  return unwrap(await client.POST("/v1/proto/resources/imports", { body: formData as never, bodySerializer: (body) => body, params: { header: { ...protoCommandHeaders(), ...turnstileHeader(turnstileToken) } }, signal }));
}
export async function getProtoResourceImport(importId: number, signal?: AbortSignal): Promise<ProtoImportResponse> {
  return unwrap(await client.GET("/v1/proto/resources/imports/{importId}", { params: { path: { importId } }, signal }));
}
export async function getProtoResourceImportItems(importId: number, offset = 0, limit = 20, signal?: AbortSignal): Promise<ProtoImportItemsResponse> {
  return unwrap(await client.GET("/v1/proto/resources/imports/{importId}/items", { params: { path: { importId }, query: { offset, limit: protoPageLimit(limit) } }, signal }));
}
export async function getProtoResourceImportFailures(importId: number, signal?: AbortSignal) {
  return unwrap(await client.GET("/v1/proto/resources/imports/{importId}/failures", { params: { path: { importId } }, parseAs: "blob", signal }));
}
export function abortableProtoDelay(ms: number, signal?: AbortSignal) {
  return new Promise<void>((resolve, reject) => {
    if (signal?.aborted) { reject(new DOMException("The operation was aborted.", "AbortError")); return; }
    const cleanup = () => signal?.removeEventListener("abort", onAbort);
    const timer = globalThis.setTimeout(() => { cleanup(); resolve(); }, ms);
    const onAbort = () => { globalThis.clearTimeout(timer); cleanup(); reject(new DOMException("The operation was aborted.", "AbortError")); };
    signal?.addEventListener("abort", onAbort, { once: true });
  });
}
export async function waitForProtoImport(importId: number, options: { intervalMs?: number; maxAttempts?: number; signal?: AbortSignal; onProgress?: (result: ProtoImportResponse) => void } = {}): Promise<ProtoImportResponse> {
  for (let attempt = 0; attempt < (options.maxAttempts ?? 120); attempt += 1) {
    const result = await getProtoResourceImport(importId, options.signal);
    options.onProgress?.(result);
    if (result.status !== "processing") return result;
    await abortableProtoDelay(options.intervalMs ?? 1000, options.signal);
  }
  throw new Error("The Proto resource import is still processing.");
}
export const waitForResourceImport = waitForProtoImport;
export async function validateResource(resourceId: number, version: number, signal?: AbortSignal) {
  return unwrap(await client.POST("/v1/proto/resources/{resourceId}/validate", { params: { path: { resourceId }, header: protoCommandHeaders() }, body: { version }, signal }));
}
export const validateProtoResource = validateResource;
export async function publishProtoResource(resourceId: number, version: number) {
  return unwrap(await client.POST("/v1/proto/resources/{resourceId}/publish", { params: { path: { resourceId }, header: protoCommandHeaders() }, body: { version } }));
}
export async function deleteProtoResource(resourceId: number, version: number) {
  return unwrap(await client.DELETE("/v1/proto/resources/{resourceId}", { params: { path: { resourceId }, header: protoCommandHeaders() }, body: { version } }));
}
async function bulk(command: "validate" | "publish" | "delete", selection: AdminProtoResourceSelection): Promise<AdminProtoBulkResponse> {
  return unwrap(await client.POST("/v1/proto/resources/bulk/{command}", { params: { path: { command }, header: protoCommandHeaders() }, body: { selection } }));
}
export const validateProtoResourcesBatch = (ids: number[]) => bulk("validate", protoIdsSelection(ids));
export const validateProtoResourcesByFilter = (filter: ResourceBulkFilter) => bulk("validate", { mode: "filter", filter: protoFilter(filter) });
export const publishProtoResourcesBatch = (ids: number[]) => bulk("publish", protoIdsSelection(ids));
export const publishProtoResourcesByFilter = (filter: ResourceBulkFilter) => bulk("publish", { mode: "filter", filter: protoFilter(filter) });
export const deleteProtoResourcesBatch = (ids: number[]) => bulk("delete", protoIdsSelection(ids));
export const deleteProtoResourcesByFilter = (filter: ResourceBulkFilter) => bulk("delete", { mode: "filter", filter: protoFilter(filter) });

export async function getProtoBulkTask(taskId: string, signal?: AbortSignal): Promise<AdminProtoBulkResponse> {
  return unwrap(await client.GET("/v1/proto/resources/bulk-tasks/{taskId}", { params: { path: { taskId } }, signal }));
}
