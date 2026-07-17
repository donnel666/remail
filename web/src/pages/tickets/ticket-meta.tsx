import { Tag } from "@douyinfe/semi-ui";

import type {
  TicketSender,
  TicketStatus,
  TicketType,
} from "./tickets-api";

type Translator = (key: string, options?: Record<string, unknown>) => string;

const MINUTE = 60_000;
const HOUR = 3_600_000;
const DAY = 86_400_000;

type TagColor =
  | "amber"
  | "blue"
  | "cyan"
  | "green"
  | "grey"
  | "indigo"
  | "light-blue"
  | "orange"
  | "pink"
  | "purple"
  | "red"
  | "teal"
  | "violet"
  | "white";

export const TICKET_STATUS_VALUES: TicketStatus[] = [
  "open",
  "processing",
  "closed",
];

const STATUS_META: Record<
  TicketStatus,
  { color: TagColor; labelKey: string }
> = {
  open: { color: "amber", labelKey: "Ticket status open" },
  processing: { color: "blue", labelKey: "Ticket status processing" },
  closed: { color: "grey", labelKey: "Ticket status closed" },
};

export function ticketStatusLabel(status: TicketStatus, t: Translator) {
  return t(STATUS_META[status].labelKey);
}

export function renderTicketStatusTag(status: TicketStatus, t: Translator) {
  const meta = STATUS_META[status];
  return (
    <Tag color={meta.color} shape="circle">
      {t(meta.labelKey)}
    </Tag>
  );
}

export function ticketTypeLabel(type: TicketType, t: Translator) {
  return type === "order" ? t("Order ticket") : t("General ticket");
}

export function renderTicketTypeTag(type: TicketType, t: Translator) {
  return (
    <Tag color={type === "order" ? "orange" : "teal"} shape="circle">
      {ticketTypeLabel(type, t)}
    </Tag>
  );
}

const SENDER_META: Record<
  TicketSender,
  { color: TagColor; labelKey: string }
> = {
  user: { color: "orange", labelKey: "Ticket sender user" },
  platform: { color: "blue", labelKey: "Platform support" },
  system: { color: "grey", labelKey: "System role" },
};

export function senderLabel(sender: TicketSender, t: Translator) {
  return t(SENDER_META[sender].labelKey);
}

export function renderSenderTag(sender: TicketSender, t: Translator) {
  const meta = SENDER_META[sender];
  return (
    <Tag color={meta.color} shape="circle" size="small">
      {t(meta.labelKey)}
    </Tag>
  );
}

export function formatTicketAmount(value: number) {
  if (!Number.isFinite(value)) return "￥0";
  const text = value.toFixed(6).replace(/\.?0+$/, "");
  return `￥${text || "0"}`;
}

export function formatTicketDateTime(value?: string) {
  if (!value) return "-";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "-";
  const pad = (input: number) => String(input).padStart(2, "0");
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(
    date.getDate()
  )} ${pad(date.getHours())}:${pad(date.getMinutes())}`;
}

export function formatRelativeTime(value: string, t: Translator) {
  const time = new Date(value).getTime();
  if (!Number.isFinite(time)) return "-";
  const diffMs = Date.now() - time;
  if (diffMs < MINUTE) return t("Just now");
  if (diffMs < HOUR) {
    return t("Minutes ago", { count: Math.floor(diffMs / MINUTE) });
  }
  if (diffMs < DAY) {
    return t("Hours ago", { count: Math.floor(diffMs / HOUR) });
  }
  if (diffMs < 7 * DAY) {
    return t("Days ago", { count: Math.floor(diffMs / DAY) });
  }
  return formatTicketDateTime(value);
}
