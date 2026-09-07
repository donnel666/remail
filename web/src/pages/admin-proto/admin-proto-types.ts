import type { components } from "@/lib/openapi/schema";

export type AdminProtoResourceStatus = components["schemas"]["ProtoResourceStatus"];
export type AdminProtoOwner = components["schemas"]["ProtoOwnerSummary"];
export type AdminProtoOwnerRole = AdminProtoOwner["role"];
export type AdminProtoResourceItem = Omit<components["schemas"]["ProtoResource"], "owner"> & {
  emailAddress: string;
  suffix: string;
  owner: AdminProtoOwner | null;
  qualityScore: number;
  longLived: boolean;
};
export type AdminProtoResourceDetail = AdminProtoResourceItem & {
  credentials: { passwordConfigured: boolean; revision: number; updatedAt?: string | null };
};
export type AdminProtoTaskKind = components["schemas"]["AdminTaskKind"];
export type AdminProtoAsyncTaskKind = AdminProtoTaskKind;
export type AdminProtoTaskStatus = components["schemas"]["AdminTaskStatus"];
export type AdminProtoAsyncTaskStatus = AdminProtoTaskStatus;
export type AdminProtoAsyncTask = components["schemas"]["AdminTaskView"];
export type AdminProtoTaskListResponse = components["schemas"]["AdminTaskListResponse"];
export type AdminProtoTaskAcceptedResponse = components["schemas"]["AdminTaskAcceptedResponse"];
export type AdminProtoMaintenanceAction = "validate" | "history";
export type AdminProtoMailboxKind = components["schemas"]["AdminMessageSummary"]["mailbox"];
export type AdminProtoMessageStatus = components["schemas"]["AdminMessageSummary"]["status"];
export type AdminProtoMessageSummary = components["schemas"]["AdminMessageSummary"];
export type AdminProtoMessageDetail = components["schemas"]["AdminMessageDetail"];
export type AdminProtoMessageListResponse = components["schemas"]["AdminMessageListResponse"];
export interface AdminProtoMessageCursor { beforeReceivedAt: string; beforeId: number }
export type AdminProtoSupplyScope = components["schemas"]["AdminAllocationItem"]["supplyScope"];
export type AdminProtoAllocationStatus = components["schemas"]["AdminAllocationItem"]["status"];
export type AdminProtoAllocation = components["schemas"]["AdminAllocationItem"];
export type AdminProtoAllocationListResponse = components["schemas"]["AdminAllocationListResponse"];
export interface AdminProtoListFilter {
  search?: string;
  status?: AdminProtoResourceStatus | "all";
  forSale?: boolean;
  longLived?: boolean;
  suffix?: string;
  ownerId?: number;
  createdFrom?: string;
  createdTo?: string;
}
export type ProtoBooleanFacet = { all: number; yes: number; no: number };
export interface AdminProtoFacets {
  status: Record<AdminProtoResourceStatus | "all", number>;
  forSale: ProtoBooleanFacet;
  longLived: ProtoBooleanFacet;
  suffixes: Array<{ key: string; count: number }>;
}
export interface AdminProtoListResponse {
  items: AdminProtoResourceItem[];
  total?: number;
  nextAfterId?: number | null;
  facets?: AdminProtoFacets;
}
export type AdminProtoImportErrorStrategy = "skip" | "abort";
export type AdminProtoImportResponse = components["schemas"]["ProtoImportResponse"];
export type AdminProtoResourceSelection =
  | { mode: "ids"; resourceIds: number[] }
  | { mode: "filter"; filter: Omit<AdminProtoListFilter, "status"> & { status?: AdminProtoResourceStatus } };
export interface AdminProtoBulkResponse {
  taskId?: string;
  status?: string;
  requested: number;
  processed?: number;
  affected: number;
  skipped: number;
  reasonCounts: Array<{ reason: string; count: number }>;
}
export interface UpdateAdminProtoResourceRequest {
  version: number;
  email?: string;
  ownerId?: number;
  qualityScore?: number;
  longLived?: boolean;
}
