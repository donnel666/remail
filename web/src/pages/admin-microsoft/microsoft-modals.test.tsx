// @vitest-environment jsdom

import "@testing-library/jest-dom/vitest";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type {
  AdminMicrosoftOwner,
  AdminMicrosoftResourceDetail,
  AdminMicrosoftResourceItem,
} from "./admin-microsoft-types";

const mocks = vi.hoisted(() => ({
  importResources: vi.fn(),
  listOwners: vi.fn(),
  replaceCredentials: vi.fn(),
  toastError: vi.fn(),
  toastInfo: vi.fn(),
  toastSuccess: vi.fn(),
  toastWarning: vi.fn(),
  updateResource: vi.fn(),
  waitForImport: vi.fn(),
}));

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (key: string) => key }),
}));

vi.mock("@douyinfe/semi-ui", () => {
  const Input = ({ onChange, placeholder, value }: any) => (
    <input
      onChange={(event) => onChange?.(event.target.value)}
      placeholder={placeholder}
      value={value ?? ""}
    />
  );
  const InputNumber = ({ onChange, value }: any) => (
    <input
      aria-label="number"
      onChange={(event) => onChange?.(event.target.value)}
      value={value ?? ""}
    />
  );
  const Modal = ({ cancelButtonProps, cancelText, children, confirmLoading, okButtonProps, onCancel, onOk, okText, title, visible }: any) =>
    visible ? (
      <section aria-label={title} role="dialog">
        <h1>{title}</h1>
        {children}
        <button disabled={cancelButtonProps?.disabled} onClick={onCancel} type="button">
          {cancelText}
        </button>
        <button disabled={confirmLoading || okButtonProps?.disabled} onClick={onOk} type="button">
          {okText}
        </button>
      </section>
    ) : null;
  const Select = ({ onChange, optionList = [], value }: any) => (
    <select aria-label="owner" onChange={(event) => onChange?.(event.target.value)} value={value ?? ""}>
      {optionList.map((option: any) => (
        <option disabled={option.disabled} key={String(option.value)} value={option.value}>
          {option.label}
        </option>
      ))}
    </select>
  );
  const Switch = ({ checked, onChange }: any) => (
    <input
      checked={Boolean(checked)}
      onChange={(event) => onChange?.(event.target.checked)}
      role="switch"
      type="checkbox"
    />
  );
  const TextArea = ({ onChange, placeholder, value }: any) => (
    <textarea
      onChange={(event) => onChange?.(event.target.value)}
      placeholder={placeholder}
      value={value ?? ""}
    />
  );
  return {
    Input,
    InputNumber,
    Modal,
    Select,
    Switch,
    TextArea,
    Toast: {
      error: mocks.toastError,
      info: mocks.toastInfo,
      success: mocks.toastSuccess,
      warning: mocks.toastWarning,
    },
    Typography: { Text: ({ children }: any) => <span>{children}</span> },
  };
});

vi.mock("@/lib/admin-microsoft-api", () => ({
  importAdminMicrosoftResources: mocks.importResources,
  listAdminMicrosoftOwners: mocks.listOwners,
  replaceAdminMicrosoftCredentials: mocks.replaceCredentials,
  updateAdminMicrosoftResource: mocks.updateResource,
  waitForAdminMicrosoftResourceImport: mocks.waitForImport,
}));

vi.mock("@/lib/iam-errors", () => ({
  getIamErrorMessage: (_t: unknown, _error: unknown, fallback: string) => fallback,
}));

vi.mock("./microsoft-meta", () => ({
  InfoItem: ({ label, value }: any) => (
    <div>
      <span>{label}</span>
      <span>{value}</span>
    </div>
  ),
  ownerRoleLabel: (role: string) => role,
}));

import { ownersWithCurrentUserFirst } from "@/components/semi/admin-user-select";
import {
  EditMicrosoftModal,
  ImportMicrosoftModal,
  ReplaceCredentialsModal,
} from "./microsoft-modals";

const owner: AdminMicrosoftOwner = {
  email: "owner@example.com",
  enabled: true,
  groupName: "Supply",
  id: 7,
  nickname: "Owner",
  role: "supplier",
};

function importFile(content: string | ArrayBuffer, name: string) {
  const bytes = typeof content === "string" ? new TextEncoder().encode(content).buffer : content;
  // jsdom's File does not implement Blob.arrayBuffer().
  return Object.assign(new File([bytes], name, { type: "text/plain" }), {
    arrayBuffer: vi.fn(async () => bytes),
  });
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason: unknown) => void;
  const promise = new Promise<T>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise;
    reject = rejectPromise;
  });
  return { promise, resolve, reject };
}

function resource(id = 41): AdminMicrosoftResourceItem {
  const now = "2026-07-12T00:00:00Z";
  return {
    activeTask: null,
    bindingAddress: "aux@example.net",
    createdAt: now,
    emailAddress: `resource-${id}@outlook.com`,
    forSale: false,
    graphAvailable: false,
    id,
    lastAllocatedAt: null,
    lastSafeError: null,
    longLived: true,
    mailProtocol: "imap",
    owner,
    qualityScore: 80,
    rtExpireAt: null,
    status: "normal",
    suffix: "@outlook.com",
    tokenHealth: "valid",
    type: "microsoft",
    updatedAt: now,
    version: 3,
  };
}

describe("admin Microsoft modal workflows", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.listOwners.mockResolvedValue([owner]);
    mocks.importResources.mockResolvedValue({ imported: 1, skipped: 0, status: "imported" });
    mocks.waitForImport.mockResolvedValue({ imported: 1, skipped: 0, status: "imported" });
  });

  afterEach(() => cleanup());

  it("submits the import modal through the real adapter boundary and refreshes once", async () => {
    mocks.importResources.mockResolvedValue({
      accepted: 1,
      importId: 91,
      imported: 1,
      skipped: 0,
      status: "imported",
      task: {},
    });
    const onCancel = vi.fn();
    const onImported = vi.fn().mockResolvedValue(undefined);
    const otherOwner = {
      ...owner,
      email: "other@example.com",
      id: 8,
      nickname: "Other",
    };
    render(
      <ImportMicrosoftModal
        onCancel={onCancel}
        onImported={onImported}
        owners={ownersWithCurrentUserFirst([otherOwner, owner], {
          email: "admin@example.com",
          enabled: true,
          id: owner.id,
          nickname: "Admin",
          role: "admin",
          userGroup: { name: "Administrators" },
        })}
        visible
      />
    );

    await waitFor(() => expect(screen.getByLabelText("owner")).toHaveValue("7"));
    fireEvent.change(screen.getByPlaceholderText("email----password"), {
      target: { value: "one@outlook.com----write-only-password" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Import" }));

    await waitFor(() =>
      expect(mocks.importResources).toHaveBeenCalledWith({
        content: "one@outlook.com----write-only-password",
        errorStrategy: "skip",
        longLived: true,
        ownerId: 7,
      }, expect.any(AbortSignal))
    );
    await waitFor(() => expect(onImported).toHaveBeenCalledTimes(1));
    expect(onCancel).toHaveBeenCalledTimes(1);
  });

  it("keeps entered import content when owners arrive after the modal opens", async () => {
    const view = render(
      <ImportMicrosoftModal
        onCancel={vi.fn()}
        onImported={vi.fn()}
        owners={[]}
        visible
      />
    );

    const input = screen.getByPlaceholderText("email----password");
    fireEvent.change(input, {
      target: { value: "one@outlook.com----write-only-password" },
    });
    view.rerender(
      <ImportMicrosoftModal
        onCancel={vi.fn()}
        onImported={vi.fn()}
        owners={[owner]}
        visible
      />
    );

    await waitFor(() => expect(screen.getByLabelText("owner")).toHaveValue("7"));
    expect(input).toHaveValue("one@outlook.com----write-only-password");
  });

  it.each(["picker", "drop"])("imports a TXT file via %s with the selected options", async (method) => {
    mocks.importResources.mockResolvedValue({ imported: 2, skipped: 0, status: "imported" });
    const onCancel = vi.fn();
    const onImported = vi.fn();
    render(<ImportMicrosoftModal onCancel={onCancel} onImported={onImported} owners={[owner]} visible />);
    fireEvent.change(screen.getByPlaceholderText("email----password"), {
      target: { value: "stale@outlook.com----password" },
    });
    fireEvent.click(screen.getByRole("button", { name: "TXT file" }));
    expect(screen.getByRole("button", { name: "Import" })).toBeDisabled();
    const content = "one@outlook.com----  密码  \r\ntwo@hotmail.com----pass----client----token\r\n";
    const file = importFile(content, "accounts.TXT");
    if (method === "picker") {
      const input = screen.getByLabelText("TXT file");
      const openPicker = vi.spyOn(input, "click");
      fireEvent.click(screen.getByRole("button", { name: /Click to select or drag file here/ }));
      expect(openPicker).toHaveBeenCalledOnce();
      fireEvent.change(input, { target: { files: [file] } });
    } else {
      fireEvent.drop(screen.getByRole("button", { name: /Click to select or drag file here/ }), {
        dataTransfer: { files: [file] },
      });
    }
    expect(screen.getByText("accounts.TXT")).toBeInTheDocument();
    expect(screen.getByText(`${(file.size / 1024).toFixed(1)} KB`)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Short-lived" }));
    fireEvent.click(screen.getByRole("button", { name: "Abort on error" }));
    fireEvent.click(screen.getByRole("button", { name: "Import" }));

    await waitFor(() => expect(mocks.importResources).toHaveBeenCalledWith({
      content, errorStrategy: "abort", longLived: false, ownerId: 7,
    }, expect.any(AbortSignal)));
    expect(mocks.importResources).toHaveBeenCalledOnce();
    await waitFor(() => expect(onImported).toHaveBeenCalledOnce());
    expect(onCancel).toHaveBeenCalledOnce();
  });

  it("clears the selected file when switching to manual input or reopening", () => {
    const props = { onCancel: vi.fn(), onImported: vi.fn(), owners: [owner] };
    const view = render(<ImportMicrosoftModal {...props} visible />);
    const file = importFile("one@outlook.com----password", "accounts.txt");
    fireEvent.click(screen.getByRole("button", { name: "TXT file" }));
    fireEvent.change(screen.getByLabelText("TXT file"), { target: { files: [file] } });
    fireEvent.click(screen.getByRole("button", { name: "Manual input" }));
    expect(screen.getByPlaceholderText("email----password")).toHaveValue("");
    fireEvent.click(screen.getByRole("button", { name: "TXT file" }));
    expect(screen.queryByText(file.name)).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Import" })).toBeDisabled();

    fireEvent.change(screen.getByLabelText("TXT file"), { target: { files: [file] } });
    view.rerender(<ImportMicrosoftModal {...props} visible={false} />);
    view.rerender(<ImportMicrosoftModal {...props} visible />);
    expect(screen.getByRole("button", { name: "Manual input" })).toHaveAttribute("aria-pressed", "true");
    fireEvent.click(screen.getByRole("button", { name: "TXT file" }));
    expect(screen.queryByText(file.name)).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Import" })).toBeDisabled();
    expect(mocks.importResources).not.toHaveBeenCalled();
  });

  it("rejects non-TXT, blank and unreadable files without submitting or closing", async () => {
    const onCancel = vi.fn();
    render(<ImportMicrosoftModal onCancel={onCancel} onImported={vi.fn()} owners={[owner]} visible />);
    fireEvent.click(screen.getByRole("button", { name: "TXT file" }));
    const input = screen.getByLabelText("TXT file");
    fireEvent.change(input, { target: { files: [importFile("invalid", "accounts.csv")] } });
    expect(mocks.toastWarning).toHaveBeenCalledWith("Please select a TXT file.");
    expect(screen.getByRole("button", { name: "Import" })).toBeDisabled();

    fireEvent.change(input, { target: { files: [importFile(" \r\n\t", "empty.txt")] } });
    fireEvent.click(screen.getByRole("button", { name: "Import" }));
    await waitFor(() => expect(mocks.toastWarning).toHaveBeenCalledWith("Please enter Microsoft resources."));
    expect(screen.getByRole("button", { name: "Cancel" })).toBeEnabled();

    const unreadable = importFile("one@outlook.com----password", "unreadable.txt");
    unreadable.arrayBuffer.mockRejectedValue(new Error("Read failed"));
    fireEvent.change(input, { target: { files: [unreadable] } });
    fireEvent.click(screen.getByRole("button", { name: "Import" }));
    await waitFor(() => expect(mocks.toastError).toHaveBeenCalledWith("Resource import failed."));
    expect(screen.getByRole("button", { name: "Import" })).toBeEnabled();
    expect(screen.getByRole("button", { name: "Cancel" })).toBeEnabled();
    expect(mocks.importResources).not.toHaveBeenCalled();
    expect(onCancel).not.toHaveBeenCalled();
  });

  it("rejects invalid UTF-8 bytes without replacing password characters or submitting", async () => {
    const bytes = new Uint8Array([...new TextEncoder().encode("one@outlook.com----pass"), 0xff]);
    const file = importFile(bytes.buffer, "invalid-utf8.txt");
    const onCancel = vi.fn();
    render(<ImportMicrosoftModal onCancel={onCancel} onImported={vi.fn()} owners={[owner]} visible />);
    fireEvent.click(screen.getByRole("button", { name: "TXT file" }));
    fireEvent.change(screen.getByLabelText("TXT file"), { target: { files: [file] } });
    fireEvent.click(screen.getByRole("button", { name: "Import" }));

    await waitFor(() => expect(mocks.toastError).toHaveBeenCalledWith("Import file must be UTF-8 encoded."));
    expect(mocks.importResources).not.toHaveBeenCalled();
    expect(onCancel).not.toHaveBeenCalled();
    expect(screen.getByRole("button", { name: "Import" })).toBeEnabled();
    expect(screen.getByRole("button", { name: "Cancel" })).toBeEnabled();
  });

  it.each(["picker", "drop"])("rejects oversized files from %s before reading, but accepts the size boundary", async (method) => {
    const maxBytes = 512 * 1024 * 1024;
    const oversized = importFile("one@outlook.com----password", "oversized.txt");
    Object.defineProperty(oversized, "size", { value: maxBytes + 1 });
    render(<ImportMicrosoftModal onCancel={vi.fn()} onImported={vi.fn()} owners={[owner]} visible />);
    fireEvent.click(screen.getByRole("button", { name: "TXT file" }));
    if (method === "picker") {
      fireEvent.change(screen.getByLabelText("TXT file"), { target: { files: [oversized] } });
    } else {
      fireEvent.drop(screen.getByRole("button", { name: /Click to select or drag file here/ }), {
        dataTransfer: { files: [oversized] },
      });
    }
    expect(mocks.toastWarning).toHaveBeenCalledWith("Import file must not exceed {{max}} MiB.");
    expect(screen.getByRole("button", { name: "Import" })).toBeDisabled();
    expect(oversized.arrayBuffer).not.toHaveBeenCalled();
    expect(mocks.importResources).not.toHaveBeenCalled();

    const atLimit = importFile("one@outlook.com----password", "at-limit.txt");
    Object.defineProperty(atLimit, "size", { value: maxBytes });
    fireEvent.change(screen.getByLabelText("TXT file"), { target: { files: [atLimit] } });
    expect(screen.getByRole("button", { name: "Import" })).toBeEnabled();
    fireEvent.click(screen.getByRole("button", { name: "Import" }));
    await waitFor(() => expect(mocks.importResources).toHaveBeenCalledOnce());
    expect(atLimit.arrayBuffer).toHaveBeenCalledOnce();
  });

  it("polls an accepted import to completion before refreshing and closing", async () => {
    mocks.importResources.mockResolvedValue({ importId: 17, status: "processing" });
    const onImported = vi.fn();
    const onCancel = vi.fn();
    render(<ImportMicrosoftModal onCancel={onCancel} onImported={onImported} owners={[owner]} visible />);
    fireEvent.change(screen.getByPlaceholderText("email----password"), {
      target: { value: "one@outlook.com----password" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Import" }));

    await waitFor(() => expect(onImported).toHaveBeenCalledOnce());
    expect(mocks.waitForImport).toHaveBeenCalledWith(17, { signal: mocks.importResources.mock.calls[0][1] });
    expect(mocks.toastSuccess).toHaveBeenCalledWith("Microsoft resources imported.");
    expect(onCancel).toHaveBeenCalledOnce();
  });

  it.each(["resolve", "reject"])("allows background dismissal and ignores an old poll's %s after reopening", async (outcome) => {
    const upload = deferred<unknown>();
    const oldPoll = deferred<unknown>();
    const newUpload = deferred<unknown>();
    mocks.importResources.mockReturnValueOnce(upload.promise).mockReturnValueOnce(newUpload.promise);
    mocks.waitForImport.mockReturnValueOnce(oldPoll.promise);
    const props = { onCancel: vi.fn(), onImported: vi.fn(), owners: [owner] };
    const view = render(<ImportMicrosoftModal {...props} visible />);
    fireEvent.change(screen.getByPlaceholderText("email----password"), {
      target: { value: "one@outlook.com----password" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Import" }));
    expect(screen.getByRole("button", { name: "Cancel" })).toBeDisabled();
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
    expect(props.onCancel).not.toHaveBeenCalled();
    await act(async () => { upload.resolve({ importId: 17, status: "processing" }); });
    const background = screen.getByRole("button", { name: "Continue in background" });
    expect(background).toBeEnabled();
    const oldSignal = mocks.importResources.mock.calls[0][1] as AbortSignal;
    expect(mocks.waitForImport).toHaveBeenCalledWith(17, { signal: oldSignal });
    fireEvent.click(background);
    expect(oldSignal.aborted).toBe(true);
    expect(props.onCancel).toHaveBeenCalledOnce();
    expect(mocks.toastInfo).toHaveBeenCalledWith("Resource import continues in background.");

    view.rerender(<ImportMicrosoftModal {...props} visible={false} />);
    view.rerender(<ImportMicrosoftModal {...props} visible />);
    fireEvent.change(screen.getByPlaceholderText("email----password"), {
      target: { value: "two@outlook.com----password" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Import" }));
    await act(async () => {
      if (outcome === "resolve") oldPoll.resolve({ imported: 1, skipped: 0, status: "imported" });
      else oldPoll.reject(new DOMException("Aborted", "AbortError"));
    });
    expect(mocks.importResources).toHaveBeenCalledTimes(2);
    expect(screen.getByPlaceholderText("email----password")).toHaveValue("two@outlook.com----password");
    expect(screen.getByRole("button", { name: "Import" })).toBeDisabled();
    expect(mocks.importResources.mock.calls[1][1].aborted).toBe(false);
    expect(props.onImported).not.toHaveBeenCalled();
    expect(props.onCancel).toHaveBeenCalledOnce();
    expect(mocks.toastError).not.toHaveBeenCalled();
    expect(mocks.toastSuccess).not.toHaveBeenCalledWith("Microsoft resources imported.");

    await act(async () => { newUpload.resolve({ imported: 1, skipped: 0, status: "imported" }); });
    expect(props.onImported).toHaveBeenCalledOnce();
    expect(props.onCancel).toHaveBeenCalledTimes(2);
  });

  it.each(["hide", "unmount"])("does not submit a file whose read finishes after %s", async (action) => {
    const read = deferred<ArrayBuffer>();
    const file = importFile("one@outlook.com----password", "accounts.txt");
    file.arrayBuffer.mockReturnValue(read.promise);
    const props = { onCancel: vi.fn(), onImported: vi.fn(), owners: [owner] };
    const view = render(<ImportMicrosoftModal {...props} visible />);
    fireEvent.click(screen.getByRole("button", { name: "TXT file" }));
    fireEvent.change(screen.getByLabelText("TXT file"), { target: { files: [file] } });
    fireEvent.click(screen.getByRole("button", { name: "Import" }));
    expect(file.arrayBuffer).toHaveBeenCalledOnce();
    if (action === "hide") view.rerender(<ImportMicrosoftModal {...props} visible={false} />);
    else view.unmount();
    await act(async () => { read.resolve(new TextEncoder().encode("one@outlook.com----password").buffer); });
    expect(mocks.importResources).not.toHaveBeenCalled();
    expect(props.onImported).not.toHaveBeenCalled();
    expect(props.onCancel).not.toHaveBeenCalled();
    expect(mocks.toastError).not.toHaveBeenCalled();
  });

  it("keeps edit as one atomic PATCH and rejects a half OAuth credential pair", async () => {
    mocks.updateResource.mockResolvedValue({});
    const onCancel = vi.fn();
    const onSaved = vi.fn().mockResolvedValue(undefined);
    render(
      <EditMicrosoftModal
        onCancel={onCancel}
        onSaved={onSaved}
        owners={[owner]}
        target={resource()}
      />
    );

    await waitFor(() =>
      expect(screen.getByPlaceholderText("name@outlook.com")).toHaveValue(
        "resource-41@outlook.com"
      )
    );
    const credentialInputs = screen.getAllByPlaceholderText("Leave blank to keep current");
    fireEvent.change(credentialInputs[0], { target: { value: "new-password" } });
    fireEvent.change(credentialInputs[1], { target: { value: "new-client" } });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(mocks.updateResource).not.toHaveBeenCalled();
    expect(mocks.toastWarning).toHaveBeenCalledWith(
      "OAuth client ID and refresh token must be configured together."
    );

    fireEvent.change(credentialInputs[2], { target: { value: "new-refresh-token" } });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() =>
      expect(mocks.updateResource).toHaveBeenCalledWith(
        41,
        expect.objectContaining({
          credentials: {
            clientId: "new-client",
            password: "new-password",
            refreshToken: "new-refresh-token",
          },
          emailAddress: "resource-41@outlook.com",
          ownerId: 7,
          version: 3,
        })
      )
    );
    expect(mocks.updateResource.mock.calls[0]?.[1]).not.toHaveProperty(
      "bindingAddress"
    );
    await waitFor(() => expect(onSaved).toHaveBeenCalledTimes(1));
    expect(onCancel).toHaveBeenCalledTimes(1);
  });

  it("sends the auxiliary mailbox only when its normalized value changes", async () => {
    mocks.updateResource.mockResolvedValue({});
    render(
      <EditMicrosoftModal
        onCancel={vi.fn()}
        onSaved={vi.fn().mockResolvedValue(undefined)}
        owners={[owner]}
        target={resource()}
      />
    );

    const auxiliary = await screen.findByPlaceholderText("Optional recovery mailbox");
    fireEvent.change(auxiliary, { target: { value: " next@example.net " } });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));

    await waitFor(() => expect(mocks.updateResource).toHaveBeenCalled());
    expect(mocks.updateResource.mock.calls[0]?.[1]).toEqual(
      expect.objectContaining({ bindingAddress: "next@example.net" })
    );
  });

  it("keeps replacement credentials write-only and returns the refreshed detail", async () => {
    const nextDetail = { id: 41, version: 4 } as AdminMicrosoftResourceDetail;
    mocks.replaceCredentials.mockResolvedValue(nextDetail);
    const onCancel = vi.fn();
    const onSaved = vi.fn().mockResolvedValue(undefined);
    render(
      <ReplaceCredentialsModal
        onCancel={onCancel}
        onSaved={onSaved}
        target={resource()}
      />
    );

    expect(screen.getByPlaceholderText("Enter a replacement password")).toHaveValue("");
    expect(screen.getByPlaceholderText("Optional; must be submitted with a refresh token")).toHaveValue("");
    expect(screen.getByPlaceholderText("Optional; must be submitted with a client ID")).toHaveValue("");

    fireEvent.change(screen.getByPlaceholderText("Enter a replacement password"), {
      target: { value: "replacement-password" },
    });
    fireEvent.change(
      screen.getByPlaceholderText("Optional; must be submitted with a refresh token"),
      { target: { value: "replacement-client" } }
    );
    fireEvent.change(screen.getByPlaceholderText("Optional; must be submitted with a client ID"), {
      target: { value: "replacement-refresh" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Replace credentials" }));

    await waitFor(() =>
      expect(mocks.replaceCredentials).toHaveBeenCalledWith(41, {
        clientId: "replacement-client",
        password: "replacement-password",
        refreshToken: "replacement-refresh",
        version: 3,
      })
    );
    await waitFor(() => expect(onSaved).toHaveBeenCalledWith(nextDetail));
    expect(onCancel).toHaveBeenCalledTimes(1);
  });
});
