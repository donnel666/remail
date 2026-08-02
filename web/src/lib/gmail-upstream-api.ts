import { apiClient, csrfHeader, unwrap } from "./api-client";
import type { components } from "./openapi/schema";

export type SMSBowerAccountStatus = components["schemas"]["SMSBowerAccountStatus"];
export type SMSBowerService = components["schemas"]["SMSBowerServiceItem"];
export type GmailUpstreamMapping = components["schemas"]["GmailUpstreamMappingItem"];
export type GmailUpstreamMappingRequest = components["schemas"]["GmailUpstreamMappingRequest"];
export type GmailUpstreamFinanceReport = components["schemas"]["GmailUpstreamFinanceReport"];
export type GmailUpstreamActivation = components["schemas"]["GmailUpstreamActivationItem"];

export async function getSMSBowerStatus(signal?: AbortSignal) {
  return unwrap<SMSBowerAccountStatus>(
    await apiClient.GET("/v1/admin/upstreams/smsbower/status", { signal })
  );
}

export async function syncSMSBower() {
  await unwrap<void>(
    await apiClient.POST("/v1/admin/upstreams/smsbower/sync", {
      params: { header: csrfHeader() },
    })
  );
}

export async function listSMSBowerServices(signal?: AbortSignal) {
  const result = await unwrap<components["schemas"]["SMSBowerServiceList"]>(
    await apiClient.GET("/v1/admin/upstreams/smsbower/services", { signal })
  );
  return result.items;
}

export async function listGmailUpstreamMappings(signal?: AbortSignal) {
  const result = await unwrap<components["schemas"]["GmailUpstreamMappingList"]>(
    await apiClient.GET("/v1/admin/upstreams/smsbower/mappings", { signal })
  );
  return result.items;
}

export async function saveGmailUpstreamMapping(
  projectId: number,
  request: GmailUpstreamMappingRequest
) {
  await unwrap<void>(
    await apiClient.PUT("/v1/admin/upstreams/smsbower/mappings/{projectId}", {
      body: request,
      params: { header: csrfHeader(), path: { projectId } },
    })
  );
}

export async function deleteGmailUpstreamMapping(
  projectId: number,
  source: "smsbower" | "local"
) {
  await unwrap<void>(
    await apiClient.DELETE("/v1/admin/upstreams/smsbower/mappings/{projectId}", {
      params: { header: csrfHeader(), path: { projectId }, query: { source } },
    })
  );
}

export async function getGmailUpstreamFinance(signal?: AbortSignal) {
  return unwrap<GmailUpstreamFinanceReport>(
    await apiClient.GET("/v1/admin/upstreams/smsbower/finance", { signal })
  );
}

export async function listGmailUpstreamActivations(offset = 0, limit = 50, signal?: AbortSignal) {
  return unwrap<components["schemas"]["GmailUpstreamActivationList"]>(
    await apiClient.GET("/v1/admin/upstreams/smsbower/activations", {
      params: { query: { offset, limit } },
      signal,
    })
  );
}
