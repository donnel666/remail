import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from "react";
import {
  Button,
  DatePicker,
  Dropdown,
  Empty,
  Input,
  Modal,
  SideSheet,
  Space,
  Spin,
  Switch,
  Table,
  Tabs,
  Tag,
  TextArea,
  Toast,
  Tooltip,
  Typography,
} from "@douyinfe/semi-ui";
import { IconSearch } from "@douyinfe/semi-icons";
import {
  IllustrationNoResult,
  IllustrationNoResultDark,
} from "@douyinfe/semi-illustrations";
import {
  AtSign,
  CloudDownload,
  FileText,
  RefreshCw,
  ShieldCheck,
  SlidersHorizontal,
  Upload,
  Workflow,
} from "lucide-react";
import { useTranslation } from "react-i18next";

import { CardPro } from "@/components/semi/card-pro";
import { createCardProPagination } from "@/components/semi/card-pro-pagination";
import {
  CardTable,
  DESKTOP_TABLE_SCROLL_Y,
} from "@/components/semi/card-table";
import { CompactModeToggle } from "@/components/semi/compact-mode-toggle";
import { CopyableTableText } from "@/components/semi/copyable-table-text";
import { StatisticFilterOption } from "@/components/semi/statistic-filter-option";
import {
  AdminUserSelect,
  ownersWithCurrentUserFirst,
  type AdminUserSelectOption,
} from "@/components/semi/admin-user-select";
import {
  hasPermissionKey,
  permissionKey,
  useAuth,
} from "@/context/auth-provider";
import { useDebouncedValue } from "@/hooks/use-debounced-value";
import { useIsMobile } from "@/hooks/use-is-mobile";
import { useSharedPageSize } from "@/hooks/use-shared-page-size";
import {
  activateAdminICloudResource,
  batchAdminICloudResourcesByFilter,
  batchAdminICloudResourcesByIds,
  confirmAdminICloudOnboardingFamilyReset,
  confirmAdminICloudOnboardingICloudActivation,
  createAdminICloudImportPreparation,
  createAdminICloudAliases,
  deleteAdminICloudResource,
  disableAdminICloudResource,
  enableAdminICloudResource,
  getAdminICloudImportPreparation,
  getAdminICloudOnboardingImport,
  getAdminICloudResourceDetail,
  importAdminICloudOnboardingAccounts,
  importAdminICloudResources,
  listAdminICloudAliases,
  listAdminICloudOnboardingImports,
  listAdminICloudOwners,
  listAdminICloudResources,
  listAdminICloudTasks,
  normalizeICloudImportContent,
  publishAdminICloudResource,
  recoverAdminICloudResource,
  retryAdminICloudOnboardingPostFamily,
  setAdminICloudResourcesExpirationByFilter,
  setAdminICloudResourcesExpirationByIds,
  submitAdminICloudOnboardingSmsCode,
  unpublishAdminICloudResource,
  updateAdminICloudResource,
  validateAdminICloudResource,
  type AdminICloudAliasItem,
  type AdminICloudBatchAction,
  type AdminICloudBulkResponse,
  type AdminICloudImportErrorStrategy,
  type AdminICloudImportPreparation,
  type AdminICloudMutationResponse,
  type AdminICloudOnboardingImportResponse,
  type AdminICloudOnboardingTask,
  type AdminICloudOwner,
  type AdminICloudResourceDetail,
  type AdminICloudResourceItem,
  type AdminICloudResourceFacets,
  type AdminICloudResourceListFilter,
  type AdminICloudResourceStatus,
  type AdminICloudSessionView,
  type AdminICloudSessionStatus,
  type AdminICloudTask,
  type AdminICloudTaskList,
  type AdminICloudUpdateRequest,
} from "@/lib/admin-icloud-api";
import { getIamErrorMessage } from "@/lib/iam-errors";

import {
  DRAWER_PANEL_HEIGHT,
  DRAWER_TABLE_SCROLL_Y,
  InfoItem,
  OwnerIdentity,
  formatTime,
  ownerRoleLabel,
  renderTaskStatusTag,
  taskKindLabel,
} from "./admin-microsoft/microsoft-meta";
import {
  RelatedOrdersTable,
  ResourceMailsPanel,
  ServerPaginatedDrawerTable,
} from "./admin-microsoft/microsoft-detail-sheet";
import {
  DATE_RANGE_DROPDOWN_CLASS,
  createDateRangePresets,
  createdFromISOString,
  createdToISOString,
  normalizeDateRangeValue,
  type DateRangeValue,
} from "./resources/date-range-filter";
import { useSelectionNotification } from "./resources/use-selection-notification";

const { Text } = Typography;
const IMPORT_ENTRY_AREA_HEIGHT = 208;
const ADMIN_ICLOUD_BATCH_MAX = 1000;
type StatusFilter = "all" | AdminICloudResourceStatus;
type BooleanFilter = "all" | "yes" | "no";
type ICloudImportFlowMode = "automatic" | "legacy";
type RowAction =
  | "toggle"
  | "publish"
  | "delete"
  | "recover"
  | "expiration";
type ImportMode = "paste" | "file";
type ICloudMaintenanceAction = "validate" | "alias";
type ICloudRowMaintenanceAction =
  | ICloudMaintenanceAction
  | "familySharing"
  | "oldCookie";
type ICloudMaintenanceTarget =
  | { item: AdminICloudResourceItem; mode: "row" }
  | { count: number; mode: "ids"; resourceIds: number[] }
  | { count: number; filter: AdminICloudResourceListFilter; mode: "filter" };
type ICloudBulkBusyAction =
  | Exclude<AdminICloudBatchAction, "validate" | "alias">
  | "expiration";

const EMPTY_FACETS: AdminICloudResourceFacets = {
  status: {
    all: 0,
    pending: 0,
    validating: 0,
    normal: 0,
    abnormal: 0,
    disabled: 0,
    deleted: 0,
  },
  forSale: { all: 0, yes: 0, no: 0 },
};

const statusMeta: Record<
  AdminICloudResourceStatus,
  { color: "green" | "orange" | "red" | "grey" | "blue"; label: string }
> = {
  pending: { color: "blue", label: "Pending" },
  validating: { color: "orange", label: "Validating" },
  normal: { color: "green", label: "Normal" },
  abnormal: { color: "orange", label: "Abnormal" },
  disabled: { color: "grey", label: "Disabled" },
  deleted: { color: "red", label: "Deleted" },
};

const sessionMeta: Record<
  AdminICloudSessionStatus,
  { color: "green" | "red" | "grey"; label: string }
> = {
  unchecked: { color: "grey", label: "Unchecked" },
  valid: { color: "green", label: "Valid" },
  invalid: { color: "red", label: "Invalid" },
};

const aliasStatusMeta = {
  normal: { color: "green", label: "Normal" },
  disabled: { color: "grey", label: "Disabled" },
  missing: { color: "orange", label: "Missing" },
  deleted: { color: "red", label: "Deleted" },
} as const;

function bulkOutcome(response: AdminICloudBulkResponse) {
  return {
    succeeded: response.affected,
    skipped: response.skipped,
    reasonCounts: response.reasonCounts,
  };
}

function defaultICloudExpireAt() {
  const value = new Date();
  value.setMonth(value.getMonth() + 1);
  return value;
}

function switchButtonClass(active: boolean) {
  return [
    "flex h-12 w-full items-center justify-center gap-2 rounded-lg border-2 px-4 text-sm font-semibold transition-all",
    active
      ? "border-[var(--semi-color-primary)] bg-[var(--semi-color-primary-light-default)] text-[var(--semi-color-primary)]"
      : "border-[var(--semi-color-border)] bg-[var(--semi-color-bg-2)] text-[var(--semi-color-text-1)] hover:border-[var(--semi-color-primary)] hover:bg-[var(--semi-color-fill-0)]",
  ].join(" ");
}

function ICloudImportFlowSelector({
  canAutomatic,
  canLegacy,
  mode,
  onChange,
}: {
  canAutomatic: boolean;
  canLegacy: boolean;
  mode: ICloudImportFlowMode;
  onChange: (mode: ICloudImportFlowMode) => void;
}) {
  const { t } = useTranslation();
  return (
    <div>
      <div className="mb-1.5 text-sm font-medium text-[var(--semi-color-text-0)]">
        {t("Import mode")}
      </div>
      <div
        aria-label={t("Import mode")}
        className="grid grid-cols-1 gap-2 sm:grid-cols-2"
        role="group"
      >
        <button
          aria-pressed={mode === "automatic"}
          className={`${switchButtonClass(mode === "automatic")} disabled:cursor-not-allowed disabled:opacity-50`}
          disabled={!canAutomatic}
          onClick={() => onChange("automatic")}
          type="button"
        >
          <Workflow size={16} />
          {t("Automated eSIM onboarding")}
        </button>
        <button
          aria-pressed={mode === "legacy"}
          className={`${switchButtonClass(mode === "legacy")} disabled:cursor-not-allowed disabled:opacity-50`}
          disabled={!canLegacy}
          onClick={() => onChange("legacy")}
          type="button"
        >
          <FileText size={16} />
          {t("Legacy double-cURL import")}
        </button>
      </div>
    </div>
  );
}

function ResourceStatusTag({ item }: { item: AdminICloudResourceItem }) {
  const { t } = useTranslation();
  const meta = statusMeta[item.status];
  const tag = (
    <Tag color={meta.color} shape="circle" size="small">
      {t(meta.label)}
    </Tag>
  );
  return item.lastSafeError ? (
    <Tooltip content={item.lastSafeError}>{tag}</Tooltip>
  ) : (
    tag
  );
}

function SessionStatusTag({ session }: { session: AdminICloudSessionView | null }) {
  const { t } = useTranslation();
  if (!session) {
    return (
      <Tag color="grey" shape="circle" size="small">
        {t("Not configured")}
      </Tag>
    );
  }
  const meta = sessionMeta[session.status];
  const tag = (
    <Tag color={meta.color} shape="circle" size="small">
      {t(meta.label)}
    </Tag>
  );
  const diagnostic = [
    session.failures ? `${t("Failures")}: ${session.failures}` : "",
    session.cooldownUntil ? `${t("Cooldown until")}: ${formatTime(session.cooldownUntil)}` : "",
    session.lastValidAt ? `${t("Last valid")}: ${formatTime(session.lastValidAt)}` : "",
  ]
    .filter(Boolean)
    .join(" · ");
  return diagnostic ? <Tooltip content={diagnostic}>{tag}</Tooltip> : tag;
}

function AliasCountTag({ count, limit }: { count: number; limit: number }) {
  const color = count === limit ? "green" : count > limit ? "red" : "orange";
  return (
    <Tag color={color} shape="circle" size="small">
      {count}/{limit}
    </Tag>
  );
}

function ownerOption(
  owner: AdminICloudOwner,
  t: ReturnType<typeof useTranslation>["t"],
): AdminUserSelectOption<AdminICloudOwner> {
  return {
    data: owner,
    disabled: !owner.enabled,
    label: `${owner.email} · ${owner.nickname} · ${t(ownerRoleLabel(owner.role))} · ${owner.groupName}`,
    value: owner.id,
  };
}

function OwnerSelect({
  onChange,
  owners,
  selectedOwner,
  t,
  value,
}: {
  onChange: (ownerId: number) => void;
  owners: AdminICloudOwner[];
  selectedOwner?: AdminICloudOwner;
  t: ReturnType<typeof useTranslation>["t"];
  value?: number;
}) {
  const options = useMemo(
    () => owners.map((owner) => ownerOption(owner, t)),
    [owners, t],
  );
  const selectedOption = useMemo(
    () => (selectedOwner ? ownerOption(selectedOwner, t) : undefined),
    [selectedOwner, t],
  );

  return (
    <AdminUserSelect
      emptyContent={t("No users found")}
      loadOptions={async (keyword) =>
        (await listAdminICloudOwners(keyword)).map((owner) =>
          ownerOption(owner, t)
        )
      }
      onChange={(ownerID) => {
        if (ownerID) onChange(ownerID);
      }}
      options={options}
      placeholder={t("Search user by email, nickname or ID")}
      selectedOption={selectedOption}
      style={{ width: "100%" }}
      value={value}
    />
  );
}

function ICloudAccountRoleTag({ role }: { role: AdminICloudResourceItem["accountRole"] }) {
  const { t } = useTranslation();
  const meta = {
    child: { color: "green" as const, label: "Child account" },
    primary: { color: "blue" as const, label: "Primary account" },
    unknown: { color: "grey" as const, label: "Unknown" },
  }[role];
  return (
    <Tag color={meta.color} shape="circle" size="small">
      {t(meta.label)}
    </Tag>
  );
}

function onboardingTaskStatusTag(task: AdminICloudOnboardingTask, t: ReturnType<typeof useTranslation>["t"]) {
  const meta = {
    completed: { color: "green" as const, label: "Completed" },
    failed: { color: "red" as const, label: "Failed" },
    processing: { color: "blue" as const, label: "Processing" },
    waiting: { color: "orange" as const, label: "Waiting" },
  }[task.status];
  return (
    <Tag color={meta.color} shape="circle" size="small">
      {t(meta.label)}
    </Tag>
  );
}

function phoneSourceLabel(source: AdminICloudResourceItem["boundPhoneSource"], t: ReturnType<typeof useTranslation>["t"]) {
  if (source === "kitesim") return t("Kitesim assigned");
  if (source === "manual") return t("Manually supplied");
  return "-";
}

function ICloudFamilySyncTag({ item }: { item: AdminICloudResourceItem }) {
  const { t } = useTranslation();
  const meta = {
    failed: { color: "red" as const, label: "Failed" },
    inactive: { color: "grey" as const, label: "Inactive" },
    ready: { color: "green" as const, label: "Ready" },
    unknown: { color: "orange" as const, label: "Unknown" },
  }[item.familySyncStatus] ?? { color: "orange" as const, label: "Unknown" };
  return <Tag color={meta.color} size="small">{t(meta.label)}</Tag>;
}

export function ICloudOnboardingTaskAction({
  disabled = false,
  onChanged,
  task,
}: {
  disabled?: boolean;
  onChanged: (task: AdminICloudOnboardingTask) => void | Promise<void>;
  task: AdminICloudOnboardingTask;
}) {
  const { t } = useTranslation();
  const [code, setCode] = useState("");
  const [busy, setBusy] = useState<"activation" | "code" | "family" | "retry" | null>(null);

  const submitCode = async () => {
    if (!/^\d{4,10}$/.test(code.trim())) {
      Toast.warning(t("Enter a 4 to 10 digit SMS code."));
      return;
    }
    setBusy("code");
    try {
      const updated = await submitAdminICloudOnboardingSmsCode(task.id, code);
      setCode("");
      await onChanged(updated);
      Toast.success(t("SMS code submitted."));
    } catch (error) {
      Toast.error(getIamErrorMessage(t, error, "SMS code submission failed."));
    } finally {
      setBusy(null);
    }
  };

  const confirmFamilyReset = () => {
    const familySharingWaiting = task.stage === "waiting_family_sharing";
    Modal.confirm({
      cancelText: t("Cancel"),
      content: familySharingWaiting
        ? t("Confirm that family sharing has been enabled manually for an automatic onboarding resource before Apple account configuration continues.")
        : t("Confirm that family sharing was disabled and enabled again on the primary account {{email}}.", {
            email: task.familyPrimaryEmail || `#${task.familyPrimaryResourceId ?? "-"}`,
          }),
      okText: t("Confirm"),
      onOk: async () => {
        setBusy("family");
        try {
          const updated = await confirmAdminICloudOnboardingFamilyReset(task.id);
          await onChanged(updated);
          Toast.success(t(familySharingWaiting ? "Family sharing confirmed." : "Family sharing reset confirmed."));
        } catch (error) {
          Toast.error(getIamErrorMessage(t, error, "Family sharing confirmation failed."));
          throw error;
        } finally {
          setBusy(null);
        }
      },
      title: t(familySharingWaiting ? "Confirm family sharing" : "Confirm family sharing reset"),
    });
  };

  const confirmICloudActivation = () => {
    Modal.confirm({
      cancelText: t("Cancel"),
      content: t("Confirm that iCloud was manually enabled for this Apple account."),
      okText: t("Confirm"),
      onOk: async () => {
        setBusy("activation");
        try {
          const updated = await confirmAdminICloudOnboardingICloudActivation(task.id);
          await onChanged(updated);
          Toast.success(t("iCloud activation confirmed."));
        } catch (error) {
          Toast.error(getIamErrorMessage(t, error, "iCloud activation confirmation failed."));
          throw error;
        } finally {
          setBusy(null);
        }
      },
      title: t("Confirm iCloud activation"),
    });
  };

  const retryPostFamily = async () => {
    setBusy("retry");
    try {
      const updated = await retryAdminICloudOnboardingPostFamily(task.id);
      await onChanged(updated);
      Toast.success(t("Onboarding retry queued."));
    } catch (error) {
      Toast.error(getIamErrorMessage(t, error, "Onboarding retry failed."));
    } finally {
      setBusy(null);
    }
  };

  if (task.needsPostFamilyRecovery) {
    return (
      <Button
        disabled={disabled || busy !== null}
        icon={<RefreshCw size={14} />}
        loading={busy === "retry"}
        onClick={() => void retryPostFamily()}
        size="small"
        type="primary"
      >
        {t("Retry")}
      </Button>
    );
  }

  if (task.needsManualCode) {
    return (
      <div className="flex min-w-56 items-center gap-2">
        <Input
          disabled={disabled || busy !== null}
          maxLength={10}
          onChange={(value) => setCode(String(value).replace(/\D/g, ""))}
          placeholder={t("SMS code")}
          size="small"
          value={code}
        />
        <Button
          disabled={disabled || busy === "family"}
          loading={busy === "code"}
          onClick={() => void submitCode()}
          size="small"
          type="primary"
        >
          {t("Submit code")}
        </Button>
      </div>
    );
  }
  if (task.needsFamilyReset) {
    const familySharingWaiting = task.stage === "waiting_family_sharing";
    return (
      <Button
        disabled={disabled || busy === "code"}
        loading={busy === "family"}
        onClick={confirmFamilyReset}
        size="small"
        type="primary"
      >
        {t(familySharingWaiting ? "Confirm family sharing" : "Confirm reset")}
      </Button>
    );
  }
  if (task.needsICloudActivation) {
    return (
      <Button
        disabled={disabled || busy !== null}
        loading={busy === "activation"}
        onClick={confirmICloudActivation}
        size="small"
        type="primary"
      >
        {t("Confirm activation")}
      </Button>
    );
  }
  return null;
}

export function ICloudOnboardingModal({
  canOperate,
  canReadTasks,
  modeSelector,
  onCancel,
  onChanged,
  owners,
  visible,
}: {
  canOperate: boolean;
  canReadTasks: boolean;
  modeSelector?: ReactNode;
  onCancel: () => void;
  onChanged: () => void | Promise<void>;
  owners: AdminICloudOwner[];
  visible: boolean;
}) {
  const { t } = useTranslation();
  const [mode, setMode] = useState<ImportMode>("paste");
  const [content, setContent] = useState("");
  const [file, setFile] = useState<File | null>(null);
  const [ownerId, setOwnerId] = useState<number | undefined>();
  const [expireAt, setExpireAt] = useState<Date | null>(() => defaultICloudExpireAt());
  const [result, setResult] = useState<AdminICloudOnboardingImportResponse | null>(null);
  const [activeImports, setActiveImports] = useState<AdminICloudTask[]>([]);
  const [activeImportsLoading, setActiveImportsLoading] = useState(false);
  const [openingImportId, setOpeningImportId] = useState<number | null>(null);
  const [recoveryError, setRecoveryError] = useState<string | null>(null);
  const [pollError, setPollError] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);
  const fileRef = useRef<HTMLInputElement>(null);
  const lineCount = useMemo(
    () => content.split(/\r?\n/).filter((line) => line.trim()).length,
    [content],
  );

  useEffect(() => {
    if (!visible) return;
    setMode("paste");
    setContent("");
    setFile(null);
    setOwnerId(undefined);
    setExpireAt(defaultICloudExpireAt());
    setResult(null);
    setActiveImports([]);
    setOpeningImportId(null);
    setRecoveryError(null);
    setPollError(null);
    setSubmitting(false);
    if (fileRef.current) fileRef.current.value = "";
  }, [visible]);

  useEffect(() => {
    if (!visible || !canReadTasks) return;
    const controller = new AbortController();
    setActiveImportsLoading(true);
    void listAdminICloudOnboardingImports(controller.signal)
      .then((response) => {
        if (controller.signal.aborted) return;
        setActiveImports(response.items);
        setRecoveryError(null);
      })
      .catch((error) => {
        if (!controller.signal.aborted) {
          setRecoveryError(getIamErrorMessage(t, error, "Active onboarding tasks could not be loaded."));
        }
      })
      .finally(() => {
        if (!controller.signal.aborted) setActiveImportsLoading(false);
      });
    return () => controller.abort();
  }, [canReadTasks, t, visible]);

  useEffect(() => {
    if (!visible || ownerId !== undefined) return;
    setOwnerId(owners.find((owner) => owner.enabled)?.id ?? owners[0]?.id);
  }, [ownerId, owners, visible]);

  useEffect(() => {
    if (!visible || !result || result.status !== "processing") return;
    let stopped = false;
    let timeoutId: number | undefined;
    let controller: AbortController | null = null;
    const poll = () => {
      timeoutId = window.setTimeout(async () => {
        controller = new AbortController();
        try {
          const next = await getAdminICloudOnboardingImport(result.importId, controller.signal);
          if (stopped) return;
          setResult(next);
          setPollError(null);
          if (next.status === "processing") poll();
          else await onChanged();
        } catch (error) {
          if (!stopped && !controller.signal.aborted) {
            setPollError(getIamErrorMessage(t, error, "Apple onboarding status check failed."));
            poll();
          }
        }
      }, 3_000);
    };
    poll();
    return () => {
      stopped = true;
      controller?.abort();
      if (timeoutId !== undefined) window.clearTimeout(timeoutId);
    };
  }, [onChanged, result?.importId, result?.status, t, visible]);

  const submit = async () => {
    if (!ownerId) {
      Toast.warning(t("Please select an owner."));
      return;
    }
    if (!expireAt || !Number.isFinite(expireAt.getTime()) || expireAt.getTime() <= Date.now()) {
      Toast.warning(t("Expiration must be in the future."));
      return;
    }
    setSubmitting(true);
    try {
      let sourceContent = content;
      if (mode === "file") {
        if (!file) {
          Toast.warning(t("Please select a TXT file."));
          return;
        }
        sourceContent = await file.text();
      }
      if (!sourceContent.trim()) {
        Toast.warning(
          t(
            mode === "file"
              ? "Please select a non-empty TXT file."
              : "Please enter iCloud resources.",
          ),
        );
        return;
      }
      const next = await importAdminICloudOnboardingAccounts({
        content: sourceContent,
        expireAt: expireAt.toISOString(),
        ownerId,
      });
      setResult(next);
      setContent("");
      setFile(null);
      if (fileRef.current) fileRef.current.value = "";
      Toast.success(t("Apple account onboarding started.", { count: next.accepted }));
      await onChanged();
    } catch (error) {
      Toast.error(getIamErrorMessage(t, error, "Apple account onboarding failed."));
    } finally {
      setSubmitting(false);
    }
  };

  const taskChanged = useCallback(async (updated: AdminICloudOnboardingTask) => {
    setResult((current) =>
      current
        ? {
            ...current,
            tasks: current.tasks.map((task) => (task.id === updated.id ? updated : task)),
            updatedAt: updated.updatedAt,
          }
        : current,
    );
    await onChanged();
  }, [onChanged]);

  const openActiveImport = async (importId: number) => {
    setOpeningImportId(importId);
    try {
      setResult(await getAdminICloudOnboardingImport(importId));
      setPollError(null);
    } catch (error) {
      Toast.error(getIamErrorMessage(t, error, "Apple onboarding status check failed."));
    } finally {
      setOpeningImportId(null);
    }
  };

  const columns = useMemo(
    () => [
      {
        dataIndex: "primaryEmail",
        title: t("Apple ID"),
        width: 220,
        render: (value: unknown) => (
          <CopyableTableText copiedText={t("Copied")} text={String(value)} />
        ),
      },
      {
        key: "account",
        title: t("Account"),
        width: 150,
        render: (_: unknown, task: AdminICloudOnboardingTask) => (
          <div className="space-y-1">
            <ICloudAccountRoleTag role={task.accountRole} />
            <div className="text-xs text-[var(--semi-color-text-2)]">
              {task.region}{task.countryCode ? ` · ${task.countryCode}` : ""}
            </div>
          </div>
        ),
      },
      {
        key: "phone",
        title: t("Bound phone"),
        width: 190,
        render: (_: unknown, task: AdminICloudOnboardingTask) => (
          <div className="space-y-1">
            {task.boundPhoneNumber ? (
              <CopyableTableText copiedText={t("Copied")} text={task.boundPhoneNumber} />
            ) : (
              <span>{t(task.accountRole === "primary" ? "Pending recovery" : "Pending assignment")}</span>
            )}
            <div className="text-xs text-[var(--semi-color-text-2)]">
              {phoneSourceLabel(task.boundPhoneSource, t)}
            </div>
          </div>
        ),
      },
      {
        key: "state",
        title: t("State"),
        width: 180,
        render: (_: unknown, task: AdminICloudOnboardingTask) => (
          <div className="space-y-1">
            {onboardingTaskStatusTag(task, t)}
            <code className="block break-all text-xs text-[var(--semi-color-text-2)]">
              {task.stage}
            </code>
          </div>
        ),
      },
      {
        dataIndex: "lastSafeError",
        title: t("Last error"),
        width: 240,
        render: (value: unknown) => (
          <span className="block break-words text-xs text-[var(--semi-color-text-2)]">
            {String(value || "-")}
          </span>
        ),
      },
      {
        key: "action",
        title: t("Action"),
        width: 270,
        render: (_: unknown, task: AdminICloudOnboardingTask) =>
          task.needsManualCode || task.needsICloudActivation || task.needsFamilyReset || task.needsPostFamilyRecovery ? (
            <ICloudOnboardingTaskAction
              disabled={!canOperate}
              onChanged={taskChanged}
              task={task}
            />
          ) : (
            "-"
          ),
      },
    ],
    [canOperate, t, taskChanged],
  );

  return (
    <Modal
      cancelText={t("Cancel")}
      centered
      confirmLoading={submitting}
      onCancel={onCancel}
      onOk={() => {
        if (result) onCancel();
        else void submit();
      }}
      okButtonProps={{
        disabled: !result && (mode === "paste" ? !content.trim() : !file),
      }}
      okText={t(result ? "Close" : "Start onboarding")}
      title={t("Automatic Apple onboarding")}
      visible={visible}
      width="min(1080px, calc(100vw - 32px))"
    >
      {!result && modeSelector ? <div className="mb-4">{modeSelector}</div> : null}
      {result ? (
        <div className="space-y-4 py-1">
          <div className="grid grid-cols-2 gap-2 sm:grid-cols-4">
            {[
              ["Accepted", result.accepted],
              ["Completed", result.completed],
              ["Waiting", result.waiting],
              ["Failed", result.failed],
            ].map(([label, value]) => (
              <div className="border-l-2 border-[var(--semi-color-border)] px-3 py-1" key={label}>
                <div className="text-xs text-[var(--semi-color-text-2)]">{t(String(label))}</div>
                <div className="mt-1 text-lg font-semibold tabular-nums">{value}</div>
              </div>
            ))}
          </div>
          <Table
            columns={columns}
            dataSource={result.tasks}
            pagination={false}
            rowKey="id"
            scroll={{ x: 1250, y: 420 }}
            size="small"
          />
          {pollError ? (
            <div className="text-xs text-[var(--semi-color-warning)]">{pollError}</div>
          ) : null}
        </div>
      ) : (
        <div className="space-y-4 py-1">
          {canReadTasks ? (
            <section className="border-b border-[var(--semi-color-border)] pb-4">
              <div className="mb-2 text-sm font-medium text-[var(--semi-color-text-0)]">
                {t("Active onboarding tasks")}
              </div>
              {activeImportsLoading ? (
                <Spin size="small" />
              ) : activeImports.length > 0 ? (
                <div className="divide-y divide-[var(--semi-color-border)]">
                  {activeImports.map((task) => (
                    <div className="flex items-center justify-between gap-3 py-2" key={task.taskId}>
                      <div className="min-w-0">
                        <div className="flex flex-wrap items-center gap-2">
                          <code className="text-xs">{task.taskId}</code>
                          {renderTaskStatusTag(task.status, t)}
                        </div>
                        <div className="mt-1 text-xs text-[var(--semi-color-text-2)]">
                          {task.progress
                            ? t("{{processed}} of {{total}} processed", {
                                processed: task.progress.processed,
                                total: task.progress.total,
                              })
                            : formatTime(task.updatedAt)}
                        </div>
                      </div>
                      <Button
                        loading={openingImportId === task.bizId}
                        onClick={() => void openActiveImport(task.bizId)}
                        size="small"
                        type="tertiary"
                      >
                        {t("Open")}
                      </Button>
                    </div>
                  ))}
                </div>
              ) : (
                <div className="text-xs text-[var(--semi-color-text-2)]">
                  {t("No active onboarding tasks")}
                </div>
              )}
              {recoveryError ? (
                <div className="mt-2 text-xs text-[var(--semi-color-warning)]">{recoveryError}</div>
              ) : null}
            </section>
          ) : null}
          <label className="block">
            <span className="mb-1.5 block text-sm font-medium text-[var(--semi-color-text-0)]">
              {t("Owner")} *
            </span>
            <OwnerSelect onChange={setOwnerId} owners={owners} t={t} value={ownerId} />
          </label>
          <label className="block">
            <span className="mb-1.5 block text-sm font-medium text-[var(--semi-color-text-0)]">
              {t("Resource expires at")} *
            </span>
            <DatePicker
              aria-label={t("Resource expires at")}
              format="yyyy-MM-dd HH:mm:ss"
              onChange={(value) => setExpireAt(value instanceof Date ? value : null)}
              showClear={false}
              style={{ width: "100%" }}
              type="dateTime"
              value={expireAt ?? undefined}
            />
          </label>
          <div
            aria-label={t("Import mode")}
            className="grid grid-cols-2 gap-2"
            role="group"
          >
            <button
              aria-pressed={mode === "paste"}
              className={switchButtonClass(mode === "paste")}
              onClick={() => {
                setMode("paste");
                setFile(null);
                if (fileRef.current) fileRef.current.value = "";
              }}
              type="button"
            >
              <FileText size={16} />
              {t("Manual input")}
            </button>
            <button
              aria-pressed={mode === "file"}
              className={switchButtonClass(mode === "file")}
              onClick={() => {
                setMode("file");
                setContent("");
              }}
              type="button"
            >
              <Upload size={16} />
              {t("TXT file")}
            </button>
          </div>
          {mode === "paste" ? (
            <label className="block">
              <span className="mb-1.5 flex items-center justify-between text-sm font-medium text-[var(--semi-color-text-0)]">
                <span>{t("iCloud resource entries")} *</span>
                <Text size="small" type="tertiary">
                  {t("Parsed entries", { count: lineCount })}
                </Text>
              </span>
              <TextArea
                aria-label={t("iCloud resource entries")}
                className="font-mono"
                onChange={setContent}
                rows={8}
                style={{ height: IMPORT_ENTRY_AREA_HEIGHT, resize: "none" }}
                value={content}
              />
            </label>
          ) : (
            <>
              <input
                accept=".txt,text/plain"
                aria-label={t("Apple account TXT file")}
                className="hidden"
                onChange={(event) => setFile(event.target.files?.[0] ?? null)}
                ref={fileRef}
                type="file"
              />
              <button
                className="flex w-full cursor-pointer flex-col items-center justify-center rounded-lg border border-dashed border-[var(--semi-color-border)] bg-[var(--semi-color-fill-0)] p-5 text-center transition-colors hover:bg-[var(--semi-color-fill-1)]"
                onClick={() => fileRef.current?.click()}
                style={{ height: IMPORT_ENTRY_AREA_HEIGHT }}
                type="button"
              >
                <FileText className="mb-2 size-8 text-[var(--semi-color-text-2)]" />
                <Text strong>{file ? file.name : t("Select TXT file")}</Text>
                <Text size="small" type="tertiary">
                  {file
                    ? `${(file.size / 1024).toFixed(1)} KB`
                    : t("Supports .txt files, one entry per line")}
                </Text>
              </button>
            </>
          )}
          <div className="border-y border-[var(--semi-color-border)] py-3">
            <div className="mb-1 text-xs font-medium text-[var(--semi-color-text-0)]">
              {t("Import format")}
            </div>
            <code className="block whitespace-pre-wrap break-all font-mono text-xs leading-5 text-[var(--semi-color-text-2)]">
              {t("Region----iCloud opened----Apple ID----Password----Security answer 1----Security answer 2----Security answer 3----Birthday[----Phone][----Invitation URL (non-empty marks primary)]")}
            </code>
          </div>
        </div>
      )}
    </Modal>
  );
}

export function ImportICloudModal({
  modeSelector,
  onCancel,
  onImported,
  owners,
  visible,
}: {
  modeSelector?: ReactNode;
  onCancel: () => void;
  onImported: () => void | Promise<void>;
  owners: AdminICloudOwner[];
  visible: boolean;
}) {
  const { t } = useTranslation();
  const [mode, setMode] = useState<ImportMode>("paste");
  const [content, setContent] = useState("");
  const [file, setFile] = useState<File | null>(null);
  const [ownerId, setOwnerId] = useState<number | undefined>();
  const [errorStrategy, setErrorStrategy] =
    useState<AdminICloudImportErrorStrategy>("skip");
  const [expireAt, setExpireAt] = useState<Date | null>(() =>
    defaultICloudExpireAt(),
  );
  const [step, setStep] = useState<"verification" | "import">("verification");
  const [preparation, setPreparation] =
    useState<AdminICloudImportPreparation | null>(null);
  const [preparationLoading, setPreparationLoading] = useState(false);
  const [preparationError, setPreparationError] = useState<string | null>(null);
  const [pollError, setPollError] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);
  const fileRef = useRef<HTMLInputElement>(null);
  const preparationRequestRef = useRef<AbortController | null>(null);
  const lineCount = useMemo(
    () =>
      normalizeICloudImportContent(content)
        .split(/\r?\n/)
        .filter((line) => line.trim()).length,
    [content],
  );

  const prepareForwardingMailbox = useCallback(async () => {
    preparationRequestRef.current?.abort();
    const controller = new AbortController();
    preparationRequestRef.current = controller;
    setPreparationLoading(true);
    setPreparationError(null);
    setPollError(null);
    setPreparation(null);
    try {
      const result = await createAdminICloudImportPreparation(controller.signal);
      if (!controller.signal.aborted) setPreparation(result);
    } catch (error) {
      if (!controller.signal.aborted) {
        setPreparationError(
          getIamErrorMessage(t, error, "iCloud forwarding mailbox preparation failed."),
        );
      }
    } finally {
      if (preparationRequestRef.current === controller) {
        preparationRequestRef.current = null;
        setPreparationLoading(false);
      }
    }
  }, [t]);

  useEffect(() => {
    if (!visible) {
      preparationRequestRef.current?.abort();
      preparationRequestRef.current = null;
      return;
    }
    setMode("paste");
    setContent("");
    setFile(null);
    if (fileRef.current) fileRef.current.value = "";
    setOwnerId(undefined);
    setErrorStrategy("skip");
    setExpireAt(defaultICloudExpireAt());
    setStep("verification");
    setSubmitting(false);
    void prepareForwardingMailbox();
    return () => {
      preparationRequestRef.current?.abort();
      preparationRequestRef.current = null;
    };
  }, [prepareForwardingMailbox, visible]);

  useEffect(() => {
    if (!visible || step !== "verification" || !preparation || preparation.status !== "waiting") {
      return;
    }
    let stopped = false;
    let timeoutId: number | undefined;
    let controller: AbortController | null = null;
    const poll = () => {
      timeoutId = window.setTimeout(async () => {
        controller = new AbortController();
        try {
          const result = await getAdminICloudImportPreparation(
            preparation.id,
            controller.signal,
          );
          if (!stopped) {
            setPreparation(result);
            setPollError(null);
            if (result.status === "waiting") poll();
          }
        } catch (error) {
          if (!stopped && !controller.signal.aborted) {
            setPollError(
              getIamErrorMessage(t, error, "iCloud verification mail check failed."),
            );
            poll();
          }
        }
      }, 5_000);
    };
    poll();
    return () => {
      stopped = true;
      controller?.abort();
      if (timeoutId !== undefined) window.clearTimeout(timeoutId);
    };
  }, [preparation, step, t, visible]);

  useEffect(() => {
    if (!visible || ownerId !== undefined) return;
    setOwnerId(owners.find((owner) => owner.enabled)?.id ?? owners[0]?.id);
  }, [ownerId, owners, visible]);

  const submit = async () => {
    if (!preparation || preparation.status !== "code_received") {
      Toast.warning(t("Verify the Apple forwarding address before importing."));
      return;
    }
    if (!ownerId) {
      Toast.warning(t("Please select an owner."));
      return;
    }
    if (!expireAt || !Number.isFinite(expireAt.getTime()) || expireAt.getTime() <= Date.now()) {
      Toast.warning(t("Expiration must be in the future."));
      return;
    }
    let sourceContent = content;
    if (mode === "file") {
      if (!file) {
        Toast.warning(t("Please select a TXT file."));
        return;
      }
      try {
        sourceContent = await file.text();
      } catch (error) {
        Toast.error(getIamErrorMessage(t, error, "iCloud import failed."));
        return;
      }
    }
    sourceContent = normalizeICloudImportContent(sourceContent);
    const sourceLineCount = sourceContent
      .split(/\r?\n/)
      .filter((line) => line.trim()).length;
    if (!sourceLineCount) {
      Toast.warning(t("Please enter iCloud resources."));
      return;
    }
    if (sourceLineCount !== 1) {
      Toast.warning(t("Each forwarding address can import only one iCloud resource."));
      return;
    }
    setSubmitting(true);
    try {
      const result = await importAdminICloudResources({
        content: sourceContent,
        errorStrategy,
        expireAt: expireAt.toISOString(),
        ownerId,
        preparationId: preparation.id,
      });
      if (result.status === "failed") {
        throw new Error(result.lastSafeError || "iCloud import failed.");
      }
      if (result.status === "processing") {
        Toast.info(
          t("iCloud import continues in background.", { id: result.importId }),
        );
      } else {
        Toast.success(t("iCloud resources imported.", { count: result.imported }));
        if (result.skipped) {
          Toast.warning(t("Import skipped errors", { count: result.skipped }));
        }
      }
    } catch (error) {
      Toast.error(getIamErrorMessage(t, error, "iCloud import failed."));
      setSubmitting(false);
      return;
    }
    try {
      await onImported();
    } catch (error) {
      Toast.error(getIamErrorMessage(t, error, "iCloud resources load failed."));
    }
    setSubmitting(false);
    onCancel();
  };

  return (
    <Modal
      cancelText={t("Cancel")}
      centered
      confirmLoading={step === "import" && submitting}
      onCancel={onCancel}
      onOk={() => {
        if (step === "verification") {
          setStep("import");
          return;
        }
        void submit();
      }}
      okButtonProps={{
        disabled:
          step === "verification" &&
          (preparationLoading || preparation?.status !== "code_received"),
      }}
      okText={t(step === "verification" ? "Next" : "Import")}
      title={t("Import iCloud Emails")}
      visible={visible}
      width="min(720px, calc(100vw - 32px))"
    >
      {modeSelector ? <div className="mb-4">{modeSelector}</div> : null}
      {step === "verification" ? (
        <div className="space-y-4 py-1">
          <div className="flex items-center justify-between text-sm">
            <span className="font-medium text-[var(--semi-color-text-0)]">
              {t("Verify forwarding address")}
            </span>
            <span className="text-[var(--semi-color-text-2)]">1 / 2</span>
          </div>
          <div className="border-y border-[var(--semi-color-border)] py-5">
            {preparationLoading ? (
              <div className="flex min-h-40 items-center justify-center gap-2 text-sm text-[var(--semi-color-text-2)]">
                <Spin />
                {t("Generating forwarding address...")}
              </div>
            ) : preparationError ? (
              <div className="flex min-h-40 flex-col items-center justify-center gap-3 text-center">
                <span className="max-w-md text-sm text-[var(--semi-color-danger)]">
                  {preparationError}
                </span>
                <Button
                  icon={<RefreshCw size={14} />}
                  onClick={() => void prepareForwardingMailbox()}
                  type="tertiary"
                >
                  {t("Try again")}
                </Button>
              </div>
            ) : preparation ? (
              <div className="space-y-4">
                <div>
                  <div className="text-sm font-medium text-[var(--semi-color-text-0)]">
                    {t("Forwarding address")}
                  </div>
                  <div className="mt-2 break-all font-mono text-base">
                    <CopyableTableText
                      copiedText={t("Copied")}
                      text={preparation.forwardToEmail}
                    />
                  </div>
                  <p className="mt-2 text-xs leading-5 text-[var(--semi-color-text-2)]">
                    {t("Add this address as an additional email address in your Apple Account, then request the verification email.")}
                  </p>
                </div>

                {preparation.status === "code_received" && preparation.verificationCode ? (
                  <div className="flex items-start gap-3 border-l-2 border-[var(--semi-color-success)] bg-[var(--semi-color-success-light-default)] px-3 py-3">
                    <ShieldCheck className="mt-0.5 size-4 shrink-0 text-[var(--semi-color-success)]" />
                    <div className="min-w-0">
                      <div className="text-xs text-[var(--semi-color-text-2)]">
                        {t("Apple verification code")}
                      </div>
                      <div className="mt-1 break-all font-mono text-xl font-semibold text-[var(--semi-color-text-0)]">
                        <CopyableTableText
                          copiedText={t("Copied")}
                          text={preparation.verificationCode}
                        />
                      </div>
                    </div>
                  </div>
                ) : preparation.status === "expired" || preparation.status === "consumed" ? (
                  <div className="flex flex-wrap items-center justify-between gap-3 border-l-2 border-[var(--semi-color-warning)] bg-[var(--semi-color-warning-light-default)] px-3 py-3 text-sm">
                    <span>{t("This forwarding address is no longer available.")}</span>
                    <Button
                      icon={<RefreshCw size={14} />}
                      onClick={() => void prepareForwardingMailbox()}
                      size="small"
                      type="tertiary"
                    >
                      {t("Generate another")}
                    </Button>
                  </div>
                ) : (
                  <div className="flex items-center gap-2 text-sm text-[var(--semi-color-text-2)]">
                    <Spin size="small" />
                    {t("Waiting for mail from noreply@apple.com; checking every 5 seconds.")}
                  </div>
                )}

                {pollError ? (
                  <div className="text-xs leading-5 text-[var(--semi-color-warning)]">
                    {pollError}
                  </div>
                ) : null}
              </div>
            ) : null}
          </div>
          <div className="text-xs leading-5 text-[var(--semi-color-text-2)]">
            {t("After entering the code on the Apple verification page, click Next to import the account credentials.")}
          </div>
        </div>
      ) : (
        <div className="space-y-4 py-1">
          <div className="flex items-center justify-between text-sm">
            <span className="font-medium text-[var(--semi-color-text-0)]">
              {t("Import account credentials")}
            </span>
            <span className="text-[var(--semi-color-text-2)]">2 / 2</span>
          </div>
          {preparation ? (
            <div className="flex flex-wrap items-center gap-x-2 gap-y-1 text-xs">
              <span className="text-[var(--semi-color-text-2)]">
                {t("Verified forwarding address")}
              </span>
              <CopyableTableText copiedText={t("Copied")} text={preparation.forwardToEmail} />
            </div>
          ) : null}
          <div className="border-y border-[var(--semi-color-border)] py-3">
            <div className="mb-3 text-sm font-medium text-[var(--semi-color-text-0)]">
              {t("Before importing")}
            </div>
            <ol className="space-y-3">
              <li className="grid grid-cols-[20px_minmax(0,1fr)] gap-2">
                <span className="flex size-5 items-center justify-center rounded-full bg-[var(--semi-color-fill-1)] text-xs font-medium text-[var(--semi-color-text-1)]">1</span>
                <div className="min-w-0">
                  <div className="text-sm font-medium text-[var(--semi-color-text-0)]">
                    {t("Copy the new-session cURL")}
                  </div>
                  <p className="mt-1 text-xs leading-5 text-[var(--semi-color-text-2)]">
                    {t("Open account.apple.com, use Developer Tools > Network, select the request below, then choose Copy as cURL (bash).")}
                  </p>
                  <div className="mt-1 space-y-0.5">
                    <code className="block break-all font-mono text-xs text-[var(--semi-color-primary)]">
                      https://appleid.apple.com/account/manage/email/private
                    </code>
                    <code className="block break-all font-mono text-xs text-[var(--semi-color-primary)]">
                      https://appleid.apple.com.cn/account/manage/email/private
                    </code>
                  </div>
                </div>
              </li>
              <li className="grid grid-cols-[20px_minmax(0,1fr)] gap-2">
                <span className="flex size-5 items-center justify-center rounded-full bg-[var(--semi-color-fill-1)] text-xs font-medium text-[var(--semi-color-text-1)]">2</span>
                <div className="min-w-0">
                  <div className="text-sm font-medium text-[var(--semi-color-text-0)]">
                    {t("Copy the old-session cURL")}
                  </div>
                  <p className="mt-1 text-xs leading-5 text-[var(--semi-color-text-2)]">
                    {t("Open Hide My Email on icloud.com, use Developer Tools > Network, select the request below, then choose Copy as cURL (bash).")}
                  </p>
                  <div className="mt-1 space-y-0.5">
                    <code className="block break-all font-mono text-xs text-[var(--semi-color-primary)]">
                      {"https://<pod>-maildomainws.icloud.com/v2/hme/list"}
                    </code>
                    <code className="block break-all font-mono text-xs text-[var(--semi-color-primary)]">
                      {"https://<pod>-maildomainws.icloud.com.cn/v2/hme/list"}
                    </code>
                  </div>
                  <p className="mt-1 text-xs leading-5 text-[var(--semi-color-text-2)]">
                    {t("The pod prefix and query parameters vary by account; filter Network by /v2/hme/list.")}
                  </p>
                </div>
              </li>
            </ol>
            <p className="mt-3 text-xs leading-5 text-[var(--semi-color-text-2)]">
              {t("At least one cURL is required. When both are provided, either order is accepted and the system identifies each session by its request URL.")}
            </p>
            <div className="mt-3 flex gap-2 rounded-lg bg-[var(--semi-color-success-light-default)] px-3 py-2 text-xs leading-5 text-[var(--semi-color-text-0)]">
              <ShieldCheck className="mt-0.5 size-4 shrink-0 text-[var(--semi-color-success)]" />
              <span>
                {t("Validation requires the exact verified forwarding address and at least one Cookie that can create an alias.")}
              </span>
            </div>
          </div>

          <label className="block">
          <span className="mb-1.5 block text-sm font-medium text-[var(--semi-color-text-0)]">
            {t("Owner")} *
          </span>
          <OwnerSelect
            onChange={setOwnerId}
            owners={owners}
            t={t}
            value={ownerId}
          />
          </label>

          <label className="block">
          <span className="mb-1.5 block text-sm font-medium text-[var(--semi-color-text-0)]">
            {t("Resource expires at")} *
          </span>
          <DatePicker
            aria-label={t("Resource expires at")}
            format="yyyy-MM-dd HH:mm:ss"
            onChange={(value) => setExpireAt(value instanceof Date ? value : null)}
            showClear={false}
            style={{ width: "100%" }}
            type="dateTime"
            value={expireAt ?? undefined}
          />
          </label>

          <div className="grid grid-cols-2 gap-2">
          <button
            aria-pressed={mode === "paste"}
            className={switchButtonClass(mode === "paste")}
            onClick={() => {
              setMode("paste");
              setContent("");
              setFile(null);
              if (fileRef.current) fileRef.current.value = "";
            }}
            type="button"
          >
            <FileText size={16} />
            {t("Manual input")}
          </button>
          <button
            aria-pressed={mode === "file"}
            className={switchButtonClass(mode === "file")}
            onClick={() => {
              setMode("file");
              setContent("");
            }}
            type="button"
          >
            <Upload size={16} />
            {t("TXT file")}
          </button>
          </div>

          <div className="grid grid-cols-2 gap-2">
          <button
            aria-pressed={errorStrategy === "skip"}
            className={switchButtonClass(errorStrategy === "skip")}
            onClick={() => setErrorStrategy("skip")}
            type="button"
          >
            {t("Skip errors")}
          </button>
          <button
            aria-pressed={errorStrategy === "abort"}
            className={switchButtonClass(errorStrategy === "abort")}
            onClick={() => setErrorStrategy("abort")}
            type="button"
          >
            {t("Abort on error")}
          </button>
          </div>

          <div>
          {mode === "paste" ? (
            <label className="block">
              <span className="mb-1.5 flex items-center justify-between text-sm font-medium text-[var(--semi-color-text-0)]">
                <span>{t("iCloud resource entries")} *</span>
                <Text size="small" type="tertiary">
                  {t("Parsed entries", { count: lineCount })}
                </Text>
              </span>
              <TextArea
                className="font-mono"
                onChange={setContent}
                placeholder="apple-id@example.com----curl ..."
                rows={8}
                style={{ height: IMPORT_ENTRY_AREA_HEIGHT, resize: "none" }}
                value={content}
              />
            </label>
          ) : (
            <>
              <input
                accept=".txt,text/plain"
                className="hidden"
                onChange={(event) => setFile(event.target.files?.[0] ?? null)}
                ref={fileRef}
                type="file"
              />
              <button
                aria-label={t("Select TXT file")}
                className="flex w-full cursor-pointer flex-col items-center justify-center rounded-xl border border-dashed border-[var(--semi-color-border)] bg-[var(--semi-color-fill-0)] p-6 text-center transition-colors hover:bg-[var(--semi-color-fill-1)]"
                onClick={() => fileRef.current?.click()}
                style={{ height: IMPORT_ENTRY_AREA_HEIGHT }}
                type="button"
              >
                <FileText className="mb-2 size-8 text-[var(--semi-color-text-2)]" />
                <Text strong>{file ? file.name : t("Select TXT file")}</Text>
                <Text size="small" type="tertiary">
                  {file
                    ? `${(file.size / 1024).toFixed(1)} KB`
                    : t("Supports .txt files, one entry per line")}
                </Text>
              </button>
            </>
          )}
          </div>

          <div className="rounded-xl border border-[var(--semi-color-border)] bg-[var(--semi-color-fill-0)] p-3">
          <div className="mb-1 text-xs font-medium text-[var(--semi-color-text-0)]">
            {t("Supported format")}
          </div>
          <code className="block whitespace-pre-wrap break-all font-mono text-xs leading-relaxed text-[var(--semi-color-text-2)]">
            {"email----oldCurl\nemail----newCurl\nemail----newCurl----oldCurl\nemail----oldCurl----newCurl"}
          </code>
          </div>

          <div className="rounded-lg border border-[var(--semi-color-border)] bg-[var(--semi-color-fill-0)] px-3 py-2 text-xs leading-5 text-[var(--semi-color-text-2)]">
            {t(
              "iCloud cookies and cURL context are write-only and never returned by the resource API.",
            )}
          </div>
        </div>
      )}
    </Modal>
  );
}

export function EditICloudModal({
  canOperate,
  credentialsOnly = false,
  onCancel,
  onSaved,
  owners,
  target,
}: {
  canOperate: boolean;
  credentialsOnly?: boolean;
  onCancel: () => void;
  onSaved: (resourceId: number) => void | Promise<void>;
  owners: AdminICloudOwner[];
  target: AdminICloudResourceItem | null;
}) {
  const { t } = useTranslation();
  const [primaryEmail, setPrimaryEmail] = useState("");
  const [ownerId, setOwnerId] = useState<number | undefined>();
  const [forSale, setForSale] = useState(false);
  const [expireAt, setExpireAt] = useState<Date | null>(() =>
    target ? new Date(target.expireAt) : null,
  );
  const [newCurl, setNewCurl] = useState("");
  const [oldCurl, setOldCurl] = useState("");
  const [submitting, setSubmitting] = useState(false);

  useEffect(() => {
    if (!target) return;
    setPrimaryEmail(target.primaryEmail);
    setOwnerId(target.owner.id);
    setForSale(target.forSale);
    setExpireAt(new Date(target.expireAt));
    setNewCurl("");
    setOldCurl("");
  }, [target]);

  const submit = async () => {
    if (!target || (!credentialsOnly && !ownerId)) return;
    const nextEmail = primaryEmail.trim().toLowerCase();
    if (!/^[^\s@]+@[^\s@]+$/.test(nextEmail)) {
      Toast.warning(t("A valid iCloud email address is required."));
      return;
    }
    const normalizedNewCurl = normalizeICloudImportContent(newCurl).trim();
    const normalizedOldCurl = normalizeICloudImportContent(oldCurl).trim();
    const curls = [normalizedNewCurl, normalizedOldCurl].filter(Boolean);
    const emailChanged = nextEmail !== target.primaryEmail.trim().toLowerCase();
    if ((credentialsOnly || emailChanged) && curls.length === 0) {
      Toast.warning(t("At least one iCloud cURL is required."));
      return;
    }
    const nextImportLine = curls.length > 0
      ? [nextEmail, ...curls].join("----")
      : "";
    const expireAtChanged =
      canOperate &&
      expireAt?.getTime() !== new Date(target.expireAt).getTime();
    if (
      expireAtChanged &&
      (!expireAt || !Number.isFinite(expireAt.getTime()) || expireAt.getTime() <= Date.now())
    ) {
      Toast.warning(t("Expiration must be in the future."));
      return;
    }
    if (nextImportLine && !canOperate) {
      Toast.warning(t("Permission denied"));
      return;
    }

    const request: AdminICloudUpdateRequest = { version: target.version };
    if (!credentialsOnly) {
      if (ownerId !== target.owner.id) request.ownerId = ownerId;
      if (canOperate && forSale !== target.forSale) request.forSale = forSale;
    }
    if (expireAtChanged && expireAt) request.expireAt = expireAt.toISOString();
    if (nextImportLine) request.importLine = nextImportLine;
    if (Object.keys(request).length === 1) {
      Toast.info(t("No changes to save."));
      return;
    }

    setSubmitting(true);
    try {
      await updateAdminICloudResource(target.id, request);
      Toast.success(t(credentialsOnly
        ? "Credentials replaced."
        : "iCloud resource updated."));
      await onSaved(target.id);
      onCancel();
    } catch (error) {
      Toast.error(getIamErrorMessage(t, error, "iCloud resource update failed."));
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Modal
      cancelText={t("Cancel")}
      centered
      confirmLoading={submitting}
      onCancel={onCancel}
      onOk={() => void submit()}
      okText={t(credentialsOnly ? "Replace credentials" : "Save")}
      title={t(credentialsOnly ? "Replace iCloud credentials" : "Edit iCloud resource")}
      visible={Boolean(target)}
      width="min(680px, calc(100vw - 32px))"
    >
      {target ? (
        <div className="space-y-4 py-1">
          {credentialsOnly ? (
            <div className="rounded-lg border border-[var(--semi-color-warning-light-active)] bg-[var(--semi-color-warning-light-default)] px-3 py-2 text-sm text-[var(--semi-color-text-0)]">
              {t("Existing cURLs are write-only. Enter at least one replacement; a blank field keeps that channel unchanged unless you change the primary email.")}
            </div>
          ) : null}

          <label className="block">
            <span className="mb-1.5 block text-sm font-medium text-[var(--semi-color-text-0)]">
              {t("Primary email")} *
            </span>
            <Input
              className="font-mono"
              disabled={!canOperate}
              onChange={(value) => setPrimaryEmail(String(value))}
              placeholder="name@example.com"
              value={primaryEmail}
            />
          </label>

          {!credentialsOnly ? (
            <>
              <label className="block">
                <span className="mb-1.5 block text-sm font-medium text-[var(--semi-color-text-0)]">
                  {t("Owner")}
                </span>
                <OwnerSelect
                  onChange={setOwnerId}
                  owners={owners}
                  selectedOwner={target.owner}
                  t={t}
                  value={ownerId}
                />
              </label>

              {canOperate ? (
                <div className="flex items-center justify-between rounded-lg bg-[var(--semi-color-fill-0)] px-3 py-2.5">
                  <div>
                    <div className="text-sm font-medium text-[var(--semi-color-text-0)]">
                      {t("Public sale")}
                    </div>
                    <div className="text-xs text-[var(--semi-color-text-2)]">
                      {t("This setting is independent from Cookie validity.")}
                    </div>
                  </div>
                  <Switch
                    aria-label={t("Public sale")}
                    checked={forSale}
                    onChange={setForSale}
                    size="small"
                  />
                </div>
              ) : null}
            </>
          ) : null}

          <label className="block">
            <span className="mb-1.5 block text-sm font-medium text-[var(--semi-color-text-0)]">
              {t("Resource expires at")} *
            </span>
            <DatePicker
              aria-label={t("Resource expires at")}
              disabled={!canOperate}
              format="yyyy-MM-dd HH:mm:ss"
              onChange={(value) => setExpireAt(value instanceof Date ? value : null)}
              showClear={false}
              style={{ width: "100%" }}
              type="dateTime"
              value={expireAt ?? undefined}
            />
          </label>

          {canOperate ? (
            <div className="rounded-lg border border-[var(--semi-color-border)] p-3">
              <div className="mb-1 text-sm font-medium text-[var(--semi-color-text-0)]">
                {t("Credentials")}
              </div>
              <div className="mb-3 text-xs leading-5 text-[var(--semi-color-text-2)]">
                {t("iCloud cURLs are write-only. With the same primary email, each non-empty field replaces only its matching channel and a blank field preserves it. Changing the primary email replaces the complete channel set.")}
              </div>
              <div className="space-y-3">
                <label className="block">
                  <span className="mb-1.5 block text-sm font-medium text-[var(--semi-color-text-0)]">
                    {t("New Cookie cURL")}
                  </span>
                  <TextArea
                    className="font-mono"
                    onChange={setNewCurl}
                    placeholder="curl --url 'https://appleid.apple.com/account/manage/...'"
                    rows={4}
                    style={{ resize: "none" }}
                    value={newCurl}
                  />
                </label>
                <label className="block">
                  <span className="mb-1.5 block text-sm font-medium text-[var(--semi-color-text-0)]">
                    {t("Old Cookie cURL")}
                  </span>
                  <TextArea
                    className="font-mono"
                    onChange={setOldCurl}
                    placeholder="curl --url 'https://*-maildomainws.icloud.com/v2/hme/list?...'"
                    rows={4}
                    style={{ resize: "none" }}
                    value={oldCurl}
                  />
                </label>
              </div>
            </div>
          ) : null}
        </div>
      ) : null}
    </Modal>
  );
}

export function ICloudMaintenanceModal({
  aliasLimit,
  onCancel,
  onCompleted,
  target,
}: {
  aliasLimit: number;
  onCancel: () => void;
  onCompleted: (resourceId?: number) => void | Promise<void>;
  target: ICloudMaintenanceTarget | null;
}) {
  const { t } = useTranslation();
  const [selected, setSelected] = useState<ICloudRowMaintenanceAction>("validate");
  const [submitting, setSubmitting] = useState(false);

  useEffect(() => {
    if (target) setSelected("validate");
  }, [target]);

  if (!target) return null;

  const rowDisabled = target.mode === "row" &&
    (target.item.status === "disabled" || target.item.status === "deleted");
  const actions: Array<{
    description: string;
    disabled: boolean;
    icon: typeof ShieldCheck;
    key: ICloudRowMaintenanceAction;
    label: string;
  }> = [
    {
      description: "Check whether each configured Cookie can create an alias.",
      disabled: rowDisabled,
      icon: ShieldCheck,
      key: "validate" as const,
      label: "Validate resource",
    },
    {
      description: "Queue dual-channel alias provisioning. Cookie state controls creation only.",
      disabled: rowDisabled || (target.mode === "row" && target.item.aliasCount >= aliasLimit),
      icon: AtSign,
      key: "alias" as const,
      label: "Create alias",
    },
  ];
  if (target.mode === "row") {
    actions.push({
      description: "Use this after enabling iCloud manually. The existing refresh workflow logs in with the permanent eSIM phone and stores the old V2 Cookie.",
      disabled:
        rowDisabled ||
        !target.item.boundPhoneNumber ||
        !target.item.kitesimPhoneId ||
        (target.item.icloudOpened && target.item.oldSession?.status === "valid"),
      icon: CloudDownload,
      key: "oldCookie",
      label: "Fetch old Cookie",
    });
    actions.push({
      description: "Confirm that family sharing has been enabled manually for an automatic onboarding resource before Apple account configuration continues.",
      disabled: rowDisabled,
      icon: Workflow,
      key: "familySharing",
      label: "Confirm family sharing",
    });
  }
  const selectedAction = actions.find((item) => item.key === selected) ?? actions[0];
  const rowItem = target.mode === "row" ? target.item : null;
  const submit = async () => {
    if (!selectedAction || selectedAction.disabled) return;
    setSubmitting(true);
    try {
      let count = 1;
      if (selected === "oldCookie") {
        if (target.mode !== "row") return;
        await activateAdminICloudResource(target.item.id, target.item.version);
      } else if (selected === "familySharing") {
        if (target.mode !== "row") return;
        const detailItem = target.item as AdminICloudResourceItem & {
          onboardingTask?: AdminICloudOnboardingTask | null;
        };
        const task = "onboardingTask" in detailItem
          ? detailItem.onboardingTask
          : (await getAdminICloudResourceDetail(target.item.id)).onboardingTask;
        if (
          !task ||
          task.taskKind !== "onboarding" ||
          task.status !== "waiting" ||
          task.stage !== "waiting_family_sharing" ||
          !task.needsFamilyReset
        ) {
          Toast.info(t("No pending family sharing confirmation."));
          return;
        }
        await confirmAdminICloudOnboardingFamilyReset(task.id);
      } else if (target.mode === "row") {
        if (selected === "alias") {
          const result = await createAdminICloudAliases(target.item.id, target.item.version);
          if (!result.changed) {
            Toast.info(t("Alias target already reached."));
            await onCompleted(target.item.id);
            onCancel();
            return;
          }
        } else {
          await validateAdminICloudResource(target.item.id, target.item.version);
        }
      } else {
        const response = target.mode === "ids"
          ? await batchAdminICloudResourcesByIds(selected, target.resourceIds)
          : await batchAdminICloudResourcesByFilter(selected, target.filter);
        count = response.affected;
      }
      Toast.success(t(
        selected === "alias"
          ? "Alias creation batch submitted."
          : selected === "familySharing"
            ? "Family sharing confirmed."
            : selected === "oldCookie"
              ? "Old Cookie refresh submitted."
              : "Resource validation batch submitted.",
        { count },
      ));
      await onCompleted(target.mode === "row" ? target.item.id : undefined);
      onCancel();
    } catch (error) {
      Toast.error(getIamErrorMessage(t, error, "iCloud resource operation failed."));
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Modal
      cancelText={t("Cancel")}
      centered
      confirmLoading={submitting}
      okButtonProps={{ disabled: selectedAction.disabled }}
      okText={t("Submit maintenance task")}
      onCancel={onCancel}
      onOk={() => void submit()}
      title={t("iCloud resource maintenance")}
      visible
      width="min(680px, calc(100vw - 32px))"
    >
      <div className="space-y-4 py-1">
        <div className="flex items-center justify-between gap-3 rounded-lg bg-[var(--semi-color-fill-0)] px-3 py-2">
          <div>
            <div className="text-xs text-[var(--semi-color-text-2)]">
              {t(target.mode === "row" ? "Resource" : "Scope")}
            </div>
            <div className="mt-1 break-all text-sm font-medium text-[var(--semi-color-text-0)]">
              {target.mode === "row"
                ? target.item.primaryEmail
                : t(target.mode === "ids" ? "Selected iCloud resources" : "Matching resources", {
                    count: target.count,
                  })}
            </div>
          </div>
          {target.mode === "row" ? null : (
            <Tag color="blue" shape="circle">{target.count}</Tag>
          )}
        </div>

        <div className="text-sm leading-6 text-[var(--semi-color-text-1)]">
          {t("Choose one maintenance operation. Ineligible resources will be skipped and counted by the server.")}
        </div>

        {rowItem ? (
          <div className="grid gap-3 rounded-lg border border-[var(--semi-color-border)] p-3 sm:grid-cols-2">
            <InfoItem
              label={t("Resource status")}
              value={<ResourceStatusTag item={rowItem} />}
            />
            <InfoItem
              label={t("Alias count")}
              value={<AliasCountTag count={rowItem.aliasCount} limit={aliasLimit} />}
            />
            <InfoItem label={t("Next validation")} value={formatTime(rowItem.nextValidationAt)} />
            <InfoItem label={t("Next provisioning")} value={formatTime(rowItem.nextProvisionAt)} />
            {rowItem.lastSafeError ? (
              <div className="sm:col-span-2 rounded-lg bg-[var(--semi-color-warning-light-default)] px-3 py-2 text-sm text-[var(--semi-color-text-0)]">
                {rowItem.lastSafeError}
              </div>
            ) : null}
          </div>
        ) : null}

        <div className="grid gap-3 sm:grid-cols-2">
          {actions.map((item) => {
            const Icon = item.icon;
            const active = selected === item.key;
            return (
              <button
                aria-pressed={active}
                className={`min-h-32 rounded-xl border p-4 text-left transition-colors ${
                  item.disabled
                    ? "cursor-not-allowed border-[var(--semi-color-border)] bg-[var(--semi-color-fill-0)] opacity-60"
                    : active
                    ? "border-[var(--semi-color-primary)] bg-[var(--semi-color-primary-light-default)]"
                    : "cursor-pointer border-[var(--semi-color-border)] bg-[var(--semi-color-bg-2)] hover:border-[var(--semi-color-primary)] hover:bg-[var(--semi-color-fill-0)]"
                }`}
                disabled={submitting || item.disabled}
                key={item.key}
                onClick={() => setSelected(item.key)}
                type="button"
              >
                <div className="flex items-start gap-3">
                  <span className="rounded-lg bg-[var(--semi-color-fill-0)] p-2 text-[var(--semi-color-primary)]">
                    <Icon aria-hidden size={20} />
                  </span>
                  <span className="min-w-0 flex-1">
                    <span className="font-semibold text-[var(--semi-color-text-0)]">
                      {t(item.label)}
                    </span>
                    <span className="mt-1.5 block text-xs leading-5 text-[var(--semi-color-text-2)]">
                      {t(item.description)}
                    </span>
                  </span>
                </div>
              </button>
            );
          })}
        </div>
      </div>
    </Modal>
  );
}

export function ICloudTasksPanel({
  canOperate,
  item,
  onRefresh,
  refreshGeneration,
}: {
  canOperate: boolean;
  item: AdminICloudResourceDetail;
  onRefresh: () => void | Promise<void>;
  refreshGeneration: number;
}) {
  const { t } = useTranslation();
  const [pageSize, setPageSize] = useSharedPageSize();
  const [page, setPage] = useState(1);
  const [refreshKey, setRefreshKey] = useState(0);
  const [busy, setBusy] = useState<ICloudMaintenanceAction | null>(null);
  const [submittedVersion, setSubmittedVersion] = useState<number | null>(null);
  const [loading, setLoading] = useState(true);
  const [errorMessage, setErrorMessage] = useState<string | null>(null);
  const [response, setResponse] = useState<AdminICloudTaskList>({
    items: [],
    limit: pageSize,
    offset: 0,
    succeeded: 0,
    total: 0,
  });

  useEffect(() => setPage(1), [item.id, pageSize]);
  useEffect(() => {
    if (submittedVersion !== null && item.version >= submittedVersion) {
      setSubmittedVersion(null);
    }
  }, [item.version, submittedVersion]);
  useEffect(() => {
    const controller = new AbortController();
    let pollTimer: ReturnType<typeof globalThis.setTimeout> | null = null;
    setLoading(true);
    setErrorMessage(null);
    void listAdminICloudTasks(
      item.id,
      (page - 1) * pageSize,
      pageSize,
      controller.signal,
    )
      .then((next) => {
        if (controller.signal.aborted) return;
        const lastPage = Math.max(1, Math.ceil(next.total / pageSize));
        if (page > lastPage) {
          setPage(lastPage);
          return;
        }
        setResponse(next);
        if (
          next.items.some(
            (task) => task.status === "queued" || task.status === "running",
          )
        ) {
          pollTimer = globalThis.setTimeout(
            () => setRefreshKey((value) => value + 1),
            1_500,
          );
        }
      })
      .catch((error) => {
        if (!controller.signal.aborted) {
          const message = getIamErrorMessage(t, error, "iCloud task load failed.");
          setErrorMessage(message);
          Toast.error(message);
        }
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false);
      });
    return () => {
      controller.abort();
      if (pollTimer) globalThis.clearTimeout(pollTimer);
    };
  }, [item.id, page, pageSize, refreshGeneration, refreshKey, t]);

  const runAction = async (action: ICloudMaintenanceAction) => {
    setBusy(action);
    try {
      if (action === "alias") {
        const result = await createAdminICloudAliases(item.id, item.version);
        setSubmittedVersion(result.version);
        if (!result.changed) {
          Toast.info(t("Alias target already reached."));
        } else {
          Toast.success(t("Alias creation batch submitted.", { count: 1 }));
        }
      } else {
        const result = await validateAdminICloudResource(item.id, item.version);
        setSubmittedVersion(result.version);
        Toast.success(t("Resource validation submitted."));
      }
      setPage(1);
      setRefreshKey((value) => value + 1);
      await onRefresh();
    } catch (error) {
      Toast.error(getIamErrorMessage(t, error, "iCloud resource operation failed."));
    } finally {
      setSubmittedVersion(null);
      setBusy(null);
    }
  };

  const columns = useMemo(
    () => [
      {
        dataIndex: "kind",
        title: t("Type"),
        width: 140,
        render: (value: unknown) => t(taskKindLabel(value as AdminICloudTask["kind"])),
      },
      {
        dataIndex: "status",
        title: t("Status"),
        width: 110,
        render: (value: unknown) => renderTaskStatusTag(value as AdminICloudTask["status"], t),
      },
      {
        dataIndex: "remainingAttempts",
        title: t("Remaining attempts"),
        width: 120,
        render: (value: unknown) => <span className="font-mono tabular-nums">{Number(value)}</span>,
      },
      {
        dataIndex: "queuedAt",
        title: t("Queued at"),
        width: 170,
        render: (value: unknown) => formatTime(value ? String(value) : null),
      },
      {
        dataIndex: "startedAt",
        title: t("Started at"),
        width: 170,
        render: (value: unknown) => formatTime(value ? String(value) : null),
      },
      {
        dataIndex: "finishedAt",
        title: t("Finished at"),
        width: 170,
        render: (value: unknown) => formatTime(value ? String(value) : null),
      },
      {
        dataIndex: "updatedAt",
        title: t("Updated at"),
        width: 170,
        render: (value: unknown) => formatTime(value ? String(value) : null),
      },
    ],
    [t],
  );
  const successRate = response.total > 0
    ? Math.round((response.succeeded / response.total) * 100)
    : 0;
  const awaitingVersionRefresh = submittedVersion !== null && item.version < submittedVersion;

  return (
    <div>
      <div className="mb-4 grid gap-3 sm:grid-cols-3">
        <InfoItem label={t("Total tasks")} value={<span className="font-mono tabular-nums">{response.total}</span>} />
        <InfoItem label={t("Succeeded tasks")} value={<span className="font-mono tabular-nums">{response.succeeded}</span>} />
        <InfoItem label={t("Success rate")} value={<span className="font-mono tabular-nums">{successRate}%</span>} />
      </div>
      {canOperate ? (
        <div className="mb-4 flex flex-wrap gap-2">
          <Button
            disabled={item.status === "deleted" || item.status === "disabled" || busy !== null || awaitingVersionRefresh}
            loading={busy === "validate"}
            onClick={() => void runAction("validate")}
            size="small"
            type="tertiary"
          >
            {t("Validate")}
          </Button>
          <Button
            disabled={item.status === "deleted" || item.status === "disabled" || item.aliasCount >= item.aliasLimit || busy !== null || awaitingVersionRefresh}
            loading={busy === "alias"}
            onClick={() => void runAction("alias")}
            size="small"
            type="tertiary"
          >
            {t("Create alias")}
          </Button>
        </div>
      ) : null}
      {errorMessage ? (
        <div className="mb-4 flex items-center justify-between gap-3 rounded-lg border border-[var(--semi-color-danger-light-active)] bg-[var(--semi-color-danger-light-default)] px-3 py-2 text-sm text-[var(--semi-color-text-0)]">
          <span>{errorMessage}</span>
          <Button onClick={() => setRefreshKey((value) => value + 1)} size="small">
            {t("Try again")}
          </Button>
        </div>
      ) : null}
      <ServerPaginatedDrawerTable
        columns={columns}
        dataSource={response.items}
        emptyDescription={t("No task records")}
        extraOffset={110}
        loading={loading}
        onPageChange={setPage}
        onPageSizeChange={(size) => {
          setPageSize(size);
          setPage(1);
        }}
        page={page}
        pageSize={pageSize}
        rowKey="taskId"
        scrollX={1050}
        t={t}
        total={response.total}
      />
    </div>
  );
}

export function ICloudDetailSheet({
  aliasLimit,
  busyAction,
  canFetchMessages,
  canOperate,
  canReadMessages,
  canReadOrders,
  canReadTasks,
  canWrite,
  item,
  loading,
  resourceId,
  refreshGeneration,
  onCancel,
  onDelete,
  onEdit,
  onMaintain,
  onRefresh,
  onReplaceCredentials,
  onRecover,
  onSetExpiration,
  onToggleDisabled,
  onTogglePublish,
}: {
  aliasLimit: number;
  busyAction: RowAction | null;
  canFetchMessages: boolean;
  canOperate: boolean;
  canReadMessages: boolean;
  canReadOrders: boolean;
  canReadTasks: boolean;
  canWrite: boolean;
  item: AdminICloudResourceDetail | null;
  loading: boolean;
  resourceId: number | null;
  refreshGeneration: number;
  onCancel: () => void;
  onDelete: (item: AdminICloudResourceItem) => void;
  onEdit: (item: AdminICloudResourceItem) => void;
  onMaintain: (item: AdminICloudResourceItem) => void;
  onRefresh: () => void | Promise<void>;
  onReplaceCredentials: (item: AdminICloudResourceItem) => void;
  onRecover: (item: AdminICloudResourceItem) => void;
  onSetExpiration: (item: AdminICloudResourceItem) => void;
  onToggleDisabled: (item: AdminICloudResourceItem) => void;
  onTogglePublish: (item: AdminICloudResourceItem) => void;
}) {
  const { t } = useTranslation();
  const isMobile = useIsMobile();
  const [activeTab, setActiveTab] = useState("basic");
  const [aliasPage, setAliasPage] = useState(1);
  const [aliasPageSize, setAliasPageSize] = useState(20);
  const [aliases, setAliases] = useState<AdminICloudAliasItem[]>([]);
  const [aliasTotal, setAliasTotal] = useState(0);
  const [aliasesLoading, setAliasesLoading] = useState(false);

  useEffect(() => {
    setActiveTab("basic");
    setAliasPage(1);
    setAliases([]);
    setAliasTotal(0);
  }, [item?.id]);

  useEffect(() => {
    if (!item || activeTab !== "aliases") return;
    const controller = new AbortController();
    setAliasesLoading(true);
    void listAdminICloudAliases(
      item.id,
      (aliasPage - 1) * aliasPageSize,
      aliasPageSize,
      controller.signal,
    )
      .then((response) => {
        if (controller.signal.aborted) return;
        setAliases(response.items);
        setAliasTotal(response.total);
        const lastPage = Math.max(1, Math.ceil(response.total / aliasPageSize));
        if (aliasPage > lastPage) setAliasPage(lastPage);
      })
      .catch((error) => {
        if (!controller.signal.aborted) {
          Toast.error(getIamErrorMessage(t, error, "iCloud aliases load failed."));
        }
      })
      .finally(() => {
        if (!controller.signal.aborted) setAliasesLoading(false);
      });
    return () => controller.abort();
  }, [activeTab, aliasPage, aliasPageSize, item?.id, refreshGeneration, t]);

  const aliasColumns = useMemo(
    () => [
      {
        dataIndex: "email",
        title: t("Alias email"),
        width: 260,
        render: (value: unknown) => (
          <CopyableTableText copiedText={t("Copied")} text={String(value)} />
        ),
      },
      {
        dataIndex: "anonymousId",
        title: t("Alias ID"),
        width: 220,
        render: (value: unknown) =>
          value ? (
            <CopyableTableText copiedText={t("Copied")} text={String(value)} />
          ) : (
            "-"
          ),
      },
      {
        dataIndex: "status",
        title: t("Status"),
        width: 110,
        render: (value: unknown) => {
          const meta = aliasStatusMeta[value as keyof typeof aliasStatusMeta];
          return (
            <Tag color={meta.color} shape="circle" size="small">
              {t(meta.label)}
            </Tag>
          );
        },
      },
      {
        dataIndex: "origin",
        title: t("Origin"),
        width: 140,
        render: (value: unknown) => String(value || "-"),
      },
      {
        dataIndex: "providerCreatedAt",
        title: t("Provider created at"),
        width: 180,
        render: (value: unknown) => formatTime(value ? String(value) : null),
      },
      {
        dataIndex: "lastSeenAt",
        title: t("Last seen"),
        width: 180,
        render: (value: unknown) => formatTime(value ? String(value) : null),
      },
      {
        dataIndex: "lastAllocatedAt",
        title: t("Last allocated"),
        width: 180,
        render: (value: unknown) => formatTime(value ? String(value) : null),
      },
    ],
    [t],
  );

  const aliasPagination = createCardProPagination({
    currentPage: aliasPage,
    isMobile,
    onPageChange: setAliasPage,
    onPageSizeChange: (size) => {
      setAliasPageSize(size);
      setAliasPage(1);
    },
    pageSize: aliasPageSize,
    pageSizeOpts: [10, 20, 50, 100],
    showSizeChanger: true,
    total: aliasTotal,
    t,
  });

  return (
    <SideSheet
      bodyStyle={{ padding: 0 }}
      onCancel={onCancel}
      placement="right"
      title={
        resourceId
          ? `${t("iCloud resource detail")} #${resourceId}`
          : t("iCloud resource detail")
      }
      visible={Boolean(resourceId)}
      width={isMobile ? "100%" : 940}
    >
      {loading && !item ? (
        <div className="flex min-h-80 items-center justify-center">
          <Spin size="large" />
        </div>
      ) : item ? (
        <div className="flex min-h-full flex-col">
          <div className="sticky top-0 z-10 bg-[var(--semi-color-bg-2)] px-5 pt-2">
            <Tabs
              activeKey={activeTab}
              collapsible
              onChange={setActiveTab}
              type="line"
            >
              <Tabs.TabPane itemKey="basic" tab={t("Basic info")} />
              {canReadOrders ? <Tabs.TabPane itemKey="orders" tab={t("Orders")} /> : null}
              <Tabs.TabPane itemKey="validation" tab={t("Validation")} />
              <Tabs.TabPane itemKey="aliases" tab={t("Aliases")} />
              {canReadTasks ? <Tabs.TabPane itemKey="tasks" tab={t("Task details")} /> : null}
              {canReadMessages ? <Tabs.TabPane itemKey="mails" tab={t("Mailbox")} /> : null}
              {canReadMessages ? <Tabs.TabPane itemKey="auxiliary" tab={t("Auxiliary mailbox")} /> : null}
            </Tabs>
          </div>

          <div className="flex-1 p-5">
            {activeTab === "basic" ? (
              <div className="space-y-5">
                <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
                  <InfoItem label="ID" value={<span className="font-mono">#{item.id}</span>} />
                  <InfoItem
                    label={t("Primary email")}
                    value={
                      <CopyableTableText copiedText={t("Copied")} text={item.primaryEmail} />
                    }
                  />
                  <InfoItem
                    label={t("Forwarding mailbox")}
                    value={
                      item.selectedForwardTo ? (
                        <CopyableTableText copiedText={t("Copied")} text={item.selectedForwardTo} />
                      ) : (
                        "-"
                      )
                    }
                  />
                  <InfoItem
                    label={t("Account role")}
                    value={<ICloudAccountRoleTag role={item.accountRole} />}
                  />
                  <InfoItem
                    label={t("Region")}
                    value={[item.region, item.countryCode].filter(Boolean).join(" · ") || "-"}
                  />
                  <InfoItem
                    label={t("iCloud opened")}
                    value={
                      <Tag color={item.icloudOpened ? "green" : "orange"} shape="circle" size="small">
                        {t(item.icloudOpened ? "Yes" : "No")}
                      </Tag>
                    }
                  />
                  <InfoItem
                    label={t("Bound phone")}
                    value={
                      item.boundPhoneNumber ? (
                        <CopyableTableText copiedText={t("Copied")} text={item.boundPhoneNumber} />
                      ) : (
                        "-"
                      )
                    }
                  />
                  <InfoItem
                    label={t("Phone source")}
                    value={phoneSourceLabel(item.boundPhoneSource, t)}
                  />
                  <InfoItem
                    label={t("Kitesim phone ID")}
                    value={item.kitesimPhoneId ? `#${item.kitesimPhoneId}` : "-"}
                  />
                  <InfoItem
                    label={t("Family primary")}
                    value={
                      item.familyPrimaryEmail ||
                      (item.familyPrimaryResourceId ? `#${item.familyPrimaryResourceId}` : "-")
                    }
                  />
                  <InfoItem
                    label={t("Family children")}
                    value={
                      item.accountRole === "primary"
                        ? `${item.familyChildCount}/${item.familyChildLimit}`
                        : "-"
                    }
                  />
                  <InfoItem
                    label={t("Family sync")}
                    value={item.accountRole === "primary" ? <ICloudFamilySyncTag item={item} /> : "-"}
                  />
                  <InfoItem
                    label={t("Family synced at")}
                    value={item.accountRole === "primary" ? formatTime(item.familySyncedAt) : "-"}
                  />
                  <InfoItem
                    label={t("Family sync error")}
                    value={item.accountRole === "primary" ? item.familySyncErrorCategory || "-" : "-"}
                  />
                  <InfoItem
                    label={t("Invitation URL")}
                    value={
                      item.familyInviteUrl ? (
                        <CopyableTableText copiedText={t("Copied")} text={item.familyInviteUrl} />
                      ) : (
                        "-"
                      )
                    }
                  />
                  <InfoItem
                    label={t("Owner")}
                    value={<OwnerIdentity owner={item.owner} t={t} />}
                  />
                  <InfoItem label={t("Status")} value={<ResourceStatusTag item={item} />} />
                  <InfoItem
                    label={t("Private")}
                    value={
                      <Tag color={!item.forSale ? "green" : "grey"} shape="circle" size="small">
                        {!item.forSale ? t("Yes") : t("No")}
                      </Tag>
                    }
                  />
                  <InfoItem label={t("Created at")} value={formatTime(item.createdAt)} />
                  <InfoItem label={t("Updated at")} value={formatTime(item.updatedAt)} />
                  <InfoItem label={t("Last allocated")} value={formatTime(item.lastAllocatedAt)} />
                </div>
              </div>
            ) : null}

            {activeTab === "validation" ? (
              <div className="space-y-5">
                <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
                  <InfoItem
                    label={t("Resource status")}
                    value={<ResourceStatusTag item={item} />}
                  />
                  <InfoItem
                    label={t("New Cookie")}
                    value={<SessionStatusTag session={item.newSession} />}
                  />
                  <InfoItem
                    label={t("Old Cookie")}
                    value={<SessionStatusTag session={item.oldSession} />}
                  />
                  <InfoItem
                    label={t("Alias count")}
                    value={<AliasCountTag count={item.aliasCount} limit={aliasLimit} />}
                  />
                  <InfoItem label={t("Aliases remaining")} value={item.aliasRemaining} />
                  <InfoItem
                    label={t("Alias provisioning")}
                    value={t(item.aliasProvisioning ? "Running" : "Idle")}
                  />
                  <InfoItem label={t("Credential revision")} value={item.credentialRevision} />
                  <InfoItem label={t("Credential updated at")} value={formatTime(item.credentialUpdatedAt)} />
                  <InfoItem label={t("Validation generation")} value={item.validationGeneration} />
                  <InfoItem label={t("Validation failures")} value={item.validationFailures} />
                  <InfoItem label={t("Resource expires at")} value={formatTime(item.expireAt)} />
                  <InfoItem label={t("Next validation")} value={formatTime(item.nextValidationAt)} />
                  <InfoItem label={t("Next provisioning")} value={formatTime(item.nextProvisionAt)} />
                  <InfoItem label={t("Last checked")} value={formatTime(item.lastCheckedAt)} />
                  <InfoItem label={t("Last valid")} value={formatTime(item.lastValidAt)} />
                  <InfoItem label={t("Last alias sync")} value={formatTime(item.lastAliasSyncAt)} />
                </div>
                {item.lastSafeError ? (
                  <div className="rounded-lg border border-[var(--semi-color-warning-light-active)] bg-[var(--semi-color-warning-light-default)] px-3 py-2 text-sm text-[var(--semi-color-text-0)]">
                    {item.lastSafeError}
                  </div>
                ) : null}
                {[
                  { task: item.onboardingTask, title: t("Automatic Apple onboarding") },
                  { task: item.refreshTask, title: t("Cookie refresh task") },
                ].map(({ task, title }) =>
                  task ? (
                    <div
                      className="border-t border-[var(--semi-color-border)] pt-4"
                      key={title}
                    >
                      <div className="mb-3 text-sm font-semibold text-[var(--semi-color-text-0)]">
                        {title}
                      </div>
                      <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
                        <InfoItem
                          label={t("Status")}
                          value={onboardingTaskStatusTag(task, t)}
                        />
                        <InfoItem
                          label={t("Stage")}
                          value={
                            <code className="break-all text-xs">{task.stage}</code>
                          }
                        />
                        <InfoItem
                          label={t("Bound phone")}
                          value={task.boundPhoneNumber || item.boundPhoneNumber || "-"}
                        />
                        <InfoItem
                          label={t("Next attempt")}
                          value={formatTime(task.nextAttemptAt)}
                        />
                      </div>
                      {task.lastSafeError ? (
                        <div className="mt-3 text-sm text-[var(--semi-color-warning)]">
                          {task.lastSafeError}
                        </div>
                      ) : null}
                      {task.needsManualCode ||
                      task.needsICloudActivation ||
                      task.needsFamilyReset ||
                      task.needsPostFamilyRecovery ? (
                        <div className="mt-3">
                          <ICloudOnboardingTaskAction
                            disabled={!canOperate}
                            onChanged={onRefresh}
                            task={task}
                          />
                        </div>
                      ) : null}
                    </div>
                  ) : null,
                )}
              </div>
            ) : null}

            {activeTab === "orders" && canReadOrders ? (
              <RelatedOrdersTable
                key={`${item.id}-${refreshGeneration}`}
                resourceId={item.id}
                resourceType="icloud"
                t={t}
              />
            ) : null}

            {activeTab === "aliases" ? (
              <div className="flex flex-col" style={{ height: DRAWER_PANEL_HEIGHT }}>
                <div className="min-h-0 flex-1 overflow-hidden">
                  {aliasesLoading && aliases.length === 0 ? (
                    <div className="flex h-full items-center justify-center">
                      <Spin size="large" />
                    </div>
                  ) : aliases.length === 0 ? (
                    <Empty description={t("No iCloud aliases found")} style={{ padding: 24 }} />
                  ) : (
                    <Table
                      columns={aliasColumns}
                      dataSource={aliases}
                      loading={aliasesLoading}
                      pagination={false}
                      rowKey="id"
                      scroll={{ x: 1390, y: DRAWER_TABLE_SCROLL_Y }}
                      size="small"
                    />
                  )}
                </div>
                {aliasTotal > 0 ? (
                  <div className="mt-3 flex flex-wrap items-center justify-end gap-3 border-t border-[var(--semi-color-border)] pt-3">
                    {aliasPagination}
                  </div>
                ) : null}
              </div>
            ) : null}

            {activeTab === "tasks" && canReadTasks ? (
              <ICloudTasksPanel
                canOperate={canOperate}
                item={item}
                onRefresh={onRefresh}
                refreshGeneration={refreshGeneration}
              />
            ) : null}

            {activeTab === "mails" && canReadMessages ? (
              <ResourceMailsPanel
                fetchDisabled={item.status === "deleted"}
                fetchEnabled={canFetchMessages}
                key={`${item.id}-${refreshGeneration}`}
                resourceId={item.id}
                resourceType="icloud"
                t={t}
              />
            ) : null}

            {activeTab === "auxiliary" && canReadMessages ? (
              item.selectedForwardTo ? (
                <>
                  <div className="mb-3 flex flex-wrap items-center gap-2 text-sm">
                    <span className="text-[var(--semi-color-text-2)]">
                      {t("Auxiliary email")}
                    </span>
                    <CopyableTableText
                      copiedText={t("Copied")}
                      text={item.selectedForwardTo}
                    />
                  </div>
                  <ResourceMailsPanel
                    auxiliary
                    emptyDescription={t("No auxiliary mail yet")}
                    extraOffset={40}
                    hideMailboxMeta
                    key={`auxiliary-${item.id}-${refreshGeneration}`}
                    resourceId={item.id}
                    resourceType="icloud"
                    t={t}
                  />
                </>
              ) : (
                <Empty
                  description={t("No auxiliary mailbox configured")}
                  style={{ padding: 32 }}
                />
              )
            ) : null}
          </div>

          {canWrite || canOperate ? (
            <div className="sticky bottom-0 flex flex-wrap items-center justify-end gap-2 border-t border-[var(--semi-color-border)] bg-[var(--semi-color-bg-0)] px-5 py-3">
              {item.status === "deleted" ? (
                canOperate ? (
                  <Button
                    disabled={Boolean(busyAction)}
                    loading={busyAction === "recover"}
                    onClick={() => onRecover(item)}
                    type="primary"
                  >
                    {t("Recover")}
                  </Button>
                ) : null
              ) : (
                <>
                  {canWrite ? (
                    <Button disabled={Boolean(busyAction)} onClick={() => onEdit(item)} type="tertiary">
                      {t("Edit")}
                    </Button>
                  ) : null}
                  {canOperate ? (
                    <>
                      <Button
                        disabled={Boolean(busyAction)}
                        onClick={() => onReplaceCredentials(item)}
                        type="tertiary"
                      >
                        {t("Replace credentials")}
                      </Button>
                      <Button disabled={Boolean(busyAction)} onClick={() => onMaintain(item)} type="primary">
                        {t("Maintenance")}
                      </Button>
                      <Button
                        disabled={Boolean(busyAction)}
                        onClick={() => onSetExpiration(item)}
                        type="tertiary"
                      >
                        {t("Set expiration")}
                      </Button>
                      <Button
                        disabled={Boolean(busyAction)}
                        loading={busyAction === "toggle"}
                        onClick={() => onToggleDisabled(item)}
                        type="tertiary"
                      >
                        {item.status === "disabled" ? t("Enable") : t("Disable")}
                      </Button>
                      <Button
                        disabled={Boolean(busyAction)}
                        loading={busyAction === "publish"}
                        onClick={() => onTogglePublish(item)}
                        type="tertiary"
                      >
                        {item.forSale ? t("Convert to private") : t("Put on sale")}
                      </Button>
                      <Button
                        disabled={Boolean(busyAction)}
                        loading={busyAction === "delete"}
                        onClick={() => onDelete(item)}
                        type="danger"
                      >
                        {t("Delete")}
                      </Button>
                    </>
                  ) : null}
                </>
              )}
            </div>
          ) : null}
        </div>
      ) : null}
    </SideSheet>
  );
}

export default function AdminICloudEmails() {
  const { t } = useTranslation();
  const { currentUser } = useAuth();
  const isMobile = useIsMobile();
  const [pageSize, setPageSize] = useSharedPageSize();
  const [activePage, setActivePage] = useState(1);
  const [searchKeyword, setSearchKeyword] = useState("");
  const [createdAtRange, setCreatedAtRange] = useState<DateRangeValue>([]);
  const [statusFilter, setStatusFilter] = useState<StatusFilter>("all");
  const [privateFilter, setPrivateFilter] = useState<BooleanFilter>("all");
  const [compactMode, setCompactMode] = useState(false);
  const [items, setItems] = useState<AdminICloudResourceItem[]>([]);
  const [facets, setFacets] = useState<AdminICloudResourceFacets>(EMPTY_FACETS);
  const [total, setTotal] = useState(0);
  const [aliasLimit, setAliasLimit] = useState(750);
  const [owners, setOwners] = useState<AdminICloudOwner[]>([]);
  const importOwners = useMemo(
    () => ownersWithCurrentUserFirst(owners, currentUser),
    [currentUser, owners],
  );
  const [loading, setLoading] = useState(true);
  const [importOpen, setImportOpen] = useState(false);
  const [importFlowMode, setImportFlowMode] =
    useState<ICloudImportFlowMode>("automatic");
  const [detailId, setDetailId] = useState<number | null>(null);
  const [detail, setDetail] = useState<AdminICloudResourceDetail | null>(null);
  const [detailLoading, setDetailLoading] = useState(false);
  const [editTarget, setEditTarget] = useState<AdminICloudResourceItem | null>(null);
  const [credentialTarget, setCredentialTarget] =
    useState<AdminICloudResourceItem | null>(null);
  const [maintenanceTarget, setMaintenanceTarget] =
    useState<ICloudMaintenanceTarget | null>(null);
  const [selectedKeys, setSelectedKeys] = useState<number[]>([]);
  const [rowBusy, setRowBusy] = useState<{ action: RowAction; id: number } | null>(
    null,
  );
  const [bulkBusy, setBulkBusy] = useState<ICloudBulkBusyAction | null>(null);
  const [refreshGeneration, setRefreshGeneration] = useState(0);
  const listRequestRef = useRef<AbortController | null>(null);
  const statsRequestRef = useRef<AbortController | null>(null);
  const detailRequestRef = useRef<AbortController | null>(null);
  const detailIdRef = useRef<number | null>(null);
  const [debouncedSearchKeyword, flushSearchKeyword] =
    useDebouncedValue(searchKeyword);
  const dateRangePresets = useMemo(() => createDateRangePresets(t), [t]);

  const canWrite = hasPermissionKey(
    currentUser,
    permissionKey("core:resource", "write"),
  );
  const canOperate = hasPermissionKey(
    currentUser,
    permissionKey("core:resource", "operate"),
  );
  const canReadOrders = hasPermissionKey(
    currentUser,
    permissionKey("alloc:allocation", "read"),
  );
  const canReadTasks = hasPermissionKey(
    currentUser,
    permissionKey("governance:task", "read"),
  );
  const canAutomaticImport = canOperate && canReadTasks;
  const canLegacyImport = canWrite;
  const canReadMessages = hasPermissionKey(
    currentUser,
    permissionKey("mailmatch:message", "read"),
  );
  const canFetchMessages = hasPermissionKey(
    currentUser,
    permissionKey("mailmatch:message", "operate"),
  );

  useEffect(() => {
    const controller = new AbortController();
    void listAdminICloudOwners("", controller.signal)
      .then((items) => {
        if (!controller.signal.aborted) setOwners(items);
      })
      .catch(() => {
        // Owner choices are optional UI data; the resource list remains usable.
      });
    return () => controller.abort();
  }, []);

  useEffect(() => () => {
    detailIdRef.current = null;
    detailRequestRef.current?.abort();
  }, []);

  const filter = useMemo<AdminICloudResourceListFilter>(() => {
    const next: AdminICloudResourceListFilter = {};
    const search = debouncedSearchKeyword.trim();
    const createdFrom = createdFromISOString(createdAtRange);
    const createdTo = createdToISOString(createdAtRange);
    if (search) next.search = search;
    if (statusFilter !== "all") next.status = statusFilter;
    if (privateFilter !== "all") next.forSale = privateFilter === "no";
    if (createdFrom) next.createdFrom = createdFrom;
    if (createdTo) next.createdTo = createdTo;
    return next;
  }, [
    createdAtRange,
    debouncedSearchKeyword,
    privateFilter,
    statusFilter,
  ]);

  const loadDetail = useCallback(async (resourceId: number, silent = false) => {
    if (detailIdRef.current !== resourceId) return true;
    detailRequestRef.current?.abort();
    const controller = new AbortController();
    detailRequestRef.current = controller;
    setDetailLoading(true);
    try {
      const response = await getAdminICloudResourceDetail(resourceId, controller.signal);
      if (controller.signal.aborted || detailIdRef.current !== resourceId) return true;
      setDetail(response);
      setAliasLimit(response.aliasLimit);
      return true;
    } catch (error) {
      if (controller.signal.aborted || detailIdRef.current !== resourceId) return true;
      if (!silent) {
        Toast.error(getIamErrorMessage(t, error, "iCloud resource detail load failed."));
      }
      return false;
    } finally {
      if (detailRequestRef.current === controller) {
        detailRequestRef.current = null;
        if (detailIdRef.current === resourceId) setDetailLoading(false);
      }
    }
  }, [t]);

  const refresh = useCallback(async () => {
    const resourceId = detailIdRef.current;
    if (resourceId !== null) await loadDetail(resourceId, true);
    setRefreshGeneration((value) => value + 1);
  }, [loadDetail]);

  const openDetail = useCallback((resourceId: number) => {
    detailIdRef.current = resourceId;
    setDetailId(resourceId);
    setDetail(null);
    void loadDetail(resourceId).then((loaded) => {
      if (!loaded && detailIdRef.current === resourceId) {
        detailIdRef.current = null;
        setDetailId(null);
      }
    });
  }, [loadDetail]);

  const closeDetail = useCallback(() => {
    detailIdRef.current = null;
    detailRequestRef.current?.abort();
    detailRequestRef.current = null;
    setDetailId(null);
    setDetail(null);
    setDetailLoading(false);
  }, []);

  useEffect(() => {
    const automationTaskActive = [detail?.onboardingTask, detail?.refreshTask].some(
      (task) => task?.status === "processing" || task?.status === "waiting",
    );
    if (
      detailId === null ||
      (!automationTaskActive &&
        detail?.status !== "pending" &&
        detail?.status !== "validating" &&
        (detail?.status !== "normal" ||
          detail.nextProvisionAt === null ||
          (detail?.newSession?.status !== "unchecked" &&
            detail?.oldSession?.status !== "unchecked")))
    ) {
      return;
    }
    let stopped = false;
    let timeoutId: number | undefined;
    const poll = () => {
      timeoutId = window.setTimeout(async () => {
        await loadDetail(detailId, true);
        if (!stopped && detailIdRef.current === detailId) poll();
      }, 3000);
    };
    poll();
    return () => {
      stopped = true;
      if (timeoutId !== undefined) window.clearTimeout(timeoutId);
    };
  }, [
    detail?.newSession?.status,
    detail?.onboardingTask?.status,
    detail?.oldSession?.status,
    detail?.refreshTask?.status,
    detail?.status,
    detailId,
    loadDetail,
  ]);

  useEffect(() => {
    listRequestRef.current?.abort();
    const controller = new AbortController();
    listRequestRef.current = controller;
    setLoading(true);
    void listAdminICloudResources(
      filter,
      (activePage - 1) * pageSize,
      pageSize,
      { includeFacets: false, includeTotal: false, signal: controller.signal },
    )
      .then((response) => {
        if (controller.signal.aborted) return;
        setItems(response.items);
        setAliasLimit(response.aliasLimit);
      })
      .catch((error) => {
        if (!controller.signal.aborted) {
          Toast.error(getIamErrorMessage(t, error, "iCloud resources load failed."));
        }
      })
      .finally(() => {
        if (listRequestRef.current === controller) {
          listRequestRef.current = null;
          setLoading(false);
        }
      });
    return () => controller.abort();
  }, [activePage, filter, pageSize, refreshGeneration, t]);

  useEffect(() => {
    statsRequestRef.current?.abort();
    const controller = new AbortController();
    statsRequestRef.current = controller;
    void listAdminICloudResources(filter, 0, 1, {
      includeFacets: true,
      includeTotal: true,
      signal: controller.signal,
    })
      .then((response) => {
        if (controller.signal.aborted) return;
        setFacets(response.facets);
        setTotal(response.total);
        setAliasLimit(response.aliasLimit);
      })
      .catch(() => {
        // The next filter change or manual refresh retries the statistics snapshot.
      })
      .finally(() => {
        if (statsRequestRef.current === controller) statsRequestRef.current = null;
      });
    return () => controller.abort();
  }, [filter, refreshGeneration]);

  useEffect(() => {
    setActivePage(1);
    setSelectedKeys([]);
  }, [filter, pageSize]);

  const applyMutation = useCallback(
    (
      item: AdminICloudResourceItem,
      action: RowAction,
      result: AdminICloudMutationResponse,
    ) => {
      const patch: Partial<AdminICloudResourceItem> = {
        forSale: result.forSale,
        status: result.status,
        version: result.version,
      };
      if (result.status === "pending") patch.lastSafeError = null;
      setItems((current) =>
        current.map((candidate) =>
          candidate.id === item.id ? { ...candidate, ...patch } : candidate,
        ),
      );
      setDetail((current) =>
        current?.id === item.id ? { ...current, ...patch } : current,
      );
    },
    [],
  );

  const runRowOperation = useCallback(
    async (
      item: AdminICloudResourceItem,
      action: RowAction,
      operation: () => Promise<AdminICloudMutationResponse>,
      successKey: string,
    ) => {
      setRowBusy({ action, id: item.id });
      try {
        const result = await operation();
        applyMutation(item, action, result);
        Toast.success(t(successKey));
        await refresh();
      } catch (error) {
        Toast.error(getIamErrorMessage(t, error, "iCloud resource operation failed."));
        if (detailIdRef.current === item.id) await loadDetail(item.id, true);
      } finally {
        setRowBusy(null);
      }
    },
    [applyMutation, loadDetail, refresh, t],
  );

  const toggleDisabled = useCallback(
    (item: AdminICloudResourceItem) =>
      runRowOperation(
        item,
        "toggle",
        () =>
          item.status === "disabled"
            ? enableAdminICloudResource(item.id, item.version)
            : disableAdminICloudResource(item.id, item.version),
        item.status === "disabled"
          ? "iCloud resource enabled and queued for validation."
          : "iCloud resource disabled.",
      ),
    [runRowOperation],
  );

  const togglePublish = useCallback(
    (item: AdminICloudResourceItem) =>
      runRowOperation(
        item,
        "publish",
        () =>
          item.forSale
            ? unpublishAdminICloudResource(item.id, item.version)
            : publishAdminICloudResource(item.id, item.version),
        item.forSale
          ? "iCloud resource converted to private."
          : "iCloud resource published for public sale.",
      ),
    [runRowOperation],
  );

  const recoverResource = useCallback(
    (item: AdminICloudResourceItem) =>
      runRowOperation(
        item,
        "recover",
        () => recoverAdminICloudResource(item.id, item.version),
        "iCloud resource recovered and queued for validation.",
      ),
    [runRowOperation],
  );

  const confirmDelete = useCallback(
    (item: AdminICloudResourceItem) => {
      Modal.confirm({
        cancelText: t("Cancel"),
        content: t("Confirm delete iCloud resource content", {
          email: item.primaryEmail,
        }),
        okButtonProps: { type: "danger" },
        okText: t("Delete"),
        onOk: () =>
          runRowOperation(
            item,
            "delete",
            () => deleteAdminICloudResource(item.id, item.version),
            "iCloud resource deleted.",
          ),
        title: t("Confirm delete"),
      });
    },
    [runRowOperation, t],
  );

  const resetFilters = () => {
    setSearchKeyword("");
    flushSearchKeyword("");
    setCreatedAtRange([]);
    setStatusFilter("all");
    setPrivateFilter("all");
    setActivePage(1);
    setSelectedKeys([]);
  };

  const totalPages = Math.max(1, Math.ceil(total / pageSize));
  const safePage = Math.min(activePage, totalPages);
  useEffect(() => {
    if (safePage !== activePage) setActivePage(safePage);
  }, [activePage, safePage]);
  const activeFilterCount =
    Number(statusFilter !== "all") +
    Number(privateFilter !== "all");

  const showBulkOutcome = useCallback(
    (response: AdminICloudBulkResponse, successKey: string) => {
      const outcome = bulkOutcome(response);
      Toast.success(t(successKey, { count: outcome.succeeded }));
      if (outcome.skipped === 0) return;
      const reasons = outcome.reasonCounts
        .map((item) => `${item.reason}: ${item.count}`)
        .join(", ");
      Toast.warning(
        `${t("Succeeded")}: ${outcome.succeeded}/${outcome.succeeded + outcome.skipped}` +
          (reasons ? ` · ${t("Reason")}: ${reasons}` : ""),
      );
    },
    [t],
  );

  const confirmExpiration = useCallback(
    (allMatching: boolean, resourceId?: number) => {
      const rowOperation = resourceId !== undefined;
      const count = rowOperation ? 1 : allMatching ? total : selectedKeys.length;
      if (count === 0) {
        Toast.info(t("No resources to check."));
        return;
      }
      if (allMatching && count > ADMIN_ICLOUD_BATCH_MAX) {
        Toast.warning(
          t("iCloud bulk selection limit exceeded.", {
            limit: ADMIN_ICLOUD_BATCH_MAX,
          }),
        );
        return;
      }
      let expireAt: Date | null = defaultICloudExpireAt();
      Modal.confirm({
        cancelText: t("Cancel"),
        content: (
          <div className="space-y-3">
            <div className="text-sm text-[var(--semi-color-text-2)]">
              {t("Set iCloud resource expiration hint", { count })}
            </div>
            <label className="block">
              <span className="mb-1.5 block text-sm font-medium text-[var(--semi-color-text-0)]">
                {t("Resource expires at")} *
              </span>
              <DatePicker
                aria-label={t("Resource expires at")}
                defaultValue={expireAt}
                format="yyyy-MM-dd HH:mm:ss"
                onChange={(value) => {
                  expireAt = value instanceof Date ? value : null;
                }}
                showClear={false}
                style={{ width: "100%" }}
                type="dateTime"
              />
            </label>
          </div>
        ),
        okText: t("Save"),
        onOk: async () => {
          if (!expireAt || !Number.isFinite(expireAt.getTime()) || expireAt.getTime() <= Date.now()) {
            Toast.warning(t("Expiration must be in the future."));
            throw new Error("expiration must be in the future");
          }
          if (resourceId !== undefined) {
            setRowBusy({ action: "expiration", id: resourceId });
          } else {
            setBulkBusy("expiration");
          }
          try {
            const response = resourceId !== undefined
              ? await setAdminICloudResourcesExpirationByIds(
                  [resourceId],
                  expireAt.toISOString(),
                )
              : allMatching
                ? await setAdminICloudResourcesExpirationByFilter(
                    filter,
                    expireAt.toISOString(),
                  )
                : await setAdminICloudResourcesExpirationByIds(
                    selectedKeys,
                    expireAt.toISOString(),
                  );
            showBulkOutcome(response, "iCloud resource expiration updated.");
            if (!rowOperation) setSelectedKeys([]);
            await refresh();
          } catch (error) {
            Toast.error(getIamErrorMessage(t, error, "iCloud resource operation failed."));
            throw error;
          } finally {
            if (rowOperation) {
              setRowBusy(null);
            } else {
              setBulkBusy(null);
            }
          }
        },
        title: t("Set expiration"),
      });
    },
    [filter, refresh, selectedKeys, showBulkOutcome, t, total],
  );

  const runBatch = useCallback(
    async (
      action: Exclude<AdminICloudBatchAction, "validate" | "alias">,
      allMatching: boolean,
    ) => {
      const count = allMatching ? total : selectedKeys.length;
      if (count === 0) {
        Toast.info(t("No resources to check."));
        return;
      }
      if (allMatching && count > ADMIN_ICLOUD_BATCH_MAX) {
        Toast.warning(
          t("iCloud bulk selection limit exceeded.", {
            limit: ADMIN_ICLOUD_BATCH_MAX,
          }),
        );
        return;
      }
      setBulkBusy(action);
      try {
        const response = allMatching
          ? await batchAdminICloudResourcesByFilter(action, filter)
          : await batchAdminICloudResourcesByIds(action, selectedKeys);
        const successKey = {
          disable: "iCloud resources disabled.",
          publish: "iCloud resources published for public sale.",
          unpublish: "iCloud resources converted to private.",
          delete: "iCloud resources deleted.",
        }[action];
        showBulkOutcome(response, successKey);
        setSelectedKeys([]);
        if (action === "delete") setActivePage(1);
        await refresh();
      } catch (error) {
        Toast.error(getIamErrorMessage(t, error, "iCloud resource operation failed."));
      } finally {
        setBulkBusy(null);
      }
    },
    [filter, refresh, selectedKeys, showBulkOutcome, t, total],
  );

  const confirmBatch = useCallback(
    (action: Exclude<AdminICloudBatchAction, "validate" | "alias">, allMatching: boolean) => {
      const count = allMatching ? total : selectedKeys.length;
      if (count === 0) {
        Toast.info(t("No resources to check."));
        return;
      }
      if (allMatching && count > ADMIN_ICLOUD_BATCH_MAX) {
        Toast.warning(
          t("iCloud bulk selection limit exceeded.", {
            limit: ADMIN_ICLOUD_BATCH_MAX,
          }),
        );
        return;
      }
      const contentKey = allMatching
        ? {
            disable: "Confirm disable all matching iCloud resources",
            publish: "Confirm put all matching iCloud resources on sale",
            unpublish: "Confirm convert all matching iCloud resources to private",
            delete: "Confirm delete all matching iCloud resources",
          }[action]
        : {
            disable: "Confirm disable selected iCloud resources",
            publish: "Confirm put selected iCloud resources on sale",
            unpublish: "Confirm convert selected iCloud resources to private",
            delete: "Confirm delete selected iCloud resources",
          }[action];
      Modal.confirm({
        cancelText: t("Cancel"),
        content: t(contentKey, { count }),
        okButtonProps: action === "delete" ? { type: "danger" } : undefined,
        okText:
          action === "publish"
            ? t("Put on sale")
            : action === "unpublish"
              ? t("Convert to private")
              : t(action === "delete" ? "Delete" : "Disable"),
        onOk: () => runBatch(action, allMatching),
        title: t(action === "delete" ? "Confirm delete" : "Confirm operation"),
      });
    },
    [runBatch, selectedKeys.length, t, total],
  );

  const openBulkMaintenance = useCallback(
    (allMatching: boolean) => {
      const count = allMatching ? total : selectedKeys.length;
      if (count === 0) {
        Toast.info(t("No resources to check."));
        return;
      }
      if (allMatching && count > ADMIN_ICLOUD_BATCH_MAX) {
        Toast.warning(
          t("iCloud bulk selection limit exceeded.", {
            limit: ADMIN_ICLOUD_BATCH_MAX,
          }),
        );
        return;
      }
      setMaintenanceTarget(
        allMatching
          ? { count, filter, mode: "filter" }
          : { count, mode: "ids", resourceIds: [...selectedKeys] },
      );
    },
    [filter, selectedKeys, t, total],
  );

  useSelectionNotification({
    checkLabelKey: "Maintenance",
    deleteLoading: bulkBusy === "delete",
    extraActions: [
      {
        key: "publish",
        labelKey: "Put on sale",
        loading: bulkBusy === "publish",
        onClick: () => confirmBatch("publish", false),
        type: "secondary",
      },
      {
        key: "private",
        labelKey: "Convert to private",
        loading: bulkBusy === "unpublish",
        onClick: () => confirmBatch("unpublish", false),
        type: "tertiary",
      },
      {
        key: "expiration",
        labelKey: "Set expiration",
        loading: bulkBusy === "expiration",
        onClick: () => confirmExpiration(false),
        type: "tertiary",
      },
    ],
    onCheck: () => openBulkMaintenance(false),
    onClear: () => setSelectedKeys([]),
    onDelete: () => confirmBatch("delete", false),
    onSell: () => confirmBatch("disable", false),
    selectedCount: canOperate ? selectedKeys.length : 0,
    selectionDescriptionKey: "Selected iCloud resources",
    sellLabelKey: "Disable",
    sellLoading: bulkBusy === "disable",
    t,
  });

  const renderRowActions = useCallback(
    (item: AdminICloudResourceItem) => {
      const busyAction = rowBusy?.id === item.id ? rowBusy.action : null;
      if (item.status === "deleted") {
        return (
          <Space spacing={4} wrap={isMobile}>
            <Button
              disabled={Boolean(busyAction)}
              onClick={() => openDetail(item.id)}
              size="small"
              type="tertiary"
            >
              {t("Details")}
            </Button>
            {canOperate ? (
              <Button
                disabled={Boolean(rowBusy && busyAction !== "recover")}
                loading={busyAction === "recover"}
                onClick={() => void recoverResource(item)}
                size="small"
                type="primary"
              >
                {t("Recover")}
              </Button>
            ) : null}
          </Space>
        );
      }

      return (
        <Space spacing={4} wrap={isMobile}>
          <Button
            disabled={Boolean(busyAction)}
            onClick={() => openDetail(item.id)}
            size="small"
            type="tertiary"
          >
            {t("Details")}
          </Button>
          {canWrite ? (
            <Button
              disabled={Boolean(busyAction)}
              onClick={() => setEditTarget(item)}
              size="small"
              type="tertiary"
            >
              {t("Edit")}
            </Button>
          ) : null}
          {canOperate ? (
            <>
              <Button
                disabled={Boolean(busyAction)}
                onClick={() => setMaintenanceTarget({ item, mode: "row" })}
                size="small"
                type="tertiary"
              >
                {t("Maintenance")}
              </Button>
              <Button
                disabled={Boolean(rowBusy && busyAction !== "toggle")}
                loading={busyAction === "toggle"}
                onClick={() => void toggleDisabled(item)}
                size="small"
                type="tertiary"
              >
                {item.status === "disabled" ? t("Enable") : t("Disable")}
              </Button>
              <Button
                disabled={Boolean(rowBusy && busyAction !== "publish")}
                loading={busyAction === "publish"}
                onClick={() => void togglePublish(item)}
                size="small"
                type="tertiary"
              >
                {item.forSale ? t("Convert to private") : t("Put on sale")}
              </Button>
              <Button
                disabled={Boolean(rowBusy && busyAction !== "delete")}
                loading={busyAction === "delete"}
                onClick={() => confirmDelete(item)}
                size="small"
                type="danger"
              >
                {t("Delete")}
              </Button>
            </>
          ) : null}
        </Space>
      );
    },
    [
      canOperate,
      canWrite,
      confirmDelete,
      isMobile,
      openDetail,
      recoverResource,
      rowBusy,
      t,
      toggleDisabled,
      togglePublish,
    ],
  );

  const columns = useMemo(
    () => [
      {
        dataIndex: "primaryEmail",
        key: "email",
        title: t("Primary email"),
        width: 280,
        render: (value: unknown) => (
          <CopyableTableText copiedText={t("Copied")} text={String(value)} />
        ),
      },
      {
        dataIndex: "region",
        key: "region",
        title: t("Region"),
        width: 130,
        render: (_: unknown, item: AdminICloudResourceItem) =>
          [item.region, item.countryCode].filter(Boolean).join(" · ") || "-",
      },
      {
        dataIndex: "selectedForwardTo",
        key: "selectedForwardTo",
        title: t("Forwarding mailbox"),
        width: 260,
        render: (value: unknown) =>
          value ? (
            <CopyableTableText copiedText={t("Copied")} text={String(value)} />
          ) : (
            "-"
          ),
      },
      {
        dataIndex: "owner",
        key: "owner",
        title: t("Owner"),
        width: 310,
        render: (_: unknown, item: AdminICloudResourceItem) => (
          <OwnerIdentity owner={item.owner} t={t} />
        ),
      },
      {
        dataIndex: "status",
        key: "status",
        title: t("Status"),
        width: 120,
        render: (_: unknown, item: AdminICloudResourceItem) => (
          <ResourceStatusTag item={item} />
        ),
      },
      {
        dataIndex: "forSale",
        key: "private",
        title: t("Private"),
        width: 100,
        render: (value: unknown) => (
          <Tag color={!value ? "green" : "grey"} shape="circle">
            {!value ? t("Yes") : t("No")}
          </Tag>
        ),
      },
      {
        dataIndex: "newSession",
        key: "newSession",
        title: t("New Cookie"),
        width: 130,
        render: (_: unknown, item: AdminICloudResourceItem) => (
          <SessionStatusTag session={item.newSession} />
        ),
      },
      {
        dataIndex: "oldSession",
        key: "oldSession",
        title: t("Old Cookie"),
        width: 130,
        render: (_: unknown, item: AdminICloudResourceItem) => (
          <SessionStatusTag session={item.oldSession} />
        ),
      },
      {
        dataIndex: "aliasCount",
        key: "aliases",
        title: t("Alias count"),
        width: 120,
        render: (value: unknown) => (
          <AliasCountTag count={Number(value)} limit={aliasLimit} />
        ),
      },
      {
        dataIndex: "expireAt",
        key: "expireAt",
        title: t("Resource expires at"),
        width: 180,
        render: (value: unknown) => {
          const text = String(value || "");
          const expired = new Date(text).getTime() <= Date.now();
          return (
            <span
              className={`whitespace-nowrap text-sm font-medium tabular-nums ${
                expired
                  ? "text-[var(--semi-color-danger)]"
                  : "text-[var(--semi-color-text-2)]"
              }`}
            >
              {formatTime(text)}
            </span>
          );
        },
      },
      {
        dataIndex: "operate",
        fixed: "right" as const,
        key: "operate",
        title: t("Action"),
        width: 360,
        render: (_: unknown, item: AdminICloudResourceItem) => renderRowActions(item),
      },
    ],
    [aliasLimit, renderRowActions, t],
  );

  const tableColumns = compactMode
    ? columns.map((column) => {
        if (column.dataIndex !== "operate") return column;
        const { fixed: _fixed, ...rest } = column;
        return rest;
      })
    : columns;

  const rowSelection = {
    selectedRowKeys: selectedKeys,
    onChange: (keys: Array<string | number>) => {
      setSelectedKeys(keys.map((key) => Number(key)));
    },
  };

  const actionsArea = (
    <div className="flex w-full flex-col items-center justify-between gap-2 md:flex-row">
      <div className="order-2 flex w-full flex-wrap gap-2 md:order-1 md:w-auto">
        <Button
          className="flex-1 md:flex-initial"
          disabled={!canAutomaticImport && !canLegacyImport}
          icon={<Upload size={14} />}
          onClick={() => {
            setImportFlowMode(canAutomaticImport ? "automatic" : "legacy");
            setImportOpen(true);
          }}
          size="small"
          type="primary"
        >
          {t("Import")}
        </Button>
        <Button
          className="remail-toolbar-fixed-button flex-1 md:flex-none"
          loading={loading}
          onClick={() => void refresh()}
          size="small"
          type="tertiary"
        >
          {t("Refresh")}
        </Button>
        {canOperate ? (
          <>
            <Tooltip
              content={t("Maintain all")}
              mouseEnterDelay={0}
              mouseLeaveDelay={0.05}
              position="top"
            >
              <Button
                className="flex-1 md:flex-initial"
                onClick={() => openBulkMaintenance(true)}
                size="small"
                type="tertiary"
              >
                {t("Maintenance")}
              </Button>
            </Tooltip>
            <Tooltip
              content={t("Put all matching iCloud resources on sale")}
              mouseEnterDelay={0}
              mouseLeaveDelay={0.05}
              position="top"
            >
              <Button
                className="flex-1 md:flex-initial"
                loading={bulkBusy === "publish"}
                onClick={() => confirmBatch("publish", true)}
                size="small"
                type="tertiary"
              >
                {t("Put on sale")}
              </Button>
            </Tooltip>
            <Tooltip
              content={t("Convert all matching iCloud resources to private")}
              mouseEnterDelay={0}
              mouseLeaveDelay={0.05}
              position="top"
            >
              <Button
                className="flex-1 md:flex-initial"
                loading={bulkBusy === "unpublish"}
                onClick={() => confirmBatch("unpublish", true)}
                size="small"
                type="tertiary"
              >
                {t("Convert to private")}
              </Button>
            </Tooltip>
            <Tooltip
              content={t("Delete all matching iCloud resources")}
              mouseEnterDelay={0}
              mouseLeaveDelay={0.05}
              position="top"
            >
              <Button
                className="flex-1 md:flex-initial"
                loading={bulkBusy === "delete"}
                onClick={() => confirmBatch("delete", true)}
                size="small"
                type="danger"
              >
                {t("Delete")}
              </Button>
            </Tooltip>
            <Tooltip
              content={t("Set expiration for all matching iCloud resources")}
              mouseEnterDelay={0}
              mouseLeaveDelay={0.05}
              position="top"
            >
              <Button
                className="flex-1 md:flex-initial"
                loading={bulkBusy === "expiration"}
                onClick={() => confirmExpiration(true)}
                size="small"
                type="tertiary"
              >
                {t("Set expiration")}
              </Button>
            </Tooltip>
          </>
        ) : null}
        <CompactModeToggle
          compactMode={compactMode}
          setCompactMode={setCompactMode}
          t={t}
        />
      </div>

      <div className="order-1 flex w-full flex-col items-center gap-2 md:order-2 md:w-auto md:flex-row">
        <Dropdown
          position="bottomRight"
          render={
            <div className="max-h-[70vh] w-[280px] overflow-auto p-2">
              <div className="px-2 pb-1 text-xs font-medium text-[var(--semi-color-text-2)]">
                {t("Status")}
              </div>
              <div className="mb-2 space-y-1">
                {(["all", ...Object.keys(statusMeta)] as StatusFilter[]).map(
                  (value) => (
                    <StatisticFilterOption
                      active={statusFilter === value}
                      count={facets.status[value]}
                      key={value}
                      label={t(value === "all" ? "All" : statusMeta[value].label)}
                      onSelect={(next) => {
                        setStatusFilter(next);
                        setActivePage(1);
                      }}
                      value={value}
                    />
                  ),
                )}
              </div>

              <div className="px-2 pb-1 text-xs font-medium text-[var(--semi-color-text-2)]">
                {t("Private")}
              </div>
              <div className="mb-2 space-y-1">
                {(["all", "yes", "no"] as BooleanFilter[]).map((value) => (
                  <StatisticFilterOption
                    active={privateFilter === value}
                    count={
                      value === "all"
                        ? facets.forSale.all
                        : value === "yes"
                          ? facets.forSale.no
                          : facets.forSale.yes
                    }
                    key={value}
                    label={t(value === "all" ? "All" : value === "yes" ? "Yes" : "No")}
                    onSelect={(next) => {
                      setPrivateFilter(next);
                      setActivePage(1);
                    }}
                    value={value}
                  />
                ))}
              </div>

            </div>
          }
          trigger="click"
        >
          <Button
            className="flex-1 md:flex-initial"
            icon={<SlidersHorizontal size={14} />}
            size="small"
            type="tertiary"
          >
            {activeFilterCount > 0
              ? `${t("Filters")} (${activeFilterCount})`
              : t("Filters")}
          </Button>
        </Dropdown>

        <Input
          className="resources-search-input w-full md:w-56"
          onChange={(value) => {
            setSearchKeyword(String(value));
            setActivePage(1);
          }}
          onEnterPress={() => {
            flushSearchKeyword();
            setActivePage(1);
          }}
          placeholder={t("Search iCloud email, owner or alias")}
          prefix={<IconSearch />}
          showClear
          size="small"
          style={{ width: isMobile ? "100%" : 224 }}
          value={searchKeyword}
        />

        <DatePicker
          dropdownClassName={DATE_RANGE_DROPDOWN_CLASS}
          format="yyyy-MM-dd HH:mm:ss"
          onChange={(value) => {
            setCreatedAtRange(normalizeDateRangeValue(value));
            setActivePage(1);
          }}
          placeholder={[t("Start time"), t("End time")]}
          presetPosition="bottom"
          presets={dateRangePresets}
          showClear
          size="small"
          style={{ width: isMobile ? "100%" : 380 }}
          type="dateTimeRange"
          value={createdAtRange}
        />

        <div className="flex w-full gap-2 md:w-auto">
          <Button
            className="remail-toolbar-fixed-button flex-1 md:flex-none"
            loading={loading}
            onClick={() => {
              flushSearchKeyword();
              setActivePage(1);
            }}
            size="small"
            type="tertiary"
          >
            {t("Query")}
          </Button>
          <Button
            className="flex-1 md:flex-initial"
            onClick={resetFilters}
            size="small"
            type="tertiary"
          >
            {t("Reset")}
          </Button>
        </div>
      </div>
    </div>
  );

  const paginationArea = createCardProPagination({
    currentPage: safePage,
    isMobile,
    onPageChange: (page) => {
      setActivePage(page);
      setSelectedKeys([]);
    },
    onPageSizeChange: (size) => {
      setPageSize(size);
      setActivePage(1);
      setSelectedKeys([]);
    },
    pageSize,
    total,
    t,
  });

  return (
    <div className="console-content-width py-5">
      <CardPro
        actionsArea={actionsArea}
        paginationArea={paginationArea}
        t={t}
        type="type3"
      >
        <CardTable
          className="overflow-hidden rounded-xl"
          columns={tableColumns}
          dataSource={items}
          empty={
            <Empty
              darkModeImage={
                <IllustrationNoResultDark style={{ height: 150, width: 150 }} />
              }
              description={t("No iCloud resources found")}
              image={<IllustrationNoResult style={{ height: 150, width: 150 }} />}
              style={{ padding: 30 }}
            />
          }
          hidePagination
          loading={loading}
          pagination={false}
          rowKey="id"
          rowSelection={canOperate ? rowSelection : undefined}
          scroll={{ x: "max(100%, 2240px)", y: DESKTOP_TABLE_SCROLL_Y }}
          size="middle"
        />
      </CardPro>

      <ImportICloudModal
        modeSelector={
          <ICloudImportFlowSelector
            canAutomatic={canAutomaticImport}
            canLegacy={canLegacyImport}
            mode={importFlowMode}
            onChange={setImportFlowMode}
          />
        }
        onCancel={() => setImportOpen(false)}
        onImported={async () => {
          setActivePage(1);
          setSelectedKeys([]);
          await refresh();
        }}
        owners={importOwners}
        visible={importOpen && importFlowMode === "legacy" && canLegacyImport}
      />

      <ICloudOnboardingModal
        canOperate={canOperate}
        canReadTasks={canReadTasks}
        modeSelector={
          <ICloudImportFlowSelector
            canAutomatic={canAutomaticImport}
            canLegacy={canLegacyImport}
            mode={importFlowMode}
            onChange={setImportFlowMode}
          />
        }
        onCancel={() => setImportOpen(false)}
        onChanged={async () => {
          setActivePage(1);
          setSelectedKeys([]);
          await refresh();
        }}
        owners={importOwners}
        visible={importOpen && importFlowMode === "automatic" && canAutomaticImport}
      />

      <EditICloudModal
        canOperate={canOperate}
        onCancel={() => setEditTarget(null)}
        onSaved={async () => {
          await refresh();
        }}
        owners={owners}
        target={editTarget}
      />

      <EditICloudModal
        canOperate
        credentialsOnly
        onCancel={() => setCredentialTarget(null)}
        onSaved={async () => {
          await refresh();
        }}
        owners={owners}
        target={credentialTarget}
      />

      <ICloudMaintenanceModal
        aliasLimit={aliasLimit}
        onCancel={() => setMaintenanceTarget(null)}
        onCompleted={async () => {
          setSelectedKeys([]);
          await refresh();
        }}
        target={canOperate ? maintenanceTarget : null}
      />

      <ICloudDetailSheet
        aliasLimit={aliasLimit}
        busyAction={rowBusy?.id === detailId ? rowBusy?.action ?? null : null}
        canFetchMessages={canFetchMessages}
        canOperate={canOperate}
        canReadMessages={canReadMessages}
        canReadOrders={canReadOrders}
        canReadTasks={canReadTasks}
        canWrite={canWrite}
        item={detail}
        loading={detailLoading}
        resourceId={detailId}
        refreshGeneration={refreshGeneration}
        onCancel={closeDetail}
        onDelete={confirmDelete}
        onEdit={setEditTarget}
        onMaintain={(item) => setMaintenanceTarget({ item, mode: "row" })}
        onRefresh={refresh}
        onReplaceCredentials={setCredentialTarget}
        onRecover={recoverResource}
        onSetExpiration={(item) => confirmExpiration(false, item.id)}
        onToggleDisabled={toggleDisabled}
        onTogglePublish={togglePublish}
      />
    </div>
  );
}
