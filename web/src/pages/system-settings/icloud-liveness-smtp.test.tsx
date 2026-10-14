// @vitest-environment jsdom
import "@testing-library/jest-dom/vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({ error: vi.fn(), success: vi.fn(), testSMTP: vi.fn() }));
vi.mock("@/lib/admin-icloud-api", () => ({ testAdminICloudSMTP: mocks.testSMTP }));
vi.mock("react-i18next", () => ({ useTranslation: () => ({ t: (key: string, values?: Record<string, string | number>) => key.replace(/{{(\w+)}}/g, (match, name: string) => String(values?.[name] ?? match)) }) }));
vi.mock("@douyinfe/semi-ui", () => ({
  Banner: ({ description }: { description: string }) => <div role="status">{description}</div>,
  Button: ({ children, disabled, loading, onClick, ...props }: any) => <button aria-label={props["aria-label"]} disabled={disabled || loading} onClick={onClick}>{children}</button>,
  Input: ({ value, onChange, disabled, type, ...props }: any) => <input aria-label={props["aria-label"]} value={value} disabled={disabled} type={type} onChange={(event) => onChange(event.target.value)} />,
  Switch: ({ checked, onChange, disabled, ...props }: any) => <input aria-label={props["aria-label"]} type="checkbox" checked={checked} disabled={disabled} onChange={(event) => onChange(event.target.checked)} />,
  Table: ({ columns, dataSource, empty }: any) => <div>{dataSource.length ? dataSource.map((row: any, index: number) => <div key={row.id}>{columns.map((column: any, col: number) => <div key={col}>{column.render?.(undefined, row, index)}</div>)}</div>) : empty}</div>,
  Toast: { error: mocks.error, success: mocks.success },
}));
vi.mock("./settings-layout", () => ({
  SettingsAccessBoundary: ({ children, canWrite }: { children: ReactNode; canWrite: boolean }) => <fieldset disabled={!canWrite}>{children}</fieldset>,
  SettingsCardHeader: ({ title }: { title: string }) => <h2>{title}</h2>,
  SettingsSection: ({ children, title }: { children: ReactNode; title: ReactNode }) => <section>{title}{children}</section>,
  SettingsSwitchField: ({ label, checked, onChange, disabled }: any) => <input aria-label={label} checked={checked} disabled={disabled} type="checkbox" onChange={(e) => onChange(e.target.checked)} />,
  SettingsNumberField: ({ label, value, onChange, min, max }: any) => <input type="number" aria-label={label} value={value} min={min} max={max} onChange={(e) => onChange(Number(e.target.value))} />,
}));

import ICloudLivenessSMTPSection from "./icloud-liveness-smtp";
import { IamApiError } from "@/lib/api-client";

afterEach(() => { cleanup(); vi.clearAllMocks(); });
const props = { options: [], onSave: vi.fn(), loading: false, canReadUserGroups: false, canWriteUserGroups: false, canWrite: true, canSensitive: true };
const server = (id: string) => ({ id, name: id, enabled: true, addr: "smtp.qq.com:465", username: `${id}@qq.com`, from: `${id}@qq.com`, hasPassword: true, password: "must-not-render" });

it("keeps authorization codes private and preserves service IDs after deleting a row", async () => {
  const onBulkSave = vi.fn().mockResolvedValue(undefined);
  render(<ICloudLivenessSMTPSection {...props} options={[{ key: "icloud_liveness_smtp_servers", value: JSON.stringify([server("a"), server("b")]) }]} onBulkSave={onBulkSave} />);
  expect(screen.getByLabelText("SMTP password or authorization code 2")).toHaveValue("");
  fireEvent.click(screen.getByLabelText("Delete SMTP service 1"));
  fireEvent.change(screen.getByLabelText("SMTP service name 1"), { target: { value: "QQ backup" } });
  fireEvent.click(screen.getByRole("button", { name: "保存设置" }));
  await waitFor(() => expect(onBulkSave).toHaveBeenCalledTimes(1));
  const list = JSON.parse(onBulkSave.mock.calls[0][0].find(({ key }: { key: string }) => key === "icloud_liveness_smtp_servers").value);
  expect(list).toHaveLength(1);
  expect(list[0]).toMatchObject({ id: "b", name: "QQ backup", password: "" });
  expect(list[0]).not.toHaveProperty("hasPassword");
  await waitFor(() => expect(screen.getByRole("button", { name: "保存设置" })).toBeEnabled());
  fireEvent.change(screen.getByLabelText("SMTP password or authorization code 1"), { target: { value: "replacement-authorization" } });
  fireEvent.click(screen.getByRole("button", { name: "保存设置" }));
  await waitFor(() => expect(onBulkSave).toHaveBeenCalledTimes(2));
  expect(onBulkSave.mock.calls[1][0][1].value).toContain("replacement-authorization");
  await waitFor(() => expect(screen.getByLabelText("SMTP password or authorization code 1")).toHaveValue(""));
});

it("adds a service and requires its authorization code before enabling it", async () => {
  const onBulkSave = vi.fn().mockResolvedValue(undefined);
  render(<ICloudLivenessSMTPSection {...props} onBulkSave={onBulkSave} />);
  fireEvent.click(screen.getByRole("button", { name: "Add SMTP service" }));
  for (const [label, value] of [["SMTP service name", "QQ 1"], ["SMTP username", "first@qq.com"], ["Liveness sender email", "first@qq.com"]]) {
    fireEvent.change(screen.getByLabelText(`${label} 1`), { target: { value } });
  }
  fireEvent.click(screen.getByRole("button", { name: "保存设置" }));
  expect(mocks.error).toHaveBeenCalled();
  expect(onBulkSave).not.toHaveBeenCalled();
  fireEvent.change(screen.getByLabelText("SMTP password or authorization code 1"), { target: { value: "qq-authorization" } });
  fireEvent.click(screen.getByLabelText("Use dedicated liveness SMTP"));
  fireEvent.click(screen.getByRole("button", { name: "保存设置" }));
  await waitFor(() => expect(onBulkSave).toHaveBeenCalledTimes(1));
  expect(onBulkSave.mock.calls[0][0][0]).toEqual({ key: "icloud_liveness_smtp_enabled", value: "true" });
  const saved = JSON.parse(onBulkSave.mock.calls[0][0][1].value);
  expect(saved[0]).toMatchObject({ addr: "smtp.qq.com:465", enabled: true, password: "qq-authorization" });
  expect(saved[0].id).toBeTruthy();
  expect(onBulkSave.mock.calls[0][0]).toContainEqual({ key: "icloud_liveness_smtp_interval_seconds", value: "60" });
});

it("loads, validates and saves the shared SMTP sending interval", async () => {
  const onBulkSave = vi.fn().mockResolvedValue(undefined);
  render(<ICloudLivenessSMTPSection {...props} options={[{ key: "icloud_liveness_smtp_interval_seconds", value: "300" }]} onBulkSave={onBulkSave} />);
  const input = screen.getByLabelText("SMTP sending interval (seconds)");
  expect(input).toHaveValue(300);
  fireEvent.change(input, { target: { value: "0" } });
  fireEvent.click(screen.getByRole("button", { name: "保存设置" }));
  expect(onBulkSave).not.toHaveBeenCalled();
  expect(mocks.error).toHaveBeenCalledWith("SMTP sending interval must be a whole number from 1 to 86400 seconds.");
  fireEvent.change(input, { target: { value: "120" } });
  fireEvent.click(screen.getByRole("button", { name: "保存设置" }));
  await waitFor(() => expect(onBulkSave).toHaveBeenCalledTimes(1));
  expect(onBulkSave.mock.calls[0][0]).toContainEqual({ key: "icloud_liveness_smtp_interval_seconds", value: "120" });
});

it.each([{ canSensitive: false, canWrite: true }, { canSensitive: true, canWrite: false }])("requires write and sensitive permission: %o", (permissions) => {
  const onBulkSave = vi.fn();
  render(<ICloudLivenessSMTPSection {...props} {...permissions} onBulkSave={onBulkSave} />);
  expect(screen.getByRole("button", { name: "Add SMTP service" })).toBeDisabled();
  expect(screen.getByRole("button", { name: "保存设置" })).toBeDisabled();
  expect(screen.getByLabelText("SMTP sending interval (seconds)")).toBeDisabled();
  fireEvent.click(screen.getByRole("button", { name: "保存设置" }));
  expect(onBulkSave).not.toHaveBeenCalled();
});

it("tests a saved service while dedicated SMTP is off and never submits credentials", async () => {
  mocks.testSMTP.mockResolvedValue({ accepted: true, recipient: "a@qq.com" });
  render(<ICloudLivenessSMTPSection {...props} options={[{ key: "icloud_liveness_smtp_servers", value: JSON.stringify([server("a")]) }]} onBulkSave={vi.fn()} />);
  expect(screen.getByRole("status")).toHaveTextContent("Current liveness transport: system mail.");
  fireEvent.click(screen.getByLabelText("Test SMTP service 1"));
  await waitFor(() => expect(mocks.success).toHaveBeenCalledWith("SMTP accepted the test email. Check a@qq.com for delivery."));
  expect(mocks.testSMTP).toHaveBeenCalledExactlyOnceWith("a");
});

it("requires saving edits and shows the persisted transport until the save succeeds", async () => {
  const onBulkSave = vi.fn().mockResolvedValue(undefined);
  render(<ICloudLivenessSMTPSection {...props} options={[{ key: "icloud_liveness_smtp_servers", value: JSON.stringify([server("a")]) }]} onBulkSave={onBulkSave} />);
  fireEvent.change(screen.getByLabelText("SMTP username 1"), { target: { value: "new@qq.com" } });
  expect(screen.getByLabelText("Test SMTP service 1")).toBeDisabled();
  fireEvent.click(screen.getByLabelText("Use dedicated liveness SMTP"));
  expect(screen.getByRole("status")).toHaveTextContent("Current liveness transport: system mail.");
  fireEvent.click(screen.getByRole("button", { name: "保存设置" }));
  await waitFor(() => expect(screen.getByLabelText("Test SMTP service 1")).toBeEnabled());
  expect(screen.getByRole("status")).toHaveTextContent("Current liveness transport: dedicated SMTP.");
});

it("prevents concurrent tests and displays the shared cooldown", async () => {
  let reject!: (error: unknown) => void;
  mocks.testSMTP.mockReturnValue(new Promise((_resolve, rejectPromise) => { reject = rejectPromise; }));
  render(<ICloudLivenessSMTPSection {...props} options={[{ key: "icloud_liveness_smtp_servers", value: JSON.stringify([server("a"), server("b")]) }]} onBulkSave={vi.fn()} />);
  fireEvent.click(screen.getByLabelText("Test SMTP service 1"));
  expect(screen.getByLabelText("Test SMTP service 2")).toBeDisabled();
  expect(screen.getByRole("button", { name: "保存设置" })).toBeDisabled();
  fireEvent.click(screen.getByLabelText("Test SMTP service 2"));
  const error = new IamApiError(429, { message: "Wait for the SMTP sending interval before testing again." }, new Response(null, { status: 429, headers: { "Retry-After": "59" } }));
  reject(error);
  await waitFor(() => expect(mocks.error).toHaveBeenCalledWith("SMTP sending is limited. Try again in 59 seconds."));
  expect(mocks.testSMTP).toHaveBeenCalledTimes(1);
  await waitFor(() => expect(screen.getByLabelText("Test SMTP service 2")).toBeEnabled());
});

it("displays SMTP authentication failure and does not retry", async () => {
  mocks.testSMTP.mockRejectedValue(new IamApiError(502, { message: "SMTP authentication failed. Check the username and password or authorization code." }));
  render(<ICloudLivenessSMTPSection {...props} options={[{ key: "icloud_liveness_smtp_servers", value: JSON.stringify([server("a")]) }]} onBulkSave={vi.fn()} />);
  fireEvent.click(screen.getByLabelText("Test SMTP service 1"));
  await waitFor(() => expect(mocks.error).toHaveBeenCalledWith("SMTP authentication failed. Check the username and password or authorization code."));
  expect(mocks.testSMTP).toHaveBeenCalledTimes(1);
});
