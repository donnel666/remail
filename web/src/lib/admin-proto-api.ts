import type { components } from "./openapi/schema";
import { apiClient as client, unwrap } from "./api-client";
import { abortableProtoDelay, protoCommandHeaders, protoFilter, protoIdsSelection, protoPageLimit } from "./proto-api";
import type {
  AdminProtoAllocationListResponse, AdminProtoAsyncTask, AdminProtoBulkResponse,
  AdminProtoImportErrorStrategy, AdminProtoImportResponse, AdminProtoListFilter,
  AdminProtoListResponse, AdminProtoMaintenanceAction, AdminProtoMessageCursor,
  AdminProtoMessageDetail, AdminProtoMessageListResponse, AdminProtoOwner,
  AdminProtoResourceDetail, AdminProtoResourceSelection, AdminProtoTaskAcceptedResponse,
  AdminProtoTaskListResponse, UpdateAdminProtoResourceRequest,
} from "@/pages/admin-proto/admin-proto-types";

export type AdminProtoBulkCommandResponse = AdminProtoBulkResponse;
type ProtoResource = components["schemas"]["ProtoResource"];

function toDetail(resource: ProtoResource): AdminProtoResourceDetail {
  return { ...resource, owner: resource.owner ?? null, emailAddress: resource.email, credentials: {
    passwordConfigured: resource.passwordConfigured,
    revision: resource.credentialRevision,
    updatedAt: resource.credentialUpdatedAt,
  } };
}
export async function listAdminProtoResources(filter: AdminProtoListFilter = {}, offset = 0, limit = 20, afterId?: number, options: { includeFacets?: boolean; includeTotal?: boolean; signal?: AbortSignal } = {}): Promise<AdminProtoListResponse> {
  const response = await unwrap(await client.GET("/v1/admin/proto/resources", {
    params: { query: { ...protoFilter(filter), offset, limit: protoPageLimit(limit), afterId, includeFacets: options.includeFacets, includeTotal: options.includeTotal } }, signal: options.signal,
  }));
  return { ...response, total: options.includeTotal === false ? undefined : response.total, items: response.items.map(toDetail) };
}
export async function getAdminProtoResourceDetail(resourceId: number, signal?: AbortSignal) {
  return toDetail(await unwrap(await client.GET("/v1/admin/proto/resources/{resourceId}", { params: { path: { resourceId } }, signal })));
}
export async function listAdminProtoOwners(search = "", signal?: AbortSignal): Promise<AdminProtoOwner[]> {
  const page = await unwrap(await client.GET("/v1/admin/users", { params: { query: { search: search.trim() || undefined, offset: 0, limit: 100 } }, signal }));
  return page.users.map((user) => ({ id: user.id, email: user.email, nickname: user.nickname, groupName: user.userGroup.name, role: user.role, enabled: user.enabled }));
}
export async function importAdminProtoResources(payload: { content: string; ownerId: number; longLived: boolean; errorStrategy: AdminProtoImportErrorStrategy }, signal?: AbortSignal): Promise<AdminProtoImportResponse> {
  const formData = new FormData();
  formData.append("file", new File([payload.content], "proto-resources.txt", { type: "text/plain" }));
  formData.append("ownerId", String(payload.ownerId));
  formData.append("longLived", String(payload.longLived));
  formData.append("errorStrategy", payload.errorStrategy);
  return unwrap(await client.POST("/v1/admin/proto/resources/imports", { body: formData as never, bodySerializer: (body) => body, params: { header: protoCommandHeaders() }, signal }));
}
export async function getAdminProtoResourceImport(importId: number, signal?: AbortSignal): Promise<AdminProtoImportResponse> {
  return unwrap(await client.GET("/v1/admin/proto/resources/imports/{importId}", { params: { path: { importId } }, signal }));
}
export async function getAdminProtoResourceImportItems(importId: number, offset = 0, limit = 20, signal?: AbortSignal) {
  return unwrap(await client.GET("/v1/admin/proto/resources/imports/{importId}/items", { params: { path: { importId }, query: { offset, limit: protoPageLimit(limit) } }, signal }));
}
export async function getAdminProtoResourceImportFailures(importId: number, signal?: AbortSignal) {
  return unwrap(await client.GET("/v1/admin/proto/resources/imports/{importId}/failures", { params: { path: { importId } }, parseAs: "blob", signal }));
}
export async function waitForAdminProtoResourceImport(importId: number, options: { signal?: AbortSignal; onProgress?: (result: AdminProtoImportResponse) => void } = {}) {
  for (let attempt = 0; attempt < 120; attempt += 1) {
    const result = await getAdminProtoResourceImport(importId, options.signal);
    options.onProgress?.(result);
    if (result.status !== "processing") return result;
    await abortableProtoDelay(1000, options.signal);
  }
  throw new Error("The Proto resource import is still processing.");
}
export async function updateAdminProtoResource(resourceId: number, patch: UpdateAdminProtoResourceRequest) {
  await unwrap(await client.PATCH("/v1/admin/proto/resources/{resourceId}", { params: { path: { resourceId }, header: protoCommandHeaders() }, body: patch }));
  return getAdminProtoResourceDetail(resourceId);
}
export async function replaceAdminProtoCredentials(resourceId: number, payload: { password: string; version: number }) {
  await unwrap(await client.PUT("/v1/admin/proto/resources/{resourceId}/credentials", { params: { path: { resourceId }, header: protoCommandHeaders() }, body: payload }));
  return getAdminProtoResourceDetail(resourceId);
}
export async function adminProtoCommand(resourceId: number, command: "validate" | "history" | "enable" | "disable" | "publish" | "unpublish" | "recover", version: number) {
  return unwrap(await client.POST("/v1/admin/proto/resources/{resourceId}/{command}", { params: { path: { resourceId, command }, header: protoCommandHeaders() }, body: { version } }));
}
export const validateAdminProtoResource = (id: number, version: number) => adminProtoCommand(id, "validate", version);
export const scanAdminProtoProjects = (id: number, version: number) => adminProtoCommand(id, "history", version);
export const enableAdminProtoResource = (id: number, version: number) => adminProtoCommand(id, "enable", version);
export const disableAdminProtoResource = (id: number, version: number) => adminProtoCommand(id, "disable", version);
export const publishAdminProtoResource = (id: number, version: number) => adminProtoCommand(id, "publish", version);
export const unpublishAdminProtoResource = (id: number, version: number) => adminProtoCommand(id, "unpublish", version);
export const recoverAdminProtoResource = (id: number, version: number) => adminProtoCommand(id, "recover", version);
export async function deleteAdminProtoResource(resourceId: number, version: number) {
  return unwrap(await client.DELETE("/v1/admin/proto/resources/{resourceId}", { params: { path: { resourceId }, header: protoCommandHeaders() }, body: { version } }));
}
async function bulk(command: "validate" | "history" | "disable" | "publish" | "unpublish" | "delete", selection: AdminProtoResourceSelection): Promise<AdminProtoBulkResponse> {
  return unwrap(await client.POST("/v1/admin/proto/resources/bulk/{command}", { params: { path: { command }, header: protoCommandHeaders() }, body: { selection } }));
}
export const maintainAdminProtoResourcesByIds = (action: AdminProtoMaintenanceAction, ids: number[]) => bulk(action, protoIdsSelection(ids));
export const maintainAdminProtoResourcesByFilter = (action: AdminProtoMaintenanceAction, filter: AdminProtoListFilter) => bulk(action, { mode: "filter", filter: protoFilter(filter) });
export const disableAdminProtoResourcesByIds = (ids: number[]) => bulk("disable", protoIdsSelection(ids));
export const deleteAdminProtoResourcesByIds = (ids: number[]) => bulk("delete", protoIdsSelection(ids));
export const deleteAdminProtoResourcesByFilter = (filter: AdminProtoListFilter) => bulk("delete", { mode: "filter", filter: protoFilter(filter) });
export const setAdminProtoResourcesForSaleByIds = (ids: number[], forSale: boolean) => bulk(forSale ? "publish" : "unpublish", protoIdsSelection(ids));
export const setAdminProtoResourcesForSaleByFilter = (filter: AdminProtoListFilter, forSale: boolean) => bulk(forSale ? "publish" : "unpublish", { mode: "filter", filter: protoFilter(filter) });

export async function listAdminProtoAllocations(resourceId: number, offset = 0, limit = 20, signal?: AbortSignal): Promise<AdminProtoAllocationListResponse> {
  return unwrap(await client.GET("/v1/admin/allocations", { params: { query: { type: "proto", resourceId, offset, limit: protoPageLimit(limit) } }, signal }));
}
export async function listAdminProtoTasks(resourceId: number, offset = 0, limit = 20, signal?: AbortSignal): Promise<AdminProtoTaskListResponse> {
  return unwrap(await client.GET("/v1/admin/tasks", { params: { query: { bizType: "proto_resource", bizId: resourceId, offset, limit: protoPageLimit(limit) } }, signal }));
}
export async function getAdminProtoTask(taskId: string, signal?: AbortSignal): Promise<AdminProtoAsyncTask> {
  return unwrap(await client.GET("/v1/admin/tasks/{taskId}", { params: { path: { taskId } }, signal }));
}
export async function getAdminProtoBulkTask(taskId: string, signal?: AbortSignal): Promise<AdminProtoBulkResponse> {
  return unwrap(await client.GET("/v1/admin/proto/resources/bulk-tasks/{taskId}", { params: { path: { taskId } }, signal }));
}
export async function listAdminProtoMessages(resourceId: number, search = "", limit = 100, cursor?: AdminProtoMessageCursor, signal?: AbortSignal): Promise<AdminProtoMessageListResponse> {
  return unwrap(await client.GET("/v1/admin/messages", { params: { query: { resourceId, type: "proto", search: search.trim() || undefined, offset: cursor ? undefined : 0, beforeReceivedAt: cursor?.beforeReceivedAt, beforeId: cursor?.beforeId, includeTotal: !cursor, limit: protoPageLimit(limit) } }, signal }));
}
export async function getAdminProtoMessage(resourceId: number, messageId: number, signal?: AbortSignal): Promise<AdminProtoMessageDetail> {
  return unwrap(await client.GET("/v1/admin/messages/{messageId}", { params: { path: { messageId }, query: { resourceId, type: "proto" } }, signal }));
}
export async function fetchAdminProtoMail(resourceId: number): Promise<AdminProtoTaskAcceptedResponse> {
  return unwrap(await client.POST("/v1/admin/resources/{resourceId}/messages/fetch", { params: { path: { resourceId }, header: protoCommandHeaders(), query: { type: "proto" } } }));
}
