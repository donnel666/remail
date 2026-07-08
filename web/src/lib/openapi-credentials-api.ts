import type { components, operations } from "./openapi/schema";
import {
  apiClient as client,
  csrfHeader,
  unwrap,
  type JsonResponse,
} from "./api-client";

export type APIKeyCreateRequest = components["schemas"]["APIKeyCreateRequest"];
export type APIKeyPatchRequest = components["schemas"]["APIKeyPatchRequest"];
export type APIKeyResponse = components["schemas"]["APIKeyResponse"];
export type APIKeyListResponse = components["schemas"]["APIKeyListResponse"];
export type APIKeyUsageResponse = components["schemas"]["APIKeyUsageResponse"];
export type DeleteAPIKeyResponse = JsonResponse<operations["deleteApiKey"], 204>;

export interface APIKeyListFilter {
  limit?: number;
  offset?: number;
}

export async function listAPIKeys(filter: APIKeyListFilter = {}) {
  return unwrap<APIKeyListResponse>(
    await client.GET("/v1/apikeys", {
      params: { query: filter },
    })
  );
}

export async function getAPIKeyUsage() {
  return unwrap<APIKeyUsageResponse>(await client.GET("/v1/apikey-usage"));
}

export async function createAPIKey(payload: APIKeyCreateRequest) {
  return unwrap<APIKeyResponse>(
    await client.POST("/v1/apikeys", {
      body: payload,
      params: {
        header: {
          ...csrfHeader(),
          "Idempotency-Key": `apikey-${crypto.randomUUID()}`,
        },
      },
    })
  );
}

export async function updateAPIKey(keyId: number, payload: APIKeyPatchRequest) {
  return unwrap<APIKeyResponse>(
    await client.PATCH("/v1/apikeys/{keyId}", {
      body: payload,
      params: { header: csrfHeader(), path: { keyId } },
    })
  );
}

export async function deleteAPIKey(keyId: number) {
  return unwrap<DeleteAPIKeyResponse>(
    await client.DELETE("/v1/apikeys/{keyId}", {
      params: { header: csrfHeader(), path: { keyId } },
    })
  );
}
