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
export type ValidateResourcesRequest =
  components["schemas"]["ValidateResourcesRequest"];
export type ResourceValidationsResponse =
  components["schemas"]["ResourceValidationsResponse"];
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
export type ResourceValidationResponse =
  components["schemas"]["ResourceValidationResponse"];
export type SupplierApplicationSubmitResponse = JsonResponse<
  operations["postSupplierApplication"],
  201
>;

export interface ResourceListFilter {
  createdFrom?: string;
  createdTo?: string;
  forSale?: boolean;
  graphAvailable?: boolean;
  longLived?: boolean;
  purpose?: "not_sale" | "sale" | "binding";
  search?: string;
  status?: string;
  suffix?: string;
  tld?: string;
}

export async function listOwnedMicrosoftResources(
  filter: ResourceListFilter = {},
  offset = 0,
  limit = 20,
  afterId?: number
) {
  return listOwnedResources("microsoft", filter, offset, limit, afterId);
}

export async function listOwnedDomainResources(
  filter: ResourceListFilter = {},
  offset = 0,
  limit = 20,
  afterId?: number
) {
  return listOwnedResources("domain", filter, offset, limit, afterId);
}

async function listOwnedResources(
  resourceType: "microsoft" | "domain",
  filter: ResourceListFilter,
  offset: number,
  limit: number,
  afterId?: number
) {
  return unwrap<ResourceListResponse>(
    await client.GET("/v1/resources", {
      params: {
        query: {
          ...filter,
          scope: "owned",
          type: resourceType,
          offset,
          afterId,
          limit,
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

export async function getResourceImportStatus(
  importId: number,
  signal?: AbortSignal
) {
  return unwrap<ImportStatusResponse>(
    await client.GET("/v1/resources/imports/{importId}", {
      params: { path: { importId } },
      signal,
    })
  );
}

export async function validateResource(resourceId: number) {
  return unwrap<ResourceValidationResponse>(
    await client.POST("/v1/resources/{resourceId}/validate", {
      params: {
        header: csrfHeader(),
        path: { resourceId },
      },
    })
  );
}

export async function getResourceValidationStatus(validationId: number) {
  return unwrap<ResourceValidationResponse>(
    await client.GET("/v1/resources/validations/{validationId}", {
      params: { path: { validationId } },
    })
  );
}

export async function validateResourcesBatch(
  payload: ValidateResourcesRequest
) {
  return unwrap<ResourceValidationsResponse>(
    await client.POST("/v1/resources/validations", {
      body: payload,
      params: { header: csrfHeader() },
    })
  );
}

export async function waitForResourceImport(
  importId: number,
  options: {
    intervalMs?: number;
    maxAttempts?: number;
    signal?: AbortSignal;
  } = {}
) {
  const intervalMs = options.intervalMs ?? 1000;
  const maxAttempts = options.maxAttempts ?? 120;

  for (let attempt = 0; attempt < maxAttempts; attempt += 1) {
    throwIfAborted(options.signal);
    const status = await getResourceImportStatus(importId, options.signal);
    if (status.status !== "processing") {
      return status;
    }
    await abortableDelay(intervalMs, options.signal);
  }

  throwIfAborted(options.signal);
  return getResourceImportStatus(importId, options.signal);
}

function throwIfAborted(signal?: AbortSignal) {
  if (!signal?.aborted) return;
  throw new DOMException("The operation was aborted.", "AbortError");
}

function abortableDelay(ms: number, signal?: AbortSignal) {
  return new Promise<void>((resolve, reject) => {
    if (signal?.aborted) {
      reject(new DOMException("The operation was aborted.", "AbortError"));
      return;
    }
    const cleanup = () => signal?.removeEventListener("abort", onAbort);
    const timer = globalThis.setTimeout(() => {
      cleanup();
      resolve();
    }, ms);
    const onAbort = () => {
      globalThis.clearTimeout(timer);
      cleanup();
      reject(new DOMException("The operation was aborted.", "AbortError"));
    };
    signal?.addEventListener("abort", onAbort, { once: true });
  });
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

export function validateMicrosoftResourcesBatch(resourceIds: number[]) {
  return validateResourcesBatch({
    selection: idsSelection(resourceIds),
  });
}

export function validateMicrosoftResourcesByFilter(filter: ResourceBulkFilter) {
  return validateResourcesBatch({
    selection: filterSelection(filter),
  });
}

export function validateDomainResourcesBatch(resourceIds: number[]) {
  return validateResourcesBatch({
    selection: idsSelection(resourceIds),
  });
}

export function validateDomainResourcesByFilter(filter: ResourceBulkFilter) {
  return validateResourcesBatch({
    selection: filterSelection(filter),
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
    await client.GET("/v1/suppliers/applications/current")
  );
}

export async function submitSupplierApplication(
  payload: SupplierApplicationRequest
) {
  return unwrap<SupplierApplicationSubmitResponse>(
    await client.POST("/v1/suppliers/applications", {
      body: payload,
      params: { header: csrfHeader() },
    })
  );
}
