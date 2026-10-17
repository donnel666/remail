// @vitest-environment jsdom
import "@testing-library/jest-dom/vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({ upload: vi.fn(), poll: vi.fn(), error: vi.fn(), warning: vi.fn(), info: vi.fn(), translate: (key: string) => key }));
vi.mock("react-i18next", () => ({ useTranslation: () => ({ t: mocks.translate }) }));
vi.mock("@douyinfe/semi-ui", () => {
  const Select = ({ children, value, onChange }: any) => <select value={value} onChange={(event) => onChange?.(event.target.value)}>{children}</select>;
  Select.Option = ({ children, value }: any) => <option value={value}>{children}</option>;
  return {
    Select,
    Modal: ({ children, visible, onCancel, onOk, cancelText, okText, confirmLoading, cancelButtonProps, okButtonProps }: any) => visible ? <div>{children}<button disabled={cancelButtonProps?.disabled} onClick={onCancel}>{cancelText}</button><button disabled={confirmLoading || okButtonProps?.disabled} onClick={onOk}>{okText}</button></div> : null,
    TextArea: ({ value, disabled, onChange, placeholder }: any) => <textarea disabled={disabled} placeholder={placeholder} value={value} onChange={(event) => onChange(event.target.value)} />,
    Typography: { Text: ({ children }: any) => <span>{children}</span> },
    Toast: { success: vi.fn(), error: mocks.error, warning: mocks.warning, info: mocks.info },
  };
});
vi.mock("@/components/semi/admin-user-select", () => ({ AdminUserSelect: () => null }));
vi.mock("@/lib/admin-daum-api", () => ({ importAdminDaumResources: mocks.upload, waitForAdminDaumResourceImport: mocks.poll, listAdminDaumOwners: vi.fn() }));
vi.mock("@/lib/iam-errors", () => ({ getIamErrorMessage: (_t: unknown, error: Error) => error.message }));
vi.mock("./daum-import-result-panel", () => ({ DaumImportResultPanel: () => null }));
vi.mock("./microsoft-import-batches", async (importOriginal) => {
  const actual = await importOriginal<typeof import("./microsoft-import-batches")>();
  return { ...actual, createMicrosoftImportBatches: (content: string) => actual.createMicrosoftImportBatches(content, 160, 160) };
});
import { ImportDaumEmailsModal } from "./import-daum-emails-modal";

const owners = [{ id: 7, email: "admin@example.com", nickname: "Admin", role: "admin" as const, groupName: "Admin", enabled: true }];
const lines = [
  "first@daum.net----" + " a ".repeat(28),
  "second@hanmail.net----login----" + "b".repeat(80),
  "third@hanmail2.net----login----app----https://remail.example/v1/pickup?email=aux%40outlook.com&token=private----account@kakao.example",
  "fourth@daum.net----" + "d".repeat(80),
];
function showImport(content: string) {
  const onSuccess = vi.fn();
  render(<ImportDaumEmailsModal open owners={owners} onOpenChange={vi.fn()} onSuccess={onSuccess} />);
  fireEvent.click(screen.getByRole("button", { name: "TXT file" }));
  const file = Object.assign(new File([content], "accounts.txt", { type: "text/plain" }), { arrayBuffer: async () => new TextEncoder().encode(content).buffer });
  fireEvent.change(screen.getByLabelText("Select TXT file"), { target: { files: [file] } });
  return onSuccess;
}
beforeEach(() => {
  vi.resetAllMocks();
  let id = 0;
  mocks.upload.mockImplementation(async () => ({ importId: ++id, imported: 0, skipped: 0, status: "processing" }));
  mocks.poll.mockImplementation(async (importId) => ({ importId, imported: 1, skipped: 0, status: "imported" }));
});
afterEach(cleanup);

describe("Daum uses the Microsoft batch importer", () => {
  it("keeps 2/3/5-field rows unchanged and never starts more than three unfinished batches", async () => {
    const pending = new Map<number, (value: object) => void>();
    mocks.poll.mockImplementation((id: number) => new Promise((resolve) => pending.set(id, resolve)));
    const content = "\ufeff" + lines.join("\r\n") + "\r\n";
    const onSuccess = showImport(content);
    fireEvent.click(screen.getByRole("button", { name: "Import" }));
    await waitFor(() => expect(mocks.upload).toHaveBeenCalledTimes(3));
    expect(pending.size).toBe(3);
    pending.get(1)!({ importId: 1, imported: 1, skipped: 0, status: "imported" });
    await waitFor(() => expect(mocks.upload).toHaveBeenCalledTimes(4));
    for (const id of [2, 3, 4]) pending.get(id)!({ importId: id, imported: 1, skipped: 0, status: "imported" });
    await waitFor(() => expect(onSuccess).toHaveBeenCalledOnce());
    expect(mocks.upload.mock.calls.map(([payload]) => payload.content).join("")).toBe(content.replace(/^\ufeff/, ""));
    expect(new Set(mocks.upload.mock.calls.map((call) => call[2])).size).toBe(4);
    for (const [payload] of mocks.upload.mock.calls) expect(payload).toMatchObject({ ownerId: 7, protocol: "imap", errorStrategy: "skip", longLived: true });
  });

  it("resumes an accepted batch by import ID after polling fails and keeps completed batches", async () => {
    mocks.poll.mockRejectedValueOnce(new Error("poll interrupted"));
    const onSuccess = showImport(lines.slice(0, 2).join("\n"));
    fireEvent.click(screen.getByRole("button", { name: "Import" }));
    await waitFor(() => expect(mocks.error).toHaveBeenCalledWith("poll interrupted"));
    expect(mocks.upload).toHaveBeenCalledTimes(2);
    fireEvent.click(screen.getByRole("button", { name: "Import" }));
    await waitFor(() => expect(onSuccess).toHaveBeenCalledTimes(2));
    expect(mocks.upload).toHaveBeenCalledTimes(2);
    expect(mocks.poll.mock.calls.map((call) => call[0])).toEqual([1, 2, 1]);
  });

  it("accepts files larger than the old 32 MiB cap and rejects above 512 MiB before reading", async () => {
    showImport("");
    const input = screen.getByLabelText("Select TXT file");
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
