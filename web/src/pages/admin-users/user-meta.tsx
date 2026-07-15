import { Tag } from "@douyinfe/semi-ui";
import type { TFunction } from "i18next";

import type {
  AdminTransactionType,
  AdminUserRole,
} from "./admin-users-mock";

type TagColor =
  | "grey"
  | "blue"
  | "green"
  | "orange"
  | "red"
  | "violet"
  | "cyan"
  | "light-blue";

const ROLE_META: Record<AdminUserRole, { color: TagColor; labelKey: string }> = {
  user: { color: "grey", labelKey: "User" },
  supplier: { color: "cyan", labelKey: "Supplier" },
  admin: { color: "blue", labelKey: "Admin" },
  super_admin: { color: "violet", labelKey: "Super Admin" },
};

export function roleLabel(role: AdminUserRole, t: TFunction) {
  return t(ROLE_META[role]?.labelKey ?? role);
}

export function renderRoleTag(role: AdminUserRole, t: TFunction) {
  const meta = ROLE_META[role] ?? { color: "grey" as const, labelKey: role };
  return (
    <Tag color={meta.color} shape="circle" size="small">
      {t(meta.labelKey)}
    </Tag>
  );
}

export function renderStatusTag(enabled: boolean, t: TFunction) {
  return (
    <Tag color={enabled ? "green" : "grey"} shape="circle" size="small">
      {enabled ? t("Enabled") : t("Disabled")}
    </Tag>
  );
}

export function renderGroupTag(name: string) {
  return (
    <Tag color="light-blue" shape="circle" size="small">
      {name}
    </Tag>
  );
}

const TRANSACTION_DIRECTION_COLOR: Record<"in" | "out", TagColor> = {
  in: "green",
  out: "orange",
};

export function renderTransactionTypeTag(
  type: AdminTransactionType,
  direction: "in" | "out",
  t: TFunction
) {
  return (
    <Tag color={TRANSACTION_DIRECTION_COLOR[direction]} shape="circle" size="small">
      {t(type)}
    </Tag>
  );
}

export function formatMoney(value: string | number | null | undefined) {
  const parsed = Number(value ?? 0);
  if (!Number.isFinite(parsed)) return "0.00";
  return parsed.toLocaleString(undefined, {
    minimumFractionDigits: 2,
    maximumFractionDigits: 2,
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

const AVATAR_COLORS = [
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

export function avatarColor(seed: number) {
  return AVATAR_COLORS[Math.abs(seed) % AVATAR_COLORS.length];
}

export function userInitial(user: { nickname?: string | null; email: string }) {
  const source = user.nickname?.trim() || user.email;
  return (source[0] ?? "?").toUpperCase();
}

export function permissionResourceLabel(resource: string, t: TFunction) {
  const key = `Permission ${resource}`;
  const label = t(key);
  return label === key ? resource : label;
}

export function permissionActionLabel(action: string, t: TFunction) {
  const key = `Permission ${action}`;
  const label = t(key);
  return label === key ? action : label;
}

export function formatRelativeTime(value: string | null | undefined, t: TFunction) {
  if (!value) return t("Never");
  const date = new Date(value);
  const timestamp = date.getTime();
  if (!Number.isFinite(timestamp)) return "-";
  const diffMs = Date.now() - timestamp;
  const minutes = Math.floor(diffMs / 60_000);
  if (minutes < 1) return t("Just now");
  if (minutes < 60) return t("Minutes ago", { count: minutes });
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return t("Hours ago", { count: hours });
  const days = Math.floor(hours / 24);
  if (days < 30) return t("Days ago", { count: days });
  return date.toLocaleDateString();
}
