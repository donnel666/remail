import type { components } from "./openapi/schema";
import {
  apiClient,
  csrfHeader,
  turnstileHeader,
  unwrap,
} from "./api-client";
import { generateIdempotencyKey } from "./idempotency";

export type Lottery = components["schemas"]["LotteryResponse"];
export type LotteryTierWeights = components["schemas"]["LotteryTierWeights"];
export type PublicLottery = components["schemas"]["PublicLotteryResponse"];
export type LotteryEntry = components["schemas"]["LotteryEntryResponse"];
export type LotteryPayout = components["schemas"]["LotteryPayoutResponse"];
export type CreateLotteryInput = components["schemas"]["CreateLotteryRequest"];

export async function getPublicLottery(token: string) {
  return unwrap<PublicLottery>(
    await apiClient.GET("/v1/lotteries/{token}", {
      params: { path: { token } },
    }),
  );
}

export async function enterLottery(token: string, turnstileToken: string) {
  return unwrap<LotteryEntry>(
    await apiClient.POST("/v1/lotteries/{token}/entries", {
      params: {
        path: { token },
        header: { ...csrfHeader(), ...turnstileHeader(turnstileToken) },
      },
    }),
  );
}

export async function listAdminLotteries(
  status?: Lottery["status"],
  offset = 0,
  limit = 20,
) {
  return unwrap<components["schemas"]["LotteryListResponse"]>(
    await apiClient.GET("/v1/admin/lotteries", {
      params: { query: { status, offset, limit } },
    }),
  );
}

export async function createAdminLottery(
  body: CreateLotteryInput,
  turnstileToken: string,
  idempotencyKey = generateIdempotencyKey(),
) {
  return unwrap<Lottery>(
    await apiClient.POST("/v1/admin/lotteries", {
      body,
      params: {
        header: {
          ...csrfHeader(),
          ...turnstileHeader(turnstileToken),
          "Idempotency-Key": idempotencyKey,
        },
      },
    }),
  );
}

export async function getAdminLottery(lotteryId: number) {
  return unwrap<Lottery>(
    await apiClient.GET("/v1/admin/lotteries/{lotteryId}", {
      params: { path: { lotteryId } },
    }),
  );
}

export async function listAdminLotteryEntries(
  lotteryId: number,
  offset = 0,
  limit = 50,
) {
  return unwrap<components["schemas"]["LotteryEntryListResponse"]>(
    await apiClient.GET("/v1/admin/lotteries/{lotteryId}/entries", {
      params: { path: { lotteryId }, query: { offset, limit } },
    }),
  );
}

export async function listAdminLotteryPayouts(
  lotteryId: number,
  offset = 0,
  limit = 50,
) {
  return unwrap<components["schemas"]["LotteryPayoutListResponse"]>(
    await apiClient.GET("/v1/admin/lotteries/{lotteryId}/payouts", {
      params: { path: { lotteryId }, query: { offset, limit } },
    }),
  );
}
