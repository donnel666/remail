import { Avatar } from "@douyinfe/semi-ui";
import type { TFunction } from "i18next";

import { CopyableTableText } from "@/components/semi/copyable-table-text";
import type { FinanceUserRole } from "./admin-finance-mock";

// Invite-scoped identity helpers. Intentionally a private copy (not shared with
// card keys) so the invite owner cell can diverge independently later.

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

function ownerInitial(email?: string | null, nickname?: string | null) {
  const source = nickname?.trim() || email?.trim() || "?";
  return source[0]?.toUpperCase() ?? "?";
}

// Copied from admin-microsoft's ownerRoleLabel so the owner cell reads identically.
function ownerRoleLabel(role?: FinanceUserRole | null) {
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

// Account cell (avatar + copyable email + nickname · role · group), used by the
// invite owner column, the invite detail owner field and the usage records
// User column.
export function InviteAccountCell({
  userId,
  email,
  nickname,
  role,
  groupName,
  t,
}: {
  userId?: number | null;
  email?: string | null;
  nickname?: string | null;
  role?: FinanceUserRole | null;
  groupName?: string | null;
  t: TFunction;
}) {
  if (!userId || !email) {
    return <span className="text-sm text-[var(--semi-color-text-2)]">-</span>;
  }

  return (
    <div className="flex min-w-0 items-center gap-2.5">
      <Avatar
        className="shrink-0"
        color={ownerAvatarColor(userId)}
        size="extra-small"
      >
        {ownerInitial(email, nickname)}
      </Avatar>
      <div className="min-w-0">
        <CopyableTableText copiedText={t("Copied")} text={email} />
        <div className="truncate text-xs text-[var(--semi-color-text-2)]">
          {nickname || "-"} · {t(ownerRoleLabel(role))} · {groupName || "-"}
        </div>
      </div>
    </div>
  );
}
