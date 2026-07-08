import type { components } from "./openapi/schema";
import { apiClient as client, unwrap } from "./api-client";

export type OrderMailResponse = components["schemas"]["OrderMailResponse"];

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
