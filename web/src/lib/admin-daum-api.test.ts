// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
const mocks = vi.hoisted(() => ({ GET: vi.fn(), POST: vi.fn(), PATCH: vi.fn(), PUT: vi.fn(), DELETE: vi.fn() }));
vi.mock("./api-client", () => ({ apiClient: mocks, csrfHeader: () => ({ "X-CSRF-Token": "csrf" }), unwrap: async (result: { data: unknown }) => result.data }));
vi.mock("./idempotency", () => ({ generateIdempotencyKey: () => "command-key" }));
import { deleteAdminDaumResourcesByIds, disableAdminDaumResourcesByIds, listAdminDaumResources, scanAdminDaumProjects, setAdminDaumResourcesForSaleByIds, updateAdminDaumResource, validateAdminDaumResource } from "./admin-daum-api";
beforeEach(() => { vi.clearAllMocks(); mocks.POST.mockResolvedValue({ data: {} }); });
afterEach(() => vi.useRealTimers());
describe("admin Daum parity API contracts", () => {
  it("submits validation and history without a stale frontend version", async () => {
    await validateAdminDaumResource(41);
    await scanAdminDaumProjects(41);
    expect(mocks.POST.mock.calls.map(([path]) => path)).toEqual(["/v1/admin/daum/resources/{resourceId}/validate", "/v1/admin/daum/resources/{resourceId}/history"]);
    for (const [, options] of mocks.POST.mock.calls) {
      expect(options).not.toHaveProperty("body");
      expect(options.params).toEqual({ path: { resourceId: 41 }, header: { "X-CSRF-Token": "csrf", "Idempotency-Key": "command-key" } });
    }
  });
  it("preserves synchronous bulk disable outcomes and skipped IDs", async () => {
    const result = { requested: 2, affected: 1, skipped: 1, affectedResourceIds: [41], skippedResourceIds: [42], reasonCounts: [{ reason: "already_target", count: 1 }] };
    mocks.POST.mockResolvedValue({ data: result });
    expect(await disableAdminDaumResourcesByIds([41, 42, 41])).toEqual(result);
    expect(mocks.POST).toHaveBeenCalledWith("/v1/admin/daum/resources/bulk/disable", { body: { selection: { mode: "ids", resourceIds: [41, 42] } }, params: { header: { "X-CSRF-Token": "csrf", "Idempotency-Key": "command-key" } } });
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
