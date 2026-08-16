import { IamApiError, apiClient, csrfHeader, unwrap } from "./api-client";
import { generateIdempotencyKey } from "./idempotency";
import type { components } from "./openapi/schema";

export type KitesimTaskStatus = components["schemas"]["AdminKitesimSyncTaskStatus"];
export type KitesimOperationKind = components["schemas"]["AdminKitesimOperationKind"];
export type KitesimOperationStatus = components["schemas"]["AdminKitesimOperationStatus"];
export type KitesimUpstreamAccount = components["schemas"]["AdminKitesimUpstreamAccount"];
export type KitesimProduct = components["schemas"]["AdminKitesimProduct"];
export type KitesimOperation = components["schemas"]["AdminKitesimOperation"];
export type KitesimUpstreamView = components["schemas"]["AdminKitesimUpstream"];
export type KitesimCardProfile = components["schemas"]["AdminKitesimCardInput"];
export type UpdateKitesimUpstreamRequest = components["schemas"]["AdminKitesimUpstreamUpdate"];
export type KitesimOperationResolutionRequest = components["schemas"]["AdminKitesimOperationResolutionRequest"];

export async function getKitesimUpstream(signal?: AbortSignal) {
  return unwrap(await apiClient.GET("/v1/admin/kitesim/upstream", { signal }));
}

export async function listKitesimProducts(signal?: AbortSignal) {
  const response = await unwrap(await apiClient.GET("/v1/admin/kitesim/products", { signal }));
  return response.items;
}

export async function updateKitesimUpstream(body: UpdateKitesimUpstreamRequest) {
  return unwrap(await apiClient.PUT("/v1/admin/kitesim/upstream", {
    body,
    params: { header: csrfHeader() },
  }));
}

export async function refreshKitesimUpstream() {
  return unwrap(await apiClient.POST("/v1/admin/kitesim/upstream/refresh", {
    params: { header: csrfHeader() },
  }));
}

type KitesimCommandKind = "purchase" | "recharge" | "renewal";

interface PendingKitesimCommand {
  key: string;
  signature: string;
}

const pendingCommands = new Map<KitesimCommandKind, PendingKitesimCommand>();

function commandHeaders(key: string) {
  return {
    ...csrfHeader(),
    "Idempotency-Key": key,
  };
}

function normalizeMoneySignature(value: string) {
  const trimmed = value.trim();
  const match = /^([+-]?)(\d+)(?:\.(\d*))?$/.exec(trimmed);
  if (!match) return trimmed;
  const integer = match[2].replace(/^0+(?=\d)/, "");
  const fraction = (match[3] ?? "").replace(/0+$/, "");
  const sign = match[1] === "+" ? "" : match[1];
  return `${sign}${integer}${fraction ? `.${fraction}` : ""}`;
}

function pendingCommand(kind: KitesimCommandKind, signature: string) {
  const current = pendingCommands.get(kind);
  if (current?.signature === signature) return current;
  const next = { key: generateIdempotencyKey(), signature };
  pendingCommands.set(kind, next);
  return next;
}

function clearPendingCommand(
  kind: KitesimCommandKind,
  pending: PendingKitesimCommand,
) {
  if (pendingCommands.get(kind) === pending) pendingCommands.delete(kind);
}

async function runKitesimCommand<T>(
  kind: KitesimCommandKind,
  signature: string,
  run: (headers: ReturnType<typeof commandHeaders>) => Promise<T>,
) {
  const pending = pendingCommand(kind, signature);
  try {
    const result = await run(commandHeaders(pending.key));
    clearPendingCommand(kind, pending);
    return result;
  } catch (error) {
    if (error instanceof IamApiError && error.status >= 400 && error.status < 500) {
      clearPendingCommand(kind, pending);
    }
    throw error;
  }
}

export async function purchaseKitesimNumbers(productId: number, count: number, maxUnitPrice: string) {
  const body = { productId, count, maxUnitPrice };
  return runKitesimCommand(
    "purchase",
    JSON.stringify([productId, count, normalizeMoneySignature(maxUnitPrice)]),
    async (headers) => unwrap(await apiClient.POST("/v1/admin/kitesim/upstream/purchases", {
      body,
      params: { header: headers },
    })),
  );
}

export async function rechargeKitesimAccount(amount: string, cvc: string) {
	return runKitesimCommand(
		"recharge",
		JSON.stringify([normalizeMoneySignature(amount)]),
    async (headers) => unwrap(await apiClient.POST("/v1/admin/kitesim/upstream/recharges", {
      body: { amount, cvc },
      params: { header: headers },
    })),
  );
}

export async function reconcileKitesimOperation(operationId: number) {
	return unwrap(await apiClient.POST("/v1/admin/kitesim/upstream/operations/{operationId}/reconcile", {
		params: { header: csrfHeader(), path: { operationId } },
	}));
}

export async function resolveKitesimOperation(
	operationId: number,
	body: KitesimOperationResolutionRequest,
) {
	return unwrap(await apiClient.POST("/v1/admin/kitesim/upstream/operations/{operationId}/resolution", {
		body,
		params: { header: csrfHeader(), path: { operationId } },
	}));
}

export async function renewKitesimPhone(phoneId: number, productId: number, maxUnitPrice: string) {
  const body = { productId, maxUnitPrice };
  return runKitesimCommand(
    "renewal",
    JSON.stringify([phoneId, productId, normalizeMoneySignature(maxUnitPrice)]),
    async (headers) => unwrap(await apiClient.POST("/v1/admin/kitesim/phones/{phoneId}/renewals", {
      body,
      params: { header: headers, path: { phoneId } },
    })),
  );
}
