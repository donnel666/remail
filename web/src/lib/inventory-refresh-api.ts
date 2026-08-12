import { apiClient, csrfHeader, unwrap } from "./api-client";
import type { components } from "./openapi/schema";

export type InventoryRefreshItem = components["schemas"]["InventoryRefreshItem"];
export type InventoryRefreshResponse = components["schemas"]["InventoryRefreshResponse"];

export async function getInventoryRefreshes(signal?: AbortSignal): Promise<InventoryRefreshResponse> {
  return unwrap(await apiClient.GET("/v1/admin/inventory/refreshes", { signal }));
}

export async function triggerInventoryRefresh(projectId?: number): Promise<number[]> {
  const response = await unwrap<components["schemas"]["InventoryRefreshAcceptedResponse"]>(
    await apiClient.POST("/v1/admin/inventory/refreshes", {
      params: { header: csrfHeader() },
      body: projectId ? { projectId } : {},
    }),
  );
  return response.projectIds;
}
