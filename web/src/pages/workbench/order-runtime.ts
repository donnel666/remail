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
    "hasDelivery" | "serviceMode" | "verificationCode"
  >,
) {
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
  return {
    ...next,
    hasDelivery: next.hasDelivery || current.hasDelivery,
    lastFetchedAt:
      next.lastMailReceivedAt ?? current.lastFetchedAt ?? next.lastFetchedAt,
    lastMailReceivedAt:
      next.lastMailReceivedAt ?? current.lastMailReceivedAt,
    messages: current.messages,
    serviceState: preserveDeliveredState ? current.serviceState : next.serviceState,
    token: next.token || current.token,
    verificationCode: next.verificationCode || current.verificationCode,
  };
}
