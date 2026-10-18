// @vitest-environment jsdom
import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({ GET: vi.fn(), POST: vi.fn(), DELETE: vi.fn() }));
vi.mock("./api-client", () => ({
  apiClient: mocks, csrfHeader: () => ({ "X-CSRF-Token": "csrf" }),
  turnstileHeader: (token: string) => ({ "X-Turnstile-Token": token }),
  unwrap: async ({ data }: { data: unknown }) => data,
}));
vi.mock("./idempotency", () => ({ generateIdempotencyKey: () => "command-key" }));
import {
  deleteDaumResource, deleteDaumResourcesBatch, importDaumResources, listOwnedDaumResources,
  publishDaumResource, publishDaumResourcesByFilter, validateDaumResource, validateDaumResourcesBatch,
} from "./daum-api";

const resource = {
  id: 41, version: 7, type: "daum", email: "user@daum.net", suffix: "daum.net", status: "normal",
  forSale: false, longLived: true, qualityScore: 100, createdAt: "2026-10-01T00:00:00Z", updatedAt: "2026-10-01T00:00:00Z",
};
beforeEach(() => {
  vi.resetAllMocks();
  mocks.POST.mockResolvedValue({ data: {} });
  mocks.DELETE.mockResolvedValue({ data: {} });
});

describe("owned Daum API boundaries", () => {
  it("preserves the owned endpoint's safe status explanation", async () => {
    const safe = { ...resource, status: "abnormal", lastSafeError: "Daum mailbox access is temporarily unavailable." };
    mocks.GET.mockResolvedValue({ data: { items: [safe], total: 1 } });
    expect((await listOwnedDaumResources()).items).toEqual([safe]);
  });

  it("only returns console fields and the allowed suffix", async () => {
    mocks.GET.mockResolvedValue({ data: {
      items: [{ ...resource, aliases: [{ email: "user@hanmail.net" }], kakaoEmail: "private@example.test", ownerUserId: 7 }, { ...resource, id: 42, email: "user@hanmail2.net" }],
      total: 1, nextAfterId: null,
      facets: {
        status: { all: 1, pending: 0, validating: 0, identifying: 0, normal: 1, abnormal: 0, disabled: 0 },
        forSale: { all: 1, yes: 0, no: 1 }, longLived: { all: 1, yes: 1, no: 0 },
        suffixes: [{ key: "daum.net", count: 1 }, { key: "hanmail.net", count: 1 }, { key: "hanmail2.net", count: 1 }],
      },
    } });
    const response = await listOwnedDaumResources({ suffix: "@daum.net", search: " user " });
    expect(response.items).toEqual([resource]);
    expect(response.facets?.suffixes).toEqual([{ key: "daum.net", count: 1 }]);
    expect(JSON.stringify(response)).not.toMatch(/aliases|hanmail|kakaoEmail|ownerUserId/);
    expect(mocks.GET.mock.calls[0][0]).toBe("/v1/daum/resources");
    expect(mocks.GET.mock.calls[0][1].params.query).toMatchObject({ suffix: "daum.net", search: "user" });
    await expect(listOwnedDaumResources({ suffix: "hanmail.net" })).rejects.toThrow("Invalid Daum resource request.");
    expect(mocks.GET).toHaveBeenCalledOnce();
  });

  it("imports with the user's verification and idempotency headers, without administrative parameters", async () => {
    const file = new File(["user@daum.net----app"], "accounts.txt");
    await importDaumResources(file, true, "verified", "abort", undefined, "stable-part-key");
    expect(mocks.POST.mock.calls[0][0]).toBe("/v1/daum/resources/imports");
    const options = mocks.POST.mock.calls[0][1];
    expect(options.params.header).toEqual({ "X-CSRF-Token": "csrf", "X-Turnstile-Token": "verified", "Idempotency-Key": "stable-part-key" });
    expect([...options.body.keys()]).toEqual(["file", "longLived", "errorStrategy"]);
    expect(options.body.get("file")).toBe(file);
    expect(options.body.get("errorStrategy")).toBe("abort");
  });

  it("uses owned versioned publish/delete and a versionless validation", async () => {
    await validateDaumResource(41);
    await publishDaumResource(41, 7);
    await deleteDaumResource(41, 7);
    expect(mocks.POST.mock.calls.map(([path]) => path)).toEqual(["/v1/daum/resources/{resourceId}/validate", "/v1/daum/resources/{resourceId}/publish"]);
    expect(mocks.POST.mock.calls[0][1]).not.toHaveProperty("body");
    expect(mocks.POST.mock.calls[1][1].body).toEqual({ version: 7 });
    expect(mocks.DELETE).toHaveBeenCalledWith("/v1/daum/resources/{resourceId}", expect.objectContaining({ body: { version: 7 } }));
    expect(mocks.GET).not.toHaveBeenCalled();
  });

  it("submits selected validation only once and keeps the receipt if later task reads fail", async () => {
    const accepted = { taskId: "daum_bulk:7", status: "queued", requested: 2, processed: 0, affected: 0, skipped: 0, reasonCounts: [] };
    mocks.POST.mockResolvedValue({ data: accepted });
    mocks.GET.mockRejectedValue(new Error("Status temporarily unavailable"));
    expect(await validateDaumResourcesBatch([41, 41, 42])).toEqual(accepted);
    expect(mocks.GET).not.toHaveBeenCalled();
    expect(mocks.POST.mock.calls[0][1].body).toEqual({ selection: { mode: "ids", resourceIds: [41, 42] } });
    expect(await deleteDaumResourcesBatch([41, 42])).toEqual(accepted);
    expect(mocks.POST).toHaveBeenCalledTimes(2);
    expect(mocks.GET).toHaveBeenCalledWith("/v1/daum/resources/bulk-tasks/{taskId}", { params: { path: { taskId: "daum_bulk:7" } } });
  });

  it("passes the current owned filter and waits for final publish counts", async () => {
    const result = { taskId: "daum_bulk:8", status: "succeeded", requested: 2, processed: 2, affected: 1, skipped: 1, reasonCounts: [] };
    mocks.POST.mockResolvedValue({ data: { ...result, status: "queued", affected: 0 } });
    mocks.GET.mockResolvedValue({ data: result });
    expect(await publishDaumResourcesByFilter({ status: "normal", forSale: false, suffix: "daum.net" })).toEqual(result);
    expect(mocks.POST.mock.calls[0][1].body).toEqual({ selection: { mode: "filter", filter: expect.objectContaining({ status: "normal", forSale: false, suffix: "daum.net" }) } });
    expect(mocks.POST.mock.calls[0][0]).not.toContain("admin");
    expect(mocks.GET.mock.calls[0][0]).not.toContain("admin");
  });
});
