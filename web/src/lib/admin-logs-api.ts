import { apiClient, csrfHeader, unwrap } from "@/lib/api-client";
import type { components } from "@/lib/openapi/schema";

export type LogCategory = "system" | "operation";
export type LogLevel = components["schemas"]["AdminLogLevel"];
export type OperationResult = components["schemas"]["AdminOperationLogResult"];

export const LOG_CATEGORIES: LogCategory[] = ["system", "operation"];

export type SystemLogRow = components["schemas"]["AdminSystemLogItem"];
export type OperationLogRow = components["schemas"]["AdminOperationLogItem"];
export type LogRow = SystemLogRow | OperationLogRow;
export type LogFacets = components["schemas"]["AdminLogFacets"];

export interface LogFilter {
  category: LogCategory;
  level?: LogLevel | "all";
  result?: OperationResult | "all";
  search?: string;
  from?: string;
  to?: string;
}

export interface LogPage {
  items: LogRow[];
  total: number;
  facets: LogFacets;
}

export async function listLogs(
  filter: LogFilter,
  offset = 0,
  limit = 20
): Promise<LogPage> {
  const common = {
    search: filter.search?.trim() || undefined,
    from: filter.from,
    to: filter.to,
    offset,
    limit,
  };
  if (filter.category === "system") {
    return unwrap<components["schemas"]["AdminSystemLogListResponse"]>(
      await apiClient.GET("/v1/admin/logs/system", {
        params: {
          query: {
            ...common,
            level:
              filter.level && filter.level !== "all"
                ? filter.level
                : undefined,
          },
        },
      })
    );
  }
  return unwrap<components["schemas"]["AdminOperationLogListResponse"]>(
    await apiClient.GET("/v1/admin/logs/operations", {
      params: {
        query: {
          ...common,
          result:
            filter.result && filter.result !== "all"
              ? filter.result
              : undefined,
        },
      },
    })
  );
}

export async function purgeLogs(
  category: LogCategory,
  before: string
): Promise<number> {
  const params = { query: { before }, header: csrfHeader() };
  const response =
    category === "system"
      ? await apiClient.DELETE("/v1/admin/logs/system", { params })
      : await apiClient.DELETE("/v1/admin/logs/operations", { params });
  return (
    await unwrap<components["schemas"]["AdminLogCleanupResponse"]>(response)
  ).removed;
}
