import { Tag } from "@douyinfe/semi-ui";

import type { MockOrderStatus, MockServiceMode } from "./orders-mock";

type Translator = (key: string) => string;

export const ORDER_STATUS_VALUES: MockOrderStatus[] = [
  "pending_payment",
  "paid",
  "active",
  "completed",
  "refunded",
  "failed",
  "closed",
];

const STATUS_META: Record<
  MockOrderStatus,
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

export function orderStatusLabel(status: MockOrderStatus, t: Translator) {
  return t(STATUS_META[status].labelKey);
}

export function renderOrderStatusTag(status: MockOrderStatus, t: Translator) {
  const meta = STATUS_META[status];
  return (
    <Tag color={meta.color} shape="circle">
      {t(meta.labelKey)}
    </Tag>
  );
}

export function serviceModeLabel(mode: MockServiceMode, t: Translator) {
  return mode === "code" ? t("Code receiving") : t("Purchase");
}

export function renderServiceModeTag(mode: MockServiceMode, t: Translator) {
  return (
    <Tag color={mode === "code" ? "orange" : "violet"} shape="circle">
      {serviceModeLabel(mode, t)}
    </Tag>
  );
}

export function formatLedgerAmount(value: number) {
  if (!Number.isFinite(value)) return "￥0";
  const text = value.toFixed(6).replace(/\.?0+$/, "");
  return `￥${text || "0"}`;
}

export function formatOrderDateTime(value?: string) {
  if (!value) return "-";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "-";
  const pad = (input: number) => String(input).padStart(2, "0");
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(
    date.getDate()
  )} ${pad(date.getHours())}:${pad(date.getMinutes())}`;
}
