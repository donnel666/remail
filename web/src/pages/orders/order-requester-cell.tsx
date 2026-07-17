import { Avatar } from "@douyinfe/semi-ui";
import type { TFunction } from "i18next";

import { CopyableTableText } from "@/components/semi/copyable-table-text";
import type { OrderOwnerSummary } from "@/lib/orders-api";

// Order-scoped owner cell. Intentionally a private copy of the ticket requester
// cell (not shared) so it can diverge later. Mirrors the admin owner cell:
// avatar + copyable email + name · role · group.

const USER_AVATAR_COLORS = [
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

function userAvatarColor(userId: number) {
  return USER_AVATAR_COLORS[Math.abs(userId) % USER_AVATAR_COLORS.length];
}

function userInitial(email?: string | null, name?: string | null) {
  const source = name?.trim() || email?.trim() || "?";
  return source[0]?.toUpperCase() ?? "?";
}

function userRoleLabel(role?: string | null) {
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

export function OrderOwnerCell({
  owner,
  userId,
  t,
}: {
  owner?: OrderOwnerSummary | null;
  userId: number;
  t: TFunction;
}) {
  // Falls back to the bare buyer ID when owner enrichment is unavailable.
  if (!owner) {
    return (
      <span className="text-[var(--semi-color-text-2)]">{`#${userId}`}</span>
    );
  }
  const displayName = owner.nickname?.trim() || `#${owner.userId}`;
  return (
    <div className="flex min-w-0 items-center gap-2.5">
      <Avatar
        className="shrink-0"
        color={userAvatarColor(owner.userId || 0)}
        size="extra-small"
      >
        {userInitial(owner.email, owner.nickname)}
      </Avatar>
      <div className="min-w-0">
        {owner.email ? (
          <CopyableTableText copiedText={t("Copied")} text={owner.email} />
        ) : (
          <div className="truncate text-sm text-[var(--semi-color-text-0)]">
            {displayName}
          </div>
        )}
        <div className="truncate text-xs text-[var(--semi-color-text-2)]">
          {displayName} · {t(userRoleLabel(owner.role))} · {owner.groupName || "-"}
        </div>
      </div>
    </div>
  );
}
