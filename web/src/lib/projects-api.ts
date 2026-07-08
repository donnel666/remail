import type { components } from "./openapi/schema";
import { apiClient as client, csrfHeader, unwrap } from "./api-client";

export type AdminCreateProjectRequest =
  components["schemas"]["AdminCreateProjectRequest"];
export type AdminUpdateProjectRequest = AdminCreateProjectRequest;
export type CreateProjectApplicationRequest =
  components["schemas"]["CreateProjectApplicationRequest"];
export type ProjectBulkCommandResponse =
  components["schemas"]["ProjectBulkCommandResponse"];
export type ProjectBulkFilter = components["schemas"]["ProjectBulkFilter"];
export type ProjectAccess = components["schemas"]["ProjectAccess"];
export type ProjectAccessListResponse =
  components["schemas"]["ProjectAccessListResponse"];
export type ProjectDetailResponse =
  components["schemas"]["ProjectDetailResponse"];
export type ProjectItem = components["schemas"]["ProjectItem"];
export type ProjectListResponse = components["schemas"]["ProjectListResponse"];
export type ProjectMailRule = components["schemas"]["ProjectMailRule"];
export type ProjectMailRuleRequest =
  components["schemas"]["ProjectMailRuleRequest"];
export type ProjectProduct = components["schemas"]["ProjectProduct"];
export type ProjectProductSummary =
  components["schemas"]["ProjectProductSummary"];
export type ProjectInventoryTotalResponse =
  components["schemas"]["ProjectInventoryTotalResponse"];
export type ProjectProductRequest =
  components["schemas"]["ProjectProductRequest"];

export interface ProjectListFilter {
  accessType?: "public" | "private";
  createdFrom?: string;
  createdTo?: string;
  looseMatch?: boolean;
  productType?: "microsoft" | "domain";
  scope?: "visible" | "mine" | "all";
  search?: string;
  status?: "reviewing" | "listed" | "delisted";
  targetPlatform?: string;
}

function toBulkFilter(filter: ProjectListFilter): ProjectBulkFilter {
  const bulkFilter: ProjectBulkFilter = {};
  if (filter.accessType) bulkFilter.accessType = filter.accessType;
  if (filter.createdFrom) bulkFilter.createdFrom = filter.createdFrom;
  if (filter.createdTo) bulkFilter.createdTo = filter.createdTo;
  if (typeof filter.looseMatch === "boolean") {
    bulkFilter.looseMatch = filter.looseMatch;
  }
  if (filter.productType) bulkFilter.productType = filter.productType;
  if (filter.search) bulkFilter.search = filter.search;
  if (filter.status) bulkFilter.status = filter.status;
  if (filter.targetPlatform) bulkFilter.targetPlatform = filter.targetPlatform;
  return bulkFilter;
}

function projectIdSelection(projectIds: number[]) {
  return {
    mode: "ids" as const,
    projectIds: Array.from(new Set(projectIds)).filter((id) => Number.isInteger(id) && id > 0),
  };
}

function filterSelection(filter: ProjectListFilter) {
  return {
    mode: "filter" as const,
    filter: toBulkFilter(filter),
  };
}

export async function listProjects(
  filter: ProjectListFilter = {},
  offset = 0,
  limit = 20
) {
  return unwrap<ProjectListResponse>(
    await client.GET("/v1/projects", {
      params: {
        query: {
          ...filter,
          offset,
          limit,
        },
      },
    })
  );
}

export async function getProject(projectId: number) {
  return unwrap<ProjectDetailResponse>(
    await client.GET("/v1/projects/{projectId}", {
      params: { path: { projectId } },
    })
  );
}

export async function getProjectInventory(projectId: number) {
  return unwrap<ProjectInventoryTotalResponse>(
    await client.GET("/v1/projects/{projectId}/inventory", {
      params: { path: { projectId } },
    })
  );
}

export async function createProjectApplication(
  payload: CreateProjectApplicationRequest
) {
  return unwrap<ProjectDetailResponse>(
    await client.POST("/v1/projects", {
      body: payload,
      params: { header: csrfHeader() },
    })
  );
}

export async function resubmitProjectApplication(
  projectId: number,
  payload: CreateProjectApplicationRequest
) {
  return unwrap<ProjectDetailResponse>(
    await client.POST("/v1/projects/{projectId}/resubmit", {
      body: payload,
      params: {
        header: csrfHeader(),
        path: { projectId },
      },
    })
  );
}

export async function createAdminProject(payload: AdminCreateProjectRequest) {
  return unwrap<ProjectDetailResponse>(
    await client.POST("/v1/admin/projects", {
      body: payload,
      params: { header: csrfHeader() },
    })
  );
}

export async function updateAdminProject(
  projectId: number,
  payload: AdminUpdateProjectRequest
) {
  return unwrap<ProjectDetailResponse>(
    await client.PUT("/v1/admin/projects/{projectId}", {
      body: payload,
      params: {
        header: csrfHeader(),
        path: { projectId },
      },
    })
  );
}

export async function approveAdminProject(
  projectId: number,
  payload?: AdminCreateProjectRequest
) {
  return unwrap<ProjectDetailResponse>(
    await client.POST("/v1/admin/projects/{projectId}/approve", {
      body: payload,
      params: {
        header: csrfHeader(),
        path: { projectId },
      },
    })
  );
}

export async function rejectAdminProject(projectId: number, reviewReason: string) {
  return unwrap<ProjectDetailResponse>(
    await client.POST("/v1/admin/projects/{projectId}/reject", {
      body: { reviewReason },
      params: {
        header: csrfHeader(),
        path: { projectId },
      },
    })
  );
}

export async function duplicateAdminProject(projectId: number, reviewReason: string) {
  return unwrap<ProjectDetailResponse>(
    await client.POST("/v1/admin/projects/{projectId}/duplicate", {
      body: { reviewReason },
      params: {
        header: csrfHeader(),
        path: { projectId },
      },
    })
  );
}

export async function delistAdminProject(projectId: number) {
  return unwrap<ProjectDetailResponse>(
    await client.POST("/v1/admin/projects/{projectId}/delist", {
      params: {
        header: csrfHeader(),
        path: { projectId },
      },
    })
  );
}

export async function relistAdminProject(projectId: number) {
  return unwrap<ProjectDetailResponse>(
    await client.POST("/v1/admin/projects/{projectId}/relist", {
      params: {
        header: csrfHeader(),
        path: { projectId },
      },
    })
  );
}

export async function deleteAdminProject(projectId: number) {
  await unwrap<void>(
    await client.DELETE("/v1/admin/projects/{projectId}", {
      params: {
        header: csrfHeader(),
        path: { projectId },
      },
    })
  );
}

export async function relistAdminProjectsByIds(projectIds: number[]) {
  return unwrap<ProjectBulkCommandResponse>(
    await client.POST("/v1/admin/projects/relist", {
      body: { selection: projectIdSelection(projectIds) },
      params: { header: csrfHeader() },
    })
  );
}

export async function relistAdminProjectsByFilter(filter: ProjectListFilter) {
  return unwrap<ProjectBulkCommandResponse>(
    await client.POST("/v1/admin/projects/relist", {
      body: { selection: filterSelection(filter) },
      params: { header: csrfHeader() },
    })
  );
}

export async function delistAdminProjectsByIds(projectIds: number[]) {
  return unwrap<ProjectBulkCommandResponse>(
    await client.POST("/v1/admin/projects/delist", {
      body: { selection: projectIdSelection(projectIds) },
      params: { header: csrfHeader() },
    })
  );
}

export async function delistAdminProjectsByFilter(filter: ProjectListFilter) {
  return unwrap<ProjectBulkCommandResponse>(
    await client.POST("/v1/admin/projects/delist", {
      body: { selection: filterSelection(filter) },
      params: { header: csrfHeader() },
    })
  );
}

export async function deleteAdminProjectsByIds(projectIds: number[]) {
  return unwrap<ProjectBulkCommandResponse>(
    await client.POST("/v1/admin/projects/delete", {
      body: { selection: projectIdSelection(projectIds) },
      params: { header: csrfHeader() },
    })
  );
}

export async function deleteAdminProjectsByFilter(filter: ProjectListFilter) {
  return unwrap<ProjectBulkCommandResponse>(
    await client.POST("/v1/admin/projects/delete", {
      body: { selection: filterSelection(filter) },
      params: { header: csrfHeader() },
    })
  );
}

export async function listAdminProjectAccess(projectId: number) {
  return unwrap<ProjectAccessListResponse>(
    await client.GET("/v1/admin/projects/{projectId}/access", {
      params: { path: { projectId } },
    })
  );
}

export async function grantAdminProjectAccess(projectId: number, userId: number) {
  return unwrap<ProjectAccess>(
    await client.POST("/v1/admin/projects/{projectId}/access", {
      body: { userId },
      params: {
        header: csrfHeader(),
        path: { projectId },
      },
    })
  );
}

export async function revokeAdminProjectAccess(projectId: number, userId: number) {
  await unwrap<void>(
    await client.DELETE("/v1/admin/projects/{projectId}/access/{userId}", {
      params: {
        header: csrfHeader(),
        path: { projectId, userId },
      },
    })
  );
}

export async function uploadAdminProjectLogo(file: File) {
  const formData = new FormData();
  formData.append("file", file);
  return unwrap<components["schemas"]["ProjectLogoUploadResponse"]>(
    await client.POST("/v1/admin/project-logos", {
      body: formData as never,
      bodySerializer: (body) => body,
      params: { header: csrfHeader() },
    })
  );
}
