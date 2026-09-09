// @vitest-environment jsdom
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { useState, type ReactNode } from "react";

type ModalTestProps = {
  children: ReactNode;
  footer?: ReactNode;
  visible: boolean;
  cancelText?: ReactNode;
  okText?: ReactNode;
  onCancel?: () => void;
  onOk?: () => void;
  confirmLoading?: boolean;
  cancelButtonProps?: { disabled?: boolean };
};

const mocks = vi.hoisted(() => ({
  importResources: vi.fn(), importAdminResources: vi.fn(),
  getImport: vi.fn(), getAdminImport: vi.fn(),
  waitForImport: vi.fn(), waitForAdminImport: vi.fn(),
  success: vi.fn(), error: vi.fn(), warning: vi.fn(), info: vi.fn(), turnstile: vi.fn(),
}));
vi.mock("react-i18next", () => ({ useTranslation: () => ({ t: (key: string) => key }) }));
vi.mock("@/context/auth-provider", () => ({ useAuth: () => ({ currentUser: { id: 31 } }) }));
vi.mock("@/components/auth/TurnstileGate", () => ({ requireTurnstile: mocks.turnstile }));
vi.mock("@/components/semi/admin-user-select", () => ({ AdminUserSelect: ({ value, options }: { value?: number; options: { value: number; label: string }[] }) => <select aria-label="owner" value={value ?? ""} onChange={() => undefined}><option value="" />{options.map((option) => <option key={option.value} value={option.value}>{option.label}</option>)}</select> }));
vi.mock("@/lib/proto-api", () => ({
  importProtoResources: mocks.importResources,
  getProtoResourceImport: mocks.getImport,
  waitForResourceImport: mocks.waitForImport,
}));
vi.mock("@/lib/admin-proto-api", () => ({
  importAdminProtoResources: mocks.importAdminResources, getAdminProtoResourceImport: mocks.getAdminImport, listAdminProtoOwners: vi.fn(), waitForAdminProtoResourceImport: mocks.waitForAdminImport,
}));
vi.mock("@/lib/iam-errors", () => ({
  getIamErrorMessage: (_t: unknown, error: Error) => error.message,
  getApiErrorBodyMessage: () => "warning",
}));
vi.mock("./proto-import-result-panel", () => ({ ProtoImportResultPanel: () => <div>Import results</div> }));
vi.mock("@douyinfe/semi-ui", () => ({
  Button: ({ children, onClick, disabled }: { children: ReactNode; onClick?: () => void; disabled?: boolean }) => <button disabled={disabled} onClick={onClick}>{children}</button>,
  Modal: (props: ModalTestProps) => props.visible ? <div>{props.children}{"footer" in props ? props.footer : <>
    <button disabled={props.cancelButtonProps?.disabled} onClick={props.onCancel}>{props.cancelText}</button>
    <button aria-busy={props.confirmLoading} disabled={props.confirmLoading} onClick={props.onOk}>{props.okText}</button>
  </>}</div> : null,
  Space: ({ children }: { children: ReactNode }) => <div>{children}</div>,
  TextArea: ({ value, onChange, placeholder }: { value: string; onChange: (value: string) => void; placeholder?: string }) => <textarea placeholder={placeholder} value={value} onChange={(event) => onChange(event.target.value)} />,
  Typography: { Text: ({ children }: { children: ReactNode }) => <span>{children}</span> },
  Toast: { success: mocks.success, error: mocks.error, warning: mocks.warning, info: mocks.info },
}));
import { ImportProtoEmailsModal } from "./import-proto-emails-modal";
import { PROTO_EMAIL_FORMAT_HINT } from "./proto-model";

const owners = [{ id: 31, email: "admin@example.com", nickname: "Admin", groupName: "Administrators", role: "admin" as const, enabled: true }];
const completed = { importId: 7, status: "imported", imported: 1, skipped: 0, failed: 0 };

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason: unknown) => void;
  const promise = new Promise<T>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise;
    reject = rejectPromise;
  });
  return { promise, resolve, reject };
}

describe("Proto import feedback", () => {
  beforeEach(() => { vi.resetAllMocks(); sessionStorage.clear(); mocks.turnstile.mockResolvedValue("challenge"); });
  afterEach(() => { cleanup(); vi.restoreAllMocks(); });
  it.each([false, true])("shows bare-account completion and submits both formats verbatim for admin=%s", async (admin) => {
    const importResources = admin ? mocks.importAdminResources : mocks.importResources;
    importResources.mockResolvedValue({ ...completed, imported: 3 });
    const content = "first----  first password  \nsecond----  second password  ---- \tAAEC/w== \t\nthird@custom.example----  full email password  ----AAA=";
    render(<ImportProtoEmailsModal admin={admin} open owners={owners} onOpenChange={vi.fn()} onSuccess={vi.fn()} />);
    expect(screen.getByRole("textbox").getAttribute("placeholder")).toBe(PROTO_EMAIL_FORMAT_HINT);
    expect(PROTO_EMAIL_FORMAT_HINT).toBe("email----password\nemail----password----base64(PKL)\naccount → account@proton.me");
    fireEvent.change(screen.getByRole("textbox"), { target: { value: content } });
    fireEvent.click(screen.getByRole("button", { name: "Import" }));
    await waitFor(() => expect(importResources).toHaveBeenCalledTimes(1));
    if (admin) {
      expect(importResources.mock.calls[0][0].content).toBe(content);
    } else {
      const uploaded = importResources.mock.calls[0][0] as File;
      const uploadedText = await new Promise<string>((resolve, reject) => {
        const reader = new FileReader();
        reader.onload = () => resolve(String(reader.result));
        reader.onerror = () => reject(reader.error);
        reader.readAsText(uploaded);
      });
      expect(uploadedText).toBe(content);
    }
    expect(mocks.error).not.toHaveBeenCalled();
  });
  it("rejects malformed PKL without exposing credentials in feedback, storage or logs", async () => {
    const stored = vi.spyOn(Storage.prototype, "setItem");
    const logged = (["log", "warn", "error"] as const).map((method) => vi.spyOn(console, method).mockImplementation(() => undefined));
    const password = "sensitive password";
    const pkl = "AAEC/w==not-pkl";
    render(<ImportProtoEmailsModal admin open owners={owners} onOpenChange={vi.fn()} onSuccess={vi.fn()} />);
    fireEvent.change(screen.getByRole("textbox"), { target: { value: `person@example.com----${password}----${pkl}` } });
    fireEvent.click(screen.getByRole("button", { name: "Import" }));
    await waitFor(() => expect(mocks.error).toHaveBeenCalledWith("No valid import entries."));
    expect(mocks.importAdminResources).not.toHaveBeenCalled();
    expect(mocks.importResources).not.toHaveBeenCalled();
    expect(mocks.turnstile).not.toHaveBeenCalled();
    expect(stored).not.toHaveBeenCalled();
    const feedback = JSON.stringify([mocks.error.mock.calls, mocks.warning.mock.calls, mocks.success.mock.calls, mocks.info.mock.calls, ...logged.map((spy) => spy.mock.calls)]);
    expect(feedback).not.toContain(password);
    expect(feedback).not.toContain(pkl);
  });
  it("does not report success when the accepted import has failed", async () => {
    mocks.importResources.mockResolvedValue({ importId: 7, status: "failed", lastSafeError: "Invalid proto import format.", imported: 0, skipped: 0, failed: 1 });
    const onSuccess = vi.fn();
    render(<ImportProtoEmailsModal open onOpenChange={vi.fn()} onSuccess={onSuccess} />);
    fireEvent.change(screen.getByRole("textbox"), { target: { value: "person@example.com----secret" } });
    fireEvent.click(screen.getByRole("button", { name: "Import" }));
    await waitFor(() => expect(mocks.error).toHaveBeenCalledWith("Invalid proto import format."));
    expect(mocks.turnstile).toHaveBeenCalledWith("proto_resource_import", expect.any(AbortSignal));
    expect(onSuccess).not.toHaveBeenCalled();
    expect(mocks.success.mock.calls).toEqual([["Resource import accepted."]]);
  });
  it("rejects malformed input before verification or upload", async () => {
    render(<ImportProtoEmailsModal open onOpenChange={vi.fn()} onSuccess={vi.fn()} />);
    fireEvent.change(screen.getByRole("textbox"), { target: { value: "person@example.com----secret----unexpected" } });
    fireEvent.click(screen.getByRole("button", { name: "Import" }));
    await waitFor(() => expect(mocks.error).toHaveBeenCalled());
    expect(mocks.turnstile).not.toHaveBeenCalled();
    expect(mocks.importResources).not.toHaveBeenCalled();
  });
  it("refreshes committed rows when a later import chunk fails", async () => {
    mocks.importResources.mockResolvedValue({ importId: 8, status: "failed", lastSafeError: "Import interrupted.", imported: 1, skipped: 0, failed: 1 });
    const onSuccess = vi.fn();
    render(<ImportProtoEmailsModal open onOpenChange={vi.fn()} onSuccess={onSuccess} />);
    fireEvent.change(screen.getByRole("textbox"), { target: { value: "person@example.com----secret" } });
    fireEvent.click(screen.getByRole("button", { name: "Import" }));
    await waitFor(() => expect(mocks.error).toHaveBeenCalledWith("Import interrupted."));
    expect(onSuccess).toHaveBeenCalledTimes(1);
  });

  it.each([false, true])("does not restore an extra result panel for admin=%s", async (admin) => {
    sessionStorage.setItem(`remail.proto.import.${admin ? "admin" : "owned"}.31`, "7");
    mocks.getImport.mockResolvedValue(completed);
    mocks.getAdminImport.mockResolvedValue(completed);
    render(<ImportProtoEmailsModal admin={admin} open owners={owners} onOpenChange={vi.fn()} onSuccess={vi.fn()} />);
    await act(async () => undefined);
    expect(screen.queryByText("Import results")).toBeNull();
    expect(mocks.getImport).not.toHaveBeenCalled();
    expect(mocks.getAdminImport).not.toHaveBeenCalled();
  });

  it("matches the administrator Microsoft input layout and closes after refresh", async () => {
    mocks.importAdminResources.mockResolvedValue(completed);
    let finishRefresh!: () => void;
    const onSuccess = vi.fn(() => new Promise<void>((resolve) => { finishRefresh = resolve; }));
    const onOpenChange = vi.fn();
    render(<ImportProtoEmailsModal admin open owners={owners} onOpenChange={onOpenChange} onSuccess={onSuccess} />);
    expect((screen.getByLabelText("owner") as HTMLSelectElement).value).toBe("31");
    expect(screen.queryByRole("button", { name: "Manual input" })).toBeNull();
    expect(screen.queryByRole("button", { name: "TXT file" })).toBeNull();
    expect(screen.getByText("Proto resource entries", { exact: false })).toBeTruthy();
    expect(screen.getByText("Parsed entries")).toBeTruthy();
    fireEvent.change(screen.getByRole("textbox"), { target: { value: "person@example.com----secret" } });
    fireEvent.click(screen.getByRole("button", { name: "Import" }));
    await waitFor(() => expect(onSuccess).toHaveBeenCalledTimes(1));
    expect(mocks.importAdminResources.mock.calls[0][0]).toEqual({ content: "person@example.com----secret", ownerId: 31, longLived: true, errorStrategy: "skip" });
    expect(mocks.turnstile).not.toHaveBeenCalled();
    expect(screen.queryByText("Import results")).toBeNull();
    expect(onOpenChange).not.toHaveBeenCalled();
    await act(async () => finishRefresh());
    expect(onOpenChange.mock.calls).toEqual([[false]]);
    expect(mocks.info).not.toHaveBeenCalled();
  });

  it("uses the administrator Microsoft empty-input warning", async () => {
    render(<ImportProtoEmailsModal admin open owners={owners} onOpenChange={vi.fn()} onSuccess={vi.fn()} />);
    fireEvent.click(screen.getByRole("button", { name: "Import" }));
    await waitFor(() => expect(mocks.warning).toHaveBeenCalledWith("Please enter Proto resources."));
    expect(mocks.importAdminResources).not.toHaveBeenCalled();
    expect(mocks.importResources).not.toHaveBeenCalled();
  });

  it("keeps entered content when administrator owners arrive late", () => {
    const view = render(<ImportProtoEmailsModal admin open onOpenChange={vi.fn()} onSuccess={vi.fn()} />);
    fireEvent.change(screen.getByRole("textbox"), { target: { value: "person@example.com----secret" } });
    view.rerender(<ImportProtoEmailsModal admin open owners={owners} onOpenChange={vi.fn()} onSuccess={vi.fn()} />);
    expect((screen.getByRole("textbox") as HTMLTextAreaElement).value).toBe("person@example.com----secret");
    expect((screen.getByLabelText("owner") as HTMLSelectElement).value).toBe("31");
  });

  it.each([false, true])("keeps asynchronous background continuation for admin=%s", async (admin) => {
    const importResources = admin ? mocks.importAdminResources : mocks.importResources;
    const waitForImport = admin ? mocks.waitForAdminImport : mocks.waitForImport;
    importResources.mockResolvedValue({ ...completed, status: "processing" });
    waitForImport.mockImplementation((_id: number, { signal }: { signal: AbortSignal }) => new Promise((_resolve, reject) => {
      signal.addEventListener("abort", () => reject(new DOMException("Aborted", "AbortError")), { once: true });
    }));
    const onOpenChange = vi.fn();
    const onSuccess = vi.fn();
    render(<ImportProtoEmailsModal admin={admin} open owners={owners} onOpenChange={onOpenChange} onSuccess={onSuccess} />);
    fireEvent.change(screen.getByRole("textbox"), { target: { value: "person@example.com----secret" } });
    fireEvent.click(screen.getByRole("button", { name: "Import" }));
    await waitFor(() => expect(waitForImport).toHaveBeenCalledTimes(1));
    expect(screen.queryByText("Import results")).toBeNull();
    if (admin) {
      expect(screen.queryByRole("button", { name: "Continue in background" })).toBeNull();
      expect(screen.getByRole("button", { name: "Import" }).getAttribute("aria-busy")).toBe("true");
    }
    fireEvent.click(screen.getByRole("button", { name: admin ? "Cancel" : "Continue in background" }));
    await waitFor(() => expect(onOpenChange).toHaveBeenCalledWith(false));
    expect(waitForImport.mock.calls[0][1].signal.aborted).toBe(true);
    expect(mocks.info).toHaveBeenCalledWith("Resource import continues in background.");
    expect(onSuccess).not.toHaveBeenCalled();
    expect(mocks.error).not.toHaveBeenCalled();
  });

  it.each(["success", "failure"] as const)("does not let a late %s refresh affect a reopened import", async (outcome) => {
    const firstRefresh = deferred<void>();
    const secondImport = deferred<typeof completed>();
    mocks.importAdminResources.mockResolvedValueOnce(completed).mockReturnValueOnce(secondImport.promise);
    const onSuccess = vi.fn().mockReturnValueOnce(firstRefresh.promise).mockResolvedValueOnce(undefined);
    const onOpenChange = vi.fn();
    function Harness() {
      const [open, setOpen] = useState(true);
      return <>
        <button onClick={() => setOpen(true)}>Reopen import</button>
        <ImportProtoEmailsModal admin open={open} owners={owners} onSuccess={onSuccess} onOpenChange={(next) => { onOpenChange(next); setOpen(next); }} />
      </>;
    }
    render(<Harness />);
    fireEvent.change(screen.getByRole("textbox"), { target: { value: "first@example.com----secret" } });
    fireEvent.click(screen.getByRole("button", { name: "Import" }));
    await waitFor(() => expect(onSuccess).toHaveBeenCalledTimes(1));
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
    expect(screen.queryByRole("textbox")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Reopen import" }));
    fireEvent.change(screen.getByRole("textbox"), { target: { value: "second@example.com----secret" } });
    fireEvent.click(screen.getByRole("button", { name: "Import" }));
    await waitFor(() => expect(mocks.importAdminResources).toHaveBeenCalledTimes(2));
    const secondSignal = mocks.importAdminResources.mock.calls[1][1] as AbortSignal;

    await act(async () => {
      if (outcome === "success") firstRefresh.resolve(undefined);
      else firstRefresh.reject(new Error("Stale refresh failed."));
    });
    expect(secondSignal.aborted).toBe(false);
    expect(onOpenChange.mock.calls).toEqual([[false]]);
    expect((screen.getByRole("textbox") as HTMLTextAreaElement).value).toBe("second@example.com----secret");
    expect((screen.getByLabelText("owner") as HTMLSelectElement).value).toBe("31");
    expect(screen.getByRole("button", { name: "Import" }).getAttribute("aria-busy")).toBe("true");
    expect((screen.getByRole("button", { name: "Cancel" }) as HTMLButtonElement).disabled).toBe(true);
    expect(mocks.error).not.toHaveBeenCalled();
    expect(onSuccess).toHaveBeenCalledTimes(1);

    await act(async () => secondImport.resolve({ ...completed, importId: 8 }));
    await waitFor(() => expect(onSuccess).toHaveBeenCalledTimes(2));
    expect(onOpenChange.mock.calls).toEqual([[false], [false]]);
    expect(screen.queryByRole("textbox")).toBeNull();
    expect(mocks.importAdminResources.mock.calls[1][0]).toMatchObject({ ownerId: 31, content: "second@example.com----secret" });
  });

  it("disables native administrator Cancel during POST and enables it during polling", async () => {
    const accepted = deferred<typeof completed>();
    mocks.importAdminResources.mockReturnValue(accepted.promise);
    mocks.waitForAdminImport.mockImplementation((_id: number, { signal }: { signal: AbortSignal }) => new Promise((_resolve, reject) => {
      signal.addEventListener("abort", () => reject(new DOMException("Aborted", "AbortError")), { once: true });
    }));
    const onOpenChange = vi.fn();
    render(<ImportProtoEmailsModal admin open owners={owners} onOpenChange={onOpenChange} onSuccess={vi.fn()} />);
    fireEvent.change(screen.getByRole("textbox"), { target: { value: "person@example.com----secret" } });
    fireEvent.click(screen.getByRole("button", { name: "Import" }));
    await waitFor(() => expect(mocks.importAdminResources).toHaveBeenCalledTimes(1));
    expect((screen.getByRole("button", { name: "Cancel" }) as HTMLButtonElement).disabled).toBe(true);
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
    expect(onOpenChange).not.toHaveBeenCalled();
    await act(async () => accepted.resolve({ ...completed, status: "processing" }));
    await waitFor(() => expect(mocks.waitForAdminImport).toHaveBeenCalledTimes(1));
    expect((screen.getByRole("button", { name: "Cancel" }) as HTMLButtonElement).disabled).toBe(false);
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
    await waitFor(() => expect(onOpenChange).toHaveBeenCalledWith(false));
    expect(mocks.waitForAdminImport.mock.calls[0][1].signal.aborted).toBe(true);
    expect(mocks.info).toHaveBeenCalledWith("Resource import continues in background.");
    expect(mocks.error).not.toHaveBeenCalled();
  });

  it("aborts an unmounted verification and ignores its late token", async () => {
    const challenge = deferred<string>();
    mocks.turnstile.mockReturnValue(challenge.promise);
    const view = render(<ImportProtoEmailsModal open onOpenChange={vi.fn()} onSuccess={vi.fn()} />);
    fireEvent.change(screen.getByRole("textbox"), { target: { value: "person@example.com----secret" } });
    fireEvent.click(screen.getByRole("button", { name: "Import" }));
    await waitFor(() => expect(mocks.turnstile).toHaveBeenCalledTimes(1));
    const signal = mocks.turnstile.mock.calls[0][1] as AbortSignal;
    view.unmount();
    expect(signal.aborted).toBe(true);
    await act(async () => challenge.resolve("late-token"));
    expect(mocks.importResources).not.toHaveBeenCalled();
    expect(mocks.importAdminResources).not.toHaveBeenCalled();
    expect(mocks.success).not.toHaveBeenCalled();
    expect(mocks.error).not.toHaveBeenCalled();
  });
});
