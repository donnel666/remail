import { Avatar } from "@douyinfe/semi-ui";
import type { TFunction } from "i18next";

import { CopyableTableText } from "@/components/semi/copyable-table-text";

// Ticket-scoped requester cell. Intentionally a private copy (not shared with
// invites / card keys / transactions / balances) so it can diverge later.

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

function userInitial(self: boolean, email?: string | null, name?: string | null) {
  if (self) return "我";
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

// Mirrors the admin owner cell (avatar + copyable email + name · role · group).
// When the requester is the current account, the name shows "我".
export function TicketRequesterCell({
  self,
  userId,
  email,
  name,
  role,
  groupName,
  t,
}: {
  self: boolean;
  userId?: number | null;
  email?: string | null;
  name?: string | null;
  role?: string | null;
  groupName?: string | null;
  t: TFunction;
}) {
  const displayName = self ? "我" : name || "-";
  return (
    <div className="flex min-w-0 items-center gap-2.5">
      <Avatar
        className="shrink-0"
        color={userAvatarColor(userId || 0)}
        size="extra-small"
      >
        {userInitial(self, email, name)}
      </Avatar>
      <div className="min-w-0">
        {email ? (
          <CopyableTableText copiedText={t("Copied")} text={email} />
        ) : (
          <div className="truncate text-sm text-[var(--semi-color-text-0)]">
            {displayName}
          </div>
        )}
        <div className="truncate text-xs text-[var(--semi-color-text-2)]">
          {displayName} · {t(userRoleLabel(role))} · {groupName || "-"}
        </div>
      </div>
    </div>
  );
}
