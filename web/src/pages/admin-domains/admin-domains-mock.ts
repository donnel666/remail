// Mock admin domain-email API for the site-wide admin management page.
//
// The surface mirrors the admin resource/domain endpoints documented in
// docs/1-core-mailbox-project.md section 6 so that swapping to the real backend
// later only needs to replace these imports with an admin-domains-api module:
//
//   listAdminDomains        -> GET    /v1/admin/domains
//   getAdminDomainDetail    -> GET    /v1/admin/domains/{domainId}
//                              (+ GET  /v1/admin/domains/{domainId}/mailboxes)
//   createAdminDomain       -> POST   /v1/admin/domains
//   updateAdminDomain       -> PATCH  /v1/admin/resources/{resourceId}
//   validateAdminDomain     -> POST   /v1/admin/resources/{resourceId}/validate
//   validateAdminDomains*   -> POST   /v1/admin/resources/validations
//   queueAdminDomainMailFetch -> POST /v1/admin/domains/{domainId}/mail-fetch
//   deleteAdminDomain       -> DELETE /v1/admin/resources/{resourceId}
//   deleteAdminDomains*     -> POST   /v1/admin/resources/delete
//   recoverAdminDomain      -> POST   /v1/admin/resources/{resourceId}/recover
//   listAdminDomainOwners   -> GET    /v1/admin/users
//
// Every domain-mutating command in the real backend writes an OperationLog;
// pure queries do not. That side effect is intentionally out of scope here.

export type AdminDomainPurpose = "not_sale" | "sale" | "binding";
export type AdminDomainStatus = "normal" | "abnormal" | "disabled" | "deleted";
export type AdminMailServerStatus = "online" | "offline" | "disabled";
export type AdminMailboxStatus = "normal" | "disabled";
export type AdminOwnerRole = "user" | "supplier" | "admin" | "super_admin";

export interface AdminDomainOwner {
  id: number;
  email: string;
  nickname: string;
  role: AdminOwnerRole;
}

export interface AdminMailServer {
  id: number;
  name: string;
  ownerId: number;
  serverAddress: string;
  mxRecord: string;
  spf: string;
  dkim: string;
  dmarc: string;
  ptr: string;
  status: AdminMailServerStatus;
}

export interface AdminGeneratedMailbox {
  id: number;
  email: string;
  status: AdminMailboxStatus;
  lastAllocatedAt?: string;
  createdAt: string;
}

export interface AdminDomainOrder {
  id: number;
  orderNo: string;
  projectName: string;
  projectLogoUrl?: string;
  deliveryEmail: string;
  supplyScope: "owned" | "public";
  serviceMode: "code" | "purchase";
  orderStatus: "paid" | "active" | "completed" | "refunded";
  allocationStatus: "allocated" | "released";
  buyerEmail: string;
  payAmount: string;
  verificationCode?: string;
  receiveUntil?: string;
  createdAt: string;
}

export interface AdminDomainTask {
  id: number;
  kind: "validation" | "alias_replenishment" | "mail_fetch";
  status: "queued" | "running" | "succeeded" | "failed";
  remainingAttempts: number;
  queuedAt: string;
  startedAt?: string;
  finishedAt?: string;
  updatedAt: string;
  lastSafeError?: string;
}

export interface AdminDomainMessage {
  id: number;
  recipient: string;
  sender: string;
  subject: string;
  status: "received" | "matched" | "ignored";
  verificationCode?: string;
  orderNo?: string;
  receivedAt: string;
  preview: string;
  body: string;
}

export interface AdminDomainItem {
  id: number;
  domain: string;
  domainTld: string;
  ownerId: number;
  ownerEmail: string;
  ownerNickname: string;
  ownerRole: AdminOwnerRole;
  purpose: AdminDomainPurpose;
  status: AdminDomainStatus;
  mailServerId: number;
  mailboxCount: number;
  lastSafeError?: string;
  lastAllocatedAt?: string;
  createdAt: string;
  updatedAt: string;
}

export interface AdminDomainDetail extends AdminDomainItem {
  mailServer: AdminMailServer;
  mailboxes: AdminGeneratedMailbox[];
  messages: AdminDomainMessage[];
  orders: AdminDomainOrder[];
  tasks: AdminDomainTask[];
}

export interface AdminDomainListFilter {
  search?: string;
  status?: AdminDomainStatus | "all";
  purpose?: AdminDomainPurpose | "all";
  tld?: string;
  ownerId?: number;
  createdFrom?: string;
  createdTo?: string;
}

export interface AdminDomainStatusFacet {
  all: number;
  normal: number;
  abnormal: number;
  disabled: number;
  deleted: number;
}

export interface AdminDomainPurposeFacet {
  all: number;
  not_sale: number;
  sale: number;
  binding: number;
}

export interface AdminDomainFacets {
  status: AdminDomainStatusFacet;
  purpose: AdminDomainPurposeFacet;
  tlds: { key: string; count: number }[];
}

export interface AdminDomainListResponse {
  items: AdminDomainItem[];
  total: number;
  offset: number;
  limit: number;
  nextAfterId?: number;
  facets: AdminDomainFacets;
}

export interface CreateAdminDomainRequest {
  domain: string;
  ownerId: number;
  purpose: AdminDomainPurpose;
  mailServerId?: number;
}

export interface UpdateAdminDomainRequest {
  ownerId?: number;
  purpose?: AdminDomainPurpose;
  // normal is used by "check passed"; disabled disables the domain; abnormal is
  // the state a disabled domain returns to when re-enabled (pending re-check).
  status?: Extract<AdminDomainStatus, "normal" | "abnormal" | "disabled">;
  mailServerId?: number;
}

export interface AdminDomainBulkResponse {
  affected: number;
}

export interface AdminDomainValidationResponse {
  queued: number;
}

// ---------- Deterministic pseudo-random data ----------

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

const random = mulberry32(0x5eed_d011);

function pick<T>(items: readonly T[]): T {
  return items[Math.floor(random() * items.length)];
}

function randomInt(min: number, max: number) {
  return min + Math.floor(random() * (max - min + 1));
}

const DAY = 86_400_000;

const OWNERS: AdminDomainOwner[] = [
  { id: 1, email: "root@aishop6.com", nickname: "平台超管", role: "super_admin" },
  { id: 2, email: "ops@aishop6.com", nickname: "运营管理员", role: "admin" },
  { id: 101, email: "starlink@outlook.com", nickname: "星链数码", role: "supplier" },
  { id: 102, email: "cloudsail@gmail.com", nickname: "云帆账号行", role: "supplier" },
  { id: 103, email: "aurora@proton.me", nickname: "极光资源社", role: "supplier" },
  { id: 104, email: "dolphin@hotmail.com", nickname: "海豚小铺", role: "supplier" },
  { id: 105, email: "nebula@qq.com", nickname: "星云批发", role: "supplier" },
  { id: 501, email: "nova.li@gmail.com", nickname: "Nova", role: "user" },
  { id: 502, email: "kite.wang@outlook.com", nickname: "Kite", role: "user" },
  { id: 503, email: "lumen.zhao@163.com", nickname: "Lumen", role: "user" },
  { id: 504, email: "orbit.chen@foxmail.com", nickname: "Orbit", role: "user" },
  { id: 505, email: "raven.wu@hotmail.com", nickname: "Raven", role: "user" },
];

const MAIL_SERVERS: AdminMailServer[] = [
  {
    id: 1,
    name: "内置本机入站",
    ownerId: 1,
    serverAddress: "mail.aishop6.com",
    mxRecord: "mx.aishop6.com",
    spf: "v=spf1 include:aishop6.com ~all",
    dkim: "v=DKIM1; k=rsa; p=MIGfMA0GCS...builtin",
    dmarc: "v=DMARC1; p=quarantine; rua=mailto:dmarc@aishop6.com",
    ptr: "mx.aishop6.com",
    status: "online",
  },
  {
    id: 2,
    name: "星链自建邮局",
    ownerId: 101,
    serverAddress: "mail.starlink-mx.com",
    mxRecord: "mx.starlink-mx.com",
    spf: "v=spf1 ip4:45.32.10.0/24 ~all",
    dkim: "v=DKIM1; k=rsa; p=MIGfMA0GCS...starlink",
    dmarc: "v=DMARC1; p=none; rua=mailto:dmarc@starlink-mx.com",
    ptr: "mx.starlink-mx.com",
    status: "online",
  },
  {
    id: 3,
    name: "云帆自建邮局",
    ownerId: 102,
    serverAddress: "mail.cloudsail-mx.net",
    mxRecord: "mx.cloudsail-mx.net",
    spf: "v=spf1 ip4:103.21.8.0/24 ~all",
    dkim: "v=DKIM1; k=rsa; p=MIGfMA0GCS...cloudsail",
    dmarc: "v=DMARC1; p=reject; rua=mailto:dmarc@cloudsail-mx.net",
    ptr: "mx.cloudsail-mx.net",
    status: "offline",
  },
  {
    id: 4,
    name: "极光自建邮局",
    ownerId: 103,
    serverAddress: "mail.aurora-mx.io",
    mxRecord: "mx.aurora-mx.io",
    spf: "v=spf1 ip4:78.46.12.0/24 ~all",
    dkim: "v=DKIM1; k=rsa; p=MIGfMA0GCS...aurora",
    dmarc: "v=DMARC1; p=quarantine; rua=mailto:dmarc@aurora-mx.io",
    ptr: "mx.aurora-mx.io",
    status: "disabled",
  },
];

const TLDS = ["com", "net", "org", "io", "top", "vip", "xyz", "shop", "cc", "info"];
const DOMAIN_HEADS = [
  "nova", "kite", "lumen", "orbit", "pine", "quartz", "raven", "sable",
  "tidal", "umber", "vesper", "willow", "zephyr", "cobalt", "ember", "flint",
  "harbor", "indigo", "juniper", "lyra", "maple", "onyx", "petal", "reef",
];
const DOMAIN_TAILS = [
  "mail", "post", "inbox", "relay", "hub", "box", "net", "zone",
  "cloud", "link", "port", "gate", "wave", "peak", "star", "loop",
];

const ABNORMAL_ERRORS = [
  "MX 记录未指向 mx.aishop6.com",
  "DNS 解析超时，未获取到有效 MX",
  "SPF 记录缺失，出站可能被拒收",
  "入站服务器暂不可达",
  "DKIM 校验未通过",
];

function serverForOwner(owner: AdminDomainOwner): AdminMailServer {
  const owned = MAIL_SERVERS.find((server) => server.ownerId === owner.id);
  return owned ?? MAIL_SERVERS[0];
}

function buildDataset(): AdminDomainItem[] {
  const now = Date.now();
  const items: AdminDomainItem[] = [];
  const usedDomains = new Set<string>();
  let id = 4200;

  const total = 214;
  for (let index = 0; index < total; index += 1) {
    const owner = pick(OWNERS);
    const tld = pick(TLDS);
    let domain = `${pick(DOMAIN_HEADS)}${pick(DOMAIN_TAILS)}${randomInt(1, 99)}.${tld}`;
    if (usedDomains.has(domain)) {
      domain = `${pick(DOMAIN_HEADS)}${randomInt(100, 999)}.${tld}`;
    }
    if (usedDomains.has(domain)) continue;
    usedDomains.add(domain);

    // Purpose weighting: binding is admin-only auxiliary mailbox usage.
    const purposeRoll = random();
    const purpose: AdminDomainPurpose =
      purposeRoll < 0.44 ? "not_sale" : purposeRoll < 0.84 ? "sale" : "binding";

    // Status weighting includes a healthy chunk of "deleted" so the admin
    // recovery flow always has trash to restore.
    const statusRoll = random();
    const status: AdminDomainStatus =
      statusRoll < 0.5
        ? "normal"
        : statusRoll < 0.72
          ? "abnormal"
          : statusRoll < 0.85
            ? "disabled"
            : "deleted";

    const server =
      purpose === "binding" ? MAIL_SERVERS[0] : serverForOwner(owner);
    const createdAt = now - randomInt(1, 180) * DAY - randomInt(0, DAY);
    const mailboxCount =
      status === "deleted"
        ? 0
        : purpose === "binding"
          ? randomInt(0, 40)
          : randomInt(0, 1800);
    const lastAllocatedAt =
      status === "normal" && mailboxCount > 0 && random() < 0.7
        ? new Date(createdAt + randomInt(1, 150) * DAY).toISOString()
        : undefined;

    items.push({
      id,
      domain,
      domainTld: tld,
      ownerId: owner.id,
      ownerEmail: owner.email,
      ownerNickname: owner.nickname,
      ownerRole: owner.role,
      purpose,
      status,
      mailServerId: server.id,
      mailboxCount,
      lastSafeError: status === "abnormal" ? pick(ABNORMAL_ERRORS) : undefined,
      lastAllocatedAt,
      createdAt: new Date(createdAt).toISOString(),
      updatedAt: new Date(createdAt + randomInt(0, 30) * DAY).toISOString(),
    });
    id += 1;
  }

  items.sort((left, right) => right.id - left.id);
  return items;
}

const dataset = buildDataset();
let nextDomainId = 4200 + dataset.length + 1;
let nextMailboxId = 900_000;
let nextDomainTaskId = 1_000_000;
const queuedTasksByDomainId = new Map<number, AdminDomainTask[]>();

// ---------- Filtering / facets ----------

type FacetDimension = "status" | "purpose" | "tld";

function matchesFilter(
  item: AdminDomainItem,
  filter: AdminDomainListFilter,
  ignore: FacetDimension[] = []
) {
  const skip = new Set(ignore);

  // Status: "all" excludes the deleted trash; "deleted" shows only trash.
  if (!skip.has("status")) {
    const status = filter.status ?? "all";
    if (status === "all") {
      if (item.status === "deleted") return false;
    } else if (item.status !== status) {
      return false;
    }
  }

  if (!skip.has("purpose")) {
    const purpose = filter.purpose ?? "all";
    if (purpose !== "all" && item.purpose !== purpose) return false;
  }

  if (!skip.has("tld") && filter.tld && item.domainTld !== filter.tld) {
    return false;
  }

  if (filter.ownerId && item.ownerId !== filter.ownerId) return false;

  if (filter.createdFrom) {
    if (new Date(item.createdAt) < new Date(filter.createdFrom)) return false;
  }
  if (filter.createdTo) {
    if (new Date(item.createdAt) > new Date(filter.createdTo)) return false;
  }

  const search = filter.search?.trim().toLowerCase();
  if (search) {
    const haystack = [
      item.domain,
      item.domainTld,
      item.ownerEmail,
      item.ownerNickname,
      String(item.id),
      String(item.ownerId),
    ]
      .join(" ")
      .toLowerCase();
    if (!haystack.includes(search)) return false;
  }

  return true;
}

function buildFacets(filter: AdminDomainListFilter): AdminDomainFacets {
  const statusScope = dataset.filter((item) =>
    matchesFilter(item, filter, ["status", "tld"])
  );
  const status: AdminDomainStatusFacet = {
    all: 0,
    normal: 0,
    abnormal: 0,
    disabled: 0,
    deleted: 0,
  };
  for (const item of statusScope) {
    if (item.status === "deleted") {
      status.deleted += 1;
    } else {
      status.all += 1;
      status[item.status] += 1;
    }
  }

  const purposeScope = dataset.filter((item) =>
    matchesFilter(item, filter, ["purpose", "tld"])
  );
  const purpose: AdminDomainPurposeFacet = {
    all: purposeScope.length,
    not_sale: 0,
    sale: 0,
    binding: 0,
  };
  for (const item of purposeScope) purpose[item.purpose] += 1;

  const tldScope = dataset.filter((item) => matchesFilter(item, filter, ["tld"]));
  const tldCounts = new Map<string, number>();
  for (const item of tldScope) {
    tldCounts.set(item.domainTld, (tldCounts.get(item.domainTld) ?? 0) + 1);
  }
  const tlds = Array.from(tldCounts.entries())
    .map(([key, count]) => ({ key, count }))
    .sort((left, right) => right.count - left.count || left.key.localeCompare(right.key));

  return { status, purpose, tlds };
}

function simulateLatency(base = 200) {
  return new Promise((resolve) =>
    globalThis.setTimeout(resolve, base + Math.random() * 240)
  );
}

function cloneItem(item: AdminDomainItem): AdminDomainItem {
  return { ...item };
}

function findItem(id: number) {
  const item = dataset.find((entry) => entry.id === id);
  if (!item) throw new Error("Domain resource not found.");
  return item;
}

function refreshOwnerSnapshot(item: AdminDomainItem, ownerId: number) {
  const owner = OWNERS.find((entry) => entry.id === ownerId);
  if (!owner) return;
  item.ownerId = owner.id;
  item.ownerEmail = owner.email;
  item.ownerNickname = owner.nickname;
  item.ownerRole = owner.role;
}

function tldFromDomain(domain: string) {
  const parts = domain.split(".");
  if (parts.length <= 2) return parts[parts.length - 1] ?? "";
  const secondLevel = new Set(["co", "com", "net", "org", "gov", "edu"]);
  const last = parts[parts.length - 1];
  const second = parts[parts.length - 2];
  return secondLevel.has(second) ? `${second}.${last}` : last;
}

// ---------- Queries ----------

export async function listAdminDomains(
  filter: AdminDomainListFilter = {},
  offset = 0,
  limit = 20,
  afterId?: number
): Promise<AdminDomainListResponse> {
  await simulateLatency();
  const filtered = dataset.filter((item) => matchesFilter(item, filter));
  const startIndex = afterId
    ? filtered.findIndex((item) => item.id < afterId)
    : offset;
  const safeStart = startIndex < 0 ? filtered.length : startIndex;
  const pageItems = filtered.slice(safeStart, safeStart + limit).map(cloneItem);
  const lastItem = pageItems[pageItems.length - 1];
  return {
    items: pageItems,
    total: filtered.length,
    offset: safeStart,
    limit,
    nextAfterId:
      lastItem && safeStart + pageItems.length < filtered.length
        ? lastItem.id
        : undefined,
    facets: buildFacets(filter),
  };
}

function buildMailboxes(item: AdminDomainItem): AdminGeneratedMailbox[] {
  if (item.mailboxCount <= 0) return [];
  const seedRandom = mulberry32(0xa11c_0000 ^ item.id);
  const sampleSize = Math.min(item.mailboxCount, 60);
  const createdBase = new Date(item.createdAt).getTime();
  const mailboxes: AdminGeneratedMailbox[] = [];
  const used = new Set<string>();
  for (let index = 0; index < sampleSize; index += 1) {
    const head = DOMAIN_HEADS[Math.floor(seedRandom() * DOMAIN_HEADS.length)];
    const tail = DOMAIN_TAILS[Math.floor(seedRandom() * DOMAIN_TAILS.length)];
    const suffixNumber = Math.floor(seedRandom() * 9000) + 100;
    const local = `${head}.${tail}${suffixNumber}`;
    const email = `${local}@${item.domain}`;
    if (used.has(email)) continue;
    used.add(email);
    const disabled = seedRandom() < 0.12;
    const allocated = seedRandom() < 0.6;
    mailboxes.push({
      id: nextMailboxId + index,
      email,
      status: disabled ? "disabled" : "normal",
      lastAllocatedAt: allocated
        ? new Date(createdBase + Math.floor(seedRandom() * 150) * DAY).toISOString()
        : undefined,
      createdAt: new Date(
        createdBase + Math.floor(seedRandom() * 30) * DAY
      ).toISOString(),
    });
  }
  return mailboxes;
}

function buildOrders(
  item: AdminDomainItem,
  mailboxes: AdminGeneratedMailbox[]
): AdminDomainOrder[] {
  const random = mulberry32(0x0d03_0000 ^ item.id);
  const base = new Date(item.createdAt).getTime();
  return mailboxes.slice(0, 6).map((mailbox, index) => ({
    id: item.id * 100 + index + 1,
    orderNo: `DM${item.id.toString().padStart(5, "0")}${index + 1}`,
    projectName: ["注册验证", "账号养号", "营销活动"][index % 3],
    deliveryEmail: mailbox.email,
    supplyScope: item.purpose === "sale" ? "public" : "owned",
    serviceMode: index % 2 === 0 ? "code" : "purchase",
    orderStatus:
      index === 5
        ? "refunded"
        : index % 3 === 0
          ? "active"
          : "completed",
    allocationStatus: index === 5 ? "released" : "allocated",
    buyerEmail: `buyer${Math.floor(random() * 900) + 100}@example.com`,
    payAmount: (Math.floor(random() * 4900) / 100 + 1).toFixed(2),
    verificationCode:
      index % 2 === 0
        ? String(Math.floor(random() * 900_000) + 100_000)
        : undefined,
    createdAt: new Date(base + (index + 1) * DAY * 6).toISOString(),
    receiveUntil: new Date(base + (index + 1) * DAY * 6 + DAY).toISOString(),
  }));
}

function buildTasks(item: AdminDomainItem): AdminDomainTask[] {
  const base = new Date(item.updatedAt).getTime();
  const firstQueued = new Date(base - DAY * 2).toISOString();
  const secondQueued = new Date(base - DAY).toISOString();
  const thirdQueued = new Date(base - 3_600_000).toISOString();
  return [
    {
      id: item.id * 10 + 1,
      kind: "validation",
      status: item.status === "abnormal" ? "failed" : "succeeded",
      remainingAttempts: item.status === "abnormal" ? 2 : 3,
      queuedAt: firstQueued,
      startedAt: new Date(base - DAY * 2 + 10_000).toISOString(),
      finishedAt: new Date(base - DAY * 2 + 40_000).toISOString(),
      updatedAt: new Date(base - DAY * 2 + 40_000).toISOString(),
      lastSafeError: item.status === "abnormal" ? item.lastSafeError : undefined,
    },
    {
      id: item.id * 10 + 2,
      kind: "alias_replenishment",
      status: item.mailboxCount > 0 ? "succeeded" : "queued",
      remainingAttempts: 3,
      queuedAt: secondQueued,
      startedAt:
        item.mailboxCount > 0
          ? new Date(base - DAY + 5_000).toISOString()
          : undefined,
      finishedAt:
        item.mailboxCount > 0
          ? new Date(base - DAY + 20_000).toISOString()
          : undefined,
      updatedAt: new Date(base - DAY + 20_000).toISOString(),
    },
    {
      id: item.id * 10 + 3,
      kind: "mail_fetch",
      status: "succeeded",
      remainingAttempts: 3,
      queuedAt: thirdQueued,
      startedAt: new Date(base - 3_590_000).toISOString(),
      finishedAt: new Date(base - 3_570_000).toISOString(),
      updatedAt: new Date(base - 3_570_000).toISOString(),
    },
  ];
}

function buildMessages(
  item: AdminDomainItem,
  mailboxes: AdminGeneratedMailbox[]
): AdminDomainMessage[] {
  const senders = ["no-reply@accounts.example", "verify@service.example", "support@platform.example"];
  const subjects = ["验证码：483921", "账户安全提醒", "欢迎使用服务"];
  const base = new Date(item.updatedAt).getTime();
  return mailboxes.slice(0, 12).map((mailbox, index) => ({
    id: item.id * 1000 + index + 1,
    recipient: mailbox.email,
    sender: senders[index % senders.length],
    subject: subjects[index % subjects.length],
    status: index % 3 === 0 ? "matched" : "received",
    verificationCode: index % 3 === 0 ? String(483921 + index) : undefined,
    orderNo: index % 2 === 0 ? `DM${item.id.toString().padStart(5, "0")}1` : undefined,
    receivedAt: new Date(base - index * 2_700_000).toISOString(),
    preview: `这是一封发送至 ${mailbox.email} 的域名邮箱 Mock 邮件。`,
    body: `这是一封发送至 ${mailbox.email} 的域名邮箱 Mock 邮件。邮件内容仅用于管理员页面预览。`,
  }));
}

export async function getAdminDomainDetail(
  id: number
): Promise<AdminDomainDetail> {
  await simulateLatency(140);
  const item = findItem(id);
  const mailServer =
    MAIL_SERVERS.find((server) => server.id === item.mailServerId) ??
    MAIL_SERVERS[0];
  const mailboxes = buildMailboxes(item);
  const queuedTasks = queuedTasksByDomainId.get(id) ?? [];
  return {
    ...cloneItem(item),
    mailServer: { ...mailServer },
    mailboxes,
    messages: buildMessages(item, mailboxes),
    orders: buildOrders(item, mailboxes),
    tasks: [
      ...queuedTasks.map((task) => ({ ...task })),
      ...buildTasks(item),
    ],
  };
}

export async function listAdminDomainMailboxes(
  id: number
): Promise<{ items: AdminGeneratedMailbox[]; total: number }> {
  await simulateLatency(140);
  const item = findItem(id);
  const mailboxes = buildMailboxes(item);
  return { items: mailboxes, total: item.mailboxCount };
}

export async function listAdminMailServers(): Promise<AdminMailServer[]> {
  await simulateLatency(100);
  return MAIL_SERVERS.map((server) => ({ ...server }));
}

export async function listAdminDomainOwners(
  search = ""
): Promise<AdminDomainOwner[]> {
  await simulateLatency(120);
  const keyword = search.trim().toLowerCase();
  const matched = keyword
    ? OWNERS.filter((owner) =>
        [owner.email, owner.nickname, String(owner.id)]
          .join(" ")
          .toLowerCase()
          .includes(keyword)
      )
    : OWNERS;
  return matched.map((owner) => ({ ...owner }));
}

// ---------- Commands ----------

export async function createAdminDomain(
  payload: CreateAdminDomainRequest
): Promise<AdminDomainItem> {
  await simulateLatency();
  const domain = payload.domain.trim().toLowerCase().replace(/\.$/, "");
  if (!domain) throw new Error("Domain is required.");

  const owner =
    OWNERS.find((entry) => entry.id === payload.ownerId) ?? OWNERS[0];

  // Re-creating a deleted domain restores it in place (reuses the resource id
  // and resets the generated mailbox pool), matching the documented behavior.
  const existing = dataset.find((entry) => entry.domain === domain);
  if (existing) {
    if (existing.status !== "deleted") {
      throw new Error("Domain already exists.");
    }
    existing.status = "abnormal";
    existing.purpose = payload.purpose;
    existing.mailServerId = payload.mailServerId ?? MAIL_SERVERS[0].id;
    existing.mailboxCount = 0;
    existing.lastSafeError = undefined;
    existing.lastAllocatedAt = undefined;
    existing.updatedAt = new Date().toISOString();
    refreshOwnerSnapshot(existing, owner.id);
    return cloneItem(existing);
  }

  const server =
    MAIL_SERVERS.find((entry) => entry.id === payload.mailServerId) ??
    serverForOwner(owner);
  const nowIso = new Date().toISOString();
  const item: AdminDomainItem = {
    id: nextDomainId++,
    domain,
    domainTld: tldFromDomain(domain),
    ownerId: owner.id,
    ownerEmail: owner.email,
    ownerNickname: owner.nickname,
    ownerRole: owner.role,
    purpose: payload.purpose,
    status: "abnormal",
    mailServerId: server.id,
    mailboxCount: 0,
    lastSafeError: undefined,
    lastAllocatedAt: undefined,
    createdAt: nowIso,
    updatedAt: nowIso,
  };
  dataset.unshift(item);
  return cloneItem(item);
}

export async function updateAdminDomain(
  id: number,
  patch: UpdateAdminDomainRequest
): Promise<AdminDomainItem> {
  await simulateLatency(160);
  const item = findItem(id);
  if (item.status === "deleted") {
    throw new Error("Cannot update a deleted domain. Recover it first.");
  }
  if (patch.ownerId) refreshOwnerSnapshot(item, patch.ownerId);
  if (patch.purpose) item.purpose = patch.purpose;
  if (patch.status) item.status = patch.status;
  if (patch.mailServerId) item.mailServerId = patch.mailServerId;
  item.updatedAt = new Date().toISOString();
  return cloneItem(item);
}

export async function validateAdminDomain(
  id: number
): Promise<AdminDomainItem> {
  await simulateLatency(600);
  const item = findItem(id);
  if (item.status === "deleted") {
    throw new Error("Cannot validate a deleted domain.");
  }
  // Simulate a DNS/inbound validation outcome.
  const ok = Math.random() < 0.68;
  item.status = ok ? "normal" : "abnormal";
  item.lastSafeError = ok ? undefined : pick(ABNORMAL_ERRORS);
  item.updatedAt = new Date().toISOString();
  return cloneItem(item);
}

export async function queueAdminDomainMailFetch(
  id: number
): Promise<AdminDomainTask> {
  await simulateLatency(180);
  const item = findItem(id);
  if (item.status === "deleted") {
    throw new Error("Cannot fetch mail for a deleted domain.");
  }

  const now = new Date().toISOString();
  const task: AdminDomainTask = {
    id: nextDomainTaskId++,
    kind: "mail_fetch",
    status: "queued",
    remainingAttempts: 3,
    queuedAt: now,
    updatedAt: now,
  };
  const current = queuedTasksByDomainId.get(id) ?? [];
  queuedTasksByDomainId.set(id, [task, ...current]);
  item.updatedAt = now;
  return { ...task };
}

export async function validateAdminDomainsByIds(
  ids: number[]
): Promise<AdminDomainValidationResponse> {
  await simulateLatency(240);
  const unique = Array.from(new Set(ids)).filter((id) => id > 0);
  let queued = 0;
  for (const id of unique) {
    const item = dataset.find((entry) => entry.id === id);
    if (item && item.status !== "deleted") queued += 1;
  }
  return { queued };
}

export async function validateAdminDomainsByFilter(
  filter: AdminDomainListFilter
): Promise<AdminDomainValidationResponse> {
  await simulateLatency(280);
  const queued = dataset.filter(
    (item) => item.status !== "deleted" && matchesFilter(item, filter)
  ).length;
  return { queued };
}

function markAdminDomainDeleted(item: AdminDomainItem) {
  item.status = "deleted";
  item.purpose = item.purpose === "sale" ? "not_sale" : item.purpose;
  item.mailboxCount = 0;
  item.lastAllocatedAt = undefined;
  item.updatedAt = new Date().toISOString();
}

export async function deleteAdminDomain(id: number): Promise<void> {
  await simulateLatency(160);
  markAdminDomainDeleted(findItem(id));
}

export async function deleteAdminDomainsByIds(
  ids: number[]
): Promise<AdminDomainBulkResponse> {
  await simulateLatency(240);
  const unique = new Set(ids.filter((id) => id > 0));
  let affected = 0;
  for (const item of dataset) {
    if (unique.has(item.id) && item.status !== "deleted") {
      markAdminDomainDeleted(item);
      affected += 1;
    }
  }
  return { affected };
}

export async function deleteAdminDomainsByFilter(
  filter: AdminDomainListFilter
): Promise<AdminDomainBulkResponse> {
  await simulateLatency(320);
  let affected = 0;
  for (const item of dataset) {
    if (item.status !== "deleted" && matchesFilter(item, filter)) {
      markAdminDomainDeleted(item);
      affected += 1;
    }
  }
  return { affected };
}

export async function disableAdminDomainsByIds(
  ids: number[]
): Promise<AdminDomainBulkResponse> {
  await simulateLatency(220);
  const unique = new Set(ids.filter((id) => id > 0));
  let affected = 0;
  for (const item of dataset) {
    if (unique.has(item.id) && item.status !== "deleted" && item.status !== "disabled") {
      item.status = "disabled";
      item.updatedAt = new Date().toISOString();
      affected += 1;
    }
  }
  return { affected };
}

export async function setAdminDomainsPurposeByIds(
  ids: number[],
  purpose: "not_sale" | "sale"
): Promise<AdminDomainBulkResponse> {
  await simulateLatency(220);
  const unique = new Set(ids.filter((id) => id > 0));
  let affected = 0;
  for (const item of dataset) {
    if (
      unique.has(item.id) &&
      item.status !== "deleted" &&
      item.purpose !== purpose
    ) {
      item.purpose = purpose;
      item.updatedAt = new Date().toISOString();
      affected += 1;
    }
  }
  return { affected };
}

export async function setAdminDomainsPurposeByFilter(
  filter: AdminDomainListFilter,
  purpose: "not_sale" | "sale"
): Promise<AdminDomainBulkResponse> {
  await simulateLatency(280);
  let affected = 0;
  for (const item of dataset) {
    if (
      item.status !== "deleted" &&
      matchesFilter(item, filter) &&
      item.purpose !== purpose
    ) {
      item.purpose = purpose;
      item.updatedAt = new Date().toISOString();
      affected += 1;
    }
  }
  return { affected };
}

export async function recoverAdminDomain(id: number): Promise<AdminDomainItem> {
  await simulateLatency(200);
  const item = findItem(id);
  if (item.status !== "deleted") {
    throw new Error("Only deleted domains can be recovered.");
  }
  // Recovery restores the resource to an unverified state pending re-validation.
  item.status = "abnormal";
  item.lastSafeError = undefined;
  item.updatedAt = new Date().toISOString();
  return cloneItem(item);
}
