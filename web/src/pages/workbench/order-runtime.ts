import type { WorkbenchOrder } from "./types";

const purchaseAutoFetchWindowMs = 60 * 60 * 1000;

export function shouldAutoFetchOrderMail(
  order: Pick<WorkbenchOrder, "createdAt" | "serviceMode" | "serviceState">,
  now = Date.now(),
) {
  if (order.serviceMode !== "purchase") return true;
  if (
    order.serviceState !== "pending_activation" &&
    order.serviceState !== "in_warranty"
  ) {
    return false;
  }
  const createdAt = Date.parse(order.createdAt);
  return (
    Number.isFinite(createdAt) &&
    createdAt <= now &&
    now - createdAt < purchaseAutoFetchWindowMs
  );
}

export function shouldShowQuickFetchControl(
  order: Pick<
    WorkbenchOrder,
    | "contentMode"
    | "hasDelivery"
    | "maxCodes"
    | "receivedCount"
    | "serviceMode"
    | "verificationCode"
  >,
) {
  if (order.contentMode === "code_only") {
    return (order.receivedCount ?? 0) < (order.maxCodes || 3);
  }
  return (
    !order.verificationCode &&
    (!order.hasDelivery || order.serviceMode === "purchase")
  );
}

export function mergeOrderRuntimeState(
  next: WorkbenchOrder,
  current?: WorkbenchOrder
): WorkbenchOrder {
  if (!current) return next;
  const preserveDeliveredState = current.hasDelivery && !next.hasDelivery;
  const preserveCodes = (current.codes?.length ?? 0) > (next.codes?.length ?? 0);
  return {
    ...next,
    codes: preserveCodes ? current.codes : next.codes,
    codesExpireAt: next.codesExpireAt ?? current.codesExpireAt,
    contentMode: next.contentMode ?? current.contentMode,
    hasDelivery: next.hasDelivery || current.hasDelivery,
    lastFetchedAt:
      next.lastMailReceivedAt ?? current.lastFetchedAt ?? next.lastFetchedAt,
    lastMailReceivedAt:
      preserveCodes
        ? current.lastMailReceivedAt
        : next.lastMailReceivedAt ?? current.lastMailReceivedAt,
    messages: current.messages,
    maxCodes: next.maxCodes || current.maxCodes,
    receivedCount: preserveCodes
      ? current.receivedCount
      : next.receivedCount,
    serviceState: preserveDeliveredState || preserveCodes ? current.serviceState : next.serviceState,
    token: next.token || current.token,
    verificationCode: preserveDeliveredState || preserveCodes
      ? current.verificationCode
      : next.verificationCode,
  };
}
