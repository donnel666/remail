// @vitest-environment jsdom

import { beforeEach, describe, expect, it, vi } from "vitest";

const apiMocks = vi.hoisted(() => ({
  DELETE: vi.fn(),
  GET: vi.fn(),
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
  deleteAdminICloudResource,
  importAdminICloudResources,
  listAdminICloudAliases,
  listAdminICloudResources,
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
    sessionStatus: { all: 0, unchecked: 0, valid: 0, invalid: 0 },
    suffixes: [],
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

  it("uses the shared OpenAPI client for list and eight-field import", async () => {
    apiMocks.GET.mockResolvedValueOnce({ data: EMPTY_LIST });
    apiMocks.POST.mockResolvedValueOnce({ data: IMPORT_RESPONSE });

    await listAdminICloudResources(
      {
        search: " owner ",
        suffix: "@icloud.com",
        status: "normal",
        forSale: false,
        sessionStatus: "valid",
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
            suffix: "icloud.com",
            status: "normal",
            forSale: false,
            sessionStatus: "valid",
            includeFacets: false,
            includeTotal: false,
          }),
        },
      }),
    );

    const content =
      "primary@icloud.com----www.icloud.com----dsid----client----build----master----Cookie=value----target@gmail.com";
    await expect(
      importAdminICloudResources({
        content,
        ownerId: 101,
        errorStrategy: "skip",
      }),
    ).resolves.toEqual(IMPORT_RESPONSE);

    const [, request] = apiMocks.POST.mock.calls[0] as [string, Record<string, any>];
    const formData = request.body as FormData;
    expect(apiMocks.POST.mock.calls[0]?.[0]).toBe(
      "/v1/admin/icloud/resources/imports",
    );
    expect(formData.get("ownerId")).toBe("101");
    expect(formData.get("errorStrategy")).toBe("skip");
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
    await validateAdminICloudResource(7, 4);
    await batchAdminICloudResourcesByIds("disable", [8, 7, 7, 0]);
    await batchAdminICloudResourcesByFilter("publish", {
      status: "normal",
      suffix: "@icloud.com",
    });
    await deleteAdminICloudResource(7, 4);

    expect(apiMocks.GET).toHaveBeenCalledWith(
      "/v1/admin/icloud/resources/{resourceId}/aliases",
      expect.objectContaining({
        params: { path: { resourceId: 7 }, query: { offset: 20, limit: 10 } },
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
      3,
      "/v1/admin/icloud/resources/batch/publish",
      expect.objectContaining({
        body: {
          selection: {
            mode: "filter",
            filter: expect.objectContaining({
              status: "normal",
              suffix: "icloud.com",
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
