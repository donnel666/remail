// @vitest-environment jsdom
import "@testing-library/jest-dom/vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { AdminDaumOwner, AdminDaumResourceItem } from "./admin-daum-types";

const mocks = vi.hoisted(() => ({
  importResources: vi.fn(), listOwners: vi.fn(), replaceCredentials: vi.fn(), updateResource: vi.fn(), waitForImport: vi.fn(),
  toastError: vi.fn(), toastWarning: vi.fn(), toastSuccess: vi.fn(), toastInfo: vi.fn(), permissions: [] as string[], translate: (key: string) => key,
}));
vi.mock("@/context/auth-provider", () => ({
  useAuth: () => ({ currentUser: { permissions: mocks.permissions } }),
  hasPermission: (user: { permissions: string[] }, resource: string, action: string) => user.permissions.includes(resource + ":" + action),
}));
vi.mock("react-i18next", () => ({ useTranslation: () => ({ t: mocks.translate }) }));
vi.mock("@douyinfe/semi-ui", () => {
  const Input = ({ onChange, placeholder, value, readOnly, disabled, mode }: any) => <input readOnly={readOnly} disabled={disabled} type={mode === "password" ? "password" : "text"} onChange={(event) => onChange?.(event.target.value)} placeholder={placeholder} value={value ?? ""} />;
  const Select = ({ children, value, onChange, disabled, "aria-label": ariaLabel }: any) => <select aria-label={ariaLabel} disabled={disabled} value={value} onChange={(event) => onChange(event.target.value)}>{children}</select>;
  Select.Option = ({ children, value }: any) => <option value={value}>{children}</option>;
  return {
    Input, Select,
    Checkbox: ({ children, checked, disabled, onChange }: any) => <label><input type="checkbox" checked={checked} disabled={disabled} onChange={(event) => onChange({ target: { checked: event.target.checked } })} />{children}</label>,
    InputNumber: ({ value, onChange }: any) => <input value={value} onChange={(event) => onChange(event.target.value)} />,
    Switch: ({ checked, onChange, disabled, "aria-label": ariaLabel }: any) => <input type="checkbox" aria-label={ariaLabel} disabled={disabled} checked={checked} onChange={(event) => onChange(event.target.checked)} />,
    TextArea: ({ onChange, placeholder, value, disabled }: any) => <textarea disabled={disabled} placeholder={placeholder} value={value} onChange={(event) => onChange(event.target.value)} />,
    Modal: ({ children, visible, title, onOk, okText, onCancel, cancelText, confirmLoading, okButtonProps }: any) => visible ? <section role="dialog" aria-label={title}>{children}<button disabled={confirmLoading || okButtonProps?.disabled} onClick={onOk}>{okText}</button><button onClick={onCancel}>{cancelText}</button></section> : null,
    Typography: { Text: ({ children }: any) => <span>{children}</span> },
    Toast: { error: mocks.toastError, warning: mocks.toastWarning, success: mocks.toastSuccess, info: mocks.toastInfo },
  };
});
vi.mock("@/components/semi/admin-user-select", () => ({
  AdminUserSelect: ({ value, onChange, options, disabled }: any) => <select disabled={disabled} value={value ?? ""} onChange={(event) => onChange(Number(event.target.value))}>
    <option value="">Owner</option>{options.map((item: any) => <option key={item.value} value={item.value}>{item.label}</option>)}
  </select>,
}));
vi.mock("@/lib/admin-daum-api", () => ({
  importAdminDaumResources: mocks.importResources, listAdminDaumOwners: mocks.listOwners,
  replaceAdminDaumCredentials: mocks.replaceCredentials, updateAdminDaumResource: mocks.updateResource,
  waitForAdminDaumResourceImport: mocks.waitForImport, getAdminDaumResourceDetail: vi.fn(),
}));
vi.mock("@/lib/iam-errors", () => ({
  getIamErrorMessage: (_t: unknown, _error: unknown, fallback: string) => fallback,
  getApiErrorBodyMessage: (_t: unknown, error: {message: string}) => error.message,
}));
vi.mock("./daum-meta", () => ({ InfoItem: ({ label, value }: any) => <div>{label}: {value}</div>, ownerRoleLabel: (role: string) => role }));
vi.mock("../resources/daum-import-result-panel", () => ({ DaumImportResultPanel: ({ result }: any) => <output>{result.status} #{result.importId}</output> }));
import { EditDaumModal, ImportDaumModal, ReplaceCredentialsModal } from "./daum-modals";

const owner: AdminDaumOwner = { id: 7, email: "owner@example.com", enabled: true, nickname: "Owner", role: "supplier", groupName: "Suppliers" };
const resource: AdminDaumResourceItem = {
  id: 41, version: 3, ownerUserId: 7, owner, email: "user@daum.net", emailAddress: "user@daum.net", suffix: "daum.net",
  proxyBindings: [], recentTasks: [], activeTask: null, status: "normal", forSale: false, longLived: true, qualityScore: 80, protocol: "imap",
  passwordConfigured: true, loginPasswordConfigured: true, appPasswordConfigured: true, credentialRevision: 2, credentialUpdatedAt: "2026-10-01T00:00:00Z",
  bindingAddress: "helper@outlook.com", kakaoEmail: "account@kakao.example", bindingStatus: "linked", pickupUrlConfigured: true, aliases: [],
  validationGeneration: 1, validationFailures: 0, createdAt: "2026-10-01T00:00:00Z", updatedAt: "2026-10-01T00:00:00Z",
};
beforeEach(() => {
  vi.clearAllMocks();
  mocks.permissions = ["core:resource:write", "core:resource:operate", "mailtransport:binding:write"];
  mocks.listOwners.mockResolvedValue([owner]);
  mocks.importResources.mockResolvedValue({ importId: 10, status: "imported", imported: 1, skipped: 0 });
  mocks.replaceCredentials.mockResolvedValue({ ...resource, version: 4 });
  mocks.updateResource.mockResolvedValue({ ...resource, version: 4 });
});
afterEach(cleanup);

describe("Daum admin credential and import forms", () => {
  it("submits mixed raw import text and POP3 explicitly, keeping result details available", async () => {
    const onImported = vi.fn();
    render(<ImportDaumModal visible owners={[owner]} onCancel={vi.fn()} onImported={onImported} />);
    const content = "\uFEFF user@daum.net----  app  \r\nsecond@hanmail.net---- login ---- app2 \r\nthird@hanmail2.net----login----app----https://remail.example/v1/pickup?email=helper%40outlook.com&token=private----account@kakao.example";
    fireEvent.change(screen.getByPlaceholderText("Daum import formats"), { target: { value: content } });
    fireEvent.change(screen.getByRole("combobox", { name: "Mail protocol" }), { target: { value: "pop3" } });
    fireEvent.click(screen.getByRole("button", { name: "Import" }));
    await waitFor(() => expect(mocks.importResources).toHaveBeenCalledWith(
      { content: content.replace(/\r\n/g, "\n"), ownerId: 7, protocol: "pop3", errorStrategy: "skip", longLived: true }, expect.any(AbortSignal), expect.any(String),
    ));
    await waitFor(() => expect(onImported).toHaveBeenCalledTimes(1));
    expect(screen.getByText("imported #10")).toBeInTheDocument();
    expect(screen.getByPlaceholderText("Daum import formats")).toHaveValue("");
  });

  it("submits invalid raw lines in abort mode so server diagnostics remain reviewable", async () => {
    mocks.importResources.mockResolvedValueOnce({ importId: 10, status: "failed", imported: 0, skipped: 0, lastSafeError: "Invalid import format." });
    render(<ImportDaumModal visible owners={[owner]} onCancel={vi.fn()} onImported={vi.fn()} />);
    fireEvent.change(screen.getByPlaceholderText("Daum import formats"), { target: { value: "user@daum.net----login----app----ambiguous" } });
    fireEvent.click(screen.getByRole("button", { name: "Abort on error" }));
    fireEvent.click(screen.getByRole("button", { name: "Import" }));
    await waitFor(() => expect(mocks.toastError).toHaveBeenCalledWith("Resource import failed."));
    expect(mocks.importResources).toHaveBeenCalledWith(expect.objectContaining({ content: "user@daum.net----login----app----ambiguous", errorStrategy: "abort" }), expect.any(AbortSignal), expect.any(String));
    expect(screen.getByText("failed #10")).toBeInTheDocument();
  });

  it("requires APP password, keeps login password absent unless supplied, and clears secrets on close", async () => {
    const onCancel = vi.fn();
    render(<ReplaceCredentialsModal target={resource} onCancel={onCancel} onSaved={vi.fn()} />);
    expect(screen.getByPlaceholderText("Leave blank to keep the existing login password")).toHaveValue("");
    fireEvent.click(screen.getByRole("button", { name: "Replace credentials" }));
    expect(mocks.replaceCredentials).not.toHaveBeenCalled();
    expect(mocks.toastWarning).toHaveBeenCalledWith("Daum APP password is required.");
    fireEvent.change(screen.getByPlaceholderText("Enter a replacement APP password"), { target: { value: "  new app  " } });
    fireEvent.click(screen.getByRole("button", { name: "Replace credentials" }));
    await waitFor(() => expect(mocks.replaceCredentials).toHaveBeenCalledWith(41, { version: 3, appPassword: "  new app  " }));
    await waitFor(() => expect(onCancel).toHaveBeenCalledOnce());
    expect(screen.getByPlaceholderText("Enter a replacement APP password")).toHaveValue("");
  });

  it("preserves existing auxiliary details when ordinary metadata is saved", async () => {
    render(<EditDaumModal target={resource} owners={[owner]} onCancel={vi.fn()} onSaved={vi.fn()} />);
    expect(screen.getByPlaceholderText("Leave blank to keep the existing pickup URL")).toHaveValue("");
    expect(screen.getByLabelText("Kakao account email")).toHaveValue("account@kakao.example");
    expect(screen.getByText(/Auxiliary pickup email: helper@outlook.com/)).toBeInTheDocument();
    fireEvent.change(screen.getByPlaceholderText("Leave blank to keep the existing pickup URL"), { target: { value: "   " } });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(mocks.updateResource).toHaveBeenCalledWith(41, { version: 3, longLived: true, qualityScore: 80 }));
  });

  it("edits the independent Kakao account and pickup URL without sending unchanged ownership or protocol", async () => {
    render(<EditDaumModal target={resource} owners={[owner]} onCancel={vi.fn()} onSaved={vi.fn()} />);
    fireEvent.change(screen.getByLabelText("Kakao account email"), { target: { value: "new@kakao.example" } });
    const pickupUrl = "https://remail.example/v1/pickup?email=helper%40outlook.com&token=replacement";
    fireEvent.change(screen.getByPlaceholderText("Leave blank to keep the existing pickup URL"), { target: { value: pickupUrl } });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(mocks.updateResource).toHaveBeenCalledWith(41, { version: 3, longLived: true, qualityScore: 80, kakaoEmail: "new@kakao.example", pickupUrl }));
  });
  it("retries a lost upload response with the same idempotency key and default IMAP", async () => {
    mocks.importResources.mockRejectedValueOnce(new Error("Response lost"));
    render(<ImportDaumModal visible owners={[owner]} onCancel={vi.fn()} onImported={vi.fn()} />);
    fireEvent.change(screen.getByPlaceholderText("Daum import formats"), { target: { value: "user@daum.net----app" } });
    const submit = screen.getByRole("button", { name: "Import" });
    fireEvent.click(submit);
    await waitFor(() => expect(mocks.toastError).toHaveBeenCalled());
    await waitFor(() => expect(submit).toBeEnabled());
    fireEvent.click(submit);
    await waitFor(() => expect(mocks.importResources).toHaveBeenCalledTimes(2));
    expect(mocks.importResources.mock.calls[0][0].protocol).toBe("imap");
    expect(mocks.importResources.mock.calls[0][2]).toBe(mocks.importResources.mock.calls[1][2]);
  });

  it("includes ownership and protocol only when the administrator changes them", async () => {
    const other = { ...owner, id: 8, email: "other@example.com" };
    render(<EditDaumModal target={resource} owners={[owner, other]} onCancel={vi.fn()} onSaved={vi.fn()} />);
    fireEvent.change(screen.getByRole("combobox", { name: "Owner" }), { target: { value: "8" } });
    fireEvent.change(screen.getByRole("combobox", { name: "Mail protocol" }), { target: { value: "pop3" } });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(mocks.updateResource).toHaveBeenCalledWith(41, {
      version: 3, ownerId: 8, protocol: "pop3", longLived: true, qualityScore: 80,
    }));
  });

  it("keeps direct auxiliary address edits available only without an existing pickup URL", async () => {
    render(<EditDaumModal target={{ ...resource, pickupUrlConfigured: false }} owners={[owner]} onCancel={vi.fn()} onSaved={vi.fn()} />);
    fireEvent.change(screen.getByLabelText("Auxiliary pickup email"), { target: { value: "direct@example.com" } });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(mocks.updateResource).toHaveBeenCalledWith(41, {
      version: 3, longLived: true, qualityScore: 80, bindingAddress: "direct@example.com",
    }));
  });

  it("saves corrected identity, sale status and write-only credentials in one PATCH", async () => {
    render(<EditDaumModal target={resource} owners={[owner]} onCancel={vi.fn()} onSaved={vi.fn()} />);
    fireEvent.change(screen.getByLabelText("Email *"), { target: { value: "corrected@daum.net" } });
    fireEvent.click(screen.getByRole("checkbox", { name: "Public sale" }));
    fireEvent.change(screen.getByLabelText("Login password"), { target: { value: "  login  " } });
    fireEvent.change(screen.getByLabelText("APP password"), { target: { value: "  app  " } });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(mocks.updateResource).toHaveBeenCalledWith(41, {
      version: 3, longLived: true, qualityScore: 80, email: "corrected@daum.net", forSale: true, password: "  login  ", appPassword: "  app  ",
    }));
    expect(mocks.replaceCredentials).not.toHaveBeenCalled();
  });

  it("explicitly unlinks both auxiliary fields without clearing the independent Kakao account", async () => {
    render(<EditDaumModal target={resource} owners={[owner]} onCancel={vi.fn()} onSaved={vi.fn()} />);
    fireEvent.click(screen.getByRole("checkbox", { name: "Unlink auxiliary mailbox" }));
    expect(screen.getByPlaceholderText("Leave blank to keep the existing pickup URL")).toBeDisabled();
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(mocks.updateResource).toHaveBeenCalledWith(41, {
      version: 3, longLived: true, qualityScore: 80, bindingAddress: null, pickupUrl: null,
    }));
  });

  it("permits auxiliary edits with binding/write without including operate-only fields", async () => {
    mocks.permissions = ["core:resource:write", "mailtransport:binding:write"];
    render(<EditDaumModal target={resource} owners={[owner]} onCancel={vi.fn()} onSaved={vi.fn()} />);
    expect(screen.getByLabelText("APP password")).toBeDisabled();
    expect(screen.getByRole("checkbox", { name: "Public sale" })).toBeDisabled();
    expect(screen.getByRole("combobox", { name: "Mail protocol" })).toBeDisabled();
    const pickupUrl = "https://remail.example/pickup?email=aux%40example.com&token=replacement";
    fireEvent.change(screen.getByPlaceholderText("Leave blank to keep the existing pickup URL"), { target: { value: pickupUrl } });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(mocks.updateResource).toHaveBeenCalledWith(41, { version: 3, longLived: true, qualityScore: 80, pickupUrl }));
  });

});
