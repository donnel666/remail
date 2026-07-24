// @vitest-environment jsdom

import "@testing-library/jest-dom/vitest";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  t: (key: string) => key,
}));

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: mocks.t }),
}));

vi.mock("@douyinfe/semi-ui", () => ({
  Button: ({ children, disabled, loading, onClick }: any) => (
    <button disabled={disabled || loading} onClick={onClick} type="button">{children}</button>
  ),
  DatePicker: () => <input />,
  Divider: () => <hr />,
  Empty: () => <div />,
  Input: () => <input />,
  InputNumber: () => <input type="number" />,
  Modal: ({ children, visible }: any) => visible ? <div>{children}</div> : null,
  Select: () => <select />,
  Space: ({ children }: any) => <div>{children}</div>,
  Switch: ({ "aria-label": ariaLabel, checked, onChange }: any) => (
    <button
      aria-checked={checked}
      aria-label={ariaLabel}
      onClick={() => onChange(!checked)}
      role="switch"
      type="button"
    />
  ),
  Table: () => <div />,
  Tag: ({ children }: any) => <span>{children}</span>,
  TextArea: () => <textarea />,
  Toast: { warning: vi.fn() },
  Tooltip: ({ children }: any) => <>{children}</>,
  Typography: { Text: ({ children }: any) => <span>{children}</span> },
}));

vi.mock("@douyinfe/semi-illustrations", () => ({
  IllustrationNoResult: () => null,
  IllustrationNoResultDark: () => null,
}));

vi.mock("./settings-layout", () => ({
  SettingsFormGrid: ({ children }: any) => <div>{children}</div>,
  SettingsSection: ({ children, title }: any) => <section>{title}{children}</section>,
  SettingsSwitchField: ({ checked, label, onChange }: any) => (
    <button aria-checked={checked} aria-label={label} onClick={() => onChange(!checked)} role="switch" type="button" />
  ),
  SettingsTextareaField: () => <textarea />,
}));

import SiteContentSection from "./site-content";

const baseProps = {
  canReadUserGroups: true,
  canSensitive: true,
  canWrite: true,
  canWriteUserGroups: true,
  loading: false,
  onSave: vi.fn(),
  options: [
    { key: "announcement_enabled", value: "true" },
    { key: "faq_enabled", value: "true" },
  ],
};

describe("SiteContentSection save failures", () => {
  afterEach(() => cleanup());

  it("keeps FAQ changes dirty and rolls back an announcement switch when saving fails", async () => {
    const onBulkSave = vi.fn().mockRejectedValue(new Error("save failed"));
    render(<SiteContentSection {...baseProps} onBulkSave={onBulkSave} />);

    const announcementSwitch = screen.getByRole("switch", { name: "系统公告开关" });
    fireEvent.click(announcementSwitch);
    await waitFor(() => expect(announcementSwitch).toHaveAttribute("aria-checked", "true"));

    const faqSwitch = screen.getByRole("switch", { name: "常见问答开关" });
    fireEvent.click(faqSwitch);
    const faqSection = screen.getByText(/常见问答管理/).closest("section");
    expect(faqSection).not.toBeNull();
    const saveButton = within(faqSection as HTMLElement).getByRole("button", { name: "保存设置" });
    expect(saveButton).toBeEnabled();

    fireEvent.click(saveButton);
    await waitFor(() => expect(onBulkSave).toHaveBeenCalledTimes(2));
    expect(saveButton).toBeEnabled();
  });
});
