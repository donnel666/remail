// @vitest-environment jsdom

import "@testing-library/jest-dom/vitest";
import { cleanup, render, screen } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  getLoginConfig: vi.fn(),
  login: vi.fn(),
  navigate: vi.fn(),
  t: (key: string) => key,
}));

vi.mock("@tanstack/react-router", () => ({
  Link: ({ children }: { children: ReactNode }) => <a href="#">{children}</a>,
  useNavigate: () => mocks.navigate,
}));
vi.mock("react-i18next", () => ({ useTranslation: () => ({ t: mocks.t }) }));
vi.mock("@/components/auth/TurnstileField", () => ({ TurnstileField: () => null }));
vi.mock("@/context/auth-provider", () => ({ useAuth: () => ({ login: mocks.login }) }));
vi.mock("@/lib/iam-errors", () => ({ getIamErrorMessage: (_t: unknown, _error: unknown, fallback: string) => fallback }));
vi.mock("@/lib/iam-api", () => ({
  getLoginConfig: mocks.getLoginConfig,
  linuxDOLoginURL: "/v1/oauth/linuxdo",
}));

import Login from "./Login";

beforeEach(() => {
  mocks.getLoginConfig.mockResolvedValue({ linuxdoOAuthEnabled: true });
  window.history.replaceState({}, "", "/login?oauth_error=trust_level");
});

afterEach(() => {
  cleanup();
  sessionStorage.clear();
  vi.clearAllMocks();
});

it("shows LinuxDO before the password form and consumes callback errors", async () => {
  render(<Login />);

  expect(await screen.findByRole("alert")).toHaveTextContent("Your LinuxDO trust level is too low.");
  const linuxDOButton = await screen.findByRole("button", { name: "Continue with LinuxDO" });
  const email = screen.getByLabelText("Email");
  expect(linuxDOButton.compareDocumentPosition(email) & Node.DOCUMENT_POSITION_FOLLOWING).not.toBe(0);
  expect(window.location.search).toBe("");
});

it("shows an explicit loading state while login configuration is pending", () => {
  mocks.getLoginConfig.mockReturnValue(new Promise(() => undefined));
  window.history.replaceState({}, "", "/login");

  render(<Login />);

  expect(screen.getByRole("button", { name: "Loading LinuxDO login..." })).toBeDisabled();
  expect(screen.queryByRole("button", { name: "Continue with LinuxDO" })).not.toBeInTheDocument();
});

it("reports LinuxDO configuration load failures without hiding password login", async () => {
  mocks.getLoginConfig.mockRejectedValue(new Error("network unavailable"));
  window.history.replaceState({}, "", "/login");

  render(<Login />);

  expect(await screen.findByText("Could not load LinuxDO login settings. Please refresh and try again.")).toHaveAttribute("role", "alert");
  expect(screen.getByLabelText("Email")).toBeInTheDocument();
});

it("uses the generic error for unknown OAuth callback codes", async () => {
  window.history.replaceState({}, "", "/login?oauth_error=toString");

  render(<Login />);

  expect(await screen.findByRole("alert")).toHaveTextContent("LinuxDO login failed.");
  expect(window.location.search).toBe("");
});
