import { Tag } from "@douyinfe/semi-ui";

import type { OrderServiceMode, OrderStatus } from "@/lib/orders-api";

type Translator = (key: string) => string;

export const ORDER_STATUS_VALUES: OrderStatus[] = [
  "pending_payment",
  "paid",
  "active",
  "completed",
  "refunded",
  "failed",
  "closed",
];

const STATUS_META: Record<
  OrderStatus,
  { color: "amber" | "cyan" | "green" | "blue" | "grey" | "red" | "white"; labelKey: string }
> = {
  pending_payment: { color: "amber", labelKey: "Pending payment" },
  paid: { color: "cyan", labelKey: "Paid" },
  active: { color: "green", labelKey: "Active" },
  completed: { color: "blue", labelKey: "Completed" },
  refunded: { color: "grey", labelKey: "Refunded" },
  failed: { color: "red", labelKey: "Failed" },
  closed: { color: "white", labelKey: "Closed" },
};

export function orderStatusLabel(status: OrderStatus, t: Translator) {
  return t(STATUS_META[status].labelKey);
}

export function renderOrderStatusTag(status: OrderStatus, t: Translator) {
  const meta = STATUS_META[status];
  return (
    <Tag color={meta.color} shape="circle">
      {t(meta.labelKey)}
    </Tag>
  );
}

export function serviceModeLabel(mode: OrderServiceMode, t: Translator) {
  return mode === "code" ? t("Code receiving") : t("Purchase");
}

export function renderServiceModeTag(mode: OrderServiceMode, t: Translator) {
  return (
    <Tag color={mode === "code" ? "orange" : "violet"} shape="circle">
      {serviceModeLabel(mode, t)}
    </Tag>
  );
}

export function getOrderDomain(email: string) {
  const index = email.lastIndexOf("@");
  return index === -1 ? "" : email.slice(index).toLowerCase();
}

// Ledger amounts are 6-decimal strings; trim trailing zeros for display.
export function formatLedgerAmount(value: string | number) {
  const text = typeof value === "number" ? value.toFixed(6) : value.trim();
  if (!/^-?\d+(\.\d+)?$/.test(text)) return "￥0";
  const trimmed = text.includes(".")
    ? text.replace(/0+$/, "").replace(/\.$/, "")
    : text;
  return `￥${trimmed || "0"}`;
}

export function formatOrderDateTime(value?: string | null) {
  if (!value) return "-";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "-";
  const pad = (input: number) => String(input).padStart(2, "0");
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(
    date.getDate()
  )} ${pad(date.getHours())}:${pad(date.getMinutes())}`;
}
