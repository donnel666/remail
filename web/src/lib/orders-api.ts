import type { components } from "./openapi/schema";
import { apiClient as client, csrfHeader, unwrap } from "./api-client";
import { notifyWalletUpdated } from "./wallet-events";

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

export async function listOrders(filter: {
  afterId?: number;
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
          afterId: filter.afterId,
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
