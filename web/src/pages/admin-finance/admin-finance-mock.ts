// Mock data for the Admin Finance Center page UI review.
//
// The API surface mirrors docs/6-billing-wallet.md, docs/8-iam.md and
// api/openapi.yaml so swapping to the real backend later only needs to replace
// these imports with real API modules:
//   - GET/POST/PATCH /v1/admin/invites              -> *MockFinanceInvite*
//   - GET/POST/PATCH /v1/admin/cards                -> *MockFinanceCardKey*
//   - GET            /v1/wallet/transactions?scope=all -> listMockFinanceTransactions
//   - POST           /v1/admin/wallets/{userId}/credit|debit -> *MockFinanceWallet*
//   - (planned)      finance summary endpoint         -> getMockFinanceSummary

import { IamApiError } from "@/lib/api-client";

export type FinanceInviteKind = "admin" | "referral";
export type FinanceEnabledFilter = "all" | "enabled" | "disabled";
export type FinanceCardKeyStatus = "enabled" | "disabled";
export type FinanceCardKeyStatusFilter = "all" | FinanceCardKeyStatus;
export type FinanceOwnerRoleFilter = "all" | FinanceUserRole;

// Canonical owner role / group option lists reused by the invite filters.
export const FINANCE_USER_ROLES: FinanceUserRole[] = [
  "user",
  "supplier",
  "admin",
  "super_admin",
];

export type FinanceTransactionType =
  | "recharge"
  | "debit"
  | "refund"
  | "freeze"
  | "credit"
  | "withdrawal"
  | "manual_adjustment"
  | "card_redeem"
  | "transfer";

export type FinanceTransactionDirection = "in" | "out";
export type FinanceTransactionBucket =
  | "consumer"
  | "supplier_available"
  | "supplier_frozen";

export interface FinanceInvite {
  code: string;
  kind: FinanceInviteKind;
  enabled: boolean;
  maxUse: number;
  used: number;
  expireAt?: string | null;
  ownerUserId?: number | null;
  ownerEmail?: string | null;
  ownerNickname?: string | null;
  ownerRole?: FinanceUserRole | null;
  ownerGroupName?: string | null;
  createdAt: string;
  updatedAt: string;
}

export interface FinanceInviteUse {
  id: number;
  inviteCode: string;
  userId: number;
  userEmail: string;
  userNickname: string;
  userRole: FinanceUserRole;
  userGroupName: string;
  usedAt: string;
}

export interface FinanceCardKey {
  key: string;
  amount: string;
  status: FinanceCardKeyStatus;
  maxRedemptions: number;
  redeemedCount: number;
  expireAt?: string | null;
  ownerUserId?: number | null;
  ownerEmail?: string | null;
  ownerNickname?: string | null;
  ownerRole?: FinanceUserRole | null;
  ownerGroupName?: string | null;
  createdAt: string;
  updatedAt: string;
}

export interface FinanceCardKeyRedemption {
  id: number;
  cardKey: string;
  userId: number;
  userEmail: string;
  userNickname: string;
  userRole: FinanceUserRole;
  userGroupName: string;
  amount: string;
  redeemedAt: string;
}

export interface FinanceTransaction {
  id: number;
  transactionNo: string;
  userId: number;
  userEmail: string;
  userNickname: string;
  userRole: FinanceUserRole;
  userGroupName: string;
  transactionType: FinanceTransactionType;
  balanceBucket: FinanceTransactionBucket;
  direction: FinanceTransactionDirection;
  amount: string;
  balanceBefore: string;
  balanceAfter: string;
  bizType: string;
  bizId: string;
  reversed?: boolean;
  reversedByNo?: string | null;
  reversalOfNo?: string | null;
  createdAt: string;
}

export type FinanceUserRole = "user" | "supplier" | "admin" | "super_admin";

export interface FinanceUserBalance {
  userId: number;
  email: string;
  nickname: string;
  role: FinanceUserRole;
  groupName: string;
  consumerBalance: string;
  supplierAvailable: string;
  supplierFrozen: string;
  updatedAt: string;
}

export interface FinanceHotItem {
  name: string;
  amount: string;
  count: number;
}

export interface FinanceTrendPoint {
  label: string;
  recharge: number;
  spend: number;
  withdraw: number;
  refund: number;
  platformRevenue: number;
  accountRevenue: number;
}

export interface FinanceSummary {
  rechargeAmount: string;
  spendAmount: string;
  withdrawAmount: string;
  refundAmount: string;
  platformRevenue: string;
  accountRevenue: string;
  trend: FinanceTrendPoint[];
  hotProjects: FinanceHotItem[];
  hotProducts: FinanceHotItem[];
}

export interface FinanceInviteListFilter {
  search?: string;
  ownerRole?: FinanceUserRole;
  ownerGroupName?: string;
  enabled?: boolean;
}

export interface FinanceCardKeyListFilter {
  search?: string;
  ownerRole?: FinanceUserRole;
  ownerGroupName?: string;
  status?: FinanceCardKeyStatus;
}

export interface FinanceTransactionListFilter {
  search?: string;
  transactionType?: FinanceTransactionType;
  direction?: FinanceTransactionDirection;
  createdFrom?: string;
  createdTo?: string;
}

export interface FinanceUserBalanceListFilter {
  search?: string;
}

export interface FinanceInviteFacets {
  role: Record<FinanceOwnerRoleFilter, number>;
  group: Record<string, number>;
  enabled: Record<FinanceEnabledFilter, number>;
}

export interface FinanceCardKeyFacets {
  role: Record<FinanceOwnerRoleFilter, number>;
  group: Record<string, number>;
  status: Record<FinanceCardKeyStatusFilter, number>;
}

export interface FinancePagedResult<T, F = undefined> {
  items: T[];
  total: number;
  offset: number;
  limit: number;
  facets?: F;
}

export interface FinanceBulkResult {
  affected: number;
  skipped: number;
}

export interface FinanceWalletAdjustmentResult {
  wallet: FinanceUserBalance;
  transaction: FinanceTransaction;
}

export interface CreateFinanceInviteInput {
  code?: string;
  enabled?: boolean;
  maxUse: number;
  expireAt?: string | null;
}

export interface BatchCreateFinanceInviteInput {
  count: number;
  maxUse: number;
  enabled?: boolean;
  expireAt?: string | null;
  prefix?: string;
}

export interface UpdateFinanceInviteInput {
  enabled?: boolean;
  maxUse?: number;
  expireAt?: string | null;
}

export interface CreateFinanceCardKeyInput {
  amount: string;
  count?: number;
  maxRedemptions?: number;
  expireAt?: string | null;
  cardKeys?: string[];
}

export interface UpdateFinanceCardKeyInput {
  status?: FinanceCardKeyStatus;
  maxRedemptions?: number;
  expireAt?: string | null;
}

const DAY = 24 * 60 * 60 * 1000;
const HOUR = 60 * 60 * 1000;
const LATENCY_MS = 180;
const MAX_SUMMARY_RANGE_DAYS = 366;

const LOCAL_HEADS = [
  "alice",
  "bob",
  "carol",
  "dave",
  "erin",
  "frank",
  "grace",
  "heidi",
  "ivan",
  "judy",
  "mallory",
  "oscar",
  "peggy",
  "trent",
  "victor",
  "wendy",
];
const DOMAINS = ["example.com", "mail.test", "remail.dev", "inbox.demo"];
const NICKNAMES = [
  "星河",
  "青柠",
  "北风",
  "流云",
  "晚舟",
  "雾岛",
  "白鹭",
  "南巷",
  "听雨",
  "清欢",
];
const ADJUST_REASONS = [
  "Manual top-up",
  "Compensation",
  "Refund correction",
  "Promo credit",
  "Risk clawback",
];

let idSeq = 1000;
let seed = 20260713;

function nextId() {
  idSeq += 1;
  return idSeq;
}

function random() {
  seed = (seed * 1664525 + 1013904223) >>> 0;
  return seed / 0x100000000;
}

function randomInt(min: number, max: number) {
  return Math.floor(random() * (max - min + 1)) + min;
}

function pick<T>(items: T[]): T {
  return items[Math.floor(random() * items.length)]!;
}

function hex(length: number) {
  let value = "";
  while (value.length < length) {
    value += Math.floor(random() * 0xffffffff)
      .toString(16)
      .padStart(8, "0");
  }
  return value.slice(0, length);
}

function money(value: number) {
  return (Math.round(Math.max(0, value) * 100) / 100).toFixed(2);
}

function simulateLatency(ms = LATENCY_MS) {
  return new Promise<void>((resolve) => {
    globalThis.setTimeout(resolve, ms);
  });
}

function nowIso(offsetMs = 0) {
  return new Date(Date.now() + offsetMs).toISOString();
}

function matchesSearch(haystack: string, keyword?: string) {
  if (!keyword?.trim()) return true;
  return haystack.toLowerCase().includes(keyword.trim().toLowerCase());
}

function inDateRange(value: string, from?: string, to?: string) {
  const time = new Date(value).getTime();
  if (!Number.isFinite(time)) return true;
  if (from) {
    const fromTime = new Date(from).getTime();
    if (Number.isFinite(fromTime) && time < fromTime) return false;
  }
  if (to) {
    const toTime = new Date(to).getTime();
    if (Number.isFinite(toTime) && time > toTime) return false;
  }
  return true;
}

function generateInviteCode(prefix = "INV") {
  return `${prefix}${hex(8).toUpperCase()}`;
}

function generateCardKey() {
  return `RM-${hex(16).toUpperCase()}`;
}

function cloneInvite(item: FinanceInvite): FinanceInvite {
  return { ...item };
}

function cloneCardKey(item: FinanceCardKey): FinanceCardKey {
  return { ...item };
}

function cloneTransaction(item: FinanceTransaction): FinanceTransaction {
  return { ...item };
}

function cloneBalance(item: FinanceUserBalance): FinanceUserBalance {
  return { ...item };
}

function getBucketBalance(
  user: FinanceUserBalance,
  bucket: FinanceTransactionBucket,
) {
  if (bucket === "supplier_available") return Number(user.supplierAvailable) || 0;
  if (bucket === "supplier_frozen") return Number(user.supplierFrozen) || 0;
  return Number(user.consumerBalance) || 0;
}

function setBucketBalance(
  user: FinanceUserBalance,
  bucket: FinanceTransactionBucket,
  value: number,
) {
  const next = money(value);
  if (bucket === "supplier_available") user.supplierAvailable = next;
  else if (bucket === "supplier_frozen") user.supplierFrozen = next;
  else user.consumerBalance = next;
}

const users: FinanceUserBalance[] = [];
const invites: FinanceInvite[] = [];
const inviteUses: FinanceInviteUse[] = [];
const cardKeys: FinanceCardKey[] = [];
const cardKeyRedemptions: FinanceCardKeyRedemption[] = [];
const transactions: FinanceTransaction[] = [];

const USER_GROUP_NAMES = ["普通用户", "VIP", "代理商", "供应商"];

// Exported so the invite filter can render one option per known group.
export const FINANCE_USER_GROUP_NAMES = USER_GROUP_NAMES;

function roleForIndex(index: number): FinanceUserRole {
  if (index === 0) return "super_admin";
  if (index <= 3) return "admin";
  if (index % 5 === 0) return "supplier";
  return "user";
}

function buildUsers(now: number) {
  for (let index = 0; index < 48; index += 1) {
    const userId = index + 1;
    const email = `${LOCAL_HEADS[index % LOCAL_HEADS.length]}${randomInt(10, 9999)}@${pick(DOMAINS)}`;
    const role = roleForIndex(index);
    users.push({
      userId,
      email,
      nickname: pick(NICKNAMES),
      role,
      groupName:
        role === "supplier"
          ? "供应商"
          : USER_GROUP_NAMES[index % USER_GROUP_NAMES.length]!,
      consumerBalance: money(randomInt(0, 1200) + random()),
      supplierAvailable: money(index % 5 === 0 ? randomInt(0, 800) + random() : 0),
      supplierFrozen: money(index % 7 === 0 ? randomInt(0, 120) + random() : 0),
      updatedAt: new Date(now - randomInt(1, 20) * DAY).toISOString(),
    });
  }
}

function pickCreatorUser() {
  // Prefer lower IDs as mock admins/operators who create platform invite codes.
  return users[randomInt(0, Math.min(4, users.length - 1))] ?? users[0]!;
}

function ownerFieldsFromUser(user: FinanceUserBalance) {
  return {
    ownerUserId: user.userId,
    ownerEmail: user.email,
    ownerNickname: user.nickname,
    ownerRole: user.role,
    ownerGroupName: user.groupName,
  };
}

function buildInvites(now: number) {
  for (let index = 0; index < 18; index += 1) {
    const creator = pickCreatorUser();
    const createdAt = now - randomInt(1, 120) * DAY;
    const maxUse = pick([1, 5, 10, 20, 50, 100]);
    const used = randomInt(0, Math.min(8, maxUse));
    const invite: FinanceInvite = {
      code: generateInviteCode("ADM"),
      kind: "admin",
      enabled: random() > 0.15,
      maxUse,
      used,
      expireAt:
        random() > 0.45
          ? new Date(now + randomInt(-10, 90) * DAY).toISOString()
          : null,
      ...ownerFieldsFromUser(creator),
      createdAt: new Date(createdAt).toISOString(),
      updatedAt: new Date(createdAt + randomInt(1, 10) * HOUR).toISOString(),
    };
    invites.push(invite);
    seedInviteUses(invite, now);
  }

  for (let index = 0; index < 22; index += 1) {
    const owner = users[index % users.length]!;
    const createdAt = now - randomInt(1, 200) * DAY;
    const used = randomInt(0, 30);
    const invite: FinanceInvite = {
      code: `AFF${hex(10).toUpperCase()}`,
      kind: "referral",
      enabled: true,
      maxUse: 2147483647,
      used,
      expireAt: null,
      ...ownerFieldsFromUser(owner),
      createdAt: new Date(createdAt).toISOString(),
      updatedAt: new Date(createdAt + randomInt(1, 48) * HOUR).toISOString(),
    };
    invites.push(invite);
    seedInviteUses(invite, now);
  }
}

function seedInviteUses(invite: FinanceInvite, now: number) {
  const count = Math.min(invite.used, 40);
  if (count <= 0) return;
  const createdAt = new Date(invite.createdAt).getTime();
  for (let index = 0; index < count; index += 1) {
    const user = users[(invite.ownerUserId ?? 1) + index] ?? pick(users);
    const usedAt = Math.min(
      now,
      createdAt + randomInt(1, Math.max(2, Math.floor((now - createdAt) / HOUR))) * HOUR
    );
    inviteUses.push({
      id: nextId(),
      inviteCode: invite.code,
      userId: user.userId,
      userEmail: user.email,
      userNickname: user.nickname,
      userRole: user.role,
      userGroupName: user.groupName,
      usedAt: new Date(usedAt).toISOString(),
    });
  }
}

function buildCardKeys(now: number) {
  const amounts = ["10.00", "20.00", "50.00", "100.00", "200.00", "500.00"];
  for (let index = 0; index < 36; index += 1) {
    const creator = pickCreatorUser();
    const createdAt = now - randomInt(1, 90) * DAY;
    const maxRedemptions = pick([1, 1, 1, 5, 10, 20]);
    const redeemedCount = randomInt(0, maxRedemptions);
    const amount = pick(amounts);
    const card: FinanceCardKey = {
      key: generateCardKey(),
      amount,
      status: random() > 0.18 ? "enabled" : "disabled",
      maxRedemptions,
      redeemedCount,
      expireAt:
        random() > 0.4
          ? new Date(now + randomInt(-5, 60) * DAY).toISOString()
          : null,
      ...ownerFieldsFromUser(creator),
      createdAt: new Date(createdAt).toISOString(),
      updatedAt: new Date(createdAt + randomInt(1, 20) * HOUR).toISOString(),
    };
    cardKeys.push(card);
    seedCardKeyRedemptions(card, now);
  }
}

function seedCardKeyRedemptions(card: FinanceCardKey, now: number) {
  const count = Math.min(card.redeemedCount, 40);
  if (count <= 0) return;
  const createdAt = new Date(card.createdAt).getTime();
  for (let index = 0; index < count; index += 1) {
    const user = users[(card.ownerUserId ?? 1) + index * 2] ?? pick(users);
    const redeemedAt = Math.min(
      now,
      createdAt + randomInt(1, Math.max(2, Math.floor((now - createdAt) / HOUR))) * HOUR
    );
    cardKeyRedemptions.push({
      id: nextId(),
      cardKey: card.key,
      userId: user.userId,
      userEmail: user.email,
      userNickname: user.nickname,
      userRole: user.role,
      userGroupName: user.groupName,
      amount: card.amount,
      redeemedAt: new Date(redeemedAt).toISOString(),
    });
  }
}

function buildTransactions(now: number) {
  const types: FinanceTransactionType[] = [
    "recharge",
    "debit",
    "refund",
    "credit",
    "card_redeem",
    "manual_adjustment",
    "transfer",
  ];

  for (let index = 0; index < 160; index += 1) {
    const user = pick(users);
    const type = pick(types);
    const direction: FinanceTransactionDirection =
      type === "debit" || type === "withdrawal" || type === "freeze"
        ? "out"
        : type === "manual_adjustment"
          ? random() < 0.55
            ? "in"
            : "out"
          : "in";
    const amountValue =
      type === "debit"
        ? randomInt(1, 80) / 10
        : type === "recharge" || type === "card_redeem"
          ? randomInt(10, 300)
          : randomInt(1, 120);
    const balanceAfter = Number(user.consumerBalance) + randomInt(-20, 40);
    const balanceBefore =
      direction === "in"
        ? Math.max(0, balanceAfter - amountValue)
        : Math.max(0, balanceAfter + amountValue);
    const createdAt = now - randomInt(1, 45) * DAY - randomInt(0, 20) * HOUR;
    transactions.push({
      id: nextId(),
      transactionNo: `TX${Math.floor(createdAt).toString(16).toUpperCase().padStart(12, "0")}${hex(6).toUpperCase()}`,
      userId: user.userId,
      userEmail: user.email,
      userNickname: user.nickname,
      userRole: user.role,
      userGroupName: user.groupName,
      transactionType: type,
      balanceBucket: "consumer",
      direction,
      amount: money(amountValue),
      balanceBefore: money(balanceBefore),
      balanceAfter: money(Math.max(0, balanceAfter)),
      bizType: type,
      bizId:
        type === "manual_adjustment" ? pick(ADJUST_REASONS) : hex(8).toUpperCase(),
      reversed: false,
      reversedByNo: null,
      reversalOfNo: null,
      createdAt: new Date(createdAt).toISOString(),
    });
  }

  transactions.sort(
    (left, right) =>
      new Date(right.createdAt).getTime() - new Date(left.createdAt).getTime()
  );
}

function seedDataset() {
  const now = Date.now();
  buildUsers(now);
  buildInvites(now);
  buildCardKeys(now);
  buildTransactions(now);
}

seedDataset();

function findUser(userId: number) {
  const user = users.find((item) => item.userId === userId);
  if (!user) {
    throw new IamApiError(404, { message: "User not found." });
  }
  return user;
}

function findInvite(code: string) {
  const invite = invites.find((item) => item.code === code);
  if (!invite) {
    throw new IamApiError(404, { message: "Invite code not found." });
  }
  return invite;
}

function findCardKey(key: string) {
  const card = cardKeys.find((item) => item.key === key);
  if (!card) {
    throw new IamApiError(404, { message: "Card key not found." });
  }
  return card;
}

function paginate<T>(items: T[], offset: number, limit: number) {
  const safeOffset = Math.max(0, offset);
  const safeLimit = Math.max(1, Math.min(limit || 20, 1000));
  return {
    items: items.slice(safeOffset, safeOffset + safeLimit),
    total: items.length,
    offset: safeOffset,
    limit: safeLimit,
  };
}

function filterInvites(filter: FinanceInviteListFilter = {}) {
  return invites
    .filter((item) => {
      if (filter.ownerRole && item.ownerRole !== filter.ownerRole) return false;
      if (
        filter.ownerGroupName &&
        item.ownerGroupName !== filter.ownerGroupName
      ) {
        return false;
      }
      if (typeof filter.enabled === "boolean" && item.enabled !== filter.enabled) {
        return false;
      }
      return matchesSearch(
        `${item.code} ${item.ownerEmail ?? ""} ${item.ownerNickname ?? ""} ${item.ownerUserId ?? ""}`,
        filter.search
      );
    })
    .sort(
      (left, right) =>
        new Date(right.createdAt).getTime() - new Date(left.createdAt).getTime()
    );
}

function filterCardKeys(filter: FinanceCardKeyListFilter = {}) {
  return cardKeys
    .filter((item) => {
      if (filter.ownerRole && item.ownerRole !== filter.ownerRole) return false;
      if (
        filter.ownerGroupName &&
        item.ownerGroupName !== filter.ownerGroupName
      ) {
        return false;
      }
      if (filter.status && item.status !== filter.status) return false;
      return matchesSearch(
        `${item.key} ${item.amount} ${item.ownerEmail ?? ""} ${item.ownerNickname ?? ""}`,
        filter.search
      );
    })
    .sort(
      (left, right) =>
        new Date(right.createdAt).getTime() - new Date(left.createdAt).getTime()
    );
}

function filterTransactions(filter: FinanceTransactionListFilter = {}) {
  return transactions
    .filter((item) => {
      if (
        filter.transactionType &&
        item.transactionType !== filter.transactionType
      ) {
        return false;
      }
      if (filter.direction && item.direction !== filter.direction) return false;
      if (!inDateRange(item.createdAt, filter.createdFrom, filter.createdTo)) {
        return false;
      }
      return matchesSearch(
        `${item.transactionNo} ${item.userEmail} ${item.userId} ${item.bizId} ${item.transactionType}`,
        filter.search
      );
    })
    .sort(
      (left, right) =>
        new Date(right.createdAt).getTime() - new Date(left.createdAt).getTime() ||
        right.id - left.id
    );
}

function filterBalances(filter: FinanceUserBalanceListFilter = {}) {
  return users
    .filter((item) =>
      matchesSearch(
        `${item.userId} ${item.email} ${item.nickname}`,
        filter.search
      )
    )
    .sort((left, right) => left.userId - right.userId);
}

function buildInviteFacets(all: FinanceInvite[]): FinanceInviteFacets {
  const role: Record<FinanceOwnerRoleFilter, number> = {
    all: all.length,
    user: 0,
    supplier: 0,
    admin: 0,
    super_admin: 0,
  };
  const group: Record<string, number> = { all: all.length };
  for (const name of FINANCE_USER_GROUP_NAMES) group[name] = 0;
  const enabled: Record<FinanceEnabledFilter, number> = {
    all: all.length,
    enabled: 0,
    disabled: 0,
  };
  for (const item of all) {
    role[item.ownerRole ?? "user"] += 1;
    if (item.ownerGroupName) {
      group[item.ownerGroupName] = (group[item.ownerGroupName] ?? 0) + 1;
    }
    enabled[item.enabled ? "enabled" : "disabled"] += 1;
  }
  return { role, group, enabled };
}

function buildCardKeyFacets(all: FinanceCardKey[]): FinanceCardKeyFacets {
  const role: Record<FinanceOwnerRoleFilter, number> = {
    all: all.length,
    user: 0,
    supplier: 0,
    admin: 0,
    super_admin: 0,
  };
  const group: Record<string, number> = { all: all.length };
  for (const name of FINANCE_USER_GROUP_NAMES) group[name] = 0;
  const status: Record<FinanceCardKeyStatusFilter, number> = {
    all: all.length,
    enabled: 0,
    disabled: 0,
  };
  for (const item of all) {
    role[item.ownerRole ?? "user"] += 1;
    if (item.ownerGroupName) {
      group[item.ownerGroupName] = (group[item.ownerGroupName] ?? 0) + 1;
    }
    status[item.status] += 1;
  }
  return { role, group, status };
}

function appendAdjustmentTransaction(
  user: FinanceUserBalance,
  direction: FinanceTransactionDirection,
  amountValue: number,
  reason: string
) {
  const before = Number(user.consumerBalance);
  const after =
    direction === "in" ? before + amountValue : Math.max(0, before - amountValue);
  if (direction === "out" && amountValue > before) {
    throw new IamApiError(422, { message: "Insufficient balance." });
  }
  user.consumerBalance = money(after);
  user.updatedAt = nowIso();
  const transaction: FinanceTransaction = {
    id: nextId(),
    transactionNo: `TX${Date.now().toString(16).toUpperCase()}${hex(4).toUpperCase()}`,
    userId: user.userId,
    userEmail: user.email,
    userNickname: user.nickname,
    userRole: user.role,
    userGroupName: user.groupName,
    transactionType: "manual_adjustment",
    balanceBucket: "consumer",
    direction,
    amount: money(amountValue),
    balanceBefore: money(before),
    balanceAfter: money(after),
    bizType: "manual_adjustment",
    bizId: reason.trim(),
    reversed: false,
    reversedByNo: null,
    reversalOfNo: null,
    createdAt: nowIso(),
  };
  transactions.unshift(transaction);
  return transaction;
}

export async function listMockFinanceInvites(
  filter: FinanceInviteListFilter = {},
  offset = 0,
  limit = 20
): Promise<FinancePagedResult<FinanceInvite, FinanceInviteFacets>> {
  await simulateLatency();
  const filtered = filterInvites(filter);
  const page = paginate(filtered, offset, limit);
  return {
    ...page,
    items: page.items.map(cloneInvite),
    facets: buildInviteFacets(invites),
  };
}

export async function createMockFinanceInvite(
  input: CreateFinanceInviteInput
): Promise<FinanceInvite> {
  await simulateLatency(220);
  if (!input.maxUse || input.maxUse < 1) {
    throw new IamApiError(422, { message: "Max use must be at least 1." });
  }
  const code = (input.code?.trim() || generateInviteCode("ADM")).toUpperCase();
  if (invites.some((item) => item.code === code)) {
    throw new IamApiError(409, { message: "Invite code already exists." });
  }
  const creator = pickCreatorUser();
  const invite: FinanceInvite = {
    code,
    kind: "admin",
    enabled: input.enabled ?? true,
    maxUse: input.maxUse,
    used: 0,
    expireAt: input.expireAt ?? null,
    ...ownerFieldsFromUser(creator),
    createdAt: nowIso(),
    updatedAt: nowIso(),
  };
  invites.unshift(invite);
  return cloneInvite(invite);
}

export async function batchCreateMockFinanceInvites(
  input: BatchCreateFinanceInviteInput
): Promise<{ items: FinanceInvite[]; created: number }> {
  await simulateLatency(280);
  const count = Math.max(1, Math.min(input.count || 1, 100));
  if (!input.maxUse || input.maxUse < 1) {
    throw new IamApiError(422, { message: "Max use must be at least 1." });
  }
  const creator = pickCreatorUser();
  const created: FinanceInvite[] = [];
  for (let index = 0; index < count; index += 1) {
    let code = generateInviteCode(input.prefix?.trim() || "ADM");
    while (invites.some((item) => item.code === code)) {
      code = generateInviteCode(input.prefix?.trim() || "ADM");
    }
    const invite: FinanceInvite = {
      code,
      kind: "admin",
      enabled: input.enabled ?? true,
      maxUse: input.maxUse,
      used: 0,
      expireAt: input.expireAt ?? null,
      ...ownerFieldsFromUser(creator),
      createdAt: nowIso(),
      updatedAt: nowIso(),
    };
    invites.unshift(invite);
    created.push(cloneInvite(invite));
  }
  return { items: created, created: created.length };
}

export async function updateMockFinanceInvite(
  code: string,
  input: UpdateFinanceInviteInput
): Promise<FinanceInvite> {
  await simulateLatency(200);
  const invite = findInvite(code);
  if (typeof input.enabled === "boolean") invite.enabled = input.enabled;
  if (typeof input.maxUse === "number") {
    if (input.maxUse < Math.max(1, invite.used)) {
      throw new IamApiError(422, {
        message: "Max use cannot be less than used count.",
      });
    }
    invite.maxUse = input.maxUse;
  }
  if (input.expireAt !== undefined) invite.expireAt = input.expireAt;
  invite.updatedAt = nowIso();
  return cloneInvite(invite);
}

export async function setMockFinanceInvitesEnabled(
  codes: string[],
  enabled: boolean
): Promise<FinanceBulkResult> {
  await simulateLatency(220);
  let affected = 0;
  let skipped = 0;
  for (const code of codes) {
    const invite = invites.find((item) => item.code === code);
    if (!invite) {
      skipped += 1;
      continue;
    }
    if (invite.enabled === enabled) {
      skipped += 1;
      continue;
    }
    invite.enabled = enabled;
    invite.updatedAt = nowIso();
    affected += 1;
  }
  return { affected, skipped };
}

export async function setMockFinanceInviteEnabled(
  code: string,
  enabled: boolean
): Promise<FinanceInvite> {
  await simulateLatency(180);
  const invite = findInvite(code);
  invite.enabled = enabled;
  invite.updatedAt = nowIso();
  return cloneInvite(invite);
}

export async function setMockFinanceInvitesEnabledByFilter(
  filter: FinanceInviteListFilter,
  enabled: boolean
): Promise<FinanceBulkResult> {
  await simulateLatency(260);
  let affected = 0;
  let skipped = 0;
  for (const invite of filterInvites(filter)) {
    if (invite.enabled === enabled) {
      skipped += 1;
      continue;
    }
    invite.enabled = enabled;
    invite.updatedAt = nowIso();
    affected += 1;
  }
  return { affected, skipped };
}

export async function listMockFinanceInviteUses(
  inviteCode: string
): Promise<FinanceInviteUse[]> {
  await simulateLatency(160);
  return inviteUses
    .filter((item) => item.inviteCode === inviteCode)
    .sort(
      (left, right) =>
        new Date(right.usedAt).getTime() - new Date(left.usedAt).getTime()
    )
    .map((item) => ({ ...item }));
}

export async function listMockFinanceCardKeys(
  filter: FinanceCardKeyListFilter = {},
  offset = 0,
  limit = 20
): Promise<FinancePagedResult<FinanceCardKey, FinanceCardKeyFacets>> {
  await simulateLatency();
  const filtered = filterCardKeys(filter);
  const page = paginate(filtered, offset, limit);
  return {
    ...page,
    items: page.items.map(cloneCardKey),
    facets: buildCardKeyFacets(cardKeys),
  };
}

export async function createMockFinanceCardKeys(
  input: CreateFinanceCardKeyInput
): Promise<{ items: FinanceCardKey[]; created: number }> {
  await simulateLatency(260);
  const amountValue = Number(input.amount);
  if (!Number.isFinite(amountValue) || amountValue <= 0) {
    throw new IamApiError(422, { message: "Amount must be positive." });
  }
  const explicitKeys = Array.from(
    new Set((input.cardKeys ?? []).map((item) => item.trim()).filter(Boolean))
  );
  const count = explicitKeys.length
    ? explicitKeys.length
    : Math.max(1, Math.min(input.count || 1, 1000));
  if (count > 1000) {
    throw new IamApiError(422, { message: "Cannot create more than 1000 cards." });
  }
  const maxRedemptions = input.maxRedemptions && input.maxRedemptions > 0
    ? input.maxRedemptions
    : 1;
  const creator = pickCreatorUser();
  const created: FinanceCardKey[] = [];
  for (let index = 0; index < count; index += 1) {
    const key = explicitKeys[index] || generateCardKey();
    if (cardKeys.some((item) => item.key === key)) {
      throw new IamApiError(409, { message: `Card key already exists: ${key}` });
    }
    const card: FinanceCardKey = {
      key,
      amount: money(amountValue),
      status: "enabled",
      maxRedemptions,
      redeemedCount: 0,
      expireAt: input.expireAt ?? null,
      ...ownerFieldsFromUser(creator),
      createdAt: nowIso(),
      updatedAt: nowIso(),
    };
    cardKeys.unshift(card);
    created.push(cloneCardKey(card));
  }
  return { items: created, created: created.length };
}

export async function updateMockFinanceCardKey(
  key: string,
  input: UpdateFinanceCardKeyInput
): Promise<FinanceCardKey> {
  await simulateLatency(200);
  const card = findCardKey(key);
  if (input.status) card.status = input.status;
  if (typeof input.maxRedemptions === "number") {
    if (input.maxRedemptions < Math.max(1, card.redeemedCount)) {
      throw new IamApiError(422, {
        message: "Max redemptions cannot be less than redeemed count.",
      });
    }
    card.maxRedemptions = input.maxRedemptions;
  }
  if (input.expireAt !== undefined) card.expireAt = input.expireAt;
  card.updatedAt = nowIso();
  return cloneCardKey(card);
}

export async function setMockFinanceCardKeyStatus(
  key: string,
  status: FinanceCardKeyStatus
): Promise<FinanceCardKey> {
  await simulateLatency(180);
  const card = findCardKey(key);
  card.status = status;
  card.updatedAt = nowIso();
  return cloneCardKey(card);
}

export async function listMockFinanceCardKeyRedemptions(
  cardKey: string
): Promise<FinanceCardKeyRedemption[]> {
  await simulateLatency(160);
  return cardKeyRedemptions
    .filter((item) => item.cardKey === cardKey)
    .sort(
      (left, right) =>
        new Date(right.redeemedAt).getTime() -
        new Date(left.redeemedAt).getTime()
    )
    .map((item) => ({ ...item }));
}

export async function setMockFinanceCardKeysStatus(
  keys: string[],
  status: FinanceCardKeyStatus
): Promise<FinanceBulkResult> {
  await simulateLatency(220);
  let affected = 0;
  let skipped = 0;
  for (const key of keys) {
    const card = cardKeys.find((item) => item.key === key);
    if (!card) {
      skipped += 1;
      continue;
    }
    card.status = status;
    card.updatedAt = nowIso();
    affected += 1;
  }
  return { affected, skipped };
}

export async function setMockFinanceCardKeysStatusByFilter(
  filter: FinanceCardKeyListFilter,
  status: FinanceCardKeyStatus
): Promise<FinanceBulkResult> {
  await simulateLatency(260);
  let affected = 0;
  let skipped = 0;
  for (const card of filterCardKeys(filter)) {
    if (card.status === status) {
      skipped += 1;
      continue;
    }
    card.status = status;
    card.updatedAt = nowIso();
    affected += 1;
  }
  return { affected, skipped };
}

export async function listMockFinanceTransactions(
  filter: FinanceTransactionListFilter = {},
  offset = 0,
  limit = 20
): Promise<FinancePagedResult<FinanceTransaction>> {
  await simulateLatency();
  const filtered = filterTransactions(filter);
  const page = paginate(filtered, offset, limit);
  return {
    ...page,
    items: page.items.map(cloneTransaction),
  };
}

// The ledger is immutable: a "reverse" appends a compensating entry with the
// opposite direction and flags the original, mirroring accounting practice.
export async function reverseMockFinanceTransaction(
  id: number
): Promise<{ original: FinanceTransaction; reversal: FinanceTransaction }> {
  await simulateLatency(240);
  const original = transactions.find((item) => item.id === id);
  if (!original) {
    throw new IamApiError(404, { message: "Transaction not found." });
  }
  if (original.reversalOfNo) {
    throw new IamApiError(422, {
      message: "A reversal entry cannot be reversed.",
    });
  }
  if (original.reversed) {
    throw new IamApiError(422, {
      message: "Transaction already reversed.",
    });
  }

  const amountValue = Number(original.amount) || 0;
  const reverseDirection: FinanceTransactionDirection =
    original.direction === "in" ? "out" : "in";
  const user = users.find((item) => item.userId === original.userId);
  const before = user
    ? getBucketBalance(user, original.balanceBucket)
    : Number(original.balanceAfter) || 0;
  if (reverseDirection === "out" && amountValue > before) {
    throw new IamApiError(422, {
      message: "Insufficient balance to reverse this transaction.",
    });
  }
  const after =
    reverseDirection === "in" ? before + amountValue : before - amountValue;
  const reversalNo = `TX${Date.now().toString(16).toUpperCase()}${hex(4).toUpperCase()}`;

  const reversal: FinanceTransaction = {
    id: nextId(),
    transactionNo: reversalNo,
    userId: original.userId,
    userEmail: original.userEmail,
    userNickname: original.userNickname,
    userRole: original.userRole,
    userGroupName: original.userGroupName,
    transactionType: "manual_adjustment",
    balanceBucket: original.balanceBucket,
    direction: reverseDirection,
    amount: money(amountValue),
    balanceBefore: money(before),
    balanceAfter: money(after),
    bizType: "reversal",
    bizId: original.transactionNo,
    reversed: false,
    reversedByNo: null,
    reversalOfNo: original.transactionNo,
    createdAt: nowIso(),
  };
  transactions.unshift(reversal);
  original.reversed = true;
  original.reversedByNo = reversalNo;

  // Reflect the compensation in the same wallet bucket when we track the user.
  if (user) {
    setBucketBalance(user, original.balanceBucket, after);
    user.updatedAt = nowIso();
  }

  return {
    original: cloneTransaction(original),
    reversal: cloneTransaction(reversal),
  };
}

export async function listMockFinanceUserBalances(
  filter: FinanceUserBalanceListFilter = {},
  offset = 0,
  limit = 20
): Promise<FinancePagedResult<FinanceUserBalance>> {
  await simulateLatency();
  const filtered = filterBalances(filter);
  const page = paginate(filtered, offset, limit);
  return {
    ...page,
    items: page.items.map(cloneBalance),
  };
}

export async function creditMockFinanceUserWallet(
  userId: number,
  amount: string,
  reason: string
): Promise<FinanceWalletAdjustmentResult> {
  await simulateLatency(220);
  const value = Number(amount);
  if (!Number.isFinite(value) || value <= 0) {
    throw new IamApiError(422, { message: "Amount must be positive." });
  }
  if (!reason.trim()) {
    throw new IamApiError(422, { message: "Reason is required." });
  }
  const user = findUser(userId);
  const transaction = appendAdjustmentTransaction(user, "in", value, reason);
  return {
    wallet: cloneBalance(user),
    transaction: cloneTransaction(transaction),
  };
}

export async function debitMockFinanceUserWallet(
  userId: number,
  amount: string,
  reason: string
): Promise<FinanceWalletAdjustmentResult> {
  await simulateLatency(220);
  const value = Number(amount);
  if (!Number.isFinite(value) || value <= 0) {
    throw new IamApiError(422, { message: "Amount must be positive." });
  }
  if (!reason.trim()) {
    throw new IamApiError(422, { message: "Reason is required." });
  }
  const user = findUser(userId);
  const transaction = appendAdjustmentTransaction(user, "out", value, reason);
  return {
    wallet: cloneBalance(user),
    transaction: cloneTransaction(transaction),
  };
}

// Withdrawal draws from the supplier-available (withdrawable) bucket and writes
// a "withdrawal" ledger entry, per docs/6-billing-wallet.md.
export async function withdrawMockFinanceUserWallet(
  userId: number,
  amount: string,
  note: string
): Promise<FinanceWalletAdjustmentResult> {
  await simulateLatency(240);
  const value = Number(amount);
  if (!Number.isFinite(value) || value <= 0) {
    throw new IamApiError(422, { message: "Amount must be positive." });
  }
  const user = findUser(userId);
  const before = Number(user.supplierAvailable) || 0;
  if (value > before) {
    throw new IamApiError(422, {
      message: "Withdrawal exceeds withdrawable balance.",
    });
  }
  const after = Math.max(0, before - value);
  user.supplierAvailable = money(after);
  user.updatedAt = nowIso();

  const transaction: FinanceTransaction = {
    id: nextId(),
    transactionNo: `TX${Date.now().toString(16).toUpperCase()}${hex(4).toUpperCase()}`,
    userId: user.userId,
    userEmail: user.email,
    userNickname: user.nickname,
    userRole: user.role,
    userGroupName: user.groupName,
    transactionType: "withdrawal",
    balanceBucket: "supplier_available",
    direction: "out",
    amount: money(value),
    balanceBefore: money(before),
    balanceAfter: money(after),
    bizType: "withdrawal",
    bizId: note.trim() || "withdrawal",
    reversed: false,
    reversedByNo: null,
    reversalOfNo: null,
    createdAt: nowIso(),
  };
  transactions.unshift(transaction);

  return {
    wallet: cloneBalance(user),
    transaction: cloneTransaction(transaction),
  };
}

export async function adjustMockFinanceUsersWallet(
  userIds: number[],
  signedAmount: number,
  reason: string
): Promise<FinanceBulkResult> {
  await simulateLatency(260);
  if (!Number.isFinite(signedAmount) || signedAmount === 0) {
    throw new IamApiError(422, { message: "Amount cannot be zero." });
  }
  if (!reason.trim()) {
    throw new IamApiError(422, { message: "Reason is required." });
  }
  let affected = 0;
  let skipped = 0;
  for (const userId of userIds) {
    const user = users.find((item) => item.userId === userId);
    if (!user) {
      skipped += 1;
      continue;
    }
    try {
      appendAdjustmentTransaction(
        user,
        signedAmount > 0 ? "in" : "out",
        Math.abs(signedAmount),
        reason
      );
      affected += 1;
    } catch {
      skipped += 1;
    }
  }
  return { affected, skipped };
}

const HOT_PROJECTS = [
  "Outlook 接码",
  "Hotmail 批发",
  "企业邮箱池",
  "域名邮箱",
  "Graph 通道",
  "IMAP 通道",
  "验证码加速",
  "库存托管",
  "账号安全服务",
  "邮件代收服务",
];

const HOT_PRODUCTS = [
  "Outlook 单次",
  "Hotmail 批量",
  "企业邮箱日租",
  "域名邮箱月租",
  "Graph 高速",
  "IMAP 稳定",
  "验证码加速包",
  "库存续费包",
  "通道保活",
  "批量导入权益",
];

function dayKey(value: string | Date) {
  const date = value instanceof Date ? value : new Date(value);
  const year = date.getFullYear();
  const month = String(date.getMonth() + 1).padStart(2, "0");
  const day = String(date.getDate()).padStart(2, "0");
  return `${year}-${month}-${day}`;
}

function isSameCalendarDay(left: Date, right: Date) {
  return (
    left.getFullYear() === right.getFullYear() &&
    left.getMonth() === right.getMonth() &&
    left.getDate() === right.getDate()
  );
}

function resolveSummaryRange(filter: {
  createdFrom?: string;
  createdTo?: string;
}) {
  const now = new Date();
  now.setSeconds(0, 0);
  const parsedFrom = filter.createdFrom
    ? new Date(filter.createdFrom)
    : undefined;
  const parsedTo = filter.createdTo ? new Date(filter.createdTo) : undefined;
  const validFrom =
    parsedFrom && !Number.isNaN(parsedFrom.getTime()) ? parsedFrom : undefined;
  const validTo =
    parsedTo && !Number.isNaN(parsedTo.getTime()) ? parsedTo : undefined;
  const defaultFrom = new Date((validTo ?? now).getTime());
  defaultFrom.setHours(0, 0, 0, 0);
  let from = new Date((validFrom ?? defaultFrom).getTime());
  let to = new Date((validTo ?? now).getTime());

  if (from.getTime() > to.getTime()) [from, to] = [to, from];
  if (to.getTime() > now.getTime()) to = new Date(now.getTime());
  if (from.getTime() > to.getTime()) from = new Date(to.getTime());

  const earliestAllowed = new Date(to.getTime());
  earliestAllowed.setDate(
    earliestAllowed.getDate() - (MAX_SUMMARY_RANGE_DAYS - 1),
  );
  if (from.getTime() < earliestAllowed.getTime()) from = earliestAllowed;

  return { from, to };
}

function pseudoNoise(seed: number) {
  const value = Math.sin(seed * 12.9898) * 43758.5453;
  return value - Math.floor(value);
}

function buildSeriesPoint(
  seed: number,
  hourWeight = 1,
  coverage = 1,
): Omit<FinanceTrendPoint, "label"> {
  const base = 180 + pseudoNoise(seed) * 220;
  const wave = 1 + Math.sin(seed / 2.4) * 0.28 + Math.cos(seed / 3.1) * 0.16;
  const recharge = Math.max(20, base * wave * hourWeight);
  const spend = Math.max(18, recharge * (0.58 + pseudoNoise(seed + 3) * 0.22));
  const withdraw = Math.max(4, spend * (0.08 + pseudoNoise(seed + 5) * 0.08));
  const refund = Math.max(2, spend * (0.04 + pseudoNoise(seed + 7) * 0.05));
  const platformRevenue = Math.max(
    6,
    spend * 0.18 - refund * 0.12 + recharge * 0.03
  );
  const accountRevenue = Math.max(2, spend * 0.07 - refund * 0.04);
  return {
    recharge: Number((recharge * coverage).toFixed(2)),
    spend: Number((spend * coverage).toFixed(2)),
    withdraw: Number((withdraw * coverage).toFixed(2)),
    refund: Number((refund * coverage).toFixed(2)),
    platformRevenue: Number((platformRevenue * coverage).toFixed(2)),
    accountRevenue: Number((accountRevenue * coverage).toFixed(2)),
  };
}

function buildTrendSeries(from: Date, to: Date): FinanceTrendPoint[] {
  const sameDay = isSameCalendarDay(from, to);
  const points: FinanceTrendPoint[] = [];

  if (sameDay) {
    const cursor = new Date(from.getTime());
    cursor.setMinutes(0, 0, 0);
    while (cursor.getTime() <= to.getTime()) {
      const hour = cursor.getHours();
      const bucketStart = Math.max(cursor.getTime(), from.getTime());
      const bucketEnd = Math.min(cursor.getTime() + HOUR, to.getTime() + 1);
      const coverage = Math.max(0, bucketEnd - bucketStart) / HOUR;
      const hourWeight =
        hour < 7
          ? 0.35
          : hour < 11
            ? 0.9
            : hour < 14
              ? 1.25
              : hour < 19
                ? 1.1
                : 0.7;
      const point = buildSeriesPoint(
        from.getFullYear() * 10000 +
          (from.getMonth() + 1) * 100 +
          from.getDate() * 10 +
          hour,
        hourWeight,
        coverage,
      );
      points.push({
        label: `${String(hour).padStart(2, "0")}:00`,
        ...point,
      });
      cursor.setTime(cursor.getTime() + HOUR);
    }
    return points;
  }

  const start = new Date(from);
  start.setHours(0, 0, 0, 0);
  const end = new Date(to);
  end.setHours(0, 0, 0, 0);
  let index = 0;
  for (
    const date = new Date(start.getTime());
    date.getTime() <= end.getTime();
    date.setDate(date.getDate() + 1), index += 1
  ) {
    const nextDay = new Date(date.getTime());
    nextDay.setDate(nextDay.getDate() + 1);
    const bucketStart = Math.max(date.getTime(), from.getTime());
    const bucketEnd = Math.min(nextDay.getTime(), to.getTime() + 1);
    const coverage =
      Math.max(0, bucketEnd - bucketStart) /
      (nextDay.getTime() - date.getTime());
    const weekdayBoost =
      [0.75, 1, 1.05, 1.08, 1.12, 1.18, 0.9][date.getDay()] ?? 1;
    const point = buildSeriesPoint(
      date.getFullYear() * 1000 +
        (date.getMonth() + 1) * 40 +
        date.getDate() +
        index,
      weekdayBoost,
      coverage,
    );
    points.push({
      label:
        from.getFullYear() === to.getFullYear()
          ? `${date.getMonth() + 1}/${date.getDate()}`
          : `${date.getFullYear()}/${date.getMonth() + 1}/${date.getDate()}`,
      ...point,
    });
  }
  return points.length ? points : [{ label: dayKey(from), ...buildSeriesPoint(1) }];
}

function applyLedgerTransactionToPoint(
  point: FinanceTrendPoint,
  item: FinanceTransaction,
) {
  const amount = Number(item.amount);
  if (!Number.isFinite(amount) || item.reversed || item.reversalOfNo) return;

  switch (item.transactionType) {
    case "recharge":
    case "card_redeem":
      point.recharge += amount;
      break;
    case "debit":
      point.spend += amount;
      point.platformRevenue += amount * 0.18;
      point.accountRevenue += amount * 0.07;
      break;
    case "withdrawal":
      point.withdraw += amount;
      break;
    case "refund":
      point.refund += amount;
      point.platformRevenue = Math.max(0, point.platformRevenue - amount * 0.12);
      point.accountRevenue = Math.max(0, point.accountRevenue - amount * 0.04);
      break;
    default:
      break;
  }
}

function mergeLedgerIntoTrend(
  trend: FinanceTrendPoint[],
  scoped: FinanceTransaction[],
  from: Date,
  to: Date,
) {
  const hourly = isSameCalendarDay(from, to);
  for (const item of scoped) {
    const createdAt = new Date(item.createdAt);
    if (Number.isNaN(createdAt.getTime())) continue;
    const label = hourly
      ? `${String(createdAt.getHours()).padStart(2, "0")}:00`
      : from.getFullYear() === to.getFullYear()
        ? `${createdAt.getMonth() + 1}/${createdAt.getDate()}`
        : `${createdAt.getFullYear()}/${createdAt.getMonth() + 1}/${createdAt.getDate()}`;
    const point = trend.find((candidate) => candidate.label === label);
    if (point) applyLedgerTransactionToPoint(point, item);
  }
}

function buildHotItems(
  names: string[],
  totalAmount: number,
  totalCount: number
): FinanceHotItem[] {
  const weights = names.map((_, index) => Math.max(1.2, names.length - index * 0.85));
  const weightSum = weights.reduce((sum, value) => sum + value, 0) || 1;
  return names
    .map((name, index) => {
      const ratio = weights[index]! / weightSum;
      const jitter = 0.82 + pseudoNoise(index * 17 + totalAmount) * 0.36;
      return {
        name,
        amount: money(totalAmount * ratio * jitter),
        count: Math.max(3, Math.round(totalCount * ratio * jitter)),
      };
    })
    .sort((left, right) => Number(right.amount) - Number(left.amount));
}

export async function getMockFinanceSummary(filter: {
  createdFrom?: string;
  createdTo?: string;
} = {}): Promise<FinanceSummary> {
  await simulateLatency(180);
  const { from, to } = resolveSummaryRange(filter);
  const trend = buildTrendSeries(from, to);
  const scoped = filterTransactions({
    createdFrom: from.toISOString(),
    createdTo: to.toISOString(),
  });
  mergeLedgerIntoTrend(trend, scoped, from, to);

  const totals = trend.reduce(
    (acc, point) => {
      acc.recharge += point.recharge;
      acc.spend += point.spend;
      acc.withdraw += point.withdraw;
      acc.refund += point.refund;
      acc.platformRevenue += point.platformRevenue;
      acc.accountRevenue += point.accountRevenue;
      return acc;
    },
    {
      recharge: 0,
      spend: 0,
      withdraw: 0,
      refund: 0,
      platformRevenue: 0,
      accountRevenue: 0,
    }
  );

  const hotProjects = buildHotItems(
    HOT_PROJECTS,
    Math.max(totals.spend, 2600),
    Math.max(scoped.length * 2, 180)
  ).slice(0, 10);
  const hotProducts = buildHotItems(
    HOT_PRODUCTS,
    Math.max(totals.spend * 0.92, 2200),
    Math.max(scoped.length * 3, 240)
  ).slice(0, 10);

  return {
    rechargeAmount: money(totals.recharge),
    spendAmount: money(totals.spend),
    withdrawAmount: money(totals.withdraw),
    refundAmount: money(totals.refund),
    platformRevenue: money(Math.max(0, totals.platformRevenue)),
    accountRevenue: money(Math.max(0, totals.accountRevenue)),
    trend,
    hotProjects,
    hotProducts,
  };
}
