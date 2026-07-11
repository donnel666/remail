// Typed in-memory mock for the site-wide Microsoft resource administration page.
//
// The mock intentionally mirrors the future admin API surface. Replacing this
// module with an HTTP adapter should not require changing the page DTOs:
//
//   listAdminMicrosoftResources             -> GET    /v1/admin/resources?type=microsoft
//   getAdminMicrosoftResourceDetail         -> GET    /v1/admin/resources/{resourceId}
//   listAdminMicrosoftOwners                -> GET    /v1/admin/users
//   importAdminMicrosoftResources           -> POST   /v1/admin/resources/imports
//   updateAdminMicrosoftResource            -> PATCH  /v1/admin/resources/{resourceId}
//   replaceAdminMicrosoftCredentials        -> PUT    /v1/admin/resources/{resourceId}/credentials
//   validateAdminMicrosoftResource          -> POST   /v1/admin/resources/{resourceId}/validate
//   validateAdminMicrosoftResourcesByIds    -> POST   /v1/admin/resources/validations
//   validateAdminMicrosoftResourcesByFilter -> POST   /v1/admin/resources/validations
//   disableAdminMicrosoftResourcesByIds     -> POST   /v1/admin/resources/disable
//   deleteAdminMicrosoftResource            -> DELETE /v1/admin/resources/{resourceId}
//   deleteAdminMicrosoftResourcesByIds      -> POST   /v1/admin/resources/delete
//   deleteAdminMicrosoftResourcesByFilter   -> POST   /v1/admin/resources/delete
//   recoverAdminMicrosoftResource           -> POST   /v1/admin/resources/{resourceId}/recover
//
// Security boundary: this file never stores or returns a password, client ID,
// refresh/access token, auxiliary-mailbox address/status, claim token, dispatch
// token, upstream response body, or object-storage key. Credential information
// is deliberately limited to configured/not-configured flags. All diagnostics
// are safe summaries suitable for an administrator UI and operation logs.

export type AdminMicrosoftResourceStatus =
  | "pending"
  | "normal"
  | "abnormal"
  | "disabled"
  | "deleted";

export type AdminMicrosoftTokenHealth =
  | "valid"
  | "expiring"
  | "expired"
  | "missing";

export type AdminMicrosoftOwnerRole =
  | "user"
  | "supplier"
  | "admin"
  | "super_admin";

export type AdminMicrosoftActiveTaskStatus = "queued" | "running" | "failed";
export type AdminMicrosoftTaskFilter =
  | "all"
  | "idle"
  | AdminMicrosoftActiveTaskStatus;

export interface AdminMicrosoftOwner {
  id: number;
  email: string;
  nickname: string;
  groupName: string;
  role: AdminMicrosoftOwnerRole;
  enabled: boolean;
}

export interface AdminMicrosoftAliasCounts {
  explicit: number;
  dot: number;
  plus: number;
}

export interface AdminMicrosoftResourceItem {
  id: number;
  emailAddress: string;
  suffix: string;
  // Auxiliary (recovery / binding) mailbox used for Microsoft verification codes.
  // Surfaced here by an explicit administrator decision to make it visible and
  // editable in the resource list and detail.
  bindingAddress?: string;
  ownerId: number;
  ownerEmail: string;
  ownerNickname: string;
  ownerGroupName: string;
  ownerRole: AdminMicrosoftOwnerRole;
  status: AdminMicrosoftResourceStatus;
  forSale: boolean;
  longLived: boolean;
  graphAvailable: boolean;
  qualityScore: number;
  rtExpireAt?: string;
  tokenHealth: AdminMicrosoftTokenHealth;
  aliasCounts: AdminMicrosoftAliasCounts;
  explicitAliasCount: number;
  dotAliasCount: number;
  plusAliasCount: number;
  activeTaskStatus?: AdminMicrosoftActiveTaskStatus;
  failedTaskCount: number;
  lastSafeError?: string;
  lastAllocatedAt?: string;
  createdAt: string;
  updatedAt: string;
}

export type AdminMicrosoftAliasKind = "explicit" | "dot" | "plus";
export type AdminMicrosoftAliasInventoryStatus =
  | "available"
  | "allocated"
  | "disabled";

export interface AdminMicrosoftAliasSample {
  id: number;
  kind: AdminMicrosoftAliasKind;
  emailAddress: string;
  status: AdminMicrosoftAliasInventoryStatus;
  createdAt: string;
  lastAllocatedAt?: string;
  // Allocation link: when the alias currently serves an order, the safe order /
  // project reference is surfaced so the admin can see alias-mailbox usage.
  orderNo?: string;
  projectName?: string;
}

export type AdminMicrosoftAliasScheduleStatus =
  | "pending"
  | "queued"
  | "running"
  | "paused";

export interface AdminMicrosoftAliasSchedule {
  id: number;
  status: AdminMicrosoftAliasScheduleStatus;
  weekCreated: number;
  weekLimit: number;
  yearCreated: number;
  yearLimit: number;
  attempts: number;
  failureStreak: number;
  nextRunAt?: string;
  lastDispatchedAt?: string;
  pauseReason?: string;
  lastSafeError?: string;
  updatedAt: string;
}

export type AdminMicrosoftAliasAttemptStatus =
  | "running"
  | "succeeded"
  | "failed"
  | "uncertain";

export interface AdminMicrosoftAliasAttempt {
  id: number;
  status: AdminMicrosoftAliasAttemptStatus;
  candidateEmail: string;
  requestId: string;
  attempts: number;
  maxAttempts: number;
  safeError?: string;
  startedAt: string;
  finishedAt?: string;
  updatedAt: string;
}

export type AdminMicrosoftAsyncTaskKind =
  | "validation"
  | "import"
  | "alias"
  | "token"
  | "fetch";
export type AdminMicrosoftAsyncTaskStatus =
  | "queued"
  | "running"
  | "succeeded"
  | "failed"
  | "uncertain";

export interface AdminMicrosoftAsyncTask {
  id: number;
  kind: AdminMicrosoftAsyncTaskKind;
  resourceId?: number;
  status: AdminMicrosoftAsyncTaskStatus;
  requestId: string;
  path: string;
  attempts: number;
  maxAttempts: number;
  safeError?: string;
  createdAt: string;
  queuedAt: string;
  startedAt?: string;
  finishedAt?: string;
  nextRunAt?: string;
  updatedAt: string;
}

export interface AdminMicrosoftCredentialConfiguration {
  passwordConfigured: boolean;
  clientIdConfigured: boolean;
  refreshTokenConfigured: boolean;
  revision: number;
  updatedAt: string;
}

export interface AdminMicrosoftTokenDiagnostic {
  health: AdminMicrosoftTokenHealth;
  rtExpireAt?: string;
  lastRefreshedAt?: string;
  scopes: string[];
  lastRefreshRequestId?: string;
  lastSafeError?: string;
}

export interface AdminMicrosoftResourceDetail
  extends AdminMicrosoftResourceItem {
  credentials: AdminMicrosoftCredentialConfiguration;
  token: AdminMicrosoftTokenDiagnostic;
  aliasSamples: {
    explicit: AdminMicrosoftAliasSample[];
    dot: AdminMicrosoftAliasSample[];
    plus: AdminMicrosoftAliasSample[];
  };
  aliasSchedule: AdminMicrosoftAliasSchedule;
  aliasAttempts: AdminMicrosoftAliasAttempt[];
  asyncTasks: {
    validations: AdminMicrosoftAsyncTask[];
    imports: AdminMicrosoftAsyncTask[];
    aliases: AdminMicrosoftAsyncTask[];
    tokens: AdminMicrosoftAsyncTask[];
    fetches: AdminMicrosoftAsyncTask[];
  };
  // BC-MAILMATCH read model: mail facts received by this resource's primary
  // mailbox and its aliases (GET /v1/admin/messages?emailResourceId=...).
  messages: AdminMicrosoftMessage[];
  // Verification mail delivered to the auxiliary (recovery / binding) mailbox.
  auxiliaryMessages: AdminMicrosoftMessage[];
  // BC-ALLOC read model: order/project allocations of this resource's primary
  // mailbox and aliases, joined with a safe order summary
  // (GET /v1/admin/allocations?resourceId=...).
  allocations: AdminMicrosoftAllocation[];
  usageSummary: AdminMicrosoftUsageSummary;
}

// ---------- Usage read models (mail facts + allocations) ----------
//
// These mirror facts owned by other bounded contexts (BC-MAILMATCH, BC-ALLOC),
// surfaced read-only on the admin resource detail so an administrator can answer
// "what mail did this mailbox receive" and "which projects/orders does it serve"
// without leaving the resource page. They never expose credentials, tokens or
// upstream payloads.

export type AdminMicrosoftMailboxKind = "main" | "alias" | "dot" | "plus";
export type AdminMicrosoftMessageStatus = "received" | "matched" | "ignored";

export interface AdminMicrosoftMessage {
  id: number;
  mailbox: AdminMicrosoftMailboxKind;
  recipient: string;
  sender: string;
  subject: string;
  preview: string;
  body: string;
  verificationCode?: string;
  status: AdminMicrosoftMessageStatus;
  matchDiagnostic?: string;
  orderNo?: string;
  receivedAt: string;
}

export type AdminMicrosoftSupplyScope = "owned" | "public";
export type AdminMicrosoftAllocationStatus = "allocated" | "released";
export type AdminMicrosoftServiceMode = "purchase" | "code";
export type AdminMicrosoftOrderStatus =
  | "pending_payment"
  | "paid"
  | "active"
  | "completed"
  | "refunded"
  | "failed"
  | "closed";

export interface AdminMicrosoftAllocation {
  id: number;
  orderNo: string;
  projectId: string;
  projectName: string;
  projectLogoUrl?: string;
  mailbox: AdminMicrosoftMailboxKind;
  supplyScope: AdminMicrosoftSupplyScope;
  deliveryEmail: string;
  status: AdminMicrosoftAllocationStatus;
  serviceMode: AdminMicrosoftServiceMode;
  orderStatus: AdminMicrosoftOrderStatus;
  payAmount: string;
  buyerEmail: string;
  mailCount: number;
  verificationCode?: string;
  createdAt: string;
  receiveUntil?: string;
  releasedAt?: string;
}

export interface AdminMicrosoftUsageProject {
  id: string;
  name: string;
  orderCount: number;
}

export interface AdminMicrosoftUsageSummary {
  activeAllocations: number;
  totalOrders: number;
  totalMails: number;
  lastAllocatedAt?: string;
  projects: AdminMicrosoftUsageProject[];
}

export interface AdminMicrosoftListFilter {
  search?: string;
  status?: AdminMicrosoftResourceStatus | "all";
  ownerId?: number;
  forSale?: boolean;
  longLived?: boolean;
  graphAvailable?: boolean;
  tokenHealth?: AdminMicrosoftTokenHealth | "all";
  taskStatus?: AdminMicrosoftTaskFilter;
  suffix?: string;
  qualityMin?: number;
  qualityMax?: number;
  createdFrom?: string;
  createdTo?: string;
}

export interface AdminMicrosoftStatusFacet {
  all: number;
  pending: number;
  normal: number;
  abnormal: number;
  disabled: number;
  deleted: number;
}

export interface AdminMicrosoftBooleanFacet {
  all: number;
  yes: number;
  no: number;
}

export interface AdminMicrosoftTokenHealthFacet {
  all: number;
  valid: number;
  expiring: number;
  expired: number;
  missing: number;
}

export interface AdminMicrosoftTaskStatusFacet {
  all: number;
  idle: number;
  queued: number;
  running: number;
  failed: number;
}

export interface AdminMicrosoftFacets {
  status: AdminMicrosoftStatusFacet;
  forSale: AdminMicrosoftBooleanFacet;
  longLived: AdminMicrosoftBooleanFacet;
  graphAvailable: AdminMicrosoftBooleanFacet;
  tokenHealth: AdminMicrosoftTokenHealthFacet;
  taskStatus: AdminMicrosoftTaskStatusFacet;
  suffixes: { key: string; count: number }[];
  owners: {
    key: number;
    count: number;
    email: string;
    nickname: string;
    groupName: string;
    role: AdminMicrosoftOwnerRole;
  }[];
  activeTasks: number;
  failedTasks: number;
}

export interface AdminMicrosoftListResponse {
  items: AdminMicrosoftResourceItem[];
  total: number;
  offset: number;
  limit: number;
  nextAfterId?: number;
  facets: AdminMicrosoftFacets;
}

export type AdminMicrosoftImportErrorStrategy = "skip" | "abort";

export interface ImportAdminMicrosoftResourcesRequest {
  content: string;
  ownerId: number;
  longLived: boolean;
  errorStrategy: AdminMicrosoftImportErrorStrategy;
}

export interface AdminMicrosoftImportResponse {
  importId: number;
  status: "imported" | "failed";
  accepted: number;
  imported: number;
  skipped: number;
  lastSafeError?: string;
  task: AdminMicrosoftAsyncTask;
}

export interface UpdateAdminMicrosoftResourceRequest {
  emailAddress?: string;
  bindingAddress?: string | null;
  ownerId?: number;
  forSale?: boolean;
  longLived?: boolean;
  status?: Exclude<AdminMicrosoftResourceStatus, "deleted">;
  qualityScore?: number;
  graphAvailable?: boolean;
}

export interface ReplaceAdminMicrosoftCredentialsRequest {
  password: string;
  clientId?: string;
  refreshToken?: string;
}

export interface AdminMicrosoftBulkResponse {
  affected: number;
  resourceIds?: number[];
}

export interface AdminMicrosoftValidationResponse {
  queued: number;
  requestId: string;
  validationIds?: number[];
  resourceIds?: number[];
}

// ---------- Deterministic seed data ----------

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

const random = mulberry32(0x5eed_a11a);

function pick<T>(items: readonly T[]): T {
  return items[Math.floor(random() * items.length)];
}

function randomInt(min: number, max: number) {
  return min + Math.floor(random() * (max - min + 1));
}

const DAY = 86_400_000;
const HOUR = 3_600_000;
const MOCK_NOW = Date.parse("2026-07-11T08:00:00.000Z");

const OWNERS: AdminMicrosoftOwner[] = [
  {
    id: 1,
    email: "root@aishop6.com",
    nickname: "平台超管",
    groupName: "平台管理组",
    role: "super_admin",
    enabled: true,
  },
  {
    id: 2,
    email: "ops@aishop6.com",
    nickname: "运营管理员",
    groupName: "运营管理组",
    role: "admin",
    enabled: true,
  },
  {
    id: 3,
    email: "risk@aishop6.com",
    nickname: "风控管理员",
    groupName: "风控管理组",
    role: "admin",
    enabled: true,
  },
  {
    id: 101,
    email: "starlink@outlook.com",
    nickname: "星链数码",
    groupName: "供应商组",
    role: "supplier",
    enabled: true,
  },
  {
    id: 102,
    email: "cloudsail@gmail.com",
    nickname: "云帆账号行",
    groupName: "供应商组",
    role: "supplier",
    enabled: true,
  },
  {
    id: 103,
    email: "aurora@proton.me",
    nickname: "极光资源社",
    groupName: "供应商组",
    role: "supplier",
    enabled: true,
  },
  {
    id: 104,
    email: "dolphin@hotmail.com",
    nickname: "海豚小铺",
    groupName: "供应商组",
    role: "supplier",
    enabled: false,
  },
  {
    id: 105,
    email: "nebula@qq.com",
    nickname: "星云批发",
    groupName: "供应商组",
    role: "supplier",
    enabled: true,
  },
  {
    id: 501,
    email: "nova.li@gmail.com",
    nickname: "Nova",
    groupName: "默认用户组",
    role: "user",
    enabled: true,
  },
  {
    id: 502,
    email: "kite.wang@outlook.com",
    nickname: "Kite",
    groupName: "默认用户组",
    role: "user",
    enabled: true,
  },
  {
    id: 503,
    email: "lumen.zhao@163.com",
    nickname: "Lumen",
    groupName: "默认用户组",
    role: "user",
    enabled: true,
  },
  {
    id: 504,
    email: "orbit.chen@foxmail.com",
    nickname: "Orbit",
    groupName: "默认用户组",
    role: "user",
    enabled: false,
  },
];

const MICROSOFT_SUFFIXES = [
  "@outlook.com",
  "@hotmail.com",
  "@live.com",
  "@msn.com",
  "@outlook.jp",
  "@outlook.de",
  "@hotmail.co.uk",
] as const;

const BINDING_DOMAINS = [
  "gmail.com",
  "qq.com",
  "163.com",
  "outlook.com",
  "proton.me",
] as const;

const LOCAL_HEADS = [
  "nova",
  "kite",
  "lumen",
  "orbit",
  "pine",
  "quartz",
  "raven",
  "sable",
  "tidal",
  "umber",
  "vesper",
  "willow",
  "zephyr",
  "cobalt",
  "ember",
  "flint",
  "harbor",
  "indigo",
  "juniper",
  "lyra",
  "maple",
  "onyx",
  "petal",
  "reef",
] as const;

const LOCAL_TAILS = [
  "mail",
  "post",
  "inbox",
  "relay",
  "hub",
  "box",
  "cloud",
  "link",
  "port",
  "wave",
  "peak",
  "star",
] as const;

const SAFE_RESOURCE_ERRORS = [
  "Microsoft refresh token is invalid or expired.",
  "Microsoft Graph access token is unauthorized or expired.",
  "Microsoft account password is incorrect.",
  "Microsoft account is locked.",
  "Microsoft account is restricted or requires recovery.",
  "Microsoft account requires authenticator verification.",
  "Microsoft upstream request timed out; retry budget is exhausted.",
] as const;

const SAFE_ALIAS_ERRORS = [
  "Microsoft alias operation was rejected by an upstream policy.",
  "Alias confirmation was not observed before the reconciliation deadline.",
  "Microsoft alias page was temporarily unavailable.",
] as const;

const TOKEN_SCOPES = [
  "offline_access",
  "openid",
  "profile",
  "email",
  "Mail.Read",
] as const;

interface InternalMicrosoftRecord {
  item: AdminMicrosoftResourceItem;
  credentials: AdminMicrosoftCredentialConfiguration;
  token: AdminMicrosoftTokenDiagnostic;
  aliasSamples: AdminMicrosoftResourceDetail["aliasSamples"];
  aliasSchedule: AdminMicrosoftAliasSchedule;
  aliasAttempts: AdminMicrosoftAliasAttempt[];
  asyncTasks: AdminMicrosoftResourceDetail["asyncTasks"];
  messages: AdminMicrosoftMessage[];
  auxiliaryMessages: AdminMicrosoftMessage[];
  allocations: AdminMicrosoftAllocation[];
}

function isoAt(timestamp: number) {
  return new Date(timestamp).toISOString();
}

function suffixFromEmail(email: string) {
  const at = email.lastIndexOf("@");
  return at < 0 ? "" : email.slice(at).toLowerCase();
}

function ownerSnapshot(owner: AdminMicrosoftOwner) {
  return {
    ownerId: owner.id,
    ownerEmail: owner.email,
    ownerNickname: owner.nickname,
    ownerGroupName: owner.groupName,
    ownerRole: owner.role,
  };
}

function buildAliasSamples(
  resourceId: number,
  emailAddress: string,
  counts: AdminMicrosoftAliasCounts,
  createdAt: number
): AdminMicrosoftResourceDetail["aliasSamples"] {
  const [local = "mail", domain = "outlook.com"] = emailAddress.split("@");
  const explicit: AdminMicrosoftAliasSample[] = [];
  const dot: AdminMicrosoftAliasSample[] = [];
  const plus: AdminMicrosoftAliasSample[] = [];

  for (let index = 0; index < Math.min(counts.explicit, 10); index += 1) {
    const aliasCreatedAt = Math.min(
      MOCK_NOW,
      createdAt + (index + 1) * randomInt(2, 12) * DAY
    );
    const allocated = random() < 0.4;
    explicit.push({
      id: resourceId * 100 + index + 1,
      kind: "explicit",
      emailAddress: `${pick(LOCAL_HEADS)}.${randomInt(10, 999)}@${domain}`,
      status: allocated ? "allocated" : "available",
      createdAt: isoAt(aliasCreatedAt),
      lastAllocatedAt: allocated
        ? isoAt(Math.min(MOCK_NOW, aliasCreatedAt + randomInt(1, 30) * DAY))
        : undefined,
    });
  }

  for (let index = 0; index < Math.min(counts.dot, 4); index += 1) {
    const dotPosition = Math.max(
      1,
      Math.min(local.length - 1, 1 + ((index * 2) % Math.max(1, local.length - 1)))
    );
    const dotted = `${local.slice(0, dotPosition)}.${local.slice(dotPosition)}`;
    const aliasCreatedAt = createdAt + (index + 1) * DAY;
    dot.push({
      id: resourceId * 100 + 20 + index,
      kind: "dot",
      emailAddress: `${dotted}@${domain}`,
      status: random() < 0.32 ? "allocated" : "available",
      createdAt: isoAt(aliasCreatedAt),
    });
  }

  const plusTags = ["shop", "code", "verify", "order"] as const;
  for (let index = 0; index < Math.min(counts.plus, 4); index += 1) {
    plus.push({
      id: resourceId * 100 + 40 + index,
      kind: "plus",
      emailAddress: `${local}+${plusTags[index % plusTags.length]}${index + 1}@${domain}`,
      status: random() < 0.28 ? "allocated" : "available",
      createdAt: isoAt(createdAt + (index + 1) * HOUR),
    });
  }

  return { explicit, dot, plus };
}

function scheduleStatusFor(
  status: AdminMicrosoftResourceStatus
): AdminMicrosoftAliasScheduleStatus {
  if (status === "disabled" || status === "deleted" || status === "abnormal") {
    return "paused";
  }
  if (status === "pending") return "pending";
  const roll = random();
  return roll < 0.2 ? "running" : roll < 0.72 ? "queued" : "pending";
}

function taskPath(kind: AdminMicrosoftAsyncTaskKind) {
  switch (kind) {
    case "validation":
      return "/microsoft/validate";
    case "import":
      return "/microsoft/import";
    case "alias":
      return "/microsoft/alias-replenish";
    case "token":
      return "/microsoft/token-refresh";
    default:
      return "/microsoft/fetch-mail";
  }
}

function makeSeedTask(
  resourceId: number,
  sequence: number,
  kind: AdminMicrosoftAsyncTaskKind,
  status: AdminMicrosoftAsyncTaskStatus,
  createdAt: number,
  safeError?: string
): AdminMicrosoftAsyncTask {
  const id = resourceId * 10 + sequence;
  const maxAttempts = kind === "alias" ? 6 : 3;
  const attempts =
    status === "queued"
      ? 0
      : status === "running"
        ? 1
        : status === "failed"
          ? maxAttempts
          : status === "uncertain"
            ? 2
            : 1;
  const startedAt = status === "queued" ? undefined : createdAt + 8 * 60_000;
  const terminal = status === "succeeded" || status === "failed";
  const finishedAt = terminal ? (startedAt ?? createdAt) + 5 * 60_000 : undefined;
  const updatedAt = finishedAt ?? startedAt ?? createdAt;
  return {
    id,
    kind,
    resourceId,
    status,
    requestId: `req_mock_${kind}_${id.toString(36)}`,
    path: taskPath(kind),
    attempts,
    maxAttempts,
    safeError,
    createdAt: isoAt(createdAt),
    queuedAt: isoAt(createdAt),
    startedAt: startedAt ? isoAt(startedAt) : undefined,
    finishedAt: finishedAt ? isoAt(finishedAt) : undefined,
    nextRunAt:
      status === "queued" || status === "uncertain"
        ? isoAt(updatedAt + randomInt(1, 8) * HOUR)
        : undefined,
    updatedAt: isoAt(updatedAt),
  };
}

function validationTaskStatusFor(
  status: AdminMicrosoftResourceStatus
): AdminMicrosoftAsyncTaskStatus {
  if (status === "pending") return random() < 0.5 ? "queued" : "running";
  if (status === "abnormal") return "failed";
  if (status === "normal") return "succeeded";
  return random() < 0.82 ? "succeeded" : "failed";
}

function aliasTaskStatusFor(
  scheduleStatus: AdminMicrosoftAliasScheduleStatus
): AdminMicrosoftAsyncTaskStatus {
  if (scheduleStatus === "running") return "running";
  if (scheduleStatus === "queued" || scheduleStatus === "pending") return "queued";
  return random() < 0.72 ? "succeeded" : "failed";
}

function buildAliasAttempts(
  itemId: number,
  emailAddress: string,
  scheduleStatus: AdminMicrosoftAliasScheduleStatus,
  explicitCount: number,
  baseTime: number
): AdminMicrosoftAliasAttempt[] {
  const [local = "mail", domain = "outlook.com"] = emailAddress.split("@");
  const attempts: AdminMicrosoftAliasAttempt[] = [];
  const historySize = Math.min(4, Math.max(1, explicitCount > 0 ? 3 : 1));
  for (let index = 0; index < historySize; index += 1) {
    let status: AdminMicrosoftAliasAttemptStatus = "succeeded";
    if (index === 0 && scheduleStatus === "running") status = "running";
    else if (random() < 0.16) status = "failed";
    else if (random() < 0.1) status = "uncertain";
    const startedAt = baseTime - index * 7 * DAY;
    const terminal = status === "succeeded" || status === "failed";
    const finishedAt = terminal ? startedAt + randomInt(2, 15) * 60_000 : undefined;
    const id = itemId * 10 + index + 1;
    attempts.push({
      id,
      status,
      candidateEmail: `${local}.alias${(itemId + index) % 997}@${domain}`,
      requestId: `req_mock_alias_attempt_${id.toString(36)}`,
      attempts: status === "running" ? 1 : status === "failed" ? 4 : 2,
      maxAttempts: 6,
      safeError:
        status === "failed" || status === "uncertain"
          ? pick(SAFE_ALIAS_ERRORS)
          : undefined,
      startedAt: isoAt(startedAt),
      finishedAt: finishedAt ? isoAt(finishedAt) : undefined,
      updatedAt: isoAt(finishedAt ?? startedAt + 5 * 60_000),
    });
  }
  return attempts;
}

function allTasks(record: InternalMicrosoftRecord) {
  return [
    ...record.asyncTasks.validations,
    ...record.asyncTasks.imports,
    ...record.asyncTasks.aliases,
    ...record.asyncTasks.tokens,
    ...record.asyncTasks.fetches,
  ];
}

function deriveActiveTaskStatus(
  record: InternalMicrosoftRecord
): AdminMicrosoftActiveTaskStatus | undefined {
  const tasks = allTasks(record);
  if (tasks.some((task) => task.status === "running")) return "running";
  if (tasks.some((task) => task.status === "queued")) return "queued";
  const latest = [...tasks].sort((left, right) =>
    right.updatedAt.localeCompare(left.updatedAt)
  )[0];
  if (latest?.status === "failed" || latest?.status === "uncertain") {
    return "failed";
  }
  return undefined;
}

function refreshDerivedTaskFields(record: InternalMicrosoftRecord) {
  record.item.activeTaskStatus = deriveActiveTaskStatus(record);
  record.item.failedTaskCount = allTasks(record).filter(
    (task) => task.status === "failed" || task.status === "uncertain"
  ).length;
}

// ---------- Usage read-model seed data ----------

const USAGE_PROJECTS = [
  { id: "prj_tiktok", name: "TikTok 接码注册" },
  { id: "prj_openai", name: "OpenAI 账号验证" },
  { id: "prj_amazon", name: "Amazon 店铺矩阵" },
  { id: "prj_discord", name: "Discord 社区验证" },
  { id: "prj_steam", name: "Steam 游戏账号" },
  { id: "prj_paypal", name: "PayPal 商户收款" },
  { id: "prj_telegram", name: "Telegram 接码" },
  { id: "prj_binance", name: "Binance 合规验证" },
] as const;

const USAGE_BUYERS = [
  "nova.li@gmail.com",
  "kite.wang@outlook.com",
  "lumen.zhao@163.com",
  "orbit.chen@foxmail.com",
  "vega.sun@qq.com",
  "atlas.wu@gmail.com",
] as const;

// Mail templates keep bodies safe and free of secrets. Templates with a
// `{code}` slot represent verification mail; the rest are non-code traffic.
const MAIL_TEMPLATES = [
  {
    sender: "no-reply@tiktok.com",
    subject: "Your TikTok verification code",
    body: "Your TikTok verification code is {code}. It expires in 5 minutes.",
  },
  {
    sender: "no-reply@accounts.google.com",
    subject: "Google security code",
    body: "Use {code} to verify your Google account. Do not share this code.",
  },
  {
    sender: "account-security@openai.com",
    subject: "Your OpenAI login code",
    body: "Enter {code} to finish signing in to OpenAI.",
  },
  {
    sender: "auto-confirm@amazon.com",
    subject: "Amazon one-time password",
    body: "Your Amazon one-time password is {code}.",
  },
  {
    sender: "noreply@discord.com",
    subject: "Discord verification",
    body: "Your Discord verification code: {code}",
  },
  {
    sender: "noreply@steampowered.com",
    subject: "Steam Guard code",
    body: "Your Steam Guard code is {code}.",
  },
  {
    sender: "service@paypal.com",
    subject: "PayPal confirmation code",
    body: "PayPal code: {code}. We will never ask you to share it.",
  },
  {
    sender: "newsletter@substack.com",
    subject: "Your weekly digest is ready",
    body: "Here is your weekly digest. No action is required.",
  },
  {
    sender: "notifications@github.com",
    subject: "New sign-in to your account",
    body: "A new sign-in to your account was detected from a new device.",
  },
] as const;

const MAIL_IGNORE_DIAGNOSTICS = [
  "No active allocation matched the recipient address.",
  "Sender did not match the project mail rule.",
  "Message did not satisfy the strict body rule.",
  "Recipient strategy did not match an allocated alias.",
] as const;

// Verification mail delivered to the auxiliary (recovery) mailbox always carries
// a Microsoft account security code.
const AUX_MAIL_TEMPLATES = [
  {
    sender: "account-security-noreply@accountprotection.microsoft.com",
    subject: "Microsoft account security code",
    body: "Your Microsoft account security code is {code}.",
  },
  {
    sender: "account-security-noreply@accountprotection.microsoft.com",
    subject: "Verify your identity",
    body: "Use code {code} to verify it is you.",
  },
  {
    sender: "no-reply@microsoft.com",
    subject: "Your single-use code",
    body: "Single-use code: {code}. It expires shortly.",
  },
] as const;

function makeVerificationCode() {
  return String(randomInt(100_000, 999_999));
}

function makeOrderNo(resourceId: number, index: number) {
  return `RM${(resourceId * 100 + index + 1).toString().padStart(9, "0")}`;
}

function collectAllocationCandidates(
  emailAddress: string,
  aliasSamples: AdminMicrosoftResourceDetail["aliasSamples"]
): { email: string; mailbox: AdminMicrosoftMailboxKind }[] {
  return [
    { email: emailAddress, mailbox: "main" },
    ...aliasSamples.explicit.map((sample) => ({
      email: sample.emailAddress,
      mailbox: "alias" as const,
    })),
    ...aliasSamples.dot.map((sample) => ({
      email: sample.emailAddress,
      mailbox: "dot" as const,
    })),
    ...aliasSamples.plus.map((sample) => ({
      email: sample.emailAddress,
      mailbox: "plus" as const,
    })),
  ];
}

function buildAllocations(
  item: AdminMicrosoftResourceItem,
  aliasSamples: AdminMicrosoftResourceDetail["aliasSamples"],
  createdAt: number
): AdminMicrosoftAllocation[] {
  if (item.status === "pending" || item.status === "deleted") return [];
  const candidates = collectAllocationCandidates(item.emailAddress, aliasSamples);
  const supplyScope: AdminMicrosoftSupplyScope = item.forSale ? "public" : "owned";
  const allocations: AdminMicrosoftAllocation[] = [];
  let sequence = 0;

  for (const candidate of candidates) {
    // Each address may have served several orders over time; the most recent can
    // still be allocated, older ones are released. This yields a realistic order
    // history that is long enough to paginate.
    const historyCount = randomInt(1, 3);
    for (let h = 0; h < historyCount; h += 1) {
      const mostRecent = h === 0;
      const active =
        mostRecent && (candidate.mailbox === "main" ? true : random() < 0.55);
      const project = pick(USAGE_PROJECTS);
      const serviceMode: AdminMicrosoftServiceMode = random() < 0.6 ? "code" : "purchase";
      const allocCreatedAt = Math.min(
        MOCK_NOW,
        Math.max(
          createdAt,
          createdAt + randomInt(1, 320) * HOUR - h * randomInt(120, 720) * HOUR
        )
      );
      const orderStatus: AdminMicrosoftOrderStatus = active
        ? serviceMode === "code"
          ? "active"
          : random() < 0.5
            ? "active"
            : "completed"
        : pick(["completed", "refunded", "closed"] as const);
      const status: AdminMicrosoftAllocationStatus = active ? "allocated" : "released";
      allocations.push({
        id: item.id * 1000 + 700 + sequence,
        orderNo: makeOrderNo(item.id, sequence),
        projectId: project.id,
        projectName: project.name,
        mailbox: candidate.mailbox,
        supplyScope,
        deliveryEmail: candidate.email,
        status,
        serviceMode,
        orderStatus,
        payAmount: (randomInt(180, 3200) / 100).toFixed(2),
        buyerEmail: pick(USAGE_BUYERS),
        mailCount: 0,
        createdAt: isoAt(allocCreatedAt),
        receiveUntil: active
          ? isoAt(Math.min(MOCK_NOW + 72 * HOUR, allocCreatedAt + randomInt(1, 72) * HOUR))
          : undefined,
        releasedAt: active
          ? undefined
          : isoAt(Math.min(MOCK_NOW, allocCreatedAt + randomInt(1, 48) * HOUR)),
      });
      sequence += 1;
    }
  }

  // Most recent orders first.
  allocations.sort((left, right) => right.createdAt.localeCompare(left.createdAt));
  return allocations;
}

function buildMessages(
  item: AdminMicrosoftResourceItem,
  aliasSamples: AdminMicrosoftResourceDetail["aliasSamples"],
  allocations: AdminMicrosoftAllocation[],
  createdAt: number
): AdminMicrosoftMessage[] {
  if (item.status === "pending" || item.status === "deleted") return [];
  const addresses = collectAllocationCandidates(item.emailAddress, aliasSamples).slice(
    0,
    5
  );
  const activeByEmail = new Map(
    allocations
      .filter((allocation) => allocation.status === "allocated")
      .map((allocation) => [allocation.deliveryEmail, allocation])
  );
  const count = randomInt(3, 12);
  const messages: AdminMicrosoftMessage[] = [];

  for (let index = 0; index < count; index += 1) {
    const address = pick(addresses);
    const template = pick(MAIL_TEMPLATES);
    const hasCode = template.body.includes("{code}");
    const code = hasCode ? makeVerificationCode() : undefined;
    const allocation = activeByEmail.get(address.email);

    let status: AdminMicrosoftMessageStatus;
    let matchDiagnostic: string | undefined;
    let orderNo: string | undefined;
    if (hasCode && allocation) {
      status = "matched";
      orderNo = allocation.orderNo;
    } else if (hasCode) {
      status = random() < 0.5 ? "received" : "ignored";
      if (status === "ignored") matchDiagnostic = pick(MAIL_IGNORE_DIAGNOSTICS);
    } else {
      status = "ignored";
      matchDiagnostic = pick(MAIL_IGNORE_DIAGNOSTICS);
    }

    const body = template.body.replace("{code}", code ?? "");
    const receivedAt = Math.min(MOCK_NOW, createdAt + randomInt(1, 400) * HOUR);
    messages.push({
      id: item.id * 1000 + index + 1,
      mailbox: address.mailbox,
      recipient: address.email,
      sender: template.sender,
      subject: template.subject,
      preview: body.length > 90 ? `${body.slice(0, 90)}…` : body,
      body,
      verificationCode: status === "matched" ? code : undefined,
      status,
      matchDiagnostic,
      orderNo,
      receivedAt: isoAt(receivedAt),
    });
  }

  messages.sort((left, right) => right.receivedAt.localeCompare(left.receivedAt));
  return messages;
}

function buildAuxiliaryMessages(
  item: AdminMicrosoftResourceItem,
  createdAt: number
): AdminMicrosoftMessage[] {
  if (
    !item.bindingAddress ||
    item.status === "pending" ||
    item.status === "deleted"
  ) {
    return [];
  }
  const count = randomInt(2, 6);
  const messages: AdminMicrosoftMessage[] = [];
  for (let index = 0; index < count; index += 1) {
    const template = pick(AUX_MAIL_TEMPLATES);
    const code = makeVerificationCode();
    const body = template.body.replace("{code}", code);
    const receivedAt = Math.min(MOCK_NOW, createdAt + randomInt(1, 400) * HOUR);
    messages.push({
      id: item.id * 1000 + 500 + index,
      mailbox: "main",
      recipient: item.bindingAddress,
      sender: template.sender,
      subject: template.subject,
      preview: body.length > 90 ? `${body.slice(0, 90)}…` : body,
      body,
      verificationCode: code,
      status: "received",
      receivedAt: isoAt(receivedAt),
    });
  }
  messages.sort((left, right) => right.receivedAt.localeCompare(left.receivedAt));
  return messages;
}


// alias visibly shows the order/project it currently serves, and backfill each
// allocation's matched-mail count.
function reconcileUsageFacts(
  aliasSamples: AdminMicrosoftResourceDetail["aliasSamples"],
  allocations: AdminMicrosoftAllocation[],
  messages: AdminMicrosoftMessage[]
) {
  for (const allocation of allocations) {
    if (allocation.status !== "allocated" || allocation.mailbox === "main") continue;
    const pool =
      allocation.mailbox === "alias"
        ? aliasSamples.explicit
        : allocation.mailbox === "dot"
          ? aliasSamples.dot
          : aliasSamples.plus;
    const sample = pool.find(
      (entry) => entry.emailAddress === allocation.deliveryEmail
    );
    if (sample) {
      sample.status = "allocated";
      sample.orderNo = allocation.orderNo;
      sample.projectName = allocation.projectName;
      sample.lastAllocatedAt = sample.lastAllocatedAt ?? allocation.createdAt;
    }
  }

  const matchedByEmail = new Map<string, number>();
  for (const message of messages) {
    if (message.status !== "matched") continue;
    matchedByEmail.set(
      message.recipient,
      (matchedByEmail.get(message.recipient) ?? 0) + 1
    );
  }
  for (const allocation of allocations) {
    allocation.mailCount = matchedByEmail.get(allocation.deliveryEmail) ?? 0;
  }

  // The order's delivered verification code is the code of its most recent
  // matched mail.
  const codeByOrder = new Map<string, { code: string; receivedAt: string }>();
  for (const message of messages) {
    if (message.status !== "matched" || !message.orderNo || !message.verificationCode) {
      continue;
    }
    const existing = codeByOrder.get(message.orderNo);
    if (!existing || message.receivedAt > existing.receivedAt) {
      codeByOrder.set(message.orderNo, {
        code: message.verificationCode,
        receivedAt: message.receivedAt,
      });
    }
  }
  for (const allocation of allocations) {
    allocation.verificationCode = codeByOrder.get(allocation.orderNo)?.code;
  }
}

function deriveUsageSummary(
  item: AdminMicrosoftResourceItem,
  allocations: AdminMicrosoftAllocation[],
  messages: AdminMicrosoftMessage[]
): AdminMicrosoftUsageSummary {
  const projects = new Map<string, AdminMicrosoftUsageProject>();
  for (const allocation of allocations) {
    const existing = projects.get(allocation.projectId);
    if (existing) existing.orderCount += 1;
    else
      projects.set(allocation.projectId, {
        id: allocation.projectId,
        name: allocation.projectName,
        orderCount: 1,
      });
  }
  return {
    activeAllocations: allocations.filter(
      (allocation) => allocation.status === "allocated"
    ).length,
    totalOrders: allocations.length,
    totalMails: messages.length,
    lastAllocatedAt: item.lastAllocatedAt,
    projects: Array.from(projects.values()).sort(
      (left, right) => right.orderCount - left.orderCount
    ),
  };
}

function buildDataset(): InternalMicrosoftRecord[] {
  const records: InternalMicrosoftRecord[] = [];
  const total = 242;
  let id = 6200;

  for (let index = 0; index < total; index += 1) {
    const owner = pick(OWNERS);
    const suffix = pick(MICROSOFT_SUFFIXES);
    const local = `${pick(LOCAL_HEADS)}.${pick(LOCAL_TAILS)}${index + 17}`;
    const emailAddress = `${local}${suffix}`;
    const statusRoll = random();
    const status: AdminMicrosoftResourceStatus =
      statusRoll < 0.16
        ? "pending"
        : statusRoll < 0.61
          ? "normal"
          : statusRoll < 0.78
            ? "abnormal"
            : statusRoll < 0.9
              ? "disabled"
              : "deleted";
    const longLived = random() < 0.57;
    const passwordConfigured = random() > 0.03;
    // Credential presence and RT lifetime are independent from the commercial
    // longLived classification selected for the resource.
    const refreshTokenConfigured = random() < 0.74;
    const clientIdConfigured = refreshTokenConfigured || random() < 0.72;
    let tokenHealth: AdminMicrosoftTokenHealth;
    if (!refreshTokenConfigured) {
      tokenHealth = "missing";
    } else if (status === "abnormal" && random() < 0.66) {
      tokenHealth = "expired";
    } else {
      const tokenRoll = random();
      tokenHealth =
        tokenRoll < 0.57 ? "valid" : tokenRoll < 0.79 ? "expiring" : "expired";
    }
    const rtExpireAt =
      tokenHealth === "missing"
        ? undefined
        : tokenHealth === "valid"
          ? isoAt(MOCK_NOW + randomInt(30, 180) * DAY)
          : tokenHealth === "expiring"
            ? isoAt(MOCK_NOW + randomInt(1, 14) * DAY)
            : isoAt(MOCK_NOW - randomInt(1, 90) * DAY);
    const graphAvailable =
      status === "normal" && refreshTokenConfigured && random() < 0.76;
    const qualityBase =
      status === "normal"
        ? randomInt(72, 100)
        : status === "pending"
          ? randomInt(45, 78)
          : status === "abnormal"
            ? randomInt(15, 58)
            : randomInt(25, 72);
    const explicitCount = status === "pending" ? randomInt(0, 3) : randomInt(0, 10);
    const dotCount = randomInt(4, 42);
    const plusCount = randomInt(12, 120);
    const aliasCounts: AdminMicrosoftAliasCounts = {
      explicit: explicitCount,
      dot: dotCount,
      plus: plusCount,
    };
    const createdAt = MOCK_NOW - randomInt(2, 420) * DAY - randomInt(0, 23) * HOUR;
    const updatedAt = Math.min(
      MOCK_NOW,
      createdAt + randomInt(0, 90) * DAY + randomInt(0, 23) * HOUR
    );
    const scheduleStatus = scheduleStatusFor(status);
    const lastSafeError =
      status === "abnormal"
        ? pick(SAFE_RESOURCE_ERRORS)
        : status === "disabled" && random() < 0.35
          ? "Resource was disabled by an administrator after a risk review."
          : undefined;
    const validationStatus = validationTaskStatusFor(status);
    const validationTask = makeSeedTask(
      id,
      1,
      "validation",
      validationStatus,
      Math.max(createdAt, updatedAt - randomInt(1, 18) * DAY),
      validationStatus === "failed" ? lastSafeError ?? pick(SAFE_RESOURCE_ERRORS) : undefined
    );
    const importTask = makeSeedTask(
      id,
      2,
      "import",
      "succeeded",
      createdAt - randomInt(5, 45) * 60_000
    );
    const aliasTaskStatus = aliasTaskStatusFor(scheduleStatus);
    const aliasTask = makeSeedTask(
      id,
      3,
      "alias",
      aliasTaskStatus,
      Math.max(createdAt, updatedAt - randomInt(1, 12) * DAY),
      aliasTaskStatus === "failed" ? pick(SAFE_ALIAS_ERRORS) : undefined
    );
    const tokenTask = makeSeedTask(
      id,
      4,
      "token",
      tokenHealth === "expired" ? "failed" : "succeeded",
      Math.max(createdAt, updatedAt - randomInt(1, 20) * DAY),
      tokenHealth === "expired"
        ? "Microsoft refresh token is invalid or expired."
        : undefined
    );
    const fetchTask = makeSeedTask(
      id,
      5,
      "fetch",
      status === "normal" ? "succeeded" : "uncertain",
      Math.max(createdAt, updatedAt - randomInt(1, 6) * DAY)
    );
    const aliasSchedule: AdminMicrosoftAliasSchedule = {
      id: 80_000 + index,
      status: scheduleStatus,
      weekCreated: Math.min(2, explicitCount % 3),
      weekLimit: 2,
      yearCreated: explicitCount,
      yearLimit: 10,
      attempts: explicitCount + randomInt(0, 4),
      failureStreak: aliasTaskStatus === "failed" ? randomInt(1, 3) : 0,
      nextRunAt:
        scheduleStatus === "pending" || scheduleStatus === "queued"
          ? isoAt(MOCK_NOW + randomInt(1, 36) * HOUR)
          : undefined,
      lastDispatchedAt:
        scheduleStatus === "running" || explicitCount > 0
          ? isoAt(MOCK_NOW - randomInt(1, 72) * HOUR)
          : undefined,
      pauseReason:
        scheduleStatus === "paused"
          ? status === "deleted"
            ? "资源已删除，显式别名补货已暂停。"
            : status === "disabled"
              ? "资源已禁用，显式别名补货已暂停。"
              : "资源当前非正常状态，等待验证恢复。"
          : undefined,
      lastSafeError:
        aliasTaskStatus === "failed" ? aliasTask.safeError : undefined,
      updatedAt: isoAt(updatedAt),
    };
    const tokenLastSafeError =
      tokenHealth === "expired"
        ? "Microsoft refresh token is invalid or expired."
        : tokenHealth === "missing"
          ? "OAuth refresh token has not been configured."
          : undefined;
    const forSale =
      status !== "deleted" && owner.role !== "user" && owner.enabled && random() < 0.46;
    const item: AdminMicrosoftResourceItem = {
      id,
      emailAddress,
      suffix,
      bindingAddress:
        random() < 0.55
          ? `${pick(LOCAL_HEADS)}.recovery${randomInt(10, 99)}@${pick(BINDING_DOMAINS)}`
          : undefined,
      ...ownerSnapshot(owner),
      status,
      forSale,
      longLived,
      graphAvailable,
      qualityScore: qualityBase,
      rtExpireAt,
      tokenHealth,
      aliasCounts,
      explicitAliasCount: aliasCounts.explicit,
      dotAliasCount: aliasCounts.dot,
      plusAliasCount: aliasCounts.plus,
      activeTaskStatus: undefined,
      failedTaskCount: 0,
      lastSafeError,
      lastAllocatedAt:
        status === "normal" && random() < 0.65
          ? isoAt(Math.max(createdAt, MOCK_NOW - randomInt(1, 120) * DAY))
          : undefined,
      createdAt: isoAt(createdAt),
      updatedAt: isoAt(updatedAt),
    };
    const aliasSamples = buildAliasSamples(id, emailAddress, aliasCounts, createdAt);
    const allocations = buildAllocations(item, aliasSamples, createdAt);
    const messages = buildMessages(item, aliasSamples, allocations, createdAt);
    const auxiliaryMessages = buildAuxiliaryMessages(item, createdAt);
    reconcileUsageFacts(aliasSamples, allocations, messages);
    // Keep the resource's last-allocated marker consistent with its allocation
    // facts so the list column and the detail summary agree.
    if (allocations.length > 0) {
      item.lastAllocatedAt = allocations
        .map((allocation) => allocation.createdAt)
        .sort((left, right) => right.localeCompare(left))[0];
    }
    const record: InternalMicrosoftRecord = {
      item,
      credentials: {
        passwordConfigured,
        clientIdConfigured,
        refreshTokenConfigured,
        revision: randomInt(1, 5),
        updatedAt: isoAt(Math.max(createdAt, updatedAt - randomInt(0, 30) * DAY)),
      },
      token: {
        health: tokenHealth,
        rtExpireAt,
        lastRefreshedAt:
          refreshTokenConfigured && tokenHealth !== "expired"
            ? isoAt(MOCK_NOW - randomInt(1, 30) * DAY)
            : undefined,
        scopes: refreshTokenConfigured ? [...TOKEN_SCOPES] : [],
        lastRefreshRequestId: refreshTokenConfigured
          ? `req_mock_refresh_${id.toString(36)}`
          : undefined,
        lastSafeError: tokenLastSafeError,
      },
      aliasSamples,
      aliasSchedule,
      aliasAttempts: buildAliasAttempts(
        id,
        emailAddress,
        scheduleStatus,
        explicitCount,
        Math.max(createdAt, updatedAt - DAY)
      ),
      asyncTasks: {
        validations: [validationTask],
        imports: [importTask],
        aliases: [aliasTask],
        tokens: [tokenTask],
        fetches: [fetchTask],
      },
      messages,
      auxiliaryMessages,
      allocations,
    };
    refreshDerivedTaskFields(record);
    records.push(record);
    id += 1;
  }

  records.sort((left, right) => right.item.id - left.item.id);
  return records;
}

const dataset = buildDataset();
let nextResourceId = Math.max(...dataset.map((record) => record.item.id)) + 1;
let nextTaskId = 900_000;
let nextImportId = 70_000;
let nextScheduleId = 95_000;
let nextRequestSequence = 1;
let latencySequence = 0;

// ---------- Clone / lookup helpers ----------

function clone<T>(value: T): T {
  return JSON.parse(JSON.stringify(value)) as T;
}

function cloneItem(record: InternalMicrosoftRecord) {
  return clone(record.item);
}

function findRecord(id: number) {
  const record = dataset.find((entry) => entry.item.id === id);
  if (!record) throw new Error("Microsoft resource not found.");
  return record;
}

function findOwner(ownerId: number) {
  const owner = OWNERS.find((entry) => entry.id === ownerId);
  if (!owner) throw new Error("Resource owner not found.");
  return owner;
}

function setOwner(record: InternalMicrosoftRecord, owner: AdminMicrosoftOwner) {
  Object.assign(record.item, ownerSnapshot(owner));
}

function nowIso() {
  return new Date().toISOString();
}

function makeRequestId(prefix: string) {
  const sequence = nextRequestSequence++;
  return `req_mock_${prefix}_${Date.now().toString(36)}_${sequence.toString(36)}`;
}

function simulateLatency(base = 140) {
  latencySequence += 1;
  const jitter = (latencySequence * 47) % 130;
  return new Promise<void>((resolve) =>
    globalThis.setTimeout(resolve, base + jitter)
  );
}

function buildDetail(record: InternalMicrosoftRecord): AdminMicrosoftResourceDetail {
  return clone({
    ...record.item,
    credentials: record.credentials,
    token: record.token,
    aliasSamples: record.aliasSamples,
    aliasSchedule: record.aliasSchedule,
    aliasAttempts: record.aliasAttempts,
    asyncTasks: record.asyncTasks,
    messages: record.messages,
    auxiliaryMessages: record.auxiliaryMessages,
    allocations: record.allocations,
    usageSummary: deriveUsageSummary(
      record.item,
      record.allocations,
      record.messages
    ),
  });
}

// ---------- Filtering / facets ----------

type FacetDimension =
  | "status"
  | "forSale"
  | "longLived"
  | "graphAvailable"
  | "tokenHealth"
  | "taskStatus"
  | "suffix"
  | "owner";

function matchesFilter(
  item: AdminMicrosoftResourceItem,
  filter: AdminMicrosoftListFilter,
  ignore: FacetDimension[] = []
) {
  const skip = new Set(ignore);

  if (!skip.has("status")) {
    const status = filter.status ?? "all";
    if (status === "all") {
      if (item.status === "deleted") return false;
    } else if (item.status !== status) {
      return false;
    }
  }

  if (!skip.has("owner") && filter.ownerId !== undefined) {
    if (item.ownerId !== filter.ownerId) return false;
  }
  if (!skip.has("forSale") && filter.forSale !== undefined) {
    if (item.forSale !== filter.forSale) return false;
  }
  if (!skip.has("longLived") && filter.longLived !== undefined) {
    if (item.longLived !== filter.longLived) return false;
  }
  if (!skip.has("graphAvailable") && filter.graphAvailable !== undefined) {
    if (item.graphAvailable !== filter.graphAvailable) return false;
  }
  if (!skip.has("tokenHealth")) {
    const tokenHealth = filter.tokenHealth ?? "all";
    if (tokenHealth !== "all" && item.tokenHealth !== tokenHealth) return false;
  }
  if (!skip.has("taskStatus")) {
    const taskStatus = filter.taskStatus ?? "all";
    if (taskStatus === "idle") {
      if (item.activeTaskStatus !== undefined) return false;
    } else if (taskStatus !== "all" && item.activeTaskStatus !== taskStatus) {
      return false;
    }
  }
  if (!skip.has("suffix") && filter.suffix) {
    const suffix = filter.suffix.trim().toLowerCase();
    const normalized = suffix.startsWith("@") ? suffix : `@${suffix}`;
    if (item.suffix !== normalized) return false;
  }
  if (filter.qualityMin !== undefined && item.qualityScore < filter.qualityMin) {
    return false;
  }
  if (filter.qualityMax !== undefined && item.qualityScore > filter.qualityMax) {
    return false;
  }
  if (filter.createdFrom) {
    const lowerBound = new Date(filter.createdFrom).getTime();
    if (!Number.isNaN(lowerBound) && new Date(item.createdAt).getTime() < lowerBound) {
      return false;
    }
  }
  if (filter.createdTo) {
    const upperBound = new Date(filter.createdTo).getTime();
    if (!Number.isNaN(upperBound) && new Date(item.createdAt).getTime() > upperBound) {
      return false;
    }
  }

  const search = filter.search?.trim().toLowerCase();
  if (search) {
    const haystack = [
      item.id,
      item.emailAddress,
      item.suffix,
      item.bindingAddress ?? "",
      item.ownerId,
      item.ownerEmail,
      item.ownerNickname,
      item.ownerGroupName,
      item.ownerRole,
      item.status,
      item.tokenHealth,
      item.lastSafeError ?? "",
    ]
      .join(" ")
      .toLowerCase();
    if (!haystack.includes(search)) return false;
  }

  return true;
}

function buildBooleanFacet(
  filter: AdminMicrosoftListFilter,
  dimension: Extract<FacetDimension, "forSale" | "longLived" | "graphAvailable">,
  read: (item: AdminMicrosoftResourceItem) => boolean
): AdminMicrosoftBooleanFacet {
  const scope = dataset.filter((record) =>
    matchesFilter(record.item, filter, [dimension])
  );
  let yes = 0;
  for (const record of scope) {
    if (read(record.item)) yes += 1;
  }
  return { all: scope.length, yes, no: scope.length - yes };
}

function buildFacets(filter: AdminMicrosoftListFilter): AdminMicrosoftFacets {
  const statusScope = dataset.filter((record) =>
    matchesFilter(record.item, filter, ["status"])
  );
  const status: AdminMicrosoftStatusFacet = {
    all: 0,
    pending: 0,
    normal: 0,
    abnormal: 0,
    disabled: 0,
    deleted: 0,
  };
  for (const record of statusScope) {
    status[record.item.status] += 1;
    if (record.item.status !== "deleted") status.all += 1;
  }

  const tokenScope = dataset.filter((record) =>
    matchesFilter(record.item, filter, ["tokenHealth"])
  );
  const tokenHealth: AdminMicrosoftTokenHealthFacet = {
    all: tokenScope.length,
    valid: 0,
    expiring: 0,
    expired: 0,
    missing: 0,
  };
  for (const record of tokenScope) tokenHealth[record.item.tokenHealth] += 1;

  const taskScope = dataset.filter((record) =>
    matchesFilter(record.item, filter, ["taskStatus"])
  );
  const taskStatus: AdminMicrosoftTaskStatusFacet = {
    all: taskScope.length,
    idle: 0,
    queued: 0,
    running: 0,
    failed: 0,
  };
  for (const record of taskScope) {
    const key = record.item.activeTaskStatus ?? "idle";
    taskStatus[key] += 1;
  }

  const suffixScope = dataset.filter((record) =>
    matchesFilter(record.item, filter, ["suffix"])
  );
  const suffixCounts = new Map<string, number>();
  for (const record of suffixScope) {
    suffixCounts.set(
      record.item.suffix,
      (suffixCounts.get(record.item.suffix) ?? 0) + 1
    );
  }
  const suffixes = Array.from(suffixCounts.entries())
    .map(([key, count]) => ({ key, count }))
    .sort((left, right) => right.count - left.count || left.key.localeCompare(right.key));

  const ownerScope = dataset.filter((record) =>
    matchesFilter(record.item, filter, ["owner"])
  );
  const ownerCounts = new Map<number, number>();
  for (const record of ownerScope) {
    ownerCounts.set(
      record.item.ownerId,
      (ownerCounts.get(record.item.ownerId) ?? 0) + 1
    );
  }
  const owners = Array.from(ownerCounts.entries())
    .map(([key, count]) => {
      const owner = findOwner(key);
      return {
        key,
        count,
        email: owner.email,
        nickname: owner.nickname,
        groupName: owner.groupName,
        role: owner.role,
      };
    })
    .sort((left, right) => right.count - left.count || left.key - right.key);

  return {
    status,
    forSale: buildBooleanFacet(filter, "forSale", (item) => item.forSale),
    longLived: buildBooleanFacet(filter, "longLived", (item) => item.longLived),
    graphAvailable: buildBooleanFacet(
      filter,
      "graphAvailable",
      (item) => item.graphAvailable
    ),
    tokenHealth,
    taskStatus,
    suffixes,
    owners,
    activeTasks: taskStatus.queued + taskStatus.running,
    failedTasks: taskScope.reduce(
      (sum, record) => sum + record.item.failedTaskCount,
      0
    ),
  };
}

// ---------- Queries ----------

export async function listAdminMicrosoftResources(
  filter: AdminMicrosoftListFilter = {},
  offset = 0,
  limit = 20,
  afterId?: number
): Promise<AdminMicrosoftListResponse> {
  await simulateLatency();
  const filtered = dataset.filter((record) => matchesFilter(record.item, filter));
  // Keep the caller's block size. useBlockPagedList intentionally requests a
  // 1000-row block, and silently capping it would make rows beyond the cap
  // unreachable because the hook treats the block as complete.
  const safeLimit = Math.max(1, Math.trunc(limit) || 20);
  const requestedOffset = Math.max(0, Math.trunc(offset) || 0);
  const cursorIndex =
    afterId !== undefined
      ? filtered.findIndex((record) => record.item.id < afterId)
      : requestedOffset;
  const start = cursorIndex < 0 ? filtered.length : cursorIndex;
  const pageRecords = filtered.slice(start, start + safeLimit);
  const lastRecord = pageRecords[pageRecords.length - 1];
  return {
    items: pageRecords.map(cloneItem),
    total: filtered.length,
    offset: start,
    limit: safeLimit,
    nextAfterId:
      lastRecord && start + pageRecords.length < filtered.length
        ? lastRecord.item.id
        : undefined,
    facets: buildFacets(filter),
  };
}

export async function getAdminMicrosoftResourceDetail(
  id: number
): Promise<AdminMicrosoftResourceDetail> {
  await simulateLatency(110);
  return buildDetail(findRecord(id));
}

export async function listAdminMicrosoftOwners(
  search = ""
): Promise<AdminMicrosoftOwner[]> {
  await simulateLatency(90);
  const keyword = search.trim().toLowerCase();
  const owners = keyword
    ? OWNERS.filter((owner) =>
        [owner.id, owner.email, owner.nickname, owner.groupName, owner.role]
          .join(" ")
          .toLowerCase()
          .includes(keyword)
      )
    : OWNERS;
  return clone(owners);
}

// ---------- Command helpers ----------

function createCommandTask(
  kind: AdminMicrosoftAsyncTaskKind,
  resourceId: number | undefined,
  status: AdminMicrosoftAsyncTaskStatus,
  requestId = makeRequestId(kind),
  safeError?: string
): AdminMicrosoftAsyncTask {
  const timestamp = nowIso();
  const maxAttempts = kind === "alias" ? 6 : 3;
  const task: AdminMicrosoftAsyncTask = {
    id: nextTaskId++,
    kind,
    resourceId,
    status,
    requestId,
    path: taskPath(kind),
    attempts: status === "queued" ? 0 : 1,
    maxAttempts,
    safeError,
    createdAt: timestamp,
    queuedAt: timestamp,
    startedAt: status === "queued" ? undefined : timestamp,
    finishedAt:
      status === "succeeded" || status === "failed" ? timestamp : undefined,
    nextRunAt:
      status === "queued" || status === "uncertain"
        ? new Date(Date.now() + HOUR).toISOString()
        : undefined,
    updatedAt: timestamp,
  };
  return task;
}

function enqueueValidation(record: InternalMicrosoftRecord) {
  const active = record.asyncTasks.validations.find(
    (task) => task.status === "queued" || task.status === "running"
  );
  if (active) return active;
  const task = createCommandTask("validation", record.item.id, "queued");
  record.asyncTasks.validations.unshift(task);
  refreshDerivedTaskFields(record);
  return task;
}

// Manually enqueue a task of the given kind for this resource. Reuses any active
// (queued/running) task of the same kind so repeated clicks stay idempotent.
function enqueueManualTask(
  record: InternalMicrosoftRecord,
  kind: Extract<AdminMicrosoftAsyncTaskKind, "token" | "fetch" | "alias">,
  bucket: AdminMicrosoftAsyncTask[]
) {
  const active = bucket.find(
    (task) => task.status === "queued" || task.status === "running"
  );
  if (active) return active;
  const task = createCommandTask(kind, record.item.id, "queued");
  bucket.unshift(task);
  touch(record);
  return task;
}

function pauseAliasSchedule(record: InternalMicrosoftRecord, reason: string) {
  record.aliasSchedule.status = "paused";
  record.aliasSchedule.nextRunAt = undefined;
  record.aliasSchedule.pauseReason = reason;
  record.aliasSchedule.updatedAt = nowIso();
}

function resumeAliasSchedule(record: InternalMicrosoftRecord) {
  record.aliasSchedule.status = "pending";
  record.aliasSchedule.pauseReason = undefined;
  record.aliasSchedule.nextRunAt = new Date(Date.now() + 2 * HOUR).toISOString();
  record.aliasSchedule.updatedAt = nowIso();
}

function failActiveTasks(record: InternalMicrosoftRecord, safeError: string) {
  const timestamp = nowIso();
  for (const task of allTasks(record)) {
    if (task.status !== "queued" && task.status !== "running") continue;
    task.status = "failed";
    task.safeError = safeError;
    task.attempts = Math.max(1, task.attempts);
    task.finishedAt = timestamp;
    task.updatedAt = timestamp;
    task.nextRunAt = undefined;
  }
  for (const attempt of record.aliasAttempts) {
    if (attempt.status !== "running") continue;
    attempt.status = "failed";
    attempt.safeError = safeError;
    attempt.finishedAt = timestamp;
    attempt.updatedAt = timestamp;
  }
  refreshDerivedTaskFields(record);
}

function touch(record: InternalMicrosoftRecord) {
  record.item.updatedAt = nowIso();
  refreshDerivedTaskFields(record);
}

function setTokenConfigurationAfterCredentialReplacement(
  record: InternalMicrosoftRecord,
  hasRefreshToken: boolean
) {
  const timestamp = nowIso();
  const rtExpireAt = hasRefreshToken
    ? new Date(Date.now() + 90 * DAY).toISOString()
    : undefined;
  record.item.tokenHealth = hasRefreshToken ? "valid" : "missing";
  record.item.rtExpireAt = rtExpireAt;
  record.token = {
    health: record.item.tokenHealth,
    rtExpireAt,
    lastRefreshedAt: undefined,
    scopes: hasRefreshToken ? [...TOKEN_SCOPES] : [],
    lastRefreshRequestId: undefined,
    lastSafeError: hasRefreshToken
      ? undefined
      : "OAuth refresh token has not been configured.",
  };
  record.credentials.updatedAt = timestamp;
}

// ---------- Import ----------

interface ParsedImportLine {
  emailAddress: string;
  bindingAddress?: string;
  clientIdConfigured: boolean;
  refreshTokenConfigured: boolean;
}

function parseImportLine(line: string): ParsedImportLine | undefined {
  const parts = line.split("----");
  if (![2, 3, 4, 5].includes(parts.length)) return undefined;
  const emailAddress = parts[0]?.trim().toLowerCase() ?? "";
  const hasPassword = (parts[1] ?? "").length > 0;
  if (!hasPassword || !/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(emailAddress)) {
    return undefined;
  }
  if (parts.length === 3 && !(parts[2] ?? "").trim()) return undefined;
  if (parts.length === 4 || parts.length === 5) {
    if (!(parts[2] ?? "").trim() || !(parts[3] ?? "").trim()) return undefined;
  }
  if (parts.length === 5 && !(parts[4] ?? "").trim()) return undefined;
  // The auxiliary mailbox is the 3rd field of a 3-part line, or the 5th field of
  // a 5-part line (email----password----clientID----refreshToken----辅助邮箱).
  const bindingAddress =
    parts.length === 3
      ? parts[2]?.trim().toLowerCase()
      : parts.length === 5
        ? parts[4]?.trim().toLowerCase()
        : undefined;
  return {
    emailAddress,
    bindingAddress: bindingAddress || undefined,
    clientIdConfigured: parts.length === 4 || parts.length === 5,
    refreshTokenConfigured: parts.length === 4 || parts.length === 5,
  };
}

function createImportedRecord(
  parsed: ParsedImportLine,
  owner: AdminMicrosoftOwner,
  longLived: boolean,
  importTask: AdminMicrosoftAsyncTask
) {
  const timestamp = nowIso();
  const aliasCounts: AdminMicrosoftAliasCounts = { explicit: 0, dot: 0, plus: 0 };
  const item: AdminMicrosoftResourceItem = {
    id: nextResourceId++,
    emailAddress: parsed.emailAddress,
    suffix: suffixFromEmail(parsed.emailAddress),
    bindingAddress: parsed.bindingAddress,
    ...ownerSnapshot(owner),
    status: "pending",
    forSale: false,
    longLived,
    graphAvailable: false,
    qualityScore: 50,
    rtExpireAt: parsed.refreshTokenConfigured
      ? new Date(Date.now() + 90 * DAY).toISOString()
      : undefined,
    tokenHealth: parsed.refreshTokenConfigured ? "valid" : "missing",
    aliasCounts,
    explicitAliasCount: 0,
    dotAliasCount: 0,
    plusAliasCount: 0,
    activeTaskStatus: "queued",
    failedTaskCount: 0,
    createdAt: timestamp,
    updatedAt: timestamp,
  };
  const validationTask = createCommandTask("validation", item.id, "queued");
  const record: InternalMicrosoftRecord = {
    item,
    credentials: {
      passwordConfigured: true,
      clientIdConfigured: parsed.clientIdConfigured,
      refreshTokenConfigured: parsed.refreshTokenConfigured,
      revision: 1,
      updatedAt: timestamp,
    },
    token: {
      health: item.tokenHealth,
      rtExpireAt: item.rtExpireAt,
      scopes: parsed.refreshTokenConfigured ? [...TOKEN_SCOPES] : [],
      lastSafeError: parsed.refreshTokenConfigured
        ? undefined
        : "OAuth refresh token has not been configured.",
    },
    aliasSamples: { explicit: [], dot: [], plus: [] },
    aliasSchedule: {
      id: nextScheduleId++,
      status: "pending",
      weekCreated: 0,
      weekLimit: 2,
      yearCreated: 0,
      yearLimit: 10,
      attempts: 0,
      failureStreak: 0,
      nextRunAt: new Date(Date.now() + 12 * HOUR).toISOString(),
      updatedAt: timestamp,
    },
    aliasAttempts: [],
    asyncTasks: {
      validations: [validationTask],
      imports: [{ ...importTask, resourceId: item.id }],
      aliases: [],
      tokens: [],
      fetches: [],
    },
    messages: [],
    auxiliaryMessages: [],
    allocations: [],
  };
  refreshDerivedTaskFields(record);
  return record;
}

function restoreDeletedFromImport(
  record: InternalMicrosoftRecord,
  parsed: ParsedImportLine,
  owner: AdminMicrosoftOwner,
  longLived: boolean,
  importTask: AdminMicrosoftAsyncTask
) {
  setOwner(record, owner);
  record.item.status = "pending";
  record.item.forSale = false;
  record.item.longLived = longLived;
  record.item.graphAvailable = false;
  record.item.qualityScore = 50;
  record.item.lastSafeError = undefined;
  record.item.bindingAddress = parsed.bindingAddress;
  record.credentials = {
    passwordConfigured: true,
    clientIdConfigured: parsed.clientIdConfigured,
    refreshTokenConfigured: parsed.refreshTokenConfigured,
    revision: record.credentials.revision + 1,
    updatedAt: nowIso(),
  };
  setTokenConfigurationAfterCredentialReplacement(
    record,
    parsed.refreshTokenConfigured
  );
  record.asyncTasks.imports.unshift({ ...importTask, resourceId: record.item.id });
  // A re-imported resource starts a fresh lifecycle; prior allocation and mail
  // facts belonged to the deleted resource and must not carry over.
  record.messages = [];
  record.auxiliaryMessages = [];
  record.allocations = [];
  record.item.lastAllocatedAt = undefined;
  resumeAliasSchedule(record);
  enqueueValidation(record);
  touch(record);
}

export async function importAdminMicrosoftResources(
  payload: ImportAdminMicrosoftResourcesRequest
): Promise<AdminMicrosoftImportResponse> {
  await simulateLatency(220);
  const owner = findOwner(payload.ownerId);
  const importId = nextImportId++;
  const requestId = makeRequestId("import");
  const task = createCommandTask("import", undefined, "running", requestId);
  const lines = payload.content.split(/\r?\n/);
  const seen = new Set<string>();
  const accepted: ParsedImportLine[] = [];
  let skipped = 0;
  let firstFailure: string | undefined;

  for (const rawLine of lines) {
    if (!rawLine.trim()) continue;
    const parsed = parseImportLine(rawLine);
    if (!parsed) {
      skipped += 1;
      firstFailure ??= "Import contains an invalid line format.";
      if (payload.errorStrategy === "abort") break;
      continue;
    }
    if (seen.has(parsed.emailAddress)) {
      skipped += 1;
      firstFailure ??= "Import contains a duplicate email address.";
      if (payload.errorStrategy === "abort") break;
      continue;
    }
    seen.add(parsed.emailAddress);
    const existing = dataset.find(
      (record) => record.item.emailAddress.toLowerCase() === parsed.emailAddress
    );
    if (existing && existing.item.status !== "deleted") {
      skipped += 1;
      firstFailure ??= "Import contains an email address that already exists.";
      if (payload.errorStrategy === "abort") break;
      continue;
    }
    accepted.push(parsed);
  }

  if (
    payload.errorStrategy === "abort" &&
    (firstFailure || accepted.length === 0)
  ) {
    const safeError = firstFailure ?? "Import does not contain any valid resource lines.";
    const timestamp = nowIso();
    task.status = "failed";
    task.safeError = safeError;
    task.attempts = 1;
    task.finishedAt = timestamp;
    task.updatedAt = timestamp;
    task.nextRunAt = undefined;
    return clone({
      importId,
      status: "failed" as const,
      accepted: 0,
      imported: 0,
      skipped,
      lastSafeError: safeError,
      task,
    });
  }

  let imported = 0;
  const importedRecords: InternalMicrosoftRecord[] = [];
  for (const parsed of accepted) {
    const existing = dataset.find(
      (record) => record.item.emailAddress.toLowerCase() === parsed.emailAddress
    );
    if (existing?.item.status === "deleted") {
      restoreDeletedFromImport(existing, parsed, owner, payload.longLived, task);
      importedRecords.push(existing);
    } else {
      const created = createImportedRecord(
        parsed,
        owner,
        payload.longLived,
        task
      );
      dataset.unshift(created);
      importedRecords.push(created);
    }
    imported += 1;
  }
  dataset.sort((left, right) => right.item.id - left.item.id);

  const timestamp = nowIso();
  task.status = imported > 0 ? "succeeded" : "failed";
  task.attempts = 1;
  task.safeError =
    imported > 0
      ? skipped > 0
        ? `Import completed; ${skipped} invalid or duplicate line(s) were safely skipped.`
        : undefined
      : firstFailure ?? "Import does not contain any valid resource lines.";
  task.finishedAt = timestamp;
  task.updatedAt = timestamp;
  task.nextRunAt = undefined;

  // The command has completed by the time this mock promise resolves. Keep
  // every resource's safe import-task read model consistent with the response.
  for (const record of importedRecords) {
    const resourceTask = record.asyncTasks.imports.find(
      (candidate) => candidate.requestId === task.requestId
    );
    if (resourceTask) {
      Object.assign(resourceTask, task, { resourceId: record.item.id });
    }
    refreshDerivedTaskFields(record);
  }

  return clone({
    importId,
    status: imported > 0 ? ("imported" as const) : ("failed" as const),
    accepted: accepted.length,
    imported,
    skipped,
    lastSafeError: task.status === "failed" ? task.safeError : undefined,
    task,
  });
}

// ---------- Resource commands ----------

export async function updateAdminMicrosoftResource(
  id: number,
  patch: UpdateAdminMicrosoftResourceRequest
): Promise<AdminMicrosoftResourceItem> {
  await simulateLatency(130);
  const record = findRecord(id);
  if (record.item.status === "deleted") {
    throw new Error("Cannot update a deleted Microsoft resource. Recover it first.");
  }

  const proposedOwner =
    patch.ownerId !== undefined ? findOwner(patch.ownerId) : findOwner(record.item.ownerId);
  const proposedForSale = patch.forSale ?? record.item.forSale;
  if (proposedForSale && (!proposedOwner.enabled || proposedOwner.role === "user")) {
    throw new Error("Public-sale resources require an enabled supplier or administrator owner.");
  }

  // The email address is the resource identity; an administrator may correct it
  // as a safety fallback, but format and cross-resource uniqueness still hold.
  if (patch.emailAddress !== undefined) {
    const nextEmail = patch.emailAddress.trim().toLowerCase();
    if (!/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(nextEmail)) {
      throw new Error("A valid Microsoft email address is required.");
    }
    if (nextEmail !== record.item.emailAddress.toLowerCase()) {
      const clash = dataset.find(
        (entry) =>
          entry.item.id !== record.item.id &&
          entry.item.status !== "deleted" &&
          entry.item.emailAddress.toLowerCase() === nextEmail
      );
      if (clash) {
        throw new Error("Another Microsoft resource already uses this email address.");
      }
      record.item.emailAddress = nextEmail;
      record.item.suffix = suffixFromEmail(nextEmail);
    }
  }

  if (patch.bindingAddress !== undefined) {
    const nextBinding = (patch.bindingAddress ?? "").trim();
    if (nextBinding && !/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(nextBinding)) {
      throw new Error("A valid auxiliary mailbox address is required.");
    }
    record.item.bindingAddress = nextBinding.toLowerCase() || undefined;
  }

  if (patch.ownerId !== undefined) setOwner(record, proposedOwner);
  if (patch.forSale !== undefined) record.item.forSale = patch.forSale;
  if (patch.longLived !== undefined) record.item.longLived = patch.longLived;
  if (patch.qualityScore !== undefined) {
    record.item.qualityScore = Math.max(0, Math.min(100, Math.round(patch.qualityScore)));
  }
  if (patch.graphAvailable !== undefined) {
    record.item.graphAvailable = patch.graphAvailable;
  }
  if (patch.status !== undefined) {
    record.item.status = patch.status;
    if (patch.status === "disabled") {
      pauseAliasSchedule(record, "资源已禁用，显式别名补货已暂停。");
      failActiveTasks(record, "Resource task stopped because an administrator disabled it.");
    } else if (patch.status === "normal") {
      record.item.lastSafeError = undefined;
      resumeAliasSchedule(record);
    } else if (patch.status === "pending") {
      resumeAliasSchedule(record);
      enqueueValidation(record);
    } else {
      pauseAliasSchedule(record, "资源当前非正常状态，等待验证恢复。");
    }
  }
  touch(record);
  return cloneItem(record);
}

export async function replaceAdminMicrosoftCredentials(
  id: number,
  payload: ReplaceAdminMicrosoftCredentialsRequest
): Promise<AdminMicrosoftResourceDetail> {
  await simulateLatency(180);
  const record = findRecord(id);
  if (record.item.status === "deleted") {
    throw new Error("Cannot replace credentials on a deleted resource. Recover it first.");
  }
  if (!payload.password || payload.password.length === 0) {
    throw new Error("Microsoft account password is required.");
  }

  const clientIdConfigured = Boolean(payload.clientId?.trim());
  const refreshTokenConfigured = Boolean(payload.refreshToken?.trim());
  if (clientIdConfigured !== refreshTokenConfigured) {
    throw new Error("OAuth client ID and refresh token must be configured together.");
  }

  record.credentials.passwordConfigured = true;
  record.credentials.clientIdConfigured = clientIdConfigured;
  record.credentials.refreshTokenConfigured = refreshTokenConfigured;
  record.credentials.revision += 1;
  setTokenConfigurationAfterCredentialReplacement(record, refreshTokenConfigured);
  record.item.status = "pending";
  record.item.graphAvailable = false;
  record.item.lastSafeError = undefined;
  resumeAliasSchedule(record);
  enqueueValidation(record);
  touch(record);
  return buildDetail(record);
}

export async function validateAdminMicrosoftResource(
  id: number
): Promise<AdminMicrosoftValidationResponse> {
  await simulateLatency(170);
  const record = findRecord(id);
  if (record.item.status === "deleted") {
    throw new Error("Cannot validate a deleted Microsoft resource.");
  }
  record.item.status = "pending";
  record.item.lastSafeError = undefined;
  resumeAliasSchedule(record);
  const task = enqueueValidation(record);
  touch(record);
  return {
    queued: 1,
    requestId: task.requestId,
    validationIds: [task.id],
    resourceIds: [record.item.id],
  };
}

// ---------- Manual per-resource task submissions ----------

export async function refreshAdminMicrosoftToken(
  id: number
): Promise<AdminMicrosoftAsyncTask> {
  await simulateLatency(150);
  const record = findRecord(id);
  if (record.item.status === "deleted") {
    throw new Error("Cannot submit a task for a deleted Microsoft resource.");
  }
  return clone(enqueueManualTask(record, "token", record.asyncTasks.tokens));
}

export async function createAdminMicrosoftExplicitAlias(
  id: number
): Promise<AdminMicrosoftAsyncTask> {
  await simulateLatency(150);
  const record = findRecord(id);
  if (record.item.status === "deleted") {
    throw new Error("Cannot submit a task for a deleted Microsoft resource.");
  }
  return clone(enqueueManualTask(record, "alias", record.asyncTasks.aliases));
}

export async function fetchAdminMicrosoftMail(
  id: number
): Promise<AdminMicrosoftAsyncTask> {
  await simulateLatency(150);
  const record = findRecord(id);
  if (record.item.status === "deleted") {
    throw new Error("Cannot submit a task for a deleted Microsoft resource.");
  }
  return clone(enqueueManualTask(record, "fetch", record.asyncTasks.fetches));
}

function validateRecords(
  records: InternalMicrosoftRecord[],
  includeIds: boolean
): AdminMicrosoftValidationResponse {
  const requestId = makeRequestId("validation_batch");
  const validationIds = new Set<number>();
  const resourceIds: number[] = [];
  for (const record of records) {
    if (record.item.status === "deleted") continue;
    record.item.status = "pending";
    record.item.lastSafeError = undefined;
    resumeAliasSchedule(record);
    const task = enqueueValidation(record);
    validationIds.add(task.id);
    resourceIds.push(record.item.id);
    touch(record);
  }
  return {
    queued: resourceIds.length,
    requestId,
    validationIds: includeIds ? Array.from(validationIds) : undefined,
    resourceIds: includeIds ? resourceIds : undefined,
  };
}

export async function validateAdminMicrosoftResourcesByIds(
  ids: number[]
): Promise<AdminMicrosoftValidationResponse> {
  await simulateLatency(210);
  const unique = new Set(ids.filter((id) => Number.isInteger(id) && id > 0));
  const records = dataset.filter((record) => unique.has(record.item.id));
  return clone(validateRecords(records, true));
}

export async function validateAdminMicrosoftResourcesByFilter(
  filter: AdminMicrosoftListFilter
): Promise<AdminMicrosoftValidationResponse> {
  await simulateLatency(240);
  const records = dataset.filter(
    (record) =>
      record.item.status !== "deleted" && matchesFilter(record.item, filter)
  );
  return clone(validateRecords(records, false));
}

function disableRecord(record: InternalMicrosoftRecord) {
  if (record.item.status === "deleted" || record.item.status === "disabled") {
    return false;
  }
  record.item.status = "disabled";
  pauseAliasSchedule(record, "资源已禁用，显式别名补货已暂停。");
  failActiveTasks(record, "Resource task stopped because an administrator disabled it.");
  touch(record);
  return true;
}

export async function disableAdminMicrosoftResourcesByIds(
  ids: number[]
): Promise<AdminMicrosoftBulkResponse> {
  await simulateLatency(190);
  const unique = new Set(ids.filter((id) => Number.isInteger(id) && id > 0));
  const resourceIds: number[] = [];
  for (const record of dataset) {
    if (unique.has(record.item.id) && disableRecord(record)) {
      resourceIds.push(record.item.id);
    }
  }
  return { affected: resourceIds.length, resourceIds };
}

// Publishing a resource for public sale requires an eligible owner; converting
// back to private is an administrator command with no owner constraint. Bulk
// commands idempotently skip resources that already hold the target flag or are
// deleted, matching the publish/delete bulk semantics from the design docs.
function setForSaleRecord(record: InternalMicrosoftRecord, forSale: boolean) {
  if (record.item.status === "deleted") return false;
  if (record.item.forSale === forSale) return false;
  if (forSale) {
    const owner = findOwner(record.item.ownerId);
    if (!owner.enabled || owner.role === "user") return false;
  }
  record.item.forSale = forSale;
  touch(record);
  return true;
}

export async function setAdminMicrosoftResourcesForSaleByIds(
  ids: number[],
  forSale: boolean
): Promise<AdminMicrosoftBulkResponse> {
  await simulateLatency(190);
  const unique = new Set(ids.filter((id) => Number.isInteger(id) && id > 0));
  const resourceIds: number[] = [];
  for (const record of dataset) {
    if (unique.has(record.item.id) && setForSaleRecord(record, forSale)) {
      resourceIds.push(record.item.id);
    }
  }
  return { affected: resourceIds.length, resourceIds };
}

export async function setAdminMicrosoftResourcesForSaleByFilter(
  filter: AdminMicrosoftListFilter,
  forSale: boolean
): Promise<AdminMicrosoftBulkResponse> {
  await simulateLatency(240);
  const selected = dataset.filter(
    (record) =>
      record.item.status !== "deleted" && matchesFilter(record.item, filter)
  );
  let affected = 0;
  for (const record of selected) {
    if (setForSaleRecord(record, forSale)) affected += 1;
  }
  // Real filter mode intentionally omits a potentially huge ID list.
  return { affected };
}

function deleteRecord(record: InternalMicrosoftRecord) {
  if (record.item.status === "deleted") return false;
  record.item.status = "deleted";
  record.item.forSale = false;
  record.item.graphAvailable = false;
  pauseAliasSchedule(record, "资源已删除，显式别名补货已暂停。");
  failActiveTasks(record, "Resource task stopped because the resource was deleted.");
  touch(record);
  return true;
}

export async function deleteAdminMicrosoftResource(id: number): Promise<void> {
  await simulateLatency(140);
  deleteRecord(findRecord(id));
}

export async function deleteAdminMicrosoftResourcesByIds(
  ids: number[]
): Promise<AdminMicrosoftBulkResponse> {
  await simulateLatency(210);
  const unique = new Set(ids.filter((id) => Number.isInteger(id) && id > 0));
  const resourceIds: number[] = [];
  for (const record of dataset) {
    if (unique.has(record.item.id) && deleteRecord(record)) {
      resourceIds.push(record.item.id);
    }
  }
  return { affected: resourceIds.length, resourceIds };
}

export async function deleteAdminMicrosoftResourcesByFilter(
  filter: AdminMicrosoftListFilter
): Promise<AdminMicrosoftBulkResponse> {
  await simulateLatency(250);
  const selected = dataset.filter(
    (record) =>
      record.item.status !== "deleted" && matchesFilter(record.item, filter)
  );
  let affected = 0;
  for (const record of selected) {
    if (deleteRecord(record)) affected += 1;
  }
  // Real filter mode intentionally omits a potentially huge ID list.
  return { affected };
}

export async function recoverAdminMicrosoftResource(
  id: number
): Promise<AdminMicrosoftResourceItem> {
  await simulateLatency(170);
  const record = findRecord(id);
  if (record.item.status !== "deleted") {
    throw new Error("Only deleted Microsoft resources can be recovered.");
  }
  record.item.status = "pending";
  record.item.forSale = false;
  record.item.graphAvailable = false;
  record.item.lastSafeError = undefined;
  resumeAliasSchedule(record);
  enqueueValidation(record);
  touch(record);
  return cloneItem(record);
}
