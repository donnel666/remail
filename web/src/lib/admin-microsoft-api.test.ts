// @vitest-environment jsdom

import { beforeEach, describe, expect, it, vi } from "vitest";

const apiMocks = vi.hoisted(() => ({
  GET: vi.fn(),
  POST: vi.fn(),
  PUT: vi.fn(),
  PATCH: vi.fn(),
  DELETE: vi.fn(),
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
  createAdminMicrosoftExplicitAlias,
  deleteAdminMicrosoftResource,
  deleteAdminMicrosoftResourcesByFilter,
  disableAdminMicrosoftResource,
  disableAdminMicrosoftResourcesByIds,
  enableAdminMicrosoftResource,
  fetchAdminMicrosoftMail,
  getAdminMicrosoftBindingMessage,
  getAdminMicrosoftMessage,
  importAdminMicrosoftResources,
  listAdminMicrosoftAliases,
  listAdminMicrosoftAllocations,
  listAdminMicrosoftBindingMessages,
  listAdminMicrosoftMessages,
  listAdminMicrosoftOwners,
  listAdminMicrosoftResources,
  listAdminMicrosoftTasks,
  publishAdminMicrosoftResource,
  recoverAdminMicrosoftResource,
  refreshAdminMicrosoftToken,
  replaceAdminMicrosoftCredentials,
  setAdminMicrosoftResourcesForSaleByFilter,
  setAdminMicrosoftResourcesForSaleByIds,
  unpublishAdminMicrosoftResource,
  updateAdminMicrosoftResource,
  validateAdminMicrosoftResource,
  validateAdminMicrosoftResourcesByIds,
  waitForAdminMicrosoftResourceImport,
} from "./admin-microsoft-api";
import type {
  AdminMicrosoftFacets,
  AdminMicrosoftImportResponse,
  AdminMicrosoftListResponse,
  AdminMicrosoftTaskAcceptedResponse,
} from "@/pages/admin-microsoft/admin-microsoft-types";

const EMPTY_FACETS: AdminMicrosoftFacets = {
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
  longLived: { all: 0, yes: 0, no: 0 },
  graphAvailable: { all: 0, yes: 0, no: 0 },
  tokenHealth: { all: 0, valid: 0, expiring: 0, expired: 0, missing: 0 },
  suffixes: [],
};

const EMPTY_LIST: AdminMicrosoftListResponse = {
  items: [],
  total: 0,
  offset: 0,
  limit: 20,
  nextAfterId: null,
  facets: EMPTY_FACETS,
};

const SYNC_BULK = {
  requested: 2,
  affected: 2,
  skipped: 0,
  reasonCounts: [],
};

const ACCEPTED_TASK_RESPONSE = {
  taskId: "token:42",
  requestId: "request-accepted-42",
  status: "queued",
  accepted: 1,
  reused: false,
  task: {
	 taskId: "token:42",
    bizType: "microsoft_resource",
    bizId: 42,
	 kind: "token",
    status: "queued",
    attempts: 0,
    maxAttempts: 3,
    remainingAttempts: 3,
    credentialRevision: 7,
    queuedAt: "2026-07-12T08:00:00Z",
    startedAt: null,
    finishedAt: null,
    updatedAt: "2026-07-12T08:00:00Z",
    progress: null,
  },
} satisfies AdminMicrosoftTaskAcceptedResponse;

const IMPORT_RESPONSE = {
  importId: 17,
  taskId: "import:17",
  requestId: "request-import-17",
  status: "imported",
  accepted: 1,
  imported: 1,
  skipped: 0,
  lastSafeError: null,
  reused: false,
  task: {
    taskId: "import:17",
    bizType: "microsoft_resource",
    bizId: 17,
    kind: "import",
    status: "succeeded",
    attempts: 1,
    maxAttempts: 3,
    remainingAttempts: 2,
    credentialRevision: null,
    queuedAt: "2026-07-12T08:00:00Z",
    startedAt: "2026-07-12T08:00:01Z",
    finishedAt: "2026-07-12T08:00:02Z",
    updatedAt: "2026-07-12T08:00:02Z",
    progress: null,
  },
  createdAt: "2026-07-12T08:00:00Z",
  updatedAt: "2026-07-12T08:00:02Z",
} satisfies AdminMicrosoftImportResponse;

function callOptions(mock: ReturnType<typeof vi.fn>, index = 0) {
  return mock.mock.calls[index]?.[1] as {
    body?: unknown;
    bodySerializer?: (body: unknown) => BodyInit;
    signal?: AbortSignal;
    params?: {
      header?: Record<string, string>;
      path?: Record<string, string | number>;
      query?: Record<string, string | number | boolean | undefined>;
    };
  };
}

function expectCommandHeader(mock: ReturnType<typeof vi.fn>, index = 0) {
  expect(callOptions(mock, index).params?.header).toEqual({
    "X-CSRF-Token": "admin-csrf",
    "Idempotency-Key": expect.stringMatching(/^admin-command-/),
  });
}

describe("admin Microsoft API adapter", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    let sequence = 0;
    idempotencyMock.mockImplementation(() => `admin-command-${++sequence}`);
  });

  it("serializes only the formal list filters and caps blocks at 100", async () => {
    apiMocks.GET.mockResolvedValueOnce({ data: EMPTY_LIST });

    await listAdminMicrosoftResources(
      {
        search: " owner@example.com ",
        status: "all",
        forSale: false,
        longLived: true,
        graphAvailable: false,
        tokenHealth: "all",
        suffix: "@outlook.com",
        createdFrom: "2026-07-01T00:00:00.000Z",
        createdTo: "2026-07-11T23:59:59.999Z",
      },
      1_000,
      1_000,
      7_000
    );

    expect(apiMocks.GET).toHaveBeenCalledWith("/v1/admin/resources", {
      params: {
        query: {
          type: "microsoft",
          search: "owner@example.com",
          status: undefined,
          forSale: false,
          longLived: true,
          graphAvailable: false,
          tokenHealth: undefined,
          suffix: "outlook.com",
          createdFrom: "2026-07-01T00:00:00.000Z",
          createdTo: "2026-07-11T23:59:59.999Z",
          offset: 1_000,
          limit: 100,
          afterId: 7_000,
        },
      },
      signal: undefined,
    });
  });

  it("uses one bounded IAM owner search instead of unbounded pagination", async () => {
    apiMocks.GET.mockResolvedValueOnce({
      data: {
        users: [
          {
            id: 9,
            email: "owner@example.com",
            nickname: "Owner",
            role: "supplier",
            enabled: true,
            userGroup: { name: "Supply" },
          },
        ],
        total: 500,
        offset: 0,
        limit: 100,
      },
    });

    await expect(listAdminMicrosoftOwners("  example  ")).resolves.toEqual([
      {
        id: 9,
        email: "owner@example.com",
        nickname: "Owner",
        groupName: "Supply",
        role: "supplier",
        enabled: true,
      },
    ]);
    expect(apiMocks.GET).toHaveBeenCalledTimes(1);
    expect(apiMocks.GET).toHaveBeenCalledWith("/v1/admin/users", {
      params: { query: { search: "example", offset: 0, limit: 100 } },
      signal: undefined,
    });
  });

  it("loads allocations from the Alloc owner endpoint with resourceId", async () => {
    const controller = new AbortController();
    apiMocks.GET.mockResolvedValueOnce({
      data: { items: [], total: 42, offset: 20, limit: 20 },
    });

    await listAdminMicrosoftAllocations(88, 20, 20, controller.signal);

    expect(apiMocks.GET).toHaveBeenCalledWith("/v1/admin/allocations", {
      params: {
        query: {
          type: "microsoft",
          resourceId: 88,
          offset: 20,
          limit: 20,
        },
      },
      signal: controller.signal,
    });
  });

  it("sends base fields and optional credentials in one versioned PATCH", async () => {
    apiMocks.PATCH.mockResolvedValueOnce({ data: { resource: { id: 88 } } });
    const patch = {
      version: 7,
      emailAddress: "new@outlook.com",
      bindingAddress: "binding@example.com",
      ownerId: 9,
      forSale: true,
      longLived: false,
      qualityScore: 77,
      credentials: {
        password: "write-only-password",
        clientId: "client-id",
        refreshToken: "refresh-token",
      },
    };

    await updateAdminMicrosoftResource(88, patch);

    expect(apiMocks.PATCH).toHaveBeenCalledWith(
      "/v1/admin/resources/{resourceId}",
      {
        body: patch,
        params: {
          header: {
            "X-CSRF-Token": "admin-csrf",
            "Idempotency-Key": "admin-command-1",
          },
          path: { resourceId: 88 },
        },
      }
    );
    expect(apiMocks.PUT).not.toHaveBeenCalled();
  });

  it("uploads import content as multipart with CSRF and idempotency", async () => {
    apiMocks.POST.mockResolvedValueOnce({ data: IMPORT_RESPONSE });

    await expect(importAdminMicrosoftResources({
      content: "mail@outlook.com----password",
      ownerId: 101,
      longLived: true,
      errorStrategy: "skip",
    })).resolves.toMatchObject({
      taskId: "import:17",
      requestId: "request-import-17",
      reused: false,
    });

    const options = callOptions(apiMocks.POST);
    const formData = options.body as FormData;
    const file = formData.get("file") as File;
    expect(apiMocks.POST.mock.calls[0]?.[0]).toBe("/v1/admin/resources/imports");
    expect(formData.get("ownerId")).toBe("101");
    expect(formData.get("longLived")).toBe("true");
    expect(formData.get("errorStrategy")).toBe("skip");
    expect(file.name).toBe("microsoft-resources.txt");
    expect(await file.text()).toBe("mail@outlook.com----password");
    expectCommandHeader(apiMocks.POST);
  });

  it("aborts durable import polling without issuing a stale request", async () => {
    vi.useFakeTimers();
    try {
      const controller = new AbortController();
      apiMocks.GET.mockResolvedValueOnce({
        data: {
          importId: 17,
          status: "processing",
          accepted: 1,
          imported: 0,
          skipped: 0,
          task: {},
        },
      });

      const result = waitForAdminMicrosoftResourceImport(17, {
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

  it("rejects an import that is still processing after the polling limit", async () => {
    vi.useFakeTimers();
    try {
      const processing = {
        ...IMPORT_RESPONSE,
        imported: 0,
        status: "processing" as const,
        task: { ...IMPORT_RESPONSE.task, finishedAt: null, status: "running" as const },
      };
      apiMocks.GET.mockResolvedValueOnce({ data: processing });

      const result = expect(
        waitForAdminMicrosoftResourceImport(17, {
          intervalMs: 1_000,
          maxAttempts: 1,
        })
      ).rejects.toThrow("The Microsoft resource import is still processing.");
      await vi.runAllTimersAsync();

      await result;
      expect(apiMocks.GET).toHaveBeenCalledTimes(1);
    } finally {
      vi.useRealTimers();
    }
  });

  it("preserves the POST import reuse metadata after status polling", async () => {
    apiMocks.POST.mockResolvedValueOnce({
      data: {
        ...IMPORT_RESPONSE,
        status: "processing",
        imported: 0,
        reused: true,
        task: { ...IMPORT_RESPONSE.task, status: "queued", finishedAt: null },
      } satisfies AdminMicrosoftImportResponse,
    });
    apiMocks.GET.mockResolvedValueOnce({
      data: { ...IMPORT_RESPONSE, reused: false } satisfies AdminMicrosoftImportResponse,
    });

    await expect(importAdminMicrosoftResources({
      content: "mail@outlook.com----password",
      ownerId: 101,
      longLived: true,
      errorStrategy: "skip",
    })).resolves.toMatchObject({
      status: "imported",
      taskId: "import:17",
      requestId: "request-import-17",
      reused: true,
    });
  });

  it("submits ids/filter selection once and uses formal type=microsoft", async () => {
    apiMocks.POST
      .mockResolvedValueOnce({ data: { task: {}, reused: false } })
      .mockResolvedValueOnce({ data: SYNC_BULK })
      .mockResolvedValueOnce({
        data: { task: { status: "succeeded", taskId: "bulk-delete-1" }, reused: false },
      });

    await validateAdminMicrosoftResourcesByIds([2, 2, -1, 0, 3, 3.5]);
    await disableAdminMicrosoftResourcesByIds([4, 4, 5]);
    await deleteAdminMicrosoftResourcesByFilter({
      status: "normal",
      forSale: false,
      tokenHealth: "all",
      suffix: "@outlook.com",
    });

    expect(apiMocks.POST.mock.calls.map((call) => call[0])).toEqual([
      "/v1/admin/resources/validations",
      "/v1/admin/resources/disable",
      "/v1/admin/resources/delete",
    ]);
    expect(callOptions(apiMocks.POST, 0).body).toEqual({
      selection: { mode: "ids", resourceIds: [2, 3] },
    });
    expect(callOptions(apiMocks.POST, 2).body).toEqual({
      selection: {
        mode: "filter",
        filter: {
          type: "microsoft",
          search: undefined,
          status: "normal",
          forSale: false,
          longLived: undefined,
          graphAvailable: undefined,
          tokenHealth: undefined,
          suffix: "outlook.com",
          createdFrom: undefined,
          createdTo: undefined,
        },
      },
    });
    for (let index = 0; index < 3; index += 1) expectCommandHeader(apiMocks.POST, index);
    expect(new Set(apiMocks.POST.mock.calls.map((_, index) =>
      callOptions(apiMocks.POST, index).params?.header?.["Idempotency-Key"]
    )).size).toBe(3);
  });

  it("keeps filter-mode commands pending until the durable task finishes", async () => {
    vi.useFakeTimers();
    try {
      apiMocks.POST.mockResolvedValueOnce({
        data: {
          task: { status: "queued", taskId: "bulk-delete-queued" },
          reused: false,
        },
      });
      apiMocks.GET
        .mockResolvedValueOnce({
          data: { status: "running", taskId: "bulk-delete-queued" },
        })
        .mockResolvedValueOnce({
          data: { status: "succeeded", taskId: "bulk-delete-queued" },
        });

      const result = deleteAdminMicrosoftResourcesByFilter({ status: "normal" });
      await vi.runAllTimersAsync();

      await expect(result).resolves.toMatchObject({
        task: { status: "succeeded", taskId: "bulk-delete-queued" },
      });
      expect(apiMocks.GET.mock.calls.map((call) => call[0])).toEqual([
        "/v1/admin/tasks/{taskId}",
        "/v1/admin/tasks/{taskId}",
      ]);
    } finally {
      vi.useRealTimers();
    }
  });

  it("keeps safe final progress when a filter-mode command fails", async () => {
    vi.useFakeTimers();
    try {
      apiMocks.POST.mockResolvedValueOnce({
        data: {
          task: { status: "queued", taskId: "bulk-delete-failed" },
          reused: false,
        },
      });
      apiMocks.GET.mockResolvedValueOnce({
        data: {
          status: "failed",
          taskId: "bulk-delete-failed",
          progress: {
            total: 4,
            processed: 4,
            succeeded: 2,
            skipped: 1,
            failed: 1,
            reasonCounts: [{ reason: "active_allocation", count: 1 }],
          },
        },
      });

      const result = deleteAdminMicrosoftResourcesByFilter({ status: "normal" });
      const rejection = expect(result).rejects.toThrow(
        "Processed 4/4; succeeded 2, skipped 1, failed 1. Reasons: active_allocation: 1."
      );
      await vi.runAllTimersAsync();
      await rejection;
    } finally {
      vi.useRealTimers();
    }
  });

  it("uses explicit versioned state commands instead of arbitrary status PATCH", async () => {
    apiMocks.POST
      .mockResolvedValueOnce({ data: { resource: { id: 55 } } })
      .mockResolvedValueOnce({ data: undefined })
      .mockResolvedValueOnce({ data: undefined })
      .mockResolvedValueOnce({ data: undefined })
      .mockResolvedValueOnce({ data: { resource: { id: 55 } } });
    apiMocks.DELETE.mockResolvedValueOnce({ data: undefined });

    await enableAdminMicrosoftResource(55, 7);
    await disableAdminMicrosoftResource(55, 8);
    await publishAdminMicrosoftResource(55, 9);
    await unpublishAdminMicrosoftResource(55, 10);
    await recoverAdminMicrosoftResource(55, 11);
    await deleteAdminMicrosoftResource(55, 12);

    expect(apiMocks.POST.mock.calls.map((call) => call[0])).toEqual([
      "/v1/admin/resources/{resourceId}/enable",
      "/v1/admin/resources/{resourceId}/disable",
      "/v1/admin/resources/{resourceId}/publish",
      "/v1/admin/resources/{resourceId}/unpublish",
      "/v1/admin/resources/{resourceId}/recover",
    ]);
    expect(apiMocks.POST.mock.calls.map((_, index) => callOptions(apiMocks.POST, index).params?.query)).toEqual([
      { version: 7 },
      { version: 8 },
      { version: 9 },
      { version: 10 },
      { version: 11 },
    ]);
    expect(callOptions(apiMocks.DELETE).params?.query).toEqual({ version: 12 });
    expect(apiMocks.PATCH).not.toHaveBeenCalled();
  });

  it("keeps validation ephemeral while the other manual tasks stay durable", async () => {
    apiMocks.POST
      .mockResolvedValueOnce({ data: { requested: 1, queued: 1 } })
      .mockResolvedValue({ data: ACCEPTED_TASK_RESPONSE });

    const [validation, ...durableTasks] = await Promise.all([
      validateAdminMicrosoftResource(55),
      refreshAdminMicrosoftToken(55),
      createAdminMicrosoftExplicitAlias(55),
      fetchAdminMicrosoftMail(55),
    ]);

    expect(validation).toEqual({ requested: 1, queued: 1 });
    for (const result of durableTasks) {
      expect(result).toMatchObject({
		taskId: "token:42",
        requestId: "request-accepted-42",
        status: "queued",
        accepted: 1,
        reused: false,
      });
      expect(result.task.taskId).toBe(result.taskId);
    }

    expect(apiMocks.POST.mock.calls.map((call) => call[0])).toEqual([
      "/v1/admin/resources/{resourceId}/validate",
      "/v1/admin/resources/{resourceId}/token/refresh",
      "/v1/admin/resources/{resourceId}/aliases",
      "/v1/admin/resources/{resourceId}/messages/fetch",
    ]);
    for (let index = 0; index < 4; index += 1) expectCommandHeader(apiMocks.POST, index);
  });

  it("routes each detail tab to its fact owner and forwards AbortSignal", async () => {
    const controller = new AbortController();
    apiMocks.GET.mockResolvedValue({
      data: { items: [], total: 0, offset: 0, limit: 20, hasMore: false },
    });

    await listAdminMicrosoftAliases(55, "explicit", 0, 20, controller.signal);
    await listAdminMicrosoftTasks(55, 0, 20, controller.signal);
    await listAdminMicrosoftMessages(55, "code", 20, undefined, controller.signal);
    await getAdminMicrosoftMessage(55, 9, controller.signal);
    await listAdminMicrosoftBindingMessages(55, "code", 20, undefined, controller.signal);
    await getAdminMicrosoftBindingMessage(55, 10, controller.signal);

    expect(apiMocks.GET.mock.calls.map((call) => call[0])).toEqual([
      "/v1/admin/resources/{resourceId}/aliases",
      "/v1/admin/tasks",
      "/v1/admin/messages",
      "/v1/admin/messages/{messageId}",
      "/v1/admin/bindings",
      "/v1/admin/bindings/messages/{messageId}",
    ]);
    for (let index = 0; index < 6; index += 1) {
      expect(callOptions(apiMocks.GET, index).signal).toBe(controller.signal);
    }
    expect(callOptions(apiMocks.GET, 1).params?.query).toMatchObject({
      bizType: "microsoft_resource",
      bizId: 55,
    });
    expect(callOptions(apiMocks.GET, 2).params?.query).toMatchObject({
      resourceId: 55,
      search: "code",
      type: "microsoft",
      offset: 0,
      includeTotal: true,
    });
    expect(callOptions(apiMocks.GET, 4).params?.query).toMatchObject({
      resourceId: 55,
      offset: 0,
      includeTotal: true,
    });
  });

  it("uses stable cursors and skips total on Microsoft mail continuations", async () => {
    apiMocks.GET.mockResolvedValue({
      data: { items: [], offset: 0, limit: 20, hasMore: false },
    });
    const cursor = {
      beforeReceivedAt: "2026-07-12T08:00:00Z",
      beforeId: 8,
    };

    await listAdminMicrosoftMessages(55, "code", 20, cursor);
    await listAdminMicrosoftBindingMessages(55, "code", 20, cursor);

    for (let index = 0; index < 2; index += 1) {
      expect(callOptions(apiMocks.GET, index).params?.query).toMatchObject({
        resourceId: 55,
        search: "code",
        offset: undefined,
        beforeReceivedAt: cursor.beforeReceivedAt,
        beforeId: cursor.beforeId,
        includeTotal: false,
        limit: 20,
      });
    }
  });

  it("versions independent credential replacement and gives every write a fresh key", async () => {
    apiMocks.PUT.mockResolvedValueOnce({ data: { resource: { id: 66 } } });
    apiMocks.POST
      .mockResolvedValueOnce({ data: SYNC_BULK })
      .mockResolvedValueOnce({
        data: { task: { status: "succeeded", taskId: "bulk-private-1" }, reused: false },
      });

    await replaceAdminMicrosoftCredentials(66, {
      version: 4,
      password: "password",
      clientId: "client-id",
      refreshToken: "refresh-token",
    });
    await setAdminMicrosoftResourcesForSaleByIds([7, 8], true);
    await setAdminMicrosoftResourcesForSaleByFilter({ status: "normal" }, false);

    expect(callOptions(apiMocks.PUT).body).toMatchObject({ version: 4 });
    expect(apiMocks.POST.mock.calls.map((call) => call[0])).toEqual([
      "/v1/admin/resources/publish",
      "/v1/admin/resources/unpublish",
    ]);
    expectCommandHeader(apiMocks.PUT);
    expectCommandHeader(apiMocks.POST, 0);
    expectCommandHeader(apiMocks.POST, 1);
  });
});
