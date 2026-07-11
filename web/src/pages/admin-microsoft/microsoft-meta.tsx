import type { ReactNode } from "react";
import { Avatar, Tag, Tooltip } from "@douyinfe/semi-ui";
import type { TFunction } from "i18next";

import { CopyableTableText } from "@/components/semi/copyable-table-text";

import { avatarColor, userInitial } from "../admin-users/user-meta";
import type {
  AdminMicrosoftAliasInventoryStatus,
  AdminMicrosoftAliasScheduleStatus,
  AdminMicrosoftAllocationStatus,
  AdminMicrosoftAsyncTaskKind,
  AdminMicrosoftAsyncTaskStatus,
  AdminMicrosoftMailboxKind,
  AdminMicrosoftMessageStatus,
  AdminMicrosoftOwnerRole,
  AdminMicrosoftResourceItem,
  AdminMicrosoftResourceStatus,
  AdminMicrosoftSupplyScope,
  AdminMicrosoftTokenHealth,
} from "./admin-microsoft-mock";

// Fill the detail drawer's height for single-table tabs: the table body keeps a
// consistent height and only scrolls once the rows overflow it. The panel height
// fills the space between the sticky tab bar and the sticky action footer.
export const DRAWER_TABLE_SCROLL_Y = "max(220px, calc(100vh - 337px))";
export const DRAWER_PANEL_HEIGHT = "max(360px, calc(100vh - 237px))";

export const STATUS_META: Record<
  AdminMicrosoftResourceStatus,
  { color: "green" | "orange" | "red" | "grey" | "blue"; label: string }
> = {
  pending: { color: "blue", label: "Pending" },
  normal: { color: "green", label: "Normal" },
  abnormal: { color: "orange", label: "Abnormal" },
  disabled: { color: "grey", label: "Disabled" },
  deleted: { color: "red", label: "Deleted" },
};

export const TOKEN_META: Record<
  AdminMicrosoftTokenHealth,
  { color: "green" | "orange" | "red" | "grey"; label: string }
> = {
  valid: { color: "green", label: "Valid" },
  expiring: { color: "orange", label: "Expiring soon" },
  expired: { color: "red", label: "Expired" },
  missing: { color: "grey", label: "Missing" },
};

export const TASK_STATUS_META: Record<
  AdminMicrosoftAsyncTaskStatus,
  { color: "green" | "orange" | "red" | "grey" | "blue"; label: string }
> = {
  queued: { color: "blue", label: "Queued" },
  running: { color: "orange", label: "Running" },
  succeeded: { color: "green", label: "Succeeded" },
  failed: { color: "red", label: "Failed" },
  uncertain: { color: "orange", label: "Uncertain" },
};

export const ALIAS_INVENTORY_META: Record<
  AdminMicrosoftAliasInventoryStatus,
  { color: "green" | "blue" | "grey"; label: string }
> = {
  available: { color: "green", label: "Available" },
  allocated: { color: "blue", label: "Allocated" },
  disabled: { color: "grey", label: "Disabled" },
};

export const ALIAS_SCHEDULE_META: Record<
  AdminMicrosoftAliasScheduleStatus,
  { color: "green" | "orange" | "grey" | "blue"; label: string }
> = {
  pending: { color: "blue", label: "Pending" },
  queued: { color: "blue", label: "Queued" },
  running: { color: "orange", label: "Running" },
  paused: { color: "grey", label: "Paused" },
};

export const MESSAGE_STATUS_META: Record<
  AdminMicrosoftMessageStatus,
  { color: "green" | "blue" | "grey"; label: string }
> = {
  received: { color: "blue", label: "Received" },
  matched: { color: "green", label: "Matched" },
  ignored: { color: "grey", label: "Ignored" },
};

export const MAILBOX_META: Record<
  AdminMicrosoftMailboxKind,
  { color: "blue" | "violet" | "green" | "orange"; label: string }
> = {
  main: { color: "blue", label: "Main mailbox" },
  alias: { color: "violet", label: "Explicit alias" },
  dot: { color: "green", label: "Dot alias" },
  plus: { color: "orange", label: "Plus alias" },
};

// Delivery addresses are distinguished by colour instead of an inline tag, using
// four well-separated hues (blue / violet / green / orange). Semi's palette vars
// hold raw RGB components (e.g. "0,98,214"), so they must be wrapped in rgb();
// the deeper -6 shade reads clearly on white without glare.
export const MAILBOX_TEXT_COLOR: Record<AdminMicrosoftMailboxKind, string> = {
  main: "rgb(var(--semi-blue-6))",
  alias: "rgb(var(--semi-violet-6))",
  dot: "rgb(var(--semi-green-6))",
  plus: "rgb(var(--semi-orange-6))",
};

export const SUPPLY_SCOPE_META: Record<
  AdminMicrosoftSupplyScope,
  { color: "blue" | "grey"; label: string }
> = {
  owned: { color: "grey", label: "Owned" },
  public: { color: "blue", label: "Public" },
};

export const ALLOCATION_STATUS_META: Record<
  AdminMicrosoftAllocationStatus,
  { color: "green" | "grey"; label: string }
> = {
  allocated: { color: "green", label: "Allocated" },
  released: { color: "grey", label: "Released" },
};

export function renderMailboxTag(mailbox: AdminMicrosoftMailboxKind, t: TFunction) {
  const meta = MAILBOX_META[mailbox];
  return (
    <Tag color={meta.color} shape="circle" size="small">
      {t(meta.label)}
    </Tag>
  );
}

export function renderMessageStatusTag(status: AdminMicrosoftMessageStatus, t: TFunction) {
  const meta = MESSAGE_STATUS_META[status];
  return (
    <Tag color={meta.color} shape="circle" size="small">
      {t(meta.label)}
    </Tag>
  );
}

export function formatTime(value?: string | null) {
  if (!value) return "-";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "-";
  return date.toLocaleString();
}

export function formatRemainingTime(value: string | undefined, t: TFunction) {
  if (!value) return "-";
  const timestamp = new Date(value).getTime();
  if (Number.isNaN(timestamp)) return "-";
  const day = 86_400_000;
  const difference = timestamp - Date.now();
  if (difference <= 0) {
    return t("Days overdue", { count: Math.max(1, Math.ceil(-difference / day)) });
  }
  return t("Days remaining", { count: Math.max(1, Math.ceil(difference / day)) });
}

export function ownerRoleLabel(role: AdminMicrosoftOwnerRole) {
  switch (role) {
    case "super_admin":
      return "Super Admin";
    case "admin":
      return "Admin";
    case "supplier":
      return "Supplier";
    default:
      return "User";
  }
}

export function taskKindLabel(kind: AdminMicrosoftAsyncTaskKind) {
  switch (kind) {
    case "validation":
      return "Validation";
    case "import":
      return "Import";
    case "alias":
      return "Alias replenishment";
    case "token":
      return "Token refresh";
    default:
      return "Mail fetch";
  }
}

export function renderStatusTag(
  status: AdminMicrosoftResourceStatus,
  t: TFunction,
  safeError?: string
) {
  const meta = STATUS_META[status];
  const tag = (
    <Tag color={meta.color} shape="circle" size="small">
      {t(meta.label)}
    </Tag>
  );
  return safeError ? <Tooltip content={safeError}>{tag}</Tooltip> : tag;
}

export function renderTokenTag(health: AdminMicrosoftTokenHealth, t: TFunction) {
  const meta = TOKEN_META[health];
  return (
    <Tag color={meta.color} shape="circle" size="small">
      {t(meta.label)}
    </Tag>
  );
}

export function renderProtocolTag(record: AdminMicrosoftResourceItem, t: TFunction) {
  const graphAvailable = record.graphAvailable;
  const imapFallback = record.status === "normal" && !graphAvailable;
  return (
    <Tag
      color={graphAvailable ? "green" : imapFallback ? "blue" : "grey"}
      shape="circle"
      size="small"
    >
      {graphAvailable ? "Graph" : imapFallback ? t("IMAP fallback") : t("Not verified")}
    </Tag>
  );
}

export function renderTaskStatusTag(status: AdminMicrosoftAsyncTaskStatus, t: TFunction) {
  const meta = TASK_STATUS_META[status];
  return (
    <Tag color={meta.color} shape="circle" size="small">
      {t(meta.label)}
    </Tag>
  );
}

export function InfoItem({ label, value }: { label: string; value: ReactNode }) {
  return (
    <div className="min-w-0 rounded-lg bg-[var(--semi-color-fill-0)] px-3 py-2">
      <div className="mb-1 text-xs text-[var(--semi-color-text-2)]">{label}</div>
      <div className="break-words text-sm text-[var(--semi-color-text-0)]">
        {value}
      </div>
    </div>
  );
}

export function OwnerIdentity({
  ownerEmail,
  ownerGroupName,
  ownerId,
  ownerNickname,
  ownerRole,
  t,
}: Pick<
  AdminMicrosoftResourceItem,
  "ownerEmail" | "ownerGroupName" | "ownerId" | "ownerNickname" | "ownerRole"
> & { t: TFunction }) {
  return (
    <div className="flex min-w-0 items-center gap-2.5">
      <Avatar className="shrink-0" color={avatarColor(ownerId)} size="extra-small">
        {userInitial({ email: ownerEmail, nickname: ownerNickname })}
      </Avatar>
      <div className="min-w-0">
        <CopyableTableText copiedText={t("Copied")} text={ownerEmail} />
        <div className="truncate text-xs text-[var(--semi-color-text-2)]">
          {ownerNickname || "-"} · {t(ownerRoleLabel(ownerRole))} · {ownerGroupName || "-"}
        </div>
      </div>
    </div>
  );
}

export function ConfiguredTag({ configured, t }: { configured: boolean; t: TFunction }) {
  return (
    <Tag color={configured ? "green" : "grey"} shape="circle" size="small">
      {configured ? t("Configured") : t("Not configured")}
    </Tag>
  );
}
