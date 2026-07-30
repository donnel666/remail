// @vitest-environment jsdom

import "@testing-library/jest-dom/vitest";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, expect, it, vi } from "vitest";

vi.mock("react-i18next", () => ({ useTranslation: () => ({ t: (key: string) => key }) }));
vi.mock("@/components/auth/LinuxDoIcon", () => ({ LinuxDoIcon: () => null }));
vi.mock("@/lib/clipboard", () => ({ copyText: vi.fn() }));
vi.mock("@douyinfe/semi-ui", () => ({
  Button: ({ children, disabled, loading, onClick }: any) => (
    <button disabled={disabled || loading} onClick={onClick} type="button">{children}</button>
  ),
  TagInput: () => <input />,
  Toast: { error: vi.fn(), success: vi.fn() },
}));
vi.mock("./settings-layout", () => ({
  FormItem: ({ children }: { children: ReactNode }) => <div>{children}</div>,
  FormLabel: ({ children }: { children: ReactNode }) => <span>{children}</span>,
  SettingsCardHeader: ({ title }: { title: string }) => <h2>{title}</h2>,
  SettingsFormGrid: ({ children }: { children: ReactNode }) => <div>{children}</div>,
  SettingsNumberField: ({ label, precision, step, value }: any) => <input aria-label={label} data-precision={precision} step={step} type="number" value={value} readOnly />,
  SettingsSection: ({ children, title }: { children: ReactNode; title: ReactNode }) => <section>{title}{children}</section>,
  SettingsSwitchField: ({ label }: { label: string }) => <button aria-label={label} role="switch" type="button" />,
  SettingsTextField: ({ disabled, label, value }: any) => <input aria-label={label} disabled={disabled} value={value} readOnly />,
}));

import AuthSecuritySection from "./auth-security";

afterEach(() => cleanup());

it("does not submit LinuxDO OAuth app credentials without sensitive permission", async () => {
  const onBulkSave = vi.fn().mockResolvedValue(undefined);
  render(<AuthSecuritySection
    canReadUserGroups
    canSensitive={false}
    canWrite
    canWriteUserGroups
    loading={false}
    onBulkSave={onBulkSave}
    onSave={vi.fn()}
    options={[
      { key: "linuxdo_client_id", value: "client-id" },
      { key: "linuxdo_callback_url", value: "https://mail.example.com/v1/oauth/linuxdo/callback" },
    ]}
  />);

  const section = screen.getByRole("heading", { name: "LinuxDO third-party login" }).closest("section");
  expect(section).not.toBeNull();
  expect(within(section as HTMLElement).getByLabelText("Client ID")).toBeDisabled();
  expect(within(section as HTMLElement).getByLabelText("Client ID")).toHaveValue("");
  expect(within(section as HTMLElement).getByLabelText("Client Secret")).toBeDisabled();
  expect(within(section as HTMLElement).getByLabelText("Authorization callback URL")).toBeDisabled();

  fireEvent.click(within(section as HTMLElement).getByRole("button", { name: "保存设置" }));
  await waitFor(() => expect(onBulkSave).toHaveBeenCalledWith([
    { key: "linuxdo_oauth_enabled", value: "false" },
    { key: "linuxdo_minimum_trust_level", value: "0" },
  ]));
});

it("keeps write-only OAuth client IDs blank and submits only newly entered credentials", async () => {
  const onBulkSave = vi.fn().mockResolvedValue(undefined);
  render(<AuthSecuritySection
    canReadUserGroups
    canSensitive
    canWrite
    canWriteUserGroups
    loading={false}
    onBulkSave={onBulkSave}
    onSave={vi.fn()}
    options={[
      { key: "github_client_id", value: "must-not-render" },
      { key: "github_client_secret", value: "must-not-render" },
      { key: "github_callback_url", value: "https://mail.example.com/v1/oauth/github/callback" },
    ]}
  />);

  const section = screen.getByRole("heading", { name: "GitHub third-party login" }).closest("section");
  expect(section).not.toBeNull();
  expect(within(section as HTMLElement).getByLabelText("Client ID")).toHaveValue("");
  expect(within(section as HTMLElement).getByLabelText("Client Secret")).toHaveValue("");

  fireEvent.click(within(section as HTMLElement).getByRole("button", { name: "保存设置" }));
  await waitFor(() => expect(onBulkSave).toHaveBeenCalled());
  const updates = onBulkSave.mock.calls[0][0] as Array<{ key: string; value: string }>;
  expect(updates.some(({ key }) => key === "github_client_id" || key === "github_client_secret")).toBe(false);
});

it("configures GitHub minimum account age as whole days", () => {
  render(<AuthSecuritySection
    canReadUserGroups
    canSensitive
    canWrite
    canWriteUserGroups
    loading={false}
    onBulkSave={vi.fn()}
    onSave={vi.fn()}
    options={[]}
  />);

  expect(screen.getByLabelText("Minimum GitHub account age (days)")).toHaveAttribute("data-precision", "0");
  expect(screen.getByLabelText("Minimum GitHub account age (days)")).toHaveAttribute("step", "1");
});
