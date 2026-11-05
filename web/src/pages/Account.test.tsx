// @vitest-environment jsdom

import "@testing-library/jest-dom/vitest";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  currentUser: {
    id: 7,
    email: "linuxdo-42@oauth.invalid",
    nickname: "LinuxDo User",
    name: "LinuxDo User",
    role: "user",
    permissions: [],
    qqNumber: "123456789",
    hasLocalPassword: false,
    twoFactorEnabled: false,
    enabled: true,
    createdAt: "2026-07-28T00:00:00Z",
    updatedAt: "2026-07-28T00:00:00Z",
    userGroup: {
      id: 1,
      code: "normal",
      name: "Normal",
      description: "",
      enabled: true,
      apiConcurrencyLimit: 3,
      priceDiscountRatio: "1",
      topupThreshold: "0",
      autoUpgradeEnabled: false,
    },
  },
  getAPIKeyUsage: vi.fn(),
  getLoginConfig: vi.fn(),
  getWallet: vi.fn(),
  disableTwoFactor: vi.fn(),
  enableTwoFactor: vi.fn(),
  setupTwoFactor: vi.fn(),
  logout: vi.fn(),
  navigate: vi.fn(),
  refreshCurrentUser: vi.fn(),
  toastInfo: vi.fn(),
  t: (key: string) => key,
}));

vi.mock("react-i18next", () => ({ useTranslation: () => ({ t: mocks.t }) }));
vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, to }: any) => <a href={to}>{children}</a>,
  useNavigate: () => mocks.navigate,
}));
vi.mock("@/context/auth-provider", () => ({
  useAuth: () => ({
    currentUser: mocks.currentUser,
    logout: mocks.logout,
    refreshCurrentUser: mocks.refreshCurrentUser,
  }),
}));
vi.mock("@/lib/iam-api", () => ({
  changePassword: vi.fn(),
  disableTwoFactor: mocks.disableTwoFactor,
  enableTwoFactor: mocks.enableTwoFactor,
  githubBindURL: "/v1/oauth/github/bind",
  getLoginConfig: mocks.getLoginConfig,
  linuxDOBindURL: "/v1/oauth/linuxdo/bind",
  nodeLocBindURL: "/v1/oauth/nodeloc/bind",
  setupTwoFactor: mocks.setupTwoFactor,
}));
vi.mock("@/lib/openapi-credentials-api", () => ({ getAPIKeyUsage: mocks.getAPIKeyUsage }));
vi.mock("@/lib/wallet-api", () => ({ getWallet: mocks.getWallet }));
vi.mock("@/lib/iam-errors", () => ({
  getIamErrorMessage: (_t: unknown, _error: unknown, fallback: string) => fallback,
}));
vi.mock("@/components/semi/overflow-tooltip", () => ({
  OverflowTooltip: ({ children }: { children: ReactNode }) => <>{children}</>,
}));
vi.mock("@/components/auth/GitHubVerificationModal", () => ({
  GitHubVerificationModal: ({ open }: { open: boolean }) => open ? <div role="dialog" aria-label="GitHub verification" /> : null,
}));
vi.mock("./account/api-key-panel", () => ({ ApiKeyPanel: () => null }));
vi.mock("./account/change-password-dialog", () => ({ ChangePasswordDialog: () => null }));
vi.mock("@douyinfe/semi-icons", () => ({
  IconDelete: () => null,
  IconGithubLogo: () => null,
  IconLock: () => null,
  IconMail: () => null,
}));
vi.mock("@douyinfe/semi-ui", async () => {
  const Box = ({ children }: { children?: ReactNode }) => <div>{children}</div>;
  const Button = ({ children, disabled, loading, onClick }: any) => (
    <button disabled={disabled || loading} onClick={onClick} type="button">
      {children}
    </button>
  );
  const Modal = ({ children, visible }: any) => visible ? <div role="dialog">{children}</div> : null;
  const Input = ({ value, onChange, ...rest }: any) => <input {...rest} value={value} onChange={(event) => onChange?.(event.target.value)} />;
  const Card = ({ children, cover }: any) => <section>{cover}{children}</section>;
  const Tabs = ({ children }: { children?: ReactNode }) => <div>{children}</div>;
  (Tabs as any).TabPane = Box;
  return {
    Avatar: Box,
    Badge: Box,
    Button,
    Card,
    Divider: () => <hr />,
    Input,
    Modal,
    Space: Box,
    Tabs,
    Tag: Box,
    Toast: { info: mocks.toastInfo, success: vi.fn(), error: vi.fn() },
    Typography: {
      Text: Box,
      Title: ({ children }: { children?: ReactNode }) => <h3>{children}</h3>,
    },
  };
});

import Account from "./Account";

describe("Account LinuxDO binding", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    window.history.replaceState({}, "", "/account");
    mocks.currentUser.email = "linuxdo-42@oauth.invalid";
    mocks.currentUser.qqNumber = "123456789";
    mocks.currentUser.hasLocalPassword = false;
    mocks.currentUser.twoFactorEnabled = false;
    mocks.getLoginConfig.mockResolvedValue({ githubOAuthEnabled: false, githubBound: false, linuxdoOAuthEnabled: true, linuxdoBound: false, nodelocOAuthEnabled: false, nodelocBound: false });
    mocks.getWallet.mockResolvedValue({ consumerBalance: "0", historicalSpend: "0" });
    mocks.getAPIKeyUsage.mockResolvedValue({ requestCount: 0 });
    mocks.refreshCurrentUser.mockResolvedValue(mocks.currentUser);
    mocks.setupTwoFactor.mockResolvedValue({ otpauthUri: "otpauth://totp/Remail:test?secret=JBSWY3DPEHPK3PXP&issuer=Remail", expiresAt: "2026-11-05T12:00:00Z" });
    mocks.enableTwoFactor.mockResolvedValue({ enabled: true });
    mocks.disableTwoFactor.mockResolvedValue(undefined);
    mocks.logout.mockResolvedValue(undefined);
  });

  afterEach(() => cleanup());

  it("shows a read-only email and exposes the authenticated bind URL", async () => {
    render(<Account />);

    const bind = await screen.findByRole("link", { name: "Bind" });
    expect(bind).toHaveAttribute("href", "/v1/oauth/linuxdo/bind");
    expect(screen.getByText("linuxdo-42@oauth.invalid")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Change Binding" })).not.toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Set login password" })).toHaveAttribute("href", "/password-reset");
    expect(screen.getByText("Set a password through email verification if you also want to sign in with email and password.")).toBeInTheDocument();
  });

  it("renders the setup QR code in the browser and confirms the authenticator code", async () => {
    render(<Account />);
    fireEvent.click(screen.getByRole("button", { name: "Enable 2FA" }));

    const code = await screen.findByLabelText("Authenticator code");
    expect(screen.getByLabelText("Authenticator setup QR code").tagName).toBe("svg");
    expect(screen.getByLabelText("Authenticator setup key")).toHaveValue("JBSWY3DPEHPK3PXP");
    fireEvent.change(code, { target: { value: "123456" } });
    fireEvent.click(screen.getByRole("button", { name: "Confirm and enable 2FA" }));

    await waitFor(() => expect(mocks.enableTwoFactor).toHaveBeenCalledWith({ code: "123456" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
  });

  it("requires the current authenticator code before disabling protection", async () => {
    mocks.currentUser.twoFactorEnabled = true;
    render(<Account />);
    fireEvent.click(screen.getByRole("button", { name: "Disable 2FA" }));
    const dialog = await screen.findByRole("dialog");
    fireEvent.change(within(dialog).getByLabelText("Authenticator code"), { target: { value: "123456" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Disable 2FA" }));

    await waitFor(() => expect(mocks.disableTwoFactor).toHaveBeenCalledWith({ code: "123456" }));
    expect(mocks.navigate).toHaveBeenCalledWith({ to: "/login", replace: true });
    expect(mocks.setupTwoFactor).not.toHaveBeenCalled();
  });

  it("shows the bound QQ number without a write action", () => {
    render(<Account />);

    const qqCard = screen.getByText("QQ number").closest("section");
    expect(qqCard).not.toBeNull();
    expect(within(qqCard as HTMLElement).getByText("123456789")).toBeInTheDocument();
    expect(within(qqCard as HTMLElement).queryByRole("button")).not.toBeInTheDocument();
    expect(within(qqCard as HTMLElement).queryByRole("link")).not.toBeInTheDocument();
  });

  it("shows the bound state without an email-change action", async () => {
    mocks.getLoginConfig.mockResolvedValue({ githubOAuthEnabled: false, githubBound: false, linuxdoOAuthEnabled: true, linuxdoBound: true, nodelocOAuthEnabled: false, nodelocBound: false });
    render(<Account />);

    expect(await screen.findByRole("button", { name: "Bound" })).toBeDisabled();
    expect(screen.queryByRole("button", { name: "Change Binding" })).not.toBeInTheDocument();
  });

  it("does not report LinuxDO as disabled while configuration is loading", () => {
    mocks.getLoginConfig.mockReturnValue(new Promise(() => undefined));
    render(<Account />);

    const linuxDOCard = screen.getByText("LinuxDO").closest("section");
    expect(linuxDOCard).not.toBeNull();
    expect(within(linuxDOCard as HTMLElement).getByRole("button", { name: "Loading..." })).toBeDisabled();
    expect(within(linuxDOCard as HTMLElement).queryByRole("button", { name: "Not enabled" })).not.toBeInTheDocument();
  });

  it("shows a clear LinuxDO configuration error", async () => {
    mocks.getLoginConfig.mockRejectedValue(new Error("network unavailable"));
    render(<Account />);

    expect(await screen.findByText("Could not load LinuxDO account status. Please try again later.")).toHaveAttribute("role", "alert");
    expect(screen.getAllByRole("button", { name: "Unavailable" })).toHaveLength(3);
  });

  it("keeps password management available for an account with a real email", async () => {
    mocks.currentUser.email = "user@example.com";
    mocks.currentUser.hasLocalPassword = true;
    render(<Account />);

    expect(await screen.findByRole("link", { name: "Bind" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Change password" })).toBeEnabled();
  });

  it("exposes the authenticated GitHub bind URL when enabled", async () => {
    mocks.getLoginConfig.mockResolvedValue({ githubOAuthEnabled: true, githubBound: false, linuxdoOAuthEnabled: false, linuxdoBound: false, nodelocOAuthEnabled: false, nodelocBound: false });

    render(<Account />);

    expect(await screen.findByRole("link", { name: "Bind" })).toHaveAttribute("href", "/v1/oauth/github/bind");
  });

  it("opens GitHub mailbox verification after the binding callback", async () => {
    window.history.replaceState({}, "", "/account?oauth_setup=github");

    render(<Account />);

    expect(await screen.findByRole("dialog", { name: "GitHub verification" })).toBeInTheDocument();
  });

  it("ignores unknown success notices and sanitizes unknown OAuth errors", async () => {
    window.history.replaceState({}, "", "/account?oauth_notice=toString&oauth_error=toString");

    render(<Account />);

    expect(await screen.findByRole("alert")).toHaveTextContent("LinuxDO login failed.");
    expect(screen.queryByRole("status")).not.toBeInTheDocument();
    expect(window.location.search).toBe("");
  });
});
