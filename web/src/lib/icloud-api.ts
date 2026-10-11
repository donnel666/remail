import { apiClient, csrfHeader, turnstileHeader, unwrap } from "./api-client";
import { generateIdempotencyKey } from "./idempotency";
import type { components, operations } from "./openapi/schema";

export type ICloudResource = components["schemas"]["OwnedICloudResource"];
export type ICloudResourceList = components["schemas"]["OwnedICloudResourceList"];
export type ICloudImport = components["schemas"]["OwnedICloudImport"];
export type ICloudAliasList = components["schemas"]["AdminICloudAliasListResponse"];
export type ICloudCommand = components["schemas"]["OwnedICloudBatchCommand"]["command"];
export type ICloudListQuery = NonNullable<operations["getOwnedICloudResources"]["parameters"]["query"]>;

export async function listICloudResources(query: ICloudListQuery, signal?: AbortSignal): Promise<ICloudResourceList> {
  return unwrap(await apiClient.GET("/v1/icloud/resources", { params: { query }, signal }));
}

export async function importICloudResources(file: File, key: string, token: string, signal?: AbortSignal): Promise<ICloudImport> {
  return unwrap(await apiClient.POST("/v1/icloud/resources/imports", {
    params: { header: { ...csrfHeader(), ...turnstileHeader(token), "Idempotency-Key": key } },
    body: { file: file as unknown as string },
    bodySerializer: () => {
      const form = new FormData();
      form.append("file", file);
      return form;
    },
    signal,
  }));
}

export async function getICloudImport(importId: number, signal?: AbortSignal): Promise<ICloudImport> {
  return unwrap(await apiClient.GET("/v1/icloud/resources/imports/{importId}", { params: { path: { importId } }, signal }));
}

export async function listICloudAliases(resourceId: number, offset = 0, signal?: AbortSignal): Promise<ICloudAliasList> {
  return unwrap(await apiClient.GET("/v1/icloud/resources/{resourceId}/aliases", {
    params: { path: { resourceId }, query: { offset, limit: 20 } }, signal,
  }));
}

export async function applyICloudCommand(command: ICloudCommand, resources: Pick<ICloudResource, "id" | "version">[]) {
  return unwrap(await apiClient.POST("/v1/icloud/resources/actions", {
    params: { header: { ...csrfHeader(), "Idempotency-Key": generateIdempotencyKey() } },
    body: { command, resources: resources.map(({ id, version }) => ({ id, version })) },
  }));
}

export async function applyICloudCommandByFilter(command: ICloudCommand, filter: Omit<ICloudListQuery, "offset" | "limit">) {
  return unwrap(await apiClient.POST("/v1/icloud/resources/actions", {
    params: { header: { ...csrfHeader(), "Idempotency-Key": generateIdempotencyKey() } },
    body: { command, filter },
  }));
}
