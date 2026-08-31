import { apiClient as client, csrfHeader, unwrap } from "./api-client";
import { generateIdempotencyKey } from "./idempotency";
import type { components, operations } from "./openapi/schema";

export type AdminGmailResourceStatus =
  components["schemas"]["AdminGmailResourceStatus"];
export type AdminGmailResourceItem =
  components["schemas"]["AdminGmailResourceItem"];
export type AdminGmailResourceList =
  components["schemas"]["AdminGmailResourceList"];
export type AdminGmailOwner =
  components["schemas"]["AdminGmailOwnerSummary"];
export type AdminGmailResourceUpdateRequest =
  components["schemas"]["AdminGmailResourceUpdateRequest"];
export type AdminGmailCredentialsReplaceRequest =
  components["schemas"]["AdminGmailCredentialsReplaceRequest"];
export type AdminGmailMutationResponse =
  components["schemas"]["AdminGmailMutationResponse"];
export type AdminGmailBulkResponse =
  components["schemas"]["AdminGmailBulkResult"];
export type AdminGmailTask = components["schemas"]["AdminTaskView"];
export type AdminGmailTaskListResponse =
  components["schemas"]["AdminTaskListResponse"];
export type AdminGmailAliasListResponse =
  components["schemas"]["AdminGmailAliasListResponse"];
type AdminGmailImportMultipart =
  operations["postAdminGmailResourceImport"]["requestBody"]["content"]["multipart/form-data"];
type AdminUserListDTO = components["schemas"]["AdminUserListResponse"];
export type AdminGmailImportResponse =
  components["schemas"]["AdminGmailImportResponse"];
export type AdminGmailImportErrorStrategy =
  AdminGmailImportMultipart["errorStrategy"];
export interface AdminGmailImportRequest {
  content: string;
  ownerId: AdminGmailImportMultipart["ownerId"];
  errorStrategy: AdminGmailImportErrorStrategy;
}
export type AdminGmailBatchAction =
  | "validate"
  | "history"
  | "disable"
  | "publish"
  | "unpublish"
  | "delete";

export interface AdminGmailResourceListFilter {
  search?: string;
  status?: AdminGmailResourceStatus;
  forSale?: boolean;
  createdFrom?: string;
  createdTo?: string;
}

type AdminGmailSelection = components["schemas"]["AdminGmailBulkSelection"];

const OWNER_PAGE_SIZE = 100;
const ALL_MATCHING_PAGE_SIZE = 200;
const COMMAND_CHUNK_SIZE = 1000;

export function normalizeAdminGmailAppPassword(value: string) {
  return value.replace(/\s/g, "");
}

function normalizeAdminGmailImportContent(content: string) {
  return content
    .split(/\r?\n/)
    .map((line) => {
      const parts = line.split("----");
      if (parts.length < 2) return line;
      parts[parts.length - 1] = normalizeAdminGmailAppPassword(
        parts[parts.length - 1] ?? "",
      );
      return parts.join("----");
    })
    .join("\n");
}

function commandHeaders() {
  return {
    ...csrfHeader(),
    "Idempotency-Key": generateIdempotencyKey(),
  };
}

export async function listAdminGmailResources(options: {
  limit: number;
  offset: number;
  search?: string;
  signal?: AbortSignal;
  status?: AdminGmailResourceStatus;
  forSale?: boolean;
  createdFrom?: string;
  createdTo?: string;
}) {
  const { signal, ...query } = options;
  return unwrap<AdminGmailResourceList>(
    await client.GET("/v1/admin/gmail/resources", {
      params: { query },
      signal,
    }),
  );
}

export async function getAdminGmailResource(
  resourceId: number,
  signal?: AbortSignal,
): Promise<AdminGmailResourceItem> {
  return unwrap(
    await client.GET("/v1/admin/gmail/resources/{resourceId}", {
      params: { path: { resourceId } },
      signal,
    }),
  );
}

export async function listAdminGmailAliases(
  resourceId: number,
  offset = 0,
  limit = 20,
  signal?: AbortSignal,
): Promise<AdminGmailAliasListResponse> {
  return unwrap(
    await client.GET("/v1/admin/gmail/resources/{resourceId}/aliases", {
      params: {
        path: { resourceId },
        query: {
          kind: "other",
          offset: Math.max(0, Math.trunc(offset)),
          limit: Math.max(1, Math.min(100, Math.trunc(limit))),
        },
      },
      signal,
    }),
  );
}

export async function updateAdminGmailResource(
  resourceId: number,
  request: AdminGmailResourceUpdateRequest,
  signal?: AbortSignal,
): Promise<AdminGmailMutationResponse> {
  const body =
    request.appPassword === undefined
      ? request
      : {
          ...request,
          appPassword: normalizeAdminGmailAppPassword(request.appPassword),
        };
  return unwrap(
    await client.PATCH("/v1/admin/gmail/resources/{resourceId}", {
      body,
      params: {
        header: commandHeaders(),
        path: { resourceId },
      },
      signal,
    }),
  );
}

export async function replaceAdminGmailCredentials(
  resourceId: number,
  request: AdminGmailCredentialsReplaceRequest,
  signal?: AbortSignal,
): Promise<AdminGmailMutationResponse> {
  const body = {
    ...request,
    appPassword: normalizeAdminGmailAppPassword(request.appPassword ?? ""),
  };
  return unwrap(
    await client.PUT("/v1/admin/gmail/resources/{resourceId}/credentials", {
      body,
      params: {
        header: commandHeaders(),
        path: { resourceId },
      },
      signal,
    }),
  );
}

export async function listAdminGmailTasks(
  resourceId: number,
  offset = 0,
  limit = 20,
  signal?: AbortSignal,
): Promise<AdminGmailTaskListResponse> {
  return unwrap(
    await client.GET("/v1/admin/tasks", {
      params: {
        query: {
          bizType: "gmail_resource",
          bizId: resourceId,
          offset,
          limit,
        },
      },
      signal,
    }),
  );
}

export async function listAdminGmailOwners(
  search = "",
  signal?: AbortSignal,
): Promise<AdminGmailOwner[]> {
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

export async function importAdminGmailResources(
  request: AdminGmailImportRequest,
  signal?: AbortSignal,
): Promise<AdminGmailImportResponse> {
  const formData = new FormData();
  const file = new File(
    [normalizeAdminGmailImportContent(request.content)],
    "gmail-resources.txt",
    {
      type: "text/plain",
    },
  );
  formData.append("file", file);
  formData.append("ownerId", String(request.ownerId));
  formData.append("errorStrategy", request.errorStrategy);

  const response = await unwrap<AdminGmailImportResponse>(
    await client.POST("/v1/admin/gmail/resources/imports", {
      body: formData as never,
      bodySerializer: (body) => body,
      params: { header: commandHeaders() },
      signal,
    }),
  );
  if (response.status !== "processing") return response;
  const completed = await waitForAdminGmailResourceImport(response.importId, {
    signal,
  });
  return {
    ...completed,
    taskId: response.taskId,
    requestId: response.requestId,
    reused: response.reused,
  };
}

export async function getAdminGmailResourceImport(
  importId: number,
  signal?: AbortSignal,
): Promise<AdminGmailImportResponse> {
  return unwrap(
    await client.GET("/v1/admin/gmail/resources/imports/{importId}", {
      params: { path: { importId } },
      signal,
    }),
  );
}

export async function waitForAdminGmailResourceImport(
  importId: number,
  options: {
    intervalMs?: number;
    maxAttempts?: number;
    signal?: AbortSignal;
  } = {},
): Promise<AdminGmailImportResponse> {
  const intervalMs = options.intervalMs ?? 1_000;
  const maxAttempts = options.maxAttempts ?? 120;
  for (let attempt = 0; attempt < maxAttempts; attempt += 1) {
    throwIfAborted(options.signal);
    const status = await getAdminGmailResourceImport(importId, options.signal);
    if (status.status !== "processing") return status;
    if (attempt + 1 < maxAttempts) {
      await abortableDelay(intervalMs, options.signal);
    }
  }
  throwIfAborted(options.signal);
  throw new Error("The Gmail resource import is still processing.");
}

export async function setAdminGmailResourceEnabled(
  resourceId: number,
  version: number,
  enabled: boolean,
) {
  const params = {
    header: commandHeaders(),
    path: { resourceId },
    query: { version },
  };
  if (enabled) {
    await unwrap<void>(
      await client.POST("/v1/admin/gmail/resources/{resourceId}/enable", {
        params,
      }),
    );
    return;
  }
  await unwrap<void>(
    await client.POST("/v1/admin/gmail/resources/{resourceId}/disable", {
      params,
    }),
  );
}

export async function validateAdminGmailResource(resourceId: number) {
  return unwrap(
    await client.POST("/v1/admin/gmail/resources/{resourceId}/validate", {
      params: {
        header: commandHeaders(),
        path: { resourceId },
      },
    }),
  );
}

export async function scanAdminGmailResourceHistory(
  resourceId: number,
  signal?: AbortSignal,
): Promise<AdminGmailMutationResponse> {
  return unwrap(
    await client.POST("/v1/admin/gmail/resources/{resourceId}/history", {
      params: {
        header: commandHeaders(),
        path: { resourceId },
      },
      signal,
    }),
  );
}

export async function setAdminGmailResourceForSale(
  resourceId: number,
  version: number,
  forSale: boolean,
) {
  const params = {
    header: commandHeaders(),
    path: { resourceId },
    query: { version },
  };
  if (forSale) {
    await unwrap<void>(
      await client.POST("/v1/admin/gmail/resources/{resourceId}/publish", {
        params,
      }),
    );
    return;
  }
  await unwrap<void>(
    await client.POST("/v1/admin/gmail/resources/{resourceId}/unpublish", {
      params,
    }),
  );
}

export async function deleteAdminGmailResource(
  resourceId: number,
  version: number,
  signal?: AbortSignal,
): Promise<AdminGmailMutationResponse> {
  return unwrap(
    await client.DELETE("/v1/admin/gmail/resources/{resourceId}", {
      params: {
        header: commandHeaders(),
        path: { resourceId },
        query: { version },
      },
      signal,
    }),
  );
}

export async function recoverAdminGmailResource(
  resourceId: number,
  version: number,
  signal?: AbortSignal,
): Promise<AdminGmailMutationResponse> {
  return unwrap(
    await client.POST("/v1/admin/gmail/resources/{resourceId}/recover", {
      params: {
        header: commandHeaders(),
        path: { resourceId },
        query: { version },
      },
      signal,
    }),
  );
}

export function batchAdminGmailResourcesByIds(
  action: AdminGmailBatchAction,
  resourceIds: number[],
  signal?: AbortSignal,
) {
  return batchAdminGmailResources(action, idsSelection(resourceIds), signal);
}

export function batchAdminGmailResourcesByFilter(
  action: AdminGmailBatchAction,
  filter: AdminGmailResourceListFilter,
  signal?: AbortSignal,
) {
  return batchAdminGmailResources(action, filterSelection(filter), signal);
}

export async function batchAllMatchingAdminGmailResources(
  action: AdminGmailBatchAction,
  filter: AdminGmailResourceListFilter,
  signal?: AbortSignal,
): Promise<AdminGmailBulkResponse> {
  const resourceIds: number[] = [];
  for (let offset = 0; ; offset += ALL_MATCHING_PAGE_SIZE) {
    throwIfAborted(signal);
    const page = await listAdminGmailResources({
      ...filter,
      limit: ALL_MATCHING_PAGE_SIZE,
      offset,
      signal,
    });
    resourceIds.push(...page.items.map((item) => item.id));
    if (page.items.length < ALL_MATCHING_PAGE_SIZE) break;
  }
  const snapshotIds = Array.from(new Set(resourceIds));

  const result: AdminGmailBulkResponse & {
    affectedResourceIds: number[];
    skippedResourceIds: number[];
  } = {
    requested: 0,
    affected: 0,
    skipped: 0,
    affectedResourceIds: [],
    skippedResourceIds: [],
    reasonCounts: [],
  };
  const reasons = new Map<string, number>();
  for (let index = 0; index < snapshotIds.length; index += COMMAND_CHUNK_SIZE) {
    throwIfAborted(signal);
    const chunk = await batchAdminGmailResourcesByIds(
      action,
      snapshotIds.slice(index, index + COMMAND_CHUNK_SIZE),
      signal,
    );
    result.requested += chunk.requested;
    result.affected += chunk.affected;
    result.skipped += chunk.skipped;
    result.affectedResourceIds.push(...(chunk.affectedResourceIds ?? []));
    result.skippedResourceIds.push(...(chunk.skippedResourceIds ?? []));
    for (const item of chunk.reasonCounts) {
      reasons.set(item.reason, (reasons.get(item.reason) ?? 0) + item.count);
    }
  }
  result.reasonCounts = Array.from(reasons, ([reason, count]) => ({
    reason,
    count,
  })).sort((left, right) => left.reason.localeCompare(right.reason));
  return result;
}

async function batchAdminGmailResources(
  action: AdminGmailBatchAction,
  selection: AdminGmailSelection,
  signal?: AbortSignal,
): Promise<AdminGmailBulkResponse> {
  const options = {
    body: { selection },
    params: { header: commandHeaders() },
    signal,
  };
  switch (action) {
    case "validate":
      return unwrap(
        await client.POST("/v1/admin/gmail/resources/batch/validation", options),
      );
    case "history":
      return unwrap(
        await client.POST("/v1/admin/gmail/resources/batch/history", options),
      );
    case "disable":
      return unwrap(
        await client.POST("/v1/admin/gmail/resources/batch/disable", options),
      );
    case "publish":
      return unwrap(
        await client.POST("/v1/admin/gmail/resources/batch/publish", options),
      );
    case "unpublish":
      return unwrap(
        await client.POST("/v1/admin/gmail/resources/batch/unpublish", options),
      );
    case "delete":
      return unwrap(
        await client.POST("/v1/admin/gmail/resources/batch/delete", options),
      );
  }
}

function idsSelection(resourceIds: number[]): AdminGmailSelection {
  return {
    mode: "ids",
    resourceIds: Array.from(new Set(resourceIds)).filter(
      (id) => Number.isInteger(id) && id > 0,
    ),
  };
}

function filterSelection(
  filter: AdminGmailResourceListFilter,
): AdminGmailSelection {
  return {
    mode: "filter",
    filter: {
      ...filter,
      search: filter.search?.trim() || undefined,
    },
  };
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
