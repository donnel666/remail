import type { components, operations } from "./openapi/schema";
import {
  apiClient as client,
  csrfHeader,
  unwrap,
  type JsonResponse,
} from "./api-client";

export type ResourceItem = components["schemas"]["ResourceItem"];
export type ImportResponse = components["schemas"]["ImportResponse"];
export type ImportStatusResponse =
  components["schemas"]["ImportStatusResponse"];
export type ImportErrorStrategy = NonNullable<
  operations["postResourceImport"]["requestBody"]["content"]["multipart/form-data"]["errorStrategy"]
>;
export type PublishResourcesRequest =
  components["schemas"]["PublishResourcesRequest"];
export type PublishResourcesResponse =
  components["schemas"]["PublishResourcesResponse"];
export type DeleteResourcesRequest =
  components["schemas"]["DeleteResourcesRequest"];
export type DeleteResourcesResponse =
  components["schemas"]["DeleteResourcesResponse"];
export type ResourceBulkFilter = components["schemas"]["ResourceBulkFilter"];
export type MicrosoftResourceDetail =
  components["schemas"]["MicrosoftResourceDetail"];
export type DomainResourceDetail = components["schemas"]["DomainResourceDetail"];
export type ResourcePublishDetail =
  | MicrosoftResourceDetail
  | DomainResourceDetail;
export type CreateDomainRequest = components["schemas"]["CreateDomainRequest"];
export type SupplierApplicationResponse =
  components["schemas"]["SupplierApplicationResponse"];
export type SupplierApplicationCurrentResponse =
  components["schemas"]["SupplierApplicationCurrentResponse"];
export type SupplierApplicationRequest =
  components["schemas"]["SupplierApplicationRequest"];
export type ResourceListResponse =
  components["schemas"]["ResourceListResponse"];
export type SupplierApplicationSubmitResponse = JsonResponse<
  operations["postSupplierApplication"],
  201
>;

const resourcePageLimit = 10_000;
const resourcePageConcurrency = 4;

interface ListOwnedResourcesOptions {
  concurrency?: number;
  onPage?: (items: ResourceItem[], response: ResourceListResponse) => void;
}

function normalizePageConcurrency(value: number | undefined) {
  if (!Number.isFinite(value)) return resourcePageConcurrency;
  return Math.max(1, Math.min(8, Math.floor(value as number)));
}

export async function listOwnedMicrosoftResources(
  options: ListOwnedResourcesOptions = {}
) {
  return listOwnedResourcesByType("microsoft", options);
}

export async function listOwnedDomainResources(
  options: ListOwnedResourcesOptions = {}
) {
  return listOwnedResourcesByType("domain", options);
}

async function listOwnedResourcesByType(
  resourceType: "microsoft" | "domain",
  options: ListOwnedResourcesOptions = {}
) {
  const collectItems = !options.onPage;
  const items: ResourceItem[] = [];
  let loaded = 0;
  let total = 0;
  let latest: ResourceListResponse | null = null;

  const firstResponse = await listOwnedResourcesPage(resourceType, 0);
  latest = firstResponse;
  total = firstResponse.total;
  loaded = firstResponse.items.length;
  if (collectItems) items.push(...firstResponse.items);
  options.onPage?.(firstResponse.items, firstResponse);

  if (firstResponse.items.length === 0 || loaded >= total) {
    return {
      ...firstResponse,
      items,
      limit: loaded,
      offset: 0,
      total,
    };
  }

  const pageOffsets: number[] = [];
  for (
    let pageOffset = firstResponse.items.length;
    pageOffset < total;
    pageOffset += resourcePageLimit
  ) {
    pageOffsets.push(pageOffset);
  }

  const pageBuffer = new Map<number, ResourceListResponse>();
  let nextFlushOffset = firstResponse.items.length;
  let nextOffsetIndex = 0;
  let firstError: unknown;

  const flushReadyPages = () => {
    for (;;) {
      const response = pageBuffer.get(nextFlushOffset);
      if (!response) return;

      pageBuffer.delete(nextFlushOffset);
      latest = response;
      loaded += response.items.length;
      if (collectItems) items.push(...response.items);
      options.onPage?.(response.items, response);
      nextFlushOffset += resourcePageLimit;
    }
  };

  const workerCount = Math.min(
    normalizePageConcurrency(options.concurrency),
    pageOffsets.length
  );
  await Promise.all(
    Array.from({ length: workerCount }, async () => {
      while (!firstError) {
        const pageOffset = pageOffsets[nextOffsetIndex];
        if (pageOffset === undefined) return;
        nextOffsetIndex += 1;

        let response: ResourceListResponse;
        try {
          response = await listOwnedResourcesPage(resourceType, pageOffset);
        } catch (error) {
          firstError ??= error;
          return;
        }
        if (firstError) return;

        pageBuffer.set(pageOffset, response);
        flushReadyPages();
      }
    })
  );

  if (firstError) throw firstError;

  return {
    ...(latest as ResourceListResponse),
    items,
    limit: loaded,
    offset: 0,
    total,
  };
}

async function listOwnedResourcesPage(
  resourceType: "microsoft" | "domain",
  offset: number
) {
  return unwrap<ResourceListResponse>(
    await client.GET("/v1/resources", {
      params: {
        query: {
          scope: "owned",
          type: resourceType,
          offset,
          limit: resourcePageLimit,
        },
      },
    })
  );
}

export async function importMicrosoftResources(
  file: File,
  longLived: boolean,
  errorStrategy: ImportErrorStrategy = "skip"
) {
  const formData = new FormData();
  formData.append("file", file);
  formData.append("longLived", String(longLived));
  formData.append("errorStrategy", errorStrategy);

  return unwrap<ImportResponse>(
    await client.POST("/v1/resources/imports", {
      body: formData as never,
      bodySerializer: (body) => body,
      params: { header: csrfHeader() },
    })
  );
}

export async function getResourceImportStatus(importId: number) {
  return unwrap<ImportStatusResponse>(
    await client.GET("/v1/resource-imports/{importId}", {
      params: { path: { importId } },
    })
  );
}

export async function waitForResourceImport(
  importId: number,
  options: { intervalMs?: number; maxAttempts?: number } = {}
) {
  const intervalMs = options.intervalMs ?? 1000;
  const maxAttempts = options.maxAttempts ?? 120;

  for (let attempt = 0; attempt < maxAttempts; attempt += 1) {
    const status = await getResourceImportStatus(importId);
    if (status.status !== "processing") {
      return status;
    }
    await new Promise((resolve) => globalThis.setTimeout(resolve, intervalMs));
  }

  return getResourceImportStatus(importId);
}

export async function publishResourcesBatch(
  payload: PublishResourcesRequest
) {
  return unwrap<PublishResourcesResponse>(
    await client.POST("/v1/resources/publish", {
      body: payload,
      params: { header: csrfHeader() },
    })
  );
}

function uniquePositiveResourceIds(resourceIds: number[]) {
  return Array.from(new Set(resourceIds)).filter((id) => id > 0);
}

function idsSelection(resourceIds: number[]) {
  return {
    mode: "ids" as const,
    resourceIds: uniquePositiveResourceIds(resourceIds),
  };
}

function filterSelection(filter: ResourceBulkFilter) {
  return {
    mode: "filter" as const,
    filter,
  };
}

export function publishMicrosoftResourcesBatch(resourceIds: number[]) {
  return publishResourcesBatch({
    selection: idsSelection(resourceIds),
  });
}

export function publishDomainResourcesBatch(resourceIds: number[]) {
  return publishResourcesBatch({
    selection: idsSelection(resourceIds),
  });
}

export function publishMicrosoftResourcesByFilter(filter: ResourceBulkFilter) {
  return publishResourcesBatch({
    selection: filterSelection(filter),
  });
}

export function publishDomainResourcesByFilter(filter: ResourceBulkFilter) {
  return publishResourcesBatch({
    selection: filterSelection(filter),
  });
}

export async function deleteResourcesBatch(payload: DeleteResourcesRequest) {
  return unwrap<DeleteResourcesResponse>(
    await client.POST("/v1/resources/delete", {
      body: payload,
      params: { header: csrfHeader() },
    })
  );
}

export async function publishMicrosoftResource(resourceId: number) {
  return (await publishResource(resourceId)) as MicrosoftResourceDetail;
}

export async function publishDomainResource(resourceId: number) {
  return (await publishResource(resourceId)) as DomainResourceDetail;
}

async function publishResource(resourceId: number) {
  return unwrap<ResourcePublishDetail>(
    await client.POST("/v1/resources/{resourceId}/publish", {
      params: {
        header: csrfHeader(),
        path: { resourceId },
      },
    })
  );
}

export async function createDomainResource(payload: CreateDomainRequest) {
  return unwrap<DomainResourceDetail>(
    await client.POST("/v1/domains", {
      body: payload,
      params: { header: csrfHeader() },
    })
  );
}

async function deleteResource(resourceId: number) {
  await unwrap<void>(
    await client.DELETE("/v1/resources/{resourceId}", {
      params: {
        header: csrfHeader(),
        path: { resourceId },
      },
    })
  );
}

export async function deleteMicrosoftResource(resourceId: number) {
  await deleteResource(resourceId);
}

export async function deleteDomainResource(resourceId: number) {
  await deleteResource(resourceId);
}

async function deleteResourcesViaBatchEndpoint(
  resourceIds: number[],
  options: { concurrency?: number; onDeleted?: (resourceId: number) => void } = {}
) {
  void options.concurrency;
  const ids = uniquePositiveResourceIds(resourceIds);
  const response = await deleteResourcesBatch({
    selection: idsSelection(ids),
  });
  for (const resourceId of response.deletedResourceIds ?? []) {
    options.onDeleted?.(resourceId);
  }
  return response;
}

async function deleteResourcesViaFilterEndpoint(filter: ResourceBulkFilter) {
  return deleteResourcesBatch({
    selection: filterSelection(filter),
  });
}

export async function deleteMicrosoftResourcesBatch(
  resourceIds: number[],
  options: { concurrency?: number; onDeleted?: (resourceId: number) => void } = {}
) {
  return deleteResourcesViaBatchEndpoint(resourceIds, options);
}

export async function deleteDomainResourcesBatch(
  resourceIds: number[],
  options: { concurrency?: number; onDeleted?: (resourceId: number) => void } = {}
) {
  return deleteResourcesViaBatchEndpoint(resourceIds, options);
}

export async function deleteMicrosoftResourcesByFilter(filter: ResourceBulkFilter) {
  return deleteResourcesViaFilterEndpoint(filter);
}

export async function deleteDomainResourcesByFilter(filter: ResourceBulkFilter) {
  return deleteResourcesViaFilterEndpoint(filter);
}

export async function getCurrentSupplierApplication() {
  return unwrap<SupplierApplicationCurrentResponse>(
    await client.GET("/v1/supplier-applications/current")
  );
}

export async function submitSupplierApplication(
  payload: SupplierApplicationRequest
) {
  return unwrap<SupplierApplicationSubmitResponse>(
    await client.POST("/v1/supplier-applications", {
      body: payload,
      params: { header: csrfHeader() },
    })
  );
}
