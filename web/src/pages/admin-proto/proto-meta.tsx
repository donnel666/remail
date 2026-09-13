import type { ReactNode } from "react";
import { Avatar, Tag, Tooltip } from "@douyinfe/semi-ui";
import type { TFunction } from "i18next";

import { CopyableTableText } from "@/components/semi/copyable-table-text";

import type {
  AdminProtoAllocationStatus,
  AdminProtoAsyncTaskKind,
  AdminProtoAsyncTaskStatus,
  AdminProtoMailboxKind,
  AdminProtoMessageStatus,
  AdminProtoOwner,
  AdminProtoOwnerRole,
  AdminProtoResourceStatus,
  AdminProtoSupplyScope,
} from "./admin-proto-types";

const OWNER_AVATAR_COLORS = [
  "amber",
  "blue",
  "cyan",
  "green",
  "indigo",
  "light-blue",
  "light-green",
  "lime",
  "orange",
  "pink",
  "purple",
  "red",
  "teal",
  "violet",
  "yellow",
] as const;

function ownerAvatarColor(ownerId: number) {
  return OWNER_AVATAR_COLORS[Math.abs(ownerId) % OWNER_AVATAR_COLORS.length];
}

function ownerInitial(owner: AdminProtoOwner) {
  const source = owner.nickname?.trim() || owner.email;
  return (source[0] ?? "?").toUpperCase();
}

// Fill the detail drawer's height for single-table tabs: the table body keeps a
// consistent height and only scrolls once the rows overflow it. The panel height
// fills the space between the sticky tab bar and the sticky action footer.
export const DRAWER_TABLE_SCROLL_Y = "max(220px, calc(100vh - 337px))";
export const DRAWER_PANEL_HEIGHT = "max(360px, calc(100vh - 237px))";

export const STATUS_META: Record<
  AdminProtoResourceStatus,
  { color: "green" | "orange" | "red" | "grey" | "blue"; label: string }
> = {
  pending: { color: "blue", label: "Pending" },
  validating: { color: "orange", label: "Validating" },
  identifying: { color: "blue", label: "Identifying" },
  normal: { color: "green", label: "Normal" },
  abnormal: { color: "orange", label: "Abnormal" },
  disabled: { color: "grey", label: "Disabled" },
  deleted: { color: "red", label: "Deleted" },
};

export const TASK_STATUS_META: Record<
  AdminProtoAsyncTaskStatus,
  { color: "green" | "orange" | "red" | "grey" | "blue"; label: string }
> = {
  queued: { color: "blue", label: "Queued" },
  running: { color: "orange", label: "Running" },
  succeeded: { color: "green", label: "Succeeded" },
  failed: { color: "red", label: "Failed" },
  uncertain: { color: "orange", label: "Uncertain" },
  canceled: { color: "grey", label: "Canceled" },
};

export const MESSAGE_STATUS_META: Record<
  AdminProtoMessageStatus,
  { color: "green" | "blue" | "grey"; label: string }
> = {
  received: { color: "blue", label: "Received" },
  matched: { color: "green", label: "Matched" },
  ignored: { color: "grey", label: "Ignored" },
};

export const MAILBOX_META: Record<
  AdminProtoMailboxKind,
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
export const MAILBOX_TEXT_COLOR: Record<AdminProtoMailboxKind, string> = {
  main: "rgb(var(--semi-blue-6))",
  alias: "rgb(var(--semi-violet-6))",
  dot: "rgb(var(--semi-green-6))",
  plus: "rgb(var(--semi-orange-6))",
};

export const SUPPLY_SCOPE_META: Record<
  AdminProtoSupplyScope,
  { color: "blue" | "grey"; label: string }
> = {
  owned: { color: "grey", label: "Owned" },
  public: { color: "blue", label: "Public" },
};

export const ALLOCATION_STATUS_META: Record<
  AdminProtoAllocationStatus,
  { color: "green" | "grey"; label: string }
> = {
  allocated: { color: "green", label: "Allocated" },
  released: { color: "grey", label: "Released" },
};

export function renderMailboxTag(mailbox: AdminProtoMailboxKind, t: TFunction) {
  const meta = MAILBOX_META[mailbox];
  return (
    <Tag color={meta.color} shape="circle" size="small">
      {t(meta.label)}
    </Tag>
  );
}

export function renderMessageStatusTag(status: AdminProtoMessageStatus, t: TFunction) {
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

export function ownerRoleLabel(role: AdminProtoOwnerRole) {
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

export function taskKindLabel(kind: AdminProtoAsyncTaskKind) {
  switch (kind) {
    case "import":
      return "Import";
    case "validation":
      return "Validation";
    case "fetch":
      return "Mail fetch";
    case "history":
      return "Project scan";
    case "bulk_validation":
      return "Validation";
    case "bulk_history":
      return "Project scan";
    case "bulk_publish":
      return "Put on sale";
    case "bulk_unpublish":
      return "Convert to private";
    case "bulk_delete":
      return "Delete";
    case "bulk_disable":
      return "Disable";
    default:
      return kind;
  }
}

export function renderStatusTag(
  status: AdminProtoResourceStatus,
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

export function renderTaskStatusTag(status: AdminProtoAsyncTaskStatus, t: TFunction) {
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
  owner,
  ownerId,
  t,
}: {
  owner?: AdminProtoOwner | null;
  ownerId?: number;
  t: TFunction;
}) {
  if (!owner) return <span className="font-mono">{ownerId ? `#${ownerId}` : "-"}</span>;
  return (
    <div className="flex min-w-0 items-center gap-2.5">
      <Avatar className="shrink-0" color={ownerAvatarColor(owner.id)} size="extra-small">
        {ownerInitial(owner)}
      </Avatar>
      <div className="min-w-0">
        <CopyableTableText copiedText={t("Copied")} text={owner.email} />
        <div className="truncate text-xs text-[var(--semi-color-text-2)]">
          {owner.nickname || "-"} · {t(ownerRoleLabel(owner.role))} · {owner.groupName || "-"}
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
