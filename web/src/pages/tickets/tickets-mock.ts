// Mock ticket data for the After-sales Tickets page UI review.
//
// Simplified business model: suppliers fully delegate resources to the
// platform, so every ticket is a conversation between a user (submitter) and
// the platform (admin / support). There is no supplier, SLA, auto-check,
// escalation or problem-code taxonomy any more. Two ticket types exist:
// "order" (linked to an order) and "general".
//
// The async API surface mirrors a future /v1/tickets backend so swapping to
// the real service later only needs to replace these imports with a
// tickets-api module.

import type {
  TicketOrderRef,
  TicketOrderSource as HandoffTicketOrderSource,
} from "../orders/ticket-order-handoff";

export type MockTicketSender = "user" | "platform" | "system";

export type MockTicketStatus = "open" | "processing" | "closed";

export type MockTicketType = "order" | "general";

export type MockTicketResolutionKind = "refunded" | "closed";

export interface MockTicketMessage {
  id: string;
  senderType: MockTicketSender;
  senderName: string;
  senderUserId?: number;
  senderEmail?: string;
  content: string;
  createdAt: string;
  attachments?: string[];
}

// Keep the ticket mock on the same handoff contract used by the Orders page.
export type MockTicketOrderRef = TicketOrderRef;

export interface MockTicketResolution {
  kind: MockTicketResolutionKind;
  note?: string;
  refundAmount?: number;
}

export interface MockTicket {
  id: number;
  ticketNo: string;
  ticketType: MockTicketType;
  title: string;
  status: MockTicketStatus;
  order?: MockTicketOrderRef;
  requesterUserId: number;
  requesterEmail: string;
  requesterName: string;
  requesterRole: string;
  requesterGroupName: string;
  resolution?: MockTicketResolution;
  requesterUnreadCount: number;
  platformUnreadCount: number;
  messages: MockTicketMessage[];
  createdAt: string;
  updatedAt: string;
}

export interface MockTicketListFilter {
  search?: string;
  ticketType?: MockTicketType;
  status?: MockTicketStatus;
  createdFrom?: string;
  createdTo?: string;
}

export interface MockTicketFacets {
  ticketType: Record<"all" | MockTicketType, number>;
  status: Record<"all" | MockTicketStatus, number>;
}

export interface MockTicketListResponse {
  items: MockTicket[];
  total: number;
  facets: MockTicketFacets;
  nextAfterId?: number;
}

const MINUTE = 60_000;
const HOUR = 3_600_000;

// The seeded "current user" — listMyTickets returns only tickets whose
// requester matches this id.
const MY_USER_ID = 1001;
const MY_NAME = "我";
const MY_EMAIL = "me@example.com";

// Deterministic PRNG so seeded data and pagination blocks stay stable.
function mulberry32(seed: number) {
  let a = seed >>> 0;
  return () => {
    a = (a + 0x6d2b79f5) >>> 0;
    let t = a;
    t = Math.imul(t ^ (t >>> 15), t | 1);
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

const random = mulberry32(20260714);

function pick<T>(items: readonly T[]): T {
  return items[Math.floor(random() * items.length)];
}

function randomInt(min: number, max: number) {
  return min + Math.floor(random() * (max - min + 1));
}

function hex(length: number) {
  let value = "";
  for (let index = 0; index < length; index += 1) {
    value += "0123456789ABCDEF"[Math.floor(random() * 16)];
  }
  return value;
}

function toTicketNo(createdAtMs: number) {
  const timePart = Math.floor(createdAtMs)
    .toString(16)
    .toUpperCase()
    .padStart(12, "0");
  return `AS${timePart}${hex(8)}`;
}

const USER_NAMES = [
  "林晓",
  "陈默",
  "赵星河",
  "周乐",
  "孙悦",
  "吴桐",
  "郑一凡",
  "王思远",
];
const PLATFORM_NAMES = ["客服小雨", "客服阿哲", "客服知夏"];
const PROJECT_POOL = [
  { name: "Telegram", codePrice: 0.98, purchasePrice: 6.8 },
  { name: "Discord", codePrice: 1.2, purchasePrice: 9.9 },
  { name: "TikTok", codePrice: 1.5, purchasePrice: 12.5 },
  { name: "Instagram", codePrice: 0.888888, purchasePrice: 8.8 },
  { name: "OpenAI", codePrice: 2.2, purchasePrice: 19.9 },
  { name: "Steam", codePrice: 0.65, purchasePrice: 15 },
];
const DOMAINS = ["outlook.com", "hotmail.com", "starmail.top", "mailhub.vip"];
const LOCAL_HEADS = [
  "nova", "kite", "lumen", "orbit", "pine", "quartz", "raven", "sable",
  "tidal", "umber", "vesper", "willow", "zephyr", "cobalt", "ember", "flint",
];
const LOCAL_TAILS = [
  "fox", "lark", "moss", "reed", "wren", "dove", "hart", "vale",
  "byte", "node", "mint", "peak", "rush", "sage", "wave", "glow",
];

const ORDER_TITLES = [
  "验证码一直没有收到",
  "账号无法登录，提示凭证错误",
  "邮箱提示已被停用",
  "收到邮件但平台没识别验证码",
  "服务未成功，申请退款",
  "订单状态异常，请人工核实",
];
const ORDER_OPENERS = [
  "下单后等了二十多分钟，一直没有收到验证码邮件，麻烦帮忙看看。",
  "购买的邮箱账号登录时提示凭证错误，换了两台设备都不行，请协助处理。",
  "登录后提示邮箱已被停用，无法继续使用，申请核实或更换。",
  "邮箱里已经能看到验证码邮件，但平台没有识别出来，取不到码。",
  "验证码始终没有收到，服务没有使用成功，申请按订单金额退款。",
  "订单信息显示异常，麻烦人工核实一下具体状态，谢谢。",
];
const GENERAL_TITLES = [
  "充值已扣款但余额未到账",
  "咨询长效邮箱是否支持重复接码",
  "账单有一笔扣款对不上",
  "想更换账号绑定邮箱",
  "邀请返佣结算周期咨询",
];
const GENERAL_OPENERS = [
  "支付宝充值 50 元已扣款，但钱包余额没有变化，麻烦帮忙核实一下。",
  "想确认长效邮箱是否支持重复接码，以及库存的补充频率大概是多久？",
  "账单里有一笔扣款对不上订单记录，麻烦帮忙查一下是哪一笔。",
  "账号想更换绑定邮箱，请问需要怎么操作，有没有什么限制？",
  "邀请返佣的金额一直是待结算状态，想了解具体的结算周期。",
];
const PLATFORM_REPLIES = [
  "您好，我是平台客服，已经收到您的工单，正在为您核实，请稍等。",
  "已经在帮您排查了，方便补充一下下单后大概等待了多久吗？",
  "正在为您重新处理，稍后同步进展，请留意消息通知。",
  "已联系资源方确认情况，请耐心等待，我们会尽快回复。",
];
const USER_FOLLOWUPS = [
  "好的，麻烦加急处理，账号今天还要用。",
  "收到，需要补充材料的话随时说。",
  "谢谢，那我先等一下。",
];

function buildOrderRef(now: number): MockTicketOrderRef {
  const project = pick(PROJECT_POOL);
  const serviceMode = random() < 0.6 ? ("code" as const) : ("purchase" as const);
  const domain = pick(DOMAINS);
  const createdAt = now - randomInt(1, 20) * HOUR;
  return {
    orderNo: `OR${Math.floor(createdAt).toString(16).toUpperCase().padStart(12, "0")}${hex(20)}`,
    projectName: project.name,
    deliveryEmail: `${pick(LOCAL_HEADS)}${pick(LOCAL_TAILS)}${randomInt(10, 99)}@${domain}`,
    payAmount: serviceMode === "code" ? project.codePrice : project.purchasePrice,
    serviceMode,
    afterSaleUntil: new Date(createdAt + randomInt(20, 72) * HOUR).toISOString(),
    hasSupplier: false,
  };
}

let seq = 1;
function nextId(prefix: string) {
  seq += 1;
  return `${prefix}-${seq}`;
}

function pushMessage(
  ticket: MockTicket,
  senderType: MockTicketSender,
  senderName: string,
  content: string,
  atMs: number,
  attachments?: string[],
  sender?: { userId?: number; email?: string }
) {
  ticket.messages.push({
    id: nextId("msg"),
    senderType,
    senderName,
    senderUserId: sender?.userId,
    senderEmail: sender?.email,
    content,
    createdAt: new Date(atMs).toISOString(),
    attachments,
  });
}

function touch(ticket: MockTicket, atMs: number) {
  if (new Date(ticket.updatedAt).getTime() < atMs) {
    ticket.updatedAt = new Date(atMs).toISOString();
  }
}

function formatAmount(value: number) {
  if (!Number.isFinite(value)) return "¥0";
  const text = value.toFixed(6).replace(/\.?0+$/, "");
  return `¥${text || "0"}`;
}

interface SeedSpec {
  mine: boolean;
  status: MockTicketStatus;
  count: number;
}

const SEED_SPECS: SeedSpec[] = [
  { mine: true, status: "open", count: 3 },
  { mine: true, status: "processing", count: 3 },
  { mine: true, status: "closed", count: 6 },
  { mine: false, status: "open", count: 8 },
  { mine: false, status: "processing", count: 8 },
  { mine: false, status: "closed", count: 12 },
];

function buildTicket(id: number, spec: SeedSpec, now: number): MockTicket {
  const ticketType: MockTicketType = random() < 0.7 ? "order" : "general";
  const isOpen = spec.status === "open";
  const createdAt = isOpen
    ? now - randomInt(10, 20 * 60) * MINUTE
    : now - randomInt(24 * 60, 30 * 24 * 60) * MINUTE;

  const order = ticketType === "order" ? buildOrderRef(createdAt) : undefined;
  const title = ticketType === "order" ? pick(ORDER_TITLES) : pick(GENERAL_TITLES);
  const opener =
    ticketType === "order" ? pick(ORDER_OPENERS) : pick(GENERAL_OPENERS);

  const requesterUserId = spec.mine ? MY_USER_ID : 100 + id;
  const platformName = pick(PLATFORM_NAMES);

  const ticket: MockTicket = {
    id,
    ticketNo: toTicketNo(createdAt),
    ticketType,
    title,
    status: spec.status,
    order,
    requesterUserId,
    requesterEmail: spec.mine ? MY_EMAIL : `user${requesterUserId}@example.com`,
    requesterName: spec.mine ? MY_NAME : pick(USER_NAMES),
    requesterRole: "user",
    requesterGroupName: "普通用户",
    requesterUnreadCount: 0,
    platformUnreadCount: 0,
    messages: [],
    createdAt: new Date(createdAt).toISOString(),
    updatedAt: new Date(createdAt).toISOString(),
  };

  let cursor = createdAt;
  pushMessage(ticket, "user", ticket.requesterName, opener, cursor, undefined, {
    userId: ticket.requesterUserId,
    email: ticket.requesterEmail,
  });

  if (spec.status === "processing" || spec.status === "closed") {
    cursor += randomInt(4, 40) * MINUTE;
    pushMessage(ticket, "platform", platformName, pick(PLATFORM_REPLIES), cursor);
    if (random() < 0.5) {
      cursor += randomInt(3, 30) * MINUTE;
      pushMessage(
        ticket,
        "user",
        ticket.requesterName,
        pick(USER_FOLLOWUPS),
        cursor,
        undefined,
        { userId: ticket.requesterUserId, email: ticket.requesterEmail }
      );
    }
  }

  if (spec.status === "closed") {
    cursor += randomInt(10, 180) * MINUTE;
    const refunded = ticketType === "order" && order && random() < 0.5;
    if (refunded && order) {
      ticket.resolution = {
        kind: "refunded",
        refundAmount: order.payAmount,
      };
      pushMessage(
        ticket,
        "system",
        "系统",
        `平台已退款 ${formatAmount(order.payAmount)} 并关闭工单。`,
        cursor
      );
    } else {
      ticket.resolution = { kind: "closed" };
      pushMessage(ticket, "system", "系统", "平台已关闭该工单。", cursor);
    }
  }

  touch(ticket, cursor);

  // The side opposite the latest participant message has one unread update.
  // Closed seed records stay read so old system messages do not light up both
  // inboxes at once.
  if (spec.status !== "closed") {
    const lastParticipantMessage = [...ticket.messages]
      .reverse()
      .find((message) => message.senderType !== "system");
    if (lastParticipantMessage?.senderType === "user") {
      ticket.platformUnreadCount = 1;
    } else if (lastParticipantMessage?.senderType === "platform") {
      ticket.requesterUnreadCount = 1;
    }
  }

  return ticket;
}

function buildDataset() {
  const now = Date.now();
  const tickets: MockTicket[] = [];
  let id = 1;
  for (const spec of SEED_SPECS) {
    for (let index = 0; index < spec.count; index += 1) {
      tickets.push(buildTicket(id, spec, now));
      id += 1;
    }
  }
  tickets.sort(
    (left, right) =>
      new Date(right.updatedAt).getTime() - new Date(left.updatedAt).getTime()
  );
  // Re-assign ids so that a higher id means a more recent ticket; the block
  // pager relies on this ordering for its afterId cursor.
  tickets.forEach((ticket, index) => {
    ticket.id = tickets.length - index;
  });
  return tickets;
}

const dataset = buildDataset();

function matchesSearchDate(ticket: MockTicket, filter: MockTicketListFilter) {
  if (filter.createdFrom) {
    if (new Date(ticket.createdAt) < new Date(filter.createdFrom)) return false;
  }
  if (filter.createdTo) {
    if (new Date(ticket.createdAt) > new Date(filter.createdTo)) return false;
  }
  const search = filter.search?.trim().toLowerCase();
  if (search) {
    const haystack = [
      ticket.ticketNo,
      ticket.title,
      ticket.requesterName,
      ticket.requesterEmail,
      ticket.order?.orderNo ?? "",
      ticket.order?.deliveryEmail ?? "",
      ticket.order?.projectName ?? "",
    ]
      .join(" ")
      .toLowerCase();
    if (!haystack.includes(search)) return false;
  }
  return true;
}

function buildFacets(tickets: MockTicket[]): MockTicketFacets {
  const facets: MockTicketFacets = {
    ticketType: { all: tickets.length, order: 0, general: 0 },
    status: { all: tickets.length, open: 0, processing: 0, closed: 0 },
  };
  for (const ticket of tickets) {
    facets.ticketType[ticket.ticketType] += 1;
    facets.status[ticket.status] += 1;
  }
  return facets;
}

function simulateLatency(base = 220) {
  return new Promise((resolve) =>
    globalThis.setTimeout(resolve, base + Math.random() * 200)
  );
}

function cloneTicket(ticket: MockTicket): MockTicket {
  return {
    ...ticket,
    order: ticket.order ? { ...ticket.order } : undefined,
    resolution: ticket.resolution ? { ...ticket.resolution } : undefined,
    messages: ticket.messages.map((message) => ({ ...message })),
  };
}

function sortByUpdatedDesc(tickets: MockTicket[]) {
  return [...tickets].sort(
    (left, right) =>
      new Date(right.updatedAt).getTime() - new Date(left.updatedAt).getTime()
  );
}

// Facets are computed over the search/date-filtered pool but BEFORE the type
// and status filters are applied, so the type tabs and the status options keep
// stable counts as the user switches between them.
function paginate(
  pool: MockTicket[],
  filter: MockTicketListFilter,
  offset: number,
  limit: number,
  afterId?: number
): MockTicketListResponse {
  const base = sortByUpdatedDesc(pool.filter((ticket) => matchesSearchDate(ticket, filter)));
  const facets = buildFacets(base);
  const filtered = base.filter((ticket) => {
    if (filter.ticketType && ticket.ticketType !== filter.ticketType) return false;
    if (filter.status && ticket.status !== filter.status) return false;
    return true;
  });
  const startIndex = afterId
    ? filtered.findIndex((ticket) => ticket.id < afterId)
    : offset;
  const safeStart = startIndex < 0 ? filtered.length : startIndex;
  const items = filtered.slice(safeStart, safeStart + limit).map(cloneTicket);
  const lastItem = items[items.length - 1];
  return {
    items,
    total: filtered.length,
    facets,
    nextAfterId:
      lastItem && safeStart + items.length < filtered.length
        ? lastItem.id
        : undefined,
  };
}

function findTicket(ticketNo: string) {
  const ticket = dataset.find((item) => item.ticketNo === ticketNo);
  if (!ticket) throw new Error("Ticket not found.");
  return ticket;
}

// ---------------------------------------------------------------------------
// User-facing API (submitter perspective)
// ---------------------------------------------------------------------------

export async function listMyTickets(
  filter: MockTicketListFilter,
  offset: number,
  limit: number,
  afterId?: number
): Promise<MockTicketListResponse> {
  await simulateLatency();
  const mine = dataset.filter((ticket) => ticket.requesterUserId === MY_USER_ID);
  return paginate(mine, filter, offset, limit, afterId);
}

export async function getTicket(ticketNo: string): Promise<MockTicket> {
  await simulateLatency(120);
  return cloneTicket(findTicket(ticketNo));
}

export interface CreateMockTicketInput {
  ticketType: MockTicketType;
  title: string;
  firstMessage: string;
  order?: MockTicketOrderRef;
  attachments?: string[];
}

export async function createTicket(
  input: CreateMockTicketInput
): Promise<MockTicket> {
  await simulateLatency();
  const now = Date.now();
  const ticket: MockTicket = {
    id: dataset.length + 1,
    ticketNo: toTicketNo(now),
    ticketType: input.ticketType,
    title: input.title,
    status: "open",
    order: input.order ? { ...input.order } : undefined,
    requesterUserId: MY_USER_ID,
    requesterEmail: MY_EMAIL,
    requesterName: MY_NAME,
    requesterRole: "user",
    requesterGroupName: "普通用户",
    requesterUnreadCount: 0,
    platformUnreadCount: 1,
    messages: [],
    createdAt: new Date(now).toISOString(),
    updatedAt: new Date(now).toISOString(),
  };
  pushMessage(ticket, "user", MY_NAME, input.firstMessage, now, input.attachments, {
    userId: MY_USER_ID,
    email: MY_EMAIL,
  });
  dataset.unshift(ticket);
  return cloneTicket(ticket);
}

export async function markTicketRead(
  ticketNo: string,
  viewerRole: "user" | "platform"
): Promise<void> {
  const ticket = dataset.find((item) => item.ticketNo === ticketNo);
  if (!ticket) return;
  if (viewerRole === "user") ticket.requesterUnreadCount = 0;
  else ticket.platformUnreadCount = 0;
}

// ---------------------------------------------------------------------------
// Admin / platform-facing API
// ---------------------------------------------------------------------------

export async function listAllTickets(
  filter: MockTicketListFilter,
  offset: number,
  limit: number,
  afterId?: number
): Promise<MockTicketListResponse> {
  await simulateLatency();
  return paginate(dataset, filter, offset, limit, afterId);
}

// Shared reply handler for both sides. A platform reply on an "open" ticket
// moves it to "processing"; a user reply never changes the status.
export async function replyTicket(
  ticketNo: string,
  content: string,
  senderType: "user" | "platform",
  attachments?: string[],
  sender?: { userId?: number; name?: string; email?: string }
): Promise<MockTicket> {
  await simulateLatency(150);
  const ticket = findTicket(ticketNo);
  const now = Date.now();
  const senderName =
    sender?.name?.trim() ||
    (senderType === "user" ? ticket.requesterName : pick(PLATFORM_NAMES));
  pushMessage(ticket, senderType, senderName, content, now, attachments, sender);
  if (senderType === "user") {
    ticket.requesterUnreadCount = 0;
    ticket.platformUnreadCount += 1;
  } else {
    ticket.platformUnreadCount = 0;
    ticket.requesterUnreadCount += 1;
  }
  if (senderType === "platform" && ticket.status === "open") {
    ticket.status = "processing";
  }
  touch(ticket, now);
  return cloneTicket(ticket);
}

export async function closeTicket(
  ticketNo: string,
  by: "user" | "platform" = "platform"
): Promise<MockTicket> {
  await simulateLatency(150);
  const ticket = findTicket(ticketNo);
  const now = Date.now();
  ticket.status = "closed";
  ticket.resolution = { kind: "closed" };
  pushMessage(
    ticket,
    "system",
    "系统",
    by === "user" ? "用户已主动关闭该工单。" : "平台已关闭该工单。",
    now
  );
  if (by === "user") {
    ticket.requesterUnreadCount = 0;
    ticket.platformUnreadCount += 1;
  } else {
    ticket.platformUnreadCount = 0;
    ticket.requesterUnreadCount += 1;
  }
  touch(ticket, now);
  return cloneTicket(ticket);
}

export async function refundAndCloseTicket(
  ticketNo: string,
  amount?: number
): Promise<MockTicket> {
  await simulateLatency(150);
  const ticket = findTicket(ticketNo);
  const now = Date.now();
  const refundAmount = amount ?? ticket.order?.payAmount ?? 0;
  ticket.status = "closed";
  ticket.resolution = { kind: "refunded", refundAmount };
  pushMessage(
    ticket,
    "system",
    "系统",
    `平台已退款 ${formatAmount(refundAmount)} 并关闭工单。`,
    now
  );
  ticket.platformUnreadCount = 0;
  ticket.requesterUnreadCount += 1;
  touch(ticket, now);
  return cloneTicket(ticket);
}

// ---------------------------------------------------------------------------
// Order → ticket handoff helpers (consumed by the create flow)
// ---------------------------------------------------------------------------

// Minimal structural order shape shared by the real OrderResponse and any mock
// order, so ticket creation can consume either source.
export interface TicketOrderSource extends HandoffTicketOrderSource {
  status: string;
}

// Eligibility of an order for creating an after-sale ticket.
export type OrderAfterSaleState =
  | { eligible: true; deadline?: string }
  | { eligible: false; reason: "expired" | "not_delivered" | "refunded" };

export function getOrderAfterSaleState(
  order: TicketOrderSource
): OrderAfterSaleState {
  if (order.status === "refunded") return { eligible: false, reason: "refunded" };
  if (["pending_payment", "paid", "failed"].includes(order.status)) {
    return { eligible: false, reason: "not_delivered" };
  }
  const deadline = order.afterSaleUntil ?? order.receiveUntil ?? undefined;
  if (order.status === "active") {
    return { eligible: true, deadline };
  }
  if (deadline && new Date(deadline).getTime() > Date.now()) {
    return { eligible: true, deadline };
  }
  return { eligible: false, reason: "expired" };
}
