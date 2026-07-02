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

interface ListOwnedMicrosoftResourcesOptions {
  onPage?: (items: ResourceItem[], response: ResourceListResponse) => void;
}

export async function listOwnedMicrosoftResources(
  options: ListOwnedMicrosoftResourcesOptions = {}
) {
  const items: ResourceItem[] = [];
  let offset = 0;
  let total = 0;
  let latest: ResourceListResponse | null = null;

  for (;;) {
    const response = await listOwnedMicrosoftResourcesPage(offset);

    latest = response;
    total = response.total;
    items.push(...response.items);
    options.onPage?.(response.items, response);

    if (response.items.length === 0 || items.length >= total) {
      break;
    }

    offset += response.items.length;
  }

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
