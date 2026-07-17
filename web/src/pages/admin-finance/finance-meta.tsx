import { Tag } from "@douyinfe/semi-ui";
import type { ReactNode } from "react";
import type { TFunction } from "i18next";

import type {
  FinanceCardKeyStatus,
  FinanceTransactionDirection,
  FinanceTransactionType,
} from "./admin-finance-mock";

const TRANSACTION_TYPE_LABEL: Record<FinanceTransactionType, string> = {
  recharge: "recharge",
  debit: "debit",
  refund: "refund",
  freeze: "freeze",
  credit: "credit",
  withdrawal: "withdrawal",
  manual_adjustment: "manual_adjustment",
  card_redeem: "card_redeem",
  transfer: "transfer",
};

export function formatMoney(value: string | number | null | undefined) {
  const parsed = Number(value ?? 0);
  if (!Number.isFinite(parsed)) return "0.00";
  return parsed.toLocaleString(undefined, {
    minimumFractionDigits: 2,
    maximumFractionDigits: 6,
  });
}

export function formatDateTime(value?: string | null) {
  if (!value) return "-";
  const date = new Date(value);
  if (!Number.isFinite(date.getTime())) return value;
  return date.toLocaleString();
}

export function formatDate(value?: string | null) {
  if (!value) return "-";
  const date = new Date(value);
  if (!Number.isFinite(date.getTime())) return value;
  return date.toLocaleDateString();
}

export function renderEnabledTag(enabled: boolean, t: TFunction) {
  return (
    <Tag color={enabled ? "green" : "grey"} shape="circle" size="small">
      {enabled ? t("Enabled") : t("Disabled")}
    </Tag>
  );
}

export function renderCardKeyStatusTag(
  status: FinanceCardKeyStatus,
  t: TFunction
) {
  return (
    <Tag
      color={status === "enabled" ? "green" : "grey"}
      shape="circle"
      size="small"
    >
      {status === "enabled" ? t("Enabled") : t("Disabled")}
    </Tag>
  );
}

export function renderTransactionTypeTag(
  type: FinanceTransactionType,
  direction: FinanceTransactionDirection,
  t: TFunction
) {
  return (
    <Tag
      color={direction === "in" ? "green" : "orange"}
      shape="circle"
      size="small"
    >
      {t(TRANSACTION_TYPE_LABEL[type] ?? type)}
    </Tag>
  );
}

export function renderDirectionTag(
  direction: FinanceTransactionDirection,
  t: TFunction
) {
  return (
    <Tag
      color={direction === "in" ? "green" : "orange"}
      shape="circle"
      size="small"
    >
      {direction === "in" ? t("Inflow") : t("Outflow")}
    </Tag>
  );
}

export function moneyClassName(direction?: FinanceTransactionDirection) {
  if (direction === "in") return "font-mono-data text-[var(--semi-color-success)]";
  if (direction === "out") {
    return "font-mono-data text-[var(--semi-color-warning)]";
  }
  return "font-mono-data";
}

// Mirrors admin-microsoft's InfoItem so detail drawers share the same style line.
export function InfoItem({
  label,
  value,
}: {
  label: string;
  value: ReactNode;
}) {
  return (
    <div className="min-w-0 rounded-lg bg-[var(--semi-color-fill-0)] px-3 py-2">
      <div className="mb-1 text-xs text-[var(--semi-color-text-2)]">{label}</div>
      <div className="break-words text-sm text-[var(--semi-color-text-0)]">
        {value}
      </div>
    </div>
  );
}

// Copied from admin-microsoft so in-drawer tables fill the height and scroll
// internally with a sticky header, matching the Microsoft detail sheet.
export const DRAWER_TABLE_SCROLL_Y = "max(220px, calc(100vh - 337px))";
export const DRAWER_PANEL_HEIGHT = "max(360px, calc(100vh - 237px))";
