import type { ReactNode } from "react";
import { Avatar, Tag, Tooltip } from "@douyinfe/semi-ui";
import type { TFunction } from "i18next";

import { CopyableTableText } from "@/components/semi/copyable-table-text";

import type {
  AdminDomainItem,
  AdminDomainPurpose,
  AdminDomainStatus,
} from "./admin-domains-api";

const STATUS_TAG: Record<
  AdminDomainStatus,
  { color: "green" | "blue" | "orange" | "grey" | "red"; label: string }
> = {
  pending: { color: "blue", label: "Pending" },
  validating: { color: "orange", label: "Validating" },
  normal: { color: "green", label: "Normal" },
  abnormal: { color: "orange", label: "Abnormal" },
  disabled: { color: "grey", label: "Disabled" },
  deleted: { color: "red", label: "Deleted" },
};

const PURPOSE_TAG: Record<
  AdminDomainPurpose,
  { color: "blue" | "green" | "grey"; label: string }
> = {
  not_sale: { color: "grey", label: "Not for sale" },
  sale: { color: "green", label: "Sale" },
  binding: { color: "blue", label: "Binding" },
};

export function renderDomainStatusTag(
  status: AdminDomainStatus,
  t: TFunction,
  safeError?: string
) {
  const meta = STATUS_TAG[status];
  const tag = (
    <Tag color={meta.color} shape="circle" size="small">
      {t(meta.label)}
    </Tag>
  );
  return safeError ? <Tooltip content={safeError}>{tag}</Tooltip> : tag;
}

export function renderDomainPurposeTag(purpose: AdminDomainPurpose, t: TFunction) {
  const meta = PURPOSE_TAG[purpose];
  return (
    <Tag color={meta.color} shape="circle" size="small">
      {t(meta.label)}
    </Tag>
  );
}

export function formatDomainTime(value?: string | null) {
  if (!value) return "-";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "-";
  return date.toLocaleString();
}

function ownerLabel(owner: Pick<AdminDomainItem, "ownerEmail" | "ownerNickname" | "ownerId">) {
  if (owner.ownerEmail) return owner.ownerEmail;
  if (owner.ownerNickname) return owner.ownerNickname;
  return `#${owner.ownerId}`;
}

export function domainOwnerRoleLabel(role: AdminDomainItem["ownerRole"]) {
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

function ownerAvatarColor(ownerId: number) {
  const colors = [
    "amber",
    "blue",
    "cyan",
    "green",
    "indigo",
    "orange",
    "pink",
    "purple",
  ] as const;
  return colors[Math.abs(ownerId) % colors.length];
}

export function DomainOwnerIdentity({
  owner,
  t,
}: {
  owner: AdminDomainItem;
  t: TFunction;
}) {
  const source = owner.ownerNickname?.trim() || owner.ownerEmail;
  return (
    <div className="flex min-w-0 items-center gap-2.5">
      <Avatar
        className="shrink-0"
        color={ownerAvatarColor(owner.ownerId)}
        size="extra-small"
      >
        {(source[0] ?? "?").toUpperCase()}
      </Avatar>
      <div className="min-w-0">
        <CopyableTableText copiedText={t("Copied")} text={ownerLabel(owner)} />
        <div className="truncate text-xs text-[var(--semi-color-text-2)]">
          {owner.ownerNickname || "-"} · {t(domainOwnerRoleLabel(owner.ownerRole))}
        </div>
      </div>
    </div>
  );
}

export function DomainInfoItem({
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
