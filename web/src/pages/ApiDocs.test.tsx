// @vitest-environment jsdom

import { cleanup, render, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const { authState } = vi.hoisted(() => ({
  authState: { currentUser: null as { id: number } | null },
}));

vi.mock("@tanstack/react-router", () => ({
  useNavigate: () => vi.fn(),
}));

vi.mock("@/context/auth-provider", () => ({
  useAuth: () => ({ currentUser: authState.currentUser }),
}));

vi.mock("@/lib/openapi-credentials-api", () => ({
  listAPIKeys: vi.fn(),
}));

vi.mock("./api-docs/assets", () => ({
  loadRuntimeOpenApiSpec: vi.fn(async () => ({})),
  loadSwaggerAssets: vi.fn(async () => undefined),
}));

vi.mock("./api-docs/swagger-zh-cn", () => ({
  installSwaggerZhCn: vi.fn(() => ({
    dispose: vi.fn(),
    setApiKeys: vi.fn(),
  })),
}));

import { listAPIKeys } from "@/lib/openapi-credentials-api";

import ApiDocs from "./ApiDocs";
import { loadRuntimeOpenApiSpec } from "./api-docs/assets";

describe("ApiDocs", () => {
  beforeEach(() => {
    authState.currentUser = null;
  });

  afterEach(() => {
    cleanup();
    vi.clearAllMocks();
  });

  it("does not request API keys for an unauthenticated visitor", async () => {
    render(<ApiDocs />);

    await waitFor(() => {
      expect(loadRuntimeOpenApiSpec).toHaveBeenCalledOnce();
    });

    expect(listAPIKeys).not.toHaveBeenCalled();
  });

  it("loads API keys for an authenticated visitor", async () => {
    authState.currentUser = { id: 1 };
    vi.mocked(listAPIKeys).mockResolvedValue({ items: [] } as never);

    render(<ApiDocs />);

    await waitFor(() => {
      expect(listAPIKeys).toHaveBeenCalledWith({ limit: 100, offset: 0 });
    });
  });
});
