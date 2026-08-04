import { beforeEach, describe, expect, it, vi } from "vitest";

const apiMocks = vi.hoisted(() => ({
  DELETE: vi.fn(),
  GET: vi.fn(),
  POST: vi.fn(),
  PUT: vi.fn(),
}));

vi.mock("./api-client", () => ({
  apiClient: apiMocks,
  csrfHeader: () => ({ "X-CSRF-Token": "upstream-csrf" }),
  unwrap: async (result: { data?: unknown }) => result.data,
}));

import {
  deleteGmailUpstreamMapping,
  listGmailUpstreamMappings,
  saveGmailUpstreamMapping,
} from "./gmail-upstream-api";

describe("Gmail upstream API adapter", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("returns the saved SMSBower mappings", async () => {
    const items = [
      {
        projectId: 3,
        projectName: "GPT",
        providerServiceCode: "gpt",
        providerServiceName: "ChatGPT",
        upstreamPrice: "0.1",
        costPoints: "0.2",
        codePrice: "1",
        purchasePrice: "2",
      },
    ];
    apiMocks.GET.mockResolvedValueOnce({
      data: { items },
    });

    await expect(listGmailUpstreamMappings()).resolves.toEqual(items);
  });

  it("writes the SMSBower-only mapping contract", async () => {
    apiMocks.PUT.mockResolvedValueOnce({ data: undefined });

    await saveGmailUpstreamMapping(42, { providerServiceCode: "gpt" });

    expect(apiMocks.PUT).toHaveBeenCalledWith(
      "/v1/admin/upstreams/smsbower/mappings/{projectId}",
      {
        body: { providerServiceCode: "gpt" },
        params: {
          header: { "X-CSRF-Token": "upstream-csrf" },
          path: { projectId: 42 },
        },
      },
    );
  });

  it("deletes a mapping by project without a source query", async () => {
    apiMocks.DELETE.mockResolvedValueOnce({ data: undefined });

    await deleteGmailUpstreamMapping(42);

    expect(apiMocks.DELETE).toHaveBeenCalledWith(
      "/v1/admin/upstreams/smsbower/mappings/{projectId}",
      {
        params: {
          header: { "X-CSRF-Token": "upstream-csrf" },
          path: { projectId: 42 },
        },
      },
    );
  });
});
