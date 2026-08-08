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
import { Layers, SlidersHorizontal } from "lucide-react";
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
  deleteAdminICloudResource,
  disableAdminICloudResource,
  enableAdminICloudResource,
  importAdminICloudResources,
  listAdminICloudAliases,
  listAdminICloudOwners,
  listAdminICloudResources,
  publishAdminICloudResource,
  recoverAdminICloudResource,
  unpublishAdminICloudResource,
  validateAdminICloudResource,
  type AdminICloudAliasItem,
  type AdminICloudBatchAction,
  type AdminICloudBulkResponse,
  type AdminICloudImportErrorStrategy,
  type AdminICloudMutationResponse,
  type AdminICloudOwner,
  type AdminICloudResourceItem,
  type AdminICloudResourceFacets,
  type AdminICloudResourceListFilter,
  type AdminICloudResourceStatus,
  type AdminICloudSessionStatus,
} from "@/lib/admin-icloud-api";
import { getIamErrorMessage } from "@/lib/iam-errors";

import {
  DRAWER_PANEL_HEIGHT,
  DRAWER_TABLE_SCROLL_Y,
  InfoItem,
  OwnerIdentity,
  formatTime,
  ownerRoleLabel,
} from "./admin-microsoft/microsoft-meta";
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
  | "validate"
  | "toggle"
  | "publish"
  | "delete"
  | "recover";

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

function ImportICloudModal({
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
  const [content, setContent] = useState("");
  const [ownerId, setOwnerId] = useState<number | undefined>();
  const [errorStrategy, setErrorStrategy] =
    useState<AdminICloudImportErrorStrategy>("skip");
  const [submitting, setSubmitting] = useState(false);
  const previousVisible = useRef(false);
  const lineCount = useMemo(
    () => content.split(/\r?\n/).filter((line) => line.trim()).length,
    [content],
  );

  useEffect(() => {
    const opened = visible && !previousVisible.current;
    previousVisible.current = visible;
    if (!opened) return;
    setContent("");
    setOwnerId(undefined);
    setErrorStrategy("skip");
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
    if (!lineCount) {
      Toast.warning(t("Please enter iCloud resources."));
      return;
    }
    setSubmitting(true);
    try {
      const result = await importAdminICloudResources({
        content,
        errorStrategy,
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

        <div className="grid grid-cols-2 gap-2">
          <button
            className={switchButtonClass(errorStrategy === "skip")}
            onClick={() => setErrorStrategy("skip")}
            type="button"
          >
            {t("Skip errors")}
          </button>
          <button
            className={switchButtonClass(errorStrategy === "abort")}
            onClick={() => setErrorStrategy("abort")}
            type="button"
          >
            {t("Abort on error")}
          </button>
        </div>

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
            placeholder="primary@icloud.com----host----dsid----clientId----clientBuildNumber----clientMasteringNumber----Cookie----gmail@gmail.com"
            rows={8}
            style={{ height: IMPORT_ENTRY_AREA_HEIGHT, resize: "none" }}
            value={content}
          />
        </label>

        <div className="rounded-xl border border-[var(--semi-color-border)] bg-[var(--semi-color-fill-0)] p-3">
          <div className="mb-1 text-xs font-medium text-[var(--semi-color-text-0)]">
            {t("Supported format")}
          </div>
          <pre className="overflow-x-auto font-mono text-xs leading-relaxed text-[var(--semi-color-text-2)]">
            primaryEmail----host----dsid----clientId----clientBuildNumber----clientMasteringNumber----Cookie----Gmail
          </pre>
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

function ICloudDetailSheet({
  aliasLimit,
  busyAction,
  canOperate,
  item,
  refreshGeneration,
  onCancel,
  onDelete,
  onRecover,
  onToggleDisabled,
  onTogglePublish,
  onValidate,
}: {
  aliasLimit: number;
  busyAction: RowAction | null;
  canOperate: boolean;
  item: AdminICloudResourceItem | null;
  refreshGeneration: number;
  onCancel: () => void;
  onDelete: (item: AdminICloudResourceItem) => void;
  onRecover: (item: AdminICloudResourceItem) => void;
  onToggleDisabled: (item: AdminICloudResourceItem) => void;
  onTogglePublish: (item: AdminICloudResourceItem) => void;
  onValidate: (item: AdminICloudResourceItem) => void;
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
        item
          ? `${t("iCloud resource detail")} #${item.id}`
          : t("iCloud resource detail")
      }
      visible={Boolean(item)}
      width={isMobile ? "100%" : 940}
    >
      {item ? (
        <div className="flex min-h-full flex-col">
          <div className="sticky top-0 z-10 bg-[var(--semi-color-bg-2)] px-5 pt-2">
            <Tabs
              activeKey={activeTab}
              collapsible
              onChange={setActiveTab}
              type="line"
            >
              <Tabs.TabPane itemKey="basic" tab={t("Basic info")} />
              <Tabs.TabPane itemKey="validation" tab={t("Validation")} />
              <Tabs.TabPane itemKey="aliases" tab={t("Aliases")} />
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
                    label={t("Linked Gmail")}
                    value={
                      <CopyableTableText copiedText={t("Copied")} text={item.gmailEmail} />
                    }
                  />
                  <InfoItem
                    label={t("Selected forwarding address")}
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
                  <InfoItem label={t("Resource expires at")} value={formatTime(item.expireAt)} />
                  <InfoItem label={t("Next validation")} value={formatTime(item.nextValidationAt)} />
                  <InfoItem label={t("Next keepalive")} value={formatTime(item.nextKeepaliveAt)} />
                  <InfoItem label={t("Last checked")} value={formatTime(item.lastCheckedAt)} />
                  <InfoItem label={t("Last valid")} value={formatTime(item.lastValidAt)} />
                  <InfoItem label={t("Last alias sync")} value={formatTime(item.lastAliasSyncAt)} />
                  <InfoItem
                    label={t("Gmail delivery verified at")}
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
                      scroll={{ x: 1400, y: DRAWER_TABLE_SCROLL_Y }}
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
          </div>

          {canOperate ? (
            <div className="sticky bottom-0 flex flex-wrap items-center justify-end gap-2 border-t border-[var(--semi-color-border)] bg-[var(--semi-color-bg-0)] px-5 py-3">
              {item.status === "deleted" ? (
                <Button
                  disabled={Boolean(busyAction)}
                  loading={busyAction === "recover"}
                  onClick={() => onRecover(item)}
                  type="primary"
                >
                  {t("Recover")}
                </Button>
              ) : (
                <>
                  <Button
                    disabled={Boolean(busyAction) || item.status === "disabled"}
                    loading={busyAction === "validate"}
                    onClick={() => onValidate(item)}
                    type="primary"
                  >
                    {t("Validate")}
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
  const [detail, setDetail] = useState<AdminICloudResourceItem | null>(null);
  const [selectedKeys, setSelectedKeys] = useState<number[]>([]);
  const [rowBusy, setRowBusy] = useState<{ action: RowAction; id: number } | null>(
    null,
  );
  const [bulkBusy, setBulkBusy] = useState<AdminICloudBatchAction | null>(null);
  const [refreshGeneration, setRefreshGeneration] = useState(0);
  const listRequestRef = useRef<AbortController | null>(null);
  const statsRequestRef = useRef<AbortController | null>(null);
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
        setDetail((current) =>
          current
            ? response.items.find((item) => item.id === current.id) ?? current
            : null,
        );
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
        refresh();
      } catch (error) {
        Toast.error(getIamErrorMessage(t, error, "iCloud resource operation failed."));
      } finally {
        setRowBusy(null);
      }
    },
    [applyMutation, refresh, t],
  );

  const validateResource = useCallback(
    (item: AdminICloudResourceItem) =>
      runRowOperation(
        item,
        "validate",
        () => validateAdminICloudResource(item.id, item.version),
        "Resource validation submitted.",
      ),
    [runRowOperation],
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

  const runBatch = useCallback(
    async (action: AdminICloudBatchAction, allMatching: boolean) => {
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
          validate: "iCloud resources queued for validation.",
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
    (action: Exclude<AdminICloudBatchAction, "validate">, allMatching: boolean) => {
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

  useSelectionNotification({
    checkLabelKey: "Validate",
    checkLoading: bulkBusy === "validate",
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
    ],
    onCheck: () => void runBatch("validate", false),
    onClear: () => setSelectedKeys([]),
    onDelete: () => confirmBatch("delete", false),
    onSell: () => confirmBatch("disable", false),
    selectedCount: selectedKeys.length,
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
              onClick={() => setDetail(item)}
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
            onClick={() => setDetail(item)}
            size="small"
            type="tertiary"
          >
            {t("Details")}
          </Button>
          {canOperate ? (
            <>
              <Button
                disabled={
                  Boolean(rowBusy && busyAction !== "validate") ||
                  item.status === "disabled"
                }
                loading={busyAction === "validate"}
                onClick={() => void validateResource(item)}
                size="small"
                type="tertiary"
              >
                {t("Validate")}
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
      confirmDelete,
      recoverResource,
      rowBusy,
      t,
      toggleDisabled,
      togglePublish,
      validateResource,
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
        dataIndex: "gmailEmail",
        key: "gmail",
        title: t("Linked Gmail"),
        width: 260,
        render: (value: unknown) => (
          <CopyableTableText copiedText={t("Copied")} text={String(value)} />
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
        width: 500,
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
              content={t("Validate all matching iCloud resources")}
              mouseEnterDelay={0}
              mouseLeaveDelay={0.05}
              position="top"
            >
              <Button
                className="flex-1 md:flex-initial"
                loading={bulkBusy === "validate"}
                onClick={() => void runBatch("validate", true)}
                size="small"
                type="tertiary"
              >
                {t("Validate all")}
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
          placeholder={t("Search iCloud email, Gmail, owner or alias")}
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
          rowSelection={rowSelection}
          scroll={{ x: "max(100%, 2010px)", y: DESKTOP_TABLE_SCROLL_Y }}
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

      <ICloudDetailSheet
        aliasLimit={aliasLimit}
        busyAction={rowBusy?.id === detail?.id ? rowBusy?.action ?? null : null}
        canOperate={canOperate}
        item={detail}
        refreshGeneration={refreshGeneration}
        onCancel={() => setDetail(null)}
        onDelete={confirmDelete}
        onRecover={recoverResource}
        onToggleDisabled={toggleDisabled}
        onTogglePublish={togglePublish}
        onValidate={(item) => void validateResource(item)}
      />
    </div>
  );
}
