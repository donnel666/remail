// @vitest-environment jsdom

import { beforeEach, describe, expect, it, vi } from "vitest";

const apiMocks = vi.hoisted(() => ({
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
  importAdminGmailResources,
  listAdminGmailOwners,
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
      query?: Record<string, string | number | undefined>;
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
          "mail@gmail.com----password----JBSWY3DPEHPK3PXP----abcdefghijklmnop",
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
    expect(await file.text()).toContain("mail@gmail.com----password");
    expect(callOptions(apiMocks.POST).params?.header).toEqual({
      "X-CSRF-Token": "admin-csrf",
      "Idempotency-Key": "gmail-command-1",
    });
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
});
