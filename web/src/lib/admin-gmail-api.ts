import { apiClient as client, csrfHeader, unwrap } from "./api-client";
import { generateIdempotencyKey } from "./idempotency";
import type { components, operations } from "./openapi/schema";

export type AdminGmailResourceStatus =
  components["schemas"]["AdminGmailResourceStatus"];
export type AdminGmailResourceItem =
  components["schemas"]["AdminGmailResourceItem"];
export type AdminGmailResourceList =
  components["schemas"]["AdminGmailResourceList"];
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
export interface AdminGmailOwner {
  id: number;
  email: string;
  nickname: string;
  groupName: string;
  role: "user" | "supplier" | "admin" | "super_admin";
  enabled: boolean;
}

const OWNER_PAGE_SIZE = 100;

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
}) {
  const { signal, ...query } = options;
  return unwrap<AdminGmailResourceList>(
    await client.GET("/v1/admin/gmail/resources", {
      params: { query },
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
  const file = new File([request.content], "gmail-resources.txt", {
    type: "text/plain",
  });
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
  enabled: boolean,
) {
  const params = {
    header: csrfHeader(),
    path: { resourceId },
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
