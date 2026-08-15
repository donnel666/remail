import { apiClient as client, csrfHeader, unwrap } from "./api-client";
import { generateIdempotencyKey } from "./idempotency";
import type { components } from "./openapi/schema";

export type AdminICloudResourceStatus =
  components["schemas"]["AdminICloudResourceStatus"];
export type AdminICloudSessionStatus =
  components["schemas"]["AdminICloudSessionStatus"];
export type AdminICloudAliasStatus =
  components["schemas"]["AdminICloudAliasStatus"];
export type AdminICloudOwner = components["schemas"]["AdminICloudOwnerSummary"];
export type AdminICloudResourceItem =
  components["schemas"]["AdminICloudResourceItem"];
export type AdminICloudResourceDetail =
  components["schemas"]["AdminICloudResourceDetail"];
export type AdminICloudSessionView =
  components["schemas"]["AdminICloudSessionView"];
export type AdminICloudResourceList =
  components["schemas"]["AdminICloudResourceListResponse"];
export type AdminICloudResourceFacets =
  components["schemas"]["AdminICloudFacets"];
export type AdminICloudAliasItem =
  components["schemas"]["AdminICloudAliasItem"];
export type AdminICloudAliasList =
  components["schemas"]["AdminICloudAliasListResponse"];
export type AdminICloudImportResponse =
  components["schemas"]["AdminICloudImportResponse"];
export type AdminICloudImportPreparation =
  components["schemas"]["AdminICloudImportPreparation"];
export type AdminICloudMutationResponse =
  components["schemas"]["AdminICloudMutationResponse"];
export type AdminICloudTask = components["schemas"]["AdminTaskView"];
export type AdminICloudTaskList =
  components["schemas"]["AdminTaskListResponse"];
export type AdminICloudUpdateRequest =
  components["schemas"]["AdminICloudUpdateRequest"];
export type AdminICloudBulkResponse =
  components["schemas"]["AdminICloudBulkResult"];
export type AdminICloudImportErrorStrategy = "skip" | "abort";
export type AdminICloudBatchAction =
  | "validate"
  | "alias"
  | "disable"
  | "publish"
  | "unpublish"
  | "delete";

export interface AdminICloudResourceListFilter {
  search?: string;
  status?: AdminICloudResourceStatus;
  forSale?: boolean;
  createdFrom?: string;
  createdTo?: string;
}

export interface AdminICloudImportRequest {
  content: string;
  ownerId: number;
  preparationId: number;
  errorStrategy: AdminICloudImportErrorStrategy;
  expireAt: string;
}

export async function createAdminICloudImportPreparation(
  signal?: AbortSignal,
): Promise<AdminICloudImportPreparation> {
  return unwrap(
    await client.POST("/v1/admin/icloud/resources/import-preparations", {
      params: { header: csrfHeader() },
      signal,
    }),
  );
}

export async function getAdminICloudImportPreparation(
  preparationId: number,
  signal?: AbortSignal,
): Promise<AdminICloudImportPreparation> {
  return unwrap(
    await client.GET(
      "/v1/admin/icloud/resources/import-preparations/{preparationId}",
      {
        params: { path: { preparationId } },
        signal,
      },
    ),
  );
}

type AdminUserListDTO = components["schemas"]["AdminUserListResponse"];
type AdminICloudSelection = components["schemas"]["AdminICloudBulkSelection"];

const OWNER_PAGE_SIZE = 100;

export function normalizeICloudImportContent(content: string) {
  return content.replace(/[ \t]*\\[ \t]*\r?\n[ \t]*/g, " ");
}

function commandHeaders() {
  return {
    ...csrfHeader(),
    "Idempotency-Key": generateIdempotencyKey(),
  };
}

const importHeaders = commandHeaders;

function pageLimit(limit: number) {
  if (!Number.isFinite(limit)) return 20;
  return Math.max(1, Math.min(100, Math.trunc(limit)));
}

function normalizeFilter(filter: AdminICloudResourceListFilter) {
  return {
    search: filter.search?.trim() || undefined,
    status: filter.status,
    forSale: filter.forSale,
    createdFrom: filter.createdFrom,
    createdTo: filter.createdTo,
  };
}

function idsSelection(resourceIds: number[]): AdminICloudSelection {
  return {
    mode: "ids",
    resourceIds: Array.from(new Set(resourceIds)).filter(
      (id) => Number.isInteger(id) && id > 0,
    ),
  };
}

function filterSelection(
  filter: AdminICloudResourceListFilter,
): AdminICloudSelection {
  return { mode: "filter", filter: normalizeFilter(filter) };
}

export async function listAdminICloudResources(
  filter: AdminICloudResourceListFilter = {},
  offset = 0,
  limit = 20,
  options: {
    includeFacets?: boolean;
    includeTotal?: boolean;
    signal?: AbortSignal;
  } = {},
): Promise<AdminICloudResourceList> {
  return unwrap(
    await client.GET("/v1/admin/icloud/resources", {
      params: {
        query: {
          ...normalizeFilter(filter),
          offset: Math.max(0, Math.trunc(offset)),
          limit: pageLimit(limit),
          includeFacets: options.includeFacets,
          includeTotal: options.includeTotal,
        },
      },
      signal: options.signal,
    }),
  );
}

export async function listAdminICloudOwners(
  search = "",
  signal?: AbortSignal,
): Promise<AdminICloudOwner[]> {
  const page = await unwrap<AdminUserListDTO>(
    await client.GET("/v1/admin/users", {
      params: {
        query: {
          search: search.trim() || undefined,
          offset: 0,
          limit: OWNER_PAGE_SIZE,
        },
      },
      signal,
    }),
  );
  return page.users.map((user) => ({
    id: user.id,
    email: user.email,
    nickname: user.nickname,
    groupName: user.userGroup.name,
    role: user.role,
    enabled: user.enabled,
  }));
}

export async function getAdminICloudResourceDetail(
  resourceId: number,
  signal?: AbortSignal,
): Promise<AdminICloudResourceDetail> {
  return unwrap(
    await client.GET("/v1/admin/icloud/resources/{resourceId}", {
      params: { path: { resourceId } },
      signal,
    }),
  );
}

export async function importAdminICloudResources(
  request: AdminICloudImportRequest,
  signal?: AbortSignal,
): Promise<AdminICloudImportResponse> {
  const content = normalizeICloudImportContent(request.content);
  const formData = new FormData();
  formData.append(
    "file",
    new File([content], "icloud-resources.txt", { type: "text/plain" }),
  );
  formData.append("ownerId", String(request.ownerId));
  formData.append("preparationId", String(request.preparationId));
  formData.append("errorStrategy", request.errorStrategy);
  formData.append("expireAt", request.expireAt);

  const response = await unwrap<AdminICloudImportResponse>(
    await client.POST("/v1/admin/icloud/resources/imports", {
      body: formData as never,
      bodySerializer: (body) => body,
      params: { header: importHeaders() },
      signal,
    }),
  );
  if (response.status !== "processing") return response;
  const completed = await waitForAdminICloudResourceImport(response.importId, {
    signal,
  });
  return {
    ...completed,
    taskId: response.taskId,
    requestId: response.requestId,
    reused: response.reused,
  };
}

export async function getAdminICloudResourceImport(
  importId: number,
  signal?: AbortSignal,
): Promise<AdminICloudImportResponse> {
  return unwrap(
    await client.GET("/v1/admin/icloud/resources/imports/{importId}", {
      params: { path: { importId } },
      signal,
    }),
  );
}

export async function waitForAdminICloudResourceImport(
  importId: number,
  options: {
    intervalMs?: number;
    maxAttempts?: number;
    signal?: AbortSignal;
  } = {},
): Promise<AdminICloudImportResponse> {
  const intervalMs = options.intervalMs ?? 1_000;
  const maxAttempts = options.maxAttempts ?? 120;
  let lastStatus: AdminICloudImportResponse | undefined;
  for (let attempt = 0; attempt < maxAttempts; attempt += 1) {
    throwIfAborted(options.signal);
    lastStatus = await getAdminICloudResourceImport(importId, options.signal);
    if (lastStatus.status !== "processing") return lastStatus;
    if (attempt + 1 < maxAttempts) {
      await abortableDelay(intervalMs, options.signal);
    }
  }
  throwIfAborted(options.signal);
  if (lastStatus) return lastStatus;
  throw new Error("The iCloud resource import is still processing.");
}

export async function listAdminICloudAliases(
  resourceId: number,
  offset = 0,
  limit = 20,
  signal?: AbortSignal,
): Promise<AdminICloudAliasList> {
  return unwrap(
    await client.GET("/v1/admin/icloud/resources/{resourceId}/aliases", {
      params: {
        path: { resourceId },
        query: {
          offset: Math.max(0, Math.trunc(offset)),
          limit: pageLimit(limit),
        },
      },
      signal,
    }),
  );
}

export async function listAdminICloudTasks(
  resourceId: number,
  offset = 0,
  limit = 20,
  signal?: AbortSignal,
): Promise<AdminICloudTaskList> {
  return unwrap(
    await client.GET("/v1/admin/tasks", {
      params: {
        query: {
          bizType: "icloud_resource",
          bizId: resourceId,
          offset: Math.max(0, Math.trunc(offset)),
          limit: pageLimit(limit),
        },
      },
      signal,
    }),
  );
}

export async function updateAdminICloudResource(
  resourceId: number,
  request: AdminICloudUpdateRequest,
  signal?: AbortSignal,
): Promise<AdminICloudMutationResponse> {
  const body =
    request.importLine === undefined
      ? request
      : {
          ...request,
          importLine: normalizeICloudImportContent(request.importLine),
        };
  return unwrap(
    await client.PATCH("/v1/admin/icloud/resources/{resourceId}", {
      body,
      params: {
        header: commandHeaders(),
        path: { resourceId },
      },
      signal,
    }),
  );
}

export async function validateAdminICloudResource(
  resourceId: number,
  version: number,
  signal?: AbortSignal,
) {
  return unwrap(
    await client.POST("/v1/admin/icloud/resources/{resourceId}/validation", {
      params: {
        header: commandHeaders(),
        path: { resourceId },
        query: { version },
      },
      signal,
    }),
  );
}

export async function createAdminICloudAliases(
  resourceId: number,
  version: number,
  signal?: AbortSignal,
) {
  return unwrap(
    await client.POST("/v1/admin/icloud/resources/{resourceId}/aliases", {
      params: {
        header: commandHeaders(),
        path: { resourceId },
        query: { version },
      },
      signal,
    }),
  );
}

export async function enableAdminICloudResource(
  resourceId: number,
  version: number,
  signal?: AbortSignal,
) {
  return unwrap(
    await client.POST("/v1/admin/icloud/resources/{resourceId}/enable", {
      params: {
        header: commandHeaders(),
        path: { resourceId },
        query: { version },
      },
      signal,
    }),
  );
}

export async function disableAdminICloudResource(
  resourceId: number,
  version: number,
  signal?: AbortSignal,
) {
  return unwrap(
    await client.POST("/v1/admin/icloud/resources/{resourceId}/disable", {
      params: {
        header: commandHeaders(),
        path: { resourceId },
        query: { version },
      },
      signal,
    }),
  );
}

export async function publishAdminICloudResource(
  resourceId: number,
  version: number,
  signal?: AbortSignal,
) {
  return unwrap(
    await client.POST("/v1/admin/icloud/resources/{resourceId}/publish", {
      params: {
        header: commandHeaders(),
        path: { resourceId },
        query: { version },
      },
      signal,
    }),
  );
}

export async function unpublishAdminICloudResource(
  resourceId: number,
  version: number,
  signal?: AbortSignal,
) {
  return unwrap(
    await client.POST("/v1/admin/icloud/resources/{resourceId}/unpublish", {
      params: {
        header: commandHeaders(),
        path: { resourceId },
        query: { version },
      },
      signal,
    }),
  );
}

export async function recoverAdminICloudResource(
  resourceId: number,
  version: number,
  signal?: AbortSignal,
) {
  return unwrap(
    await client.POST("/v1/admin/icloud/resources/{resourceId}/recover", {
      params: {
        header: commandHeaders(),
        path: { resourceId },
        query: { version },
      },
      signal,
    }),
  );
}

export async function deleteAdminICloudResource(
  resourceId: number,
  version: number,
  signal?: AbortSignal,
) {
  return unwrap(
    await client.DELETE("/v1/admin/icloud/resources/{resourceId}", {
      params: {
        header: commandHeaders(),
        path: { resourceId },
        query: { version },
      },
      signal,
    }),
  );
}

export function batchAdminICloudResourcesByIds(
  action: AdminICloudBatchAction,
  resourceIds: number[],
  signal?: AbortSignal,
) {
  return batchAdminICloudResources(action, idsSelection(resourceIds), signal);
}

export function batchAdminICloudResourcesByFilter(
  action: AdminICloudBatchAction,
  filter: AdminICloudResourceListFilter,
  signal?: AbortSignal,
) {
  return batchAdminICloudResources(action, filterSelection(filter), signal);
}

export function setAdminICloudResourcesExpirationByIds(
  resourceIds: number[],
  expireAt: string,
  signal?: AbortSignal,
) {
  return setAdminICloudResourcesExpiration(
    idsSelection(resourceIds),
    expireAt,
    signal,
  );
}

export function setAdminICloudResourcesExpirationByFilter(
  filter: AdminICloudResourceListFilter,
  expireAt: string,
  signal?: AbortSignal,
) {
  return setAdminICloudResourcesExpiration(
    filterSelection(filter),
    expireAt,
    signal,
  );
}

async function setAdminICloudResourcesExpiration(
  selection: AdminICloudSelection,
  expireAt: string,
  signal?: AbortSignal,
): Promise<AdminICloudBulkResponse> {
  return unwrap(
    await client.POST("/v1/admin/icloud/resources/batch/expiration", {
      body: { selection, expireAt },
      params: { header: commandHeaders() },
      signal,
    }),
  );
}

async function batchAdminICloudResources(
  action: AdminICloudBatchAction,
  selection: AdminICloudSelection,
  signal?: AbortSignal,
): Promise<AdminICloudBulkResponse> {
  const options = {
    body: { selection },
    params: { header: commandHeaders() },
    signal,
  };
  switch (action) {
    case "validate":
      return unwrap(
        await client.POST("/v1/admin/icloud/resources/batch/validation", options),
      );
    case "alias":
      return unwrap(
        await client.POST("/v1/admin/icloud/resources/batch/alias", options),
      );
    case "disable":
      return unwrap(
        await client.POST("/v1/admin/icloud/resources/batch/disable", options),
      );
    case "publish":
      return unwrap(
        await client.POST("/v1/admin/icloud/resources/batch/publish", options),
      );
    case "unpublish":
      return unwrap(
        await client.POST("/v1/admin/icloud/resources/batch/unpublish", options),
      );
    case "delete":
      return unwrap(
        await client.POST("/v1/admin/icloud/resources/batch/delete", options),
      );
  }
}

function throwIfAborted(signal?: AbortSignal) {
  if (!signal?.aborted) return;
  throw new DOMException("The operation was aborted.", "AbortError");
}

function abortableDelay(ms: number, signal?: AbortSignal) {
  return new Promise<void>((resolve, reject) => {
    if (signal?.aborted) {
      reject(new DOMException("The operation was aborted.", "AbortError"));
      return;
    }
    const cleanup = () => signal?.removeEventListener("abort", onAbort);
    const timer = globalThis.setTimeout(() => {
      cleanup();
      resolve();
    }, ms);
    const onAbort = () => {
      globalThis.clearTimeout(timer);
      cleanup();
      reject(new DOMException("The operation was aborted.", "AbortError"));
    };
    signal?.addEventListener("abort", onAbort, { once: true });
  });
}
