// Real API client for the Admin Finance Center page.
//
// Endpoint -> function map:
//   GET    /v1/admin/invites                       -> listFinanceInvites
//   POST   /v1/admin/invites                       -> createFinanceInvite
//   POST   /v1/admin/invites/batch                 -> batchCreateFinanceInvites
//   PATCH  /v1/admin/invites/{code}                -> updateFinanceInvite / setFinanceInviteEnabled
//   POST   /v1/admin/invites/enable|disable        -> setFinanceInvitesEnabled* (ids / filter)
//   GET    /v1/admin/invites/{code}/uses           -> listFinanceInviteUses
//   GET    /v1/admin/cards                          -> listFinanceCardKeys
//   POST   /v1/admin/cards                          -> createFinanceCardKeys
//   PATCH  /v1/admin/cards/{cardKey}               -> updateFinanceCardKey / setFinanceCardKeyStatus
//   POST   /v1/admin/cards/enable|disable          -> setFinanceCardKeysStatus* (ids / filter)
//   GET    /v1/admin/cards/{cardKey}/redemptions   -> listFinanceCardKeyRedemptions
//   GET    /v1/admin/transactions                   -> listFinanceTransactions
//   POST   /v1/admin/transactions/{id}/reverse     -> reverseFinanceTransaction
//   GET    /v1/admin/wallets                        -> listFinanceUserBalances
//   POST   /v1/admin/wallets/{userId}/credit       -> creditFinanceUserWallet
//   POST   /v1/admin/wallets/{userId}/debit        -> debitFinanceUserWallet
//   POST   /v1/admin/wallets/{userId}/withdraw     -> withdrawFinanceUserWallet
//   POST   /v1/admin/wallets/adjust                 -> adjustFinanceUsersWallet
//   GET    /v1/admin/finance/summary                -> getFinanceSummary

import { apiClient, csrfHeader, unwrap } from "@/lib/api-client";
import { generateIdempotencyKey } from "@/lib/idempotency";
import type { components } from "@/lib/openapi/schema";

export type FinanceInviteKind = "admin" | "referral";
export type FinanceEnabledFilter = "all" | "enabled" | "disabled";
export type FinanceCardKeyStatus = "enabled" | "disabled";
export type FinanceCardKeyStatusFilter = "all" | FinanceCardKeyStatus;
export type FinanceOwnerRoleFilter = "all" | FinanceUserRole;

// Canonical owner role option list reused by the invite / card filters.
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
  ownerGroupId?: number;
  enabled?: boolean;
}

export interface FinanceCardKeyListFilter {
  search?: string;
  ownerRole?: FinanceUserRole;
  ownerGroupId?: number;
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

// Owner-group facets are id/name/count lists sourced from the backend (not a
// hardcoded name list), so the group filter can key on the real group id.
export interface FinanceInviteFacets {
  role: Record<FinanceOwnerRoleFilter, number>;
  group: { id: number; name: string; count: number }[];
  enabled: Record<FinanceEnabledFilter, number>;
}

export interface FinanceCardKeyFacets {
  role: Record<FinanceOwnerRoleFilter, number>;
  group: { id: number; name: string; count: number }[];
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

type InviteResponse = components["schemas"]["InviteResponse"];
type InviteListResponse = components["schemas"]["InviteListResponse"];
type InviteFacetsResponse = components["schemas"]["InviteFacets"];
type InviteUseResponse = components["schemas"]["InviteUseResponse"];
type InviteBulkSelection = components["schemas"]["InviteBulkSelection"];
type CardKeyResponse = components["schemas"]["CardKey"];
type CardKeyListResponse = components["schemas"]["CardKeyListResponse"];
type CardKeyFacetsResponse = components["schemas"]["CardKeyFacets"];
type CardRedemptionResponse = components["schemas"]["CardRedemptionResponse"];
type CardBulkSelection = components["schemas"]["CardBulkSelection"];
type AdminTransactionItem = components["schemas"]["AdminTransactionItem"];
type AdminWalletItem = components["schemas"]["AdminWalletItem"];
type WalletResponse = components["schemas"]["WalletResponse"];
type TransactionItem = components["schemas"]["TransactionItem"];
type WalletAdjustmentResponse = components["schemas"]["WalletAdjustmentResponse"];
type AdminBulkResponse = components["schemas"]["AdminBulkResponse"];
type AdminUserBulkResponse = components["schemas"]["AdminUserBulkResponse"];

function toFinanceInvite(invite: InviteResponse): FinanceInvite {
  return {
    code: invite.code,
    kind: invite.kind,
    enabled: invite.enabled,
    maxUse: invite.maxUse,
    used: invite.used,
    expireAt: invite.expireAt,
    ownerUserId: invite.ownerUserId,
    ownerEmail: invite.ownerEmail,
    ownerNickname: invite.ownerNickname,
    ownerRole: invite.ownerRole as FinanceUserRole | null | undefined,
    ownerGroupName: invite.ownerGroupName,
    createdAt: invite.createdAt,
    updatedAt: invite.updatedAt,
  };
}

function toFinanceInviteUse(use: InviteUseResponse): FinanceInviteUse {
  return { ...use, userRole: use.userRole as FinanceUserRole };
}

function toFinanceInviteFacets(facets: InviteFacetsResponse): FinanceInviteFacets {
  return { role: facets.role, group: facets.group, enabled: facets.enabled };
}

export function toFinanceCardKey(card: CardKeyResponse): FinanceCardKey {
  return {
    key: card.cardKey,
    amount: card.amount,
    status: card.status,
    maxRedemptions: card.maxRedemptions,
    redeemedCount: card.redeemedCount,
    expireAt: card.expireAt,
    ownerUserId: card.ownerUserId,
    ownerEmail: card.ownerEmail,
    ownerNickname: card.ownerNickname,
    ownerRole: card.ownerRole as FinanceUserRole | null | undefined,
    ownerGroupName: card.ownerGroupName,
    createdAt: card.createdAt,
    updatedAt: card.updatedAt,
  };
}

function toFinanceCardKeyRedemption(
  item: CardRedemptionResponse
): FinanceCardKeyRedemption {
  return { ...item, userRole: item.userRole as FinanceUserRole };
}

function toFinanceCardKeyFacets(
  facets: CardKeyFacetsResponse
): FinanceCardKeyFacets {
  return { role: facets.role, group: facets.group, status: facets.status };
}

function toFinanceTransaction(item: AdminTransactionItem): FinanceTransaction {
  return { ...item, userRole: item.userRole as FinanceUserRole };
}

export function toFinanceUserBalance(item: AdminWalletItem): FinanceUserBalance {
  return {
    userId: item.userId,
    email: item.userEmail,
    nickname: item.userNickname,
    role: item.userRole as FinanceUserRole,
    groupName: item.userGroupName,
    consumerBalance: item.consumerBalance,
    supplierAvailable: item.supplierAvailable,
    supplierFrozen: item.supplierFrozen,
    updatedAt: item.updatedAt ?? "",
  };
}

// The wallet returned by credit/debit/withdraw carries the three balance
// buckets but no user enrichment; the balances panel merges the buckets into
// the existing enriched row, so the blank identity fields here never render.
export function walletToFinanceBalance(wallet: WalletResponse): FinanceUserBalance {
  return {
    userId: wallet.userId,
    email: "",
    nickname: "",
    role: "user",
    groupName: "",
    consumerBalance: wallet.consumerBalance,
    supplierAvailable: wallet.supplierAvailable,
    supplierFrozen: wallet.supplierFrozen,
    updatedAt: wallet.updatedAt,
  };
}

// The ledger item returned by wallet commands is not enriched and the panels
// only read the wallet buckets, so identity/reversal fields are left blank.
function transactionItemToFinance(item: TransactionItem): FinanceTransaction {
  return {
    ...item,
    userEmail: "",
    userNickname: "",
    userRole: "user",
    userGroupName: "",
    reversed: false,
    reversedByNo: null,
    reversalOfNo: null,
  };
}

function toFinanceWalletAdjustment(
  response: WalletAdjustmentResponse
): FinanceWalletAdjustmentResult {
  return {
    wallet: walletToFinanceBalance(response.wallet),
    transaction: transactionItemToFinance(response.transaction),
  };
}

function commandHeaders() {
  return {
    ...csrfHeader(),
    "Idempotency-Key": generateIdempotencyKey(),
  };
}

function toBulkResult(response: AdminBulkResponse): FinanceBulkResult {
  return { affected: response.affected, skipped: response.skipped };
}

export async function listFinanceInvites(
  filter: FinanceInviteListFilter = {},
  offset = 0,
  limit = 20
): Promise<FinancePagedResult<FinanceInvite, FinanceInviteFacets>> {
  const response = await unwrap<InviteListResponse>(
    await apiClient.GET("/v1/admin/invites", {
      params: {
        query: {
          kind: "all",
          search: filter.search,
          ownerRole: filter.ownerRole,
          ownerGroupId: filter.ownerGroupId,
          enabled: filter.enabled,
          offset,
          limit,
        },
      },
    })
  );
  return {
    items: response.invites.map(toFinanceInvite),
    total: response.total,
    offset: response.offset,
    limit: response.limit,
    facets: toFinanceInviteFacets(response.facets),
  };
}

export async function createFinanceInvite(
  input: CreateFinanceInviteInput
): Promise<FinanceInvite> {
  const response = await unwrap<{ invite: InviteResponse }>(
    await apiClient.POST("/v1/admin/invites", {
      body: {
        code: input.code ?? "",
        enabled: input.enabled,
        maxUse: input.maxUse,
        expireAt: input.expireAt,
      },
      params: { header: csrfHeader() },
    })
  );
  return toFinanceInvite(response.invite);
}

export async function batchCreateFinanceInvites(
  input: BatchCreateFinanceInviteInput
): Promise<{ items: FinanceInvite[]; created: number }> {
  const response = await unwrap<
    components["schemas"]["AdminBatchCreateInviteResponse"]
  >(
    await apiClient.POST("/v1/admin/invites/batch", {
      body: {
        count: input.count,
        maxUse: input.maxUse,
        enabled: input.enabled,
        expireAt: input.expireAt ?? undefined,
        prefix: input.prefix,
      },
      params: { header: csrfHeader() },
    })
  );
  return {
    items: response.items.map(toFinanceInvite),
    created: response.created,
  };
}

export async function updateFinanceInvite(
  code: string,
  input: UpdateFinanceInviteInput
): Promise<FinanceInvite> {
  const body: components["schemas"]["AdminUpdateInviteRequest"] = {};
  if (input.enabled !== undefined) body.enabled = input.enabled;
  if (input.maxUse !== undefined) body.maxUse = input.maxUse;
  if (input.expireAt !== undefined) body.expireAt = input.expireAt;
  const response = await unwrap<{ invite: InviteResponse }>(
    await apiClient.PATCH("/v1/admin/invites/{code}", {
      body,
      params: { header: csrfHeader(), path: { code } },
    })
  );
  return toFinanceInvite(response.invite);
}

export function setFinanceInviteEnabled(
  code: string,
  enabled: boolean
): Promise<FinanceInvite> {
  return updateFinanceInvite(code, { enabled });
}

function inviteIdsSelection(codes: string[]): InviteBulkSelection {
  return {
    mode: "ids",
    codes: Array.from(new Set(codes)).filter(Boolean),
  };
}

function inviteFilterSelection(
  filter: FinanceInviteListFilter
): InviteBulkSelection {
  return {
    mode: "filter",
    filter: {
      kind: "all",
      search: filter.search,
      ownerRole: filter.ownerRole,
      ownerGroupId: filter.ownerGroupId,
      enabled: filter.enabled,
    },
  };
}

async function inviteBulk(
  enabled: boolean,
  selection: InviteBulkSelection
): Promise<FinanceBulkResult> {
  return toBulkResult(
    await unwrap<AdminBulkResponse>(
      await apiClient.POST(
        enabled ? "/v1/admin/invites/enable" : "/v1/admin/invites/disable",
        {
          body: { selection },
          params: { header: commandHeaders() },
        }
      )
    )
  );
}

export function setFinanceInvitesEnabled(
  codes: string[],
  enabled: boolean
): Promise<FinanceBulkResult> {
  return inviteBulk(enabled, inviteIdsSelection(codes));
}

export function setFinanceInvitesEnabledByFilter(
  filter: FinanceInviteListFilter,
  enabled: boolean
): Promise<FinanceBulkResult> {
  return inviteBulk(enabled, inviteFilterSelection(filter));
}

export async function listFinanceInviteUses(
  inviteCode: string
): Promise<FinanceInviteUse[]> {
  const response = await unwrap<
    components["schemas"]["AdminInviteUsesResponse"]
  >(
    await apiClient.GET("/v1/admin/invites/{code}/uses", {
      params: { path: { code: inviteCode } },
    })
  );
  return response.uses.map(toFinanceInviteUse);
}

export async function listFinanceCardKeys(
  filter: FinanceCardKeyListFilter = {},
  offset = 0,
  limit = 20
): Promise<FinancePagedResult<FinanceCardKey, FinanceCardKeyFacets>> {
  const response = await unwrap<CardKeyListResponse>(
    await apiClient.GET("/v1/admin/cards", {
      params: {
        query: {
          search: filter.search,
          status: filter.status,
          ownerRole: filter.ownerRole,
          ownerGroupId: filter.ownerGroupId,
          offset,
          limit,
        },
      },
    })
  );
  return {
    items: response.items.map(toFinanceCardKey),
    total: response.total,
    offset: response.offset,
    limit: response.limit,
    facets: toFinanceCardKeyFacets(response.facets),
  };
}

export async function createFinanceCardKeys(
  input: CreateFinanceCardKeyInput
): Promise<{ items: FinanceCardKey[]; created: number }> {
  const response = await unwrap<components["schemas"]["CreateCardsResponse"]>(
    await apiClient.POST("/v1/admin/cards", {
      body: {
        amount: input.amount,
        count: input.count,
        maxRedemptions: input.maxRedemptions,
        expireAt: input.expireAt ?? null,
        cardKeys: input.cardKeys,
      },
      params: { header: commandHeaders() },
    })
  );
  return {
    items: response.items.map(toFinanceCardKey),
    created: response.created,
  };
}

export async function updateFinanceCardKey(
  key: string,
  input: UpdateFinanceCardKeyInput
): Promise<FinanceCardKey> {
  const body: components["schemas"]["UpdateCardRequest"] = {};
  if (input.status !== undefined) body.status = input.status;
  if (input.maxRedemptions !== undefined) {
    body.maxRedemptions = input.maxRedemptions;
  }
  if (input.expireAt !== undefined) body.expireAt = input.expireAt;
  const response = await unwrap<CardKeyResponse>(
    await apiClient.PATCH("/v1/admin/cards/{cardKey}", {
      body,
      params: { header: csrfHeader(), path: { cardKey: key } },
    })
  );
  return toFinanceCardKey(response);
}

export function setFinanceCardKeyStatus(
  key: string,
  status: FinanceCardKeyStatus
): Promise<FinanceCardKey> {
  return updateFinanceCardKey(key, { status });
}

function cardIdsSelection(keys: string[]): CardBulkSelection {
  return {
    mode: "ids",
    cardKeys: Array.from(new Set(keys)).filter(Boolean),
  };
}

function cardFilterSelection(
  filter: FinanceCardKeyListFilter
): CardBulkSelection {
  return {
    mode: "filter",
    filter: {
      search: filter.search,
      status: filter.status,
      ownerRole: filter.ownerRole,
      ownerGroupId: filter.ownerGroupId,
    },
  };
}

async function cardBulk(
  status: FinanceCardKeyStatus,
  selection: CardBulkSelection
): Promise<FinanceBulkResult> {
  return toBulkResult(
    await unwrap<AdminBulkResponse>(
      await apiClient.POST(
        status === "enabled" ? "/v1/admin/cards/enable" : "/v1/admin/cards/disable",
        {
          body: { selection },
          params: { header: commandHeaders() },
        }
      )
    )
  );
}

export function setFinanceCardKeysStatus(
  keys: string[],
  status: FinanceCardKeyStatus
): Promise<FinanceBulkResult> {
  return cardBulk(status, cardIdsSelection(keys));
}

export function setFinanceCardKeysStatusByFilter(
  filter: FinanceCardKeyListFilter,
  status: FinanceCardKeyStatus
): Promise<FinanceBulkResult> {
  return cardBulk(status, cardFilterSelection(filter));
}

export async function listFinanceCardKeyRedemptions(
  cardKey: string
): Promise<FinanceCardKeyRedemption[]> {
  const response = await unwrap<
    components["schemas"]["AdminCardRedemptionsResponse"]
  >(
    await apiClient.GET("/v1/admin/cards/{cardKey}/redemptions", {
      params: { path: { cardKey } },
    })
  );
  return response.redemptions.map(toFinanceCardKeyRedemption);
}

export async function listFinanceTransactions(
  filter: FinanceTransactionListFilter = {},
  offset = 0,
  limit = 20
): Promise<FinancePagedResult<FinanceTransaction>> {
  const response = await unwrap<
    components["schemas"]["AdminTransactionListResponse"]
  >(
    await apiClient.GET("/v1/admin/transactions", {
      params: {
        query: {
          search: filter.search,
          type: filter.transactionType,
          direction: filter.direction,
          createdFrom: filter.createdFrom,
          createdTo: filter.createdTo,
          offset,
          limit,
        },
      },
    })
  );
  return {
    items: response.items.map(toFinanceTransaction),
    total: response.total,
    offset: response.offset,
    limit: response.limit,
  };
}

// The ledger is immutable: the backend appends a compensating entry and derives
// reversal state at query time. The panels only refresh afterwards.
export async function reverseFinanceTransaction(
  id: number
): Promise<{ original: FinanceTransaction; reversal: FinanceTransaction }> {
  const response = await unwrap<
    components["schemas"]["AdminReverseTransactionResponse"]
  >(
    await apiClient.POST("/v1/admin/transactions/{id}/reverse", {
      params: { header: commandHeaders(), path: { id } },
    })
  );
  return {
    original: toFinanceTransaction(response.original),
    reversal: toFinanceTransaction(response.reversal),
  };
}

export async function listFinanceUserBalances(
  filter: FinanceUserBalanceListFilter = {},
  offset = 0,
  limit = 20
): Promise<FinancePagedResult<FinanceUserBalance>> {
  const response = await unwrap<
    components["schemas"]["AdminWalletListResponse"]
  >(
    await apiClient.GET("/v1/admin/wallets", {
      params: { query: { search: filter.search, offset, limit } },
    })
  );
  return {
    items: response.items.map(toFinanceUserBalance),
    total: response.total,
    offset: response.offset,
    limit: response.limit,
  };
}

export async function creditFinanceUserWallet(
  userId: number,
  amount: string,
  reason: string
): Promise<FinanceWalletAdjustmentResult> {
  const response = await unwrap<WalletAdjustmentResponse>(
    await apiClient.POST("/v1/admin/wallets/{userId}/credit", {
      body: { amount, reason },
      params: { header: commandHeaders(), path: { userId } },
    })
  );
  return toFinanceWalletAdjustment(response);
}

export async function debitFinanceUserWallet(
  userId: number,
  amount: string,
  reason: string
): Promise<FinanceWalletAdjustmentResult> {
  const response = await unwrap<WalletAdjustmentResponse>(
    await apiClient.POST("/v1/admin/wallets/{userId}/debit", {
      body: { amount, reason },
      params: { header: commandHeaders(), path: { userId } },
    })
  );
  return toFinanceWalletAdjustment(response);
}

// Withdrawal draws from the supplier-available bucket and writes a "withdrawal"
// ledger entry, per docs/6-billing-wallet.md.
export async function withdrawFinanceUserWallet(
  userId: number,
  amount: string,
  note: string
): Promise<FinanceWalletAdjustmentResult> {
  const response = await unwrap<WalletAdjustmentResponse>(
    await apiClient.POST("/v1/admin/wallets/{userId}/withdraw", {
      body: { amount, note },
      params: { header: commandHeaders(), path: { userId } },
    })
  );
  return toFinanceWalletAdjustment(response);
}

export async function adjustFinanceUsersWallet(
  userIds: number[],
  signedAmount: number,
  reason: string
): Promise<FinanceBulkResult> {
  const response = await unwrap<AdminUserBulkResponse>(
    await apiClient.POST("/v1/admin/wallets/adjust", {
      body: {
        selection: {
          mode: "ids",
          userIds: Array.from(new Set(userIds)).filter(
            (id) => Number.isInteger(id) && id > 0
          ),
        },
        amount: signedAmount.toFixed(6),
        reason,
      },
      params: { header: commandHeaders() },
    })
  );
  return { affected: response.affected, skipped: response.skipped };
}

export async function getFinanceSummary(
  filter: { createdFrom?: string; createdTo?: string } = {}
): Promise<FinanceSummary> {
  return unwrap<components["schemas"]["FinanceSummaryResponse"]>(
    await apiClient.GET("/v1/admin/finance/summary", {
      params: {
        query: { createdFrom: filter.createdFrom, createdTo: filter.createdTo },
      },
    })
  );
}
