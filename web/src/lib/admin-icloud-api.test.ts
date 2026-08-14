// @vitest-environment jsdom

import { beforeEach, describe, expect, it, vi } from "vitest";

const apiMocks = vi.hoisted(() => ({
  DELETE: vi.fn(),
  GET: vi.fn(),
  PATCH: vi.fn(),
  POST: vi.fn(),
}));
const idempotencyMock = vi.hoisted(() => vi.fn());

vi.mock("./api-client", () => ({
  apiClient: apiMocks,
  csrfHeader: () => ({ "X-CSRF-Token": "admin-csrf" }),
  unwrap: async (result: { data?: unknown }) => result.data,
}));

vi.mock("./idempotency", () => ({
  generateIdempotencyKey: idempotencyMock,
}));

import {
  batchAdminICloudResourcesByFilter,
  batchAdminICloudResourcesByIds,
  createAdminICloudAliases,
  deleteAdminICloudResource,
  getAdminICloudResourceDetail,
  importAdminICloudResources,
  listAdminICloudAliases,
  listAdminICloudResources,
  listAdminICloudTasks,
  setAdminICloudResourcesExpirationByFilter,
  setAdminICloudResourcesExpirationByIds,
  updateAdminICloudResource,
  validateAdminICloudResource,
  waitForAdminICloudResourceImport,
  type AdminICloudImportResponse,
  type AdminICloudResourceList,
} from "./admin-icloud-api";

const EMPTY_LIST = {
  items: [],
  total: 0,
  offset: 0,
  limit: 20,
  aliasLimit: 750,
  facets: {
    status: {
      all: 0,
      pending: 0,
      validating: 0,
      normal: 0,
      abnormal: 0,
      disabled: 0,
      deleted: 0,
    },
    forSale: { all: 0, yes: 0, no: 0 },
  },
} satisfies AdminICloudResourceList;

const IMPORT_RESPONSE = {
  importId: 17,
  taskId: "icloud_import:17",
  requestId: "request-import-17",
  status: "imported",
  accepted: 1,
  imported: 1,
  skipped: 0,
  lastSafeError: null,
  reused: false,
  task: {
    taskId: "icloud_import:17",
    bizType: "icloud_resource_import",
    bizId: 17,
    kind: "import",
    status: "succeeded",
    credentialRevision: null,
    attempts: 1,
    maxAttempts: 3,
    remainingAttempts: 2,
    queuedAt: "2026-08-07T08:00:00Z",
    startedAt: "2026-08-07T08:00:01Z",
    finishedAt: "2026-08-07T08:00:02Z",
    updatedAt: "2026-08-07T08:00:02Z",
    progress: null,
  },
  createdAt: "2026-08-07T08:00:00Z",
  updatedAt: "2026-08-07T08:00:02Z",
} satisfies AdminICloudImportResponse;

describe("admin iCloud API adapter", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    idempotencyMock.mockReturnValue("icloud-command-1");
  });

  it("uses the shared OpenAPI client for list and expiration-aware import", async () => {
    apiMocks.GET.mockResolvedValueOnce({ data: EMPTY_LIST });
    apiMocks.POST.mockResolvedValueOnce({ data: IMPORT_RESPONSE });

    await listAdminICloudResources(
      {
        search: " owner ",
        status: "normal",
        forSale: false,
      },
      0,
      20,
      { includeFacets: false, includeTotal: false },
    );
    expect(apiMocks.GET).toHaveBeenCalledWith(
      "/v1/admin/icloud/resources",
      expect.objectContaining({
        params: {
          query: expect.objectContaining({
            search: "owner",
            status: "normal",
            forSale: false,
            includeFacets: false,
            includeTotal: false,
          }),
        },
      }),
    );

    const content =
      "primary@icloud.com----app-password----curl --url 'https://p217-maildomainws.icloud.com.cn/v2/hme/list?dsid=123' -H 'scnt: scnt-value' -b 'X-APPLE-WEBAUTH-TOKEN=secret'";
    await expect(
      importAdminICloudResources({
        content,
        ownerId: 101,
        errorStrategy: "skip",
        expireAt: "2026-10-07T08:00:00Z",
      }),
    ).resolves.toEqual(IMPORT_RESPONSE);

    const [, request] = apiMocks.POST.mock.calls[0] as [string, Record<string, any>];
    const formData = request.body as FormData;
    expect(apiMocks.POST.mock.calls[0]?.[0]).toBe(
      "/v1/admin/icloud/resources/imports",
    );
    expect(formData.get("ownerId")).toBe("101");
    expect(formData.get("errorStrategy")).toBe("skip");
    expect(formData.get("expireAt")).toBe("2026-10-07T08:00:00Z");
    expect(formData.has("longLived")).toBe(false);
    expect((formData.get("file") as File).name).toBe("icloud-resources.txt");
    expect(await (formData.get("file") as File).text()).toBe(content);
    expect(request.params.header).toEqual({
      "X-CSRF-Token": "admin-csrf",
      "Idempotency-Key": "icloud-command-1",
    });
  });

  it("uses typed alias, lifecycle, and ids/filter batch endpoints", async () => {
    apiMocks.GET.mockResolvedValueOnce({
      data: { items: [], total: 0, offset: 0, limit: 20 },
    });
    apiMocks.GET.mockResolvedValueOnce({ data: { id: 7, aliasLimit: 750 } });
    apiMocks.GET.mockResolvedValueOnce({
      data: { items: [], total: 0, succeeded: 0, offset: 0, limit: 20 },
    });
    apiMocks.POST.mockResolvedValue({
      data: {
        requested: 2,
        affected: 2,
        skipped: 0,
        affectedResourceIds: [7, 8],
        skippedResourceIds: [],
        reasonCounts: [],
      },
    });
    apiMocks.DELETE.mockResolvedValueOnce({
      data: { resourceId: 7, version: 5, status: "deleted", forSale: false },
    });

    await listAdminICloudAliases(7, 20, 10);
    await getAdminICloudResourceDetail(7);
    await listAdminICloudTasks(7, 20, 10);
    await validateAdminICloudResource(7, 4);
    await createAdminICloudAliases(7, 4);
    await batchAdminICloudResourcesByIds("alias", [7, 8]);
    await batchAdminICloudResourcesByIds("disable", [8, 7, 7, 0]);
    await batchAdminICloudResourcesByFilter("publish", {
      status: "normal",
    });
    await setAdminICloudResourcesExpirationByIds(
      [8, 7, 7, 0],
      "2026-10-07T08:00:00Z",
    );
    await setAdminICloudResourcesExpirationByFilter(
      { status: "normal" },
      "2026-11-07T08:00:00Z",
    );
    await deleteAdminICloudResource(7, 4);

    expect(apiMocks.GET).toHaveBeenCalledWith(
      "/v1/admin/icloud/resources/{resourceId}/aliases",
      expect.objectContaining({
        params: { path: { resourceId: 7 }, query: { offset: 20, limit: 10 } },
      }),
    );
    expect(apiMocks.GET).toHaveBeenCalledWith(
      "/v1/admin/icloud/resources/{resourceId}",
      expect.objectContaining({ params: { path: { resourceId: 7 } } }),
    );
    expect(apiMocks.GET).toHaveBeenCalledWith(
      "/v1/admin/tasks",
      expect.objectContaining({
        params: {
          query: {
            bizType: "icloud_resource",
            bizId: 7,
            offset: 20,
            limit: 10,
          },
        },
      }),
    );
    expect(apiMocks.POST).toHaveBeenNthCalledWith(
      1,
      "/v1/admin/icloud/resources/{resourceId}/validation",
      expect.objectContaining({
        params: {
          header: {
            "X-CSRF-Token": "admin-csrf",
            "Idempotency-Key": "icloud-command-1",
          },
          path: { resourceId: 7 },
          query: { version: 4 },
        },
      }),
    );
    expect(apiMocks.POST).toHaveBeenNthCalledWith(
      2,
      "/v1/admin/icloud/resources/{resourceId}/aliases",
      expect.objectContaining({
        params: expect.objectContaining({
          path: { resourceId: 7 },
          query: { version: 4 },
        }),
      }),
    );
    expect(apiMocks.POST).toHaveBeenNthCalledWith(
      3,
      "/v1/admin/icloud/resources/batch/alias",
      expect.objectContaining({
        body: { selection: { mode: "ids", resourceIds: [7, 8] } },
      }),
    );
    expect(apiMocks.POST).toHaveBeenNthCalledWith(
      4,
      "/v1/admin/icloud/resources/batch/disable",
      expect.objectContaining({
        body: { selection: { mode: "ids", resourceIds: [8, 7] } },
        params: {
          header: {
            "X-CSRF-Token": "admin-csrf",
            "Idempotency-Key": "icloud-command-1",
          },
        },
      }),
    );
    expect(apiMocks.POST).toHaveBeenNthCalledWith(
      5,
      "/v1/admin/icloud/resources/batch/publish",
      expect.objectContaining({
        body: {
          selection: {
            mode: "filter",
            filter: expect.objectContaining({
              status: "normal",
            }),
          },
        },
        params: {
          header: {
            "X-CSRF-Token": "admin-csrf",
            "Idempotency-Key": "icloud-command-1",
          },
        },
      }),
    );
    expect(apiMocks.POST).toHaveBeenNthCalledWith(
      6,
      "/v1/admin/icloud/resources/batch/expiration",
      expect.objectContaining({
        body: {
          selection: { mode: "ids", resourceIds: [8, 7] },
          expireAt: "2026-10-07T08:00:00Z",
        },
      }),
    );
    expect(apiMocks.POST).toHaveBeenNthCalledWith(
      7,
      "/v1/admin/icloud/resources/batch/expiration",
      expect.objectContaining({
        body: {
          selection: {
            mode: "filter",
            filter: expect.objectContaining({
              status: "normal",
            }),
          },
          expireAt: "2026-11-07T08:00:00Z",
        },
      }),
    );
    expect(apiMocks.DELETE).toHaveBeenCalledWith(
      "/v1/admin/icloud/resources/{resourceId}",
      expect.objectContaining({
        params: expect.objectContaining({
          header: {
            "X-CSRF-Token": "admin-csrf",
            "Idempotency-Key": "icloud-command-1",
          },
          path: { resourceId: 7 },
          query: { version: 4 },
        }),
      }),
    );
  });

  it("patches safe fields and a complete write-only credential line with command headers", async () => {
    apiMocks.PATCH.mockResolvedValueOnce({
      data: { resourceId: 7, version: 5, status: "pending", forSale: false },
    });

    await updateAdminICloudResource(7, {
      version: 4,
      ownerId: 101,
      expireAt: "2026-10-07T08:00:00Z",
      importLine:
        "primary@icloud.com----app-password----curl --url 'https://p217-maildomainws.icloud.com.cn/v2/hme/list?dsid=123' -H 'scnt: scnt-value' -b 'X-APPLE-WEBAUTH-TOKEN=secret'",
    });

    expect(apiMocks.PATCH).toHaveBeenCalledWith(
      "/v1/admin/icloud/resources/{resourceId}",
      {
        body: expect.objectContaining({
          version: 4,
          ownerId: 101,
          expireAt: "2026-10-07T08:00:00Z",
          importLine: expect.stringContaining("primary@icloud.com----app-password----curl"),
        }),
        params: {
          header: {
            "X-CSRF-Token": "admin-csrf",
            "Idempotency-Key": "icloud-command-1",
          },
          path: { resourceId: 7 },
        },
        signal: undefined,
      },
    );
  });

  it("returns the last processing state when polling reaches its budget", async () => {
    const processing = {
      ...IMPORT_RESPONSE,
      status: "processing",
      task: { ...IMPORT_RESPONSE.task, status: "running" },
    } satisfies AdminICloudImportResponse;
    apiMocks.GET.mockResolvedValueOnce({ data: processing });

    await expect(
      waitForAdminICloudResourceImport(17, { maxAttempts: 1 }),
    ).resolves.toEqual(processing);
  });
});
