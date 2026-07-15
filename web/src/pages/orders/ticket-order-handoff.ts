export const TICKET_CREATE_ORDER_STORAGE_KEY = "remail:ticket-create-order";

export interface TicketOrderSource {
  orderNo: string;
  projectName?: string;
  projectLogoUrl?: string | null;
  deliveryEmail: string;
  payAmount: string | number;
  serviceMode: "code" | "purchase";
  supplyPolicy?: "private_first" | "public_only";
  afterSaleUntil?: string | null;
  receiveUntil?: string | null;
}

export interface TicketOrderRef {
  orderNo: string;
  projectName: string;
  projectLogoUrl?: string;
  deliveryEmail: string;
  payAmount: number;
  serviceMode: "code" | "purchase";
  afterSaleUntil?: string;
  hasSupplier: boolean;
  supplierName?: string;
}

const SUPPLIER_NAMES = [
  "星链数码",
  "云帆账号行",
  "极光资源社",
  "海豚小铺",
] as const;

// Keeps the Orders-to-Tickets handoff independent from the unfinished ticket
// mock UI while preserving the same deterministic preview payload.
export function buildOrderRefFromOrder(order: TicketOrderSource): TicketOrderRef {
  let hash = 0;
  for (const char of order.orderNo) {
    hash = (hash * 31 + char.charCodeAt(0)) >>> 0;
  }
  const hasSupplier = order.supplyPolicy === "private_first" || hash % 5 < 2;
  const payAmount = Number(order.payAmount);
  return {
    orderNo: order.orderNo,
    projectName: order.projectName || "-",
    projectLogoUrl: order.projectLogoUrl ?? undefined,
    deliveryEmail: order.deliveryEmail,
    payAmount: Number.isFinite(payAmount) ? payAmount : 0,
    serviceMode: order.serviceMode,
    afterSaleUntil: order.afterSaleUntil ?? order.receiveUntil ?? undefined,
    hasSupplier,
    supplierName: hasSupplier
      ? SUPPLIER_NAMES[hash % SUPPLIER_NAMES.length]
      : undefined,
  };
}
