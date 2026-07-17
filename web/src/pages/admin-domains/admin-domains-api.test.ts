// @vitest-environment jsdom

import { beforeEach, describe, expect, it, vi } from "vitest";

const apiMocks = vi.hoisted(() => ({
  GET: vi.fn(),
  POST: vi.fn(),
  PATCH: vi.fn(),
  DELETE: vi.fn(),
}));
const idempotencyMock = vi.hoisted(() => vi.fn());
type AdminDomainGetOptions = { params?: { query?: { offset?: number } } };

vi.mock("@/lib/api-client", () => ({
  apiClient: apiMocks,
  csrfHeader: () => ({ "X-CSRF-Token": "domain-csrf" }),
  unwrap: async (result: { data?: unknown }) => result.data,
}));

vi.mock("@/lib/idempotency", () => ({
  generateIdempotencyKey: idempotencyMock,
}));

import {
  getAdminDomainDetail,
  listAdminDomainOwners,
  listAdminDomainMessages,
  listAdminDomains,
  setAdminDomainsPurposeByFilter,
  updateAdminDomain,
} from "./admin-domains-api";

const DOMAIN = {
  id: 42,
  version: 7,
  ownerId: 9,
  ownerEmail: "owner@example.com",
  ownerNickname: "Owner",
  ownerRole: "supplier" as const,
  domain: "real.example.com",
  domainTld: "com",
  mailServerId: 3,
  purpose: "not_sale" as const,
  status: "disabled" as const,
  mailboxCount: 1,
  lastAllocatedAt: null,
  createdAt: "2026-07-01T00:00:00Z",
  updatedAt: "2026-07-15T00:00:00Z",
};

const FACETS = {
  status: { all: 1, normal: 0, abnormal: 0, disabled: 1, deleted: 0 },
  purpose: { all: 1, not_sale: 1, sale: 0, binding: 0 },
  tlds: [{ key: "com", count: 1 }],
};

describe("admin domain API adapter", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    idempotencyMock.mockReturnValue("domain-command-1");
  });

  it("serializes the complete server-side list filter and removes nullable timestamps", async () => {
    apiMocks.GET.mockResolvedValueOnce({
      data: {
        items: [DOMAIN],
        total: 1,
        offset: 0,
        limit: 100,
        facets: FACETS,
      },
    });

    const result = await listAdminDomains(
      {
        search: " owner@example.com ",
        status: "all",
        purpose: "sale",
        tld: " com ",
        ownerId: 9,
        mailServerId: 3,
        createdFrom: "2026-07-01T00:00:00Z",
        createdTo: "2026-07-16T00:00:00Z",
      },
      200,
      1_000,
      99
    );

    expect(apiMocks.GET).toHaveBeenCalledWith("/v1/admin/domains", {
      params: {
        query: {
          search: "owner@example.com",
          status: undefined,
          purpose: "sale",
          tld: "com",
          ownerId: 9,
          mailServerId: 3,
          createdFrom: "2026-07-01T00:00:00Z",
          createdTo: "2026-07-16T00:00:00Z",
          offset: 200,
          limit: 100,
          afterId: 99,
        },
      },
      signal: undefined,
    });
    expect(result.items[0].lastAllocatedAt).toBeUndefined();
  });

  it("maps the existing status selector to an explicit enable command", async () => {
    apiMocks.GET.mockResolvedValueOnce({ data: DOMAIN });
    apiMocks.PATCH.mockResolvedValueOnce({
      data: { ...DOMAIN, version: 8, status: "abnormal", ownerId: 10 },
    });

    const result = await updateAdminDomain(42, {
      ownerId: 10,
      mailServerId: 4,
      purpose: "sale",
      status: "normal",
    });

    expect(result.status).toBe("abnormal");
    expect(apiMocks.PATCH).toHaveBeenCalledWith(
      "/v1/admin/domains/{domainId}",
      {
        body: {
          ownerId: 10,
          purpose: "sale",
          mailServerId: 4,
          statusCommand: "enable",
        },
        params: {
          header: {
            "X-CSRF-Token": "domain-csrf",
            "Idempotency-Key": "domain-command-1",
          },
          path: { domainId: 42 },
          query: { version: 7 },
        },
      }
    );
  });

  it("does not send unchanged editor fields", async () => {
    apiMocks.GET.mockResolvedValueOnce({ data: DOMAIN });

    await expect(
      updateAdminDomain(42, {
        ownerId: DOMAIN.ownerId,
        purpose: DOMAIN.purpose,
        mailServerId: DOMAIN.mailServerId,
        status: DOMAIN.status,
      })
    ).resolves.toMatchObject({ id: 42 });

    expect(apiMocks.PATCH).not.toHaveBeenCalled();
  });

  it("keeps owner availability for editor validation", async () => {
    apiMocks.GET.mockResolvedValueOnce({
      data: {
        users: [
          {
            id: 9,
            email: "owner@example.com",
            nickname: "Owner",
            role: "supplier",
            enabled: false,
          },
        ],
        total: 1,
      },
    });

    await expect(listAdminDomainOwners()).resolves.toEqual([
      {
        id: 9,
        email: "owner@example.com",
        nickname: "Owner",
        role: "supplier",
        enabled: false,
      },
    ]);
  });

  it("composes the existing detail tabs from their owning backend APIs", async () => {
    apiMocks.GET.mockImplementation((path: string, options?: AdminDomainGetOptions) => {
      switch (path) {
        case "/v1/admin/domains/{domainId}":
          return Promise.resolve({ data: { ...DOMAIN, status: "normal" } });
        case "/v1/admin/servers":
          return Promise.resolve({
            data: {
              items: [
                {
                  id: 3,
                  ownerId: 9,
                  name: "Inbound",
                  serverAddress: "mx.example.com",
                  mxRecord: "mx.example.com",
                  spfRecord: "spf",
                  dkimRecord: "dkim",
                  dmarcRecord: "dmarc",
                  ptrRecord: "ptr",
                  status: "online",
                  createdAt: "2026-07-01T00:00:00Z",
                  updatedAt: "2026-07-01T00:00:00Z",
                },
              ],
              total: 1,
              offset: 0,
              limit: 100,
            },
          });
        case "/v1/admin/domains/{domainId}/mailboxes":
          if (options?.params?.query?.offset === 100) {
            return Promise.resolve({
              data: {
                items: [
                  {
                    id: 108,
                    email: "last@real.example.com",
                    status: "normal",
                    createdAt: "2026-07-10T00:00:00Z",
                  },
                ],
                total: 101,
                offset: 100,
                limit: 100,
              },
            });
          }
          return Promise.resolve({
            data: {
              items: Array.from({ length: 100 }, (_, index) => ({
                id: index + 8,
                email: `user${index}@real.example.com`,
                status: "normal",
                createdAt: "2026-07-10T00:00:00Z",
              })),
              total: 101,
              offset: 0,
              limit: 100,
            },
          });
        case "/v1/admin/allocations":
          return Promise.resolve({
            data: { items: [], total: 0, offset: 0, limit: 100 },
          });
        case "/v1/admin/tasks":
          return Promise.resolve({
            data: { items: [], total: 0, succeeded: 0, offset: 0, limit: 100 },
          });
        case "/v1/admin/messages":
          return Promise.resolve({
            data: {
              items: [
                {
                  id: 5,
                  mailbox: "main",
                  recipient: "user@real.example.com",
                  sender: "sender@example.net",
                  subject: "Real mail",
                  preview: "Real preview",
                  status: "received",
                  verificationCode: null,
                  orderNo: null,
                  receivedAt: "2026-07-15T00:00:00Z",
                },
              ],
              total: 1,
              offset: 0,
              limit: 100,
            },
          });
        default:
          throw new Error(`Unexpected GET ${path}`);
      }
    });

    const detail = await getAdminDomainDetail(42);

    expect(detail.domain).toBe("real.example.com");
    expect(detail.mailServer.mxRecord).toBe("mx.example.com");
    expect(detail.mailboxes).toHaveLength(101);
    expect(detail.mailboxes[100].email).toBe("last@real.example.com");
    expect(
      apiMocks.GET.mock.calls.find(
        ([path]) => path === "/v1/admin/messages"
      )
    ).toBeUndefined();
    expect(
      apiMocks.GET.mock.calls.find(
        ([path]) => path === "/v1/admin/tasks"
      )?.[1]?.params?.query
    ).toMatchObject({ bizType: "domain_resource", bizId: 42 });
    expect(
      apiMocks.GET.mock.calls.find(
        ([path]) => path === "/v1/admin/allocations"
      )?.[1]?.params?.query
    ).toMatchObject({ type: "domain", resourceId: 42 });
  });

  it("loads one bounded domain-mail page with server-side search and total", async () => {
    apiMocks.GET.mockResolvedValueOnce({
      data: { items: [], total: 37, offset: 0, limit: 10, hasMore: false },
    });

    await expect(
      listAdminDomainMessages(42, " sender@example.net ", 10)
    ).resolves.toEqual({
      items: [],
      total: 37,
      offset: 0,
      limit: 10,
      hasMore: false,
    });

    expect(apiMocks.GET).toHaveBeenCalledWith("/v1/admin/messages", {
      params: {
        query: {
          resourceId: 42,
          type: "domain",
          search: "sender@example.net",
          offset: 0,
          beforeReceivedAt: undefined,
          beforeId: undefined,
          includeTotal: true,
          limit: 10,
        },
      },
      signal: undefined,
    });
  });

  it("uses a stable cursor and skips total on domain-mail continuations", async () => {
    apiMocks.GET.mockResolvedValueOnce({
      data: { items: [], offset: 0, limit: 20, hasMore: false },
    });

    const cursor = {
      beforeReceivedAt: "2026-07-15T00:00:00Z",
      beforeId: 9,
    };
    await listAdminDomainMessages(42, "", 20, cursor);

    expect(apiMocks.GET).toHaveBeenCalledWith("/v1/admin/messages", {
      params: {
        query: {
          resourceId: 42,
          type: "domain",
          search: undefined,
          offset: undefined,
          beforeReceivedAt: cursor.beforeReceivedAt,
          beforeId: cursor.beforeId,
          includeTotal: false,
          limit: 20,
        },
      },
      signal: undefined,
    });
  });

  it("sends all-matching purpose changes as one filter command", async () => {
    apiMocks.POST.mockResolvedValueOnce({
      data: { queued: 0 },
    });

    await expect(
      setAdminDomainsPurposeByFilter(
        { search: "example", status: "normal", tld: "com" },
        "sale"
      )
    ).resolves.toMatchObject({ queued: 0 });

    expect(apiMocks.POST).toHaveBeenCalledWith("/v1/admin/domains/publish", {
      body: {
        selection: {
          mode: "filter",
          filter: {
            search: "example",
            status: "normal",
            purpose: undefined,
            tld: "com",
            ownerId: undefined,
            mailServerId: undefined,
            createdFrom: undefined,
            createdTo: undefined,
          },
        },
      },
      params: {
        header: {
          "X-CSRF-Token": "domain-csrf",
          "Idempotency-Key": "domain-command-1",
        },
      },
    });
  });
});
