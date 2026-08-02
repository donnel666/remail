import { apiClient as client, csrfHeader, unwrap } from "./api-client";
import type { components } from "./openapi/schema";

export type AdminGmailResourceStatus =
  components["schemas"]["AdminGmailResourceStatus"];
export type AdminGmailResourceItem =
  components["schemas"]["AdminGmailResourceItem"];
export type AdminGmailResourceList =
  components["schemas"]["AdminGmailResourceList"];
export type AdminGmailResourceImportRequest =
  components["schemas"]["AdminGmailResourceImportRequest"];
export type AdminGmailResourceImportResult =
  components["schemas"]["AdminGmailResourceImportResult"];

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

export async function importAdminGmailResources(
  request: AdminGmailResourceImportRequest,
) {
  return unwrap<AdminGmailResourceImportResult>(
    await client.POST("/v1/admin/gmail/resources/import", {
      body: request,
      params: { header: csrfHeader() },
    }),
  );
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
