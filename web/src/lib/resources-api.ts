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
export type PublishResourcesRequest =
  components["schemas"]["PublishResourcesRequest"];
export type PublishResourcesResponse =
  components["schemas"]["PublishResourcesResponse"];
export type MicrosoftResourceDetail =
  components["schemas"]["MicrosoftResourceDetail"];
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

interface ListOwnedMicrosoftResourcesOptions {
  concurrency?: number;
  onPage?: (items: ResourceItem[], response: ResourceListResponse) => void;
}

function normalizePageConcurrency(value: number | undefined) {
  if (!Number.isFinite(value)) return resourcePageConcurrency;
  return Math.max(1, Math.min(8, Math.floor(value as number)));
}

export async function listOwnedMicrosoftResources(
  options: ListOwnedMicrosoftResourcesOptions = {}
) {
  const items: ResourceItem[] = [];
  let total = 0;
  let latest: ResourceListResponse | null = null;

  const firstResponse = await listOwnedMicrosoftResourcesPage(0);
  latest = firstResponse;
  total = firstResponse.total;
  items.push(...firstResponse.items);
  options.onPage?.(firstResponse.items, firstResponse);

  if (firstResponse.items.length === 0 || items.length >= total) {
    return {
      ...firstResponse,
      items,
      limit: items.length,
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
      items.push(...response.items);
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
          response = await listOwnedMicrosoftResourcesPage(pageOffset);
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
    limit: items.length,
    offset: 0,
    total,
  };
}

async function listOwnedMicrosoftResourcesPage(offset: number) {
  return unwrap<ResourceListResponse>(
    await client.GET("/v1/resources", {
      params: {
        query: {
          scope: "owned",
          type: "microsoft",
          offset,
          limit: resourcePageLimit,
        },
      },
    })
  );
}

export async function importMicrosoftResources(file: File, longLived: boolean) {
  const formData = new FormData();
  formData.append("file", file);
  formData.append("longLived", String(longLived));

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

export async function publishMicrosoftResourcesBatch(
  payload: PublishResourcesRequest
) {
  return unwrap<PublishResourcesResponse>(
    await client.POST("/v1/resources/publish", {
      body: payload,
      params: { header: csrfHeader() },
    })
  );
}

export async function publishMicrosoftResource(resourceId: number) {
  return unwrap<MicrosoftResourceDetail>(
    await client.POST("/v1/resources/{resourceId}/publish", {
      params: {
        header: csrfHeader(),
        path: { resourceId },
      },
    })
  );
}

export async function deleteMicrosoftResource(resourceId: number) {
  await unwrap<void>(
    await client.DELETE("/v1/resources/{resourceId}", {
      params: {
        header: csrfHeader(),
        path: { resourceId },
      },
    })
  );
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
