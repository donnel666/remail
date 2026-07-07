import type { components } from "./openapi/schema";
import { apiClient as client, csrfHeader, unwrap } from "./api-client";

export type WalletResponse = components["schemas"]["WalletResponse"];
export type WalletReferralResponse =
  components["schemas"]["WalletReferralResponse"];
export type WalletReferralTransferResponse =
  components["schemas"]["WalletReferralTransferResponse"];
export type RechargeItem = components["schemas"]["RechargeItem"];
export type RechargeListResponse = components["schemas"]["RechargeListResponse"];
export type RedeemCardResponse = components["schemas"]["RedeemCardResponse"];
export type TransactionItem = components["schemas"]["TransactionItem"];
export type TransactionListResponse =
  components["schemas"]["TransactionListResponse"];

export interface RechargeListFilter {
  search?: string;
  status?: "paying" | "callback" | "reconciled" | "credited" | "failed";
}

function idempotencyKey() {
  if (typeof crypto !== "undefined" && "randomUUID" in crypto) {
    return crypto.randomUUID();
  }
  return `${Date.now()}-${Math.random().toString(36).slice(2)}`;
}

export async function getWallet() {
  return unwrap<WalletResponse>(await client.GET("/v1/wallet"));
}

export async function getWalletReferrals() {
  return unwrap<WalletReferralResponse>(
    await client.GET("/v1/wallet/referrals")
  );
}

export async function transferReferralRewards(key = idempotencyKey()) {
  return unwrap<WalletReferralTransferResponse>(
    await client.POST("/v1/wallet/referrals/transfer", {
      params: {
        header: {
          ...csrfHeader(),
          "Idempotency-Key": key,
        },
      },
    })
  );
}

export async function listRecharges(
  filter: RechargeListFilter = {},
  offset = 0,
  limit = 20
) {
  return unwrap<RechargeListResponse>(
    await client.GET("/v1/recharges", {
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

export async function listWalletTransactions(
  filter: { search?: string } = {},
  offset = 0,
  limit = 20
) {
  return unwrap<TransactionListResponse>(
    await client.GET("/v1/wallet/transactions", {
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

export async function redeemCard(cardKey: string, key = idempotencyKey()) {
  return unwrap<RedeemCardResponse>(
    await client.POST("/v1/cards/redeem", {
      body: { cardKey },
      params: {
        header: {
          ...csrfHeader(),
          "Idempotency-Key": key,
        },
      },
    })
  );
}
