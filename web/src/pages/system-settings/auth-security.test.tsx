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
  SettingsNumberField: ({ label, value }: any) => <input aria-label={label} type="number" value={value} readOnly />,
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
  expect(within(section as HTMLElement).getByLabelText("Client Secret")).toBeDisabled();
  expect(within(section as HTMLElement).getByLabelText("Authorization callback URL")).toBeDisabled();

  fireEvent.click(within(section as HTMLElement).getByRole("button", { name: "保存设置" }));
  await waitFor(() => expect(onBulkSave).toHaveBeenCalledWith([
    { key: "linuxdo_oauth_enabled", value: "false" },
    { key: "linuxdo_minimum_trust_level", value: "0" },
  ]));
});
