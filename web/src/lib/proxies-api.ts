import type { components } from "./openapi/schema";
import {
  apiClient as client,
  csrfHeader,
  unwrap,
} from "./api-client";

export type ProxyItem = components["schemas"]["ProxyItem"];
export type ProxyListResponse = components["schemas"]["ProxyListResponse"];
export type ProxyStatsResponse = components["schemas"]["ProxyStatsResponse"];
export type CreateProxyRequest = components["schemas"]["CreateProxyRequest"];
export type ImportProxiesRequest = components["schemas"]["ImportProxiesRequest"];
export type ImportProxiesResponse =
  components["schemas"]["ImportProxiesResponse"];
export type UpdateProxyRequest = components["schemas"]["UpdateProxyRequest"];
export type ProxyBulkFilter = components["schemas"]["ProxyBulkFilter"];
export type DeleteProxiesResponse =
  components["schemas"]["DeleteProxiesResponse"];
export type DisableProxiesResponse =
  components["schemas"]["DisableProxiesResponse"];
export type CheckProxiesResponse =
  components["schemas"]["CheckProxiesResponse"];

export interface ProxyListFilter {
  createdFrom?: string;
  createdTo?: string;
  country?: string;
  ip?: "auto" | "ipv4" | "ipv6";
  ipv6?: boolean;
  pool?: "resource" | "system";
  search?: string;
  status?: "checking" | "normal" | "abnormal" | "disabled" | "expired";
}

export async function listAdminProxies(
  filter: ProxyListFilter = {},
  offset = 0,
  limit = 20
) {
  return unwrap<ProxyListResponse>(
    await client.GET("/v1/admin/proxies", {
      params: {
        query: {
          ...filter,
          offset,
          limit,
        },
      },
    })
  );
}

export async function getAdminProxyStats(filter: ProxyListFilter = {}) {
  return unwrap<ProxyStatsResponse>(
    await client.GET("/v1/admin/proxies/stats", {
      params: { query: filter },
    })
  );
}

export async function createResourceProxy(payload: CreateProxyRequest) {
  return unwrap<ProxyItem>(
    await client.POST("/v1/admin/proxies/resource", {
      body: payload,
      params: { header: csrfHeader() },
    })
  );
}

export async function createSystemProxy(payload: CreateProxyRequest) {
  return unwrap<ProxyItem>(
    await client.POST("/v1/admin/proxies/system", {
      body: payload,
      params: { header: csrfHeader() },
    })
  );
}

export async function importAdminProxies(payload: ImportProxiesRequest) {
  return unwrap<ImportProxiesResponse>(
    await client.POST("/v1/admin/proxies/imports", {
      body: payload,
      params: { header: csrfHeader() },
    })
  );
}

export async function getAdminProxy(proxyId: number) {
  return unwrap<ProxyItem>(
    await client.GET("/v1/admin/proxies/{proxyId}", {
      params: { path: { proxyId } },
    })
  );
}

export async function updateAdminProxy(
  proxyId: number,
  payload: UpdateProxyRequest
) {
  return unwrap<ProxyItem>(
    await client.PATCH("/v1/admin/proxies/{proxyId}", {
      body: payload,
      params: {
        header: csrfHeader(),
        path: { proxyId },
      },
    })
  );
}

export async function checkAdminProxy(proxyId: number) {
  return unwrap<ProxyItem>(
    await client.POST("/v1/admin/proxies/{proxyId}/check", {
      params: {
        header: csrfHeader(),
        path: { proxyId },
      },
    })
  );
}

export async function checkAdminProxies(proxyIds: number[]) {
  return unwrap<CheckProxiesResponse>(
    await client.POST("/v1/admin/proxies/check", {
      body: { proxyIds },
      params: { header: csrfHeader() },
    })
  );
}

export async function checkAdminProxiesByFilter(filter: ProxyBulkFilter) {
  return unwrap<CheckProxiesResponse>(
    await client.POST("/v1/admin/proxies/check", {
      body: { all: true, filter },
      params: { header: csrfHeader() },
    })
  );
}

export async function deleteAdminProxies(proxyIds: number[]) {
  return unwrap<DeleteProxiesResponse>(
    await client.POST("/v1/admin/proxies/delete", {
      body: { proxyIds },
      params: { header: csrfHeader() },
    })
  );
}

export async function deleteAdminProxiesByFilter(filter: ProxyBulkFilter) {
  return unwrap<DeleteProxiesResponse>(
    await client.POST("/v1/admin/proxies/delete", {
      body: { all: true, filter },
      params: { header: csrfHeader() },
    })
  );
}

export async function disableAdminProxiesByFilter(filter: ProxyBulkFilter) {
  return unwrap<DisableProxiesResponse>(
    await client.POST("/v1/admin/proxies/disable", {
      body: { all: true, filter },
      params: { header: csrfHeader() },
    })
  );
}
