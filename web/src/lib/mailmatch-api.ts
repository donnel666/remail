import type { components } from "./openapi/schema";
import { apiClient as client, unwrap } from "./api-client";

export type OrderMailResponse = components["schemas"]["OrderMailResponse"];
export type MailContentDetailResponse =
  components["schemas"]["MailContentDetailResponse"];
export type PickupBatchRequest = components["schemas"]["PickupBatchRequest"];
export type PickupBatchResponse = components["schemas"]["PickupBatchResponse"];

export async function readPickupMail(email: string, token: string) {
  return unwrap<OrderMailResponse>(
    await client.GET("/v1/pickup", {
      params: {
        query: {
          email,
          token,
        },
      },
    })
  );
}

export async function readPickupMessage(
  email: string,
  token: string,
  messageId: number
) {
  return unwrap<MailContentDetailResponse>(
    await client.GET("/v1/pickup/messages/{messageId}", {
      params: {
        path: { messageId },
        query: { email, token },
      },
    })
  );
}

export async function readPickupMailBatch(items: PickupBatchRequest["items"]) {
  return unwrap<PickupBatchResponse>(
    await client.POST("/v1/pickup/batch", {
      body: { items },
    }),
  );
}
