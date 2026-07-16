import type { components } from "@/lib/openapi/schema";
import { apiClient as client, csrfHeader, unwrap } from "@/lib/api-client";
import { generateIdempotencyKey } from "@/lib/idempotency";

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
  enabled: boolean;
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
  orderStatus:
    | "pending_payment"
    | "paid"
    | "active"
    | "completed"
    | "refunded"
    | "failed"
    | "closed";
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

export type AdminDomainItem = Omit<
  components["schemas"]["AdminDomainItem"],
  "lastAllocatedAt"
> & { lastAllocatedAt?: string };

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
  mailServerId?: number;
  createdFrom?: string;
  createdTo?: string;
}

export type AdminDomainListResponse = Omit<
  components["schemas"]["AdminDomainListResponse"],
  "items"
> & { items: AdminDomainItem[] };

export interface CreateAdminDomainRequest {
  domain: string;
  ownerId: number;
  purpose: AdminDomainPurpose;
  mailServerId?: number;
}

export interface UpdateAdminDomainRequest {
  ownerId?: number;
  purpose?: AdminDomainPurpose;
  status?: Extract<AdminDomainStatus, "normal" | "abnormal" | "disabled">;
  mailServerId?: number;
}

export interface AdminDomainBulkResponse {
  affected: number;
}

export interface AdminDomainValidationResponse {
  queued: number;
}

type DomainDTO = components["schemas"]["AdminDomainItem"];
type ServerDTO = components["schemas"]["AdminMailServerItem"];
type MailboxDTO = components["schemas"]["MailboxItem"];
type AllocationDTO = components["schemas"]["AdminAllocationItem"];
type TaskDTO = components["schemas"]["AdminTaskView"];
type MessageDTO = components["schemas"]["AdminMessageSummary"];
type MessageDetailDTO = components["schemas"]["AdminMessageDetail"];

const PAGE_SIZE = 100;

function commandHeaders() {
  return {
    ...csrfHeader(),
    "Idempotency-Key": generateIdempotencyKey(),
  };
}

function normalizeFilter(filter: AdminDomainListFilter) {
  return {
    search: filter.search?.trim() || undefined,
    status: filter.status === "all" ? undefined : filter.status,
    purpose: filter.purpose === "all" ? undefined : filter.purpose,
    tld: filter.tld?.trim() || undefined,
    ownerId: filter.ownerId,
    mailServerId: filter.mailServerId,
    createdFrom: filter.createdFrom,
    createdTo: filter.createdTo,
  };
}

function normalizeDomain(item: DomainDTO): AdminDomainItem {
  return {
    ...item,
    lastAllocatedAt: item.lastAllocatedAt ?? undefined,
  };
}

function idsSelection(resourceIds: number[]) {
  return {
    mode: "ids" as const,
    resourceIds: Array.from(new Set(resourceIds)).filter(
      (id) => Number.isInteger(id) && id > 0
    ),
  };
}

function filterSelection(filter: AdminDomainListFilter) {
  return { mode: "filter" as const, filter: normalizeFilter(filter) };
}

export async function listAdminDomains(
  filter: AdminDomainListFilter = {},
  offset = 0,
  limit = 20,
  afterId?: number,
  signal?: AbortSignal
): Promise<AdminDomainListResponse> {
  const response = await unwrap(
    await client.GET("/v1/admin/domains", {
      params: {
        query: {
          ...normalizeFilter(filter),
          offset: Math.max(0, Math.trunc(offset)),
          limit: Math.max(1, Math.min(PAGE_SIZE, Math.trunc(limit))),
          afterId,
        },
      },
      signal,
    })
  );
  return { ...response, items: response.items.map(normalizeDomain) };
}

export async function getAdminDomain(
  id: number,
  signal?: AbortSignal
): Promise<AdminDomainItem> {
  return normalizeDomain(
    await unwrap(
      await client.GET("/v1/admin/domains/{domainId}", {
        params: { path: { domainId: id } },
        signal,
      })
    )
  );
}

export async function listAdminDomainOwners(
  search = "",
  signal?: AbortSignal
): Promise<AdminDomainOwner[]> {
  const owners: AdminDomainOwner[] = [];
  let offset = 0;
  for (;;) {
    const page = await unwrap(
      await client.GET("/v1/admin/users", {
        params: {
          query: {
            search: search.trim() || undefined,
            offset,
            limit: PAGE_SIZE,
          },
        },
        signal,
      })
    );
    owners.push(
      ...page.users.map((user) => ({
        id: user.id,
        email: user.email,
        nickname: user.nickname,
        role: user.role,
        enabled: user.enabled,
      }))
    );
    offset += page.users.length;
    if (offset >= page.total || page.users.length === 0) return owners;
  }
}

export async function listAdminMailServers(
  signal?: AbortSignal
): Promise<AdminMailServer[]> {
  const servers: AdminMailServer[] = [];
  let offset = 0;
  for (;;) {
    const page = await unwrap(
      await client.GET("/v1/admin/servers", {
        params: { query: { offset, limit: PAGE_SIZE } },
        signal,
      })
    );
    servers.push(...page.items.map(adminMailServer));
    offset += page.items.length;
    if (offset >= page.total || page.items.length === 0) return servers;
  }
}

export async function createAdminDomain(
  payload: CreateAdminDomainRequest
): Promise<AdminDomainItem> {
  return normalizeDomain(
    await unwrap(
      await client.POST("/v1/admin/domains", {
        body: payload,
        params: { header: commandHeaders() },
      })
    )
  );
}

export async function updateAdminDomain(
  id: number,
  patch: UpdateAdminDomainRequest
): Promise<AdminDomainItem> {
  const current = await getAdminDomain(id);
  let statusCommand:
    | "mark_normal"
    | "mark_abnormal"
    | "enable"
    | "disable"
    | undefined;
  if (patch.status && patch.status !== current.status) {
    if (patch.status === "disabled") statusCommand = "disable";
    else if (current.status === "disabled") statusCommand = "enable";
    else if (patch.status === "normal") statusCommand = "mark_normal";
    else statusCommand = "mark_abnormal";
  }
  const body = {
    ownerId:
      patch.ownerId !== undefined && patch.ownerId !== current.ownerId
        ? patch.ownerId
        : undefined,
    purpose:
      patch.purpose !== undefined && patch.purpose !== current.purpose
        ? patch.purpose
        : undefined,
    mailServerId:
      patch.mailServerId !== undefined &&
      patch.mailServerId !== current.mailServerId
        ? patch.mailServerId
        : undefined,
    statusCommand,
  };
  if (Object.values(body).every((value) => value === undefined)) return current;
  return normalizeDomain(
    await unwrap(
      await client.PATCH("/v1/admin/domains/{domainId}", {
        body,
        params: {
          header: commandHeaders(),
          path: { domainId: id },
          query: { version: current.version },
        },
      })
    )
  );
}

export async function validateAdminDomain(
  id: number
): Promise<AdminDomainItem> {
  await unwrap(
    await client.POST("/v1/admin/domains/{domainId}/validate", {
      params: { header: commandHeaders(), path: { domainId: id } },
    })
  );
  return getAdminDomain(id);
}

export async function deleteAdminDomain(id: number): Promise<void> {
  const current = await getAdminDomain(id);
  await unwrap(
    await client.DELETE("/v1/admin/domains/{domainId}", {
      params: {
        header: commandHeaders(),
        path: { domainId: id },
        query: { version: current.version },
      },
    })
  );
}

export async function recoverAdminDomain(
  id: number
): Promise<AdminDomainItem> {
  const current = await getAdminDomain(id);
  return normalizeDomain(
    await unwrap(
      await client.POST("/v1/admin/domains/{domainId}/recover", {
        params: {
          header: commandHeaders(),
          path: { domainId: id },
          query: { version: current.version },
        },
      })
    )
  );
}

export async function validateAdminDomainsByIds(
  ids: number[]
): Promise<AdminDomainValidationResponse> {
  return unwrap(
    await client.POST("/v1/admin/domains/validations", {
      body: { selection: idsSelection(ids) },
      params: { header: commandHeaders() },
    })
  );
}

export async function validateAdminDomainsByFilter(
  filter: AdminDomainListFilter
): Promise<AdminDomainValidationResponse> {
  return unwrap(
    await client.POST("/v1/admin/domains/validations", {
      body: { selection: filterSelection(filter) },
      params: { header: commandHeaders() },
    })
  );
}

export async function disableAdminDomainsByIds(
  ids: number[]
): Promise<AdminDomainBulkResponse> {
  return unwrap(
    await client.POST("/v1/admin/domains/disable", {
      body: { selection: idsSelection(ids) },
      params: { header: commandHeaders() },
    })
  );
}

export async function deleteAdminDomainsByIds(
  ids: number[]
): Promise<AdminDomainBulkResponse> {
  return domainBulkByIds("/v1/admin/domains/delete", ids);
}

export async function deleteAdminDomainsByFilter(
  filter: AdminDomainListFilter
): Promise<AdminDomainBulkResponse> {
  return domainBulkByFilter("/v1/admin/domains/delete", filter);
}

export async function setAdminDomainsPurposeByIds(
  ids: number[],
  purpose: "not_sale" | "sale"
): Promise<AdminDomainBulkResponse> {
  return domainBulkByIds(
    purpose === "sale"
      ? "/v1/admin/domains/publish"
      : "/v1/admin/domains/unpublish",
    ids
  );
}

export async function setAdminDomainsPurposeByFilter(
  filter: AdminDomainListFilter,
  purpose: "not_sale" | "sale"
): Promise<AdminDomainBulkResponse> {
  return domainBulkByFilter(
    purpose === "sale"
      ? "/v1/admin/domains/publish"
      : "/v1/admin/domains/unpublish",
    filter
  );
}

async function domainBulkByIds(
  path:
    | "/v1/admin/domains/publish"
    | "/v1/admin/domains/unpublish"
    | "/v1/admin/domains/delete",
  ids: number[]
) {
  return unwrap(
    await client.POST(path, {
      body: { selection: idsSelection(ids) },
      params: { header: commandHeaders() },
    })
  );
}

async function domainBulkByFilter(
  path:
    | "/v1/admin/domains/publish"
    | "/v1/admin/domains/unpublish"
    | "/v1/admin/domains/delete",
  filter: AdminDomainListFilter
) {
  return unwrap(
    await client.POST(path, {
      body: { selection: filterSelection(filter) },
      params: { header: commandHeaders() },
    })
  );
}

export async function getAdminDomainDetail(
  id: number,
  signal?: AbortSignal
): Promise<AdminDomainDetail> {
  const [domainItem, servers, mailboxes, orders, tasks, messages] =
    await Promise.all([
      getAdminDomain(id, signal),
      listAdminMailServers(signal),
      listAdminDomainMailboxes(id, signal),
      listAdminDomainOrders(id, signal),
      listAdminDomainTasks(id, signal),
      refreshAdminDomainMessages(id, signal),
    ]);
  return {
    ...domainItem,
    mailServer:
      servers.find((server) => server.id === domainItem.mailServerId) ??
      missingMailServer(domainItem.mailServerId, domainItem.ownerId),
    mailboxes,
    orders,
    tasks,
    messages,
  };
}

export async function getAdminDomainMessage(
  resourceId: number,
  messageId: number,
  signal?: AbortSignal
): Promise<AdminDomainMessage> {
  const item = await unwrap<MessageDetailDTO>(
    await client.GET("/v1/admin/messages/{messageId}", {
      params: {
        path: { messageId },
        query: { resourceId, type: "domain" },
      },
      signal,
    })
  );
  return adminDomainMessage(item, item.body);
}

async function listAdminDomainMailboxes(
  id: number,
  signal?: AbortSignal
): Promise<AdminGeneratedMailbox[]> {
  const items: MailboxDTO[] = [];
  let offset = 0;
  for (;;) {
    const page = await unwrap(
      await client.GET("/v1/admin/domains/{domainId}/mailboxes", {
        params: {
          path: { domainId: id },
          query: { offset, limit: PAGE_SIZE },
        },
        signal,
      })
    );
    items.push(...page.items);
    offset += page.items.length;
    if (offset >= page.total || page.items.length === 0) {
      return items.map(adminGeneratedMailbox);
    }
  }
}

async function listAdminDomainOrders(
  id: number,
  signal?: AbortSignal
): Promise<AdminDomainOrder[]> {
  const items: AllocationDTO[] = [];
  let offset = 0;
  for (;;) {
    const page = await unwrap(
      await client.GET("/v1/admin/allocations", {
        params: {
          query: {
            type: "domain",
            resourceId: id,
            offset,
            limit: PAGE_SIZE,
          },
        },
        signal,
      })
    );
    items.push(...page.items);
    offset += page.items.length;
    if (offset >= page.total || page.items.length === 0) {
      return items.map(adminDomainOrder);
    }
  }
}

async function listAdminDomainTasks(
  id: number,
  signal?: AbortSignal
): Promise<AdminDomainTask[]> {
  const items: TaskDTO[] = [];
  let offset = 0;
  for (;;) {
    const page = await unwrap(
      await client.GET("/v1/admin/tasks", {
        params: {
          query: {
            bizType: "domain_resource",
            bizId: id,
            offset,
            limit: PAGE_SIZE,
          },
        },
        signal,
      })
    );
    items.push(...page.items);
    offset += page.items.length;
    if (offset >= page.total || page.items.length === 0) {
      return items.map(adminDomainTask);
    }
  }
}

export async function refreshAdminDomainMessages(
  id: number,
  signal?: AbortSignal
): Promise<AdminDomainMessage[]> {
  const items: MessageDTO[] = [];
  let offset = 0;
  for (;;) {
    const page = await unwrap(
      await client.GET("/v1/admin/messages", {
        params: {
          query: {
            resourceId: id,
            type: "domain",
            offset,
            limit: PAGE_SIZE,
          },
        },
        signal,
      })
    );
    items.push(...page.items);
    offset += page.items.length;
    if (offset >= page.total || page.items.length === 0) {
      return items.map((item) => adminDomainMessage(item, item.preview));
    }
  }
}

function adminMailServer(item: ServerDTO): AdminMailServer {
  return {
    id: item.id,
    name: item.name,
    ownerId: item.ownerId,
    serverAddress: item.serverAddress,
    mxRecord: item.mxRecord,
    spf: item.spfRecord,
    dkim: item.dkimRecord,
    dmarc: item.dmarcRecord,
    ptr: item.ptrRecord,
    status: item.status,
  };
}

function missingMailServer(id: number, ownerId: number): AdminMailServer {
  return {
    id,
    ownerId,
    name: `#${id}`,
    serverAddress: "",
    mxRecord: "",
    spf: "",
    dkim: "",
    dmarc: "",
    ptr: "",
    status: "disabled",
  };
}

function adminGeneratedMailbox(item: MailboxDTO): AdminGeneratedMailbox {
  return {
    id: item.id,
    email: item.email,
    status: item.status === "disabled" ? "disabled" : "normal",
    lastAllocatedAt: item.lastAllocatedAt ?? undefined,
    createdAt: item.createdAt,
  };
}

function adminDomainOrder(item: AllocationDTO): AdminDomainOrder {
  return {
    id: item.id,
    orderNo: item.orderNo,
    projectName: item.projectName,
    projectLogoUrl: item.projectLogoUrl ?? undefined,
    deliveryEmail: item.deliveryEmail,
    supplyScope: item.supplyScope,
    serviceMode: item.serviceMode,
    orderStatus: item.orderStatus,
    allocationStatus: item.status,
    buyerEmail: item.buyerEmail,
    payAmount: item.payAmount,
    verificationCode: item.verificationCode ?? undefined,
    receiveUntil: item.receiveUntil ?? undefined,
    createdAt: item.createdAt,
  };
}

function adminDomainTask(item: TaskDTO): AdminDomainTask {
  const parts = item.taskId.split(":");
  const sourceId = Number(parts[parts.length - 1]);
  return {
    id: Number.isFinite(sourceId) ? sourceId : 0,
    kind:
      item.kind === "validation"
        ? "validation"
        : item.kind === "fetch"
          ? "mail_fetch"
          : "alias_replenishment",
    status:
      item.status === "queued" ||
      item.status === "running" ||
      item.status === "succeeded"
        ? item.status
        : "failed",
    remainingAttempts: item.remainingAttempts,
    queuedAt: item.queuedAt,
    startedAt: item.startedAt ?? undefined,
    finishedAt: item.finishedAt ?? undefined,
    updatedAt: item.updatedAt,
  };
}

function adminDomainMessage(
  item: MessageDTO,
  body: string
): AdminDomainMessage {
  return {
    id: item.id,
    recipient: item.recipient,
    sender: item.sender,
    subject: item.subject,
    status: item.status,
    verificationCode: item.verificationCode ?? undefined,
    orderNo: item.orderNo ?? undefined,
    receivedAt: item.receivedAt,
    preview: item.preview,
    body,
  };
}
