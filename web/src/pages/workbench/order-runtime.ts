import type { WorkbenchOrder } from "./types";

export function shouldShowQuickFetchControl(
  order: Pick<
    WorkbenchOrder,
    "hasDelivery" | "productType" | "serviceMode" | "verificationCode"
  >,
) {
  return (
    order.productType !== "domain" &&
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
