import { Avatar } from "@douyinfe/semi-ui";
import type { TFunction } from "i18next";

import { OverflowTooltip } from "@/components/semi/overflow-tooltip";
import { ProjectIcon } from "@/pages/workbench/project-icon";

import {
  formatRelativeTime,
  renderTicketStatusTag,
  renderTicketTypeTag,
  senderLabel,
} from "./ticket-meta";
import type { Ticket } from "./tickets-api";

const AVATAR_COLORS = [
  "amber",
  "blue",
  "cyan",
  "green",
  "indigo",
  "light-blue",
  "orange",
  "pink",
  "purple",
  "red",
  "teal",
  "violet",
] as const;

function avatarColor(userId: number) {
  return AVATAR_COLORS[Math.abs(userId) % AVATAR_COLORS.length];
}

function avatarInitial(name: string, email: string) {
  const source = name.trim() || email.trim() || "?";
  return source[0]?.toUpperCase() ?? "?";
}

function lastMessagePreview(ticket: Ticket, t: TFunction) {
  const last = ticket.messages[ticket.messages.length - 1];
  if (!last) return ticket.title;
  const text = last.content.replace(/\s+/g, " ").trim();
  if (last.senderType === "user") return text;
  return `${senderLabel(last.senderType, t)}: ${text}`;
}

// FreeScout-style inbox row shared by the user and admin ticket pages. It is a
// full-width clickable row; the parent passes onClick to open the drawer.
export function TicketInboxRow({
  ticket,
  showRequester,
  viewerRole,
  t,
  onClick,
}: {
  ticket: Ticket;
  showRequester: boolean;
  viewerRole: "user" | "platform";
  t: TFunction;
  onClick?: () => void;
}) {
  const unread =
    (viewerRole === "user"
      ? ticket.requesterUnreadCount
      : ticket.platformUnreadCount) > 0;
  const preview = lastMessagePreview(ticket, t);

  return (
    <div
      className="ticket-inbox-row"
      onClick={onClick}
      onKeyDown={(event) => {
        if (event.key === "Enter" || event.key === " ") {
          event.preventDefault();
          onClick?.();
        }
      }}
      role="button"
      tabIndex={0}
    >
      <Avatar
        className="shrink-0"
        color={avatarColor(ticket.requesterUserId)}
        size="small"
      >
        {avatarInitial(ticket.requesterName, ticket.requesterEmail)}
      </Avatar>

      <div className="min-w-0 flex-1">
        <div className="flex min-w-0 items-center gap-2">
          <OverflowTooltip
            className={`min-w-0 max-w-full text-[14px] ${
              unread
                ? "font-semibold text-[var(--semi-color-text-0)]"
                : "font-medium text-[var(--semi-color-text-0)]"
            }`}
            content={ticket.title}
          >
            {ticket.title}
          </OverflowTooltip>
          {renderTicketTypeTag(ticket.ticketType, t)}
        </div>

        <OverflowTooltip
          className="mt-0.5 block max-w-full text-[13px] text-[var(--semi-color-text-2)]"
          content={preview}
        >
          {preview}
        </OverflowTooltip>

        <div className="mt-1 flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1 text-xs text-[var(--semi-color-text-3)]">
          <span className="font-mono-data">{ticket.ticketNo}</span>
          {ticket.order ? (
            <span className="flex min-w-0 items-center gap-1">
              <ProjectIcon
                logoUrl={ticket.order.projectLogoUrl}
                name={ticket.order.projectName}
                size={16}
              />
              <span className="text-[var(--semi-color-text-2)]">
                {ticket.order.projectName}
              </span>
            </span>
          ) : null}
          {showRequester ? (
            <OverflowTooltip
              className="font-mono-data max-w-[180px] text-[var(--semi-color-text-2)]"
              content={ticket.requesterEmail}
            >
              {ticket.requesterEmail}
            </OverflowTooltip>
          ) : null}
        </div>
      </div>

      <div className="flex shrink-0 flex-col items-end gap-1.5">
        <span className="flex items-center gap-2">
          {unread ? <span className="ticket-inbox-unread-dot" /> : null}
          {renderTicketStatusTag(ticket.status, t)}
        </span>
        <span className="text-xs text-[var(--semi-color-text-3)]">
          {formatRelativeTime(ticket.updatedAt, t)}
        </span>
      </div>
    </div>
  );
}
