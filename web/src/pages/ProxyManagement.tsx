import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  Button,
  DatePicker,
  Dropdown,
  Empty,
  Form,
  Input,
  Modal,
  Space,
  Tabs,
  Tag,
  TextArea,
  Tooltip,
  Toast,
  Typography,
} from "@douyinfe/semi-ui";
import { IconSearch } from "@douyinfe/semi-icons";
import {
  IllustrationNoResult,
  IllustrationNoResultDark,
} from "@douyinfe/semi-illustrations";
import { FileText, Globe2, SlidersHorizontal, Upload } from "lucide-react";
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
import { useBlockPagedList } from "@/hooks/use-block-paged-list";
import { useDebouncedValue } from "@/hooks/use-debounced-value";
import { useIsMobile } from "@/hooks/use-is-mobile";
import { useSharedPageSize } from "@/hooks/use-shared-page-size";
import { getIamErrorMessage } from "@/lib/iam-errors";
import {
  checkAdminProxy,
  checkAdminProxies,
  checkAdminProxiesByFilter,
  deleteAdminProxies,
  deleteAdminProxiesByFilter,
  disableAdminProxiesByFilter,
  getAdminProxyStats,
  importAdminProxies,
  listAdminProxies,
  type ProxyBulkFilter,
  updateAdminProxy,
  type ProxyItem,
  type ProxyListFilter,
  type ProxyStatsResponse,
} from "@/lib/proxies-api";

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

type SystemProxyFilter = "all" | "yes" | "no";
type IPv6Filter = "all" | "yes" | "no";
type StatusFilter = "all" | "checking" | "normal" | "abnormal" | "disabled" | "expired";
type ProxyPool = "resource" | "system";
type ProxyImportMode = "paste" | "file";

const PROXY_ENTRY_AREA_HEIGHT = 208;
const PROXY_IMPORT_FORMAT_HINT = [
  "http://user:password@host:port",
  "https://user:password@host:port",
  "socks5://user:password@host:port",
  "socks5h://user:password@host:port",
].join("\n");

function renderProxyStatus(status: string, t: (key: string) => string, reason?: string) {
  const statusMap: Record<
    string,
    { color: "green" | "grey" | "orange" | "red"; label: string }
  > = {
    checking: { color: "orange", label: t("Checking") },
    normal: { color: "green", label: t("Normal") },
    abnormal: { color: "red", label: t("Abnormal") },
    disabled: { color: "grey", label: t("Disabled") },
    expired: { color: "red", label: t("Expired") },
  };
  const item = statusMap[status] ?? { color: "grey" as const, label: status };
  const tag = (
    <Tag color={item.color} shape="circle" size="small">
      {item.label}
    </Tag>
  );
  if ((status === "abnormal" || status === "disabled" || status === "expired") && reason) {
    return (
      <Tooltip
        content={reason}
        mouseEnterDelay={0}
        mouseLeaveDelay={0.05}
        position="top"
      >
        <span className="inline-flex">{tag}</span>
      </Tooltip>
    );
  }
  return tag;
}

function formatDateTime(value?: string | null) {
  if (!value) return "-";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "-";
  return date.toLocaleString();
}

function countryOf(proxy: ProxyItem) {
  return proxy.country?.trim() || "UNKNOWN";
}

function maskMiddle(value: string, head = 4, tail = 4) {
  if (!value) return "-";
  if (value.length <= head + tail) return "****";
  return `${value.slice(0, head)}****${value.slice(-tail)}`;
}

function maskProxyHost(host: string) {
  const normalized = host.replace(/^\[/, "").replace(/\]$/, "");
  if (!normalized) return "****";

  if (/^\d{1,3}(\.\d{1,3}){3}$/.test(normalized)) {
    const parts = normalized.split(".");
    return `${parts[0]}.****.${parts[3]}`;
  }

  if (normalized.includes(":")) {
    const parts = normalized.split(":").filter(Boolean);
    if (parts.length <= 2) return maskMiddle(normalized, 4, 4);
    return `[${parts[0]}:****:${parts[parts.length - 1]}]`;
  }

  return maskMiddle(normalized, 4, 4);
}

function maskProxyURL(url: string) {
  try {
    const parsed = new URL(url);
    const hasAuth = Boolean(parsed.username || parsed.password);
    const auth = hasAuth ? "****:****@" : "";
    const port = parsed.port ? `:${parsed.port}` : "";
    return `${parsed.protocol}//${auth}${maskProxyHost(parsed.hostname)}${port}`;
  } catch {
    return maskMiddle(url, 10, 8);
  }
}

function proxyMatchesListFilter(proxy: ProxyItem, filter: ProxyListFilter) {
  if (filter.pool && proxy.pool !== filter.pool) return false;
  if (filter.status && proxy.status !== filter.status) return false;
  if (filter.ip && filter.ip !== "auto" && proxy.ipVersion !== filter.ip) {
    return false;
  }
  if (filter.ipv6 === true && proxy.ipVersion !== "ipv6") return false;
  if (filter.ipv6 === false && proxy.ipVersion === "ipv6") return false;
  if (filter.country && countryOf(proxy) !== filter.country) return false;
  if (filter.createdFrom) {
    const createdAt = new Date(proxy.createdAt).getTime();
    const createdFrom = new Date(filter.createdFrom).getTime();
    if (Number.isNaN(createdAt) || createdAt < createdFrom) return false;
  }
  if (filter.createdTo) {
    const createdAt = new Date(proxy.createdAt).getTime();
    const createdTo = new Date(filter.createdTo).getTime();
    if (Number.isNaN(createdAt) || createdAt > createdTo) return false;
  }
  return true;
}

const emptyStats = {
  ipv6: { all: 0, no: 0, yes: 0 },
  systemProxy: { all: 0, no: 0, yes: 0 },
  status: { all: 0, abnormal: 0, checking: 0, disabled: 0, expired: 0, normal: 0 },
};

function countOf(
  items: ProxyStatsResponse["statuses"],
  key: string
) {
  return items.find((item) => item.key === key)?.count ?? 0;
}

function proxyStatsFromResponse(stats?: ProxyStatsResponse | null) {
  if (!stats) return emptyStats;
  const total = stats.total;
  const systemCount = countOf(stats.pools, "system");
  const ipv6Count = countOf(stats.ipVersions, "ipv6");
  return {
    ipv6: {
      all: total,
      no: Math.max(total - ipv6Count, 0),
      yes: ipv6Count,
    },
    systemProxy: {
      all: total,
      no: Math.max(total - systemCount, 0),
      yes: systemCount,
    },
    status: {
      all: total,
      abnormal: countOf(stats.statuses, "abnormal"),
      checking: countOf(stats.statuses, "checking"),
      disabled: countOf(stats.statuses, "disabled"),
      expired: countOf(stats.statuses, "expired"),
      normal: countOf(stats.statuses, "normal"),
    },
  };
}

function getCountryCounts(stats?: ProxyStatsResponse | null) {
  return (stats?.countries ?? [])
    .map((item) => [item.key || "UNKNOWN", item.count] as [string, number])
    .sort((a, b) => {
      const leftUnknown = a[0].toUpperCase() === "UNKNOWN";
      const rightUnknown = b[0].toUpperCase() === "UNKNOWN";
      if (leftUnknown && !rightUnknown) return 1;
      if (!leftUnknown && rightUnknown) return -1;
      return a[0].localeCompare(b[0]);
    });
}

function parseProxyImportContent(content: string) {
  const entries: string[] = [];
  const invalidLines: number[] = [];
  const seen = new Set<string>();

  content.split("\n").forEach((rawLine, index) => {
    const line = rawLine.trim();
    if (!line || line.startsWith("#")) return;
    try {
      const parsed = new URL(line);
      const protocol = parsed.protocol.toLowerCase();
      if (
        !["http:", "https:", "socks5:", "socks5h:"].includes(protocol) ||
        !parsed.hostname ||
        !parsed.port
      ) {
        invalidLines.push(index + 1);
        return;
      }
      if (seen.has(line)) return;
      seen.add(line);
      entries.push(line);
    } catch {
      invalidLines.push(index + 1);
    }
  });

  return { entries, invalidLines };
}

function proxyExpireAtAfter(amount: "day" | "month" | "year") {
  const next = new Date();
  if (amount === "day") next.setDate(next.getDate() + 1);
  if (amount === "month") next.setMonth(next.getMonth() + 1);
  if (amount === "year") next.setFullYear(next.getFullYear() + 1);
  return next;
}

function ProxyExpireAtPicker({
  onChange,
  value,
}: {
  onChange: (value: Date | null) => void;
  value: Date | null;
}) {
  const { t } = useTranslation();
  return (
    <div className="space-y-2">
      <DatePicker
        type="dateTime"
        format="yyyy-MM-dd HH:mm:ss"
        showClear
        value={value ?? undefined}
        style={{ width: "100%" }}
        onChange={(nextValue) => {
          if (nextValue instanceof Date) {
            onChange(nextValue);
          } else {
            onChange(null);
          }
        }}
      />
      <div className="grid grid-cols-3 gap-2">
        <Button
          size="small"
          theme="outline"
          type="tertiary"
          onClick={() => onChange(proxyExpireAtAfter("day"))}
        >
          {t("One day")}
        </Button>
        <Button
          size="small"
          theme="outline"
          type="tertiary"
          onClick={() => onChange(proxyExpireAtAfter("month"))}
        >
          {t("One month")}
        </Button>
        <Button
          size="small"
          theme="outline"
          type="tertiary"
          onClick={() => onChange(proxyExpireAtAfter("year"))}
        >
          {t("One year")}
        </Button>
      </div>
    </div>
  );
}

interface ImportProxyModalProps {
  open: boolean;
  onOpenChange: (value: boolean) => void;
  onSubmit: (payload: {
    expireAt: string | null;
    pool: ProxyPool;
    urls: string[];
  }) => Promise<void>;
}

function ImportProxyModal({
  open,
  onOpenChange,
  onSubmit,
}: ImportProxyModalProps) {
  const { t } = useTranslation();
  const [mode, setMode] = useState<ProxyImportMode>("paste");
  const [pool, setPool] = useState<ProxyPool>("resource");
  const [text, setText] = useState("");
  const [expireAt, setExpireAt] = useState<Date | null>(null);
  const [file, setFile] = useState<File | null>(null);
  const [busy, setBusy] = useState(false);
  const fileRef = useRef<HTMLInputElement>(null);

  const parsed = useMemo(
    () => parseProxyImportContent(text),
    [text]
  );

  const reset = () => {
    setMode("paste");
    setPool("resource");
    setText("");
    setExpireAt(null);
    setFile(null);
    setBusy(false);
  };

  const close = () => {
    reset();
    onOpenChange(false);
  };

  const switchButtonClass = (active: boolean) =>
    [
      "flex h-12 w-full items-center justify-center gap-2 rounded-lg border-2 px-4 text-sm font-semibold transition-all",
      active
        ? "border-[var(--semi-color-primary)] bg-[var(--semi-color-primary-light-default)] text-[var(--semi-color-primary)]"
        : "border-[var(--semi-color-border)] bg-[var(--semi-color-bg-2)] text-[var(--semi-color-text-1)] hover:border-[var(--semi-color-primary)] hover:bg-[var(--semi-color-fill-0)]",
    ].join(" ");

  const handleImport = async () => {
    setBusy(true);
    try {
      const sourceText = mode === "paste" ? text : (await file?.text()) ?? "";
      const prepared = parseProxyImportContent(sourceText);
      if (prepared.invalidLines.length > 0) {
        throw new Error(
          t("Proxy import invalid line", { line: prepared.invalidLines[0] })
        );
      }
      if (prepared.entries.length === 0) {
        throw new Error(t("No valid proxy entries."));
      }
      await onSubmit({
        expireAt: expireAt?.toISOString() ?? null,
        pool,
        urls: prepared.entries,
      });
      close();
    } catch (error) {
      Toast.error(getIamErrorMessage(t, error, "Proxy import failed."));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal
      title={t("Import proxies")}
      visible={open}
      onCancel={close}
      footer={
        <Space>
          <Button disabled={busy} onClick={close} theme="outline">
            {t("Cancel")}
          </Button>
          <Button
            disabled={mode === "paste" ? parsed.entries.length === 0 : !file}
            loading={busy}
            onClick={handleImport}
            type="primary"
          >
            {busy ? t("Importing") : t("Import")}
          </Button>
        </Space>
      }
    >
      <div className="space-y-4">
        <div className="grid grid-cols-2 gap-2">
          <button
            className={switchButtonClass(mode === "paste")}
            onClick={() => {
              setMode("paste");
              setFile(null);
            }}
            type="button"
          >
            <FileText size={16} />
            {t("Manual input")}
          </button>
          <button
            className={switchButtonClass(mode === "file")}
            onClick={() => {
              setMode("file");
              setText("");
            }}
            type="button"
          >
            <Upload size={16} />
            {t("TXT file")}
          </button>
        </div>

        <div className="grid grid-cols-2 gap-2">
          <button
            className={switchButtonClass(pool === "resource")}
            onClick={() => setPool("resource")}
            type="button"
          >
            {t("Resource proxy")}
          </button>
          <button
            className={switchButtonClass(pool === "system")}
            onClick={() => setPool("system")}
            type="button"
          >
            {t("System proxy")}
          </button>
        </div>

        <div>
          <div className="mb-1 text-xs font-medium text-[var(--semi-color-text-1)]">
            {t("Expire at")}
          </div>
          <ProxyExpireAtPicker value={expireAt} onChange={setExpireAt} />
        </div>

        <div>
          {mode === "paste" ? (
            <TextArea
              className="font-mono"
              onChange={(value) => setText(value)}
              placeholder="socks5://user:password@127.0.0.1:1080"
              rows={8}
              style={{ height: PROXY_ENTRY_AREA_HEIGHT, resize: "none" }}
              value={text}
            />
          ) : (
            <button
              className="flex w-full flex-col items-center justify-center rounded-xl border border-dashed border-[var(--semi-color-border)] bg-[var(--semi-color-fill-0)] p-6 text-center transition-colors hover:bg-[var(--semi-color-fill-1)]"
              onClick={() => fileRef.current?.click()}
              style={{ height: PROXY_ENTRY_AREA_HEIGHT }}
              type="button"
            >
              <input
                accept=".txt"
                className="hidden"
                onChange={(event) => setFile(event.target.files?.[0] ?? null)}
                ref={fileRef}
                type="file"
              />
              <FileText className="mb-2 size-8 text-[var(--semi-color-text-2)]" />
              <Text strong>
                {file ? file.name : t("Click to select or drag file here")}
              </Text>
              <Text size="small" type="tertiary">
                {file
                  ? `${(file.size / 1024).toFixed(1)} KB`
                  : t("Supports .txt files, one entry per line")}
              </Text>
            </button>
          )}
          <div className="mt-1 min-h-5">
            {mode === "paste" && text.length > 0 ? (
              <Text size="small" type="tertiary">
                {t("Parsed proxies", { count: parsed.entries.length })}
              </Text>
            ) : null}
          </div>
        </div>

        <div className="rounded-xl border border-[var(--semi-color-border)] bg-[var(--semi-color-fill-0)] p-3">
          <div className="mb-1 text-xs font-medium text-[var(--semi-color-text-0)]">
            {t("Supported format")}
          </div>
          <pre className="font-mono text-xs leading-relaxed text-[var(--semi-color-text-2)]">
            {PROXY_IMPORT_FORMAT_HINT}
          </pre>
        </div>
      </div>
    </Modal>
  );
}

interface EditProxyModalProps {
  open: boolean;
  proxy?: ProxyItem | null;
  onCancel: () => void;
  onSubmit: (payload: { expireAt: string | null; url: string }) => Promise<void>;
}

function EditProxyModal({
  open,
  proxy,
  onCancel,
  onSubmit,
}: EditProxyModalProps) {
  const { t } = useTranslation();
  const [proxyURL, setProxyURL] = useState("");
  const [expireAt, setExpireAt] = useState<Date | null>(null);
  const [submitting, setSubmitting] = useState(false);

  useEffect(() => {
    if (!open) return;
    setProxyURL(proxy?.url ?? "");
    setExpireAt(proxy?.expireAt ? new Date(proxy.expireAt) : null);
  }, [open, proxy]);

  return (
    <Modal
      title={t("Edit proxy")}
      visible={open}
      onCancel={onCancel}
      okText={t("Save")}
      cancelText={t("Cancel")}
      confirmLoading={submitting}
      onOk={async () => {
        if (!proxyURL.trim()) {
          Toast.error(t("Please enter proxy URL."));
          return;
        }
        setSubmitting(true);
        try {
          await onSubmit({
            expireAt: expireAt?.toISOString() ?? null,
            url: proxyURL.trim(),
          });
        } catch (error) {
          Toast.error(getIamErrorMessage(t, error, "Proxy update failed."));
        } finally {
          setSubmitting(false);
        }
      }}
    >
      <Form labelPosition="top">
        <Form.Slot label={t("Proxy URL")}>
          <Input
            value={proxyURL}
            onChange={(value) => setProxyURL(String(value))}
          />
        </Form.Slot>
        <Form.Slot label={t("Expire at")}>
          <ProxyExpireAtPicker value={expireAt} onChange={setExpireAt} />
        </Form.Slot>
      </Form>
    </Modal>
  );
}

export default function ProxyManagement() {
  const { t } = useTranslation();
  const isMobile = useIsMobile();
  const [proxyStats, setProxyStats] = useState<ProxyStatsResponse | null>(null);
  const [currentProxyStats, setCurrentProxyStats] =
    useState<ProxyStatsResponse | null>(null);
  const [operationLoading, setOperationLoading] = useState(false);
  const [checkingIDs, setCheckingIDs] = useState<Set<number>>(new Set());
  const [updatingID, setUpdatingID] = useState<number | null>(null);
  const [deletingBatch, setDeletingBatch] = useState(false);
  const [togglingAllDisabled, setTogglingAllDisabled] = useState(false);
  const [activeCountry, setActiveCountry] = useState("all");
  const [searchKeyword, setSearchKeyword] = useState("");
  const [createdAtRange, setCreatedAtRange] = useState<DateRangeValue>([]);
  const [statusFilter, setStatusFilter] = useState<StatusFilter>("all");
  const [systemProxyFilter, setSystemProxyFilter] =
    useState<SystemProxyFilter>("all");
  const [ipv6Filter, setIPv6Filter] = useState<IPv6Filter>("all");
  const [compactMode, setCompactMode] = useState(false);
  const [selectedKeys, setSelectedKeys] = useState<number[]>([]);
  const [activePage, setActivePage] = useState(1);
  const [pageSize, setPageSize] = useSharedPageSize();

  useEffect(() => setActivePage(1), [pageSize]);
  const [importOpen, setImportOpen] = useState(false);
  const [editingProxy, setEditingProxy] = useState<ProxyItem | null>(null);
  const dateRangePresets = useMemo(() => createDateRangePresets(t), [t]);
  const [debouncedSearchKeyword, flushSearchKeyword] =
    useDebouncedValue(searchKeyword);

  const listFilter = useMemo<ProxyListFilter>(() => {
    const filter: ProxyListFilter = {};
    const search = debouncedSearchKeyword.trim();
    const createdFrom = createdFromISOString(createdAtRange);
    const createdTo = createdToISOString(createdAtRange);
    if (activeCountry !== "all") filter.country = activeCountry;
    if (search) filter.search = search;
    if (statusFilter !== "all") filter.status = statusFilter;
    if (systemProxyFilter === "yes") filter.pool = "system";
    if (systemProxyFilter === "no") filter.pool = "resource";
    if (ipv6Filter === "yes") filter.ipv6 = true;
    if (ipv6Filter === "no") filter.ipv6 = false;
    if (createdFrom) filter.createdFrom = createdFrom;
    if (createdTo) filter.createdTo = createdTo;
    return filter;
  }, [
    activeCountry,
    createdAtRange,
    debouncedSearchKeyword,
    ipv6Filter,
    statusFilter,
    systemProxyFilter,
  ]);

  const statsFilter = useMemo<ProxyListFilter>(() => {
    const { country: _country, ...filter } = listFilter;
    return filter;
  }, [listFilter]);

  const proxyBulkFilter = useMemo<ProxyBulkFilter>(
    () => ({ ...listFilter }),
    [listFilter]
  );

  const loadProxyBlock = useCallback(
    async (offset: number, limit: number) => {
      const response = await listAdminProxies(listFilter, offset, limit);
      return { items: response.items, total: response.total };
    },
    [listFilter]
  );

  const {
    loading,
    pagedItems,
    refresh: refreshProxyList,
    total: totalItems,
    updateLoadedItems,
  } = useBlockPagedList<ProxyItem>({
    activePage,
    filterKey: JSON.stringify(listFilter),
    loadBlock: loadProxyBlock,
    onError: (error) => {
      Toast.error(getIamErrorMessage(t, error, "Proxies load failed."));
    },
    pageSize,
  });

  const countryCounts = useMemo(
    () => getCountryCounts(proxyStats),
    [proxyStats]
  );
  const countrySet = useMemo(
    () => new Set(countryCounts.map(([country]) => country)),
    [countryCounts]
  );

  useEffect(() => {
    if (activeCountry !== "all" && !countrySet.has(activeCountry)) {
      setActiveCountry("all");
    }
  }, [activeCountry, countrySet]);

  const stats = useMemo(() => proxyStatsFromResponse(proxyStats), [proxyStats]);
  const currentStats = useMemo(
    () => proxyStatsFromResponse(currentProxyStats),
    [currentProxyStats]
  );
  const disableCandidateCount =
    currentStats.status.checking +
    currentStats.status.normal +
    currentStats.status.abnormal +
    currentStats.status.expired;
  const enableCandidateCount = currentStats.status.disabled;
  const hasToggleCandidates = disableCandidateCount > 0 || enableCandidateCount > 0;
  const allFilteredDisabled = disableCandidateCount === 0 && enableCandidateCount > 0;

  const activeStatisticFilterCount =
    Number(statusFilter !== "all") +
    Number(systemProxyFilter !== "all") +
    Number(ipv6Filter !== "all");

  const totalPages = Math.max(1, Math.ceil(totalItems / pageSize));
  const safePage = Math.min(activePage, totalPages);

  useEffect(() => {
    if (safePage !== activePage) setActivePage(safePage);
  }, [activePage, safePage]);

  const updateProxyItem = useCallback((next: ProxyItem) => {
    updateLoadedItems((previous) => {
      const exists = previous.some((item) => item.id === next.id);
      if (!exists) return previous;
      return previous.map((item) => (item.id === next.id ? next : item));
    });
  }, [updateLoadedItems]);

  const refreshStats = useCallback(async () => {
    try {
      const [statsResponse, currentStatsResponse] = await Promise.all([
        getAdminProxyStats(statsFilter),
        getAdminProxyStats(listFilter),
      ]);
      setProxyStats(statsResponse);
      setCurrentProxyStats(currentStatsResponse);
    } catch {
      // Stats are secondary; the next page refresh will retry.
    }
  }, [listFilter, statsFilter]);

  useEffect(() => {
    void refreshStats();
  }, [refreshStats]);

  const refresh = useCallback(
    async (
      options: {
        clearSelection?: boolean;
        refreshStats?: boolean;
      } = {}
    ) => {
      const { clearSelection = true, refreshStats: shouldRefreshStats = true } =
        options;
      if (shouldRefreshStats) {
        await refreshStats();
      }
      await refreshProxyList();
      if (clearSelection) setSelectedKeys([]);
    },
    [refreshProxyList, refreshStats]
  );

  const reconcileProxyItem = useCallback(
    (next: ProxyItem) => {
      if (proxyMatchesListFilter(next, listFilter)) {
        updateProxyItem(next);
      } else {
        setSelectedKeys((previous) => previous.filter((id) => id !== next.id));
        void refresh({ clearSelection: false });
      }
      if (proxyMatchesListFilter(next, listFilter)) {
        void refreshStats();
      }
    },
    [listFilter, refresh, refreshStats, updateProxyItem]
  );

  const updateProxyItems = useCallback((nextItems: ProxyItem[]) => {
    if (nextItems.length === 0) return;
    updateLoadedItems((previous) => {
      const byID = new Map(nextItems.map((item) => [item.id, item]));
      return previous.map((item) => byID.get(item.id) ?? item);
    });
  }, [updateLoadedItems]);

  const handleCheckProxy = useCallback(
    async (proxyID: number, notify = true) => {
      setCheckingIDs((previous) => new Set(previous).add(proxyID));
      try {
        const queued = await checkAdminProxy(proxyID);
        reconcileProxyItem(queued);
        if (notify) {
          Toast.success(t("Proxy check submitted."));
        }
      } catch (error) {
        if (notify) {
          Toast.error(getIamErrorMessage(t, error, "Proxy check failed."));
        }
      } finally {
        setCheckingIDs((previous) => {
          const next = new Set(previous);
          next.delete(proxyID);
          return next;
        });
      }
    },
    [reconcileProxyItem, t]
  );

  const handleCheckFiltered = useCallback(async () => {
    if (totalItems === 0) {
      Toast.info(t("No proxies to check."));
      return;
    }
    setOperationLoading(true);
    try {
      const response = await checkAdminProxiesByFilter(proxyBulkFilter);
      updateProxyItems(response.items);
      await refresh();
      Toast.success(
        t("Proxy check submitted with summary", {
          queued: response.queued,
        })
      );
    } catch (error) {
      Toast.error(getIamErrorMessage(t, error, "Proxy check failed."));
    } finally {
      setOperationLoading(false);
    }
  }, [proxyBulkFilter, refresh, t, totalItems, updateProxyItems]);

  const handleCheckSelected = useCallback(async () => {
    if (selectedKeys.length === 0) {
      Toast.info(t("No proxies to check."));
      return;
    }
    setCheckingIDs((previous) => {
      const next = new Set(previous);
      selectedKeys.forEach((id) => next.add(id));
      return next;
    });
    try {
      const response = await checkAdminProxies(selectedKeys);
      const visibleItems = response.items.filter((item) =>
        proxyMatchesListFilter(item, listFilter)
      );
      const hiddenIDs = response.items
        .filter((item) => !proxyMatchesListFilter(item, listFilter))
        .map((item) => item.id);
      updateProxyItems(visibleItems);
      if (hiddenIDs.length > 0) {
        const hiddenIDSet = new Set(hiddenIDs);
        setSelectedKeys((previous) =>
          previous.filter((id) => !hiddenIDSet.has(id))
        );
        await refresh({ clearSelection: false });
      } else {
        void refreshStats();
      }
      Toast.success(
        t("Proxy check submitted with summary", {
          queued: response.queued,
        })
      );
    } catch (error) {
      Toast.error(getIamErrorMessage(t, error, "Proxy check failed."));
    } finally {
      setCheckingIDs((previous) => {
        const next = new Set(previous);
        selectedKeys.forEach((id) => next.delete(id));
        return next;
      });
    }
  }, [listFilter, refresh, refreshStats, selectedKeys, t, updateProxyItems]);

  const deleteProxyIDs = useCallback(
    async (proxyIDs: number[]) => {
      if (proxyIDs.length === 0) {
        Toast.info(t("No proxies to delete."));
        return;
      }
      const response = await deleteAdminProxies(proxyIDs);
      const deletedIDs = new Set(response.deletedProxyIds ?? proxyIDs);
      setSelectedKeys((previous) =>
        previous.filter((id) => !deletedIDs.has(id))
      );
      await refresh();
      Toast.success(t("Proxies deleted.", { count: response.deleted }));
    },
    [refresh, t]
  );

  const handleDeleteSelected = useCallback(() => {
    if (selectedKeys.length === 0) {
      Toast.info(t("No proxies to delete."));
      return;
    }

    Modal.confirm({
      title: t("Confirm delete selected"),
      content: t("Confirm delete selected proxy content", {
        count: selectedKeys.length,
      }),
      okText: t("Delete selected"),
      cancelText: t("Cancel"),
      onOk: async () => {
        setDeletingBatch(true);
        try {
          await deleteProxyIDs(selectedKeys);
        } catch (error) {
          Toast.error(getIamErrorMessage(t, error, "Delete failed."));
        } finally {
          setDeletingBatch(false);
        }
      },
    });
  }, [deleteProxyIDs, selectedKeys, t]);

  const handleDeleteFiltered = useCallback(() => {
    if (totalItems === 0) {
      Toast.info(t("No proxies to delete."));
      return;
    }

    Modal.confirm({
      title: t("Confirm delete all"),
      content: t("Confirm delete all proxy content", {
        count: totalItems,
      }),
      okText: t("Delete all"),
      cancelText: t("Cancel"),
      onOk: async () => {
        setDeletingBatch(true);
        try {
          const response = await deleteAdminProxiesByFilter(proxyBulkFilter);
          setSelectedKeys([]);
          setActivePage(1);
          await refreshStats();
          await refreshProxyList();
          Toast.success(t("Proxies deleted.", { count: response.deleted }));
        } catch (error) {
          Toast.error(getIamErrorMessage(t, error, "Delete failed."));
        } finally {
          setDeletingBatch(false);
        }
      },
    });
  }, [proxyBulkFilter, refreshProxyList, refreshStats, t, totalItems]);

  const handleToggleFilteredDisabled = useCallback(() => {
    if (!hasToggleCandidates) {
      Toast.info(t("No proxies to update."));
      return;
    }

    if (allFilteredDisabled) {
      Modal.confirm({
        title: t("Confirm enable all"),
        content: t("Confirm enable all proxy content", {
          count: enableCandidateCount,
        }),
        okText: t("Enable all"),
        cancelText: t("Cancel"),
        onOk: async () => {
          setTogglingAllDisabled(true);
          try {
            const response = await checkAdminProxiesByFilter({
              ...proxyBulkFilter,
              status: "disabled",
            });
            await refresh();
            Toast.success(
              t("Proxy check submitted with summary", {
                queued: response.queued,
              })
            );
          } catch (error) {
            Toast.error(getIamErrorMessage(t, error, "Proxy check failed."));
          } finally {
            setTogglingAllDisabled(false);
          }
        },
      });
      return;
    }

    Modal.confirm({
      title: t("Confirm disable all"),
      content: t("Confirm disable all proxy content", {
        count: disableCandidateCount,
      }),
      okText: t("Disable all"),
      cancelText: t("Cancel"),
      okButtonProps: { type: "danger" },
      onOk: async () => {
        setTogglingAllDisabled(true);
        try {
          const response = await disableAdminProxiesByFilter(proxyBulkFilter);
          await refresh();
          Toast.success(t("Proxies disabled.", { count: response.disabled }));
        } catch (error) {
          Toast.error(getIamErrorMessage(t, error, "Proxy update failed."));
        } finally {
          setTogglingAllDisabled(false);
        }
      },
    });
  }, [
    allFilteredDisabled,
    disableCandidateCount,
    enableCandidateCount,
    hasToggleCandidates,
    proxyBulkFilter,
    refresh,
    t,
  ]);

  const handleImportProxies = useCallback(
    async (payload: { expireAt: string | null; pool: ProxyPool; urls: string[] }) => {
      const response = await importAdminProxies({
        expireAt: payload.expireAt,
        pool: payload.pool,
        urls: payload.urls,
      });
      updateProxyItems(response.items);
      await refresh();
      setSelectedKeys([]);
      Toast.success(
        t("Proxy import accepted with summary", {
          count: response.created,
          duplicated: response.duplicated,
        })
      );
    },
    [refresh, t, updateProxyItems]
  );

  const handleEditProxy = useCallback(
    async (payload: { expireAt: string | null; url: string }) => {
      if (!editingProxy) return;
      const updated = await updateAdminProxy(editingProxy.id, {
        expireAt: payload.expireAt,
        url: payload.url,
      });
      reconcileProxyItem(updated);
      setEditingProxy(null);
      Toast.success(t("Proxy updated."));
    },
    [editingProxy, reconcileProxyItem, t]
  );

  const openEditProxy = useCallback((record: ProxyItem) => {
    setEditingProxy(record);
  }, []);

  const handleDeleteProxy = useCallback(
    (record: ProxyItem) => {
      Modal.confirm({
        title: t("Confirm"),
        content: t("Confirm delete selected proxy content", { count: 1 }),
        okText: t("Delete"),
        cancelText: t("Cancel"),
        okButtonProps: { type: "danger" },
        onOk: () => deleteProxyIDs([record.id]),
      });
    },
    [deleteProxyIDs, t]
  );

  const handleToggleDisabled = useCallback(
    async (record: ProxyItem) => {
      setUpdatingID(record.id);
      try {
        const nextStatus =
          record.status === "disabled" ? "checking" : "disabled";
        const updated = await updateAdminProxy(record.id, {
          status: nextStatus,
        });
        reconcileProxyItem(updated);
        Toast.success(
          nextStatus === "disabled"
            ? t("Proxy disabled.")
            : t("Proxy enabled for checking.")
        );
      } catch (error) {
        Toast.error(getIamErrorMessage(t, error, "Proxy update failed."));
      } finally {
        setUpdatingID(null);
      }
    },
    [reconcileProxyItem, t]
  );

  const selectCountry = (country: string) => {
    setActiveCountry(country);
    setActivePage(1);
    setSelectedKeys([]);
  };

  const resetFilters = () => {
    setSearchKeyword("");
    flushSearchKeyword("");
    setCreatedAtRange([]);
    setStatusFilter("all");
    setSystemProxyFilter("all");
    setIPv6Filter("all");
    setActiveCountry("all");
    setActivePage(1);
    setSelectedKeys([]);
  };

  useSelectionNotification({
    selectedCount: selectedKeys.length,
    onCheck: () => void handleCheckSelected(),
    onClear: () => setSelectedKeys([]),
    onDelete: handleDeleteSelected,
    deleteLoading: deletingBatch,
    selectionDescriptionKey: "Selected proxies",
    t,
  });

  const columns = useMemo(
    () =>
      [
        {
          key: "country",
          title: t("Country"),
          dataIndex: "country",
          width: 96,
          render: (_: string, record: ProxyItem) => (
            <Tag color="white" shape="circle">
              {countryOf(record)}
            </Tag>
          ),
        },
        {
          key: "url",
          title: t("Proxy URL"),
          dataIndex: "url",
          width: 280,
          render: (text: string) => (
            <CopyableTableText
              copiedText={t("Copied")}
              copyContent={text}
              text={maskProxyURL(text)}
            />
          ),
        },
        {
          key: "systemProxy",
          title: t("System proxy"),
          dataIndex: "pool",
          width: 120,
          render: (pool: string) => (
            <Tag color={pool === "system" ? "green" : "grey"} shape="circle">
              {pool === "system" ? t("Yes") : t("No")}
            </Tag>
          ),
        },
        {
          key: "ipv6",
          title: "IPV6",
          dataIndex: "ipVersion",
          width: 80,
          render: (ipVersion: string) => (
            <Tag color={ipVersion === "ipv6" ? "green" : "grey"} shape="circle">
              {ipVersion === "ipv6" ? t("Yes") : t("No")}
            </Tag>
          ),
        },
        {
          key: "outboundIp",
          title: t("Outbound IP"),
          dataIndex: "outboundIp",
          width: 140,
          render: (text: string) => (
            <span className="break-all">{text || "-"}</span>
          ),
        },
        {
          key: "latency",
          title: t("Latency"),
          dataIndex: "latencyMs",
          width: 100,
          render: (value: number) =>
            value > 0 ? (
              <span className="tabular-nums">{value} ms</span>
            ) : (
              "-"
            ),
        },
        {
          key: "status",
          title: t("Status"),
          dataIndex: "status",
          width: 100,
          render: (status: string, record: ProxyItem) =>
            renderProxyStatus(status, t, record.lastSafeError),
        },
        {
          key: "expireAt",
          title: t("Expire at"),
          dataIndex: "expireAt",
          width: 150,
          render: (value: string) => (
            <span className="remail-table-cell-ellipsis">
              {formatDateTime(value)}
            </span>
          ),
        },
        {
          key: "errors",
          title: t("Errors"),
          dataIndex: "errors",
          width: 80,
          render: (value: number) => (
            <span className="tabular-nums">{value}</span>
          ),
        },
        {
          key: "operate",
          title: t("Action"),
          dataIndex: "operate",
          width: 240,
          fixed: "right",
          render: (_: unknown, record: ProxyItem) => (
            <Space spacing={4} wrap={false}>
              <Button
                type="tertiary"
                size="small"
                loading={checkingIDs.has(record.id)}
                onClick={() => void handleCheckProxy(record.id)}
              >
                {t("Check")}
              </Button>
              <Button
                type="tertiary"
                size="small"
                onClick={() => openEditProxy(record)}
              >
                {t("Edit")}
              </Button>
              <Button
                type="danger"
                size="small"
                onClick={() => handleDeleteProxy(record)}
              >
                {t("Delete")}
              </Button>
              <Button
                type={record.status === "disabled" ? "tertiary" : "danger"}
                size="small"
                loading={updatingID === record.id}
                onClick={() => void handleToggleDisabled(record)}
              >
                {record.status === "disabled" ? t("Enable") : t("Disable")}
              </Button>
            </Space>
          ),
        },
      ] as any[],
    [
      checkingIDs,
      handleCheckProxy,
      handleDeleteProxy,
      handleToggleDisabled,
      openEditProxy,
      t,
      updatingID,
    ]
  );

  const tableColumns = useMemo(() => {
    if (!compactMode) return columns;
    return columns.map((column) => {
      if (column.dataIndex !== "operate") return column;
      const { fixed: _fixed, ...rest } = column;
      return rest;
    });
  }, [columns, compactMode]);

  const rowSelection = {
    selectedRowKeys: selectedKeys,
    onChange: (keys: Array<string | number>) => {
      setSelectedKeys(keys.map((key) => Number(key)));
    },
  };

  const tabsArea = (
    <Tabs
      activeKey={activeCountry}
      type="card"
      collapsible
      onChange={(key) => selectCountry(String(key))}
      className="mb-2"
    >
      <Tabs.TabPane
        itemKey="all"
        tab={
          <span className="flex items-center gap-2">
            {t("All")}
            <Tag color={activeCountry === "all" ? "red" : "grey"} shape="circle">
              {stats.status.all}
            </Tag>
          </span>
        }
      />
      {countryCounts.map(([country, count]) => (
        <Tabs.TabPane
          key={country}
          itemKey={country}
          tab={
            <span className="flex items-center gap-2">
              <Globe2 size={14} />
              {country}
              <Tag
                color={activeCountry === country ? "red" : "grey"}
                shape="circle"
              >
                {count}
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
          type="primary"
          size="small"
          className="flex-1 md:flex-initial"
          onClick={() => setImportOpen(true)}
        >
          {t("Import")}
        </Button>
        <Button
          type="tertiary"
          size="small"
          className="remail-toolbar-fixed-button flex-1 md:flex-none"
          loading={loading}
          onClick={() => void refresh()}
        >
          {t("Refresh")}
        </Button>
        <Tooltip
          content={t("Check all")}
          mouseEnterDelay={0}
          mouseLeaveDelay={0.05}
          position="top"
        >
          <Button
            type="tertiary"
            size="small"
            className="flex-1 md:flex-initial"
            loading={operationLoading}
            onClick={() => void handleCheckFiltered()}
          >
            {t("Check")}
          </Button>
        </Tooltip>
        <Tooltip
          content={allFilteredDisabled ? t("Enable all") : t("Disable all")}
          mouseEnterDelay={0}
          mouseLeaveDelay={0.05}
          position="top"
        >
          <Button
            type={allFilteredDisabled ? "tertiary" : "danger"}
            size="small"
            className="flex-1 md:flex-initial"
            disabled={!hasToggleCandidates}
            loading={togglingAllDisabled}
            onClick={handleToggleFilteredDisabled}
          >
            {allFilteredDisabled ? t("Enable") : t("Disable")}
          </Button>
        </Tooltip>
        <Tooltip
          content={t("Delete all")}
          mouseEnterDelay={0}
          mouseLeaveDelay={0.05}
          position="top"
        >
          <Button
            type="danger"
            size="small"
            className="flex-1 md:flex-initial"
            loading={deletingBatch}
            onClick={handleDeleteFiltered}
          >
            {t("Delete")}
          </Button>
        </Tooltip>
        <CompactModeToggle
          compactMode={compactMode}
          setCompactMode={setCompactMode}
          t={t}
        />
      </div>

      <div className="order-1 flex w-full flex-col items-center gap-2 md:order-2 md:w-auto md:flex-row">
        <Dropdown
          position="bottomRight"
          trigger="click"
          render={
            <div className="w-[280px] p-2">
              <div className="px-2 pb-1 text-xs font-medium text-[var(--semi-color-text-2)]">
                {t("Status")}
              </div>
              <div className="mb-2 space-y-1">
                <StatisticFilterOption
                  active={statusFilter === "all"}
                  count={stats.status.all}
                  label={t("All")}
                  onSelect={(value) => {
                    setStatusFilter(value);
                    setActivePage(1);
                    setSelectedKeys([]);
                  }}
                  value="all"
                />
                <StatisticFilterOption
                  active={statusFilter === "checking"}
                  count={stats.status.checking}
                  label={t("Checking")}
                  onSelect={(value) => {
                    setStatusFilter(value);
                    setActivePage(1);
                    setSelectedKeys([]);
                  }}
                  value="checking"
                />
                <StatisticFilterOption
                  active={statusFilter === "normal"}
                  count={stats.status.normal}
                  label={t("Normal")}
                  onSelect={(value) => {
                    setStatusFilter(value);
                    setActivePage(1);
                    setSelectedKeys([]);
                  }}
                  value="normal"
                />
                <StatisticFilterOption
                  active={statusFilter === "abnormal"}
                  count={stats.status.abnormal}
                  label={t("Abnormal")}
                  onSelect={(value) => {
                    setStatusFilter(value);
                    setActivePage(1);
                    setSelectedKeys([]);
                  }}
                  value="abnormal"
                />
                <StatisticFilterOption
                  active={statusFilter === "disabled"}
                  count={stats.status.disabled}
                  label={t("Disabled")}
                  onSelect={(value) => {
                    setStatusFilter(value);
                    setActivePage(1);
                    setSelectedKeys([]);
                  }}
                  value="disabled"
                />
                <StatisticFilterOption
                  active={statusFilter === "expired"}
                  count={stats.status.expired}
                  label={t("Expired")}
                  onSelect={(value) => {
                    setStatusFilter(value);
                    setActivePage(1);
                    setSelectedKeys([]);
                  }}
                  value="expired"
                />
              </div>

              <div className="px-2 pb-1 text-xs font-medium text-[var(--semi-color-text-2)]">
                {t("System proxy")}
              </div>
              <div className="mb-2 space-y-1">
                <StatisticFilterOption
                  active={systemProxyFilter === "all"}
                  count={stats.systemProxy.all}
                  label={t("All")}
                  onSelect={(value) => {
                    setSystemProxyFilter(value);
                    setActivePage(1);
                    setSelectedKeys([]);
                  }}
                  value="all"
                />
                <StatisticFilterOption
                  active={systemProxyFilter === "yes"}
                  count={stats.systemProxy.yes}
                  label={t("Yes")}
                  onSelect={(value) => {
                    setSystemProxyFilter(value);
                    setActivePage(1);
                    setSelectedKeys([]);
                  }}
                  value="yes"
                />
                <StatisticFilterOption
                  active={systemProxyFilter === "no"}
                  count={stats.systemProxy.no}
                  label={t("No")}
                  onSelect={(value) => {
                    setSystemProxyFilter(value);
                    setActivePage(1);
                    setSelectedKeys([]);
                  }}
                  value="no"
                />
              </div>

              <div className="px-2 pb-1 text-xs font-medium text-[var(--semi-color-text-2)]">
                IPV6
              </div>
              <div className="space-y-1">
                <StatisticFilterOption
                  active={ipv6Filter === "all"}
                  count={stats.ipv6.all}
                  label={t("All")}
                  onSelect={(value) => {
                    setIPv6Filter(value);
                    setActivePage(1);
                    setSelectedKeys([]);
                  }}
                  value="all"
                />
                <StatisticFilterOption
                  active={ipv6Filter === "yes"}
                  count={stats.ipv6.yes}
                  label={t("Yes")}
                  onSelect={(value) => {
                    setIPv6Filter(value);
                    setActivePage(1);
                    setSelectedKeys([]);
                  }}
                  value="yes"
                />
                <StatisticFilterOption
                  active={ipv6Filter === "no"}
                  count={stats.ipv6.no}
                  label={t("No")}
                  onSelect={(value) => {
                    setIPv6Filter(value);
                    setActivePage(1);
                    setSelectedKeys([]);
                  }}
                  value="no"
                />
              </div>
            </div>
          }
        >
          <Button
            className="flex-1 md:flex-initial"
            icon={<SlidersHorizontal size={14} />}
            size="small"
            type="tertiary"
          >
            {activeStatisticFilterCount > 0
              ? `${t("Filters")} (${activeStatisticFilterCount})`
              : t("Filters")}
          </Button>
        </Dropdown>
        <Input
          prefix={<IconSearch />}
          placeholder={t("Search proxy")}
          showClear
          size="small"
          value={searchKeyword}
          style={{ width: isMobile ? "100%" : 224 }}
          onChange={(value) => {
            setSearchKeyword(String(value));
            setActivePage(1);
            setSelectedKeys([]);
          }}
          className="resources-search-input w-full md:w-56"
        />
        <DatePicker
          type="dateTimeRange"
          format="yyyy-MM-dd HH:mm:ss"
          placeholder={[t("Start time"), t("End time")]}
          presetPosition="bottom"
          presets={dateRangePresets}
          dropdownClassName={DATE_RANGE_DROPDOWN_CLASS}
          showClear
          size="small"
          value={createdAtRange}
          style={{ width: isMobile ? "100%" : 380 }}
          onChange={(value) => {
            setCreatedAtRange(normalizeDateRangeValue(value));
            setActivePage(1);
            setSelectedKeys([]);
          }}
        />
        <div className="flex w-full gap-2 md:w-auto">
          <Button
            type="tertiary"
            size="small"
            loading={loading}
            className="remail-toolbar-fixed-button flex-1 md:flex-none"
            onClick={() => {
              flushSearchKeyword();
              setActivePage(1);
            }}
          >
            {t("Query")}
          </Button>
          <Button
            type="tertiary"
            size="small"
            className="flex-1 md:flex-initial"
            onClick={resetFilters}
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
      setSelectedKeys([]);
      setActivePage(page);
    },
    onPageSizeChange: (size) => {
      setPageSize(size);
      setActivePage(1);
    },
    pageSize,
    total: totalItems,
    t,
  });

  return (
    <div className="px-2 pt-5">
      <CardPro
        type="type3"
        tabsArea={tabsArea}
        actionsArea={actionsArea}
        paginationArea={paginationArea}
        t={t}
      >
        <CardTable
          columns={tableColumns}
          dataSource={pagedItems}
          empty={
            <Empty
              darkModeImage={
                <IllustrationNoResultDark style={{ height: 150, width: 150 }} />
              }
              description={t("No proxies yet")}
              image={<IllustrationNoResult style={{ height: 150, width: 150 }} />}
              style={{ padding: 30 }}
            />
          }
          hidePagination
          loading={loading}
          pagination={false}
          className="overflow-hidden rounded-xl"
          rowKey="id"
          rowSelection={rowSelection}
          scroll={{ x: "max(100%, 1446px)", y: DESKTOP_TABLE_SCROLL_Y }}
          size="middle"
        />
      </CardPro>

      <ImportProxyModal
        open={importOpen}
        onOpenChange={setImportOpen}
        onSubmit={handleImportProxies}
      />
      <EditProxyModal
        open={editingProxy !== null}
        proxy={editingProxy}
        onCancel={() => setEditingProxy(null)}
        onSubmit={handleEditProxy}
      />
    </div>
  );
}
