// @vitest-environment jsdom
import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({ GET: vi.fn(), POST: vi.fn(), DELETE: vi.fn(), nextKey: 0 }));
vi.mock("./api-client", async (importOriginal) => ({
  ...await importOriginal<typeof import("./api-client")>(),
  apiClient: mocks, csrfHeader: () => ({ "X-CSRF-Token": "csrf" }),
  turnstileHeader: (token: string) => ({ "X-Turnstile-Token": token }),
  unwrap: async ({ data }: { data: unknown }) => data,
}));
vi.mock("./idempotency", () => ({ generateIdempotencyKey: () => `command-key-${++mocks.nextKey}` }));
import { IamApiError } from "./api-client";
import {
  createDaumBulkSession, deleteDaumResource, deleteDaumResourcesBatch, deleteDaumResourcesByFilter,
  importDaumResources, isDaumBulkOutcomeUnconfirmed, listOwnedDaumResources,
  publishDaumResource, publishDaumResourcesByFilter, validateDaumResource, validateDaumResourcesBatch,
} from "./daum-api";

const resource = {
  id: 41, version: 7, type: "daum", email: "user@daum.net", suffix: "daum.net", status: "normal",
  forSale: false, longLived: true, qualityScore: 100, createdAt: "2026-10-01T00:00:00Z", updatedAt: "2026-10-01T00:00:00Z",
};
beforeEach(() => {
  vi.resetAllMocks();
  mocks.nextKey = 0;
  mocks.POST.mockResolvedValue({ data: {} });
  mocks.DELETE.mockResolvedValue({ data: {} });
});

const accepted = { taskId: "daum_bulk:1", status: "queued", requested: 2, processed: 0, affected: 0, skipped: 0, reasonCounts: [] };
const completed = { ...accepted, status: "succeeded", processed: 2, affected: 2 };
const postedKeys = () => mocks.POST.mock.calls.map((call) => call[1].params.header["Idempotency-Key"]);

describe("owned Daum bulk retry lifecycle", () => {
  it("reuses the receipt key after the server accepted validation but its response was lost", async () => {
    const session = createDaumBulkSession();
    const durableTasks = new Map<string, string>();
    let loseResponse = true;
    mocks.POST.mockImplementation(async (_path, options) => {
      const key = options.params.header["Idempotency-Key"];
      const taskId = durableTasks.get(key) ?? `daum_bulk:${durableTasks.size + 1}`;
      durableTasks.set(key, taskId);
      if (loseResponse) {
        loseResponse = false;
        throw new TypeError("Response lost after server acceptance");
      }
      return { data: { ...accepted, taskId } };
    });

    await expect(validateDaumResourcesBatch([42, 41, 42], session)).rejects.toThrow("Response lost");
    expect(await validateDaumResourcesBatch([41, 42], session)).toEqual(accepted);
    expect(durableTasks.size).toBe(1);
    expect(new Set(postedKeys()).size).toBe(1);
    expect(mocks.POST.mock.calls.map((call) => call[1].body.selection.resourceIds)).toEqual([[41, 42], [41, 42]]);
    expect(mocks.GET).not.toHaveBeenCalled();
  });

  it.each([
    new TypeError("Network unavailable"),
    new DOMException("Request timed out", "AbortError"),
    new IamApiError(408, { message: "Request timed out" }),
    new IamApiError(503, { message: "Upstream unavailable" }),
  ])("keeps an uncertain deletion key for %s", async (error) => {
    const session = createDaumBulkSession();
    mocks.POST.mockRejectedValueOnce(error).mockResolvedValue({ data: completed });
    await expect(deleteDaumResourcesByFilter({ forSale: false }, session)).rejects.toBe(error);
    expect(isDaumBulkOutcomeUnconfirmed(error)).toBe(true);
    await deleteDaumResourcesByFilter({ forSale: false }, session);
    expect(new Set(postedKeys()).size).toBe(1);
  });

  it.each([400, 403, 409, 429])("ends a freshly rejected HTTP %i operation", async (status) => {
    const session = createDaumBulkSession();
    const error = new IamApiError(status, { message: "Request rejected" });
    mocks.POST.mockRejectedValueOnce(error).mockResolvedValue({ data: completed });
    await expect(deleteDaumResourcesBatch([41], session)).rejects.toBe(error);
    expect(isDaumBulkOutcomeUnconfirmed(error)).toBe(false);
    await deleteDaumResourcesBatch([41], session);
    expect(new Set(postedKeys()).size).toBe(2);
  });

  it("keeps the original unknown key when a later retry is rejected before reaching the service", async () => {
    const session = createDaumBulkSession();
    mocks.POST.mockRejectedValueOnce(new TypeError("Response lost"))
      .mockRejectedValueOnce(new IamApiError(403, { message: "CSRF token expired" }))
      .mockResolvedValue({ data: completed });
    await expect(deleteDaumResourcesBatch([41], session)).rejects.toThrow("Response lost");
    const retryError = await deleteDaumResourcesBatch([41], session).catch((error: unknown) => error);
    expect(isDaumBulkOutcomeUnconfirmed(retryError)).toBe(true);
    await deleteDaumResourcesBatch([41], session);
    expect(new Set(postedKeys()).size).toBe(1);
  });

  it("does not confirm an empty response without a task receipt", async () => {
    const session = createDaumBulkSession();
    mocks.POST.mockResolvedValueOnce({ data: undefined }).mockResolvedValue({ data: accepted });
    const error = await validateDaumResourcesBatch([41], session).catch((error: unknown) => error);
    expect(isDaumBulkOutcomeUnconfirmed(error)).toBe(true);
    expect(await validateDaumResourcesBatch([41], session)).toEqual(accepted);
    expect(new Set(postedKeys()).size).toBe(1);
  });

  it("preserves unknown selection A while selection B and a different command have independent keys", async () => {
    const session = createDaumBulkSession();
    mocks.POST.mockRejectedValueOnce(new Error("A response lost"))
      .mockRejectedValueOnce(new Error("B response lost"))
      .mockRejectedValueOnce(new Error("Publish response lost"))
      .mockResolvedValue({ data: completed });
    await expect(deleteDaumResourcesByFilter({ status: "normal", search: " user " }, session)).rejects.toThrow();
    await expect(deleteDaumResourcesByFilter({ status: "abnormal", search: "user" }, session)).rejects.toThrow();
    await expect(publishDaumResourcesByFilter({ search: "user", status: "normal" }, session)).rejects.toThrow();
    await deleteDaumResourcesByFilter({ search: "user", status: "normal" }, session);
    const keys = postedKeys();
    expect(new Set(keys.slice(0, 3)).size).toBe(3);
    expect(keys[3]).toBe(keys[0]);
  });

  it("shares concurrent identical requests and starts a new operation only after confirmed acceptance", async () => {
    const session = createDaumBulkSession();
    let finish!: (value: { data: typeof accepted }) => void;
    mocks.POST.mockReturnValueOnce(new Promise((resolve) => { finish = resolve; }))
      .mockResolvedValue({ data: accepted });
    const first = validateDaumResourcesBatch([41, 42], session);
    const duplicate = validateDaumResourcesBatch([42, 41], session);
    expect(duplicate).toBe(first);
    await Promise.resolve();
    expect(mocks.POST).toHaveBeenCalledOnce();
    finish({ data: accepted });
    expect(await first).toEqual(accepted);
    expect(await duplicate).toEqual(accepted);
    await validateDaumResourcesBatch([41, 42], session);
    expect(mocks.POST).toHaveBeenCalledTimes(2);
    expect(new Set(postedKeys()).size).toBe(2);
    expect(mocks.GET).not.toHaveBeenCalled();
  });

  it("coalesces a concurrent lost response and never revives its key after a successful retry", async () => {
    const session = createDaumBulkSession();
    let fail!: (reason: Error) => void;
    mocks.POST.mockReturnValueOnce(new Promise((_resolve, reject) => { fail = reject; }))
      .mockResolvedValue({ data: completed });
    const first = deleteDaumResourcesBatch([41], session);
    const duplicate = deleteDaumResourcesBatch([41], session);
    const failures = Promise.allSettled([first, duplicate]);
    await Promise.resolve();
    fail(new Error("Response lost"));
    expect((await failures).map((result) => result.status)).toEqual(["rejected", "rejected"]);
    await deleteDaumResourcesBatch([41], session);
    await deleteDaumResourcesBatch([41], session);
    const keys = postedKeys();
    expect(keys).toHaveLength(3);
    expect(keys[1]).toBe(keys[0]);
    expect(keys[2]).not.toBe(keys[0]);
  });

  it("returns an accepted task after polling failure without repeating POST, then permits a new explicit operation", async () => {
    const session = createDaumBulkSession();
    mocks.POST.mockResolvedValue({ data: accepted });
    mocks.GET.mockRejectedValue(new Error("Status temporarily unavailable"));
    const first = publishDaumResourcesByFilter({}, session);
    const duplicate = publishDaumResourcesByFilter({}, session);
    expect(duplicate).toBe(first);
    expect(await first).toEqual(accepted);
    expect(mocks.POST).toHaveBeenCalledOnce();
    expect(mocks.GET).toHaveBeenCalledOnce();
    await publishDaumResourcesByFilter({}, session);
    expect(new Set(postedKeys()).size).toBe(2);
  });

  it("does not share an unknown operation between page or user sessions", async () => {
    mocks.POST.mockRejectedValueOnce(new Error("Response lost")).mockResolvedValue({ data: accepted });
    await expect(validateDaumResourcesBatch([41], createDaumBulkSession())).rejects.toThrow();
    await validateDaumResourcesBatch([41], createDaumBulkSession());
    expect(new Set(postedKeys()).size).toBe(2);
  });
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
