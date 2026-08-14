// @vitest-environment jsdom

import "@testing-library/jest-dom/vitest";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type {
  AdminICloudOwner,
  AdminICloudResourceDetail,
  AdminICloudResourceItem,
} from "@/lib/admin-icloud-api";

const mocks = vi.hoisted(() => ({
  alias: vi.fn(),
  batchByFilter: vi.fn(),
  batchByIds: vi.fn(),
  expirationByFilter: vi.fn(),
  expirationByIds: vi.fn(),
  importResources: vi.fn(),
  listResources: vi.fn(),
  modalConfirm: vi.fn(),
  permissions: {} as Record<string, boolean>,
  selectionNotification: vi.fn(),
  tasks: vi.fn(),
  translate: (key: string) => key,
  toastError: vi.fn(),
  toastInfo: vi.fn(),
  toastSuccess: vi.fn(),
  toastWarning: vi.fn(),
  updateResource: vi.fn(),
  validate: vi.fn(),
}));

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: mocks.translate }),
}));

vi.mock("@douyinfe/semi-ui", async () => {
  const ReactModule = await import("react");
  const passthrough = ({ children }: any) => <>{children}</>;
  const Button = ({ children, disabled, onClick }: any) => (
    <button disabled={disabled} onClick={onClick} type="button">{children}</button>
  );
  const dateInputValue = (value: unknown) =>
    value instanceof Date && Number.isFinite(value.getTime())
      ? value.toISOString().slice(0, 16)
      : undefined;
  const DatePicker = ({ "aria-label": ariaLabel, defaultValue, disabled, onChange, value }: any) => (
    <input
      aria-label={ariaLabel}
      defaultValue={value === undefined ? dateInputValue(defaultValue) : undefined}
      disabled={disabled}
      onChange={(event) =>
        onChange?.(event.target.value ? new Date(event.target.value) : null)
      }
      type="datetime-local"
      value={dateInputValue(value)}
    />
  );
  const Input = ({ disabled, onChange, placeholder, value }: any) => (
    <input
      disabled={disabled}
      onChange={(event) => onChange?.(event.target.value)}
      placeholder={placeholder}
      value={value ?? ""}
    />
  );
  const Modal = ({ children, okButtonProps, onCancel, onOk, okText, title, visible }: any) =>
    visible ? (
      <section aria-label={title} role="dialog">
        <h1>{title}</h1>
        {children}
        <button onClick={onCancel} type="button">Cancel</button>
        <button disabled={okButtonProps?.disabled} onClick={onOk} type="button">{okText}</button>
      </section>
    ) : null;
  (Modal as any).confirm = mocks.modalConfirm;
  const Select = ({ onChange, optionList = [], value }: any) => (
    <select aria-label="owner" onChange={(event) => onChange?.(event.target.value)} value={value ?? ""}>
      {optionList.map((option: any) => (
        <option disabled={option.disabled} key={String(option.value)} value={option.value}>
          {option.label}
        </option>
      ))}
    </select>
  );
  const Switch = ({ checked, onChange, ...props }: any) => (
    <input
      {...props}
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
  const TabPane = () => null;
  const Tabs = ({ children, onChange }: any) => (
    <div role="tablist">
      {ReactModule.Children.map(children, (child) =>
        ReactModule.isValidElement(child) ? (
          <button onClick={() => onChange?.((child.props as any).itemKey)} role="tab" type="button">
            {(child.props as any).tab}
          </button>
        ) : null
      )}
    </div>
  );
  (Tabs as any).TabPane = TabPane;
  return {
    Button,
    DatePicker,
    Dropdown: passthrough,
    Empty: passthrough,
    Input,
    Modal,
    Select,
    SideSheet: ({ children, title, visible }: any) => visible ? <section><h1>{title}</h1>{children}</section> : null,
    Space: passthrough,
    Spin: passthrough,
    Switch,
    Table: passthrough,
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

vi.mock("@douyinfe/semi-icons", () => ({ IconSearch: () => null }));
vi.mock("@douyinfe/semi-illustrations", () => ({
  IllustrationNoResult: () => null,
  IllustrationNoResultDark: () => null,
}));
vi.mock("@/components/semi/card-pro", () => ({ CardPro: ({ children }: any) => <>{children}</> }));
vi.mock("@/components/semi/card-pro-pagination", () => ({ createCardProPagination: () => null }));
vi.mock("@/components/semi/card-table", () => ({
  CardTable: ({ rowSelection }: any) => rowSelection ? (
    <button onClick={() => rowSelection.onChange([41])} type="button">Select test row</button>
  ) : <div data-testid="selection-disabled" />,
  DESKTOP_TABLE_SCROLL_Y: 400,
}));
vi.mock("@/components/semi/compact-mode-toggle", () => ({ CompactModeToggle: () => null }));
vi.mock("@/components/semi/copyable-table-text", () => ({
  CopyableTableText: ({ text }: any) => <span>{text}</span>,
}));
vi.mock("@/components/semi/statistic-filter-option", () => ({ StatisticFilterOption: () => null }));
vi.mock("@/context/auth-provider", () => ({
  hasPermissionKey: (_user: unknown, key: string) => mocks.permissions[key] ?? true,
  permissionKey: (resource: string, action: string) => `${resource}/${action}`,
  useAuth: () => ({ currentUser: null }),
}));
vi.mock("@/hooks/use-debounced-value", () => ({
  SHARED_SEARCH_DEBOUNCE_MS: 1,
  useDebouncedValue: (value: string) => [value, vi.fn()],
}));
vi.mock("@/hooks/use-is-mobile", () => ({ useIsMobile: () => false }));
vi.mock("@/hooks/use-shared-page-size", () => ({ useSharedPageSize: () => [20, vi.fn()] }));
vi.mock("@/lib/admin-icloud-api", async (importOriginal) => ({
  ...(await importOriginal<any>()),
  batchAdminICloudResourcesByFilter: mocks.batchByFilter,
  batchAdminICloudResourcesByIds: mocks.batchByIds,
  createAdminICloudAliases: mocks.alias,
  deleteAdminICloudResource: vi.fn(),
  disableAdminICloudResource: vi.fn(),
  enableAdminICloudResource: vi.fn(),
  getAdminICloudResourceDetail: vi.fn(),
  importAdminICloudResources: mocks.importResources,
  listAdminICloudAliases: vi.fn(),
  listAdminICloudOwners: vi.fn().mockResolvedValue([]),
  listAdminICloudResources: mocks.listResources,
  listAdminICloudTasks: mocks.tasks,
  publishAdminICloudResource: vi.fn(),
  recoverAdminICloudResource: vi.fn(),
  setAdminICloudResourcesExpirationByFilter: mocks.expirationByFilter,
  setAdminICloudResourcesExpirationByIds: mocks.expirationByIds,
  unpublishAdminICloudResource: vi.fn(),
  updateAdminICloudResource: mocks.updateResource,
  validateAdminICloudResource: mocks.validate,
}));
vi.mock("@/lib/iam-errors", () => ({
  getIamErrorMessage: (_t: unknown, _error: unknown, fallback: string) => fallback,
}));
vi.mock("./admin-microsoft/microsoft-meta", () => ({
  DRAWER_PANEL_HEIGHT: 500,
  DRAWER_TABLE_SCROLL_Y: 400,
  InfoItem: ({ label, value }: any) => <div><span>{label}</span><span>{value}</span></div>,
  OwnerIdentity: () => null,
  formatTime: (value: unknown) => String(value ?? "-"),
  ownerRoleLabel: (role: string) => role,
}));
vi.mock("./admin-microsoft/microsoft-detail-sheet", () => ({
  RelatedOrdersTable: () => <div>Orders panel</div>,
  ResourceMailsPanel: () => <div>Mailbox panel</div>,
  ServerPaginatedDrawerTable: () => <div>Tasks table</div>,
}));
vi.mock("./resources/date-range-filter", () => ({
  DATE_RANGE_DROPDOWN_CLASS: "date-range",
  createDateRangePresets: () => [],
  createdFromISOString: () => undefined,
  createdToISOString: () => undefined,
  normalizeDateRangeValue: () => [],
}));
vi.mock("./resources/use-selection-notification", () => ({
  useSelectionNotification: (options: unknown) => mocks.selectionNotification(options),
}));

import {
  default as AdminICloudEmails,
  EditICloudModal,
  ICloudDetailSheet,
  ICloudMaintenanceModal,
  ICloudTasksPanel,
  ImportICloudModal,
} from "./AdminICloudEmails";

const owner: AdminICloudOwner = {
  email: "owner@example.com",
  enabled: true,
  groupName: "Supply",
  id: 7,
  nickname: "Owner",
  role: "supplier",
};

function resource(): AdminICloudResourceItem {
  const now = "2026-08-08T00:00:00Z";
  return {
    aliasCount: 12,
    createdAt: now,
    expireAt: "2026-09-08T00:00:00Z",
    forSale: true,
    id: 41,
    lastAliasSyncAt: null,
    lastAllocatedAt: null,
    lastCheckedAt: now,
    lastMailSyncAt: null,
    lastSafeError: null,
    lastValidAt: now,
    newSession: {
      cooldownUntil: null,
      failures: 0,
      lastCheckedAt: now,
      lastValidAt: now,
      nextKeepaliveAt: null,
      status: "valid",
    },
    nextValidationAt: null,
    nextProvisionAt: null,
    oldSession: null,
    owner,
    primaryEmail: "main@icloud.com",
    status: "normal",
    updatedAt: now,
    version: 3,
  };
}

function resourceDetail(): AdminICloudResourceDetail {
  return {
    ...resource(),
    aliasLimit: 750,
    aliasProvisioning: false,
    aliasRemaining: 738,
    credentialRevision: 2,
    credentialUpdatedAt: "2026-08-08T00:00:00Z",
    validationFailures: 0,
    validationGeneration: 3,
  } as AdminICloudResourceDetail;
}

describe("admin iCloud modal workflows", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.permissions = {};
    mocks.alias.mockResolvedValue({ changed: true });
    mocks.importResources.mockResolvedValue({ imported: 1, skipped: 0, status: "imported" });
    mocks.updateResource.mockResolvedValue({});
    mocks.tasks.mockResolvedValue({ items: [], limit: 20, offset: 0, succeeded: 0, total: 0 });
    mocks.listResources.mockResolvedValue({
      aliasLimit: 750,
      facets: {
        forSale: { all: 0, no: 0, yes: 0 },
        status: { abnormal: 0, all: 0, deleted: 0, disabled: 0, normal: 0, pending: 0, validating: 0 },
      },
      items: [],
      limit: 20,
      offset: 0,
      total: 0,
    });
  });

  afterEach(() => cleanup());

  it("submits create-alias through the independent alias endpoint", async () => {
    render(
      <ICloudMaintenanceModal
        aliasLimit={750}
        onCancel={vi.fn()}
        onCompleted={vi.fn()}
        target={{ item: resource(), mode: "row" }}
      />,
    );

    fireEvent.click(screen.getByRole("button", { name: /Create alias/ }));
    fireEvent.click(screen.getByRole("button", { name: "Submit maintenance task" }));

    await waitFor(() => expect(mocks.alias).toHaveBeenCalledWith(41, 3));
    expect(mocks.validate).not.toHaveBeenCalled();
    expect(mocks.toastSuccess).toHaveBeenCalledWith("Alias creation batch submitted.");
  });

  it("hides fact-owner tabs when their read permissions are missing", () => {
    render(
      <ICloudDetailSheet
        aliasLimit={750}
        busyAction={null}
        canFetchMessages={false}
        canOperate={false}
        canReadMessages={false}
        canReadOrders={false}
        canReadTasks={false}
        canWrite={false}
        item={resourceDetail()}
        loading={false}
        onCancel={vi.fn()}
        onDelete={vi.fn()}
        onEdit={vi.fn()}
        onMaintain={vi.fn()}
        onRecover={vi.fn()}
        onReplaceCredentials={vi.fn()}
        onSetExpiration={vi.fn()}
        onToggleDisabled={vi.fn()}
        onTogglePublish={vi.fn()}
        refreshGeneration={0}
        resourceId={41}
      />,
    );

    expect(screen.getAllByRole("tab").map((tab) => tab.textContent)).toEqual([
      "Basic info",
      "Validation",
      "Aliases",
    ]);
  });

  it("exposes expiration as an operate-only resource action", () => {
    const onSetExpiration = vi.fn();
    render(
      <ICloudDetailSheet
        aliasLimit={750}
        busyAction={null}
        canFetchMessages={false}
        canOperate
        canReadMessages={false}
        canReadOrders={false}
        canReadTasks={false}
        canWrite={false}
        item={resourceDetail()}
        loading={false}
        onCancel={vi.fn()}
        onDelete={vi.fn()}
        onEdit={vi.fn()}
        onMaintain={vi.fn()}
        onRecover={vi.fn()}
        onReplaceCredentials={vi.fn()}
        onSetExpiration={onSetExpiration}
        onToggleDisabled={vi.fn()}
        onTogglePublish={vi.fn()}
        refreshGeneration={0}
        resourceId={41}
      />,
    );

    fireEvent.click(screen.getByRole("button", { name: "Set expiration" }));
    expect(onSetExpiration).toHaveBeenCalledWith(expect.objectContaining({ id: 41 }));
    expect(screen.queryByRole("button", { name: "Edit" })).not.toBeInTheDocument();
  });

  it("removes row selection without operate permission", async () => {
    mocks.permissions["core:resource/operate"] = false;
    render(<AdminICloudEmails />);

    expect(await screen.findByTestId("selection-disabled")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Select test row" })).not.toBeInTheDocument();
    await waitFor(() => expect(mocks.selectionNotification).toHaveBeenCalled());
    const calls = mocks.selectionNotification.mock.calls;
    expect(calls[calls.length - 1]?.[0]).toMatchObject({ selectedCount: 0 });
  });

  it("exposes expiration for selected resources", async () => {
    mocks.expirationByIds.mockResolvedValue({
      affected: 1,
      affectedResourceIds: [41],
      reasonCounts: [],
      requested: 1,
      skipped: 0,
      skippedResourceIds: [],
    });
    render(<AdminICloudEmails />);
    fireEvent.click(await screen.findByRole("button", { name: "Select test row" }));

    await waitFor(() => {
      const calls = mocks.selectionNotification.mock.calls;
      const options = calls[calls.length - 1]?.[0] as any;
      expect(options.selectedCount).toBe(1);
      expect(options.extraActions).toEqual(
        expect.arrayContaining([
          expect.objectContaining({ key: "expiration", labelKey: "Set expiration" }),
        ]),
      );
    });

    const selectionCalls = mocks.selectionNotification.mock.calls;
    const options = selectionCalls[selectionCalls.length - 1]?.[0] as any;
    options.extraActions.find((action: any) => action.key === "expiration").onClick();
    const confirmCalls = mocks.modalConfirm.mock.calls;
    const confirmOptions = confirmCalls[confirmCalls.length - 1]?.[0] as any;
    await act(async () => confirmOptions.onOk());

    expect(mocks.expirationByIds).toHaveBeenCalledWith([41], expect.any(String));
  });

  it("shows an inline retry when task history fails", async () => {
    mocks.tasks.mockRejectedValueOnce(new Error("offline"));
    render(<ICloudTasksPanel refreshGeneration={0} resourceId={41} />);

    expect(await screen.findByText("iCloud task load failed.")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));

    await waitFor(() => expect(mocks.tasks).toHaveBeenCalledTimes(2));
  });

  it("does not report a full alias resource as submitted", async () => {
    mocks.alias.mockResolvedValueOnce({ changed: false });
    const onCancel = vi.fn();
    const onCompleted = vi.fn();
    render(
      <ICloudMaintenanceModal
        aliasLimit={751}
        onCancel={onCancel}
        onCompleted={onCompleted}
        target={{ item: { ...resource(), aliasCount: 750 }, mode: "row" }}
      />,
    );

    fireEvent.click(screen.getByRole("button", { name: /Create alias/ }));
    fireEvent.click(screen.getByRole("button", { name: "Submit maintenance task" }));

    await waitFor(() => expect(mocks.toastInfo).toHaveBeenCalledWith("Alias target already reached."));
    expect(mocks.toastSuccess).not.toHaveBeenCalled();
    expect(onCompleted).toHaveBeenCalledWith(41);
    expect(onCancel).toHaveBeenCalled();
  });

  it("reads a selected TXT file and submits its contents", async () => {
    const onImported = vi.fn();
    render(
      <ImportICloudModal
        onCancel={vi.fn()}
        onImported={onImported}
        owners={[owner]}
        visible
      />,
    );

    await waitFor(() => expect(screen.getByLabelText("owner")).toHaveValue("7"));
    const fileButton = screen.getByRole("button", { name: "TXT file" });
    expect(fileButton).toHaveAttribute("aria-pressed", "false");
    fireEvent.click(fileButton);
    expect(fileButton).toHaveAttribute("aria-pressed", "true");

    const content =
      "main@icloud.com----app-password----curl --url 'https://p217-maildomainws.icloud.com.cn/v2/hme/list?dsid=123' \\\r\n  -H 'scnt: scnt-value' \\\r\n  -b 'X-APPLE-WEBAUTH-TOKEN=secret'";
    const normalizedContent =
      "main@icloud.com----app-password----curl --url 'https://p217-maildomainws.icloud.com.cn/v2/hme/list?dsid=123' -H 'scnt: scnt-value' -b 'X-APPLE-WEBAUTH-TOKEN=secret'";
    const file = new File([content], "icloud.txt", { type: "text/plain" });
    Object.defineProperty(file, "text", { value: vi.fn().mockResolvedValue(content) });
    fireEvent.change(document.querySelector('input[type="file"]')!, { target: { files: [file] } });
    fireEvent.click(screen.getByRole("button", { name: "Import" }));

    await waitFor(() => expect(mocks.importResources).toHaveBeenCalledWith(
      expect.objectContaining({
        content: normalizedContent,
        errorStrategy: "skip",
        expireAt: expect.any(String),
        ownerId: 7,
      }),
    ));
    await waitFor(() => expect(onImported).toHaveBeenCalledTimes(1));
  });

  it("submits a complete credential line from manual input", async () => {
    render(
      <ImportICloudModal
        onCancel={vi.fn()}
        onImported={vi.fn()}
        owners={[owner]}
        visible
      />,
    );

    await waitFor(() => expect(screen.getByLabelText("owner")).toHaveValue("7"));
    const content =
      "main@icloud.com----app-password----curl --url 'https://p217-maildomainws.icloud.com.cn/v2/hme/list?dsid=123' \\\n  -H 'scnt: scnt-value' \\\n  -b 'X-APPLE-WEBAUTH-TOKEN=secret'";
    const normalizedContent =
      "main@icloud.com----app-password----curl --url 'https://p217-maildomainws.icloud.com.cn/v2/hme/list?dsid=123' -H 'scnt: scnt-value' -b 'X-APPLE-WEBAUTH-TOKEN=secret'";
    fireEvent.change(screen.getByPlaceholderText("primary@icloud.com----app-password----curl ..."), {
      target: { value: content },
    });
    fireEvent.click(screen.getByRole("button", { name: "Import" }));

    await waitFor(() => expect(mocks.importResources).toHaveBeenCalledWith(
      expect.objectContaining({
        content: normalizedContent,
        errorStrategy: "skip",
        expireAt: expect.any(String),
        ownerId: 7,
      }),
    ));
  });

  it("updates a resource expiration from the edit modal", async () => {
    const expireAt = new Date(Date.now() + 60 * 24 * 60 * 60 * 1_000);
    render(
      <EditICloudModal
        canOperate
        onCancel={vi.fn()}
        onSaved={vi.fn()}
        owners={[owner]}
        target={resource()}
      />,
    );

    fireEvent.change(screen.getByLabelText("Resource expires at"), {
      target: { value: expireAt.toISOString().slice(0, 16) },
    });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));

    await waitFor(() => expect(mocks.updateResource).toHaveBeenCalledWith(
      41,
      expect.objectContaining({
        version: 3,
        expireAt: expect.any(String),
      }),
    ));
    const [, request] = mocks.updateResource.mock.calls[0] as [number, { expireAt: string }];
    expect(new Date(request.expireAt).getTime()).toBeGreaterThan(Date.now());
  });

  it("requires a complete credential line when replacing credentials", async () => {
    render(
      <EditICloudModal
        canOperate
        credentialsOnly
        onCancel={vi.fn()}
        onSaved={vi.fn()}
        owners={[owner]}
        target={resource()}
      />,
    );

    fireEvent.click(screen.getByRole("button", { name: "Replace credentials" }));

    expect(mocks.updateResource).not.toHaveBeenCalled();
    expect(mocks.toastWarning).toHaveBeenCalledWith(
      "Complete iCloud credential line is required.",
    );
  });

  it("normalizes Bash cURL continuations when replacing credentials", async () => {
    render(
      <EditICloudModal
        canOperate
        credentialsOnly
        onCancel={vi.fn()}
        onSaved={vi.fn()}
        owners={[owner]}
        target={resource()}
      />,
    );

    const content =
      "main@icloud.com----app-password----curl 'https://appleid.apple.com/account/manage/gs/ws/token' \\\n  -H 'cookie: myacinfo=secret' \\\n  -H 'scnt: scnt-value'";
    fireEvent.change(screen.getByPlaceholderText("email----appPassword----curl[----curl]"), {
      target: { value: content },
    });
    fireEvent.click(screen.getByRole("button", { name: "Replace credentials" }));

    await waitFor(() =>
      expect(mocks.updateResource).toHaveBeenCalledWith(
        41,
        expect.objectContaining({
          importLine:
            "main@icloud.com----app-password----curl 'https://appleid.apple.com/account/manage/gs/ws/token' -H 'cookie: myacinfo=secret' -H 'scnt: scnt-value'",
        }),
      ),
    );
  });
});
