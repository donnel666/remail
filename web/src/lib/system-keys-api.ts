import { apiClient, csrfHeader, unwrap } from "./api-client";
import type { components } from "./openapi/schema";

export type AdminSystemKey = components["schemas"]["AdminSystemKey"];
export type SystemKeyPurpose = NonNullable<components["schemas"]["AdminSystemKeyCreateRequest"]["purpose"]>;
export type BotSystemKeyScope = {
  platform: NonNullable<components["schemas"]["AdminSystemKeyCreateRequest"]["platform"]>;
  subjectNamespace: NonNullable<components["schemas"]["AdminSystemKeyCreateRequest"]["subjectNamespace"]>;
  allowedGroupIds: string[];
};

export async function listSystemKeys(signal?: AbortSignal) {
  return unwrap(
    await apiClient.GET("/v1/admin/system-keys", { signal }),
  );
}

export async function createSystemKey(name: string, purpose: SystemKeyPurpose, scope?: BotSystemKeyScope) {
  return unwrap(
    await apiClient.POST("/v1/admin/system-keys", {
      body: { name, purpose, ...scope },
      params: { header: csrfHeader() },
    }),
  );
}

export async function deleteSystemKey(keyId: number) {
  await unwrap(
    await apiClient.DELETE("/v1/admin/system-keys/{keyId}", {
      params: { header: csrfHeader(), path: { keyId } },
    }),
  );
}
