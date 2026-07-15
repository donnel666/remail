// Mock data for the Admin User Management page UI review.
//
// The API surface mirrors docs/8-iam.md and api/openapi.yaml so swapping to the
// real backend later only needs to replace these imports with real API modules:
//   - GET   /v1/admin/users                          -> listMockAdminUsers
//   - PATCH /v1/admin/users/{userId}                 -> updateMockAdminUser
//   - POST  /v1/users                                -> createMockAdminUser
//   - POST  /v1/admin/users/{userId}/sessions/revoke -> revokeMockAdminUserSessions
//   - PATCH /v1/password (admin-initiated reset)     -> resetMockAdminUserPassword
//   - GET   /v1/admin/users/groups                   -> listMockUserGroups
//   - GET   /v1/admin/permissions                    -> getMockPermissionCatalog
//   - GET   /v1/admin/users/{userId}/permissions     -> getMockAdminUserPermissions
//   - PUT   /v1/admin/users/{userId}/permissions     -> putMockAdminUserPermissions
//   - GET   /v1/wallet (per user)                    -> getMockAdminUserWallet
//   - POST  /v1/admin/wallets/{userId}/credit        -> creditMockAdminUserWallet
//   - POST  /v1/admin/wallets/{userId}/debit         -> debitMockAdminUserWallet
//   - GET   /v1/wallet/transactions (per user)       -> listMockAdminUserTransactions
//   - GET/POST/PATCH/DELETE /v1/apikeys (per user)   -> *MockAdminUserApiKey*
//
// The list response adds `facets` and a denormalized `consumerBalance` as
// mock-only conveniences for the tabs, filters and balance column; the real
// AdminUserListResponse only returns { users, total, offset, limit }.

import { IamApiError } from "@/lib/api-client";

export type AdminUserRole = "user" | "supplier" | "admin" | "super_admin";
export type AdminUserRoleFilter = "all" | AdminUserRole;
export type AdminUserStatusFilter = "all" | "enabled" | "disabled";

export type AdminTransactionType =
  | "recharge"
  | "debit"
  | "refund"
  | "freeze"
  | "credit"
  | "withdrawal"
  | "manual_adjustment"
  | "card_redeem"
  | "transfer";

export type AdminTransactionBucket =
  | "consumer"
  | "supplier_available"
  | "supplier_frozen";

export type PermissionEffect = "allow" | "deny";

export interface AdminUserGroup {
  id: number;
  code: string;
  name: string;
  description: string;
  enabled: boolean;
}

export interface AdminUser {
  id: number;
  email: string;
  nickname: string;
  role: AdminUserRole;
  userGroup: AdminUserGroup;
  permissions?: string[];
  enabled: boolean;
  createdAt: string;
  updatedAt: string;
  lastLoginAt?: string | null;
  // Mock-only convenience so the list can render a balance column without an
  // extra wallet request per row.
  consumerBalance: string;
}

export interface AdminWallet {
  userId: number;
  consumerBalance: string;
  supplierAvailable: string;
  supplierFrozen: string;
  historicalSpend: string;
  orderCount: number;
  updatedAt: string;
}

export interface AdminUserInvitationMember {
  id: number;
  email: string;
  nickname: string;
  role: AdminUserRole;
  enabled: boolean;
  joinedAt: string;
}

export interface AdminUserInvitationOverview {
  inviter: AdminUserInvitationMember | null;
  invitees: AdminUserInvitationMember[];
}

export interface AdminTransaction {
  id: number;
  transactionNo: string;
  userId: number;
  transactionType: AdminTransactionType;
  balanceBucket: AdminTransactionBucket;
  direction: "in" | "out";
  amount: string;
  balanceBefore: string;
  balanceAfter: string;
  bizType: string;
  bizId: string;
  createdAt: string;
}

export interface AdminApiKey {
  id: number;
  name: string;
  keyPrefix: string;
  keyPlain?: string;
  enabled: boolean;
  rateLimitPerMinute: number | null;
  concurrencyLimit: number;
  quotaLimit?: number | null;
  quotaUsed: number;
  remainingQuota?: number | null;
  activeRequests: number;
  expireAt?: string | null;
  lastUsedAt?: string | null;
  createdAt: string;
  updatedAt: string;
}

export interface PermissionCatalogItem {
  resource: string;
  actions: string[];
}

export interface PermissionPolicy {
  resource: string;
  action: string;
  effect: PermissionEffect;
}

export interface AdminUserListFilter {
  search?: string;
  role?: AdminUserRole;
  enabled?: boolean;
  userGroupId?: number;
  createdFrom?: string;
  createdTo?: string;
}

export interface AdminUserFacets {
  role: Record<AdminUserRoleFilter, number>;
  status: { all: number; enabled: number; disabled: number };
  group: { id: number; code: string; name: string; count: number }[];
}

export interface AdminUserListResult {
  users: AdminUser[];
  total: number;
  offset: number;
  limit: number;
  facets: AdminUserFacets;
}

export interface AdminUserBulkResult {
  affected: number;
  skipped: number;
}

export interface AdminWalletAdjustmentResult {
  wallet: AdminWallet;
  transaction: AdminTransaction;
}

export interface CreateAdminUserInput {
  email: string;
  nickname?: string;
  password: string;
  role: AdminUserRole;
  userGroupId: number;
}

export interface UpdateAdminUserInput {
  email?: string;
  nickname?: string;
  password?: string;
  role?: AdminUserRole;
  userGroupId?: number;
  enabled?: boolean;
}

export interface AdminApiKeyInput {
  name?: string;
  enabled?: boolean;
  expireAt?: string | null;
  rateLimitPerMinute?: number | null;
  concurrencyLimit?: number;
  quotaLimit?: number | null;
}

const HOUR = 3_600_000;
const DAY = 24 * HOUR;

// Deterministic PRNG so pagination blocks and seeded data stay stable.
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

function randomInt(min: number, max: number) {
  return min + Math.floor(random() * (max - min + 1));
}

function hex(length: number) {
  let value = "";
  for (let index = 0; index < length; index += 1) {
    value += "0123456789abcdef"[Math.floor(random() * 16)];
  }
  return value;
}

function money(value: number) {
  return (Math.round(value * 100) / 100).toFixed(2);
}

function simulateLatency(base = 220) {
  return new Promise((resolve) =>
    globalThis.setTimeout(resolve, base + Math.random() * 240)
  );
}

export const USER_GROUPS: AdminUserGroup[] = [
  {
    id: 1,
    code: "normal",
    name: "普通用户",
    description: "默认权益分组",
    enabled: true,
  },
  {
    id: 2,
    code: "VIP1",
    name: "VIP1",
    description: "享受基础折扣与优先库存",
    enabled: true,
  },
  {
    id: 3,
    code: "VIP2",
    name: "VIP2",
    description: "享受更高折扣、专属额度与批量优惠",
    enabled: true,
  },
  {
    id: 4,
    code: "partner",
    name: "渠道合作",
    description: "渠道合作方专属分组",
    enabled: true,
  },
];

function groupById(id: number): AdminUserGroup {
  return USER_GROUPS.find((group) => group.id === id) ?? USER_GROUPS[0];
}

export const PERMISSION_CATALOG: PermissionCatalogItem[] = [
  { resource: "iam:user", actions: ["read", "write", "operate"] },
  { resource: "iam:permission", actions: ["read", "write", "sensitive"] },
  { resource: "iam:invite", actions: ["read", "write"] },
  { resource: "core:resource", actions: ["read", "write", "operate"] },
  { resource: "core:project", actions: ["read", "write", "operate"] },
  { resource: "trade:order", actions: ["read", "write", "operate"] },
  { resource: "billing:wallet", actions: ["read", "operate", "sensitive"] },
  { resource: "billing:card", actions: ["read", "write", "sensitive"] },
  { resource: "proxy:proxy", actions: ["read", "write", "operate"] },
];

const ROLE_BASELINE: Record<AdminUserRole, string[]> = {
  user: [],
  supplier: ["core:resource:read", "core:resource:write", "core:resource:operate"],
  admin: [
    "iam:user:read",
    "iam:user:write",
    "iam:invite:read",
    "iam:invite:write",
    "core:resource:read",
    "core:resource:write",
    "core:resource:operate",
    "core:project:read",
    "core:project:write",
    "core:project:operate",
    "trade:order:read",
    "trade:order:write",
    "trade:order:operate",
    "billing:wallet:read",
    "billing:wallet:operate",
    "billing:card:read",
    "billing:card:write",
    "proxy:proxy:read",
    "proxy:proxy:write",
    "proxy:proxy:operate",
  ],
  super_admin: PERMISSION_CATALOG.flatMap((item) =>
    item.actions.map((action) => `${item.resource}:${action}`)
  ),
};

export function roleBaselinePermissions(role: AdminUserRole): string[] {
  return ROLE_BASELINE[role] ?? [];
}

const CLEAN_NICKNAMES = [
  "林晚舟", "苏挽星", "陈屿", "顾南央", "沈流苏", "江照白", "温故知新",
  "萧然", "白鹿鸣", "路知远", "叶知秋", "闻鹊", "云归", "程野",
  "夏未央", "许清欢", "宋亦寒", "简凛", "楚河", "洛清岚",
  "秦时明", "周慕云", "郑一鸣", "王砚舟", "赵向晚",
];

const LOCAL_HEADS = [
  "nova", "kite", "lumen", "orbit", "pine", "quartz", "raven", "sable",
  "tidal", "umber", "vesper", "willow", "zephyr", "cobalt", "ember", "flint",
  "aster", "briar", "cove", "delta", "echo", "fable", "grove", "haven",
];
const DOMAINS = [
  "outlook.com", "hotmail.com", "gmail.com", "starmail.top", "mailhub.vip",
  "qq.com", "163.com", "proton.me",
];

const API_KEY_NAMES = [
  "默认密钥", "接码脚本", "生产环境", "测试环境", "监控采集", "备用密钥",
  "批量下单", "数据同步",
];

const ADJUST_REASONS = ["活动补偿", "客服补发", "异常扣款回冲", "批量赠送"];

let sequence = 1000;
function nextId() {
  sequence += 1;
  return sequence;
}

function buildApiKeys(now: number, count: number): AdminApiKey[] {
  const keys: AdminApiKey[] = [];
  for (let index = 0; index < count; index += 1) {
    const created = now - randomInt(1, 180) * DAY;
    const hasQuota = random() < 0.6;
    const quotaLimit = hasQuota ? randomInt(5, 200) * 1000 : null;
    const quotaUsed = hasQuota
      ? randomInt(0, quotaLimit ?? 0)
      : randomInt(0, 40000);
    const enabled = random() < 0.82;
    const hasExpiry = random() < 0.5;
    keys.push({
      id: nextId(),
      name: `${pick(API_KEY_NAMES)}`,
      keyPrefix: `sk-${hex(6)}`,
      keyPlain: `sk-${hex(6)}${hex(26)}`,
      enabled,
      rateLimitPerMinute: random() < 0.5 ? randomInt(1, 12) * 60 : null,
      concurrencyLimit: pick([1, 5, 5, 10, 20]),
      quotaLimit,
      quotaUsed,
      remainingQuota: quotaLimit == null ? null : Math.max(0, quotaLimit - quotaUsed),
      activeRequests: enabled ? randomInt(0, 3) : 0,
      expireAt: hasExpiry
        ? new Date(created + randomInt(30, 400) * DAY).toISOString()
        : null,
      lastUsedAt:
        random() < 0.85
          ? new Date(now - randomInt(1, 240) * HOUR).toISOString()
          : null,
      createdAt: new Date(created).toISOString(),
      updatedAt: new Date(created + randomInt(0, 40) * DAY).toISOString(),
    });
  }
  return keys.sort(
    (left, right) =>
      new Date(right.createdAt).getTime() - new Date(left.createdAt).getTime()
  );
}

function buildTransactions(
  userId: number,
  now: number,
  balance: number,
  count: number
): AdminTransaction[] {
  const transactions: AdminTransaction[] = [];
  let runningBalance = balance;
  let cursor = now - randomInt(1, 20) * HOUR;
  for (let index = 0; index < count; index += 1) {
    const type = pick<AdminTransactionType>([
      "recharge",
      "debit",
      "debit",
      "refund",
      "card_redeem",
      "manual_adjustment",
    ]);
    const direction: "in" | "out" =
      type === "debit" || type === "withdrawal" || type === "freeze"
        ? "out"
        : type === "manual_adjustment"
          ? random() < 0.5
            ? "in"
            : "out"
          : "in";
    const amountValue =
      type === "debit"
        ? randomInt(1, 50) / 10
        : type === "recharge" || type === "card_redeem"
          ? randomInt(5, 200)
          : randomInt(1, 80);
    const balanceAfter = runningBalance;
    const balanceBefore =
      direction === "in"
        ? Math.max(0, runningBalance - amountValue)
        : runningBalance + amountValue;
    runningBalance = balanceBefore;
    transactions.push({
      id: nextId(),
      transactionNo: `TX${Math.floor(cursor).toString(16).toUpperCase().padStart(12, "0")}${hex(6).toUpperCase()}`,
      userId,
      transactionType: type,
      balanceBucket: "consumer",
      direction,
      amount: money(amountValue),
      balanceBefore: money(Math.max(0, balanceBefore)),
      balanceAfter: money(Math.max(0, balanceAfter)),
      bizType: type,
      bizId: type === "manual_adjustment" ? pick(ADJUST_REASONS) : `${hex(8)}`,
      createdAt: new Date(cursor).toISOString(),
    });
    cursor -= randomInt(2, 72) * HOUR;
  }
  return transactions;
}

interface UserRecord {
  user: AdminUser;
  wallet: AdminWallet;
  inviterId: number | null;
  transactions: AdminTransaction[];
  apiKeys: AdminApiKey[];
  overrides: PermissionPolicy[];
  password: string;
  tokenVersion: number;
}

interface SeedSpec {
  role: AdminUserRole;
  count: number;
  enabledRatio: number;
}

const SEED_SPECS: SeedSpec[] = [
  { role: "super_admin", count: 1, enabledRatio: 1 },
  { role: "admin", count: 4, enabledRatio: 1 },
  { role: "supplier", count: 12, enabledRatio: 0.92 },
  { role: "user", count: 66, enabledRatio: 0.85 },
];

function buildUserRecord(id: number, role: AdminUserRole, enabled: boolean, now: number): UserRecord {
  const head = pick(LOCAL_HEADS);
  const email = `${head}${randomInt(10, 9999)}@${pick(DOMAINS)}`;
  const createdAt = now - randomInt(1, 540) * DAY;
  const group =
    role === "super_admin" || role === "admin"
      ? USER_GROUPS[0]
      : groupById(
          pick([1, 1, 1, 2, 2, 3, 4])
        );
  const consumerBalanceValue =
    role === "supplier" ? randomInt(0, 400) + random() : randomInt(0, 900) + random();
  const supplierAvailableValue =
    role !== "user" ? randomInt(0, 2000) + random() : 0;
  const supplierFrozenValue =
    role !== "user" ? randomInt(0, 300) + random() : 0;
  const historicalSpendValue = randomInt(0, 5000) + random();
  const orderCount = randomInt(0, 800);

  const user: AdminUser = {
    id,
    email,
    nickname: pick(CLEAN_NICKNAMES),
    role,
    userGroup: group,
    permissions: roleBaselinePermissions(role),
    enabled,
    createdAt: new Date(createdAt).toISOString(),
    updatedAt: new Date(createdAt + randomInt(0, 60) * DAY).toISOString(),
    lastLoginAt:
      random() < 0.9
        ? new Date(now - randomInt(1, 30) * DAY).toISOString()
        : null,
    consumerBalance: money(consumerBalanceValue),
  };

  const wallet: AdminWallet = {
    userId: id,
    consumerBalance: money(consumerBalanceValue),
    supplierAvailable: money(supplierAvailableValue),
    supplierFrozen: money(supplierFrozenValue),
    historicalSpend: money(historicalSpendValue),
    orderCount,
    updatedAt: new Date(now - randomInt(1, 48) * HOUR).toISOString(),
  };

  return {
    user,
    wallet,
    inviterId: null,
    transactions: buildTransactions(id, now, consumerBalanceValue, randomInt(4, 26)),
    apiKeys: buildApiKeys(now, randomInt(0, 4)),
    overrides:
      random() < 0.15
        ? [
            {
              resource: pick(PERMISSION_CATALOG).resource,
              action: "read",
              effect: random() < 0.5 ? "allow" : "deny",
            },
          ]
        : [],
    password: "",
    tokenVersion: randomInt(1, 8),
  };
}

function buildDataset(): UserRecord[] {
  const now = Date.now();
  const records: UserRecord[] = [];
  let id = 1;
  for (const spec of SEED_SPECS) {
    for (let index = 0; index < spec.count; index += 1) {
      const enabled = index === 0 ? true : random() < spec.enabledRatio;
      records.push(buildUserRecord(id, spec.role, enabled, now));
      id += 1;
    }
  }
  for (const record of records) {
    if (record.user.id === 1) {
      record.inviterId = null;
    } else if (record.user.role === "admin") {
      record.inviterId = 1;
    } else if (record.user.role === "supplier") {
      record.inviterId = 2 + ((record.user.id - 6) % 4);
    } else {
      record.inviterId = 6 + ((record.user.id - 18) % 12);
    }
  }
  // Pin a couple of well-known accounts to the top for demos.
  records[0].user.email = "root@remail.local";
  records[0].user.nickname = "超级管理员";
  records[0].user.lastLoginAt = new Date(now - 2 * HOUR).toISOString();
  records.sort(
    (left, right) =>
      new Date(right.user.createdAt).getTime() -
      new Date(left.user.createdAt).getTime()
  );
  return records;
}

const dataset = buildDataset();

function findRecord(userId: number): UserRecord {
  const record = dataset.find((item) => item.user.id === userId);
  if (!record) {
    throw new IamApiError(404, { message: "User not found." });
  }
  return record;
}

function matchesFilter(user: AdminUser, filter: AdminUserListFilter) {
  if (filter.role && user.role !== filter.role) return false;
  if (filter.enabled !== undefined && user.enabled !== filter.enabled) {
    return false;
  }
  if (filter.userGroupId && user.userGroup.id !== filter.userGroupId) {
    return false;
  }
  if (filter.createdFrom && new Date(user.createdAt) < new Date(filter.createdFrom)) {
    return false;
  }
  if (filter.createdTo && new Date(user.createdAt) > new Date(filter.createdTo)) {
    return false;
  }
  const search = filter.search?.trim().toLowerCase();
  if (search) {
    const haystack = [
      user.email,
      user.nickname,
      String(user.id),
      user.userGroup.name,
    ]
      .join(" ")
      .toLowerCase();
    if (!haystack.includes(search)) return false;
  }
  return true;
}

const ROLE_KEYS: AdminUserRole[] = ["user", "supplier", "admin", "super_admin"];

function buildFacets(users: AdminUser[]): AdminUserFacets {
  const role = { all: users.length } as AdminUserFacets["role"];
  for (const key of ROLE_KEYS) role[key] = 0;
  const status = { all: users.length, enabled: 0, disabled: 0 };
  const groupCounts = new Map<number, number>();
  for (const user of users) {
    role[user.role] += 1;
    if (user.enabled) status.enabled += 1;
    else status.disabled += 1;
    groupCounts.set(
      user.userGroup.id,
      (groupCounts.get(user.userGroup.id) ?? 0) + 1
    );
  }
  return {
    role,
    status,
    group: USER_GROUPS.map((group) => ({
      id: group.id,
      code: group.code,
      name: group.name,
      count: groupCounts.get(group.id) ?? 0,
    })),
  };
}

function buildFilteredFacets(
  users: AdminUser[],
  filter: AdminUserListFilter
): AdminUserFacets {
  const roleFilter = { ...filter };
  delete roleFilter.role;
  const statusFilter = { ...filter };
  delete statusFilter.enabled;
  const groupFilter = { ...filter };
  delete groupFilter.userGroupId;

  const roleFacets = buildFacets(
    users.filter((user) => matchesFilter(user, roleFilter))
  );
  const statusFacets = buildFacets(
    users.filter((user) => matchesFilter(user, statusFilter))
  );
  const groupFacets = buildFacets(
    users.filter((user) => matchesFilter(user, groupFilter))
  );

  return {
    role: roleFacets.role,
    status: statusFacets.status,
    group: groupFacets.group,
  };
}

function cloneUser(user: AdminUser): AdminUser {
  return { ...user, userGroup: { ...user.userGroup }, permissions: [...(user.permissions ?? [])] };
}

function cloneWallet(wallet: AdminWallet): AdminWallet {
  return { ...wallet };
}

function toInvitationMember(record: UserRecord): AdminUserInvitationMember {
  return {
    id: record.user.id,
    email: record.user.email,
    nickname: record.user.nickname,
    role: record.user.role,
    enabled: record.user.enabled,
    joinedAt: record.user.createdAt,
  };
}

function buildSyntheticInvitees(record: UserRecord) {
  const count = 8 + (record.user.id % 13);
  const createdAt = new Date(record.user.createdAt).getTime();
  const now = Date.now();
  const availableRange = Math.max(DAY, now - createdAt);
  return Array.from({ length: count }, (_, index) => {
    const sequence = index + 1;
    return {
      id: 100_000 + record.user.id * 100 + sequence,
      email: `invitee${record.user.id}-${sequence}@mailhub.vip`,
      nickname: `受邀用户 ${sequence}`,
      role: sequence % 7 === 0 ? "supplier" : "user",
      enabled: sequence % 9 !== 0,
      joinedAt: new Date(
        createdAt + (availableRange * sequence) / (count + 1)
      ).toISOString(),
    } satisfies AdminUserInvitationMember;
  });
}

function cloneApiKey(key: AdminApiKey): AdminApiKey {
  return { ...key };
}

export async function listMockAdminUsers(
  filter: AdminUserListFilter,
  offset: number,
  limit: number
): Promise<AdminUserListResult> {
  await simulateLatency();
  const allUsers = dataset.map((record) => record.user);
  const filtered = allUsers.filter((user) => matchesFilter(user, filter));
  const users = filtered.slice(offset, offset + limit).map(cloneUser);
  return {
    users,
    total: filtered.length,
    offset,
    limit,
    facets: buildFilteredFacets(allUsers, filter),
  };
}

export async function getMockAdminUser(userId: number): Promise<AdminUser> {
  await simulateLatency(140);
  return cloneUser(findRecord(userId).user);
}

export async function getMockAdminUserInvitations(
  userId: number
): Promise<AdminUserInvitationOverview> {
  await simulateLatency(160);
  const record = findRecord(userId);
  const inviter = record.inviterId
    ? dataset.find((item) => item.user.id === record.inviterId)
    : null;
  const directInvitees = dataset
    .filter((item) => item.inviterId === userId)
    .sort(
      (left, right) =>
        new Date(right.user.createdAt).getTime() -
        new Date(left.user.createdAt).getTime()
    )
    .map(toInvitationMember);
  return {
    inviter: inviter ? toInvitationMember(inviter) : null,
    invitees:
      directInvitees.length > 0
        ? directInvitees
        : buildSyntheticInvitees(record),
  };
}

export async function createMockAdminUser(
  input: CreateAdminUserInput
): Promise<AdminUser> {
  await simulateLatency();
  const email = input.email.trim().toLowerCase();
  if (!email) {
    throw new IamApiError(400, { message: "Please enter your email." });
  }
  if (!/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(email)) {
    throw new IamApiError(422, { message: "Please enter a valid email address." });
  }
  if (dataset.some((record) => record.user.email.toLowerCase() === email)) {
    throw new IamApiError(422, { message: "Email already exists." });
  }
  if (!input.password || input.password.length < 6) {
    throw new IamApiError(422, { message: "Password must be at least 6 characters." });
  }
  const now = Date.now();
  const id = Math.max(...dataset.map((record) => record.user.id)) + 1;
  const group = groupById(input.userGroupId);
  const user: AdminUser = {
    id,
    email,
    nickname: input.nickname?.trim() || email.split("@")[0],
    role: input.role,
    userGroup: group,
    permissions: roleBaselinePermissions(input.role),
    enabled: true,
    createdAt: new Date(now).toISOString(),
    updatedAt: new Date(now).toISOString(),
    lastLoginAt: null,
    consumerBalance: money(0),
  };
  const record: UserRecord = {
    user,
    wallet: {
      userId: id,
      consumerBalance: money(0),
      supplierAvailable: money(0),
      supplierFrozen: money(0),
      historicalSpend: money(0),
      orderCount: 0,
      updatedAt: new Date(now).toISOString(),
    },
    inviterId: null,
    transactions: [],
    apiKeys: [],
    overrides: [],
    password: input.password,
    tokenVersion: 1,
  };
  dataset.unshift(record);
  return cloneUser(user);
}

export async function updateMockAdminUser(
  userId: number,
  input: UpdateAdminUserInput
): Promise<AdminUser> {
  await simulateLatency(200);
  const record = findRecord(userId);
  const email = input.email?.trim().toLowerCase();
  if (
    record.user.role === "super_admin" &&
    ((email && email !== record.user.email.toLowerCase()) ||
      input.password !== undefined)
  ) {
    throw new IamApiError(422, {
      message: "Super administrator protected.",
    });
  }
  if (input.email !== undefined) {
    if (!email || !/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(email)) {
      throw new IamApiError(422, {
        message: "Please enter a valid email address.",
      });
    }
    if (
      dataset.some(
        (item) =>
          item.user.id !== userId &&
          item.user.email.toLowerCase() === email
      )
    ) {
      throw new IamApiError(422, { message: "Email already exists." });
    }
  }
  if (input.password !== undefined && input.password.length < 6) {
    throw new IamApiError(422, {
      message: "Password must be at least 6 characters.",
    });
  }
  if (email) record.user.email = email;
  if (input.nickname !== undefined) record.user.nickname = input.nickname.trim();
  if (input.password !== undefined) {
    record.password = input.password;
    record.tokenVersion += 1;
  }
  if (input.role !== undefined) {
    record.user.role = input.role;
    record.user.permissions = mergePermissions(
      roleBaselinePermissions(input.role),
      record.overrides
    );
  }
  if (input.userGroupId !== undefined) {
    record.user.userGroup = groupById(input.userGroupId);
  }
  if (input.enabled !== undefined) {
    record.user.enabled = input.enabled;
    if (!input.enabled) record.tokenVersion += 1;
  }
  record.user.updatedAt = new Date().toISOString();
  return cloneUser(record.user);
}

export async function deleteMockAdminUser(userId: number): Promise<void> {
  await simulateLatency(200);
  const record = findRecord(userId);
  if (record.user.role === "super_admin") {
    throw new IamApiError(422, {
      message: "Super administrators cannot be deleted.",
    });
  }
  const index = dataset.findIndex((item) => item.user.id === userId);
  if (index >= 0) dataset.splice(index, 1);
}

export async function revokeMockAdminUserSessions(userId: number): Promise<void> {
  await simulateLatency(180);
  const record = findRecord(userId);
  record.tokenVersion += 1;
}

export async function setMockAdminUsersEnabled(
  userIds: number[],
  enabled: boolean
): Promise<AdminUserBulkResult> {
  await simulateLatency(240);
  const ids = new Set(userIds.filter((id) => id > 0));
  let affected = 0;
  let skipped = 0;
  for (const record of dataset) {
    if (!ids.has(record.user.id)) continue;
    if (record.user.role === "super_admin") {
      skipped += 1;
      continue;
    }
    if (record.user.enabled !== enabled) {
      record.user.enabled = enabled;
      record.user.updatedAt = new Date().toISOString();
      if (!enabled) record.tokenVersion += 1;
      affected += 1;
    } else {
      skipped += 1;
    }
  }
  return { affected, skipped };
}

export async function revokeMockAdminUsersSessions(
  userIds: number[]
): Promise<AdminUserBulkResult> {
  await simulateLatency(240);
  const ids = new Set(userIds.filter((id) => id > 0));
  let affected = 0;
  let skipped = 0;
  for (const record of dataset) {
    if (!ids.has(record.user.id)) continue;
    if (record.user.role === "super_admin") {
      skipped += 1;
      continue;
    }
    record.tokenVersion += 1;
    affected += 1;
  }
  return { affected, skipped };
}

export async function deleteMockAdminUsers(
  userIds: number[]
): Promise<AdminUserBulkResult> {
  await simulateLatency(260);
  const ids = new Set(userIds.filter((id) => id > 0));
  let affected = 0;
  let skipped = 0;
  for (let index = dataset.length - 1; index >= 0; index -= 1) {
    const record = dataset[index];
    if (!ids.has(record.user.id)) continue;
    if (record.user.role === "super_admin") {
      skipped += 1;
      continue;
    }
    dataset.splice(index, 1);
    affected += 1;
  }
  return { affected, skipped };
}

export async function resetMockAdminUserPassword(
  userId: number,
  newPassword: string
): Promise<void> {
  await simulateLatency(200);
  const record = findRecord(userId);
  if (!newPassword || newPassword.length < 6) {
    throw new IamApiError(422, {
      message: "Password must be at least 6 characters.",
    });
  }
  record.password = newPassword;
  record.tokenVersion += 1;
}

export async function listMockUserGroups(): Promise<AdminUserGroup[]> {
  await simulateLatency(120);
  return USER_GROUPS.map((group) => ({ ...group }));
}

// --- Wallet ---

export async function getMockAdminUserWallet(userId: number): Promise<AdminWallet> {
  await simulateLatency(160);
  return cloneWallet(findRecord(userId).wallet);
}

function adjustWallet(
  userId: number,
  direction: "in" | "out",
  amount: string,
  reason: string
): AdminWalletAdjustmentResult {
  const record = findRecord(userId);
  const value = Number(amount);
  if (!Number.isFinite(value) || value <= 0) {
    throw new IamApiError(422, { message: "Amount must be a positive number." });
  }
  if (!reason.trim()) {
    throw new IamApiError(422, { message: "Reason is required." });
  }
  const before = Number(record.wallet.consumerBalance);
  if (direction === "out" && value > before) {
    throw new IamApiError(422, { message: "Insufficient balance." });
  }
  const after = direction === "in" ? before + value : before - value;
  const now = Date.now();
  record.wallet.consumerBalance = money(after);
  record.wallet.updatedAt = new Date(now).toISOString();
  record.user.consumerBalance = money(after);
  record.user.updatedAt = new Date(now).toISOString();
  const transaction: AdminTransaction = {
    id: nextId(),
    transactionNo: `TX${Math.floor(now).toString(16).toUpperCase().padStart(12, "0")}${hex(6).toUpperCase()}`,
    userId,
    transactionType: "manual_adjustment",
    balanceBucket: "consumer",
    direction,
    amount: money(value),
    balanceBefore: money(before),
    balanceAfter: money(after),
    bizType: "manual_adjustment",
    bizId: reason.trim(),
    createdAt: new Date(now).toISOString(),
  };
  record.transactions.unshift(transaction);
  return {
    wallet: cloneWallet(record.wallet),
    transaction: { ...transaction },
  };
}

export async function creditMockAdminUserWallet(
  userId: number,
  amount: string,
  reason: string
): Promise<AdminWalletAdjustmentResult> {
  await simulateLatency(220);
  return adjustWallet(userId, "in", amount, reason);
}

export async function debitMockAdminUserWallet(
  userId: number,
  amount: string,
  reason: string
): Promise<AdminWalletAdjustmentResult> {
  await simulateLatency(220);
  return adjustWallet(userId, "out", amount, reason);
}

export async function adjustMockAdminUsersWallet(
  userIds: number[],
  signedAmount: number,
  reason: string
): Promise<AdminUserBulkResult> {
  await simulateLatency(260);
  if (!Number.isFinite(signedAmount) || signedAmount === 0) {
    throw new IamApiError(422, { message: "Amount cannot be zero." });
  }
  if (!reason.trim()) {
    throw new IamApiError(422, { message: "Reason is required." });
  }
  const ids = new Set(userIds.filter((id) => id > 0));
  let affected = 0;
  let skipped = 0;
  for (const record of dataset) {
    if (!ids.has(record.user.id)) continue;
    if (
      record.user.role === "super_admin" ||
      (signedAmount < 0 &&
        Math.abs(signedAmount) > Number(record.wallet.consumerBalance))
    ) {
      skipped += 1;
      continue;
    }
    adjustWallet(
      record.user.id,
      signedAmount > 0 ? "in" : "out",
      Math.abs(signedAmount).toFixed(2),
      reason.trim()
    );
    affected += 1;
  }
  return { affected, skipped };
}

export interface AdminTransactionPage {
  items: AdminTransaction[];
  nextAfterId?: number;
  hasNext: boolean;
  limit: number;
}

export async function listMockAdminUserTransactions(
  userId: number,
  afterId: number | undefined,
  limit = 20
): Promise<AdminTransactionPage> {
  await simulateLatency(180);
  const record = findRecord(userId);
  const sorted = [...record.transactions].sort((left, right) => right.id - left.id);
  const startIndex = afterId
    ? sorted.findIndex((item) => item.id < afterId)
    : 0;
  const safeStart = startIndex < 0 ? sorted.length : startIndex;
  const items = sorted.slice(safeStart, safeStart + limit).map((item) => ({ ...item }));
  const lastItem = items[items.length - 1];
  const hasNext = Boolean(lastItem) && safeStart + items.length < sorted.length;
  return {
    items,
    nextAfterId: hasNext ? lastItem.id : undefined,
    hasNext,
    limit,
  };
}

// --- API keys ---

export async function listMockAdminUserApiKeys(
  userId: number
): Promise<AdminApiKey[]> {
  await simulateLatency(180);
  return findRecord(userId).apiKeys.map(cloneApiKey);
}

export async function createMockAdminUserApiKey(
  userId: number,
  input: AdminApiKeyInput
): Promise<AdminApiKey> {
  await simulateLatency(220);
  const record = findRecord(userId);
  const now = Date.now();
  const quotaLimit = input.quotaLimit ?? null;
  const key: AdminApiKey = {
    id: nextId(),
    name: input.name?.trim() || "未命名密钥",
    keyPrefix: `sk-${hex(6)}`,
    keyPlain: `sk-${hex(6)}${hex(26)}`,
    enabled: input.enabled ?? true,
    rateLimitPerMinute: input.rateLimitPerMinute ?? null,
    concurrencyLimit: input.concurrencyLimit ?? 5,
    quotaLimit,
    quotaUsed: 0,
    remainingQuota: quotaLimit,
    activeRequests: 0,
    expireAt: input.expireAt ?? null,
    lastUsedAt: null,
    createdAt: new Date(now).toISOString(),
    updatedAt: new Date(now).toISOString(),
  };
  record.apiKeys.unshift(key);
  return cloneApiKey(key);
}

export async function updateMockAdminUserApiKey(
  userId: number,
  keyId: number,
  input: AdminApiKeyInput
): Promise<AdminApiKey> {
  await simulateLatency(200);
  const record = findRecord(userId);
  const key = record.apiKeys.find((item) => item.id === keyId);
  if (!key) {
    throw new IamApiError(404, { message: "Resource not found." });
  }
  if (input.name !== undefined) key.name = input.name.trim() || key.name;
  if (input.enabled !== undefined) key.enabled = input.enabled;
  if (input.expireAt !== undefined) key.expireAt = input.expireAt;
  if (input.rateLimitPerMinute !== undefined) {
    key.rateLimitPerMinute = input.rateLimitPerMinute;
  }
  if (input.concurrencyLimit !== undefined) {
    key.concurrencyLimit = input.concurrencyLimit;
  }
  if (input.quotaLimit !== undefined) {
    key.quotaLimit = input.quotaLimit;
    key.remainingQuota =
      input.quotaLimit == null
        ? null
        : Math.max(0, input.quotaLimit - key.quotaUsed);
  }
  key.updatedAt = new Date().toISOString();
  return cloneApiKey(key);
}

export async function deleteMockAdminUserApiKey(
  userId: number,
  keyId: number
): Promise<void> {
  await simulateLatency(180);
  const record = findRecord(userId);
  const index = record.apiKeys.findIndex((item) => item.id === keyId);
  if (index >= 0) record.apiKeys.splice(index, 1);
}

// --- Permissions ---

function mergePermissions(baseline: string[], overrides: PermissionPolicy[]): string[] {
  const set = new Set(baseline);
  for (const policy of overrides) {
    const key = `${policy.resource}:${policy.action}`;
    if (policy.effect === "allow") set.add(key);
    else set.delete(key);
  }
  return Array.from(set).sort();
}

export async function getMockPermissionCatalog(): Promise<PermissionCatalogItem[]> {
  await simulateLatency(120);
  return PERMISSION_CATALOG.map((item) => ({
    resource: item.resource,
    actions: [...item.actions],
  }));
}

export async function getMockAdminUserPermissions(
  userId: number
): Promise<PermissionPolicy[]> {
  await simulateLatency(160);
  return findRecord(userId).overrides.map((policy) => ({ ...policy }));
}

export async function putMockAdminUserPermissions(
  userId: number,
  policies: PermissionPolicy[]
): Promise<void> {
  await simulateLatency(220);
  const record = findRecord(userId);
  record.overrides = policies.map((policy) => ({ ...policy }));
  record.user.permissions = mergePermissions(
    roleBaselinePermissions(record.user.role),
    record.overrides
  );
  record.user.updatedAt = new Date().toISOString();
}
