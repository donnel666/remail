// @vitest-environment jsdom

import "@testing-library/jest-dom/vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  getSystemOptions: vi.fn(),
  toastError: vi.fn(),
  t: (key: string) => key,
}));

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: mocks.t }),
}));

vi.mock("@douyinfe/semi-ui", () => ({
  Button: ({ children, onClick }: any) => <button onClick={onClick} type="button">{children}</button>,
  Spin: ({ children }: any) => <>{children}</>,
  TabPane: ({ children }: any) => <>{children}</>,
  Tabs: ({ children }: any) => <>{children}</>,
  Toast: { error: mocks.toastError },
}));

vi.mock("@/context/auth-provider", () => ({
  hasPermissionKey: (user: { permissions: string[] }, key: string) => user.permissions.includes(key),
  useAuth: () => ({
    currentUser: {
      permissions: [
        "system:settings:read",
        "system:settings:write",
        "iam:user_group:read",
        "iam:user_group:write",
      ],
    },
  }),
}));

vi.mock("@/lib/system-settings-api", () => ({
  getSystemOptions: mocks.getSystemOptions,
  updateSystemOption: vi.fn(),
  updateSystemOptionsBulk: vi.fn(),
}));

vi.mock("./settings-layout", () => ({
  SettingsAccessBoundary: ({ children }: any) => <>{children}</>,
}));

vi.mock("./site-content", () => ({ default: () => <div data-testid="settings-section" /> }));
vi.mock("./auth-security", () => ({ default: () => <div data-testid="settings-section" /> }));
vi.mock("./email-service", () => ({ default: () => <div data-testid="settings-section" /> }));
vi.mock("./orders-payment", () => ({ default: () => <div data-testid="settings-section" /> }));
vi.mock("./system-operations", () => ({ default: () => <div data-testid="settings-section" /> }));
vi.mock("./users-rebates", () => ({ default: () => <div data-testid="settings-section" /> }));

import SystemSettingsPage from "./index";

describe("SystemSettingsPage", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  afterEach(() => cleanup());

  it("does not mount editable defaults after loading fails and retries explicitly", async () => {
    mocks.getSystemOptions
      .mockRejectedValueOnce(new Error("network unavailable"))
      .mockResolvedValueOnce({ options: [] });

    render(<SystemSettingsPage />);

    expect(await screen.findByText("系统设置加载失败：network unavailable")).toBeInTheDocument();
    expect(screen.queryByTestId("settings-section")).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "重试" }));

    await waitFor(() => {
      expect(screen.getAllByTestId("settings-section").length).toBeGreaterThan(0);
    });
    expect(mocks.getSystemOptions).toHaveBeenCalledTimes(2);
  });
});
