// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
const mocks = vi.hoisted(() => ({ GET: vi.fn(), POST: vi.fn(), PATCH: vi.fn(), PUT: vi.fn(), DELETE: vi.fn(), nextKey: 0 }));
vi.mock("./api-client", async (importOriginal) => ({ ...await importOriginal<typeof import("./api-client")>(), apiClient: mocks, csrfHeader: () => ({ "X-CSRF-Token": "csrf" }), unwrap: async (result: { data: unknown }) => result.data }));
vi.mock("./idempotency", () => ({ generateIdempotencyKey: () => `command-key-${++mocks.nextKey}` }));
import { IamApiError } from "./api-client";
import { isDaumBulkOutcomeUnconfirmed, type DaumBulkSession } from "./daum-bulk-session";
import {
  createAdminDaumBulkSession, deleteAdminDaumResourcesByFilter, deleteAdminDaumResourcesByIds,
  disableAdminDaumResourcesByIds, listAdminDaumResources, maintainAdminDaumResourcesByFilter,
  maintainAdminDaumResourcesByIds, scanAdminDaumProjects, setAdminDaumResourcesForSaleByFilter,
  setAdminDaumResourcesForSaleByIds, updateAdminDaumResource, validateAdminDaumResource,
} from "./admin-daum-api";
beforeEach(() => { vi.resetAllMocks(); mocks.nextKey = 0; mocks.POST.mockResolvedValue({ data: {} }); });
afterEach(() => vi.useRealTimers());
describe("admin Daum parity API contracts", () => {
  it("submits validation and history without a stale frontend version", async () => {
    await validateAdminDaumResource(41);
    await scanAdminDaumProjects(41);
    expect(mocks.POST.mock.calls.map(([path]) => path)).toEqual(["/v1/admin/daum/resources/{resourceId}/validate", "/v1/admin/daum/resources/{resourceId}/history"]);
    for (const [, options] of mocks.POST.mock.calls) {
      expect(options).not.toHaveProperty("body");
      expect(options.params).toEqual({ path: { resourceId: 41 }, header: { "X-CSRF-Token": "csrf", "Idempotency-Key": expect.any(String) } });
    }
  });
  it("preserves synchronous bulk disable outcomes and skipped IDs", async () => {
    const result = { requested: 2, affected: 1, skipped: 1, affectedResourceIds: [41], skippedResourceIds: [42], reasonCounts: [{ reason: "already_target", count: 1 }] };
    mocks.POST.mockResolvedValue({ data: result });
    expect(await disableAdminDaumResourcesByIds([41, 42, 41])).toEqual(result);
    expect(mocks.POST).toHaveBeenCalledWith("/v1/admin/daum/resources/bulk/disable", { body: { selection: { mode: "ids", resourceIds: [41, 42] } }, params: { header: { "X-CSRF-Token": "csrf", "Idempotency-Key": "command-key-1" } } });
  });
  it("passes protocol and suffix filters to the same scoped list", async () => {
    mocks.GET.mockResolvedValue({ data: { items: [], total: 0 } });
    await listAdminDaumResources({ protocol: "pop3", suffix: "@hanmail2.net", status: "all" });
    expect(mocks.GET.mock.calls[0][1].params.query).toMatchObject({ protocol: "pop3", suffix: "hanmail2.net", status: undefined });
  });
  it("retains explicit null unlink fields in the atomic edit request", async () => {
    mocks.PATCH.mockResolvedValue({ data: {} });
    mocks.GET.mockResolvedValue({ data: { id: 41, email: "user@daum.net" } });
    await updateAdminDaumResource(41, { version: 3, bindingAddress: null, pickupUrl: null, forSale: false, password: "login", appPassword: "app" });
    expect(mocks.PATCH.mock.calls[0][1].body).toEqual({ version: 3, bindingAddress: null, pickupUrl: null, forSale: false, password: "login", appPassword: "app" });
  });
  it("waits for selected tasks using only the Daum resource permission", async () => {
    const finished = { taskId: "daum_bulk:7", status: "succeeded", requested: 2, affected: 1, skipped: 1, reasonCounts: [{ reason: "already_target", count: 1 }] };
    mocks.POST.mockResolvedValue({ data: { ...finished, status: "queued", affected: 0, skipped: 0 } });
    mocks.GET.mockImplementation(async (path: string) => {
      if (path === "/v1/admin/tasks/{taskId}") throw new Error("403 governance:task/read denied");
      return { data: finished };
    });
    expect(await setAdminDaumResourcesForSaleByIds([41, 42], true)).toEqual(finished);
    expect(mocks.GET.mock.calls.map(([path]) => path)).toEqual(["/v1/admin/daum/resources/bulk-tasks/{taskId}"]);
  });

  it.each(["publish", "unpublish", "delete"])("preserves accepted %s tasks when progress lookup fails", async (action) => {
    const accepted = { taskId: "daum_bulk:7", status: "queued", requested: 1, affected: 0, skipped: 0, reasonCounts: [] };
    mocks.POST.mockResolvedValue({ data: accepted });
    mocks.GET.mockRejectedValue(new Error("progress lookup unavailable"));
    const result = action === "delete"
      ? await deleteAdminDaumResourcesByIds([41])
      : await setAdminDaumResourcesForSaleByIds([41], action === "publish");
    expect(result).toEqual(accepted);
    expect(mocks.POST).toHaveBeenCalledOnce();
    expect(mocks.GET).toHaveBeenCalledOnce();
  });

  it("returns failed task progress without confusing it with a rejected submission", async () => {
    const accepted = { taskId: "daum_bulk:7", status: "queued", requested: 1, affected: 0, skipped: 0, reasonCounts: [] };
    const failed = { ...accepted, status: "failed" };
    mocks.POST.mockResolvedValue({ data: accepted });
    mocks.GET.mockResolvedValue({ data: failed });
    expect(await deleteAdminDaumResourcesByIds([41])).toEqual(failed);
  });

  it("polls queued tasks and retains the last progress if a later read fails", async () => {
    vi.useFakeTimers();
    const accepted = { taskId: "daum_bulk:7", status: "queued", requested: 2, affected: 0, skipped: 0, reasonCounts: [] };
    const running = { ...accepted, status: "running", processed: 1, affected: 1 };
    mocks.POST.mockResolvedValue({ data: accepted });
    mocks.GET.mockResolvedValueOnce({ data: running }).mockRejectedValueOnce(new TypeError("Failed to fetch"));
    const result = deleteAdminDaumResourcesByIds([41, 42]);
    await vi.advanceTimersByTimeAsync(1000);
    expect(await result).toEqual(running);
    expect(mocks.POST).toHaveBeenCalledOnce();
    expect(mocks.GET).toHaveBeenCalledTimes(2);
  });

  it("still reports a rejected mutation and never starts progress polling", async () => {
    mocks.POST.mockRejectedValue(new Error("mutation rejected"));
    await expect(deleteAdminDaumResourcesByIds([41])).rejects.toThrow("mutation rejected");
    expect(mocks.GET).not.toHaveBeenCalled();
  });
});

const completed = { taskId: "daum_bulk:1", status: "succeeded", requested: 2, processed: 2, affected: 1, skipped: 1, reasonCounts: [{ reason: "already_target", count: 1 }] };
const syncResult = { requested: 2, affected: 1, skipped: 1, affectedResourceIds: [41], skippedResourceIds: [42], reasonCounts: completed.reasonCounts };
const keys = () => mocks.POST.mock.calls.map((call) => call[1].params.header["Idempotency-Key"]);
const bulkOperations: Array<[string, (session: DaumBulkSession) => Promise<unknown>]> = [
  ["validate ids", (session) => maintainAdminDaumResourcesByIds("validate", [42, 41, 42], session)],
  ["validate filter", (session) => maintainAdminDaumResourcesByFilter("validate", { status: "normal" }, session)],
  ["history ids", (session) => maintainAdminDaumResourcesByIds("history", [41, 42], session)],
  ["history filter", (session) => maintainAdminDaumResourcesByFilter("history", { status: "normal" }, session)],
  ["publish ids", (session) => setAdminDaumResourcesForSaleByIds([41, 42], true, session)],
  ["publish filter", (session) => setAdminDaumResourcesForSaleByFilter({}, true, session)],
  ["unpublish ids", (session) => setAdminDaumResourcesForSaleByIds([41, 42], false, session)],
  ["unpublish filter", (session) => setAdminDaumResourcesForSaleByFilter({}, false, session)],
  ["delete ids", (session) => deleteAdminDaumResourcesByIds([41, 42], session)],
  ["delete filter", (session) => deleteAdminDaumResourcesByFilter({}, session)],
  ["disable ids", (session) => disableAdminDaumResourcesByIds([41, 42], session)],
];

describe("administrator Daum bulk operation confirmation", () => {
  it.each(bulkOperations)("retries an unconfirmed %s with its original key and ends it after a receipt", async (name, send) => {
    const session = createAdminDaumBulkSession();
    const result = name === "disable ids" ? syncResult : completed;
    const durableTasks = new Map<string, unknown>();
    let loseResponse = true;
    mocks.POST.mockImplementation(async (_path, options) => {
      const key = options.params.header["Idempotency-Key"];
      durableTasks.set(key, result);
      if (loseResponse) { loseResponse = false; throw new TypeError("Response lost after durable acceptance"); }
      return { data: durableTasks.get(key) };
    });
    await expect(send(session)).rejects.toThrow("Response lost");
    expect(await send(session)).toEqual(result);
    expect(durableTasks.size).toBe(1);
    expect(new Set(keys()).size).toBe(1);
    expect(await send(session)).toEqual(result);
    expect(durableTasks.size).toBe(2);
    if (name === "disable ids") expect(mocks.GET).not.toHaveBeenCalled();
  });

  it("keeps unknown A when B or another action runs and normalizes filter ordering and suffix spelling", async () => {
    const session = createAdminDaumBulkSession();
    mocks.POST.mockRejectedValueOnce(new TypeError("A lost"))
      .mockResolvedValue({ data: completed });
    await expect(deleteAdminDaumResourcesByFilter({ forSale: false, suffix: " @DAUM.NET " }, session)).rejects.toThrow();
    await deleteAdminDaumResourcesByFilter({ status: "abnormal" }, session);
    await setAdminDaumResourcesForSaleByFilter({ suffix: "daum.net", forSale: false }, true, session);
    await deleteAdminDaumResourcesByFilter({ suffix: "daum.net", forSale: false }, session);
    expect(new Set(keys().slice(0, 3)).size).toBe(3);
    expect(keys()[3]).toBe(keys()[0]);
  });

  it("shares concurrent normalized selections and keeps the key after unknown then rejected retries", async () => {
    const session = createAdminDaumBulkSession();
    let fail!: (error: Error) => void;
    mocks.POST.mockReturnValueOnce(new Promise((_resolve, reject) => { fail = reject; }))
      .mockRejectedValueOnce(new IamApiError(403, { message: "CSRF token expired" }))
      .mockResolvedValue({ data: completed });
    const first = deleteAdminDaumResourcesByIds([42, 41, 41], session);
    const duplicate = deleteAdminDaumResourcesByIds([41, 42], session);
    expect(duplicate).toBe(first);
    const settled = Promise.allSettled([first, duplicate]);
    await Promise.resolve();
    fail(new TypeError("Response lost"));
    await settled;
    const error = await deleteAdminDaumResourcesByIds([41, 42], session).catch((error: unknown) => error);
    expect(isDaumBulkOutcomeUnconfirmed(error)).toBe(true);
    await deleteAdminDaumResourcesByIds([41, 42], session);
    expect(new Set(keys()).size).toBe(1);
    await deleteAdminDaumResourcesByIds([41, 42], session);
    expect(keys()[3]).not.toBe(keys()[0]);
  });

  it.each(["async", "disable"])("keeps an empty %s receipt unconfirmed", async (mode) => {
    const session = createAdminDaumBulkSession();
    const send = () => mode === "async" ? deleteAdminDaumResourcesByFilter({}, session) : disableAdminDaumResourcesByIds([41, 42], session);
    mocks.POST.mockResolvedValueOnce({ data: {} }).mockResolvedValue({ data: mode === "async" ? completed : syncResult });
    await expect(send()).rejects.toThrow("not confirmed");
    await send();
    expect(new Set(keys()).size).toBe(1);
  });

  it("retains the key for an unknown POST status and the accepted receipt for an unreadable polling response", async () => {
    const session = createAdminDaumBulkSession();
    const accepted = { ...completed, status: "queued", affected: 0, skipped: 0 };
    mocks.POST.mockResolvedValueOnce({ data: { taskId: accepted.taskId, status: "unknown" } })
      .mockResolvedValue({ data: accepted });
    mocks.GET.mockResolvedValue({ data: {} });
    await expect(deleteAdminDaumResourcesByIds([41, 42], session)).rejects.toThrow("not confirmed");
    expect(await deleteAdminDaumResourcesByIds([41, 42], session)).toEqual(accepted);
    expect(new Set(keys()).size).toBe(1);
  });

  it("starts a new key after a fresh known rejection or in a different user's session", async () => {
    const session = createAdminDaumBulkSession();
    mocks.POST.mockRejectedValueOnce(new IamApiError(403, { message: "Permission denied" })).mockResolvedValue({ data: completed });
    await expect(deleteAdminDaumResourcesByFilter({}, session)).rejects.toThrow("Permission denied");
    await deleteAdminDaumResourcesByFilter({}, session);
    await deleteAdminDaumResourcesByFilter({}, createAdminDaumBulkSession());
    expect(new Set(keys()).size).toBe(3);
  });

  it("coalesces selected polling and preserves acceptance without a second POST when status is unavailable", async () => {
    const session = createAdminDaumBulkSession();
    const accepted = { ...completed, status: "queued", processed: 0, affected: 0, skipped: 0 };
    mocks.POST.mockResolvedValue({ data: accepted });
    mocks.GET.mockRejectedValue(new TypeError("Status unavailable"));
    const first = deleteAdminDaumResourcesByIds([41, 42], session);
    expect(deleteAdminDaumResourcesByIds([42, 41], session)).toBe(first);
    expect(await first).toEqual(accepted);
    expect(mocks.POST).toHaveBeenCalledOnce();
    expect(mocks.GET).toHaveBeenCalledOnce();
    await deleteAdminDaumResourcesByIds([41, 42], session);
    expect(new Set(keys()).size).toBe(2);
  });
});
