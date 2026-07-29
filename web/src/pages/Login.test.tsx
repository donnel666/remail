// @vitest-environment jsdom

import "@testing-library/jest-dom/vitest";
import { cleanup, render, screen, within } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  completeLinuxDO: vi.fn(),
  getLinuxDOPending: vi.fn(),
  getLoginConfig: vi.fn(),
  login: vi.fn(),
  navigate: vi.fn(),
  refreshCurrentUser: vi.fn(),
  sendLinuxDOEmailCode: vi.fn(),
  t: (key: string) => key,
}));

vi.mock("@tanstack/react-router", () => ({
  Link: ({ children }: { children: ReactNode }) => <a href="#">{children}</a>,
  useNavigate: () => mocks.navigate,
}));
vi.mock("react-i18next", () => ({ useTranslation: () => ({ t: mocks.t }) }));
vi.mock("@douyinfe/semi-ui", () => ({
  Button: ({ children, disabled, loading, onClick }: any) => (
    <button disabled={disabled || loading} onClick={onClick} type="button">{children}</button>
  ),
  Modal: ({ children, footer, title, visible }: any) => visible ? (
    <div role="dialog" aria-label={title}>{children}{footer}</div>
  ) : null,
  Radio: ({ children, disabled, value }: any) => (
    <label><input disabled={disabled} readOnly type="radio" value={value} />{children}</label>
  ),
  RadioGroup: ({ children }: any) => <div>{children}</div>,
}));
vi.mock("@/components/auth/SendCodeField", () => ({
  SendCodeField: ({ code, onCodeChange }: any) => (
    <input aria-label="Verification code" onChange={(event) => onCodeChange(event.target.value)} value={code} />
  ),
}));
vi.mock("@/components/auth/TurnstileField", () => ({ TurnstileField: () => null }));
vi.mock("@/context/auth-provider", () => ({
  useAuth: () => ({ login: mocks.login, refreshCurrentUser: mocks.refreshCurrentUser }),
}));
vi.mock("@/lib/iam-errors", () => ({ getIamErrorMessage: (_t: unknown, _error: unknown, fallback: string) => fallback }));
vi.mock("@/lib/iam-api", () => ({
  completeLinuxDO: mocks.completeLinuxDO,
  getLinuxDOPending: mocks.getLinuxDOPending,
  getLoginConfig: mocks.getLoginConfig,
  linuxDOLoginURL: "/v1/oauth/linuxdo",
  sendLinuxDOEmailCode: mocks.sendLinuxDOEmailCode,
}));

import Login from "./Login";

beforeEach(() => {
  mocks.getLoginConfig.mockResolvedValue({ linuxdoOAuthEnabled: true });
  mocks.getLinuxDOPending.mockResolvedValue({
    provider: "linuxdo",
    providerUserId: "42",
    username: "linuxdo-user",
    suggestedEmail: "owner@qq.com",
    suggestedEmailExists: true,
    registrationEnabled: true,
    legacyAccount: false,
  });
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

it("asks first-time LinuxDO users to bind or create a verified email account", async () => {
  window.history.replaceState({}, "", "/login?oauth_setup=linuxdo");

  render(<Login />);

  expect(await screen.findByRole("dialog", { name: "Finish LinuxDO sign-in" })).toBeInTheDocument();
  expect(screen.getByDisplayValue("owner@qq.com")).toBeInTheDocument();
  expect(screen.getByText("Bind existing account")).toBeInTheDocument();
  expect(screen.getByText("Create new account")).toBeInTheDocument();
});

it("only allows an in-place email upgrade for legacy LinuxDO accounts", async () => {
  mocks.getLinuxDOPending.mockResolvedValue({
    provider: "linuxdo",
    providerUserId: "42",
    username: "linuxdo-user",
    suggestedEmail: "owner@qq.com",
    suggestedEmailExists: true,
    registrationEnabled: true,
    legacyAccount: true,
  });
  window.history.replaceState({}, "", "/login?oauth_setup=linuxdo");

  render(<Login />);

  const dialog = await screen.findByRole("dialog", { name: "Finish LinuxDO sign-in" });
  expect(screen.getByRole("radio", { name: "Bind existing account" })).toBeDisabled();
  expect(screen.getByRole("radio", { name: "Upgrade current account" })).toBeEnabled();
  expect(screen.getByText("This legacy LinuxDO account already contains site data. Verify a new email to keep this account, balance, orders, and resources.")).toBeInTheDocument();
  expect(within(dialog).getByLabelText("Email")).toHaveValue("");
});
