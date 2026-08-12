import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  Button,
  DatePicker,
  Dropdown,
  Empty,
  Input,
  Modal,
  Select,
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
import { AtSign, Code2, FileText, Layers, ShieldCheck, SlidersHorizontal, Upload } from "lucide-react";
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
  hasPermissionKey,
  permissionKey,
  useAuth,
} from "@/context/auth-provider";
import {
  SHARED_SEARCH_DEBOUNCE_MS,
  useDebouncedValue,
} from "@/hooks/use-debounced-value";
import { useIsMobile } from "@/hooks/use-is-mobile";
import { useSharedPageSize } from "@/hooks/use-shared-page-size";
import {
  batchAdminICloudResourcesByFilter,
  batchAdminICloudResourcesByIds,
  createAdminICloudAliases,
  deleteAdminICloudResource,
  disableAdminICloudResource,
  enableAdminICloudResource,
  getAdminICloudResourceDetail,
  importAdminICloudResources,
  listAdminICloudAliases,
  listAdminICloudOwners,
  listAdminICloudResources,
  listAdminICloudTasks,
  publishAdminICloudResource,
  recoverAdminICloudResource,
  setAdminICloudResourcesExpirationByFilter,
  setAdminICloudResourcesExpirationByIds,
  unpublishAdminICloudResource,
  updateAdminICloudResource,
  validateAdminICloudResource,
  type AdminICloudAliasItem,
  type AdminICloudBatchAction,
  type AdminICloudBulkResponse,
  type AdminICloudImportErrorStrategy,
  type AdminICloudMutationResponse,
  type AdminICloudOwner,
  type AdminICloudResourceDetail,
  type AdminICloudResourceItem,
  type AdminICloudResourceFacets,
  type AdminICloudResourceListFilter,
  type AdminICloudResourceStatus,
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
type SessionFilter = "all" | AdminICloudSessionStatus;
type BooleanFilter = "all" | "yes" | "no";
type RowAction =
  | "toggle"
  | "publish"
  | "delete"
  | "recover"
  | "expiration";
type ImportMode = "paste" | "curl" | "file";
type ICloudMaintenanceAction = "validate" | "alias";
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
  sessionStatus: { all: 0, unchecked: 0, valid: 0, invalid: 0 },
  suffixes: [],
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

function SessionStatusTag({ status }: { status: AdminICloudSessionStatus }) {
  const { t } = useTranslation();
  const meta = sessionMeta[status];
  return (
    <Tag color={meta.color} shape="circle" size="small">
      {t(meta.label)}
    </Tag>
  );
}

function AliasCountTag({ count, limit }: { count: number; limit: number }) {
  const color = count === limit ? "green" : count > limit ? "red" : "orange";
  return (
    <Tag color={color} shape="circle" size="small">
      {count}/{limit}
    </Tag>
  );
}

function OwnerSelect({
  onChange,
  owners,
  t,
  value,
}: {
  onChange: (ownerId: number) => void;
  owners: AdminICloudOwner[];
  t: ReturnType<typeof useTranslation>["t"];
  value?: number;
}) {
  const [options, setOptions] = useState(owners);
  const [loading, setLoading] = useState(false);
  const requestSequence = useRef(0);
  const searchDebounce = useRef<ReturnType<typeof globalThis.setTimeout> | null>(
    null,
  );

  useEffect(() => setOptions(owners), [owners]);
  useEffect(
    () => () => {
      if (searchDebounce.current) globalThis.clearTimeout(searchDebounce.current);
    },
    [],
  );

  const searchOwners = async (keyword: string) => {
    const sequence = ++requestSequence.current;
    setLoading(true);
    try {
      const result = await listAdminICloudOwners(keyword);
      if (requestSequence.current === sequence) {
        const selected = owners.find((owner) => owner.id === value);
        setOptions(
          selected && !result.some((owner) => owner.id === selected.id)
            ? [selected, ...result]
            : result,
        );
      }
    } catch {
      // Keep the previous bounded result; the next search retries IAM.
    } finally {
      if (requestSequence.current === sequence) setLoading(false);
    }
  };

  const queueOwnerSearch = (keyword: string) => {
    if (searchDebounce.current) globalThis.clearTimeout(searchDebounce.current);
    searchDebounce.current = globalThis.setTimeout(() => {
      void searchOwners(keyword);
    }, SHARED_SEARCH_DEBOUNCE_MS);
  };

  return (
    <Select
      emptyContent={t("No users found")}
      filter
      loading={loading}
      onChange={(next) => onChange(Number(next))}
      onDropdownVisibleChange={(visible) => {
        if (visible && options.length === 0) void searchOwners("");
      }}
      onSearch={queueOwnerSearch}
      optionList={options.map((owner) => ({
        disabled: !owner.enabled,
        label: `${owner.email} · ${owner.nickname} · ${t(ownerRoleLabel(owner.role))} · ${owner.groupName}`,
        value: owner.id,
      }))}
      placeholder={t("Search user by email, nickname or ID")}
      remote
      searchPosition="dropdown"
      style={{ width: "100%" }}
      value={value}
    />
  );
}

export function ImportICloudModal({
  onCancel,
  onImported,
  owners,
  visible,
}: {
  onCancel: () => void;
  onImported: () => void | Promise<void>;
  owners: AdminICloudOwner[];
  visible: boolean;
}) {
  const { t } = useTranslation();
  const [mode, setMode] = useState<ImportMode>("paste");
  const [content, setContent] = useState("");
  const [curlPrimaryEmail, setCurlPrimaryEmail] = useState("");
  const [file, setFile] = useState<File | null>(null);
  const [ownerId, setOwnerId] = useState<number | undefined>();
  const [errorStrategy, setErrorStrategy] =
    useState<AdminICloudImportErrorStrategy>("skip");
  const [expireAt, setExpireAt] = useState<Date | null>(() =>
    defaultICloudExpireAt(),
  );
  const [submitting, setSubmitting] = useState(false);
  const fileRef = useRef<HTMLInputElement>(null);
  const previousVisible = useRef(false);
  const lineCount = useMemo(() => {
    if (mode === "curl") return content.trim() ? 1 : 0;
    return content.split(/\r?\n/).filter((line) => line.trim()).length;
  }, [content, mode]);

  useEffect(() => {
    const opened = visible && !previousVisible.current;
    previousVisible.current = visible;
    if (!opened) return;
    setMode("paste");
    setContent("");
    setCurlPrimaryEmail("");
    setFile(null);
    if (fileRef.current) fileRef.current.value = "";
    setOwnerId(undefined);
    setErrorStrategy("skip");
    setExpireAt(defaultICloudExpireAt());
  }, [visible]);

  useEffect(() => {
    if (!visible || ownerId !== undefined) return;
    setOwnerId(owners.find((owner) => owner.enabled)?.id ?? owners[0]?.id);
  }, [ownerId, owners, visible]);

  const submit = async () => {
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
    if (mode === "curl") {
      const primaryEmail = curlPrimaryEmail.trim().toLowerCase();
      if (!/^[^\s@]+@[^\s@]+$/.test(primaryEmail)) {
        Toast.warning(t("A valid iCloud email address is required."));
        return;
      }
      if (!/^curl(?:\s|\\|$)/i.test(content.trim())) {
        Toast.warning(t("Please paste an iCloud HME cURL request."));
        return;
      }
      sourceContent = `${primaryEmail}----${content.trim()}`;
    }
    const sourceLineCount = sourceContent
      .split(/\r?\n/)
      .filter((line) => line.trim()).length;
    if (!sourceLineCount) {
      Toast.warning(t("Please enter iCloud resources."));
      return;
    }
    setSubmitting(true);
    try {
      const result = await importAdminICloudResources({
        content: sourceContent,
        errorStrategy,
        expireAt: expireAt.toISOString(),
        ownerId,
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
      confirmLoading={submitting}
      onCancel={onCancel}
      onOk={() => void submit()}
      okText={t("Import")}
      title={t("Import iCloud Emails")}
      visible={visible}
      width="min(666px, calc(100vw - 32px))"
    >
      <div className="space-y-4 py-1">
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

        <div className="grid grid-cols-3 gap-2">
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
            aria-pressed={mode === "curl"}
            className={switchButtonClass(mode === "curl")}
            onClick={() => {
              setMode("curl");
              setContent("");
              setFile(null);
              if (fileRef.current) fileRef.current.value = "";
            }}
            type="button"
          >
            <Code2 size={16} />
            cURL
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
                placeholder="primary@icloud.com----host----dsid----clientId----clientBuildNumber----clientMasteringNumber----Cookie"
                rows={8}
                style={{ height: IMPORT_ENTRY_AREA_HEIGHT, resize: "none" }}
                value={content}
              />
            </label>
          ) : mode === "curl" ? (
            <div className="space-y-3">
              <label className="block">
                <span className="mb-1.5 block text-sm font-medium text-[var(--semi-color-text-0)]">
                  {t("Primary email")} *
                </span>
                <Input
                  className="font-mono"
                  onChange={(value) => setCurlPrimaryEmail(String(value))}
                  placeholder="name@icloud.com"
                  value={curlPrimaryEmail}
                />
              </label>
              <label className="block">
                <span className="mb-1.5 flex items-center justify-between text-sm font-medium text-[var(--semi-color-text-0)]">
                  <span>{t("HME cURL request")} *</span>
                  <Text size="small" type="tertiary">
                    {t("Parsed entries", { count: lineCount })}
                  </Text>
                </span>
                <TextArea
                  className="font-mono"
                  onChange={setContent}
                  placeholder="curl --url 'https://p217-maildomainws.icloud.com.cn/v2/hme/list?...' ..."
                  rows={8}
                  style={{ height: IMPORT_ENTRY_AREA_HEIGHT - 68, resize: "none" }}
                  value={content}
                />
              </label>
              <div className="text-xs leading-5 text-[var(--semi-color-text-2)]">
                {t("The HME cURL does not contain the primary email. Enter it separately.")}
              </div>
            </div>
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
          <code className="block break-all whitespace-normal font-mono text-xs leading-relaxed text-[var(--semi-color-text-2)]">
            {mode === "curl"
              ? "primaryEmail----curl --url 'https://.../v2/hme/list?...' ..."
              : "primaryEmail----host----dsid----clientId----clientBuildNumber----clientMasteringNumber----Cookie"}
          </code>
        </div>

        <div className="rounded-lg border border-[var(--semi-color-border)] bg-[var(--semi-color-fill-0)] px-3 py-2 text-xs leading-5 text-[var(--semi-color-text-2)]">
          {t(
            "iCloud Cookie and HME request context are write-only and never returned by the resource API.",
          )}
        </div>
      </div>
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
  const [host, setHost] = useState("");
  const [dsid, setDsid] = useState("");
  const [clientId, setClientId] = useState("");
  const [clientBuildNumber, setClientBuildNumber] = useState("");
  const [clientMasteringNumber, setClientMasteringNumber] = useState("");
  const [cookie, setCookie] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const credentialPlaceholder = t(credentialsOnly ? "Required" : "Leave blank to keep current");

  useEffect(() => {
    if (!target) return;
    setPrimaryEmail(target.primaryEmail);
    setOwnerId(target.owner.id);
    setForSale(target.forSale);
    setExpireAt(new Date(target.expireAt));
    setHost("");
    setDsid("");
    setClientId("");
    setClientBuildNumber("");
    setClientMasteringNumber("");
    setCookie("");
  }, [target]);

  const submit = async () => {
    if (!target || (!credentialsOnly && !ownerId)) return;
    const nextEmail = primaryEmail.trim().toLowerCase();
    if (!credentialsOnly && !nextEmail) {
      Toast.warning(t("A valid iCloud email address is required."));
      return;
    }
    const credentialValues = [
      host,
      dsid,
      clientId,
      clientBuildNumber,
      clientMasteringNumber,
      cookie,
    ].map((value) => value.trim());
    const wantsCredentialChange = credentialValues.some(Boolean);
    const emailChanged = !credentialsOnly && nextEmail !== target.primaryEmail;
    const expireAtChanged =
      !credentialsOnly &&
      canOperate &&
      expireAt?.getTime() !== new Date(target.expireAt).getTime();
    if (
      expireAtChanged &&
      (!expireAt || !Number.isFinite(expireAt.getTime()) || expireAt.getTime() <= Date.now())
    ) {
      Toast.warning(t("Expiration must be in the future."));
      return;
    }
    const requiresCredentials = credentialsOnly || emailChanged;
    if (requiresCredentials && (!canOperate || !credentialValues.every(Boolean))) {
      Toast.warning(t("Changing the primary email or replacing credentials requires every iCloud credential field."));
      return;
    }
    if (wantsCredentialChange && (!canOperate || !credentialValues.every(Boolean))) {
      Toast.warning(t("Complete every iCloud credential field or leave all of them blank."));
      return;
    }

    const request: AdminICloudUpdateRequest = { version: target.version };
    if (!credentialsOnly) {
      if (emailChanged) request.primaryEmail = nextEmail;
      if (ownerId !== target.owner.id) request.ownerId = ownerId;
      if (canOperate && forSale !== target.forSale) request.forSale = forSale;
      if (expireAtChanged && expireAt) request.expireAt = expireAt.toISOString();
    }
    if (requiresCredentials || wantsCredentialChange) {
      request.credentials = {
        host: credentialValues[0],
        dsid: credentialValues[1],
        clientId: credentialValues[2],
        clientBuildNumber: credentialValues[3],
        clientMasteringNumber: credentialValues[4],
        cookie: credentialValues[5],
      };
    }
    if (Object.keys(request).length === 1) {
      Toast.info(t("No changes to save."));
      return;
    }

    setSubmitting(true);
    try {
      await updateAdminICloudResource(target.id, request);
      Toast.success(t(credentialsOnly
        ? "Credentials replaced and validation queued."
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
            <>
              <div className="rounded-lg border border-[var(--semi-color-warning-light-active)] bg-[var(--semi-color-warning-light-default)] px-3 py-2 text-sm text-[var(--semi-color-text-0)]">
                {t("All credential fields are write-only. Existing values are never displayed, and submitting replaces the complete credential set.")}
              </div>
              <InfoItem
                label={t("Primary email")}
                value={<span className="font-mono">{target.primaryEmail}</span>}
              />
            </>
          ) : (
            <>
          <div className="grid gap-3 sm:grid-cols-2">
            <label className="block">
              <span className="mb-1.5 block text-sm font-medium text-[var(--semi-color-text-0)]">
                {t("Primary email")} *
              </span>
              <Input
                className="font-mono"
                disabled={!canOperate}
                onChange={(value) => setPrimaryEmail(String(value))}
                placeholder="name@icloud.com"
                value={primaryEmail}
              />
            </label>
            <div>
              <div className="mb-1.5 text-sm font-medium text-[var(--semi-color-text-0)]">
                {t("Current forwarding mailbox")}
              </div>
              <div className="flex h-8 items-center rounded-md bg-[var(--semi-color-fill-0)] px-3 font-mono text-sm text-[var(--semi-color-text-1)]">
                {target.selectedForwardTo || t("Not configured")}
              </div>
            </div>
          </div>

          <label className="block">
            <span className="mb-1.5 block text-sm font-medium text-[var(--semi-color-text-0)]">
              {t("Owner")}
            </span>
            <OwnerSelect onChange={setOwnerId} owners={owners} t={t} value={ownerId} />
          </label>

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

          <div className="text-xs leading-5 text-[var(--semi-color-text-2)]">
            {t("Changing the primary email requires the complete write-only credential set and re-queues validation.")}
          </div>

          {canOperate ? (
            <div className="flex items-center justify-between rounded-lg bg-[var(--semi-color-fill-0)] px-3 py-2.5">
              <div>
                <div className="text-sm font-medium text-[var(--semi-color-text-0)]">
                  {t("Public sale")}
                </div>
                <div className="text-xs text-[var(--semi-color-text-2)]">
                  {t("Credential replacement automatically converts the resource to private supply.")}
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
          )}

          {canOperate ? (
            <div className="rounded-lg border border-[var(--semi-color-border)] p-3">
              <div className="mb-1 text-sm font-medium text-[var(--semi-color-text-0)]">
                {t("Credentials")}
              </div>
              <div className="mb-3 text-xs leading-5 text-[var(--semi-color-text-2)]">
                {t(credentialsOnly
                  ? "All credential fields are required and replace the complete HME credential set."
                  : "Write-only. Leave every field blank to keep the current values; complete all fields to replace the HME credential set and re-queue validation.")}
              </div>
              <div className="grid gap-3 sm:grid-cols-2">
                <label className="block sm:col-span-2">
                  <span className="mb-1.5 block text-sm font-medium text-[var(--semi-color-text-0)]">
                    {t("Host")}
                  </span>
                  <Input
                    className="font-mono"
                    onChange={(value) => setHost(String(value))}
                    placeholder="p119-maildomainws.icloud.com"
                    value={host}
                  />
                </label>
                <label className="block">
                  <span className="mb-1.5 block text-sm font-medium text-[var(--semi-color-text-0)]">
                    {t("DSID")}
                  </span>
                  <Input
                    autoComplete="off"
                    mode="password"
                    onChange={(value) => setDsid(String(value))}
                    placeholder={credentialPlaceholder}
                    value={dsid}
                  />
                </label>
                <label className="block">
                  <span className="mb-1.5 block text-sm font-medium text-[var(--semi-color-text-0)]">
                    {t("Client ID")}
                  </span>
                  <Input
                    autoComplete="off"
                    mode="password"
                    onChange={(value) => setClientId(String(value))}
                    placeholder={credentialPlaceholder}
                    value={clientId}
                  />
                </label>
                <label className="block">
                  <span className="mb-1.5 block text-sm font-medium text-[var(--semi-color-text-0)]">
                    {t("Client build number")}
                  </span>
                  <Input
                    className="font-mono"
                    onChange={(value) => setClientBuildNumber(String(value))}
                    placeholder={credentialPlaceholder}
                    value={clientBuildNumber}
                  />
                </label>
                <label className="block">
                  <span className="mb-1.5 block text-sm font-medium text-[var(--semi-color-text-0)]">
                    {t("Client mastering number")}
                  </span>
                  <Input
                    className="font-mono"
                    onChange={(value) => setClientMasteringNumber(String(value))}
                    placeholder={credentialPlaceholder}
                    value={clientMasteringNumber}
                  />
                </label>
                <label className="block sm:col-span-2">
                  <span className="mb-1.5 block text-sm font-medium text-[var(--semi-color-text-0)]">
                    {t("Cookie")}
                  </span>
                  <TextArea
                    className="font-mono"
                    onChange={setCookie}
                    placeholder={credentialPlaceholder}
                    rows={4}
                    style={{ resize: "none" }}
                    value={cookie}
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
  const [selected, setSelected] = useState<ICloudMaintenanceAction>("validate");
  const [submitting, setSubmitting] = useState(false);

  useEffect(() => {
    if (target) setSelected("validate");
  }, [target]);

  if (!target) return null;

  const rowDisabled = target.mode === "row" &&
    (target.item.status === "disabled" || target.item.status === "deleted");
  const actions = [
    {
      description: "Run the complete iCloud health check, session refresh, alias sync and delivery verification.",
      disabled: rowDisabled,
      icon: ShieldCheck,
      key: "validate" as const,
      label: "Validate resource",
    },
    {
      description: "Queue the fenced iCloud maintenance flow to create and activate missing HME aliases.",
      disabled: rowDisabled || (target.mode === "row" && target.item.aliasCount >= aliasLimit),
      icon: AtSign,
      key: "alias" as const,
      label: "Create alias",
    },
  ];
  const selectedAction = actions.find((item) => item.key === selected) ?? actions[0];
  const rowItem = target.mode === "row" ? target.item : null;
  const maintenanceState = rowItem?.status === "validating"
    ? "Running"
    : rowItem?.status === "pending"
      ? "Queued"
      : "Idle";

  const submit = async () => {
    if (!selectedAction || selectedAction.disabled) return;
    setSubmitting(true);
    try {
      // iCloud validation already owns session refresh, alias synchronization,
      // missing-alias provisioning, and delivery verification as one fenced task.
      let count = 1;
      if (target.mode === "row") {
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
        selected === "alias" ? "Alias creation batch submitted." : "Resource validation batch submitted.",
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
              label={t("Maintenance status")}
              value={
                <Tag
                  color={maintenanceState === "Running" ? "orange" : maintenanceState === "Queued" ? "blue" : "grey"}
                  shape="circle"
                  size="small"
                >
                  {t(maintenanceState)}
                </Tag>
              }
            />
            <InfoItem
              label={t("Alias count")}
              value={<AliasCountTag count={rowItem.aliasCount} limit={aliasLimit} />}
            />
            <InfoItem label={t("Last checked")} value={formatTime(rowItem.lastCheckedAt)} />
            <InfoItem label={t("Next run at")} value={formatTime(rowItem.nextValidationAt)} />
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
  refreshGeneration,
  resourceId,
}: {
  refreshGeneration: number;
  resourceId: number;
}) {
  const { t } = useTranslation();
  const [pageSize, setPageSize] = useSharedPageSize();
  const [page, setPage] = useState(1);
  const [refreshKey, setRefreshKey] = useState(0);
  const [loading, setLoading] = useState(true);
  const [errorMessage, setErrorMessage] = useState<string | null>(null);
  const [response, setResponse] = useState<AdminICloudTaskList>({
    items: [],
    limit: pageSize,
    offset: 0,
    succeeded: 0,
    total: 0,
  });

  useEffect(() => setPage(1), [pageSize, resourceId]);
  useEffect(() => {
    const controller = new AbortController();
    let pollTimer: ReturnType<typeof globalThis.setTimeout> | null = null;
    setLoading(true);
    setErrorMessage(null);
    void listAdminICloudTasks(
      resourceId,
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
        if (next.items.some((task) => task.status === "queued" || task.status === "running")) {
          pollTimer = globalThis.setTimeout(
            () => setRefreshKey((value) => value + 1),
            1500,
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
  }, [page, pageSize, refreshGeneration, refreshKey, resourceId, t]);

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

  return (
    <div>
      <div className="mb-4 grid gap-3 sm:grid-cols-3">
        <InfoItem label={t("Total tasks")} value={<span className="font-mono tabular-nums">{response.total}</span>} />
        <InfoItem label={t("Succeeded tasks")} value={<span className="font-mono tabular-nums">{response.succeeded}</span>} />
        <InfoItem label={t("Success rate")} value={<span className="font-mono tabular-nums">{successRate}%</span>} />
      </div>
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
        dataIndex: "recipientMailId",
        title: t("Recipient ID"),
        width: 240,
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
        dataIndex: "forwardToEmail",
        title: t("Forwarding address"),
        width: 240,
        render: (value: unknown) =>
          value ? (
            <CopyableTableText copiedText={t("Copied")} text={String(value)} />
          ) : (
            "-"
          ),
      },
      {
        dataIndex: "providerDomain",
        title: t("Provider domain"),
        width: 180,
        render: (value: unknown, alias: AdminICloudAliasItem) =>
          [alias.origin, String(value || "")].filter(Boolean).join(" · ") || "-",
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
                  <InfoItem label={t("Suffix")} value={item.suffix || "-"} />
                  <InfoItem
                    label={t("Current forwarding mailbox")}
                    value={
                      item.selectedForwardTo ? (
                        <CopyableTableText
                          copiedText={t("Copied")}
                          text={item.selectedForwardTo}
                        />
                      ) : (
                        t("Not configured")
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
                    label={t("iCloud session")}
                    value={<SessionStatusTag status={item.sessionStatus} />}
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
                  <InfoItem label={t("Session failures")} value={item.sessionFailures} />
                  <InfoItem label={t("Resource expires at")} value={formatTime(item.expireAt)} />
                  <InfoItem label={t("Next validation")} value={formatTime(item.nextValidationAt)} />
                  <InfoItem label={t("Next keepalive")} value={formatTime(item.nextKeepaliveAt)} />
                  <InfoItem label={t("Last checked")} value={formatTime(item.lastCheckedAt)} />
                  <InfoItem label={t("Last valid")} value={formatTime(item.lastValidAt)} />
                  <InfoItem label={t("Last alias sync")} value={formatTime(item.lastAliasSyncAt)} />
                  <InfoItem
                    label={t("Delivery probe started at")}
                    value={formatTime(item.deliveryProbeStartedAt)}
                  />
                  <InfoItem
                    label={t("Delivery verified at")}
                    value={formatTime(item.deliveryProbeVerifiedAt)}
                  />
                </div>
                {item.lastSafeError ? (
                  <div className="rounded-lg border border-[var(--semi-color-warning-light-active)] bg-[var(--semi-color-warning-light-default)] px-3 py-2 text-sm text-[var(--semi-color-text-0)]">
                    {item.lastSafeError}
                  </div>
                ) : null}
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
                      scroll={{ x: 1860, y: DRAWER_TABLE_SCROLL_Y }}
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
                refreshGeneration={refreshGeneration}
                resourceId={item.id}
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
  const [activeSuffix, setActiveSuffix] = useState("all");
  const [searchKeyword, setSearchKeyword] = useState("");
  const [createdAtRange, setCreatedAtRange] = useState<DateRangeValue>([]);
  const [statusFilter, setStatusFilter] = useState<StatusFilter>("all");
  const [privateFilter, setPrivateFilter] = useState<BooleanFilter>("all");
  const [sessionFilter, setSessionFilter] = useState<SessionFilter>("all");
  const [compactMode, setCompactMode] = useState(false);
  const [items, setItems] = useState<AdminICloudResourceItem[]>([]);
  const [facets, setFacets] = useState<AdminICloudResourceFacets>(EMPTY_FACETS);
  const [total, setTotal] = useState(0);
  const [aliasLimit, setAliasLimit] = useState(750);
  const [owners, setOwners] = useState<AdminICloudOwner[]>([]);
  const [loading, setLoading] = useState(true);
  const [importOpen, setImportOpen] = useState(false);
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
    if (activeSuffix !== "all") next.suffix = activeSuffix;
    if (statusFilter !== "all") next.status = statusFilter;
    if (privateFilter !== "all") next.forSale = privateFilter === "no";
    if (sessionFilter !== "all") next.sessionStatus = sessionFilter;
    if (createdFrom) next.createdFrom = createdFrom;
    if (createdTo) next.createdTo = createdTo;
    return next;
  }, [
    activeSuffix,
    createdAtRange,
    debouncedSearchKeyword,
    privateFilter,
    sessionFilter,
    statusFilter,
  ]);

  const refresh = useCallback(() => {
    setRefreshGeneration((value) => value + 1);
  }, []);

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
    const resourceId = detailIdRef.current;
    if (resourceId === null) return;
    void loadDetail(resourceId, true);
  }, [loadDetail, refreshGeneration]);

  useEffect(() => {
    if (detailId === null || (detail?.status !== "pending" && detail?.status !== "validating")) {
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
  }, [detail?.status, detailId, loadDetail]);

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

  const suffixCounts = facets.suffixes;
  const suffixSet = useMemo(
    () => new Set(suffixCounts.map((item) => item.key)),
    [suffixCounts],
  );
  useEffect(() => {
    if (activeSuffix !== "all" && !suffixSet.has(activeSuffix)) {
      setActiveSuffix("all");
    }
  }, [activeSuffix, suffixSet]);

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
      if (action === "recover" || (action === "toggle" && item.status === "disabled")) {
        patch.sessionStatus = "unchecked";
      }
      setItems((current) =>
        current.map((candidate) =>
          candidate.id === item.id ? { ...candidate, ...patch } : candidate,
        ),
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
        refresh();
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
    setSessionFilter("all");
    setActiveSuffix("all");
    setActivePage(1);
    setSelectedKeys([]);
  };

  const totalPages = Math.max(1, Math.ceil(total / pageSize));
  const safePage = Math.min(activePage, totalPages);
  useEffect(() => {
    if (safePage !== activePage) setActivePage(safePage);
  }, [activePage, safePage]);
  const allTabCount = suffixCounts.reduce((sum, item) => sum + item.count, 0);
  const activeFilterCount =
    Number(statusFilter !== "all") +
    Number(privateFilter !== "all") +
    Number(sessionFilter !== "all");

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
            refresh();
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
        refresh();
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
          <Space spacing={4} wrap={false}>
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
        <Space spacing={4} wrap={false}>
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
        dataIndex: "suffix",
        key: "suffix",
        title: t("Suffix"),
        width: 120,
        render: (value: unknown) => (
          <Tag color="white" shape="circle">
            {String(value || "-")}
          </Tag>
        ),
      },
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
        dataIndex: "selectedForwardTo",
        key: "forwardingMailbox",
        title: t("Current forwarding mailbox"),
        width: 260,
        render: (value: unknown) => (
          value ? (
            <CopyableTableText copiedText={t("Copied")} text={String(value)} />
          ) : (
            "-"
          )
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
        dataIndex: "sessionStatus",
        key: "session",
        title: t("iCloud session"),
        width: 130,
        render: (value: unknown) => (
          <SessionStatusTag status={value as AdminICloudSessionStatus} />
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

  const tabsArea = (
    <Tabs
      activeKey={activeSuffix}
      className="mb-2"
      collapsible
      onChange={(key) => {
        setActiveSuffix(String(key));
        setActivePage(1);
        setSelectedKeys([]);
      }}
      type="card"
    >
      <Tabs.TabPane
        itemKey="all"
        tab={
          <span className="flex items-center gap-2">
            {t("All")}
            <Tag color={activeSuffix === "all" ? "red" : "grey"} shape="circle">
              {allTabCount}
            </Tag>
          </span>
        }
      />
      {suffixCounts.map((item) => (
        <Tabs.TabPane
          itemKey={item.key}
          key={item.key}
          tab={
            <span className="flex items-center gap-2">
              <Layers size={14} />
              {item.key}
              <Tag color={activeSuffix === item.key ? "red" : "grey"} shape="circle">
                {item.count}
              </Tag>
            </span>
          }
        />
      ))}
    </Tabs>
  );

  const actionsArea = (
    <div className="flex w-full flex-col items-center justify-between gap-2 md:flex-row">
      <div className="order-2 flex w-full flex-wrap gap-2 md:order-1 md:w-auto">
        <Button
          className="flex-1 md:flex-initial"
          disabled={!canWrite}
          onClick={() => setImportOpen(true)}
          size="small"
          type="primary"
        >
          {t("Import")}
        </Button>
        <Button
          className="remail-toolbar-fixed-button flex-1 md:flex-none"
          loading={loading}
          onClick={refresh}
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

              <div className="px-2 pb-1 text-xs font-medium text-[var(--semi-color-text-2)]">
                {t("iCloud session")}
              </div>
              <div className="space-y-1">
                {(["all", ...Object.keys(sessionMeta)] as SessionFilter[]).map(
                  (value) => (
                    <StatisticFilterOption
                      active={sessionFilter === value}
                      count={facets.sessionStatus[value]}
                      key={value}
                      label={t(value === "all" ? "All" : sessionMeta[value].label)}
                      onSelect={(next) => {
                        setSessionFilter(next);
                        setActivePage(1);
                      }}
                      value={value}
                    />
                  ),
                )}
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
          placeholder={t("Search iCloud email, forwarding mailbox, owner, alias or recipient ID")}
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
        tabsArea={tabsArea}
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
          scroll={{ x: "max(100%, 1980px)", y: DESKTOP_TABLE_SCROLL_Y }}
          size="middle"
        />
      </CardPro>

      <ImportICloudModal
        onCancel={() => setImportOpen(false)}
        onImported={async () => {
          setActivePage(1);
          setSelectedKeys([]);
          refresh();
        }}
        owners={owners}
        visible={importOpen && canWrite}
      />

      <EditICloudModal
        canOperate={canOperate}
        onCancel={() => setEditTarget(null)}
        onSaved={() => {
          refresh();
        }}
        owners={owners}
        target={editTarget}
      />

      <EditICloudModal
        canOperate
        credentialsOnly
        onCancel={() => setCredentialTarget(null)}
        onSaved={() => {
          refresh();
        }}
        owners={owners}
        target={credentialTarget}
      />

      <ICloudMaintenanceModal
        aliasLimit={aliasLimit}
        onCancel={() => setMaintenanceTarget(null)}
        onCompleted={() => {
          setSelectedKeys([]);
          refresh();
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
        onReplaceCredentials={setCredentialTarget}
        onRecover={recoverResource}
        onSetExpiration={(item) => confirmExpiration(false, item.id)}
        onToggleDisabled={toggleDisabled}
        onTogglePublish={togglePublish}
      />
    </div>
  );
}
