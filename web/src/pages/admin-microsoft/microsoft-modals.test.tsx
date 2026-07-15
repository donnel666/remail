// @vitest-environment jsdom

import "@testing-library/jest-dom/vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
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
  toastSuccess: vi.fn(),
  toastWarning: vi.fn(),
  updateResource: vi.fn(),
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
  const Modal = ({ children, onCancel, onOk, okText, title, visible }: any) =>
    visible ? (
      <section aria-label={title} role="dialog">
        <h1>{title}</h1>
        {children}
        <button onClick={onCancel} type="button">
          Cancel
        </button>
        <button onClick={onOk} type="button">
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
    render(
      <ImportMicrosoftModal
        onCancel={onCancel}
        onImported={onImported}
        owners={[owner]}
        visible
      />
    );

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
      })
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
