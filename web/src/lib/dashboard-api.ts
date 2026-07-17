// Real console data-dashboard API. Replaces the former deterministic mock
// (console-dashboard/dashboard-mock.ts) — the response shape is identical to the
// old DashboardData, so ConsoleOverview and every panel render unchanged. The
// type aliases below are re-exported so the panels keep importing the same names.
import type { components } from "./openapi/schema";
import { apiClient as client, unwrap } from "./api-client";

export type DashboardData = components["schemas"]["DashboardResponse"];
export type DashboardStats = components["schemas"]["DashboardStats"];
export type DashboardTrendPoint = components["schemas"]["DashboardTrendPoint"];
export type DashboardRankItem = components["schemas"]["DashboardRankItem"];
export type DashboardProjectSeries = components["schemas"]["DashboardProjectSeries"];

export async function getDashboardData(
  filter: { createdFrom?: string; createdTo?: string } = {},
) {
  return unwrap<DashboardData>(
    await client.GET("/v1/dashboard", {
      params: {
        query: {
          createdFrom: filter.createdFrom,
          createdTo: filter.createdTo,
        },
      },
    }),
  );
}
