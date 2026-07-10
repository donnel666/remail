// Mock order data for the Order Records page UI review.
// The API surface mirrors GET /v1/orders (list + facets) so swapping to the
// real backend later only needs to replace these imports with orders-api.

import type { WorkbenchMessage } from "@/pages/workbench/types";

export type MockOrderStatus =
  | "pending_payment"
  | "paid"
  | "active"
  | "completed"
  | "refunded"
  | "failed"
  | "closed";
export type MockServiceMode = "code" | "purchase";
export type MockProductType = "microsoft" | "domain";

export interface MockOrder {
  id: number;
  orderNo: string;
  projectName: string;
  productType: MockProductType;
  serviceMode: MockServiceMode;
  supplyPolicy: "private_first" | "public_only";
  status: MockOrderStatus;
  payAmount: number;
  refundAmount: number;
  deliveryEmail: string;
  hasDelivery: boolean;
  verificationCode?: string;
  serviceToken?: string;
  clientChannel: "console" | "api_key";
  receiveStartedAt?: string;
  receiveUntil?: string;
  activatedAt?: string;
  afterSaleUntil?: string;
  lastMailReceivedAt?: string;
  archivedAt?: string;
  createdAt: string;
}

export interface MockOrderListFilter {
  domain?: string;
  search?: string;
  status?: MockOrderStatus;
  serviceMode?: MockServiceMode;
  createdFrom?: string;
  createdTo?: string;
}

export interface MockOrderFacets {
  domains: { key: string; count: number }[];
  status: Record<"all" | MockOrderStatus, number>;
  serviceMode: Record<"all" | MockServiceMode, number>;
}

export interface MockOrderListResponse {
  items: MockOrder[];
  nextAfterId?: number;
  total: number;
  facets: MockOrderFacets;
}

const MINUTE = 60_000;
const HOUR = 3_600_000;

// Deterministic PRNG so pagination blocks stay stable between calls.
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

const random = mulberry32(20260710);

function pick<T>(items: readonly T[]): T {
  return items[Math.floor(random() * items.length)];
}

function pickWeighted<T extends string>(weights: Record<T, number>): T {
  const entries = Object.entries(weights) as [T, number][];
  let roll = random() * entries.reduce((sum, [, weight]) => sum + weight, 0);
  for (const [value, weight] of entries) {
    roll -= weight;
    if (roll <= 0) return value;
  }
  return entries[entries.length - 1][0];
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

function toOrderNo(createdAtMs: number) {
  const timePart = Math.floor(createdAtMs).toString(16).toUpperCase().padStart(12, "0");
  return `OR${timePart}${hex(20)}`;
}

function toServiceToken() {
  let value = "";
  const alphabet =
    "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789";
  for (let index = 0; index < 28; index += 1) {
    value += alphabet[Math.floor(random() * alphabet.length)];
  }
  return `ot_${value}`;
}

function toLocalPart() {
  const heads = [
    "nova", "kite", "lumen", "orbit", "pine", "quartz", "raven", "sable",
    "tidal", "umber", "vesper", "willow", "zephyr", "cobalt", "ember", "flint",
  ];
  const tails = [
    "fox", "lark", "moss", "reed", "wren", "dove", "hart", "vale",
    "byte", "node", "mint", "peak", "rush", "sage", "wave", "glow",
  ];
  return `${pick(heads)}${pick(tails)}${randomInt(10, 99)}`;
}

function toVerificationCode() {
  return String(randomInt(100000, 999999));
}

const DOMAIN_POOL: { domain: string; productType: MockProductType; weight: number }[] = [
  { domain: "outlook.com", productType: "microsoft", weight: 0.38 },
  { domain: "hotmail.com", productType: "microsoft", weight: 0.27 },
  { domain: "starmail.top", productType: "domain", weight: 0.2 },
  { domain: "mailhub.vip", productType: "domain", weight: 0.15 },
];

const PROJECT_POOL = [
  { name: "Telegram", codePrice: 0.98, purchasePrice: 6.8 },
  { name: "Discord", codePrice: 1.2, purchasePrice: 9.9 },
  { name: "TikTok", codePrice: 1.5, purchasePrice: 12.5 },
  { name: "Instagram", codePrice: 0.888888, purchasePrice: 8.8 },
  { name: "OpenAI", codePrice: 2.2, purchasePrice: 19.9 },
  { name: "Steam", codePrice: 0.65, purchasePrice: 15 },
];

const STATUS_WEIGHTS: Record<MockOrderStatus, number> = {
  pending_payment: 0.03,
  paid: 0.04,
  active: 0.16,
  completed: 0.42,
  refunded: 0.14,
  failed: 0.08,
  closed: 0.13,
};

function buildOrder(id: number, now: number): MockOrder {
  const domainEntry = pickWeighted(
    Object.fromEntries(
      DOMAIN_POOL.map((entry) => [entry.domain, entry.weight])
    ) as Record<string, number>
  );
  const { productType } = DOMAIN_POOL.find(
    (entry) => entry.domain === domainEntry
  )!;
  const project = pick(PROJECT_POOL);
  const serviceMode: MockServiceMode = random() < 0.62 ? "code" : "purchase";
  const status = pickWeighted(STATUS_WEIGHTS);

  // Active orders must look in-window, so keep them recent.
  const createdAt =
    status === "active" || status === "pending_payment" || status === "paid"
      ? now - randomInt(2, 240) * MINUTE
      : now - randomInt(4 * 60, 30 * 24 * 60) * MINUTE;

  const supplyPolicy = random() < 0.25 ? "private_first" : "public_only";
  const hitPrivateInventory = supplyPolicy === "private_first" && random() < 0.5;
  const listPrice =
    serviceMode === "code" ? project.codePrice : project.purchasePrice;
  const payAmount =
    status === "pending_payment" ? 0 : hitPrivateInventory ? 0 : listPrice;

  const hasAllocation = !["pending_payment", "paid", "failed"].includes(status);
  const order: MockOrder = {
    id,
    orderNo: toOrderNo(createdAt),
    projectName: project.name,
    productType,
    serviceMode,
    supplyPolicy,
    status,
    payAmount,
    refundAmount: 0,
    deliveryEmail: `${toLocalPart()}@${domainEntry}`,
    hasDelivery: false,
    serviceToken: hasAllocation ? toServiceToken() : undefined,
    clientChannel: random() < 0.8 ? "console" : "api_key",
    createdAt: new Date(createdAt).toISOString(),
  };

  if (!hasAllocation) {
    if (status === "failed") order.refundAmount = payAmount;
    return order;
  }

  order.receiveStartedAt = order.createdAt;

  if (serviceMode === "code") {
    const windowMinutes = pick([15, 20, 30] as const);
    let receiveUntil = createdAt + windowMinutes * MINUTE;
    if (status === "completed" || status === "closed") {
      const matchedAt = createdAt + randomInt(1, windowMinutes - 2) * MINUTE;
      order.hasDelivery = true;
      order.verificationCode = toVerificationCode();
      order.lastMailReceivedAt = new Date(matchedAt).toISOString();
      receiveUntil = matchedAt + HOUR;
    }
    if (status === "refunded") {
      order.refundAmount = payAmount;
    }
    order.receiveUntil = new Date(receiveUntil).toISOString();
    return order;
  }

  // Purchase orders: activation window first, warranty after activation.
  const activationWindowMs = 24 * HOUR;
  const activated =
    status === "completed" || status === "closed"
      ? random() < 0.85
      : status === "active"
        ? random() < 0.6
        : false;
  if (activated) {
    const activatedAt = createdAt + randomInt(5, 180) * MINUTE;
    const warrantyMs = pick([24, 48, 72] as const) * HOUR;
    order.activatedAt = new Date(activatedAt).toISOString();
    order.afterSaleUntil = new Date(activatedAt + warrantyMs).toISOString();
    order.receiveUntil = order.afterSaleUntil;
    order.hasDelivery = true;
    order.verificationCode = toVerificationCode();
    order.lastMailReceivedAt = new Date(
      activatedAt + randomInt(1, 30) * MINUTE
    ).toISOString();
  } else {
    order.receiveUntil = new Date(createdAt + activationWindowMs).toISOString();
  }
  if (status === "refunded") order.refundAmount = payAmount;
  return order;
}

function buildDataset() {
  const now = Date.now();
  const orders: MockOrder[] = [];
  for (let id = 1; id <= 236; id += 1) {
    orders.push(buildOrder(id, now));
  }
  // Newest first with descending ids, mirroring the backend list ordering.
  orders.sort(
    (left, right) =>
      new Date(right.createdAt).getTime() - new Date(left.createdAt).getTime()
  );
  orders.forEach((order, index) => {
    order.id = orders.length - index;
  });
  return orders;
}

const dataset = buildDataset();

export function getOrderDomain(email: string) {
  const index = email.lastIndexOf("@");
  return index === -1 ? "" : email.slice(index).toLowerCase();
}

function matchesFilter(order: MockOrder, filter: MockOrderListFilter) {
  if (order.archivedAt) return false;
  if (filter.domain && getOrderDomain(order.deliveryEmail) !== filter.domain) {
    return false;
  }
  if (filter.status && order.status !== filter.status) return false;
  if (filter.serviceMode && order.serviceMode !== filter.serviceMode) {
    return false;
  }
  if (filter.createdFrom) {
    if (new Date(order.createdAt) < new Date(filter.createdFrom)) return false;
  }
  if (filter.createdTo) {
    if (new Date(order.createdAt) > new Date(filter.createdTo)) return false;
  }
  const search = filter.search?.trim().toLowerCase();
  if (search) {
    const haystack =
      `${order.orderNo} ${order.deliveryEmail} ${order.projectName}`.toLowerCase();
    if (!haystack.includes(search)) return false;
  }
  return true;
}

function buildFacets(orders: MockOrder[]): MockOrderFacets {
  const domainCounts = new Map<string, number>();
  const status: MockOrderFacets["status"] = {
    all: orders.length,
    pending_payment: 0,
    paid: 0,
    active: 0,
    completed: 0,
    refunded: 0,
    failed: 0,
    closed: 0,
  };
  const serviceMode: MockOrderFacets["serviceMode"] = {
    all: orders.length,
    code: 0,
    purchase: 0,
  };
  for (const order of orders) {
    const domain = getOrderDomain(order.deliveryEmail);
    domainCounts.set(domain, (domainCounts.get(domain) ?? 0) + 1);
    status[order.status] += 1;
    serviceMode[order.serviceMode] += 1;
  }
  return {
    domains: Array.from(domainCounts.entries())
      .map(([key, count]) => ({ key, count }))
      .sort((left, right) => right.count - left.count),
    status,
    serviceMode,
  };
}

function simulateLatency() {
  return new Promise((resolve) =>
    window.setTimeout(resolve, 220 + Math.random() * 260)
  );
}

export async function listMockOrders(
  filter: MockOrderListFilter,
  offset: number,
  limit: number,
  afterId?: number
): Promise<MockOrderListResponse> {
  await simulateLatency();
  const filtered = dataset.filter((order) => matchesFilter(order, filter));
  const startIndex = afterId
    ? filtered.findIndex((order) => order.id < afterId)
    : offset;
  const safeStart = startIndex < 0 ? filtered.length : startIndex;
  const items = filtered
    .slice(safeStart, safeStart + limit)
    .map((order) => ({ ...order }));
  const lastItem = items[items.length - 1];
  return {
    items,
    nextAfterId:
      lastItem && safeStart + items.length < filtered.length
        ? lastItem.id
        : undefined,
    total: filtered.length,
    facets: buildFacets(filtered),
  };
}

// ---------------------------------------------------------------------------
// Order mailbox mock (pickup-style mail list per order).

const PROJECT_SENDERS: Record<string, string> = {
  Telegram: "noreply@telegram.org",
  Discord: "noreply@discord.com",
  TikTok: "register@account.tiktok.com",
  Instagram: "security@mail.instagram.com",
  OpenAI: "noreply@tm.openai.com",
  Steam: "noreply@steampowered.com",
};

const messagesByOrderNo = new Map<string, WorkbenchMessage[]>();
let messageSeq = 1;

function buildCodeMessage(order: MockOrder, code: string, receivedAt: string) {
  const sender =
    PROJECT_SENDERS[order.projectName] ??
    `noreply@${order.projectName.toLowerCase()}.com`;
  const body = [
    "Hi,",
    "",
    `Your ${order.projectName} verification code is ${code}.`,
    "",
    "This code expires in 10 minutes. If you didn't request it, you can safely ignore this email.",
    "",
    `— The ${order.projectName} Team`,
  ].join("\n");
  const message: WorkbenchMessage = {
    id: `m-${messageSeq++}`,
    subject: `${code} is your ${order.projectName} verification code`,
    sender,
    preview: `Your ${order.projectName} verification code is ${code}.`,
    body,
    receivedAt,
    status: "matched",
    verificationCode: code,
  };
  return message;
}

function buildNoiseMessage(order: MockOrder, receivedAt: string) {
  const isMicrosoft = order.productType === "microsoft";
  const subject = isMicrosoft
    ? "Getting started with your new Outlook.com account"
    : "Your mailbox has been provisioned";
  const sender = isMicrosoft
    ? "member_services@microsoft.com"
    : `postmaster@${getOrderDomain(order.deliveryEmail).slice(1)}`;
  const message: WorkbenchMessage = {
    id: `m-${messageSeq++}`,
    subject,
    sender,
    preview: isMicrosoft
      ? "Welcome! Here are a few tips to get the most out of your inbox."
      : "Inbound routing is active. Messages sent to this address will arrive here.",
    body: isMicrosoft
      ? "Welcome!\n\nHere are a few tips to get the most out of your inbox: organize with folders, sweep newsletters, and enable two-step verification."
      : "Inbound routing is active.\n\nMessages sent to this address will arrive in this mailbox automatically.",
    receivedAt,
    status: "ignored",
  };
  return message;
}

function ensureOrderMessages(order: MockOrder) {
  let messages = messagesByOrderNo.get(order.orderNo);
  if (messages) return messages;

  messages = [];
  const createdAtMs = new Date(order.createdAt).getTime();
  if (random() < 0.55) {
    messages.push(
      buildNoiseMessage(order, new Date(createdAtMs + MINUTE).toISOString())
    );
  }
  if (order.verificationCode && order.lastMailReceivedAt) {
    messages.push(
      buildCodeMessage(order, order.verificationCode, order.lastMailReceivedAt)
    );
  }
  messagesByOrderNo.set(order.orderNo, messages);
  return messages;
}

function sortedMessages(messages: WorkbenchMessage[]) {
  return [...messages].sort(
    (left, right) =>
      new Date(right.receivedAt).getTime() - new Date(left.receivedAt).getTime()
  );
}

export async function getMockOrderMessages(orderNo: string) {
  await simulateLatency();
  const order = dataset.find((item) => item.orderNo === orderNo);
  if (!order) return [];
  return sortedMessages(ensureOrderMessages(order));
}

export interface MockFetchMailResult {
  cooldownSeconds: number;
  delivered: boolean;
  messages: WorkbenchMessage[];
  order: MockOrder;
}

export async function fetchMockOrderMail(
  orderNo: string
): Promise<MockFetchMailResult> {
  await simulateLatency();
  const order = dataset.find((item) => item.orderNo === orderNo);
  if (!order) throw new Error("Order not found.");

  const messages = ensureOrderMessages(order);
  const now = Date.now();
  let delivered = false;

  const waitingDelivery = order.status === "active" && !order.verificationCode;
  if (waitingDelivery && Math.random() < 0.45) {
    const code = toVerificationCode();
    const receivedAt = new Date(now).toISOString();
    order.verificationCode = code;
    order.hasDelivery = true;
    order.lastMailReceivedAt = receivedAt;
    if (order.serviceMode === "code") {
      order.status = "completed";
      order.receiveUntil = new Date(now + HOUR).toISOString();
    } else if (!order.activatedAt) {
      order.activatedAt = receivedAt;
      order.afterSaleUntil = new Date(now + 48 * HOUR).toISOString();
      order.receiveUntil = order.afterSaleUntil;
    }
    messages.push(buildCodeMessage(order, code, receivedAt));
    delivered = true;
  } else if (Math.random() < 0.2) {
    messages.push(buildNoiseMessage(order, new Date(now).toISOString()));
  }

  return {
    cooldownSeconds: randomInt(5, 8),
    delivered,
    messages: sortedMessages(messages),
    order: { ...order },
  };
}
