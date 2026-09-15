// @vitest-environment jsdom
import "@testing-library/jest-dom/vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import type { ButtonHTMLAttributes, ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const deviceMocks = vi.hoisted(() => ({ balance: vi.fn(), recharge: vi.fn(), success: vi.fn() }));
vi.mock("@/lib/admin-icloud-api", () => ({ getAdminICloudDeviceBalance: deviceMocks.balance, rechargeAdminICloudDevice: deviceMocks.recharge }));

const translate = (key: string) => key;
vi.mock("react-i18next", () => ({ useTranslation: () => ({ t: translate }) }));
vi.mock("@douyinfe/semi-ui", () => ({
  Button: ({ children, disabled, onClick }: ButtonHTMLAttributes<HTMLButtonElement>) => <button disabled={disabled} onClick={onClick}>{children}</button>,
  Typography: { Text: ({ children }: { children: ReactNode }) => <span>{children}</span> },
  Toast: { success: deviceMocks.success },
}));
vi.mock("./settings-layout", () => ({
  SettingsAccessBoundary: ({ children, canWrite }: { children: ReactNode; canWrite: boolean }) => <fieldset disabled={!canWrite}>{children}</fieldset>,
  SettingsCardHeader: () => null,
  SettingsSection: ({ children }: { children: ReactNode }) => <section>{children}</section>,
  SettingsFormGrid: ({ children }: { children: ReactNode }) => <div>{children}</div>,
  SettingsTextField: ({ label, value, onChange, type, disabled }: { label: string; value: string; onChange: (value: string) => void; type?: string; disabled?: boolean }) => <label>{label}<input type={type} value={value} disabled={disabled} onChange={(e) => onChange(e.target.value)} /></label>,
  SettingsNumberField: ({ label, value, onChange }: { label: string; value: number; onChange: (value: number) => void }) => <label>{label}<input type="number" value={value} onChange={(e) => onChange(Number(e.target.value))} /></label>,
}));

import { AppleDeviceSettings, KitesimSMSSettings } from "./upstreams";

afterEach(cleanup);
beforeEach(() => {
  vi.clearAllMocks();
  deviceMocks.balance.mockReset().mockResolvedValue({ configured: false, balance: null, checkedAt: null });
  deviceMocks.recharge.mockReset();
});

describe("Kitesim SMS receipt window", () => {
  it("starts at 120 seconds, rejects invalid values and saves the configured window", async () => {
    const save = vi.fn().mockResolvedValue(undefined);
    render(<KitesimSMSSettings options={[]} onSave={save} canWrite />);
    const input = screen.getByRole("spinbutton", { name: "SMS receipt window (seconds)" });
    const button = screen.getByRole("button", { name: "Save settings" });
    expect(input).toHaveValue(120);
    expect(button).toBeDisabled();
    for (const value of ["0", "-1", "1.5", "86401"]) {
      fireEvent.change(input, { target: { value } });
      expect(button).toBeDisabled();
    }
    fireEvent.change(input, { target: { value: "300" } });
    fireEvent.click(button);
    await waitFor(() => expect(save).toHaveBeenCalledWith("kitesim_sms_window_seconds", "300"));
  });

  it("shows the saved window and disables editing without write permission", () => {
    render(<KitesimSMSSettings options={[{ key: "kitesim_sms_window_seconds", value: "300" }]} onSave={vi.fn()} canWrite={false} />);
    expect(screen.getByRole("spinbutton")).toHaveValue(300);
    expect(screen.getByRole("spinbutton")).toBeDisabled();
    expect(screen.getByRole("button", { name: "Save settings" })).toBeDisabled();
  });
});

describe("Apple device settings", () => {
  it("shows zero points and replaces the balance after card redemption", async () => {
    deviceMocks.balance.mockResolvedValue({ configured: true, balance: "0", checkedAt: "2026-09-15T06:06:31Z" });
    deviceMocks.recharge.mockResolvedValue({ configured: true, balance: "1000", checkedAt: "2026-09-15T06:07:00Z" });
    render(<AppleDeviceSettings options={[]} onBulkSave={vi.fn()} onSave={vi.fn()} canWrite canSensitive loading={false} canReadUserGroups={false} canWriteUserGroups={false} />);
    await screen.findByText("Remaining tai points: 0");
    fireEvent.change(screen.getByLabelText("Device recharge card"), { target: { value: " CARD-001 " } });
    fireEvent.click(screen.getByRole("button", { name: "Redeem tai points" }));
    await screen.findByText("Remaining tai points: 1000");
    expect(deviceMocks.recharge).toHaveBeenCalledExactlyOnceWith("CARD-001");
    expect(screen.getByLabelText("Device recharge card")).toHaveValue("");
  });

  it("allows balance visibility but disables redemption without sensitive permission", async () => {
    deviceMocks.balance.mockResolvedValue({ configured: true, balance: "1000", checkedAt: null });
    render(<AppleDeviceSettings options={[]} onBulkSave={vi.fn()} onSave={vi.fn()} canWrite canSensitive={false} loading={false} canReadUserGroups={false} canWriteUserGroups={false} />);
    await screen.findByText("Remaining tai points: 1000");
    expect(screen.getByLabelText("Device recharge card")).toBeDisabled();
    expect(screen.getByRole("button", { name: "Redeem tai points" })).toBeDisabled();
    expect(deviceMocks.recharge).not.toHaveBeenCalled();
  });

  it("preserves an existing key when left blank and clears a newly saved key from the form", async () => {
    const save = vi.fn().mockResolvedValue(undefined);
    render(<AppleDeviceSettings options={[]} onBulkSave={save} onSave={vi.fn()} canWrite canSensitive loading={false} canReadUserGroups={false} canWriteUserGroups={false} />);
    fireEvent.click(screen.getByRole("button", { name: "Save settings" }));
    await waitFor(() => expect(save).toHaveBeenCalledTimes(1));
    expect(save.mock.calls[0][0].some((value: {key: string}) => value.key === "icloud_device_api_key")).toBe(false);
    await waitFor(() => expect(screen.getByRole("button", { name: "Save settings" })).toBeEnabled());
    fireEvent.change(screen.getByLabelText("API Key"), { target: { value: "new-secret" } });
    fireEvent.click(screen.getByRole("button", { name: "Save settings" }));
    await waitFor(() => expect(save).toHaveBeenCalledTimes(2));
    expect(save.mock.calls[1][0]).toContainEqual({ key: "icloud_device_api_key", value: "new-secret" });
    await waitFor(() => expect(screen.getByLabelText("API Key")).toHaveValue(""));
  });
});
