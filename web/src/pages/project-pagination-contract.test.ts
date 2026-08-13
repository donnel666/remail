// @ts-expect-error -- Vitest runs this source contract in Node without Node types.
import { readFileSync } from "node:fs";

import { beforeEach, describe, expect, it, vi } from "vitest";

const apiMocks = vi.hoisted(() => ({ GET: vi.fn() }));

vi.mock("../lib/api-client", () => ({
  apiClient: apiMocks,
  csrfHeader: () => ({}),
  turnstileHeader: () => ({}),
  unwrap: async (result: { data?: unknown }) => result.data,
}));

import { listProjects } from "../lib/projects-api";

const adminSource = readFileSync(new URL("./AdminProjects.tsx", import.meta.url), "utf8");
const projectsSource = readFileSync(new URL("./Projects.tsx", import.meta.url), "utf8");

describe("project pagination", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    apiMocks.GET.mockResolvedValue({
      data: { items: [], total: 0, offset: 0, limit: 100 },
    });
  });

  it("loads blocks within the project API's 100-item limit", () => {
    const blockSize = /useBlockPagedList<ProjectItem>\(\{\s*activePage,\s*blockSize:\s*100,/s;
    expect(adminSource).toMatch(blockSize);
    expect(projectsSource).toMatch(blockSize);
  });

  it("clamps oversized project list requests to the API limit", async () => {
    await listProjects({ scope: "all" }, 0, 1_000);

    expect(apiMocks.GET).toHaveBeenCalledWith("/v1/projects", {
      params: { query: { scope: "all", offset: 0, limit: 100 } },
    });
  });
});
