// @vitest-environment jsdom

import "@testing-library/jest-dom/vitest";
import { cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import type { AnchorHTMLAttributes, ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({ mobile: true }));

vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, onClick, to, ...props }: AnchorHTMLAttributes<HTMLAnchorElement> & {
    children: ReactNode;
    to: string;
  }) => (
    <a
      href={to}
      onClick={(event) => {
        event.preventDefault();
        onClick?.(event);
      }}
      {...props}
    >
      {children}
    </a>
  ),
  useLocation: () => ({ pathname: "/console" }),
  useRouterState: () => ({ location: { pathname: "/console" } }),
}));

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (key: string) => key }),
}));

vi.mock("@/context/auth-provider", () => ({
  permissionKey: (resource: string, action: string) => `${resource}:${action}`,
  useAuth: () => ({ currentUser: null }),
}));

vi.mock("@/hooks/use-is-mobile", () => ({ useIsMobile: () => mocks.mobile }));

vi.mock("@/components/language-menu", () => ({ LanguageMenu: () => null }));
vi.mock("@/components/notification-popover", () => ({ NotificationPopover: () => null }));
vi.mock("@/components/theme-switch", () => ({ ThemeSwitch: () => null }));
vi.mock("@/components/user-menu", () => ({ UserMenu: () => null }));
vi.mock("@/components/header-wallet-shortcut", () => ({ HeaderWalletShortcut: () => null }));
vi.mock("@/lib/auth-flow", () => ({ clearLoginReturnTo: vi.fn() }));
vi.mock("./components/header-logo", () => ({ HeaderLogo: () => null }));

import AppShell from "./AppShell";

describe("AppShell mobile navigation", () => {
  beforeEach(() => {
    mocks.mobile = true;
  });

  afterEach(() => cleanup());

  it("keeps desktop collapse working after opening the mobile sidebar", () => {
    const view = render(<AppShell><p>Page content</p></AppShell>);

    const toggle = screen.getByRole("button", { name: "Expand sidebar" });
    const sidebar = document.getElementById("app-sidebar");

    expect(toggle).toHaveAttribute("aria-expanded", "false");
    expect(sidebar).toHaveClass("hidden");

    fireEvent.click(toggle);

    expect(toggle).toHaveAttribute("aria-expanded", "true");
    expect(sidebar).toHaveClass("fixed", "flex");

    mocks.mobile = false;
    view.rerender(<AppShell><p>Page content</p></AppShell>);

    fireEvent.click(within(sidebar!).getByRole("button", { name: "Collapse sidebar" }));
    expect(sidebar).toHaveClass("lg:w-16");

    fireEvent.click(screen.getByRole("link", { name: "Workbench" }));

    expect(toggle).toHaveAttribute("aria-expanded", "false");
    expect(sidebar).toHaveClass("hidden");
  });
});
