import type { components, operations } from "@/lib/openapi/schema";

export type AdminMicrosoftResourceStatus =
  components["schemas"]["AdminMicrosoftResourceStatus"];
export type AdminMicrosoftTokenHealth =
  components["schemas"]["AdminMicrosoftTokenHealth"];
export type AdminMicrosoftOwner =
  components["schemas"]["AdminMicrosoftOwnerSummary"];
export type AdminMicrosoftOwnerRole = AdminMicrosoftOwner["role"];

export type AdminMicrosoftResourceItem =
  components["schemas"]["AdminMicrosoftResourceItem"];
export type AdminMicrosoftResourceDetail =
  components["schemas"]["AdminMicrosoftResourceDetail"];
export type AdminMicrosoftCredentialConfiguration =
  components["schemas"]["AdminMicrosoftCredentialConfiguration"];
export type AdminMicrosoftTokenDiagnostic =
  components["schemas"]["AdminMicrosoftTokenDiagnostic"];
export type AdminMicrosoftAliasCounts =
  components["schemas"]["AdminMicrosoftAliasCounts"];

export type AdminMicrosoftTaskKind = components["schemas"]["AdminTaskKind"];
export type AdminMicrosoftAsyncTaskKind = AdminMicrosoftTaskKind;
export type AdminMicrosoftTaskStatus =
  components["schemas"]["AdminTaskStatus"];
export type AdminMicrosoftAsyncTaskStatus = AdminMicrosoftTaskStatus;
export type AdminMicrosoftActiveTaskStatus = AdminMicrosoftTaskStatus;
export type AdminMicrosoftAsyncTask = components["schemas"]["AdminTaskView"];
export type AdminMicrosoftTaskSummary =
  components["schemas"]["AdminTaskSummary"];
export type AdminMicrosoftTaskListResponse =
  components["schemas"]["AdminTaskListResponse"];
export type AdminMicrosoftTaskAcceptedResponse =
  components["schemas"]["AdminTaskAcceptedResponse"];
export type AdminMicrosoftValidationResponse =
  components["schemas"]["ResourceValidationsResponse"];
export type AdminMicrosoftMaintenanceAction =
  components["schemas"]["AdminMicrosoftMaintenanceAction"];

export type AdminMicrosoftAliasKind =
  components["schemas"]["AdminMicrosoftAliasItem"]["kind"];
export type AdminMicrosoftAliasSample =
  components["schemas"]["AdminMicrosoftAliasItem"];
export type AdminMicrosoftAliasSchedule =
  components["schemas"]["AdminMicrosoftAliasSchedule"];
export type AdminMicrosoftAliasListResponse =
  components["schemas"]["AdminMicrosoftAliasListResponse"];

export type AdminMicrosoftMailboxKind =
  components["schemas"]["AdminMessageSummary"]["mailbox"];
export type AdminMicrosoftMessageStatus =
  components["schemas"]["AdminMessageSummary"]["status"];
export type AdminMicrosoftMessageSummary =
  components["schemas"]["AdminMessageSummary"];
export type AdminMicrosoftMessageDetail =
  components["schemas"]["AdminMessageDetail"];
export type AdminMicrosoftMessageListResponse =
  components["schemas"]["AdminMessageListResponse"];
export type AdminMicrosoftAuxiliaryMessageSummary =
  components["schemas"]["AdminAuxiliaryMessageSummary"];
export type AdminMicrosoftAuxiliaryMessageDetail =
  components["schemas"]["AdminAuxiliaryMessageDetail"];
export type AdminMicrosoftBindingMessageListResponse =
  components["schemas"]["AdminBindingMessageListResponse"];
export type AdminMicrosoftBindingSummary =
  components["schemas"]["AdminBindingSummary"];

export interface AdminMicrosoftMessageCursor {
  beforeReceivedAt: string;
  beforeId: number;
}

export type AdminMicrosoftSupplyScope =
  components["schemas"]["AdminAllocationItem"]["supplyScope"];
export type AdminMicrosoftAllocationStatus =
  components["schemas"]["AdminAllocationItem"]["status"];
export type AdminMicrosoftServiceMode =
  components["schemas"]["AdminAllocationItem"]["serviceMode"];
export type AdminMicrosoftOrderStatus =
  components["schemas"]["AdminAllocationItem"]["orderStatus"];
export type AdminMicrosoftAllocation =
  components["schemas"]["AdminAllocationItem"];
export type AdminMicrosoftAllocationListResponse =
  components["schemas"]["AdminAllocationListResponse"];

type AdminMicrosoftListQuery =
  operations["getAdminMicrosoftResources"]["parameters"]["query"];

export type AdminMicrosoftListFilter = Omit<
  AdminMicrosoftListQuery,
  "type" | "offset" | "limit" | "afterId" | "status" | "tokenHealth"
> & {
  status?: AdminMicrosoftResourceStatus | "all";
  tokenHealth?: AdminMicrosoftTokenHealth | "all";
};

export type AdminMicrosoftStatusFacet =
  components["schemas"]["AdminMicrosoftStatusFacet"];
export type AdminMicrosoftBooleanFacet =
  components["schemas"]["AdminMicrosoftBooleanFacet"];
export type AdminMicrosoftTokenHealthFacet =
  components["schemas"]["AdminMicrosoftTokenHealthFacet"];
export type AdminMicrosoftFacets =
  components["schemas"]["AdminMicrosoftFacets"];
export type AdminMicrosoftListResponse =
  components["schemas"]["AdminMicrosoftResourceListResponse"];

type AdminMicrosoftImportMultipart =
  operations["postAdminMicrosoftResourceImport"]["requestBody"]["content"]["multipart/form-data"];

export type AdminMicrosoftImportErrorStrategy =
  AdminMicrosoftImportMultipart["errorStrategy"];

export interface ImportAdminMicrosoftResourcesRequest {
  content: string;
  ownerId: AdminMicrosoftImportMultipart["ownerId"];
  longLived: AdminMicrosoftImportMultipart["longLived"];
  errorStrategy: AdminMicrosoftImportErrorStrategy;
}

export type AdminMicrosoftImportResponse =
  components["schemas"]["AdminMicrosoftImportResponse"];
export type ReplaceAdminMicrosoftCredentialsRequest =
  components["schemas"]["AdminMicrosoftReplaceCredentialsRequest"];
export type UpdateAdminMicrosoftResourceRequest =
  components["schemas"]["AdminMicrosoftUpdateRequest"];
export type AdminMicrosoftMutationResponse =
  components["schemas"]["AdminMicrosoftMutationResponse"];
export type AdminMicrosoftBulkFilter =
  components["schemas"]["AdminMicrosoftBulkFilter"];
export type AdminMicrosoftResourceSelection =
  components["schemas"]["AdminMicrosoftBulkSelection"];
export type AdminMicrosoftIdsSelection =
  components["schemas"]["AdminMicrosoftIdsSelection"];
export type AdminMicrosoftBulkResponse =
  components["schemas"]["AdminMicrosoftBulkResult"];
