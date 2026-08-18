// @vitest-environment jsdom

import "@testing-library/jest-dom/vitest";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type {
  AdminICloudImportPreparation,
  AdminICloudOnboardingImportResponse,
  AdminICloudOnboardingTask,
  AdminICloudOwner,
  AdminICloudResourceDetail,
  AdminICloudResourceItem,
} from "@/lib/admin-icloud-api";

const mocks = vi.hoisted(() => ({
  activate: vi.fn(),
  alias: vi.fn(),
  batchByFilter: vi.fn(),
  batchByIds: vi.fn(),
  expirationByFilter: vi.fn(),
  expirationByIds: vi.fn(),
  createPreparation: vi.fn(),
  confirmFamilyReset: vi.fn(),
  getPreparation: vi.fn(),
  getOnboarding: vi.fn(),
  importOnboarding: vi.fn(),
  importResources: vi.fn(),
  listResources: vi.fn(),
  listOnboardingImports: vi.fn(),
  modalConfirm: vi.fn(),
  permissions: {} as Record<string, boolean>,
  resourceMailsPanel: vi.fn(),
  retryPostFamily: vi.fn(),
  selectionNotification: vi.fn(),
  tasks: vi.fn(),
  submitSmsCode: vi.fn(),
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
  const TextArea = ({ onChange, ...props }: any) => (
    <textarea
      onChange={(event) => onChange?.(event.target.value)}
      {...props}
      value={props.value ?? ""}
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
vi.mock("@/components/semi/card-pro", () => ({
  CardPro: ({ actionsArea, children }: any) => <>{actionsArea}{children}</>,
}));
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
  activateAdminICloudResource: mocks.activate,
  batchAdminICloudResourcesByFilter: mocks.batchByFilter,
  batchAdminICloudResourcesByIds: mocks.batchByIds,
  confirmAdminICloudOnboardingFamilyReset: mocks.confirmFamilyReset,
  createAdminICloudAliases: mocks.alias,
  createAdminICloudImportPreparation: mocks.createPreparation,
  deleteAdminICloudResource: vi.fn(),
  disableAdminICloudResource: vi.fn(),
  enableAdminICloudResource: vi.fn(),
  getAdminICloudResourceDetail: vi.fn(),
  getAdminICloudImportPreparation: mocks.getPreparation,
  getAdminICloudOnboardingImport: mocks.getOnboarding,
  importAdminICloudResources: mocks.importResources,
  importAdminICloudOnboardingAccounts: mocks.importOnboarding,
  listAdminICloudAliases: vi.fn(),
  listAdminICloudOnboardingImports: mocks.listOnboardingImports,
  listAdminICloudOwners: vi.fn().mockResolvedValue([]),
  listAdminICloudResources: mocks.listResources,
  listAdminICloudTasks: mocks.tasks,
  publishAdminICloudResource: vi.fn(),
  recoverAdminICloudResource: vi.fn(),
  retryAdminICloudOnboardingPostFamily: mocks.retryPostFamily,
  setAdminICloudResourcesExpirationByFilter: mocks.expirationByFilter,
  setAdminICloudResourcesExpirationByIds: mocks.expirationByIds,
  submitAdminICloudOnboardingSmsCode: mocks.submitSmsCode,
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
  renderTaskStatusTag: (status: string) => <span>{status}</span>,
  taskKindLabel: (kind: string) => kind,
}));
vi.mock("./admin-microsoft/microsoft-detail-sheet", () => ({
  RelatedOrdersTable: () => <div>Orders panel</div>,
  ResourceMailsPanel: (props: any) => {
    mocks.resourceMailsPanel(props);
    return <div>Mailbox panel</div>;
  },
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
  ICloudOnboardingModal,
  ICloudOnboardingTaskAction,
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
    accountRole: "primary",
    aliasCount: 12,
    boundPhoneCountryCode: "1",
    boundPhoneNumber: "+15550001111",
    boundPhoneSource: "kitesim",
    countryCode: "US",
    createdAt: now,
    expireAt: "2026-09-08T00:00:00Z",
    forSale: true,
    familyChildCount: 2,
    familyChildLimit: 5,
    familyInviteUrl: "https://www.icloud.com/iclouddrive/family-invite",
    familyPrimaryResourceId: null,
    familySyncStatus: "ready",
    familySyncedAt: now,
    id: 41,
    icloudOpened: true,
    kitesimPhoneId: 91,
    lastAliasSyncAt: null,
    lastAllocatedAt: null,
    lastCheckedAt: now,
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
    region: "美国区",
    selectedForwardTo: "inbox@relay.example",
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
    onboardingTask: null,
    validationFailures: 0,
    validationGeneration: 3,
    refreshTask: null,
  } as AdminICloudResourceDetail;
}

function onboardingTask(
  overrides: Partial<AdminICloudOnboardingTask> = {},
): AdminICloudOnboardingTask {
  const now = "2026-08-16T08:00:00Z";
  return {
    accountRole: "child",
    attempts: 0,
    boundPhoneCountryCode: "1",
    boundPhoneNumber: "+15550001111",
    boundPhoneSource: "manual",
    countryCode: "US",
    createdAt: now,
    familyPrimaryEmail: "primary@example.com",
    familyPrimaryResourceId: 41,
    finishedAt: null,
    icloudOpened: true,
    id: 31,
    kitesimPhoneId: null,
    lineNumber: 1,
    maxAttempts: 5,
    needsFamilyReset: false,
    needsICloudActivation: false,
    needsManualCode: true,
    needsPostFamilyRecovery: false,
    nextAttemptAt: null,
    primaryEmail: "child@example.com",
    region: "美国区",
    resourceId: null,
    stage: "sms_wait",
    startedAt: now,
    status: "waiting",
    taskKind: "onboarding",
    updatedAt: now,
    ...overrides,
  };
}

function onboardingResponse(
  overrides: Partial<AdminICloudOnboardingImportResponse> = {},
): AdminICloudOnboardingImportResponse {
  const now = "2026-08-16T08:00:00Z";
  return {
    accepted: 1,
    completed: 1,
    createdAt: now,
    failed: 0,
    importId: 23,
    requestId: "request-23",
    reused: false,
    status: "completed",
    tasks: [
      onboardingTask({
        finishedAt: now,
        needsManualCode: false,
        stage: "completed",
        status: "completed",
      }),
    ],
    updatedAt: now,
    waiting: 0,
    ...overrides,
  };
}

function importPreparation(
  overrides: Partial<AdminICloudImportPreparation> = {},
): AdminICloudImportPreparation {
  return {
    id: 31,
    forwardToEmail: "icloud_test@relay.example",
    status: "code_received",
    verificationCode: "088556",
    expiresAt: "2026-08-15T08:30:00Z",
    createdAt: "2026-08-15T08:00:00Z",
    ...overrides,
  };
}

describe("admin iCloud modal workflows", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.permissions = {};
    mocks.activate.mockResolvedValue({ changed: true, version: 4 });
    mocks.alias.mockResolvedValue({ changed: true });
    mocks.createPreparation.mockResolvedValue(importPreparation());
    mocks.confirmFamilyReset.mockResolvedValue(onboardingTask());
    mocks.getPreparation.mockResolvedValue(importPreparation());
    mocks.importResources.mockResolvedValue({ imported: 1, skipped: 0, status: "imported" });
    mocks.importOnboarding.mockResolvedValue(onboardingResponse());
    mocks.getOnboarding.mockResolvedValue(onboardingResponse());
    mocks.retryPostFamily.mockResolvedValue(onboardingTask({ needsPostFamilyRecovery: false }));
    mocks.submitSmsCode.mockResolvedValue(onboardingTask({ needsManualCode: false }));
    mocks.updateResource.mockResolvedValue({});
    mocks.tasks.mockResolvedValue({ items: [], limit: 20, offset: 0, succeeded: 0, total: 0 });
    mocks.listResources.mockResolvedValue({
      aliasLimit: 750,
      forwardingSuffixes: ["relay.example"],
      facets: {
        forSale: { all: 0, no: 0, yes: 0 },
        status: { abnormal: 0, all: 0, deleted: 0, disabled: 0, normal: 0, pending: 0, validating: 0 },
      },
      items: [],
      limit: 20,
      offset: 0,
      total: 0,
    });
    mocks.listOnboardingImports.mockResolvedValue({
      items: [], limit: 100, offset: 0, succeeded: 0, total: 0,
    });
  });

  afterEach(() => {
    vi.useRealTimers();
    cleanup();
  });

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

  it("queues an old Cookie refresh after iCloud is enabled manually", async () => {
    render(
      <ICloudMaintenanceModal
        aliasLimit={750}
        onCancel={vi.fn()}
        onCompleted={vi.fn()}
        target={{ item: { ...resource(), icloudOpened: false }, mode: "row" }}
      />,
    );

    fireEvent.click(screen.getByRole("button", { name: /Fetch old Cookie/ }));
    fireEvent.click(screen.getByRole("button", { name: "Submit maintenance task" }));

    await waitFor(() => expect(mocks.activate).toHaveBeenCalledWith(41, 3));
    expect(mocks.toastSuccess).toHaveBeenCalledWith("Old Cookie refresh submitted.");
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
        onRefresh={vi.fn()}
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

  it("opens the exact iCloud auxiliary mailbox from resource details", async () => {
    render(
      <ICloudDetailSheet
        aliasLimit={750}
        busyAction={null}
        canFetchMessages={false}
        canOperate={false}
        canReadMessages
        canReadOrders={false}
        canReadTasks={false}
        canWrite={false}
        item={resourceDetail()}
        loading={false}
        onCancel={vi.fn()}
        onDelete={vi.fn()}
        onEdit={vi.fn()}
        onMaintain={vi.fn()}
        onRefresh={vi.fn()}
        onRecover={vi.fn()}
        onReplaceCredentials={vi.fn()}
        onSetExpiration={vi.fn()}
        onToggleDisabled={vi.fn()}
        onTogglePublish={vi.fn()}
        refreshGeneration={0}
        resourceId={41}
      />,
    );

    fireEvent.click(screen.getByRole("tab", { name: "Auxiliary mailbox" }));
    await waitFor(() =>
      expect(mocks.resourceMailsPanel).toHaveBeenCalledWith(
        expect.objectContaining({
          auxiliary: true,
          resourceId: 41,
          resourceType: "icloud",
        }),
      ),
    );
    expect(screen.getByText("inbox@relay.example")).toBeInTheDocument();
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
        onRefresh={vi.fn()}
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
    expect(screen.getByRole("button", { name: "Import" })).toBeEnabled();
    expect(screen.queryByRole("button", { name: "Select test row" })).not.toBeInTheDocument();
    await waitFor(() => expect(mocks.selectionNotification).toHaveBeenCalled());
    const calls = mocks.selectionNotification.mock.calls;
    expect(calls[calls.length - 1]?.[0]).toMatchObject({ selectedCount: 0 });
  });

  it("falls back to legacy import when governance tasks cannot be read", async () => {
    mocks.permissions["governance:task/read"] = false;
    render(<AdminICloudEmails />);

    fireEvent.click(await screen.findByRole("button", { name: "Import" }));
    expect(await screen.findByRole("dialog", { name: "Import iCloud Emails" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Automated eSIM onboarding" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Legacy double-cURL import" })).toHaveAttribute(
      "aria-pressed",
      "true",
    );
  });

  it("opens automated onboarding by default and can switch to legacy double-cURL import", async () => {
    render(<AdminICloudEmails />);

    fireEvent.click(await screen.findByRole("button", { name: "Import" }));
    expect(
      await screen.findByRole("dialog", { name: "Automatic Apple onboarding" }),
    ).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Automated eSIM onboarding" })).toHaveAttribute(
      "aria-pressed",
      "true",
    );

    fireEvent.click(screen.getByRole("button", { name: "Legacy double-cURL import" }));
    expect(await screen.findByRole("dialog", { name: "Import iCloud Emails" })).toBeInTheDocument();
    expect(
      screen.queryByRole("dialog", { name: "Automatic Apple onboarding" }),
    ).not.toBeInTheDocument();
  });

  it("does not refresh the resource list while a Cookie check is pending", async () => {
    vi.useFakeTimers();
    const pending = resource();
    pending.newSession = { ...pending.newSession!, status: "unchecked" };
    pending.nextProvisionAt = "2026-08-15T04:00:00Z";
    mocks.listResources.mockResolvedValue({
      aliasLimit: 750,
      forwardingSuffixes: ["relay.example"],
      facets: {
        forSale: { all: 1, no: 0, yes: 1 },
        status: { abnormal: 0, all: 1, deleted: 0, disabled: 0, normal: 1, pending: 0, validating: 0 },
      },
      items: [pending],
      limit: 20,
      offset: 0,
      total: 1,
    });

    render(<AdminICloudEmails />);
    await act(async () => {
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(mocks.listResources).toHaveBeenCalledTimes(2);

    await act(async () => {
      await vi.advanceTimersByTimeAsync(3000);
    });
    expect(mocks.listResources).toHaveBeenCalledTimes(2);
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
    render(
      <ICloudTasksPanel
        canOperate={false}
        item={resourceDetail()}
        onRefresh={vi.fn()}
        refreshGeneration={0}
      />,
    );

    expect(await screen.findByText("iCloud task load failed.")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));

    await waitFor(() => expect(mocks.tasks).toHaveBeenCalledTimes(2));
  });

  it("polls active tasks and stops after the task reaches a terminal state", async () => {
    vi.useFakeTimers();
    mocks.tasks
      .mockResolvedValueOnce({
        items: [{ status: "running", taskId: "alias:41" }],
        limit: 20,
        offset: 0,
        succeeded: 0,
        total: 1,
      })
      .mockResolvedValueOnce({
        items: [{ status: "succeeded", taskId: "alias:41" }],
        limit: 20,
        offset: 0,
        succeeded: 1,
        total: 1,
      });
    render(
      <ICloudTasksPanel
        canOperate={false}
        item={resourceDetail()}
        onRefresh={vi.fn()}
        refreshGeneration={0}
      />,
    );

    await act(async () => {
      await Promise.resolve();
    });
    expect(mocks.tasks).toHaveBeenCalledTimes(1);

    await act(async () => {
      await vi.advanceTimersByTimeAsync(1_500);
    });
    expect(mocks.tasks).toHaveBeenCalledTimes(2);

    await act(async () => {
      await vi.advanceTimersByTimeAsync(1_500);
    });
    expect(mocks.tasks).toHaveBeenCalledTimes(2);
  });

  it("submits alias provisioning from task details", async () => {
    const onRefresh = vi.fn();
    render(
      <ICloudTasksPanel
        canOperate
        item={resourceDetail()}
        onRefresh={onRefresh}
        refreshGeneration={0}
      />,
    );

    fireEvent.click(screen.getByRole("button", { name: "Create alias" }));

    await waitFor(() => expect(mocks.alias).toHaveBeenCalledWith(41, 3));
    expect(onRefresh).toHaveBeenCalled();
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

    await screen.findByText("088556");
    fireEvent.click(screen.getByRole("button", { name: "Next" }));
    await waitFor(() => expect(screen.getByLabelText("owner")).toHaveValue("7"));
    const fileButton = screen.getByRole("button", { name: "TXT file" });
    expect(fileButton).toHaveAttribute("aria-pressed", "false");
    fireEvent.click(fileButton);
    expect(fileButton).toHaveAttribute("aria-pressed", "true");

    const content =
      "main@icloud.com----curl --url 'https://p217-maildomainws.icloud.com.cn/v2/hme/list?dsid=123' \\\r\n  -H 'scnt: scnt-value' \\\r\n  -b 'X-APPLE-WEBAUTH-TOKEN=secret'";
    const normalizedContent =
      "main@icloud.com----curl --url 'https://p217-maildomainws.icloud.com.cn/v2/hme/list?dsid=123' -H 'scnt: scnt-value' -b 'X-APPLE-WEBAUTH-TOKEN=secret'";
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
        preparationId: 31,
      }),
    ));
    await waitFor(() => expect(onImported).toHaveBeenCalledTimes(1));
  });

  it("shows the prepared address and complete cURL guidance", async () => {
    render(
      <ImportICloudModal
        onCancel={vi.fn()}
        onImported={vi.fn()}
        owners={[owner]}
        visible
      />,
    );

    const dialog = screen.getByRole("dialog");
    expect(await screen.findByText("icloud_test@relay.example")).toBeInTheDocument();
    expect(dialog).toHaveTextContent("088556");
    fireEvent.click(screen.getByRole("button", { name: "Next" }));
    expect(screen.getByText("https://appleid.apple.com/account/manage/email/private")).toBeInTheDocument();
    expect(screen.getByText("https://appleid.apple.com.cn/account/manage/email/private")).toBeInTheDocument();
    expect(screen.getByText("https://<pod>-maildomainws.icloud.com/v2/hme/list")).toBeInTheDocument();
    expect(screen.getByText("https://<pod>-maildomainws.icloud.com.cn/v2/hme/list")).toBeInTheDocument();
    expect(screen.getByText(/either order is accepted/)).toBeInTheDocument();
    expect(screen.getByText(/exact verified forwarding address/)).toBeInTheDocument();
  });

  it("retries when forwarding-address preparation fails", async () => {
    mocks.createPreparation
      .mockRejectedValueOnce(new Error("unavailable"))
      .mockResolvedValueOnce(importPreparation());
    render(
      <ImportICloudModal
        onCancel={vi.fn()}
        onImported={vi.fn()}
        owners={[owner]}
        visible
      />,
    );

    expect(await screen.findByText("iCloud forwarding mailbox preparation failed.")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByText("icloud_test@relay.example")).toBeInTheDocument();
    expect(mocks.createPreparation).toHaveBeenCalledTimes(2);
  });

  it("polls every five seconds and stops after receiving the Apple code", async () => {
    vi.useFakeTimers();
    mocks.createPreparation.mockResolvedValueOnce(importPreparation({
      status: "waiting",
      verificationCode: null,
    }));
    mocks.getPreparation.mockResolvedValueOnce(importPreparation());
    const { rerender } = render(
      <ImportICloudModal
        onCancel={vi.fn()}
        onImported={vi.fn()}
        owners={[owner]}
        visible
      />,
    );

    await act(async () => {
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(screen.getByRole("button", { name: "Next" })).toBeDisabled();
    expect(mocks.getPreparation).not.toHaveBeenCalled();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(4_999);
    });
    expect(mocks.getPreparation).not.toHaveBeenCalled();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1);
    });
    expect(mocks.getPreparation).toHaveBeenCalledWith(31, expect.any(AbortSignal));
    expect(screen.getByText("088556")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Next" })).toBeEnabled();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(10_000);
    });
    expect(mocks.getPreparation).toHaveBeenCalledTimes(1);

    rerender(
      <ImportICloudModal
        onCancel={vi.fn()}
        onImported={vi.fn()}
        owners={[owner]}
        visible={false}
      />,
    );
  });

  it("stops preparation polling when the import dialog closes", async () => {
    vi.useFakeTimers();
    mocks.createPreparation.mockResolvedValueOnce(importPreparation({
      status: "waiting",
      verificationCode: null,
    }));
    const { rerender } = render(
      <ImportICloudModal
        onCancel={vi.fn()}
        onImported={vi.fn()}
        owners={[owner]}
        visible
      />,
    );
    await act(async () => {
      await Promise.resolve();
      await Promise.resolve();
    });
    rerender(
      <ImportICloudModal
        onCancel={vi.fn()}
        onImported={vi.fn()}
        owners={[owner]}
        visible={false}
      />,
    );
    await act(async () => {
      await vi.advanceTimersByTimeAsync(5_000);
    });
    expect(mocks.getPreparation).not.toHaveBeenCalled();
  });

  it("pastes Apple account entries and starts onboarding", async () => {
    const onChanged = vi.fn();
    render(
      <ICloudOnboardingModal
        canOperate
        canReadTasks
        onCancel={vi.fn()}
        onChanged={onChanged}
        owners={[owner]}
        visible
      />,
    );

    await waitFor(() => expect(screen.getByLabelText("owner")).toHaveValue("7"));
    const content =
      "美国区----是----primary@example.test----not-a-real-password----answer-1----answer-2----answer-3----2000-11-02--------https://example.test/family/invite";
    expect(screen.getByRole("button", { name: "Manual input" })).toHaveAttribute(
      "aria-pressed",
      "true",
    );
    fireEvent.change(screen.getByLabelText("iCloud resource entries"), {
      target: { value: content },
    });
    fireEvent.click(screen.getByRole("button", { name: "Start onboarding" }));

    await waitFor(() =>
      expect(mocks.importOnboarding).toHaveBeenCalledWith({
        content,
        expireAt: expect.any(String),
        ownerId: 7,
      }),
    );
    expect(onChanged).toHaveBeenCalledTimes(1);
  });

  it("uploads an Apple account TXT file and starts onboarding", async () => {
    const onChanged = vi.fn();
    render(
      <ICloudOnboardingModal
        canOperate
        canReadTasks
        onCancel={vi.fn()}
        onChanged={onChanged}
        owners={[owner]}
        visible
      />,
    );

    await waitFor(() => expect(screen.getByLabelText("owner")).toHaveValue("7"));
    const content =
      "美国区----是----primary@example.test----not-a-real-password----answer-1----answer-2----answer-3----2000-11-02--------https://example.test/family/invite";
    const file = new File([content], "apple-accounts.txt", { type: "text/plain" });
    Object.defineProperty(file, "text", { value: vi.fn().mockResolvedValue(content) });
    fireEvent.click(screen.getByRole("button", { name: "TXT file" }));
    fireEvent.change(screen.getByLabelText("Apple account TXT file"), {
      target: { files: [file] },
    });
    fireEvent.click(screen.getByRole("button", { name: "Start onboarding" }));

    await waitFor(() => expect(mocks.importOnboarding).toHaveBeenCalledWith({
      content,
      expireAt: expect.any(String),
      ownerId: 7,
    }));
    expect(onChanged).toHaveBeenCalledTimes(1);
  });

  it("recovers manual waiting tasks after closing and reopening the onboarding dialog", async () => {
    mocks.listOnboardingImports.mockResolvedValue({
      items: [{
        attempts: 0,
        bizId: 23,
        bizType: "icloud_resource_import",
        credentialRevision: null,
        finishedAt: null,
        kind: "import",
        maxAttempts: 5,
        progress: { failed: 0, processed: 0, reasonCounts: [], skipped: 0, succeeded: 0, total: 1 },
        queuedAt: "2026-08-16T08:00:00Z",
        remainingAttempts: 5,
        startedAt: "2026-08-16T08:00:01Z",
        status: "running",
        taskId: "server_filtered:23",
        updatedAt: "2026-08-16T08:00:01Z",
      }],
      limit: 100,
      offset: 0,
      succeeded: 0,
      total: 1,
    });
    mocks.getOnboarding.mockResolvedValue(onboardingResponse({
      completed: 0,
      status: "processing",
      tasks: [
        onboardingTask(),
        onboardingTask({
          id: 32,
          needsFamilyReset: true,
          needsManualCode: false,
          stage: "waiting_family_reset",
        }),
      ],
      waiting: 2,
    }));

    const onCancel = vi.fn();
    const { rerender } = render(
      <ICloudOnboardingModal
        canOperate
        canReadTasks
        onCancel={onCancel}
        onChanged={vi.fn()}
        owners={[owner]}
        visible
      />,
    );

    await screen.findByText("server_filtered:23");
    fireEvent.click(screen.getByRole("button", { name: "Open" }));
    await waitFor(() => expect(mocks.getOnboarding).toHaveBeenCalledTimes(1));

    fireEvent.click(screen.getByRole("button", { name: "Close" }));
    expect(onCancel).toHaveBeenCalledTimes(1);
    rerender(
      <ICloudOnboardingModal
        canOperate
        canReadTasks
        onCancel={onCancel}
        onChanged={vi.fn()}
        owners={[owner]}
        visible={false}
      />,
    );
    rerender(
      <ICloudOnboardingModal
        canOperate
        canReadTasks
        onCancel={onCancel}
        onChanged={vi.fn()}
        owners={[owner]}
        visible
      />,
    );

    await screen.findByText("server_filtered:23");
    expect(mocks.listOnboardingImports).toHaveBeenCalledTimes(2);
    fireEvent.click(screen.getByRole("button", { name: "Open" }));
    await waitFor(() => expect(mocks.getOnboarding).toHaveBeenCalledTimes(2));
    expect(mocks.getOnboarding).toHaveBeenLastCalledWith(23);
  });

  it("submits the manual SMS code for an onboarding task", async () => {
    const updated = onboardingTask({ needsManualCode: false, stage: "account_login" });
    const onChanged = vi.fn();
    mocks.submitSmsCode.mockResolvedValueOnce(updated);
    render(<ICloudOnboardingTaskAction onChanged={onChanged} task={onboardingTask()} />);

    fireEvent.change(screen.getByPlaceholderText("SMS code"), {
      target: { value: "12a3456" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Submit code" }));

    await waitFor(() => expect(mocks.submitSmsCode).toHaveBeenCalledWith(31, "123456"));
    expect(onChanged).toHaveBeenCalledWith(updated);
  });

  it("confirms the manual family-sharing reset", async () => {
    const task = onboardingTask({
      needsFamilyReset: true,
      needsManualCode: false,
      stage: "waiting_family_reset",
    });
    const updated = onboardingTask({ needsFamilyReset: false, needsManualCode: false });
    const onChanged = vi.fn();
    mocks.confirmFamilyReset.mockResolvedValueOnce(updated);
    render(<ICloudOnboardingTaskAction onChanged={onChanged} task={task} />);

    fireEvent.click(screen.getByRole("button", { name: "Confirm reset" }));
    const confirmOptions = mocks.modalConfirm.mock.calls[0]?.[0] as any;
    expect(confirmOptions).toMatchObject({ title: "Confirm family sharing reset" });
    await act(async () => confirmOptions.onOk());

    expect(mocks.confirmFamilyReset).toHaveBeenCalledWith(31);
    expect(onChanged).toHaveBeenCalledWith(updated);
  });

  it("retries a recoverable post-family onboarding task", async () => {
    const task = onboardingTask({
      needsManualCode: false,
      needsPostFamilyRecovery: true,
      stage: "family_join_apply",
    });
    const updated = onboardingTask({
      needsManualCode: false,
      needsPostFamilyRecovery: false,
      stage: "family_join_apply",
      status: "processing",
    });
    const onChanged = vi.fn();
    mocks.retryPostFamily.mockResolvedValueOnce(updated);
    render(<ICloudOnboardingTaskAction onChanged={onChanged} task={task} />);

    fireEvent.click(screen.getByRole("button", { name: "Retry" }));

    await waitFor(() => expect(mocks.retryPostFamily).toHaveBeenCalledWith(31));
    expect(onChanged).toHaveBeenCalledWith(updated);
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

    await screen.findByText("088556");
    fireEvent.click(screen.getByRole("button", { name: "Next" }));
    await waitFor(() => expect(screen.getByLabelText("owner")).toHaveValue("7"));
    const content =
      "main@icloud.com----curl --url 'https://p217-maildomainws.icloud.com.cn/v2/hme/list?dsid=123' \\\n  -H 'scnt: scnt-value' \\\n  -b 'X-APPLE-WEBAUTH-TOKEN=secret'";
    const normalizedContent =
      "main@icloud.com----curl --url 'https://p217-maildomainws.icloud.com.cn/v2/hme/list?dsid=123' -H 'scnt: scnt-value' -b 'X-APPLE-WEBAUTH-TOKEN=secret'";
    fireEvent.change(screen.getByPlaceholderText("apple-id@example.com----curl ..."), {
      target: { value: content },
    });
    fireEvent.click(screen.getByRole("button", { name: "Import" }));

    await waitFor(() => expect(mocks.importResources).toHaveBeenCalledWith(
      expect.objectContaining({
        content: normalizedContent,
        errorStrategy: "skip",
        expireAt: expect.any(String),
        ownerId: 7,
        preparationId: 31,
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

  it("requires at least one cURL when replacing credentials", async () => {
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
      "At least one iCloud cURL is required.",
    );
  });

  it("submits only the non-empty credential channel", async () => {
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

    const oldCurl =
      "curl --url 'https://p217-maildomainws.icloud.com.cn/v2/hme/list?dsid=123' -b 'X-APPLE-WEBAUTH-TOKEN=rotated'";
    fireEvent.change(screen.getByLabelText("Old Cookie cURL"), {
      target: { value: oldCurl },
    });
    fireEvent.click(screen.getByRole("button", { name: "Replace credentials" }));

    await waitFor(() => expect(mocks.updateResource).toHaveBeenCalledWith(41, {
      importLine: `main@icloud.com----${oldCurl}`,
      version: 3,
    }));
    expect(screen.getByText(/a blank field keeps that channel unchanged/)).toBeInTheDocument();
  });

  it("builds one normalized import line from the separate email and cURL fields", async () => {
    render(
      <EditICloudModal
        canOperate
        onCancel={vi.fn()}
        onSaved={vi.fn()}
        owners={[owner]}
        target={resource()}
      />,
    );

    const newCurl =
      "curl 'https://appleid.apple.com/account/manage/email/private' \\\n  -H 'cookie: myacinfo=secret' \\\n  -H 'X-Apple-Api-Key: api-key' \\\n  -H 'scnt: scnt-value'";
    const oldCurl =
      "curl --url 'https://p217-maildomainws.icloud.com.cn/v2/hme/list?dsid=123' \\\n  -b 'X-APPLE-WEBAUTH-TOKEN=secret'";
    fireEvent.change(screen.getByLabelText(/Primary email/), {
      target: { value: "replacement@example.com" },
    });
    fireEvent.change(screen.getByLabelText("New Cookie cURL"), {
      target: { value: newCurl },
    });
    fireEvent.change(screen.getByLabelText("Old Cookie cURL"), {
      target: { value: oldCurl },
    });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));

    await waitFor(() =>
      expect(mocks.updateResource).toHaveBeenCalledWith(
        41,
        expect.objectContaining({
          importLine:
            "replacement@example.com----curl 'https://appleid.apple.com/account/manage/email/private' -H 'cookie: myacinfo=secret' -H 'X-Apple-Api-Key: api-key' -H 'scnt: scnt-value'----curl --url 'https://p217-maildomainws.icloud.com.cn/v2/hme/list?dsid=123' -b 'X-APPLE-WEBAUTH-TOKEN=secret'",
        }),
      ),
    );
  });
});
