import type { components } from "./openapi/schema";
import { apiClient as client, csrfHeader, unwrap } from "./api-client";
import { generateIdempotencyKey } from "./idempotency";
import type {
  AdminMicrosoftAliasListResponse,
  AdminMicrosoftAllocationListResponse,
  AdminMicrosoftAuxiliaryMessageDetail,
  AdminMicrosoftBindingMessageListResponse,
  AdminMicrosoftBulkFilter,
  AdminMicrosoftBulkResponse,
  AdminMicrosoftImportResponse,
  AdminMicrosoftListFilter,
  AdminMicrosoftListResponse,
  AdminMicrosoftMessageDetail,
  AdminMicrosoftMessageCursor,
  AdminMicrosoftMessageListResponse,
  AdminMicrosoftMutationResponse,
  AdminMicrosoftOwner,
  AdminMicrosoftResourceDetail,
  AdminMicrosoftResourceSelection,
  AdminMicrosoftTaskAcceptedResponse,
  AdminMicrosoftAsyncTask,
  AdminMicrosoftTaskListResponse,
  ImportAdminMicrosoftResourcesRequest,
  ReplaceAdminMicrosoftCredentialsRequest,
  UpdateAdminMicrosoftResourceRequest,
} from "@/pages/admin-microsoft/admin-microsoft-types";

type AdminUserListDTO = components["schemas"]["AdminUserListResponse"];
export type AdminMicrosoftBulkCommandResponse =
  | AdminMicrosoftBulkResponse
  | AdminMicrosoftTaskAcceptedResponse;

const MAX_PAGE_SIZE = 100;
const OWNER_PAGE_SIZE = 100;

function commandHeaders() {
  return {
    ...csrfHeader(),
    "Idempotency-Key": generateIdempotencyKey(),
  };
}

function pageLimit(limit: number) {
  if (!Number.isFinite(limit)) return 20;
  return Math.max(1, Math.min(MAX_PAGE_SIZE, Math.trunc(limit)));
}

function normalizeFilter(filter: AdminMicrosoftListFilter) {
  const suffix = filter.suffix?.trim();
  return {
    search: filter.search?.trim() || undefined,
    status: filter.status === "all" ? undefined : filter.status,
    forSale: filter.forSale,
    longLived: filter.longLived,
    graphAvailable: filter.graphAvailable,
    tokenHealth:
      filter.tokenHealth === "all" ? undefined : filter.tokenHealth,
    suffix: suffix?.startsWith("@") ? suffix.slice(1) : suffix || undefined,
    createdFrom: filter.createdFrom,
    createdTo: filter.createdTo,
  };
}

function idsSelection(resourceIds: number[]) {
  return {
    mode: "ids" as const,
    resourceIds: Array.from(new Set(resourceIds)).filter(
      (id) => Number.isInteger(id) && id > 0
    ),
  };
}

function filterSelection(
  filter: AdminMicrosoftListFilter
): AdminMicrosoftResourceSelection {
  const bulkFilter: AdminMicrosoftBulkFilter = {
    type: "microsoft",
    ...normalizeFilter(filter),
  };
  return { mode: "filter", filter: bulkFilter };
}

function requireSynchronousBulkResult(
  response: AdminMicrosoftBulkCommandResponse
): AdminMicrosoftBulkResponse {
  if ("affected" in response) return response;
  throw new Error("The server returned an asynchronous result for an ids-only command.");
}

export async function listAdminMicrosoftResources(
  filter: AdminMicrosoftListFilter = {},
  offset = 0,
  limit = 20,
  afterId?: number,
  signal?: AbortSignal
): Promise<AdminMicrosoftListResponse> {
  return unwrap(
    await client.GET("/v1/admin/resources", {
      params: {
        query: {
          type: "microsoft",
          ...normalizeFilter(filter),
          offset: Math.max(0, Math.trunc(offset)),
          limit: pageLimit(limit),
          afterId,
        },
      },
      signal,
    })
  );
}

export async function getAdminMicrosoftResourceDetail(
  resourceId: number,
  signal?: AbortSignal
): Promise<AdminMicrosoftResourceDetail> {
  return unwrap(
    await client.GET("/v1/admin/resources/{resourceId}", {
      params: { path: { resourceId } },
      signal,
    })
  );
}

export async function listAdminMicrosoftAllocations(
  resourceId: number,
  offset = 0,
  limit = 20,
  signal?: AbortSignal
): Promise<AdminMicrosoftAllocationListResponse> {
  return unwrap(
    await client.GET("/v1/admin/allocations", {
      params: {
        query: {
          type: "microsoft",
          resourceId,
          offset: Math.max(0, Math.trunc(offset)),
          limit: pageLimit(limit),
        },
      },
      signal,
    })
  );
}

export async function listAdminMicrosoftOwners(
  search = "",
  signal?: AbortSignal
): Promise<AdminMicrosoftOwner[]> {
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
    })
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

export async function importAdminMicrosoftResources(
  payload: ImportAdminMicrosoftResourcesRequest,
  signal?: AbortSignal
): Promise<AdminMicrosoftImportResponse> {
  const formData = new FormData();
  const file = new File([payload.content], "microsoft-resources.txt", {
    type: "text/plain",
  });
  formData.append("file", file);
  formData.append("ownerId", String(payload.ownerId));
  formData.append("longLived", String(payload.longLived));
  formData.append("errorStrategy", payload.errorStrategy);

  const response = await unwrap<AdminMicrosoftImportResponse>(
    await client.POST("/v1/admin/resources/imports", {
      body: formData as never,
      bodySerializer: (body) => body,
      params: { header: commandHeaders() },
      signal,
    })
  );
  if (response.status !== "processing") return response;
  const completed = await waitForAdminMicrosoftResourceImport(response.importId, {
    signal,
  });
  return {
    ...completed,
    taskId: response.taskId,
    requestId: response.requestId,
    reused: response.reused,
  };
}

export async function getAdminMicrosoftResourceImport(
  importId: number,
  signal?: AbortSignal
): Promise<AdminMicrosoftImportResponse> {
  return unwrap(
    await client.GET("/v1/admin/resources/imports/{importId}", {
      params: { path: { importId } },
      signal,
    })
  );
}

export async function waitForAdminMicrosoftResourceImport(
  importId: number,
  options: {
    intervalMs?: number;
    maxAttempts?: number;
    signal?: AbortSignal;
  } = {}
): Promise<AdminMicrosoftImportResponse> {
  const intervalMs = options.intervalMs ?? 1_000;
  const maxAttempts = options.maxAttempts ?? 120;
  for (let attempt = 0; attempt < maxAttempts; attempt += 1) {
    throwIfAborted(options.signal);
    const status = await getAdminMicrosoftResourceImport(importId, options.signal);
    if (status.status !== "processing") return status;
    if (attempt + 1 < maxAttempts) {
      await abortableDelay(intervalMs, options.signal);
    }
  }
  throwIfAborted(options.signal);
  throw new Error("The Microsoft resource import is still processing.");
}

export async function updateAdminMicrosoftResource(
  resourceId: number,
  patch: UpdateAdminMicrosoftResourceRequest
): Promise<AdminMicrosoftResourceDetail> {
  const response = await unwrap<AdminMicrosoftMutationResponse>(
    await client.PATCH("/v1/admin/resources/{resourceId}", {
      body: patch,
      params: {
        header: commandHeaders(),
        path: { resourceId },
      },
    })
  );
  return response.resource;
}

export async function replaceAdminMicrosoftCredentials(
  resourceId: number,
  payload: ReplaceAdminMicrosoftCredentialsRequest
): Promise<AdminMicrosoftResourceDetail> {
  const response = await unwrap<AdminMicrosoftMutationResponse>(
    await client.PUT("/v1/admin/resources/{resourceId}/credentials", {
      body: payload,
      params: {
        header: commandHeaders(),
        path: { resourceId },
      },
    })
  );
  return response.resource;
}

export async function validateAdminMicrosoftResource(
  resourceId: number
): Promise<AdminMicrosoftTaskAcceptedResponse> {
  return unwrap(
    await client.POST("/v1/admin/resources/{resourceId}/validate", {
      params: {
        header: commandHeaders(),
        path: { resourceId },
      },
    })
  );
}

export async function enableAdminMicrosoftResource(
  resourceId: number,
  version: number
): Promise<AdminMicrosoftResourceDetail> {
  const response = await unwrap<AdminMicrosoftMutationResponse>(
    await client.POST("/v1/admin/resources/{resourceId}/enable", {
      params: {
        header: commandHeaders(),
        path: { resourceId },
        query: { version },
      },
    })
  );
  return response.resource;
}

export async function disableAdminMicrosoftResource(
  resourceId: number,
  version: number
): Promise<void> {
  await unwrap(
    await client.POST("/v1/admin/resources/{resourceId}/disable", {
      params: {
        header: commandHeaders(),
        path: { resourceId },
        query: { version },
      },
    })
  );
}

export async function publishAdminMicrosoftResource(
  resourceId: number,
  version: number
): Promise<void> {
  await unwrap(
    await client.POST("/v1/admin/resources/{resourceId}/publish", {
      params: {
        header: commandHeaders(),
        path: { resourceId },
        query: { version },
      },
    })
  );
}

export async function unpublishAdminMicrosoftResource(
  resourceId: number,
  version: number
): Promise<void> {
  await unwrap(
    await client.POST("/v1/admin/resources/{resourceId}/unpublish", {
      params: {
        header: commandHeaders(),
        path: { resourceId },
        query: { version },
      },
    })
  );
}

export async function deleteAdminMicrosoftResource(
  resourceId: number,
  version: number
): Promise<void> {
  await unwrap(
    await client.DELETE("/v1/admin/resources/{resourceId}", {
      params: {
        header: commandHeaders(),
        path: { resourceId },
        query: { version },
      },
    })
  );
}

export async function recoverAdminMicrosoftResource(
  resourceId: number,
  version: number
): Promise<AdminMicrosoftResourceDetail> {
  const response = await unwrap<AdminMicrosoftMutationResponse>(
    await client.POST("/v1/admin/resources/{resourceId}/recover", {
      params: {
        header: commandHeaders(),
        path: { resourceId },
        query: { version },
      },
    })
  );
  return response.resource;
}

export async function refreshAdminMicrosoftToken(
  resourceId: number
): Promise<AdminMicrosoftTaskAcceptedResponse> {
  return unwrap(
    await client.POST("/v1/admin/resources/{resourceId}/token/refresh", {
      params: { header: commandHeaders(), path: { resourceId } },
    })
  );
}

export async function createAdminMicrosoftExplicitAlias(
  resourceId: number
): Promise<AdminMicrosoftTaskAcceptedResponse> {
  return unwrap(
    await client.POST("/v1/admin/resources/{resourceId}/aliases", {
      params: { header: commandHeaders(), path: { resourceId } },
    })
  );
}

export async function fetchAdminMicrosoftMail(
  resourceId: number
): Promise<AdminMicrosoftTaskAcceptedResponse> {
  return unwrap(
    await client.POST("/v1/admin/resources/{resourceId}/messages/fetch", {
      params: { header: commandHeaders(), path: { resourceId } },
    })
  );
}

export async function listAdminMicrosoftAliases(
  resourceId: number,
  kind: "explicit" | "other",
  offset = 0,
  limit = 20,
  signal?: AbortSignal
): Promise<AdminMicrosoftAliasListResponse> {
  return unwrap(
    await client.GET("/v1/admin/resources/{resourceId}/aliases", {
      params: {
        path: { resourceId },
        query: {
          kind,
          offset: Math.max(0, Math.trunc(offset)),
          limit: pageLimit(limit),
        },
      },
      signal,
    })
  );
}

export async function listAdminMicrosoftTasks(
  resourceId: number,
  offset = 0,
  limit = 20,
  signal?: AbortSignal
): Promise<AdminMicrosoftTaskListResponse> {
  return unwrap(
    await client.GET("/v1/admin/tasks", {
      params: {
        query: {
          bizType: "microsoft_resource",
          bizId: resourceId,
          offset: Math.max(0, Math.trunc(offset)),
          limit: pageLimit(limit),
        },
      },
      signal,
    })
  );
}

export async function getAdminMicrosoftTask(
  taskId: string,
  signal?: AbortSignal
): Promise<AdminMicrosoftAsyncTask> {
  return unwrap(
    await client.GET("/v1/admin/tasks/{taskId}", {
      params: { path: { taskId } },
      signal,
    })
  );
}

async function waitForAdminMicrosoftTask(
  task: AdminMicrosoftAsyncTask,
  options: {
    intervalMs?: number;
    maxAttempts?: number;
    signal?: AbortSignal;
  } = {}
) {
  const intervalMs = options.intervalMs ?? 1_000;
  const maxAttempts = options.maxAttempts ?? 300;
  let current = task;
  for (let attempt = 0; attempt < maxAttempts; attempt += 1) {
    if (current.status === "succeeded") return current;
    if (["failed", "uncertain", "canceled"].includes(current.status)) {
      const progress = current.progress;
      if (!progress) {
        throw new Error("The administrator batch command did not complete successfully.");
      }
      const reasons = progress.reasonCounts
        .map((item) => `${item.reason}: ${item.count}`)
        .join(", ");
      throw new Error(
        `The administrator batch command did not complete successfully. ` +
          `Processed ${progress.processed}/${progress.total}; ` +
          `succeeded ${progress.succeeded}, skipped ${progress.skipped}, failed ${progress.failed}.` +
          (reasons ? ` Reasons: ${reasons}.` : "")
      );
    }
    throwIfAborted(options.signal);
    await abortableDelay(intervalMs, options.signal);
    current = await getAdminMicrosoftTask(current.taskId, options.signal);
  }
  throw new Error("The administrator batch command is still running.");
}

export async function listAdminMicrosoftMessages(
  resourceId: number,
  search = "",
  limit = 100,
  cursor?: AdminMicrosoftMessageCursor,
  signal?: AbortSignal
): Promise<AdminMicrosoftMessageListResponse> {
  return unwrap(
    await client.GET("/v1/admin/messages", {
      params: {
        query: {
          resourceId,
          type: "microsoft",
          search: search.trim() || undefined,
          offset: cursor ? undefined : 0,
          beforeReceivedAt: cursor?.beforeReceivedAt,
          beforeId: cursor?.beforeId,
          includeTotal: !cursor,
          limit: pageLimit(limit),
        },
      },
      signal,
    })
  );
}

export async function getAdminMicrosoftMessage(
  resourceId: number,
  messageId: number,
  signal?: AbortSignal
): Promise<AdminMicrosoftMessageDetail> {
  return unwrap(
    await client.GET("/v1/admin/messages/{messageId}", {
      params: { path: { messageId }, query: { resourceId } },
      signal,
    })
  );
}

export async function listAdminMicrosoftBindingMessages(
  resourceId: number,
  search = "",
  limit = 100,
  cursor?: AdminMicrosoftMessageCursor,
  signal?: AbortSignal
): Promise<AdminMicrosoftBindingMessageListResponse> {
  return unwrap(
    await client.GET("/v1/admin/bindings", {
      params: {
        query: {
          resourceId,
          search: search.trim() || undefined,
          offset: cursor ? undefined : 0,
          beforeReceivedAt: cursor?.beforeReceivedAt,
          beforeId: cursor?.beforeId,
          includeTotal: !cursor,
          limit: pageLimit(limit),
        },
      },
      signal,
    })
  );
}

export async function getAdminMicrosoftBindingMessage(
  resourceId: number,
  messageId: number,
  signal?: AbortSignal
): Promise<AdminMicrosoftAuxiliaryMessageDetail> {
  return unwrap(
    await client.GET("/v1/admin/bindings/messages/{messageId}", {
      params: { path: { messageId }, query: { resourceId } },
      signal,
    })
  );
}

export function validateAdminMicrosoftResourcesByIds(resourceIds: number[]) {
  return validateAdminMicrosoftResources(idsSelection(resourceIds));
}

export function validateAdminMicrosoftResourcesByFilter(
  filter: AdminMicrosoftListFilter
) {
  return validateAdminMicrosoftResources(filterSelection(filter));
}

async function validateAdminMicrosoftResources(
  selection: AdminMicrosoftResourceSelection
): Promise<AdminMicrosoftTaskAcceptedResponse> {
  return unwrap(
    await client.POST("/v1/admin/resources/validations", {
      body: { selection },
      params: { header: commandHeaders() },
    })
  );
}

export async function disableAdminMicrosoftResourcesByIds(
  resourceIds: number[]
): Promise<AdminMicrosoftBulkResponse> {
  return unwrap(
    await client.POST("/v1/admin/resources/disable", {
      body: { selection: idsSelection(resourceIds) },
      params: { header: commandHeaders() },
    })
  );
}

export async function setAdminMicrosoftResourcesForSaleByIds(
  resourceIds: number[],
  forSale: boolean
): Promise<AdminMicrosoftBulkResponse> {
  const options = {
    body: { selection: idsSelection(resourceIds) },
    params: { header: commandHeaders() },
  };
  const response: AdminMicrosoftBulkCommandResponse = forSale
    ? await unwrap(await client.POST("/v1/admin/resources/publish", options))
    : await unwrap(await client.POST("/v1/admin/resources/unpublish", options));
  return requireSynchronousBulkResult(response);
}

export function setAdminMicrosoftResourcesForSaleByFilter(
  filter: AdminMicrosoftListFilter,
  forSale: boolean
) {
  return setAdminMicrosoftResourcesForSale(filterSelection(filter), forSale);
}

async function setAdminMicrosoftResourcesForSale(
  selection: AdminMicrosoftResourceSelection,
  forSale: boolean
): Promise<AdminMicrosoftBulkCommandResponse> {
  const options = {
    body: { selection },
    params: { header: commandHeaders() },
  };
  const response: AdminMicrosoftBulkCommandResponse = forSale
    ? await unwrap(await client.POST("/v1/admin/resources/publish", options))
    : await unwrap(await client.POST("/v1/admin/resources/unpublish", options));
  if ("affected" in response) return response;
  return {
    ...response,
    task: await waitForAdminMicrosoftTask(response.task),
  };
}

export async function deleteAdminMicrosoftResourcesByIds(
  resourceIds: number[]
): Promise<AdminMicrosoftBulkResponse> {
  const response = await unwrap<AdminMicrosoftBulkCommandResponse>(
    await client.POST("/v1/admin/resources/delete", {
      body: { selection: idsSelection(resourceIds) },
      params: { header: commandHeaders() },
    })
  );
  return requireSynchronousBulkResult(response);
}

export async function deleteAdminMicrosoftResourcesByFilter(
  filter: AdminMicrosoftListFilter
): Promise<AdminMicrosoftBulkCommandResponse> {
  const response = await unwrap<AdminMicrosoftBulkCommandResponse>(
    await client.POST("/v1/admin/resources/delete", {
      body: { selection: filterSelection(filter) },
      params: { header: commandHeaders() },
    })
  );
  if ("affected" in response) return response;
  return {
    ...response,
    task: await waitForAdminMicrosoftTask(response.task),
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
