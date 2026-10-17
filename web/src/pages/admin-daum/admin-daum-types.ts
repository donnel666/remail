import type { components } from "@/lib/openapi/schema";

export type AdminDaumResourceStatus = components["schemas"]["DaumResourceStatus"];
export type AdminDaumOwner = components["schemas"]["DaumOwnerSummary"];
export type AdminDaumOwnerRole = AdminDaumOwner["role"];
export type AdminDaumResourceItem = Omit<components["schemas"]["DaumResource"], "owner"> & {
  emailAddress: string;
  suffix: string;
  owner: AdminDaumOwner | null;
  qualityScore: number;
  longLived: boolean;
};
export type AdminDaumResourceDetail = AdminDaumResourceItem & {
  credentials: { passwordConfigured: boolean; appPasswordConfigured: boolean; revision: number; updatedAt?: string | null };
};
export type AdminDaumTaskKind = components["schemas"]["AdminTaskKind"];
export type AdminDaumAsyncTaskKind = AdminDaumTaskKind;
export type AdminDaumTaskStatus = components["schemas"]["AdminTaskStatus"];
export type AdminDaumAsyncTaskStatus = AdminDaumTaskStatus;
export type AdminDaumAsyncTask = components["schemas"]["AdminTaskView"];
export type AdminDaumTaskListResponse = components["schemas"]["AdminTaskListResponse"];
export type AdminDaumTaskAcceptedResponse = components["schemas"]["AdminTaskAcceptedResponse"];
export type AdminDaumMaintenanceAction = "validate" | "history";
export type AdminDaumMailboxKind = components["schemas"]["AdminMessageSummary"]["mailbox"];
export type AdminDaumMessageStatus = components["schemas"]["AdminMessageSummary"]["status"];
export type AdminDaumMessageSummary = components["schemas"]["AdminMessageSummary"];
export type AdminDaumMessageDetail = components["schemas"]["AdminMessageDetail"];
export type AdminDaumMessageListResponse = components["schemas"]["AdminMessageListResponse"];
export interface AdminDaumMessageCursor { beforeReceivedAt: string; beforeId: number }
export type AdminDaumSupplyScope = components["schemas"]["AdminAllocationItem"]["supplyScope"];
export type AdminDaumAllocationStatus = components["schemas"]["AdminAllocationItem"]["status"];
export type AdminDaumAllocation = components["schemas"]["AdminAllocationItem"];
export type AdminDaumAllocationListResponse = components["schemas"]["AdminAllocationListResponse"];
export interface AdminDaumListFilter {
  search?: string;
  status?: AdminDaumResourceStatus | "all";
  forSale?: boolean;
  longLived?: boolean;
  suffix?: string;
  protocol?: "imap" | "pop3";
  ownerId?: number;
  createdFrom?: string;
  createdTo?: string;
}
export type DaumBooleanFacet = { all: number; yes: number; no: number };
export interface AdminDaumFacets {
  status: Record<AdminDaumResourceStatus | "all", number>;
  forSale: DaumBooleanFacet;
  longLived: DaumBooleanFacet;
  suffixes: Array<{ key: string; count: number }>;
}
export interface AdminDaumListResponse {
  items: AdminDaumResourceItem[];
  total?: number;
  nextAfterId?: number | null;
  facets?: AdminDaumFacets;
}
export type AdminDaumImportErrorStrategy = "skip" | "abort";
export type AdminDaumImportResponse = components["schemas"]["DaumImportResponse"];
export type AdminDaumResourceSelection =
  | { mode: "ids"; resourceIds: number[] }
  | { mode: "filter"; filter: Omit<AdminDaumListFilter, "status"> & { status?: AdminDaumResourceStatus } };
export interface AdminDaumBulkResponse {
  taskId?: string;
  status?: string;
  requested: number;
  processed?: number;
  affected: number;
  skipped: number;
  affectedResourceIds?: number[];
  skippedResourceIds?: number[];
  reasonCounts: Array<{ reason: string; count: number }>;
}
export type UpdateAdminDaumResourceRequest = components["schemas"]["DaumResourceEditRequest"];
