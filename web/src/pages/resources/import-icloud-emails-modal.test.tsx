// @vitest-environment jsdom
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { ReactNode } from "react";

const mocks = vi.hoisted(() => ({ upload: vi.fn(), progress: vi.fn(), turnstile: vi.fn(), success: vi.fn(), error: vi.fn(), info: vi.fn() }));
vi.mock("react-i18next", () => ({ useTranslation: () => ({ t: (key: string) => key }) }));
vi.mock("@/components/auth/TurnstileGate", () => ({ requireTurnstile: mocks.turnstile }));
vi.mock("@/lib/icloud-api", () => ({ importICloudResources: mocks.upload, getICloudImport: mocks.progress }));
vi.mock("@/lib/iam-errors", () => ({ getIamErrorMessage: (_t: unknown, error: Error) => error.message }));
vi.mock("@douyinfe/semi-ui", () => ({
  Button: ({ children, onClick, disabled }: { children: ReactNode; onClick?: () => void; disabled?: boolean }) => <button disabled={disabled} onClick={onClick}>{children}</button>,
  Modal: ({ children, footer }: { children: ReactNode; footer: ReactNode }) => <div>{children}{footer}</div>,
  Space: ({ children }: { children: ReactNode }) => <div>{children}</div>,
  TextArea: ({ value, onChange, ...props }: { value: string; onChange: (value: string) => void }) => <textarea {...props} value={value} onChange={(event) => onChange(event.target.value)} />,
  Toast: { success: mocks.success, error: mocks.error, info: mocks.info },
}));
import { ImportICloudEmailsModal } from "./import-icloud-emails-modal";

const accepted = { importId: 8, status: "processing", accepted: 1, completed: 0, failed: 0, waiting: 0, tasks: [{ id: 8, email: "apple@example.com", status: "processing", stage: "manage_prepare", lastSafeError: "" }] };
const content = "apple@example.com----password----https://codes.example.com/token";
const enter = (value: string) => fireEvent.change(screen.getByRole("textbox"), { target: { value } });
const submit = () => fireEvent.click(screen.getByRole("button", { name: "Import" }));

describe("console Apple imports", () => {
  beforeEach(() => { vi.resetAllMocks(); mocks.turnstile.mockResolvedValue("challenge"); mocks.upload.mockResolvedValue(accepted); });
  afterEach(() => { cleanup(); vi.useRealTimers(); });

  it.each(["apple@example.com----password", content + "----invite", "apple@example.com----password----1234567", "apple@example.com----password----file:///tmp/code", "apple@example.com----password----https://user:secret@example.com/code"])("rejects unsupported fields before uploading: %s", async (input) => {
    render(<ImportICloudEmailsModal onClose={vi.fn()} onAccepted={vi.fn()} />);
    enter(input); submit();
    await waitFor(() => expect(mocks.error).toHaveBeenCalled());
    expect(mocks.upload).not.toHaveBeenCalled();
    expect(mocks.turnstile).not.toHaveBeenCalled();
  });

  it("has no expiry selector and reuses the upload key after a lost response", async () => {
    const onAccepted = vi.fn();
    mocks.upload.mockRejectedValueOnce(new Error("Network error"));
    render(<ImportICloudEmailsModal onClose={vi.fn()} onAccepted={onAccepted} />);
    expect(screen.queryByRole("combobox")).toBeNull();
    expect(screen.getByText("Apple import lifetime")).toBeTruthy();
    enter(content); submit();
    await waitFor(() => expect(mocks.error).toHaveBeenCalled());
    submit();
    await waitFor(() => expect(onAccepted).toHaveBeenCalledTimes(1));
    expect(mocks.upload).toHaveBeenCalledTimes(2);
    expect(mocks.upload.mock.calls[0][1]).toBe(mocks.upload.mock.calls[1][1]);
    expect(mocks.turnstile).toHaveBeenCalledWith("icloud_resource_import", expect.any(AbortSignal));
    expect(screen.queryByRole("textbox")).toBeNull();
    expect(screen.getByText("apple@example.com")).toBeTruthy();
  });

  it("shows a terminal account failure and refreshes the list", async () => {
    vi.useFakeTimers();
    const onAccepted = vi.fn();
    mocks.progress.mockResolvedValue({ ...accepted, status: "failed", failed: 1, tasks: [{ ...accepted.tasks[0], status: "failed", lastSafeError: "Apple rejected the password." }] });
    render(<ImportICloudEmailsModal onClose={vi.fn()} onAccepted={onAccepted} />);
    enter(content);
    await act(async () => { submit(); });
    await act(async () => { await vi.advanceTimersByTimeAsync(3000); });
    expect(screen.getByText("Apple rejected the password.")).toBeTruthy();
    expect(screen.getByText("Apple import has failures")).toBeTruthy();
    expect(onAccepted).toHaveBeenCalledTimes(2);
    await act(async () => { await vi.advanceTimersByTimeAsync(30_000); });
    expect(mocks.progress).toHaveBeenCalledTimes(1);
  });

  it("aborts pending progress requests when the dialog unmounts", async () => {
    vi.useFakeTimers();
    mocks.progress.mockReturnValue(new Promise(() => undefined));
    const { unmount } = render(<ImportICloudEmailsModal onClose={vi.fn()} onAccepted={vi.fn()} />);
    enter(content);
    await act(async () => { submit(); });
    await act(async () => { await vi.advanceTimersByTimeAsync(3000); });
    const signal = mocks.progress.mock.calls[0][1] as AbortSignal;
    unmount();
    expect(signal.aborted).toBe(true);
  });
});
