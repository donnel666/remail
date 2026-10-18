// @vitest-environment jsdom
import "@testing-library/jest-dom/vitest";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({ upload: vi.fn(), poll: vi.fn(), turnstile: vi.fn(), success: vi.fn(), error: vi.fn(), warning: vi.fn(), info: vi.fn() }));
vi.mock("react-i18next", () => ({ useTranslation: () => ({ t: (key: string) => key }) }));
vi.mock("@douyinfe/semi-ui", () => ({
  Button: ({ children, disabled, loading, onClick }: any) => <button disabled={disabled || loading} onClick={onClick}>{children}</button>,
  Modal: ({ children, footer, visible, width }: any) => visible ? <section role="dialog" data-width={width}>{children}{footer}</section> : null,
  Space: ({ children }: any) => <div>{children}</div>,
  TextArea: ({ value, disabled, onChange, placeholder }: any) => <textarea disabled={disabled} placeholder={placeholder} value={value} onChange={(event) => onChange(event.target.value)} />,
  Typography: { Text: ({ children }: any) => <span>{children}</span> },
  Toast: { success: mocks.success, error: mocks.error, warning: mocks.warning, info: mocks.info },
}));
vi.mock("@/components/auth/TurnstileGate", () => ({ requireTurnstile: mocks.turnstile }));
vi.mock("@/lib/daum-api", () => ({ importDaumResources: mocks.upload, waitForDaumImport: mocks.poll }));
vi.mock("@/lib/iam-errors", () => ({
  getIamErrorMessage: (_t: unknown, error: Error) => error.message,
  getApiErrorBodyMessage: (_t: unknown, body: { message: string }) => body.message,
}));
vi.mock("./microsoft-import-batches", async (importOriginal) => {
  const actual = await importOriginal<typeof import("./microsoft-import-batches")>();
  return { ...actual, createMicrosoftImportBatches: (content: string) => actual.createMicrosoftImportBatches(content, 160, 160) };
});
import { ImportOwnedDaumEmailsModal } from "./import-owned-daum-emails-modal";

function showImport(content: string) {
  const onClose = vi.fn();
  const onSuccess = vi.fn();
  const view = render(<ImportOwnedDaumEmailsModal open onOpenChange={onClose} onSuccess={onSuccess} />);
  fireEvent.change(screen.getByPlaceholderText("email@daum.net----APP password"), { target: { value: content } });
  return { ...view, onClose, onSuccess };
}
function readFile(file: File) {
  return new Promise<string>((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => resolve(String(reader.result));
    reader.onerror = () => reject(reader.error);
    reader.readAsText(file);
  });
}
function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => { resolve = done; });
  return { promise, resolve };
}
beforeEach(() => {
  vi.resetAllMocks();
  let id = 0;
  let token = 0;
  mocks.upload.mockImplementation(async () => ({ importId: ++id, status: "processing", imported: 0 }));
  mocks.poll.mockImplementation(async (importId: number) => ({ importId, status: "imported", imported: 1, skipped: 0 }));
  mocks.turnstile.mockImplementation(async () => `token-${++token}`);
});
afterEach(cleanup);

it("keeps the console Microsoft import layout and sends all three formats through owned imports", async () => {
  const lines = [
    "first@daum.net----  app  ",
    "second@daum.net---- login ---- app2 ",
    "third@daum.net----login----app----https://r.test/v1/pickup?email=helper%40outlook.com&token=synthetic----account@kakao.example",
  ];
  const view = showImport(lines.join("\n"));
  expect(screen.getByRole("dialog")).toHaveAttribute("data-width", "min(666px, calc(100vw - 32px))");
  expect(screen.queryByRole("combobox")).not.toBeInTheDocument();
  fireEvent.click(screen.getByRole("button", { name: "Short-lived" }));
  fireEvent.click(screen.getByRole("button", { name: "Abort on error" }));
  fireEvent.click(screen.getByRole("button", { name: "Import" }));
  await waitFor(() => expect(view.onClose).toHaveBeenCalledWith(false));
  const chunks = await Promise.all(mocks.upload.mock.calls.map(([file]) => readFile(file)));
  expect(chunks.join("")).toBe(lines.join("\n"));
  expect(mocks.turnstile).toHaveBeenCalledTimes(chunks.length);
  for (const [file, longLived, token, strategy, signal, idempotencyKey] of mocks.upload.mock.calls) {
    expect(file).toBeInstanceOf(File);
    expect(longLived).toBe(false);
    expect(token).toMatch(/^token-/);
    expect(strategy).toBe("abort");
    expect(signal).toBeInstanceOf(AbortSignal);
    expect(idempotencyKey).toEqual(expect.any(String));
  }
  for (const [action] of mocks.turnstile.mock.calls) expect(action).toBe("daum_resource_import");
  expect(view.onSuccess).toHaveBeenCalledOnce();
});

it.each(["hanmail.net", "hanmail2.net"])("rejects a %s account before human verification or upload in abort mode", async (suffix) => {
  showImport(`first@daum.net----app\nsecond@${suffix}----app`);
  fireEvent.click(screen.getByRole("button", { name: "Abort on error" }));
  fireEvent.click(screen.getByRole("button", { name: "Import" }));
  await waitFor(() => expect(mocks.error).toHaveBeenCalledWith("Import non-Daum line"));
  expect(mocks.upload).not.toHaveBeenCalled();
  expect(mocks.turnstile).not.toHaveBeenCalled();
});

it("excludes unsupported addresses from uploads in skip mode", async () => {
  const view = showImport("one@hanmail.net----app\ntwo@daum.net----app\nthree@hanmail2.net----app");
  fireEvent.click(screen.getByRole("button", { name: "Import" }));
  await waitFor(() => expect(view.onClose).toHaveBeenCalledOnce());
  expect(mocks.warning).toHaveBeenCalledWith("Import skipped errors");
  expect(await readFile(mocks.upload.mock.calls[0][0])).toBe("two@daum.net----app");
});

it("reuses the accepted import ID after a status error without reuploading or reverifying", async () => {
  const view = showImport("one@daum.net----app");
  mocks.poll.mockRejectedValueOnce(new Error("Poll failed"));
  fireEvent.click(screen.getByRole("button", { name: "Import" }));
  await waitFor(() => expect(mocks.error).toHaveBeenCalledWith("Poll failed"));
  fireEvent.click(screen.getByRole("button", { name: "Import" }));
  await waitFor(() => expect(view.onClose).toHaveBeenCalledOnce());
  expect(mocks.upload).toHaveBeenCalledOnce();
  expect(mocks.turnstile).toHaveBeenCalledOnce();
  expect(mocks.poll.mock.calls.map(([id]) => id)).toEqual([1, 1]);
});

it("uses a fresh single-use verification token but the same batch key after a lost upload response", async () => {
  const view = showImport("one@daum.net----app");
  mocks.upload.mockRejectedValueOnce(new Error("Response lost"));
  fireEvent.click(screen.getByRole("button", { name: "Import" }));
  await waitFor(() => expect(mocks.error).toHaveBeenCalledWith("Response lost"));
  fireEvent.click(screen.getByRole("button", { name: "Import" }));
  await waitFor(() => expect(view.onClose).toHaveBeenCalledOnce());
  expect(mocks.upload).toHaveBeenCalledTimes(2);
  expect(mocks.upload.mock.calls[0][2]).not.toBe(mocks.upload.mock.calls[1][2]);
  expect(mocks.upload.mock.calls[0][5]).toBe(mocks.upload.mock.calls[1][5]);
});

it("keeps background completion from refreshing a closed dialog", async () => {
  const pending = deferred<{ importId: number; status: string; imported: number }>();
  const view = showImport("one@daum.net----app");
  mocks.poll.mockReturnValueOnce(pending.promise);
  fireEvent.click(screen.getByRole("button", { name: "Import" }));
  const background = await screen.findByRole("button", { name: "Continue in background" });
  fireEvent.click(background);
  expect(mocks.poll.mock.calls[0][1].signal.aborted).toBe(true);
  await act(async () => { pending.resolve({ importId: 1, status: "imported", imported: 1 }); });
  expect(view.onClose).toHaveBeenCalledOnce();
  expect(view.onSuccess).not.toHaveBeenCalled();
  expect(mocks.error).not.toHaveBeenCalled();
});
