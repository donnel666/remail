import type { components } from "./openapi/schema";
import { apiClient as client, csrfHeader, unwrap } from "./api-client";

export type CreateOrderRequest = components["schemas"]["CreateOrderRequest"];
export type OrderResponse = components["schemas"]["OrderResponse"];
export type OrderListResponse = components["schemas"]["OrderListResponse"];

export async function createOrder(
  payload: CreateOrderRequest,
  options: {
    idempotencyKey: string;
    serviceMode: "purchase" | "code";
    supply: "private_first" | "public_only";
  }
) {
  return unwrap<OrderResponse>(
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
}

export async function listOrders(filter: {
  offset?: number;
  limit?: number;
  search?: string;
  serviceMode?: "purchase" | "code";
  status?: OrderResponse["status"];
}) {
  return unwrap<OrderListResponse>(
    await client.GET("/v1/orders", {
      params: {
        query: {
          scope: "mine",
          offset: filter.offset ?? 0,
          limit: filter.limit ?? 100,
          search: filter.search,
          serviceMode: filter.serviceMode,
          status: filter.status,
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
