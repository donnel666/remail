// @vitest-environment jsdom
import "@testing-library/jest-dom/vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import type { ButtonHTMLAttributes, ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";

const translate = (key: string) => key;
vi.mock("react-i18next", () => ({ useTranslation: () => ({ t: translate }) }));
vi.mock("@douyinfe/semi-ui", () => ({
  Button: ({ children, disabled, onClick }: ButtonHTMLAttributes<HTMLButtonElement>) => <button disabled={disabled} onClick={onClick}>{children}</button>,
  Typography: { Text: ({ children }: { children: ReactNode }) => <span>{children}</span> },
}));
vi.mock("./settings-layout", () => ({
  SettingsAccessBoundary: ({ children, canWrite }: { children: ReactNode; canWrite: boolean }) => <fieldset disabled={!canWrite}>{children}</fieldset>,
  SettingsCardHeader: () => null,
  SettingsSection: ({ children }: { children: ReactNode }) => <section>{children}</section>,
  SettingsFormGrid: ({ children }: { children: ReactNode }) => <div>{children}</div>,
  SettingsNumberField: ({ label, value, onChange }: { label: string; value: number; onChange: (value: number) => void }) => <label>{label}<input type="number" value={value} onChange={(e) => onChange(Number(e.target.value))} /></label>,
}));

import { KitesimSMSSettings } from "./upstreams";

afterEach(cleanup);

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
