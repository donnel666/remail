// Real API client for the Admin User Management page.
//
// Endpoint -> function map:
//   GET    /v1/admin/users                          -> listAdminUsers
//   GET    /v1/admin/wallets/balances               -> getAdminUserBalances
//   POST   /v1/admin/users                          -> createAdminUser
//   PATCH  /v1/admin/users/{userId}                 -> updateAdminUser
//   DELETE /v1/admin/users/{userId}                 -> deleteAdminUser
//   POST   /v1/admin/users/{userId}/sessions/revoke -> revokeAdminUserSessions
//   GET    /v1/admin/users/{userId}/invitations     -> getAdminUserInvitations
//   GET    /v1/admin/users/groups                   -> listUserGroups
//   GET    /v1/admin/permissions                    -> getPermissionCatalog
//   GET    /v1/admin/users/{userId}/permissions     -> getAdminUserPermissions
//   PUT    /v1/admin/users/{userId}/permissions     -> putAdminUserPermissions
//   GET    /v1/admin/wallets/{userId}               -> getAdminUserWallet
//   GET    /v1/admin/wallets/{userId}/transactions  -> listAdminUserTransactions
//   POST   /v1/admin/wallets/{userId}/credit        -> creditAdminUserWallet
//   POST   /v1/admin/wallets/{userId}/debit         -> debitAdminUserWallet
//   GET/POST/PATCH/DELETE .../apikeys               -> *AdminUserApiKey*
//   POST   /v1/admin/users/enable                   -> setAdminUsersEnabled*
//   POST   /v1/admin/users/disable                  -> setAdminUsersEnabled*
//   POST   /v1/admin/users/delete                   -> deleteAdminUsers*
//   POST   /v1/admin/users/sessions/revoke          -> revokeAdminUsersSessions*
//   POST   /v1/admin/wallets/adjust                 -> adjustAdminUsersWallet*

import { apiClient, csrfHeader, unwrap } from "@/lib/api-client";
import { generateIdempotencyKey } from "@/lib/idempotency";
import type { components } from "@/lib/openapi/schema";

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

export interface AdminTransactionPage {
  items: AdminTransaction[];
  nextAfterId?: number;
  hasNext: boolean;
  limit: number;
}

// Client-side display baseline. The real Casbin baseline is not exposed
// per-user, so this drives the "inherited" column in the permissions tab only.
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
  super_admin: [
    "iam:user:read",
    "iam:user:write",
    "iam:user:operate",
    "iam:permission:read",
    "iam:permission:write",
    "iam:permission:sensitive",
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
    "billing:wallet:sensitive",
    "billing:card:read",
    "billing:card:write",
    "billing:card:sensitive",
    "proxy:proxy:read",
    "proxy:proxy:write",
    "proxy:proxy:operate",
  ],
};

export function roleBaselinePermissions(role: AdminUserRole): string[] {
  return ROLE_BASELINE[role] ?? [];
}

type UserResponse = components["schemas"]["UserResponse"];
type WalletResponse = components["schemas"]["WalletResponse"];
type APIKeyResponse = components["schemas"]["APIKeyResponse"];

function toAdminUser(user: UserResponse, consumerBalance: string): AdminUser {
  return { ...user, consumerBalance };
}

function toInvitationMember(
  member: components["schemas"]["AdminUserInvitationMember"]
): AdminUserInvitationMember {
  return { ...member, role: member.role as AdminUserRole };
}

function toAdminUserFacets(
  facets: components["schemas"]["AdminUserFacets"] | undefined
): AdminUserFacets {
  const role = facets?.role ?? {};
  return {
    role: {
      all: role.all ?? 0,
      user: role.user ?? 0,
      supplier: role.supplier ?? 0,
      admin: role.admin ?? 0,
      super_admin: role.super_admin ?? 0,
    },
    status: facets?.status ?? { all: 0, enabled: 0, disabled: 0 },
    group: facets?.group ?? [],
  };
}

export async function getAdminUserBalances(
  ids: number[]
): Promise<Record<number, string>> {
  if (ids.length === 0) return {};
  const response = await unwrap<components["schemas"]["AdminWalletBalanceList"]>(
    await apiClient.GET("/v1/admin/wallets/balances", {
      params: { query: { userIds: ids } },
    })
  );
  const balances: Record<number, string> = {};
  for (const item of response.balances) {
    balances[item.userId] = item.consumerBalance;
  }
  return balances;
}

export async function listAdminUsers(
  filter: AdminUserListFilter,
  offset: number,
  limit: number
): Promise<AdminUserListResult> {
  const response = await unwrap<components["schemas"]["AdminUserListResponse"]>(
    await apiClient.GET("/v1/admin/users", {
      params: { query: { offset, limit, ...filter } },
    })
  );
  const balances = await getAdminUserBalances(
    response.users.map((u) => u.id)
  ).catch(() => ({}) as Record<number, string>);
  return {
    users: response.users.map((u) => toAdminUser(u, balances[u.id] ?? "0")),
    total: response.total,
    offset: response.offset,
    limit: response.limit,
    facets: toAdminUserFacets(response.facets),
  };
}

export async function createAdminUser(
  input: CreateAdminUserInput
): Promise<AdminUser> {
  const response = await unwrap<{ user: UserResponse }>(
    await apiClient.POST("/v1/admin/users", {
      body: {
        email: input.email,
        nickname: input.nickname,
        password: input.password,
        role: input.role,
        userGroupId: input.userGroupId,
      },
      params: { header: csrfHeader() },
    })
  );
  return toAdminUser(response.user, "0");
}

export async function updateAdminUser(
  userId: number,
  input: UpdateAdminUserInput
): Promise<AdminUser> {
  const body: components["schemas"]["AdminUpdateUserRequest"] = {};
  if (input.email !== undefined) body.email = input.email;
  if (input.nickname !== undefined) body.nickname = input.nickname;
  if (input.password !== undefined) body.password = input.password;
  if (input.role !== undefined) body.role = input.role;
  if (input.userGroupId !== undefined) body.userGroupId = input.userGroupId;
  if (input.enabled !== undefined) body.enabled = input.enabled;
  const response = await unwrap<{ user: UserResponse }>(
    await apiClient.PATCH("/v1/admin/users/{userId}", {
      body,
      params: { header: csrfHeader(), path: { userId } },
    })
  );
  const balances = await getAdminUserBalances([userId]).catch(
    () => ({}) as Record<number, string>
  );
  return toAdminUser(response.user, balances[userId] ?? "0");
}

export async function deleteAdminUser(userId: number): Promise<void> {
  await unwrap<void>(
    await apiClient.DELETE("/v1/admin/users/{userId}", {
      params: { header: csrfHeader(), path: { userId } },
    })
  );
}

export async function revokeAdminUserSessions(userId: number): Promise<void> {
  await unwrap<void>(
    await apiClient.POST("/v1/admin/users/{userId}/sessions/revoke", {
      params: { header: csrfHeader(), path: { userId } },
    })
  );
}

export async function getAdminUserInvitations(
  userId: number
): Promise<AdminUserInvitationOverview> {
  const response = await unwrap<
    components["schemas"]["AdminUserInvitationsResponse"]
  >(
    await apiClient.GET("/v1/admin/users/{userId}/invitations", {
      params: { path: { userId } },
    })
  );
  return {
    inviter: response.inviter ? toInvitationMember(response.inviter) : null,
    invitees: response.invitees.map(toInvitationMember),
  };
}

export async function listUserGroups(): Promise<AdminUserGroup[]> {
  const response = await unwrap<
    components["schemas"]["AdminUserGroupListResponse"]
  >(await apiClient.GET("/v1/admin/users/groups"));
  return response.groups;
}

export async function getPermissionCatalog(): Promise<PermissionCatalogItem[]> {
  const response = await unwrap<
    components["schemas"]["PermissionCatalogResponse"]
  >(await apiClient.GET("/v1/admin/permissions"));
  return response.permissions;
}

export async function getAdminUserPermissions(
  userId: number
): Promise<PermissionPolicy[]> {
  const response = await unwrap<
    components["schemas"]["UserPermissionPoliciesResponse"]
  >(
    await apiClient.GET("/v1/admin/users/{userId}/permissions", {
      params: { path: { userId } },
    })
  );
  return response.policies;
}

export async function putAdminUserPermissions(
  userId: number,
  policies: PermissionPolicy[]
): Promise<void> {
  await unwrap<void>(
    await apiClient.PUT("/v1/admin/users/{userId}/permissions", {
      body: { policies },
      params: { header: csrfHeader(), path: { userId } },
    })
  );
}

export async function getAdminUserWallet(userId: number): Promise<AdminWallet> {
  return unwrap<WalletResponse>(
    await apiClient.GET("/v1/admin/wallets/{userId}", {
      params: { path: { userId } },
    })
  );
}

export async function listAdminUserTransactions(
  userId: number,
  afterId?: number,
  limit = 20
): Promise<AdminTransactionPage> {
  const response = await unwrap<
    components["schemas"]["TransactionListResponse"]
  >(
    await apiClient.GET("/v1/admin/wallets/{userId}/transactions", {
      params: { path: { userId }, query: { afterId, limit } },
    })
  );
  return {
    items: response.items,
    nextAfterId: response.nextAfterId,
    hasNext: response.hasNext,
    limit: response.limit,
  };
}

export async function creditAdminUserWallet(
  userId: number,
  amount: string,
  reason: string
): Promise<AdminWalletAdjustmentResult> {
  return unwrap<components["schemas"]["WalletAdjustmentResponse"]>(
    await apiClient.POST("/v1/admin/wallets/{userId}/credit", {
      body: { amount, reason },
      params: {
        header: commandHeaders(),
        path: { userId },
      },
    })
  );
}

export async function debitAdminUserWallet(
  userId: number,
  amount: string,
  reason: string
): Promise<AdminWalletAdjustmentResult> {
  return unwrap<components["schemas"]["WalletAdjustmentResponse"]>(
    await apiClient.POST("/v1/admin/wallets/{userId}/debit", {
      body: { amount, reason },
      params: {
        header: commandHeaders(),
        path: { userId },
      },
    })
  );
}

export async function listAdminUserApiKeys(
  userId: number
): Promise<AdminApiKey[]> {
  const response = await unwrap<components["schemas"]["APIKeyListResponse"]>(
    await apiClient.GET("/v1/admin/users/{userId}/apikeys", {
      params: { path: { userId }, query: { limit: 100 } },
    })
  );
  return response.items;
}

export async function createAdminUserApiKey(
  userId: number,
  input: AdminApiKeyInput
): Promise<AdminApiKey> {
  return unwrap<APIKeyResponse>(
    await apiClient.POST("/v1/admin/users/{userId}/apikeys", {
      body: {
        name: input.name,
        expireAt: input.expireAt,
        rateLimitPerMinute: input.rateLimitPerMinute,
        concurrencyLimit: input.concurrencyLimit,
        quotaLimit: input.quotaLimit,
      },
      params: { header: csrfHeader(), path: { userId } },
    })
  );
}

export async function updateAdminUserApiKey(
  userId: number,
  keyId: number,
  input: AdminApiKeyInput
): Promise<AdminApiKey> {
  const body: components["schemas"]["APIKeyPatchRequest"] = {};
  if (input.name !== undefined) body.name = input.name;
  if (input.enabled !== undefined) body.enabled = input.enabled;
  if (input.expireAt !== undefined) body.expireAt = input.expireAt;
  if (input.rateLimitPerMinute !== undefined) {
    body.rateLimitPerMinute = input.rateLimitPerMinute;
  }
  if (input.concurrencyLimit !== undefined) {
    body.concurrencyLimit = input.concurrencyLimit;
  }
  if (input.quotaLimit !== undefined) body.quotaLimit = input.quotaLimit;
  return unwrap<APIKeyResponse>(
    await apiClient.PATCH("/v1/admin/users/{userId}/apikeys/{keyId}", {
      body,
      params: { header: csrfHeader(), path: { userId, keyId } },
    })
  );
}

export async function deleteAdminUserApiKey(
  userId: number,
  keyId: number
): Promise<void> {
  await unwrap<void>(
    await apiClient.DELETE("/v1/admin/users/{userId}/apikeys/{keyId}", {
      params: { header: csrfHeader(), path: { userId, keyId } },
    })
  );
}

// Selection-based bulk operations. The backend resolves the selection (either an
// explicit id list or the current list filter) and returns { requested, affected,
// skipped }; the page only surfaces affected/skipped.
type AdminUserBulkResponse = components["schemas"]["AdminUserBulkResponse"];
type AdminUserBulkSelection = components["schemas"]["AdminUserBulkSelection"];

type UserBulkPath =
  | "/v1/admin/users/enable"
  | "/v1/admin/users/disable"
  | "/v1/admin/users/delete"
  | "/v1/admin/users/sessions/revoke";

function commandHeaders() {
  return {
    ...csrfHeader(),
    "Idempotency-Key": generateIdempotencyKey(),
  };
}

function idsSelection(userIds: number[]): AdminUserBulkSelection {
  return {
    mode: "ids",
    userIds: Array.from(new Set(userIds)).filter(
      (id) => Number.isInteger(id) && id > 0
    ),
  };
}

function filterSelection(filter: AdminUserListFilter): AdminUserBulkSelection {
  return {
    mode: "filter",
    filter: {
      search: filter.search?.trim() || undefined,
      role: filter.role,
      enabled: filter.enabled,
      userGroupId: filter.userGroupId,
      createdFrom: filter.createdFrom,
      createdTo: filter.createdTo,
    },
  };
}

function toBulkResult(response: AdminUserBulkResponse): AdminUserBulkResult {
  return { affected: response.affected, skipped: response.skipped };
}

async function userBulk(
  path: UserBulkPath,
  selection: AdminUserBulkSelection
): Promise<AdminUserBulkResult> {
  return toBulkResult(
    await unwrap<AdminUserBulkResponse>(
      await apiClient.POST(path, {
        body: { selection },
        params: { header: commandHeaders() },
      })
    )
  );
}

async function walletBulk(
  selection: AdminUserBulkSelection,
  signedAmount: number,
  reason: string
): Promise<AdminUserBulkResult> {
  return toBulkResult(
    await unwrap<AdminUserBulkResponse>(
      await apiClient.POST("/v1/admin/wallets/adjust", {
        body: { selection, amount: signedAmount.toFixed(6), reason },
        params: { header: commandHeaders() },
      })
    )
  );
}

export function setAdminUsersEnabledByIds(
  userIds: number[],
  enabled: boolean
): Promise<AdminUserBulkResult> {
  return userBulk(
    enabled ? "/v1/admin/users/enable" : "/v1/admin/users/disable",
    idsSelection(userIds)
  );
}

export function setAdminUsersEnabledByFilter(
  filter: AdminUserListFilter,
  enabled: boolean
): Promise<AdminUserBulkResult> {
  return userBulk(
    enabled ? "/v1/admin/users/enable" : "/v1/admin/users/disable",
    filterSelection(filter)
  );
}

export function revokeAdminUsersSessionsByIds(
  userIds: number[]
): Promise<AdminUserBulkResult> {
  return userBulk("/v1/admin/users/sessions/revoke", idsSelection(userIds));
}

export function revokeAdminUsersSessionsByFilter(
  filter: AdminUserListFilter
): Promise<AdminUserBulkResult> {
  return userBulk("/v1/admin/users/sessions/revoke", filterSelection(filter));
}

export function deleteAdminUsersByIds(
  userIds: number[]
): Promise<AdminUserBulkResult> {
  return userBulk("/v1/admin/users/delete", idsSelection(userIds));
}

export function deleteAdminUsersByFilter(
  filter: AdminUserListFilter
): Promise<AdminUserBulkResult> {
  return userBulk("/v1/admin/users/delete", filterSelection(filter));
}

export function adjustAdminUsersWalletByIds(
  userIds: number[],
  signedAmount: number,
  reason: string
): Promise<AdminUserBulkResult> {
  return walletBulk(idsSelection(userIds), signedAmount, reason);
}

export function adjustAdminUsersWalletByFilter(
  filter: AdminUserListFilter,
  signedAmount: number,
  reason: string
): Promise<AdminUserBulkResult> {
  return walletBulk(filterSelection(filter), signedAmount, reason);
}
