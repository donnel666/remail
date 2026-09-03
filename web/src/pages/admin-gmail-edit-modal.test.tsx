// @vitest-environment jsdom

import "@testing-library/jest-dom/vitest";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { AdminGmailResourceItem } from "@/lib/admin-gmail-api";

const mocks = vi.hoisted(() => ({
  importResources: vi.fn(),
  replaceCredentials: vi.fn(),
  toastError: vi.fn(),
  toastInfo: vi.fn(),
  toastSuccess: vi.fn(),
  toastWarning: vi.fn(),
  updateResource: vi.fn(),
}));

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (key: string) => key }),
}));

vi.mock("@douyinfe/semi-ui", () => {
  const passthrough = ({ children }: any) => <>{children}</>;
  const Modal = ({ children, confirmLoading, okButtonProps, onCancel, onOk, okText, title, visible }: any) =>
    visible ? (
      <section aria-label={title} role="dialog">
        {children}
        <button onClick={onCancel} type="button">Cancel</button>
        <button disabled={confirmLoading || okButtonProps?.disabled} onClick={onOk} type="button">{okText}</button>
      </section>
    ) : null;
  (Modal as any).confirm = vi.fn();
  const Input = ({ disabled, onChange, placeholder, value }: any) => (
    <input
      disabled={disabled}
      onChange={(event) => onChange?.(event.target.value)}
      placeholder={placeholder}
      value={value ?? ""}
    />
  );
  const TextArea = ({ onChange, value, ...props }: any) => (
    <textarea {...props} onChange={(event) => onChange?.(event.target.value)} value={value ?? ""} />
  );
  const Tabs = ({ children }: any) => <>{children}</>;
  (Tabs as any).TabPane = passthrough;
  return {
    Button: ({ children, onClick }: any) => <button onClick={onClick}>{children}</button>,
    DatePicker: () => null,
    Dropdown: passthrough,
    Empty: passthrough,
    Input,
    Modal,
    SideSheet: passthrough,
    Space: passthrough,
    Spin: passthrough,
    Tabs,
    Tag: passthrough,
    TextArea,
    Toast: {
      error: mocks.toastError,
      info: mocks.toastInfo,
      success: mocks.toastSuccess,
      warning: mocks.toastWarning,
    },
    Tooltip: passthrough,
    Typography: { Text: passthrough },
  };
});

vi.mock("@/components/semi/admin-user-select", () => ({
  AdminUserSelect: () => null,
  ownersWithCurrentUserFirst: (owners: unknown[]) => owners,
}));

vi.mock("@/lib/admin-gmail-api", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/admin-gmail-api")>()),
  importAdminGmailResources: mocks.importResources,
  replaceAdminGmailCredentials: mocks.replaceCredentials,
  updateAdminGmailResource: mocks.updateResource,
}));

vi.mock("@/lib/iam-errors", () => ({
  getIamErrorMessage: () => "safe-error",
}));

import { EditGmailModal, ImportGmailModal } from "./AdminGmailEmails";

const target = {
  id: 7,
  version: 3,
  ownerUserId: 9,
  owner: {
    id: 9,
    email: "owner@example.com",
    nickname: "Owner",
    groupName: "Suppliers",
    role: "supplier",
    enabled: true,
  },
  email: "mail@gmail.com",
  bindingEmail: "recovery@example.com",
  status: "normal",
  forSale: false,
  passwordConfigured: true,
  twoFactorConfigured: true,
  appPasswordConfigured: true,
  credentialRevision: 2,
  credentialUpdatedAt: "2026-08-08T08:00:00Z",
  validationFailures: 0,
  createdAt: "2026-08-08T08:00:00Z",
  updatedAt: "2026-08-08T08:00:00Z",
} satisfies AdminGmailResourceItem;

describe("Gmail edit modal permissions", () => {
  beforeEach(() => vi.clearAllMocks());
  afterEach(cleanup);

  it("keeps one operate-only editor and submits normalized credentials through PUT", async () => {
    let resolveRequest!: () => void;
    mocks.replaceCredentials.mockReturnValue(
      new Promise<void>((resolve) => { resolveRequest = resolve; }),
    );
    const onCancel = vi.fn();
    const onSaved = vi.fn();

    render(
      <EditGmailModal
        canOperate
        canWrite={false}
        onCancel={onCancel}
        onSaved={onSaved}
        owners={[]}
        target={target}
      />,
    );

    expect(screen.queryByLabelText("Email *")).not.toBeInTheDocument();
    expect(screen.queryByText("Owner")).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(mocks.toastInfo).toHaveBeenCalledWith("No changes to save.");
    expect(mocks.replaceCredentials).not.toHaveBeenCalled();

    fireEvent.change(screen.getByLabelText("Password *"), {
      target: { value: "account-password" },
    });
    fireEvent.change(screen.getByLabelText("App password"), {
      target: { value: "ghsf peof qvby aqiq" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));

    await waitFor(() =>
      expect(mocks.replaceCredentials).toHaveBeenCalledWith(7, {
        appPassword: "ghsfpeofqvbyaqiq",
        password: "account-password",
        twoFactorSecret: "",
        version: 3,
      }),
    );
    expect(mocks.updateResource).not.toHaveBeenCalled();
    expect(screen.getByRole("button", { name: "Save" })).toBeDisabled();

    await act(async () => resolveRequest());
    await waitFor(() => expect(onSaved).toHaveBeenCalledOnce());
    expect(onCancel).toHaveBeenCalledOnce();
    expect(mocks.toastSuccess).toHaveBeenCalledWith("Gmail resource updated.");
  });

  it("reads a selected TXT file and submits its contents", async () => {
    mocks.importResources.mockResolvedValue({
      accepted: 1,
      imported: 1,
      skipped: 0,
      status: "imported",
    });
    const onCancel = vi.fn();
    const onImported = vi.fn().mockResolvedValue(undefined);
    render(
      <ImportGmailModal
        onCancel={onCancel}
        onImported={onImported}
        owners={[target.owner]}
        visible
      />,
    );

    await waitFor(() => expect(screen.getByRole("button", { name: "Import" })).toBeDisabled());
    fireEvent.click(screen.getByRole("button", { name: "TXT file" }));
    const content = "mail@gmail.com;password;abcdefghijklmnop";
    const file = new File([content], "gmail.txt", { type: "text/plain" });
    Object.defineProperty(file, "text", { value: vi.fn().mockResolvedValue(content) });
    fireEvent.change(screen.getByLabelText("Select TXT file"), {
      target: { files: [file] },
    });
    fireEvent.click(screen.getByRole("button", { name: "Import" }));

    await waitFor(() =>
      expect(mocks.importResources).toHaveBeenCalledWith({
        content,
        errorStrategy: "skip",
        ownerId: target.owner.id,
      }),
    );
    await waitFor(() => expect(onImported).toHaveBeenCalledOnce());
    expect(onCancel).toHaveBeenCalledOnce();
  });

  it("keeps the modal open and clears loading when credential replacement fails", async () => {
    mocks.replaceCredentials.mockRejectedValue(new Error("secret upstream detail"));
    const onCancel = vi.fn();

    render(
      <EditGmailModal
        canOperate
        canWrite={false}
        onCancel={onCancel}
        onSaved={vi.fn()}
        owners={[]}
        target={target}
      />,
    );
    fireEvent.change(screen.getByLabelText("Password *"), {
      target: { value: "account-password" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));

    await waitFor(() => expect(mocks.toastError).toHaveBeenCalledWith("safe-error"));
    expect(screen.getByRole("button", { name: "Save" })).toBeEnabled();
    expect(onCancel).not.toHaveBeenCalled();
  });
});
