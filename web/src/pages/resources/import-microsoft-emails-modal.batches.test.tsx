// @vitest-environment jsdom
import "@testing-library/jest-dom/vitest";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  upload: vi.fn(), adminUpload: vi.fn(), poll: vi.fn(), adminPoll: vi.fn(),
  turnstile: vi.fn(), success: vi.fn(), error: vi.fn(), warning: vi.fn(), info: vi.fn(),
}));
vi.mock("react-i18next", () => ({ useTranslation: () => ({
  t: (key: string, values?: any) => key === "Import batch progress"
    ? `batch ${values.current}/${values.total}, completed ${values.completed}` : key,
}) }));
vi.mock("@douyinfe/semi-ui", () => ({
  Button: ({ children, disabled, loading, onClick }: any) => <button disabled={disabled || loading} onClick={onClick}>{children}</button>,
  Modal: ({ children, footer, visible, onCancel, onOk, cancelText, okText, confirmLoading, cancelButtonProps, okButtonProps }: any) => visible ? <div>
    {children}{footer ?? <>
      <button disabled={cancelButtonProps?.disabled} onClick={onCancel}>{cancelText}</button>
      <button disabled={confirmLoading || okButtonProps?.disabled} onClick={onOk}>{okText}</button>
    </>}
  </div> : null,
  Space: ({ children }: any) => <div>{children}</div>,
  TextArea: ({ value, disabled, onChange, placeholder }: any) => <textarea disabled={disabled} placeholder={placeholder} value={value} onChange={(event) => onChange(event.target.value)} />,
  Typography: { Text: ({ children }: any) => <span>{children}</span> },
  Toast: { success: mocks.success, error: mocks.error, warning: mocks.warning, info: mocks.info },
}));
vi.mock("@/components/semi/admin-user-select", () => ({ AdminUserSelect: () => null }));
vi.mock("../admin-microsoft/microsoft-meta", () => ({ InfoItem: () => null, ownerRoleLabel: (value: string) => value }));
vi.mock("@/components/auth/TurnstileGate", () => ({ requireTurnstile: mocks.turnstile }));
vi.mock("@/lib/resources-api", () => ({ importMicrosoftResources: mocks.upload, waitForResourceImport: mocks.poll }));
vi.mock("@/lib/admin-microsoft-api", () => ({ importAdminMicrosoftResources: mocks.adminUpload, waitForAdminMicrosoftResourceImport: mocks.adminPoll }));
vi.mock("@/lib/iam-errors", () => ({
  getIamErrorMessage: (_t: unknown, error: Error) => error.message,
  getApiErrorBodyMessage: (_t: unknown, body: { message: string }) => body.message,
}));
vi.mock("./microsoft-import-batches", async (importOriginal) => {
  const actual = await importOriginal<typeof import("./microsoft-import-batches")>();
  return { ...actual, createMicrosoftImportBatches: (content: string) => actual.createMicrosoftImportBatches(content, 50, 50) };
});

import { ImportMicrosoftModal } from "../admin-microsoft/microsoft-modals";
import { ImportMicrosoftEmailsModal } from "./import-microsoft-emails-modal";

const lines = ["first@outlook.com----  密码  ", "second@hotmail.com----next", "third@outlook.com----final"];
const owners = [{ id: 7, email: "admin@example.com", nickname: "Admin", role: "admin" as const, groupName: "Admin", enabled: true }];

function showImport(admin: boolean, content: string) {
  const onClose = vi.fn();
  const onImported = vi.fn();
  const view = render(admin
    ? <ImportMicrosoftModal visible owners={owners} onCancel={onClose} onImported={onImported} />
    : <ImportMicrosoftEmailsModal open onOpenChange={onClose} onSuccess={onImported} />);
  fireEvent.click(screen.getByRole("button", { name: "TXT file" }));
  const file = Object.assign(new File([content], "accounts.txt", { type: "text/plain" }), {
    arrayBuffer: async () => new TextEncoder().encode(content).buffer,
  });
  fireEvent.change(screen.getByLabelText("TXT file"), { target: { files: [file] } });
  return { ...view, onClose, onImported, upload: admin ? mocks.adminUpload : mocks.upload, poll: admin ? mocks.adminPoll : mocks.poll };
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((resolvePromise) => { resolve = resolvePromise; });
  return { promise, resolve };
}

function readFile(file: File) {
  return new Promise<string>((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => resolve(String(reader.result));
    reader.onerror = () => reject(reader.error);
    reader.readAsText(file);
  });
}

describe("Microsoft batch import dialogs", () => {
  beforeEach(() => {
    vi.resetAllMocks();
    let id = 0;
    let token = 0;
    for (const upload of [mocks.upload, mocks.adminUpload]) upload.mockImplementation(async () => ({ importId: ++id, imported: 0, status: "processing" }));
    for (const poll of [mocks.poll, mocks.adminPoll]) poll.mockImplementation(async (importId) => ({ importId, imported: 1, skipped: 0, status: "imported" }));
    mocks.turnstile.mockImplementation(async () => `token-${++token}`);
  });
  afterEach(() => cleanup());

  it.each([false, true])("uploads every whole-line batch with unchanged options (admin=%s)", async (admin) => {
    const content = lines.join("\n");
    const view = showImport(admin, content);
    fireEvent.click(screen.getByRole("button", { name: "Short-lived" }));
    fireEvent.click(screen.getByRole("button", { name: "Abort on error" }));
    fireEvent.click(screen.getByRole("button", { name: "Import" }));
    await waitFor(() => expect(view.onImported).toHaveBeenCalledOnce());
    expect(view.upload).toHaveBeenCalledTimes(3);
    expect(view.onClose).toHaveBeenCalledOnce();
    const chunks = await Promise.all(view.upload.mock.calls.map(async (args) => admin ? args[0].content : readFile(args[0])));
    expect(chunks.join("")).toBe(content);
    expect(chunks.every((chunk) => new Blob([chunk]).size <= 50)).toBe(true);
    if (admin) {
      for (const [payload, signal] of view.upload.mock.calls) {
        expect(payload).toMatchObject({ ownerId: 7, longLived: false, errorStrategy: "abort" });
        expect(signal).toBeInstanceOf(AbortSignal);
      }
      expect(mocks.turnstile).not.toHaveBeenCalled();
    } else {
      expect(mocks.turnstile).toHaveBeenCalledTimes(3);
      expect(view.upload.mock.calls.map((args) => args[2])).toEqual(["token-1", "token-2", "token-3"]);
      for (const args of view.upload.mock.calls) {
        expect(args[1]).toBe(false);
        expect(args[3]).toBe("abort");
        expect(args[4]).toBeInstanceOf(AbortSignal);
      }
    }
  });

  it.each([false, true])("allows background closing only after the final batch is uploaded (admin=%s)", async (admin) => {
    const first = deferred<unknown>();
    const last = deferred<unknown>();
    const view = showImport(admin, [...lines, "fourth@outlook.com----password"].join("\n"));
    view.poll.mockReturnValue(last.promise).mockReturnValueOnce(first.promise);
    fireEvent.click(screen.getByRole("button", { name: "Import" }));
    await waitFor(() => expect(view.poll).toHaveBeenCalledTimes(3));
    expect(view.upload).toHaveBeenCalledTimes(3);
    expect(screen.getByRole("button", { name: "Cancel" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "TXT file" })).toBeDisabled();
    await act(async () => { first.resolve({ importId: 1, imported: 1, status: "imported" }); });
    await waitFor(() => expect(view.poll).toHaveBeenCalledTimes(4));
    const background = screen.getByRole("button", { name: "Continue in background" });
    expect(background).toBeEnabled();
    fireEvent.click(background);
    expect(view.poll.mock.calls[3][1].signal.aborted).toBe(true);
    await act(async () => { last.resolve({ importId: 2, imported: 1, status: "imported" }); });
    expect(view.onClose).toHaveBeenCalledOnce();
    expect(view.onImported).not.toHaveBeenCalled();
    expect(mocks.error).not.toHaveBeenCalled();
  });

  it.each([false, true])("resumes a failed status request without re-uploading accepted batches (admin=%s)", async (admin) => {
    const view = showImport(admin, lines.slice(0, 2).join("\n"));
    view.poll.mockResolvedValueOnce({ importId: 1, imported: 1, status: "imported" }).mockRejectedValueOnce(new Error("Network error"));
    fireEvent.click(screen.getByRole("button", { name: "Import" }));
    await waitFor(() => expect(mocks.error).toHaveBeenCalledWith("Network error"));
    await waitFor(() => expect(screen.getByRole("button", { name: "Import" })).toBeEnabled());
    expect(screen.getByRole("status")).toHaveTextContent("completed 1");
    expect(view.onImported).toHaveBeenCalledOnce();
    fireEvent.click(screen.getByRole("button", { name: "Import" }));
    await waitFor(() => expect(view.onClose).toHaveBeenCalledOnce());
    expect(view.upload).toHaveBeenCalledTimes(2);
    expect(view.poll.mock.calls.map(([id]) => id)).toEqual([1, 2, 2]);
  });

  it("validates the entire file before the first upload in abort mode", async () => {
    const view = showImport(false, `${lines[0]}\n${lines[1]}\ninvalid`);
    fireEvent.click(screen.getByRole("button", { name: "Abort on error" }));
    fireEvent.click(screen.getByRole("button", { name: "Import" }));
    await waitFor(() => expect(mocks.error).toHaveBeenCalledWith("Import invalid line"));
    expect(view.upload).not.toHaveBeenCalled();
    expect(mocks.turnstile).not.toHaveBeenCalled();
  });

  it("resumes remaining batches after a later human verification is dismissed", async () => {
    const view = showImport(false, lines.slice(0, 2).join("\n"));
    mocks.turnstile.mockResolvedValueOnce("first-token").mockResolvedValueOnce(null).mockResolvedValueOnce("second-token");
    fireEvent.click(screen.getByRole("button", { name: "Import" }));
    await waitFor(() => expect(mocks.info).toHaveBeenCalledWith("Import paused. Click Import to continue the remaining batches."));
    expect(view.upload).toHaveBeenCalledOnce();
    fireEvent.click(screen.getByRole("button", { name: "Import" }));
    await waitFor(() => expect(view.onClose).toHaveBeenCalledOnce());
    expect(view.upload).toHaveBeenCalledTimes(2);
    expect(view.upload.mock.calls[1][2]).toBe("second-token");
  });

  it("reports discarded lines before background dismissal", async () => {
    const pending = deferred<unknown>();
    const view = showImport(false, `${lines[0]}\ninvalid`);
    view.poll.mockReturnValueOnce(pending.promise);
    fireEvent.click(screen.getByRole("button", { name: "Import" }));
    await waitFor(() => expect(view.poll).toHaveBeenCalledOnce());
    expect(mocks.warning).toHaveBeenCalledExactlyOnceWith("Import skipped errors");
    fireEvent.click(screen.getByRole("button", { name: "Continue in background" }));
    await act(async () => { pending.resolve({ importId: 1, imported: 1, status: "imported" }); });
    expect(mocks.warning).toHaveBeenCalledOnce();
    expect(view.onClose).toHaveBeenCalledOnce();
  });

  it.each([false, true])("reports each batch's skipped rows once before later batches finish (admin=%s)", async (admin) => {
    const pending = deferred<unknown>();
    const view = showImport(admin, lines.slice(0, 2).join("\n"));
    view.poll.mockResolvedValueOnce({ importId: 1, imported: 0, skipped: 2, status: "imported", lastSafeError: "Skipped 2 import entries." })
      .mockReturnValueOnce(pending.promise);
    fireEvent.click(screen.getByRole("button", { name: "Import" }));
    await waitFor(() => expect(mocks.warning).toHaveBeenCalledExactlyOnceWith("Import skipped errors"));
    await act(async () => { pending.resolve({ importId: 2, imported: 1, skipped: 0, status: "imported" }); });
    expect(view.onClose).toHaveBeenCalledOnce();
    expect(mocks.warning).toHaveBeenCalledOnce();
  });

  it("blocks blind re-upload when the ordinary upload result is unknown", async () => {
    const view = showImport(false, lines[0]);
    view.upload.mockRejectedValueOnce(new Error("Response lost"));
    fireEvent.click(screen.getByRole("button", { name: "Import" }));
    await waitFor(() => expect(mocks.error).toHaveBeenCalledWith("Upload result is unknown. Check imported emails before importing the remaining data."));
    expect(screen.getByRole("status")).toHaveTextContent("Upload result is unknown");
    expect(screen.getByRole("button", { name: "Import" })).toBeDisabled();
    fireEvent.click(screen.getByRole("button", { name: "Import" }));
    expect(view.upload).toHaveBeenCalledOnce();
    expect(view.poll).not.toHaveBeenCalled();
  });

  it("reuses the administrator batch key after a lost response and completes the rest", async () => {
    const view = showImport(true, lines.slice(0, 2).join("\n"));
    const receipts = new Map<string, { importId: number; imported: number; status: string }>();
    let lost = false;
    view.upload.mockImplementation(async (_payload, _signal, key: string) => {
      if (!receipts.has(key)) receipts.set(key, { importId: receipts.size + 1, imported: 0, status: "processing" });
      if (!lost) { lost = true; throw new Error("Response lost"); }
      return receipts.get(key);
    });
    fireEvent.click(screen.getByRole("button", { name: "Import" }));
    await waitFor(() => expect(mocks.error).toHaveBeenCalledWith("Response lost"));
    await waitFor(() => expect(screen.getByRole("button", { name: "Import" })).toBeEnabled());
    expect(view.upload).toHaveBeenCalledTimes(2);
    fireEvent.click(screen.getByRole("button", { name: "Import" }));
    await waitFor(() => expect(view.onClose).toHaveBeenCalledOnce());
    expect(receipts.size).toBe(2);
    expect(view.upload).toHaveBeenCalledTimes(3);
    expect(view.upload.mock.calls[0][2]).toBe(view.upload.mock.calls[2][2]);
    expect(view.upload.mock.calls[0][2]).not.toBe(view.upload.mock.calls[1][2]);
  });

  it("serializes human challenges while allowing three uploads to overlap", async () => {
    const view = showImport(false, lines.join("\n"));
    const challenges = [deferred<string>(), deferred<string>(), deferred<string>()];
    const uploads = [deferred<unknown>(), deferred<unknown>(), deferred<unknown>()];
    let activeChallenges = 0;
    let peakChallenges = 0;
    mocks.turnstile.mockImplementation(async () => {
      const index = mocks.turnstile.mock.calls.length - 1;
      activeChallenges += 1;
      peakChallenges = Math.max(peakChallenges, activeChallenges);
      const token = await challenges[index].promise;
      activeChallenges -= 1;
      return token;
    });
    view.upload.mockImplementation(() => uploads[view.upload.mock.calls.length - 1].promise);
    fireEvent.click(screen.getByRole("button", { name: "Import" }));
    await waitFor(() => expect(mocks.turnstile).toHaveBeenCalledOnce());
    for (let index = 0; index < 3; index += 1) {
      await act(async () => { challenges[index].resolve(`token-${index}`); });
      await waitFor(() => expect(view.upload).toHaveBeenCalledTimes(index + 1));
    }
    expect(peakChallenges).toBe(1);
    expect(view.poll).not.toHaveBeenCalled();
    await act(async () => {
      uploads.forEach((upload, index) => upload.resolve({ importId: index + 1, imported: 0, status: "processing" }));
    });
    expect(view.onClose).toHaveBeenCalledOnce();
  });
});
