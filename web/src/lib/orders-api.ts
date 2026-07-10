import type { components } from "./openapi/schema";
import { apiClient as client, csrfHeader, unwrap } from "./api-client";
import { notifyWalletUpdated } from "./wallet-events";

export type CreateOrderRequest = components["schemas"]["CreateOrderRequest"];
export type OrderResponse = components["schemas"]["OrderResponse"];
export type OrderListResponse = components["schemas"]["OrderListResponse"];
export type OrderListFacets = components["schemas"]["OrderListFacets"];
export type OrderStatus = OrderResponse["status"];
export type OrderServiceMode = OrderResponse["serviceMode"];

export async function createOrder(
  payload: CreateOrderRequest,
  options: {
    idempotencyKey: string;
    serviceMode: "purchase" | "code";
    supply: "private_first" | "public_only";
  }
) {
  const response = await unwrap<OrderResponse>(
    await client.POST("/v1/orders", {
      body: payload,
      params: {
        header: {
          ...csrfHeader(),
          "Idempotency-Key": options.idempotencyKey,
        },
        query: {
          serviceMode: options.serviceMode,
          supply: options.supply,
        },
      },
    })
  );
  notifyWalletUpdated();
  return response;
}

export interface OrderListFilter {
  afterId?: number;
  createdFrom?: string;
  createdTo?: string;
  domain?: string;
  limit?: number;
  offset?: number;
  search?: string;
  serviceMode?: OrderServiceMode;
  status?: OrderStatus;
}

export async function listOrders(filter: OrderListFilter) {
  return unwrap<OrderListResponse>(
    await client.GET("/v1/orders", {
      params: {
        query: {
          scope: "mine",
          afterId: filter.afterId,
          offset: filter.offset,
          limit: filter.limit ?? 100,
          search: filter.search,
          serviceMode: filter.serviceMode,
          status: filter.status,
          domain: filter.domain,
          createdFrom: filter.createdFrom,
          createdTo: filter.createdTo,
        },
      },
    })
  );
}

export async function getOrder(orderNo: string) {
  return unwrap<OrderResponse>(
    await client.GET("/v1/orders/{orderNo}", {
      params: { path: { orderNo } },
    })
  );
}
