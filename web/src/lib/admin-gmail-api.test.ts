// @vitest-environment jsdom

import { beforeEach, describe, expect, it, vi } from "vitest";

const apiMocks = vi.hoisted(() => ({
  DELETE: vi.fn(),
  GET: vi.fn(),
  PATCH: vi.fn(),
  POST: vi.fn(),
  PUT: vi.fn(),
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
  batchAllMatchingAdminGmailResources,
  batchAdminGmailResourcesByFilter,
  batchAdminGmailResourcesByIds,
  deleteAdminGmailResource,
  getAdminGmailResource,
  importAdminGmailResources,
  listAdminGmailAliases,
  listAdminGmailOwners,
  listAdminGmailTasks,
  recoverAdminGmailResource,
  replaceAdminGmailCredentials,
  scanAdminGmailResourceHistory,
  updateAdminGmailResource,
  waitForAdminGmailResourceImport,
  type AdminGmailImportResponse,
} from "./admin-gmail-api";

const IMPORT_RESPONSE = {
  importId: 17,
  taskId: "gmail_import:17",
  requestId: "request-import-17",
  status: "imported",
  accepted: 1,
  imported: 1,
  skipped: 0,
  lastSafeError: null,
  reused: false,
  task: {
    taskId: "gmail_import:17",
    bizType: "gmail_resource_import",
    bizId: 17,
    kind: "import",
    status: "succeeded",
    attempts: 1,
    maxAttempts: 3,
    remainingAttempts: 2,
    credentialRevision: null,
    queuedAt: "2026-08-03T08:00:00Z",
    startedAt: "2026-08-03T08:00:01Z",
    finishedAt: "2026-08-03T08:00:02Z",
    updatedAt: "2026-08-03T08:00:02Z",
    progress: null,
  },
  createdAt: "2026-08-03T08:00:00Z",
  updatedAt: "2026-08-03T08:00:02Z",
} satisfies AdminGmailImportResponse;

function callOptions(mock: ReturnType<typeof vi.fn>, index = 0) {
  return mock.mock.calls[index]?.[1] as {
    body?: unknown;
    signal?: AbortSignal;
    params?: {
      header?: Record<string, string>;
      path?: Record<string, string | number>;
      query?: Record<string, string | number | boolean | undefined>;
    };
  };
}

describe("admin Gmail API adapter", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    idempotencyMock.mockReturnValue("gmail-command-1");
  });

  it("uploads multipart owner fields without the Microsoft longLived node", async () => {
    apiMocks.POST.mockResolvedValueOnce({ data: IMPORT_RESPONSE });

    await expect(
      importAdminGmailResources({
        content:
          "mail@gmail.com----password----JBSWY3DPEHPK3PXP----abcd efgh ijkl mnop",
        ownerId: 101,
        errorStrategy: "skip",
      }),
    ).resolves.toEqual(IMPORT_RESPONSE);

    const formData = callOptions(apiMocks.POST).body as FormData;
    const file = formData.get("file") as File;
    expect(apiMocks.POST.mock.calls[0]?.[0]).toBe(
      "/v1/admin/gmail/resources/imports",
    );
    expect(formData.get("ownerId")).toBe("101");
    expect(formData.get("errorStrategy")).toBe("skip");
    expect(formData.has("longLived")).toBe(false);
    expect(file.name).toBe("gmail-resources.txt");
    expect(await file.text()).toBe(
      "mail@gmail.com----password----JBSWY3DPEHPK3PXP----abcdefghijklmnop",
    );
    expect(callOptions(apiMocks.POST).params?.header).toEqual({
      "X-CSRF-Token": "admin-csrf",
      "Idempotency-Key": "gmail-command-1",
    });
  });

  it("normalizes App Password whitespace with the semicolon delimiter", async () => {
    apiMocks.POST.mockResolvedValueOnce({ data: IMPORT_RESPONSE });

    await expect(
      importAdminGmailResources({
        content: "mail@gmail.com;password;abcd efgh ijkl mnop",
        ownerId: 101,
        errorStrategy: "skip",
      }),
    ).resolves.toEqual(IMPORT_RESPONSE);

    const formData = callOptions(apiMocks.POST).body as FormData;
    const file = formData.get("file") as File;
    expect(await file.text()).toBe(
      "mail@gmail.com;password;abcdefghijklmnop",
    );
  });

  it("polls Redis-backed status and preserves POST reuse metadata", async () => {
    apiMocks.POST.mockResolvedValueOnce({
      data: {
        ...IMPORT_RESPONSE,
        status: "processing",
        imported: 0,
        reused: true,
        task: {
          ...IMPORT_RESPONSE.task,
          status: "queued",
          startedAt: null,
          finishedAt: null,
        },
      } satisfies AdminGmailImportResponse,
    });
    apiMocks.GET.mockResolvedValueOnce({ data: IMPORT_RESPONSE });

    await expect(
      importAdminGmailResources({
        content:
          "mail@gmail.com----password----JBSWY3DPEHPK3PXP----abcdefghijklmnop",
        ownerId: 101,
        errorStrategy: "abort",
      }),
    ).resolves.toMatchObject({
      status: "imported",
      taskId: "gmail_import:17",
      requestId: "request-import-17",
      reused: true,
    });
    expect(apiMocks.GET).toHaveBeenCalledWith(
      "/v1/admin/gmail/resources/imports/{importId}",
      expect.objectContaining({ params: { path: { importId: 17 } } }),
    );
  });

  it("aborts polling before issuing another status request", async () => {
    vi.useFakeTimers();
    try {
      const controller = new AbortController();
      apiMocks.GET.mockResolvedValueOnce({
        data: {
          ...IMPORT_RESPONSE,
          status: "processing",
          imported: 0,
          task: {
            ...IMPORT_RESPONSE.task,
            status: "running",
            finishedAt: null,
          },
        } satisfies AdminGmailImportResponse,
      });

      const result = waitForAdminGmailResourceImport(17, {
        intervalMs: 1_000,
        maxAttempts: 5,
        signal: controller.signal,
      });
      await vi.advanceTimersByTimeAsync(0);
      controller.abort();

      await expect(result).rejects.toMatchObject({ name: "AbortError" });
      await vi.runAllTimersAsync();
      expect(apiMocks.GET).toHaveBeenCalledTimes(1);
    } finally {
      vi.useRealTimers();
    }
  });

  it("loads the same bounded owner shape without importing Microsoft code", async () => {
    apiMocks.GET.mockResolvedValueOnce({
      data: {
        users: [
          {
            id: 7,
            email: "owner@example.com",
            nickname: "Owner",
            role: "supplier",
            userGroup: {
              id: 3,
              code: "supplier",
              name: "Suppliers",
              description: "",
              enabled: true,
              createdAt: "2026-08-03T08:00:00Z",
              updatedAt: "2026-08-03T08:00:00Z",
            },
            hasLocalPassword: true,
            enabled: true,
            createdAt: "2026-08-03T08:00:00Z",
            updatedAt: "2026-08-03T08:00:00Z",
          },
        ],
        total: 1,
        offset: 0,
        limit: 100,
      },
    });

    await expect(listAdminGmailOwners(" owner ")).resolves.toEqual([
      {
        id: 7,
        email: "owner@example.com",
        nickname: "Owner",
        role: "supplier",
        groupName: "Suppliers",
        enabled: true,
      },
    ]);
    expect(callOptions(apiMocks.GET).params?.query).toEqual({
      search: "owner",
      offset: 0,
      limit: 100,
    });
  });

  it("lists observed dot and plus aliases through the Gmail resource route", async () => {
    const response = {
      items: [
        {
          id: 12,
          kind: "plus",
          emailAddress: "mail+tag@gmail.com",
          createdAt: "2026-08-08T08:00:00Z",
        },
      ],
      total: 1,
      offset: 20,
      limit: 20,
    } as const;
    apiMocks.GET.mockResolvedValueOnce({ data: response });

    await expect(listAdminGmailAliases(7, 20, 20)).resolves.toEqual(
      response,
    );
    expect(apiMocks.GET).toHaveBeenCalledWith(
      "/v1/admin/gmail/resources/{resourceId}/aliases",
      expect.objectContaining({
        params: {
          path: { resourceId: 7 },
          query: { kind: "other", offset: 20, limit: 20 },
        },
      }),
    );
  });

  it("uses safe detail/edit and fenced row management routes", async () => {
    const resource = {
      id: 7,
      version: 3,
      ownerUserId: 9,
      owner: {
        id: 9,
        email: "owner@example.com",
        nickname: "Owner",
        groupName: "Suppliers",
        role: "supplier",
        enabled: true,
      },
      email: "mail@gmail.com",
      bindingEmail: "recovery@example.com",
      status: "normal",
      forSale: false,
      passwordConfigured: true,
      twoFactorConfigured: true,
      appPasswordConfigured: true,
      credentialRevision: 2,
      credentialUpdatedAt: "2026-08-08T08:00:00Z",
      validationFailures: 0,
      createdAt: "2026-08-08T08:00:00Z",
      updatedAt: "2026-08-08T08:00:00Z",
    } as const;
    const mutation = {
      resourceId: 7,
      version: 4,
      status: "pending",
      forSale: false,
    } as const;
    apiMocks.GET.mockResolvedValueOnce({ data: resource });
    apiMocks.PATCH.mockResolvedValueOnce({ data: mutation });
    apiMocks.PUT.mockResolvedValueOnce({ data: mutation });
    apiMocks.POST.mockResolvedValueOnce({ data: mutation });
    apiMocks.DELETE.mockResolvedValueOnce({ data: mutation });
    apiMocks.POST.mockResolvedValueOnce({ data: mutation });

    await expect(getAdminGmailResource(7)).resolves.toEqual(resource);
    await expect(
      updateAdminGmailResource(7, {
        version: 3,
        ownerId: 9,
        email: "mail@gmail.com",
        bindingEmail: "recovery@example.com",
        appPassword: "abcd efgh ijkl mnop",
      }),
    ).resolves.toEqual(mutation);
    await expect(
      replaceAdminGmailCredentials(7, {
        version: 3,
        password: "write-only-password",
        twoFactorSecret: "JBSWY3DPEHPK3PXP",
        appPassword: "abcd efgh ijkl mnop",
      }),
    ).resolves.toEqual(mutation);
    await expect(scanAdminGmailResourceHistory(7)).resolves.toEqual(mutation);
    await expect(deleteAdminGmailResource(7, 4)).resolves.toEqual(mutation);
    await expect(recoverAdminGmailResource(7, 4)).resolves.toEqual(mutation);

    expect(apiMocks.PATCH).toHaveBeenCalledWith(
      "/v1/admin/gmail/resources/{resourceId}",
      expect.objectContaining({
        body: {
          version: 3,
          ownerId: 9,
          email: "mail@gmail.com",
          bindingEmail: "recovery@example.com",
          appPassword: "abcdefghijklmnop",
        },
        params: expect.objectContaining({ path: { resourceId: 7 } }),
      }),
    );
    expect(apiMocks.PUT).toHaveBeenCalledWith(
      "/v1/admin/gmail/resources/{resourceId}/credentials",
      expect.objectContaining({
        body: {
          version: 3,
          password: "write-only-password",
          twoFactorSecret: "JBSWY3DPEHPK3PXP",
          appPassword: "abcdefghijklmnop",
        },
        params: expect.objectContaining({ path: { resourceId: 7 } }),
      }),
    );
    expect(callOptions(apiMocks.DELETE).params?.query).toEqual({ version: 4 });
  });

  it("loads Gmail resource tasks through the governance owner", async () => {
    apiMocks.GET.mockResolvedValueOnce({
      data: { items: [], total: 0, succeeded: 0, offset: 20, limit: 20 },
    });

    await expect(listAdminGmailTasks(7, 20, 20)).resolves.toEqual({
      items: [],
      total: 0,
      succeeded: 0,
      offset: 20,
      limit: 20,
    });
    expect(apiMocks.GET).toHaveBeenCalledWith("/v1/admin/tasks", {
      params: {
        query: {
          bizType: "gmail_resource",
          bizId: 7,
          offset: 20,
          limit: 20,
        },
      },
      signal: undefined,
    });
  });

  it("snapshots every matching ID and submits bounded command chunks", async () => {
    const page = (start: number, end: number) => ({
      data: {
        items: Array.from({ length: end - start + 1 }, (_, index) => ({
          id: start + index,
        })),
        total: 1001,
      },
    });
    apiMocks.GET
      .mockResolvedValueOnce(page(1, 200))
      .mockResolvedValueOnce(page(200, 399))
      .mockResolvedValueOnce(page(400, 599))
      .mockResolvedValueOnce(page(600, 799))
      .mockResolvedValueOnce(page(800, 999))
      .mockResolvedValueOnce(page(1000, 1001));
    apiMocks.POST
      .mockResolvedValueOnce({
        data: {
          requested: 1000,
          affected: 999,
          skipped: 1,
          affectedResourceIds: Array.from(
            { length: 999 },
            (_, index) => index + 1,
          ),
          skippedResourceIds: [1000],
          reasonCounts: [{ reason: "invalid_state", count: 1 }],
        },
      })
      .mockResolvedValueOnce({
        data: {
          requested: 1,
          affected: 0,
          skipped: 1,
          affectedResourceIds: [],
          skippedResourceIds: [1001],
          reasonCounts: [{ reason: "invalid_state", count: 1 }],
        },
      });

    await expect(
      batchAllMatchingAdminGmailResources("disable", {
        search: "owner",
        status: "normal",
      }),
    ).resolves.toMatchObject({
      requested: 1001,
      affected: 999,
      skipped: 2,
      skippedResourceIds: [1000, 1001],
      reasonCounts: [{ reason: "invalid_state", count: 2 }],
    });

    expect(
      apiMocks.GET.mock.calls.map(
        (call) => (call[1] as { params: { query: { offset: number } } }).params
          .query.offset,
      ),
    ).toEqual([0, 200, 400, 600, 800, 1000]);
    expect(callOptions(apiMocks.POST, 0).body).toEqual({
      selection: {
        mode: "ids",
        resourceIds: Array.from({ length: 1000 }, (_, index) => index + 1),
      },
    });
    expect(callOptions(apiMocks.POST, 1).body).toEqual({
      selection: { mode: "ids", resourceIds: [1001] },
    });
  });

  it("sends ids and current-filter selections to Gmail batch routes", async () => {
    const result = {
      requested: 2,
      affected: 2,
      skipped: 0,
      affectedResourceIds: [3, 5],
      skippedResourceIds: [],
      reasonCounts: [],
    };
    apiMocks.POST.mockResolvedValue({ data: result });

    await expect(
      batchAdminGmailResourcesByIds("delete", [5, 3, 5, 0]),
    ).resolves.toEqual(result);
    await expect(
      batchAdminGmailResourcesByFilter("history", {
        search: " owner ",
        status: "normal",
        forSale: false,
        createdFrom: "2026-08-01T00:00:00Z",
        createdTo: "2026-08-08T23:59:59Z",
      }),
    ).resolves.toEqual(result);

    expect(apiMocks.POST.mock.calls[0]?.[0]).toBe(
      "/v1/admin/gmail/resources/batch/delete",
    );
    expect(callOptions(apiMocks.POST, 0).body).toEqual({
      selection: { mode: "ids", resourceIds: [5, 3] },
    });
    expect(apiMocks.POST.mock.calls[1]?.[0]).toBe(
      "/v1/admin/gmail/resources/batch/history",
    );
    expect(callOptions(apiMocks.POST, 1).body).toEqual({
      selection: {
        mode: "filter",
        filter: {
          search: "owner",
          status: "normal",
          forSale: false,
          createdFrom: "2026-08-01T00:00:00Z",
          createdTo: "2026-08-08T23:59:59Z",
        },
      },
    });
  });
});
