// @vitest-environment jsdom
import "@testing-library/jest-dom/vitest";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { useState } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  upload: vi.fn(), poll: vi.fn(), microsoftUpload: vi.fn(), microsoftPoll: vi.fn(),
  error: vi.fn(), warning: vi.fn(), info: vi.fn(), success: vi.fn(), translate: (key: string) => key,
}));
vi.mock("react-i18next", () => ({ useTranslation: () => ({ t: mocks.translate }) }));
vi.mock("@douyinfe/semi-ui", () => ({
  Modal: ({ children, visible, onCancel, onOk, cancelText, okText, title, width, centered, bodyStyle, confirmLoading, cancelButtonProps, okButtonProps }: any) => visible ? <div role="dialog" data-width={width} data-centered={String(centered)} data-body-style={JSON.stringify(bodyStyle)}>
    <h2>{title}</h2>{children}<footer><button disabled={cancelButtonProps?.disabled} onClick={onCancel}>{cancelText}</button><button disabled={confirmLoading || okButtonProps?.disabled} onClick={onOk}>{okText}</button></footer>
  </div> : null,
  TextArea: ({ value, disabled, onChange, placeholder, ...props }: any) => <textarea {...props} disabled={disabled} placeholder={placeholder} value={value} onChange={(event) => onChange(event.target.value)} />,
  Typography: { Text: ({ children }: any) => <span>{children}</span> },
  Toast: { success: mocks.success, error: mocks.error, warning: mocks.warning, info: mocks.info },
}));
vi.mock("@/components/semi/admin-user-select", () => ({ AdminUserSelect: ({ options, value, onChange }: any) => <select aria-label="Owner" value={value ?? ""} onChange={(event) => onChange(Number(event.target.value))}>
  {options.map((owner: any) => <option key={owner.value} value={owner.value} disabled={owner.disabled}>{owner.label}</option>)}
</select> }));
vi.mock("@/lib/admin-daum-api", () => ({ importAdminDaumResources: mocks.upload, waitForAdminDaumResourceImport: mocks.poll, listAdminDaumOwners: vi.fn() }));
vi.mock("@/lib/admin-microsoft-api", () => ({ importAdminMicrosoftResources: mocks.microsoftUpload, waitForAdminMicrosoftResourceImport: mocks.microsoftPoll, listAdminMicrosoftOwners: vi.fn() }));
vi.mock("@/lib/iam-errors", () => ({ getIamErrorMessage: (_t: unknown, error: Error) => error.message }));
vi.mock("./microsoft-import-batches", async (importOriginal) => {
  const actual = await importOriginal<typeof import("./microsoft-import-batches")>();
  return { ...actual, createMicrosoftImportBatches: (content: string) => actual.createMicrosoftImportBatches(content, 160, 160) };
});
import { ImportDaumEmailsModal } from "./import-daum-emails-modal";
import { ImportMicrosoftModal } from "../admin-microsoft/microsoft-modals";

const owners = [{ id: 7, email: "admin@example.com", nickname: "Admin", role: "admin" as const, groupName: "Admin", enabled: true }];
const lines = [
  "first@daum.net----" + " a ".repeat(28),
  "second@hanmail.net----login----" + "b".repeat(80),
  "third@hanmail2.net----login----app----https://remail.example/v1/pickup?email=aux%40outlook.com&token=private----account@kakao.example",
  "fourth@daum.net----" + "d".repeat(80),
];
function showImport(content: string, onSuccess = vi.fn()) {
  const onOpenChange = vi.fn();
  function Harness() {
    const [open, setOpen] = useState(true);
    return <ImportDaumEmailsModal open={open} owners={owners} onOpenChange={(value) => { onOpenChange(value); setOpen(value); }} onSuccess={onSuccess} />;
  }
  render(<Harness />);
  fireEvent.click(screen.getByRole("button", { name: "TXT file" }));
  const file = Object.assign(new File([content], "accounts.txt", { type: "text/plain" }), { arrayBuffer: async () => new TextEncoder().encode(content).buffer });
  fireEvent.change(screen.getByLabelText("TXT file"), { target: { files: [file] } });
  return { onSuccess, onOpenChange };
}
function normalizedDialog() {
  const clone = screen.getByRole("dialog").cloneNode(true) as HTMLElement;
  clone.querySelector("pre")!.textContent = "provider credential formats";
  const input = clone.querySelector("textarea");
  if (input) input.placeholder = "provider credential format";
  return clone.outerHTML.replace(/Microsoft/g, "Provider").replace(/Daum/g, "Provider")
    .replace("Passwords, client IDs and tokens", "Passwords and tokens");
}
beforeEach(() => {
  vi.resetAllMocks();
  let id = 0;
  mocks.upload.mockImplementation(async () => ({ importId: ++id, imported: 0, skipped: 0, status: "processing" }));
  mocks.poll.mockImplementation(async (importId) => ({ importId, imported: 1, skipped: 0, status: "imported" }));
});
afterEach(cleanup);

describe("Daum copies the administrator Microsoft import modal", () => {
  it.each(["paste", "file"])("matches the real administrator layout and controls in %s mode", async (mode) => {
    const microsoft = render(<ImportMicrosoftModal onCancel={vi.fn()} onImported={vi.fn()} owners={owners} visible />);
    if (mode === "file") fireEvent.click(screen.getByRole("button", { name: "TXT file" }));
    await waitFor(() => expect(screen.getByRole("combobox", { name: "Owner" })).toHaveValue("7"));
    const expected = normalizedDialog();
    microsoft.unmount();
    render(<ImportDaumEmailsModal open owners={owners} onOpenChange={vi.fn()} onSuccess={vi.fn()} />);
    if (mode === "file") fireEvent.click(screen.getByRole("button", { name: "TXT file" }));
    await waitFor(() => expect(screen.getByRole("combobox", { name: "Owner" })).toHaveValue("7"));
    expect(normalizedDialog()).toBe(expected);
    expect(screen.getAllByRole("combobox")).toHaveLength(1);
    expect(screen.queryByText("Mail protocol")).not.toBeInTheDocument();
    expect(screen.queryByText("Import result")).not.toBeInTheDocument();
  });

  it("keeps 2/3/5-field rows unchanged, limits concurrency to three and closes after success", async () => {
    const pending = new Map<number, (value: object) => void>();
    mocks.poll.mockImplementation((id: number) => new Promise((resolve) => pending.set(id, resolve)));
    const content = "\ufeff" + lines.join("\r\n") + "\r\n";
    const { onSuccess, onOpenChange } = showImport(content);
    fireEvent.click(screen.getByRole("button", { name: "Import" }));
    await waitFor(() => expect(mocks.upload).toHaveBeenCalledTimes(3));
    expect(pending.size).toBe(3);
    pending.get(1)!({ importId: 1, imported: 1, skipped: 0, status: "imported" });
    await waitFor(() => expect(mocks.upload).toHaveBeenCalledTimes(4));
    for (const id of [2, 3, 4]) pending.get(id)!({ importId: id, imported: 1, skipped: 0, status: "imported" });
    await waitFor(() => expect(onOpenChange).toHaveBeenCalledWith(false));
    expect(onSuccess).toHaveBeenCalledOnce();
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(onSuccess.mock.invocationCallOrder[0]).toBeLessThan(onOpenChange.mock.invocationCallOrder[0]);
    expect(mocks.upload.mock.calls.map(([payload]) => payload.content).join("")).toBe(content.replace(/^\ufeff/, ""));
    expect(new Set(mocks.upload.mock.calls.map((call) => call[2])).size).toBe(4);
    for (const [payload] of mocks.upload.mock.calls) expect(payload).toMatchObject({ ownerId: 7, protocol: "imap", errorStrategy: "skip", longLived: true });
  });

  it("resumes accepted batches by import ID after polling fails and keeps completed batches", async () => {
    mocks.poll.mockRejectedValueOnce(new Error("poll interrupted"));
    const { onSuccess, onOpenChange } = showImport(lines.slice(0, 2).join("\n"));
    fireEvent.click(screen.getByRole("button", { name: "Import" }));
    await waitFor(() => expect(mocks.error).toHaveBeenCalledWith("poll interrupted"));
    expect(mocks.upload).toHaveBeenCalledTimes(2);
    expect(onOpenChange).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: "Import" }));
    await waitFor(() => expect(onOpenChange).toHaveBeenCalledWith(false));
    expect(onSuccess).toHaveBeenCalledTimes(2);
    expect(mocks.upload).toHaveBeenCalledTimes(2);
    expect(mocks.poll.mock.calls.map((call) => call[0])).toEqual([1, 2, 1]);
  });

  it("reuses the upload idempotency key after a lost response", async () => {
    mocks.upload.mockRejectedValueOnce(new Error("upload response lost"));
    const { onOpenChange } = showImport(lines[0]);
    fireEvent.click(screen.getByRole("button", { name: "Import" }));
    await waitFor(() => expect(mocks.error).toHaveBeenCalledWith("upload response lost"));
    const firstKey = mocks.upload.mock.calls[0][2];
    expect(firstKey).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Import" }));
    await waitFor(() => expect(onOpenChange).toHaveBeenCalledWith(false));
    expect(mocks.upload).toHaveBeenCalledTimes(2);
    expect(mocks.upload.mock.calls[1][2]).toBe(firstKey);
  });

  it("keeps the administrator success-and-close behavior when the list refresh fails", async () => {
    const refresh = vi.fn().mockRejectedValue(new Error("list refresh failed"));
    const { onOpenChange } = showImport(lines[0], refresh);
    fireEvent.click(screen.getByRole("button", { name: "Import" }));
    await waitFor(() => expect(onOpenChange).toHaveBeenCalledWith(false));
    expect(mocks.success).toHaveBeenCalledWith("Daum resources imported.");
    expect(mocks.error).toHaveBeenCalledWith("list refresh failed");
    expect(mocks.upload).toHaveBeenCalledOnce();
    expect(refresh).toHaveBeenCalledOnce();
  });

  it("allows continuing in the background only after the upload is accepted", async () => {
    let complete!: (value: object) => void;
    mocks.poll.mockImplementationOnce(() => new Promise((resolve) => { complete = resolve; }));
    const { onSuccess, onOpenChange } = showImport(lines[0]);
    fireEvent.click(screen.getByRole("button", { name: "Import" }));
    await waitFor(() => expect(mocks.poll).toHaveBeenCalledOnce());
    const signal = mocks.poll.mock.calls[0][1].signal as AbortSignal;
    fireEvent.click(screen.getByRole("button", { name: "Continue in background" }));
    expect(onOpenChange).toHaveBeenCalledWith(false);
    expect(signal.aborted).toBe(true);
    expect(mocks.info).toHaveBeenCalledWith("Resource import continues in background.");
    await act(async () => { complete({ importId: 1, imported: 1, status: "imported" }); });
    expect(onSuccess).not.toHaveBeenCalled();
    expect(mocks.error).not.toHaveBeenCalled();
  });

  it("accepts files above 32 MiB and rejects above the shared 512 MiB limit before reading", () => {
    showImport("");
    const input = screen.getByLabelText("TXT file");
    const accepted = new File(["user@daum.net----app"], "large.txt", { type: "text/plain" });
    Object.defineProperty(accepted, "size", { value: 33 * 1024 * 1024 });
    fireEvent.change(input, { target: { files: [accepted] } });
    expect(mocks.warning).not.toHaveBeenCalled();
    const rejected = new File([], "oversized.txt", { type: "text/plain" });
    Object.defineProperty(rejected, "size", { value: 512 * 1024 * 1024 + 1 });
    fireEvent.change(input, { target: { files: [rejected] } });
    expect(mocks.warning).toHaveBeenCalledWith("Import file must not exceed {{max}} MiB.");
    expect(mocks.upload).not.toHaveBeenCalled();
  });
});
