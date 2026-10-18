import type { components } from "./openapi/schema";
import { apiClient as client, csrfHeader, unwrap } from "./api-client";
import { generateIdempotencyKey } from "./idempotency";
import { createDaumBulkSession, DaumBulkOutcomeUnconfirmedError, type DaumBulkSession } from "./daum-bulk-session";
export { createDaumBulkSession as createAdminDaumBulkSession } from "./daum-bulk-session";
import { abortableProtoDelay as abortableDaumDelay } from "./proto-api";
import type {
  AdminDaumAllocationListResponse, AdminDaumBulkResponse,
  AdminDaumImportErrorStrategy, AdminDaumImportResponse, AdminDaumListFilter,
  AdminDaumListResponse, AdminDaumMaintenanceAction, AdminDaumOwner,
  AdminDaumResourceDetail, AdminDaumResourceSelection, AdminDaumTaskAcceptedResponse,
  AdminDaumTaskListResponse, UpdateAdminDaumResourceRequest,
} from "@/pages/admin-daum/admin-daum-types";

function daumCommandHeaders(idempotencyKey = generateIdempotencyKey()) {
  return { ...csrfHeader(), "Idempotency-Key": idempotencyKey };
}
function daumFilter(filter: AdminDaumListFilter) {
  return {
    search: filter.search?.trim() || undefined,
    status: filter.status === "all" ? undefined : filter.status,
    forSale: filter.forSale,
    longLived: filter.longLived,
    suffix: filter.suffix?.trim().toLowerCase().replace(/^@/, "") || undefined,
    protocol: filter.protocol,
    ownerId: filter.ownerId,
    createdFrom: filter.createdFrom,
    createdTo: filter.createdTo,
  };
}
function daumPageLimit(limit: number) {
  return Math.max(1, Math.min(100, Number.isFinite(limit) ? Math.trunc(limit) : 20));
}
function daumIdsSelection(resourceIds: number[]): Extract<AdminDaumResourceSelection, { mode: "ids" }> {
  return { mode: "ids", resourceIds: Array.from(new Set(resourceIds)).filter((id) => Number.isInteger(id) && id > 0).sort((a, b) => a - b) };
}

export type AdminDaumBulkCommandResponse = AdminDaumBulkResponse;
type DaumResource = components["schemas"]["DaumResource"];

function toDetail(resource: DaumResource): AdminDaumResourceDetail {
  return { ...resource, owner: resource.owner ?? null, emailAddress: resource.email, credentials: {
    passwordConfigured: resource.passwordConfigured,
    appPasswordConfigured: resource.appPasswordConfigured,
    revision: resource.credentialRevision,
    updatedAt: resource.credentialUpdatedAt,
  } };
}
export async function listAdminDaumResources(filter: AdminDaumListFilter = {}, offset = 0, limit = 20, afterId?: number, options: { includeFacets?: boolean; includeTotal?: boolean; signal?: AbortSignal } = {}): Promise<AdminDaumListResponse> {
  const response = await unwrap(await client.GET("/v1/admin/daum/resources", {
    params: { query: { ...daumFilter(filter), offset, limit: daumPageLimit(limit), afterId, includeFacets: options.includeFacets, includeTotal: options.includeTotal } }, signal: options.signal,
  }));
  return { ...response, total: options.includeTotal === false ? undefined : response.total, items: response.items.map(toDetail) };
}
export async function getAdminDaumResourceDetail(resourceId: number, signal?: AbortSignal) {
  return toDetail(await unwrap(await client.GET("/v1/admin/daum/resources/{resourceId}", { params: { path: { resourceId } }, signal })));
}
export async function listAdminDaumOwners(search = "", signal?: AbortSignal): Promise<AdminDaumOwner[]> {
  const page = await unwrap(await client.GET("/v1/admin/users", { params: { query: { search: search.trim() || undefined, offset: 0, limit: 100 } }, signal }));
  return page.users.map((user) => ({ id: user.id, email: user.email, nickname: user.nickname, groupName: user.userGroup.name, role: user.role, enabled: user.enabled }));
}
export async function importAdminDaumResources(payload: { content: string; ownerId: number; longLived: boolean; errorStrategy: AdminDaumImportErrorStrategy; protocol: "imap" | "pop3" }, signal?: AbortSignal, idempotencyKey?: string): Promise<AdminDaumImportResponse> {
  const formData = new FormData();
  formData.append("file", new File([payload.content], "daum-resources.txt", { type: "text/plain" }));
  formData.append("ownerId", String(payload.ownerId));
  formData.append("longLived", String(payload.longLived));
  formData.append("errorStrategy", payload.errorStrategy);
  formData.append("protocol", payload.protocol);
  return unwrap(await client.POST("/v1/admin/daum/resources/imports", { body: formData as never, bodySerializer: (body) => body, params: { header: daumCommandHeaders(idempotencyKey) }, signal }));
}
export async function getAdminDaumResourceImport(importId: number, signal?: AbortSignal): Promise<AdminDaumImportResponse> {
  return unwrap(await client.GET("/v1/admin/daum/resources/imports/{importId}", { params: { path: { importId } }, signal }));
}
export async function getAdminDaumResourceImportItems(importId: number, offset = 0, limit = 20, signal?: AbortSignal) {
  return unwrap(await client.GET("/v1/admin/daum/resources/imports/{importId}/items", { params: { path: { importId }, query: { offset, limit: daumPageLimit(limit) } }, signal }));
}
export async function getAdminDaumResourceImportFailures(importId: number, signal?: AbortSignal) {
  return unwrap(await client.GET("/v1/admin/daum/resources/imports/{importId}/failures", { params: { path: { importId } }, parseAs: "blob", signal }));
}
export async function waitForAdminDaumResourceImport(importId: number, options: { signal?: AbortSignal; onProgress?: (result: AdminDaumImportResponse) => void } = {}) {
  for (let attempt = 0; attempt < 120; attempt += 1) {
    const result = await getAdminDaumResourceImport(importId, options.signal);
    options.onProgress?.(result);
    if (result.status !== "processing") return result;
    await abortableDaumDelay(1000, options.signal);
  }
  throw new Error("The Daum resource import is still processing.");
}
export async function updateAdminDaumResource(resourceId: number, patch: UpdateAdminDaumResourceRequest) {
  await unwrap(await client.PATCH("/v1/admin/daum/resources/{resourceId}", { params: { path: { resourceId }, header: daumCommandHeaders() }, body: patch }));
  return getAdminDaumResourceDetail(resourceId);
}
export async function replaceAdminDaumCredentials(resourceId: number, payload: components["schemas"]["DaumCredentialsReplaceRequest"]) {
  await unwrap(await client.PUT("/v1/admin/daum/resources/{resourceId}/credentials", { params: { path: { resourceId }, header: daumCommandHeaders() }, body: payload }));
  return getAdminDaumResourceDetail(resourceId);
}
export async function adminDaumCommand(resourceId: number, command: "enable" | "disable" | "publish" | "unpublish" | "recover", version: number) {
  return unwrap(await client.POST("/v1/admin/daum/resources/{resourceId}/{command}", { params: { path: { resourceId, command }, header: daumCommandHeaders() }, body: { version } }));
}
export async function validateAdminDaumResource(resourceId: number) {
  return unwrap(await client.POST("/v1/admin/daum/resources/{resourceId}/validate", { params: { path: { resourceId }, header: daumCommandHeaders() } }));
}
export async function scanAdminDaumProjects(resourceId: number) {
  return unwrap(await client.POST("/v1/admin/daum/resources/{resourceId}/history", { params: { path: { resourceId }, header: daumCommandHeaders() } }));
}
export const enableAdminDaumResource = (id: number, version: number) => adminDaumCommand(id, "enable", version);
export const disableAdminDaumResource = (id: number, version: number) => adminDaumCommand(id, "disable", version);
export const publishAdminDaumResource = (id: number, version: number) => adminDaumCommand(id, "publish", version);
export const unpublishAdminDaumResource = (id: number, version: number) => adminDaumCommand(id, "unpublish", version);
export const recoverAdminDaumResource = (id: number, version: number) => adminDaumCommand(id, "recover", version);
export async function deleteAdminDaumResource(resourceId: number, version: number) {
  return unwrap(await client.DELETE("/v1/admin/daum/resources/{resourceId}", { params: { path: { resourceId }, header: daumCommandHeaders() }, body: { version } }));
}
function bulk(command: "validate" | "history" | "publish" | "unpublish" | "delete", selection: AdminDaumResourceSelection, session = createDaumBulkSession(), waitForResult = false): Promise<AdminDaumBulkResponse> {
  return session.run(JSON.stringify({ command, selection }), async (key) => {
    const accepted = await unwrap(await client.POST("/v1/admin/daum/resources/bulk/{command}", { params: { path: { command }, header: daumCommandHeaders(key) }, body: { selection } }));
    if (!accepted?.taskId || !["queued", "running", "succeeded", "failed"].includes(accepted.status)) throw new DaumBulkOutcomeUnconfirmedError();
    return waitForResult ? waitForSelectedBulk(accepted) : accepted;
  });
}
export const maintainAdminDaumResourcesByIds = (action: AdminDaumMaintenanceAction, ids: number[], session?: DaumBulkSession) => bulk(action, daumIdsSelection(ids), session);
export const maintainAdminDaumResourcesByFilter = (action: AdminDaumMaintenanceAction, filter: AdminDaumListFilter, session?: DaumBulkSession) => bulk(action, { mode: "filter", filter: daumFilter(filter) }, session);
export function disableAdminDaumResourcesByIds(ids: number[], session = createDaumBulkSession()): Promise<AdminDaumBulkResponse> {
  const selection = daumIdsSelection(ids);
  return session.run(JSON.stringify({ command: "disable", selection }), async (key) => {
    const result = await unwrap(await client.POST("/v1/admin/daum/resources/bulk/disable", { params: { header: daumCommandHeaders(key) }, body: { selection } }));
    // Disable commits synchronously and has no taskId; require its actual count receipt.
    if (!result || ![result.requested, result.affected, result.skipped].every((value) => Number.isInteger(value) && value >= 0) || !Array.isArray(result.reasonCounts)) {
      throw new DaumBulkOutcomeUnconfirmedError();
    }
    return result;
  });
}
async function waitForSelectedBulk(result: AdminDaumBulkResponse): Promise<AdminDaumBulkResponse> {
  if (!result.taskId) return result;
  const taskId = result.taskId;
  let current = result;
  for (let attempt = 0; attempt < 300; attempt += 1) {
    if (current.status !== "queued" && current.status !== "running") return current;
    try {
      const next = await getAdminDaumBulkTask(taskId);
      if (!next || !["queued", "running", "succeeded", "failed"].includes(next.status ?? "")) return current;
      current = { ...next, taskId };
    } catch {
      // The mutation was accepted. Keep its task for the page's progress/retry UI.
      return current;
    }
    if (current.status !== "queued" && current.status !== "running") return current;
    if (attempt < 299) await abortableDaumDelay(1000);
  }
  return current;
}
export const deleteAdminDaumResourcesByIds = (ids: number[], session?: DaumBulkSession) => bulk("delete", daumIdsSelection(ids), session, true);
export const deleteAdminDaumResourcesByFilter = (filter: AdminDaumListFilter, session?: DaumBulkSession) => bulk("delete", { mode: "filter", filter: daumFilter(filter) }, session);
export const setAdminDaumResourcesForSaleByIds = (ids: number[], forSale: boolean, session?: DaumBulkSession) => bulk(forSale ? "publish" : "unpublish", daumIdsSelection(ids), session, true);
export const setAdminDaumResourcesForSaleByFilter = (filter: AdminDaumListFilter, forSale: boolean, session?: DaumBulkSession) => bulk(forSale ? "publish" : "unpublish", { mode: "filter", filter: daumFilter(filter) }, session);

export async function listAdminDaumAllocations(resourceId: number, offset = 0, limit = 20, signal?: AbortSignal): Promise<AdminDaumAllocationListResponse> {
  return unwrap(await client.GET("/v1/admin/allocations", { params: { query: { type: "daum", resourceId, offset, limit: daumPageLimit(limit) } }, signal }));
}
export async function listAdminDaumTasks(resourceId: number, offset = 0, limit = 20, signal?: AbortSignal): Promise<AdminDaumTaskListResponse> {
  return unwrap(await client.GET("/v1/admin/tasks", { params: { query: { bizType: "daum_resource", bizId: resourceId, offset, limit: daumPageLimit(limit) } }, signal }));
}
export async function getAdminDaumBulkTask(taskId: string, signal?: AbortSignal): Promise<AdminDaumBulkResponse> {
  return unwrap(await client.GET("/v1/admin/daum/resources/bulk-tasks/{taskId}", { params: { path: { taskId } }, signal }));
}
export async function fetchAdminDaumMail(resourceId: number): Promise<AdminDaumTaskAcceptedResponse> {
  return unwrap(await client.POST("/v1/admin/resources/{resourceId}/messages/fetch", { params: { path: { resourceId }, header: daumCommandHeaders(), query: { type: "daum" } } }));
}
