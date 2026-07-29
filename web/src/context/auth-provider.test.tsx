// @vitest-environment jsdom

import "@testing-library/jest-dom/vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => {
  class IamApiError extends Error {
    constructor(readonly status: number) {
      super("Request failed.");
    }
  }

  return {
    clearBrowserAuthState: vi.fn(),
    getMe: vi.fn(),
    IamApiError,
    login: vi.fn(),
    logout: vi.fn(),
  };
});

vi.mock("@/lib/iam-api", () => ({
  getMe: mocks.getMe,
  IamApiError: mocks.IamApiError,
  login: mocks.login,
  logout: mocks.logout,
}));
vi.mock("@/lib/auth-flow", () => ({
  AUTH_REQUIRED_EVENT: "remail-auth-required",
  clearBrowserAuthState: mocks.clearBrowserAuthState,
}));

import { AuthProvider, useAuth } from "./auth-provider";

const user = {
  id: 7,
  email: "user@example.com",
  nickname: "User",
  role: "user",
  userGroup: {
    id: 1,
    code: "normal",
    name: "Normal",
    description: "",
    enabled: true,
    apiConcurrencyLimit: 1,
    priceDiscountRatio: "1",
    topupThreshold: "0",
    autoUpgradeEnabled: false,
  },
  permissions: [],
  hasLocalPassword: true,
  enabled: true,
  createdAt: "2026-07-29T00:00:00Z",
  updatedAt: "2026-07-29T00:00:00Z",
};

function Probe() {
  const {
    authenticationError,
    currentUser,
    loading,
    logout,
    retryCurrentUser,
  } = useAuth();
  return (
    <>
      <span>
        {loading
          ? "loading"
          : authenticationError
            ? "auth-error"
            : currentUser?.email ?? "anonymous"}
      </span>
      <button onClick={() => void logout().catch(() => undefined)} type="button">
        logout
      </button>
      <button onClick={() => void retryCurrentUser()} type="button">
        retry
      </button>
    </>
  );
}

describe("AuthProvider", () => {
  beforeEach(() => vi.clearAllMocks());
  afterEach(() => cleanup());

  it("keeps transient profile failures retryable without becoming anonymous", async () => {
    mocks.getMe
      .mockRejectedValueOnce(new mocks.IamApiError(503))
      .mockResolvedValueOnce({ user });

    render(<AuthProvider><Probe /></AuthProvider>);

    expect(await screen.findByText("auth-error")).toBeInTheDocument();
    expect(mocks.clearBrowserAuthState).not.toHaveBeenCalled();

    fireEvent.click(screen.getByRole("button", { name: "retry" }));

    expect(await screen.findByText(user.email)).toBeInTheDocument();
    expect(mocks.getMe).toHaveBeenCalledTimes(2);
  });

  it("clears browser authentication state on an unauthorized profile", async () => {
    mocks.getMe.mockRejectedValue(new mocks.IamApiError(401));

    render(<AuthProvider><Probe /></AuthProvider>);

    expect(await screen.findByText("anonymous")).toBeInTheDocument();
    expect(mocks.clearBrowserAuthState).toHaveBeenCalledOnce();
  });

  it("keeps the current user when the logout request fails", async () => {
    mocks.getMe.mockResolvedValue({ user });
    mocks.logout.mockRejectedValue(new Error("network unavailable"));
    render(<AuthProvider><Probe /></AuthProvider>);
    expect(await screen.findByText(user.email)).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "logout" }));

    await waitFor(() => expect(mocks.logout).toHaveBeenCalledOnce());
    expect(screen.getByText(user.email)).toBeInTheDocument();
    expect(mocks.clearBrowserAuthState).not.toHaveBeenCalled();
  });

  it("clears local authentication when the logout endpoint reports cleanup failure", async () => {
    mocks.getMe.mockResolvedValue({ user });
    mocks.logout.mockRejectedValue(new mocks.IamApiError(500));
    render(<AuthProvider><Probe /></AuthProvider>);
    expect(await screen.findByText(user.email)).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "logout" }));

    expect(await screen.findByText("anonymous")).toBeInTheDocument();
    expect(mocks.clearBrowserAuthState).toHaveBeenCalledOnce();
  });

  it("clears browser and in-memory authentication after logout succeeds", async () => {
    mocks.getMe.mockResolvedValue({ user });
    mocks.logout.mockResolvedValue(undefined);
    render(<AuthProvider><Probe /></AuthProvider>);
    expect(await screen.findByText(user.email)).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "logout" }));

    expect(await screen.findByText("anonymous")).toBeInTheDocument();
    expect(mocks.clearBrowserAuthState).toHaveBeenCalledOnce();
  });
});
