import { apiClient as client, csrfHeader, unwrap } from "./api-client";
import type { components } from "./openapi/schema";

export type AdminKitesimPhoneStatus =
  components["schemas"]["AdminKitesimPhoneStatus"];
export type AdminKitesimSyncTaskStatus =
  components["schemas"]["AdminKitesimSyncTaskStatus"];
export type AdminKitesimPhoneItem =
  components["schemas"]["AdminKitesimPhoneItem"];
export type AdminKitesimPhoneList =
  components["schemas"]["AdminKitesimPhoneList"];
export type AdminKitesimPhoneFacets =
  components["schemas"]["AdminKitesimPhoneFacets"];
export type AdminKitesimImportResult =
  components["schemas"]["AdminKitesimImportResult"];
export type AdminKitesimSyncTask =
  components["schemas"]["AdminKitesimSyncTask"];
export type AdminKitesimSyncRun =
  components["schemas"]["AdminKitesimSyncRun"];
export type AdminKitesimSyncRunList =
  components["schemas"]["AdminKitesimSyncRunList"];
export type AdminKitesimMessage =
  components["schemas"]["AdminKitesimMessage"];
export type AdminKitesimPhoneMutationResult =
  components["schemas"]["AdminKitesimPhoneMutationResult"];

export interface AdminKitesimListFilter {
  search?: string;
  status?: AdminKitesimPhoneStatus;
  autoRenew?: boolean;
  tokenAvailable?: boolean;
  syncHealthy?: boolean;
  phoneAvailable?: boolean;
  createdFrom?: string;
  createdTo?: string;
}

export async function listAdminKitesimPhones(
  filter: AdminKitesimListFilter = {},
  offset = 0,
  limit = 20,
  signal?: AbortSignal,
): Promise<AdminKitesimPhoneList> {
  return unwrap(
    await client.GET("/v1/admin/kitesim/phones", {
      params: {
        query: {
          ...filter,
          search: filter.search?.trim() || undefined,
          offset: Math.max(0, Math.trunc(offset)),
          limit: Math.max(1, Math.min(100, Math.trunc(limit))),
        },
      },
      signal,
    }),
  );
}

export async function importAdminKitesimAccounts(
  content: string,
): Promise<AdminKitesimImportResult> {
  return unwrap(
    await client.POST("/v1/admin/kitesim/accounts/imports", {
      body: { content },
      params: { header: csrfHeader() },
    }),
  );
}

export async function syncAdminKitesimAccount(
  accountId: number,
): Promise<AdminKitesimSyncTask> {
  return unwrap(
    await client.POST("/v1/admin/kitesim/accounts/{accountId}/sync", {
      params: { header: csrfHeader(), path: { accountId } },
    }),
  );
}

export async function listAdminKitesimAccountTasks(
  accountId: number,
  offset = 0,
  limit = 20,
  signal?: AbortSignal,
): Promise<AdminKitesimSyncRunList> {
  return unwrap(
    await client.GET("/v1/admin/kitesim/accounts/{accountId}/tasks", {
      params: {
        path: { accountId },
        query: {
          offset: Math.max(0, Math.trunc(offset)),
          limit: Math.max(1, Math.min(100, Math.trunc(limit))),
        },
      },
      signal,
    }),
  );
}

export async function disableAdminKitesimPhones(
  phoneIds: number[],
): Promise<AdminKitesimPhoneMutationResult> {
  return unwrap(
    await client.POST("/v1/admin/kitesim/phones/disable", {
      body: { phoneIds },
      params: { header: csrfHeader() },
    }),
  );
}

export async function enableAdminKitesimPhones(
  phoneIds: number[],
): Promise<AdminKitesimPhoneMutationResult> {
  return unwrap(
    await client.POST("/v1/admin/kitesim/phones/enable", {
      body: { phoneIds },
      params: { header: csrfHeader() },
    }),
  );
}

export async function deleteAdminKitesimPhones(
  phoneIds: number[],
  accountIds: number[] = [],
): Promise<AdminKitesimPhoneMutationResult> {
  if (phoneIds.length === 0 && accountIds.length === 0) {
    throw new Error("At least one Kitesim phone or account is required.");
  }
  return unwrap(
    await client.POST("/v1/admin/kitesim/phones/delete", {
      body: { phoneIds, accountIds },
      params: { header: csrfHeader() },
    }),
  );
}

export async function listAdminKitesimMessages(
  phoneId: number,
  signal?: AbortSignal,
): Promise<AdminKitesimMessage[]> {
  const response = await unwrap(
    await client.GET("/v1/admin/kitesim/phones/{phoneId}/messages", {
      params: { path: { phoneId } },
      signal,
    }),
  );
  return response.items;
}
