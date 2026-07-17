// Real admin/platform data-dashboard API. Replaces the deterministic mock
// (admin-dashboard/admin-dashboard-mock.ts) — the response shape matches the
// former AdminDashboardData, so AdminDashboard and every panel render unchanged.
import type { components } from "./openapi/schema";
import { apiClient as client, unwrap } from "./api-client";

export type AdminDashboardData = components["schemas"]["AdminDashboardResponse"];
export type AdminDashboardStats = components["schemas"]["AdminDashboardStats"];
export type AdminDashboardTrendPoint =
  components["schemas"]["AdminDashboardTrendPoint"];
export type AdminDashboardRankItem =
  components["schemas"]["AdminDashboardRankItem"];
export type AdminDashboardInventoryRankItem =
  components["schemas"]["AdminDashboardInventoryRankItem"];

export async function getAdminDashboardData(
  filter: { createdFrom?: string; createdTo?: string } = {},
) {
  return unwrap<AdminDashboardData>(
    await client.GET("/v1/admin/dashboard", {
      params: {
        query: {
          createdFrom: filter.createdFrom,
          createdTo: filter.createdTo,
        },
      },
    }),
  );
}
