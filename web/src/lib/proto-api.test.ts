// @vitest-environment jsdom
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({ GET: vi.fn(), POST: vi.fn(), DELETE: vi.fn(), PUT: vi.fn() }));
vi.mock("./api-client", () => ({
  apiClient: mocks,
  csrfHeader: () => ({ "X-CSRF-Token": "csrf" }),
  turnstileHeader: (token: string) => token ? { "X-Turnstile-Token": token } : {},
  unwrap: async (result: { data?: unknown }) => result.data,
}));
vi.mock("./idempotency", () => ({ generateIdempotencyKey: () => "proto-key" }));

import { importProtoResources, listProtoResources, publishProtoResource, validateProtoResource, publishProtoResourcesByFilter, waitForProtoImport } from "./proto-api";
import { listAdminProtoResources } from "./admin-proto-api";
import { useBlockPagedList } from "@/hooks/use-block-paged-list";

describe("Proto API adapter", () => {
  beforeEach(() => vi.resetAllMocks());
  afterEach(cleanup);

  it.each([
    ["owned", listProtoResources, undefined],
    ["admin", listAdminProtoResources, undefined],
    ["owned with old skipped count", listProtoResources, -1],
    ["admin with old skipped count", listAdminProtoResources, -1],
  ] as const)("keeps the exact total after %s block prefetch", async (_name, listResources, skippedTotal) => {
    const rows = Array.from({ length: 250 }, (_, index) => ({ id: 250 - index }));
    mocks.GET.mockImplementation(async (_path, options) => {
      const { offset, limit, includeTotal } = options.params.query;
      return { data: {
        items: rows.slice(offset, offset + limit),
        ...(includeTotal !== false ? { total: rows.length } : skippedTotal === undefined ? {} : { total: skippedTotal }),
        nextAfterId: offset + limit < rows.length ? rows[offset + limit - 1].id : null,
      } };
    });
    const loadBlock = async (offset: number, limit: number, cursor: { afterId?: number } | undefined, signal: AbortSignal) => {
      const response = await listResources({}, offset, limit, cursor?.afterId, { includeTotal: false, includeFacets: false, signal });
      return { ...response, items: response.items.map((item) => item.id) };
    };
    const { result, rerender } = renderHook(({ activePage }) => useBlockPagedList({
      activePage, blockSize: 100, filterKey: "proto", loadBlock, pageSize: 10,
    }), { initialProps: { activePage: 1 } });
    await waitFor(() => expect(result.current.loaded).toBe(true));
    expect(result.current.total).toBe(101);
    await act(async () => {
      const stats = await listResources({}, 0, 1);
      result.current.setTotal(stats.total!);
    });

    rerender({ activePage: 8 });
    await waitFor(() => expect(result.current.loadedItems).toHaveLength(200));
    expect(mocks.GET.mock.calls[2]?.[1].params.query).toMatchObject({ offset: 100, afterId: 151, includeTotal: false });
    expect(result.current.total).toBe(250);
    expect(result.current.pagedItems).toEqual(rows.slice(70, 80).map((item) => item.id));

    rerender({ activePage: 11 });
    expect(result.current.pagedItems).toEqual(rows.slice(100, 110).map((item) => item.id));
    expect(result.current.total).toBe(250);
  });

  it("preserves uploaded credentials and sends classification plus verification", async () => {
    mocks.POST.mockResolvedValueOnce({ data: { importId: 1, status: "processing" } });
    const source = "a@proto.test----  secret  \n";
    await importProtoResources(new File([source], "accounts.txt"), false, "challenge", "abort");
    const options = mocks.POST.mock.calls[0]?.[1] as { body: FormData; params: { header: Record<string, string> } };
    expect(mocks.POST.mock.calls[0]?.[0]).toBe("/v1/proto/resources/imports");
    expect(await (options.body.get("file") as File).text()).toBe(source);
    expect(options.body.get("longLived")).toBe("false");
    expect(options.body.has("clientId")).toBe(false);
    expect(options.body.get("errorStrategy")).toBe("abort");
    expect(options.params.header).toEqual({ "X-CSRF-Token": "csrf", "Idempotency-Key": "proto-key", "X-Turnstile-Token": "challenge" });
    expect(mocks.GET).not.toHaveBeenCalled();
  });

  it("sends resource versions and one server-filtered bulk command", async () => {
    mocks.GET.mockResolvedValueOnce({ data: { items: [], total: 0, offset: 0, limit: 20 } });
    mocks.POST.mockResolvedValue({ data: { taskId: "proto_bulk:42", status: "queued" } });
    await listProtoResources({ search: " proto ", status: "pending", forSale: false, suffix: "@example.com" }, 0, 100, 7, { includeTotal: false });
    await validateProtoResource(9, 4);
    await publishProtoResource(9, 4);
    await publishProtoResourcesByFilter({ status: "normal", forSale: false });
    expect(mocks.GET.mock.calls[0]?.[1].params.query).toMatchObject({ search: "proto", suffix: "example.com", forSale: false, afterId: 7, includeTotal: false });
    expect(mocks.POST.mock.calls.slice(0, 2).map((call) => call[1].body)).toEqual([{ version: 4 }, { version: 4 }]);
    expect(mocks.POST.mock.calls[2]?.[0]).toBe("/v1/proto/resources/bulk/{command}");
    expect(mocks.POST.mock.calls[2]?.[1].body).toMatchObject({ selection: { mode: "filter", filter: { status: "normal", forSale: false } } });
  });

  it("preserves a failed import result and cancels polling without stale requests", async () => {
    mocks.GET.mockResolvedValueOnce({ data: { importId: 1, status: "failed", lastSafeError: "Invalid proto import format." } });
    await expect(waitForProtoImport(1)).resolves.toMatchObject({ status: "failed" });
    const controller = new AbortController();
    mocks.GET.mockResolvedValueOnce({ data: { importId: 2, status: "processing" } });
    const waiting = waitForProtoImport(2, { signal: controller.signal, intervalMs: 10000 });
    const rejection = expect(waiting).rejects.toMatchObject({ name: "AbortError" });
    await Promise.resolve();
    controller.abort();
    await rejection;
    expect(mocks.GET).toHaveBeenCalledTimes(2);
  });
});
