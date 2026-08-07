import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  Button,
  Dropdown,
  Empty,
  Input,
  Modal,
  Select,
  Space,
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
import { SlidersHorizontal } from "lucide-react";
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
  importAdminGmailResources,
  listAdminGmailOwners,
  listAdminGmailResources,
  setAdminGmailResourceEnabled,
  setAdminGmailResourceForSale,
  validateAdminGmailResource,
  type AdminGmailImportErrorStrategy,
  type AdminGmailOwner,
  type AdminGmailResourceItem,
  type AdminGmailResourceList,
  type AdminGmailResourceStatus,
} from "@/lib/admin-gmail-api";
import { getIamErrorMessage } from "@/lib/iam-errors";

const { Text } = Typography;
type StatusFilter = "all" | AdminGmailResourceStatus;
const IMPORT_ENTRY_AREA_HEIGHT = 208;

const statusMeta: Record<
  AdminGmailResourceStatus,
  { color: "green" | "grey" | "orange" | "blue"; label: string }
> = {
  pending: { color: "blue", label: "Pending" },
  validating: { color: "orange", label: "Validating" },
  identifying: { color: "blue", label: "Identifying" },
  normal: { color: "green", label: "Normal" },
  abnormal: { color: "orange", label: "Abnormal" },
  disabled: { color: "grey", label: "Disabled" },
};

function formatTime(value?: string | null) {
  if (!value) return "-";
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? "-" : date.toLocaleString();
}

function switchButtonClass(active: boolean) {
  return [
    "flex h-12 w-full items-center justify-center gap-2 rounded-lg border-2 px-4 text-sm font-semibold transition-all",
    active
      ? "border-[var(--semi-color-primary)] bg-[var(--semi-color-primary-light-default)] text-[var(--semi-color-primary)]"
      : "border-[var(--semi-color-border)] bg-[var(--semi-color-bg-2)] text-[var(--semi-color-text-1)] hover:border-[var(--semi-color-primary)] hover:bg-[var(--semi-color-fill-0)]",
  ].join(" ");
}

function ConfiguredTag({ configured }: { configured: boolean }) {
  const { t } = useTranslation();
  return (
    <Tag color={configured ? "green" : "grey"} shape="circle" size="small">
      {configured ? t("Configured") : t("Not configured")}
    </Tag>
  );
}

function ownerRoleLabel(role: AdminGmailOwner["role"]) {
  switch (role) {
    case "super_admin":
      return "Super Admin";
    case "admin":
      return "Admin";
    case "supplier":
      return "Supplier";
    default:
      return "User";
  }
}

function OwnerSelect({
  onChange,
  owners,
  t,
  value,
}: {
  onChange: (ownerId: number) => void;
  owners: AdminGmailOwner[];
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
      const result = await listAdminGmailOwners(keyword);
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
      onDropdownVisibleChange={(nextVisible) => {
        if (nextVisible && options.length === 0) void searchOwners("");
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

function ImportGmailModal({
  onCancel,
  onImported,
  owners,
  visible,
}: {
  onCancel: () => void;
  onImported: () => Promise<void>;
  owners: AdminGmailOwner[];
  visible: boolean;
}) {
  const { t } = useTranslation();
  const [content, setContent] = useState("");
  const [ownerId, setOwnerId] = useState<number | undefined>();
  const [errorStrategy, setErrorStrategy] =
    useState<AdminGmailImportErrorStrategy>("skip");
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
      Toast.warning(t("Please enter Gmail accounts."));
      return;
    }
    setSubmitting(true);
    let result: Awaited<ReturnType<typeof importAdminGmailResources>>;
    try {
      result = await importAdminGmailResources({
        content,
        errorStrategy,
        ownerId,
      });
      if (result.status === "failed") {
        throw new Error(result.lastSafeError || "Gmail import failed.");
      }
    } catch (error) {
      Toast.error(getIamErrorMessage(t, error, "Gmail import failed."));
      setSubmitting(false);
      return;
    }
    Toast.success(
      t("Gmail accounts imported.", {
        count: result.imported,
      }),
    );
    if (result.skipped) {
      Toast.warning(
        t("Gmail import skipped entries.", {
          count: result.skipped,
        }),
      );
    }
    onCancel();
    try {
      await onImported();
    } catch (error) {
      Toast.error(getIamErrorMessage(t, error, "Gmail resources load failed."));
    }
    setSubmitting(false);
  };

  return (
    <Modal
      cancelText={t("Cancel")}
      centered
      confirmLoading={submitting}
      onCancel={onCancel}
      onOk={() => void submit()}
      okText={t("Import")}
      title={t("Import Gmail Accounts")}
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
            <span>{t("Gmail resource entries")} *</span>
            <Text size="small" type="tertiary">
              {t("Parsed entries", { count: lineCount })}
            </Text>
          </span>
          <TextArea
            className="font-mono"
            onChange={setContent}
            placeholder="email@gmail.com----password"
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
            {`email@gmail.com----password
email@gmail.com----password----2FA
email@gmail.com----password----binding-email
email@gmail.com----password----2FA----app-password
email@gmail.com----password----binding-email----2FA`}
          </pre>
        </div>

        <div className="rounded-lg border border-[var(--semi-color-border)] bg-[var(--semi-color-fill-0)] px-3 py-2 text-xs leading-5 text-[var(--semi-color-text-2)]">
          {t("Gmail credentials are write-only and never returned by the resource API.")}
        </div>
      </div>
    </Modal>
  );
}

export default function AdminGmailEmails() {
  const { t } = useTranslation();
  const { currentUser } = useAuth();
  const isMobile = useIsMobile();
  const [pageSize, setPageSize] = useSharedPageSize();
  const [activePage, setActivePage] = useState(1);
  const [searchKeyword, setSearchKeyword] = useState("");
  const [debouncedSearchKeyword, flushSearchKeyword] =
    useDebouncedValue(searchKeyword);
  const [statusFilter, setStatusFilter] = useState<StatusFilter>("all");
  const [compactMode, setCompactMode] = useState(false);
  const [response, setResponse] = useState<AdminGmailResourceList | null>(null);
  const [owners, setOwners] = useState<AdminGmailOwner[]>([]);
  const [loading, setLoading] = useState(true);
  const [importVisible, setImportVisible] = useState(false);
  const [rowBusy, setRowBusy] = useState<{
    action: "publish" | "toggle" | "validate";
    id: number;
  } | null>(null);
  const listRequestRef = useRef<AbortController | null>(null);

  useEffect(() => {
    const controller = new AbortController();
    void listAdminGmailOwners("", controller.signal)
      .then((items) => {
        if (!controller.signal.aborted) setOwners(items);
      })
      .catch(() => {
        // Owner choices are optional UI data; the resource list remains usable.
      });
    return () => controller.abort();
  }, []);

  const canWrite = hasPermissionKey(
    currentUser,
    permissionKey("core:resource", "write"),
  );
  const canOperate = hasPermissionKey(
    currentUser,
    permissionKey("core:resource", "operate"),
  );

  const load = useCallback(async () => {
    listRequestRef.current?.abort();
    const controller = new AbortController();
    listRequestRef.current = controller;
    setLoading(true);
    try {
      const next = await listAdminGmailResources({
        limit: pageSize,
        offset: (activePage - 1) * pageSize,
        search: debouncedSearchKeyword.trim() || undefined,
        signal: controller.signal,
        status: statusFilter === "all" ? undefined : statusFilter,
      });
      if (controller.signal.aborted) return;
      setResponse(next);
      const lastPage = Math.max(1, Math.ceil(next.total / pageSize));
      if (activePage > lastPage) setActivePage(lastPage);
    } catch (error) {
      if (controller.signal.aborted) return;
      Toast.error(getIamErrorMessage(t, error, "Gmail resources load failed."));
    } finally {
      if (listRequestRef.current === controller) {
        listRequestRef.current = null;
        setLoading(false);
      }
    }
  }, [activePage, debouncedSearchKeyword, pageSize, statusFilter, t]);

  useEffect(() => {
    void load();
    return () => listRequestRef.current?.abort();
  }, [load]);

  useEffect(
    () => setActivePage(1),
    [debouncedSearchKeyword, pageSize, statusFilter],
  );

  const runResourceOperation = async (
    item: AdminGmailResourceItem,
    action: "publish" | "toggle" | "validate",
    operation: () => Promise<unknown>,
    successKey: string,
  ) => {
    setRowBusy({ action, id: item.id });
    try {
      await operation();
      Toast.success(t(successKey));
      await load();
    } catch (error) {
      Toast.error(getIamErrorMessage(t, error, "Gmail resource operation failed."));
    } finally {
      setRowBusy(null);
    }
  };

  const toggleResource = (item: AdminGmailResourceItem) => {
    const enabled = item.status === "disabled";
    return runResourceOperation(
      item,
      "toggle",
      () => setAdminGmailResourceEnabled(item.id, item.version, enabled),
      enabled ? "Gmail account enabled." : "Gmail account disabled.",
    );
  };

  const validateResource = (item: AdminGmailResourceItem) =>
    runResourceOperation(
      item,
      "validate",
      () => validateAdminGmailResource(item.id),
      "Resource validation submitted.",
    );

  const toggleResourceForSale = (item: AdminGmailResourceItem) =>
    runResourceOperation(
      item,
      "publish",
      () => setAdminGmailResourceForSale(item.id, item.version, !item.forSale),
      item.forSale
        ? "Gmail resource converted to private."
        : "Gmail resource published for public sale.",
    );

  const facets = response?.facets ?? {
    all: 0,
    pending: 0,
    validating: 0,
    identifying: 0,
    normal: 0,
    abnormal: 0,
    disabled: 0,
  };

  const applyStatusFilter = (value: StatusFilter) => {
    setStatusFilter(value);
    setActivePage(1);
  };

  const resetFilters = () => {
    setSearchKeyword("");
    flushSearchKeyword("");
    setStatusFilter("all");
    setActivePage(1);
  };

  const activeFilterCount = statusFilter === "all" ? 0 : 1;

  const columns = [
    {
      dataIndex: "email",
      title: t("Email"),
      width: 250,
      render: (value: unknown) => (
        <CopyableTableText copiedText={t("Copied")} text={String(value)} />
      ),
    },
    {
      dataIndex: "bindingEmail",
      title: t("Binding email"),
      width: 250,
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
      width: 120,
      render: (value: unknown, item: AdminGmailResourceItem) => {
        const meta = statusMeta[value as AdminGmailResourceStatus];
        if (!meta) return "-";
        const tag = (
          <Tag color={meta.color} shape="circle" size="small">
            {t(meta.label)}
          </Tag>
        );
        return item.lastSafeError ? (
          <Tooltip content={item.lastSafeError}>{tag}</Tooltip>
        ) : tag;
      },
    },
    {
      dataIndex: "forSale",
      title: t("Private"),
      width: 100,
      render: (value: unknown) => (
        <Tag color={!value ? "green" : "grey"} shape="circle" size="small">
          {!value ? t("Yes") : t("No")}
        </Tag>
      ),
    },
    {
      dataIndex: "passwordConfigured",
      title: t("Password"),
      width: 110,
      render: (value: unknown) => <ConfiguredTag configured={Boolean(value)} />,
    },
    {
      dataIndex: "twoFactorConfigured",
      title: "2FA",
      width: 100,
      render: (value: unknown) => <ConfiguredTag configured={Boolean(value)} />,
    },
    {
      dataIndex: "appPasswordConfigured",
      title: t("App password"),
      width: 130,
      render: (value: unknown) => <ConfiguredTag configured={Boolean(value)} />,
    },
    {
      dataIndex: "lastCheckedAt",
      title: t("Last checked"),
      width: 180,
      render: (value: unknown) => (
        <span className="text-xs text-[var(--semi-color-text-2)]">
          {formatTime(value ? String(value) : undefined)}
        </span>
      ),
    },
    {
      dataIndex: "createdAt",
      title: t("Created at"),
      width: 180,
      render: (value: unknown) => (
        <span className="text-xs text-[var(--semi-color-text-2)]">
          {formatTime(String(value))}
        </span>
      ),
    },
    {
      dataIndex: "operate",
      fixed: "right" as const,
      key: "operate",
      title: t("Action"),
      width: 330,
      render: (_value: unknown, item: AdminGmailResourceItem) => {
        if (!canOperate) return <Text type="tertiary">-</Text>;
        const busyAction = rowBusy?.id === item.id ? rowBusy.action : null;
        return (
          <Space spacing={4} wrap={false}>
            <Button
              disabled={Boolean(rowBusy) || item.status === "disabled"}
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
              onClick={() => void toggleResource(item)}
              size="small"
              type="tertiary"
            >
              {t(item.status === "disabled" ? "Enable" : "Disable")}
            </Button>
            <Button
              disabled={Boolean(rowBusy && busyAction !== "publish")}
              loading={busyAction === "publish"}
              onClick={() => void toggleResourceForSale(item)}
              size="small"
              type="tertiary"
            >
              {item.forSale ? t("Convert to private") : t("Put on sale")}
            </Button>
          </Space>
        );
      },
    },
  ];

  const tableColumns = compactMode
    ? columns.map((column) => {
        if (column.dataIndex !== "operate") return column;
        const { fixed: _fixed, ...rest } = column;
        return rest;
      })
    : columns;

  const actionsArea = (
    <div className="flex w-full flex-col items-center justify-between gap-2 md:flex-row">
      <div className="order-2 flex w-full flex-wrap gap-2 md:order-1 md:w-auto">
        <Button
          className="flex-1 md:flex-initial"
          disabled={!canWrite}
          onClick={() => setImportVisible(true)}
          size="small"
          type="primary"
        >
          {t("Import")}
        </Button>
        <Button
          className="remail-toolbar-fixed-button flex-1 md:flex-none"
          loading={loading}
          onClick={() => void load()}
          size="small"
          type="tertiary"
        >
          {t("Refresh")}
        </Button>
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
            <div className="w-[280px] p-2">
              <div className="px-2 pb-1 text-xs font-medium text-[var(--semi-color-text-2)]">
                {t("Status")}
              </div>
              <div className="space-y-1">
                <StatisticFilterOption
                  active={statusFilter === "all"}
                  count={facets.all}
                  label={t("All")}
                  onSelect={applyStatusFilter}
                  value="all"
                />
                {(Object.entries(statusMeta) as Array<
                  [
                    AdminGmailResourceStatus,
                    (typeof statusMeta)[AdminGmailResourceStatus],
                  ]
                >).map(([value, meta]) => (
                  <StatisticFilterOption
                    active={statusFilter === value}
                    count={facets[value]}
                    key={value}
                    label={t(meta.label)}
                    onSelect={applyStatusFilter}
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
          placeholder={t("Search Gmail address")}
          prefix={<IconSearch />}
          showClear
          size="small"
          style={{ width: isMobile ? "100%" : 224 }}
          value={searchKeyword}
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
    currentPage: activePage,
    isMobile,
    onPageChange: setActivePage,
    onPageSizeChange: (size) => {
      setPageSize(size);
      setActivePage(1);
    },
    pageSize,
    t,
    total: response?.total ?? 0,
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
          dataSource={response?.items ?? []}
          empty={
            <Empty
              darkModeImage={
                <IllustrationNoResultDark style={{ height: 150, width: 150 }} />
              }
              description={t("No Gmail resources found")}
              image={<IllustrationNoResult style={{ height: 150, width: 150 }} />}
              style={{ padding: 30 }}
            />
          }
          hidePagination
          loading={loading}
          pagination={false}
          rowKey="id"
          scroll={{ x: "max(100%, 1500px)", y: DESKTOP_TABLE_SCROLL_Y }}
          size="middle"
        />
      </CardPro>

      <ImportGmailModal
        onCancel={() => setImportVisible(false)}
        onImported={async () => {
          if (activePage === 1) {
            await load();
            return;
          }
          setActivePage(1);
        }}
        owners={owners}
        visible={importVisible && canWrite}
      />
    </div>
  );
}
