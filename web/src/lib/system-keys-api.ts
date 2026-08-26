import { apiClient, csrfHeader, unwrap } from "./api-client";
import type { components } from "./openapi/schema";

export type AdminSystemKey = components["schemas"]["AdminSystemKey"];
export type SystemKeyPurpose = NonNullable<components["schemas"]["AdminSystemKeyCreateRequest"]["purpose"]>;

export async function listSystemKeys(signal?: AbortSignal) {
  return unwrap(
    await apiClient.GET("/v1/admin/system-keys", { signal }),
  );
}

export async function createSystemKey(name: string, purpose: SystemKeyPurpose) {
  return unwrap(
    await apiClient.POST("/v1/admin/system-keys", {
      body: { name, purpose },
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
