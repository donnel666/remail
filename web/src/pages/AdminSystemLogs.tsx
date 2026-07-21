import { useCallback, useEffect, useMemo, useState } from "react";
import {
  Banner,
  Button,
  DatePicker,
  Empty,
  Input,
  Modal,
  Select,
  Tabs,
  Tag,
  Toast,
  Typography,
} from "@douyinfe/semi-ui";
import { IconSearch } from "@douyinfe/semi-icons";
import {
  IllustrationNoResult,
  IllustrationNoResultDark,
} from "@douyinfe/semi-illustrations";
import { Trash2 } from "lucide-react";
import { useTranslation } from "react-i18next";

import { CardPro } from "@/components/semi/card-pro";
import { createCardProPagination } from "@/components/semi/card-pro-pagination";
import {
  CardTable,
  DESKTOP_TABLE_SCROLL_Y,
} from "@/components/semi/card-table";
import { CopyableTableText } from "@/components/semi/copyable-table-text";
import { hasPermission, useAuth } from "@/context/auth-provider";
import { useDebouncedValue } from "@/hooks/use-debounced-value";
import { useIsMobile } from "@/hooks/use-is-mobile";
import { useSharedPageSize } from "@/hooks/use-shared-page-size";
import {
  listLogs,
  purgeLogs,
  LOG_CATEGORIES,
  type LogCategory,
  type LogFacets,
  type LogLevel,
  type LogRow,
  type OperationLogRow,
  type OperationResult,
  type SystemLogRow,
} from "@/lib/admin-logs-api";

import {
  DATE_RANGE_DROPDOWN_CLASS,
  createDateRangePresets,
  createdFromISOString,
  createdToISOString,
  normalizeDateRangeValue,
  type DateRangeValue,
} from "./resources/date-range-filter";

type LevelFilter = "all" | LogLevel;
type ResultFilter = "all" | OperationResult;
type TagColor = "blue" | "purple" | "green" | "grey" | "amber" | "red";

const CATEGORY_META: Record<LogCategory, { color: TagColor; labelKey: string }> = {
  system: { color: "purple", labelKey: "System events" },
  operation: { color: "blue", labelKey: "Operation audit" },
};

const LEVEL_META: Record<LogLevel, { color: TagColor; labelKey: string }> = {
  info: { color: "grey", labelKey: "Info" },
  warning: { color: "amber", labelKey: "Warning" },
  error: { color: "red", labelKey: "Error" },
};

const RESULT_META: Record<OperationResult, { color: TagColor; labelKey: string }> = {
  success: { color: "green", labelKey: "Success" },
  failure: { color: "red", labelKey: "Failure" },
};

function formatTime(iso: string) {
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) return "-";
  const pad = (value: number) => String(value).padStart(2, "0");
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())} ${pad(date.getHours())}:${pad(date.getMinutes())}:${pad(date.getSeconds())}`;
}

function summaryCell(value: unknown) {
  return (
    <Typography.Paragraph
      ellipsis={{ rows: 1, showTooltip: { type: "popover", opts: { style: { width: 420 } } } }}
      style={{ marginBottom: 0 }}
    >
      {String(value)}
    </Typography.Paragraph>
  );
}

export default function AdminSystemLogs() {
  const { t } = useTranslation();
  const { currentUser } = useAuth();
  const isMobile = useIsMobile();
  const canCleanup =
    currentUser?.role === "super_admin" &&
    hasPermission(currentUser, "governance:log", "operate");

  const [category, setCategory] = useState<LogCategory>("system");
  const [levelFilter, setLevelFilter] = useState<LevelFilter>("all");
  const [resultFilter, setResultFilter] = useState<ResultFilter>("all");
  const [search, setSearch] = useState("");
  const [debouncedSearch, flushSearch] = useDebouncedValue(search);
  const [range, setRange] = useState<DateRangeValue>([]);

  const [activePage, setActivePage] = useState(1);
  const [pageSize, setPageSize] = useSharedPageSize();
  const [reloadTick, setReloadTick] = useState(0);

  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState(false);
  const [items, setItems] = useState<LogRow[]>([]);
  const [total, setTotal] = useState(0);
  const [facets, setFacets] = useState<LogFacets>({ system: 0, operation: 0 });

  const [cleanupOpen, setCleanupOpen] = useState(false);
  const [cleanupScope, setCleanupScope] = useState<LogCategory>("system");
  const [cleanupBefore, setCleanupBefore] = useState<Date | null>(null);
  const [cleanupBusy, setCleanupBusy] = useState(false);

  const dateRangePresets = useMemo(() => createDateRangePresets(t), [t]);
  const from = createdFromISOString(range);
  const to = createdToISOString(range);

  useEffect(() => {
    setActivePage(1);
  }, [category, levelFilter, resultFilter, debouncedSearch, from, to, pageSize]);

  useEffect(() => {
    const offset = (activePage - 1) * pageSize;
    let cancelled = false;
    setLoading(true);
    listLogs(
      {
        category,
        level: category === "system" ? levelFilter : undefined,
        result: category === "operation" ? resultFilter : undefined,
        search: debouncedSearch,
        from,
        to,
      },
      offset,
      pageSize
    )
      .then((page) => {
        if (cancelled) return;
        setItems(page.items);
        setTotal(page.total);
        setFacets(page.facets);
        setLoadError(false);
      })
      .catch(() => {
        if (cancelled) return;
        setItems([]);
        setTotal(0);
        setFacets({ system: 0, operation: 0 });
        setLoadError(true);
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [category, levelFilter, resultFilter, debouncedSearch, from, to, activePage, pageSize, reloadTick]);

  const refresh = useCallback(() => {
    setActivePage(1);
    setReloadTick((tick) => tick + 1);
  }, []);

  const resetFilters = useCallback(() => {
    setSearch("");
    flushSearch("");
    setLevelFilter("all");
    setResultFilter("all");
    setRange([]);
  }, [flushSearch]);

  const openCleanup = useCallback(() => {
    setCleanupScope(category);
    setCleanupBefore(null);
    setCleanupOpen(true);
  }, [category]);

  const runCleanup = useCallback(async () => {
    if (!canCleanup) {
      Toast.error(t("Permission denied."));
      return;
    }
    if (!cleanupBefore) {
      Toast.warning(t("Please choose a cutoff date."));
      return;
    }
    setCleanupBusy(true);
    try {
      const removed = await purgeLogs(cleanupScope, cleanupBefore.toISOString());
      Toast.success(
        t("Removed {{count}} {{type}} entries.", {
          count: removed,
          type: t(CATEGORY_META[cleanupScope].labelKey),
        })
      );
      setCleanupOpen(false);
      setCleanupBefore(null);
      refresh();
    } catch {
      Toast.error(t("Cleanup failed"));
    } finally {
      setCleanupBusy(false);
    }
  }, [canCleanup, cleanupBefore, cleanupScope, refresh, t]);

  const columns = useMemo(() => {
    const timeColumn = {
      dataIndex: "createdAt",
      key: "time",
      title: t("Time"),
      width: 178,
      render: (value: unknown) => (
        <span
          className="whitespace-nowrap tabular-nums text-[var(--semi-color-text-1)]"
          title={String(value)}
        >
          {formatTime(String(value))}
        </span>
      ),
    };
    const requestColumn = {
      dataIndex: "requestId",
      key: "request",
      title: t("Request ID"),
      width: 170,
      render: (value: unknown) => {
        const requestId = String(value ?? "");
        return requestId ? (
          <CopyableTableText copiedText={t("Copied")} text={requestId} />
        ) : (
          <span className="text-[var(--semi-color-text-3)]">{t("None")}</span>
        );
      },
    };

    if (category === "system") {
      return [
        timeColumn,
        {
          dataIndex: "level",
          key: "level",
          title: t("Level"),
          width: 92,
          render: (value: unknown) => {
            const meta = LEVEL_META[value as LogLevel];
            return (
              <Tag color={meta?.color ?? "grey"} shape="circle">
                {meta ? t(meta.labelKey) : String(value || "-")}
              </Tag>
            );
          },
        },
        {
          dataIndex: "module",
          key: "module",
          title: t("Module"),
          width: 125,
          render: (value: unknown) => <code className="text-xs">{String(value)}</code>,
        },
        {
          dataIndex: "eventType",
          key: "event",
          title: t("Event"),
          width: 235,
          render: (value: unknown) => <code className="text-xs break-all">{String(value)}</code>,
        },
        {
          key: "object",
          title: t("Business object"),
          width: 185,
          render: (_: unknown, row: SystemLogRow) => (
            <span className="text-sm">{row.bizType} <code>#{row.bizId}</code></span>
          ),
        },
        {
          dataIndex: "message",
          key: "summary",
          title: t("Summary"),
          width: 320,
          render: summaryCell,
        },
        requestColumn,
      ] as any[];
    }

    return [
      timeColumn,
      {
        dataIndex: "result",
        key: "result",
        title: t("Result"),
        width: 92,
        render: (value: unknown) => {
          const meta = RESULT_META[value as OperationResult];
          return (
            <Tag color={meta?.color ?? "grey"} shape="circle">
              {meta ? t(meta.labelKey) : String(value || "-")}
            </Tag>
          );
        },
      },
      {
        dataIndex: "operator",
        key: "operator",
        title: t("Operator"),
        width: 205,
        render: (value: unknown, row: OperationLogRow) => (
          <div className="min-w-0">
            <Typography.Text ellipsis={{ showTooltip: true }}>{String(value)}</Typography.Text>
            <div className="text-xs text-[var(--semi-color-text-3)]">#{row.operatorUserId}</div>
          </div>
        ),
      },
      {
        dataIndex: "operationType",
        key: "operation",
        title: t("Operation"),
        width: 255,
        render: (value: unknown) => <code className="text-xs break-all">{String(value)}</code>,
      },
      {
        key: "resource",
        title: t("Resource"),
        width: 195,
        render: (_: unknown, row: OperationLogRow) => (
          <span className="text-sm">{row.resourceType} <code>#{row.resourceId}</code></span>
        ),
      },
      {
        dataIndex: "safeSummary",
        key: "summary",
        title: t("Summary"),
        width: 320,
        render: summaryCell,
      },
      requestColumn,
    ] as any[];
  }, [category, t]);

  const renderDetails = useCallback(
    (row: LogRow) => {
      const details = row.category === "system"
        ? [
            [t("Time"), formatTime(row.createdAt)],
            [t("Module"), row.module],
            [t("Event"), row.eventType],
            [t("Business object"), `${row.bizType} #${row.bizId}`],
            [t("Summary"), row.message],
            [t("Detail"), row.detail || t("None")],
            [t("Request ID"), row.requestId || t("None")],
          ]
        : [
            [t("Time"), formatTime(row.createdAt)],
            [t("Operator"), `${row.operator} · #${row.operatorUserId}`],
            [t("Operation"), row.operationType],
            [t("Resource"), `${row.resourceType} #${row.resourceId}`],
            [t("Path"), row.path],
            [t("Summary"), row.safeSummary],
            [t("Request ID"), row.requestId || t("None")],
          ];
      return (
        <div className="grid gap-x-8 gap-y-2 bg-[var(--semi-color-fill-0)] p-4 md:grid-cols-2">
          {details.map(([label, value]) => (
            <div className="min-w-0" key={label}>
              <div className="mb-0.5 text-xs font-medium text-[var(--semi-color-text-2)]">{label}</div>
              <div className="break-all text-sm text-[var(--semi-color-text-0)]">{value}</div>
            </div>
          ))}
        </div>
      );
    },
    [t]
  );

  const tabsArea = (
    <Tabs
      activeKey={category}
      className="mb-2"
      onChange={(key) => {
        setLoading(true);
        setCategory(key as LogCategory);
      }}
      type="card"
    >
      {LOG_CATEGORIES.map((item) => (
        <Tabs.TabPane
          itemKey={item}
          key={item}
          tab={
            <span className="flex items-center gap-2">
              {t(CATEGORY_META[item].labelKey)}
              <Tag color={category === item ? CATEGORY_META[item].color : "grey"} shape="circle">
                {facets[item]}
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
          className="remail-toolbar-fixed-button flex-1 md:flex-none"
          loading={loading}
          onClick={refresh}
          size="small"
          type="tertiary"
        >
          {t("Refresh")}
        </Button>
        {canCleanup ? (
          <Button
            className="flex-1 md:flex-none"
            icon={<Trash2 size={14} />}
            onClick={openCleanup}
            size="small"
            type="danger"
          >
            {t("Clean up")}
          </Button>
        ) : null}
      </div>
      <div className="order-1 flex w-full flex-wrap items-center gap-2 md:order-2 md:w-auto">
        {category === "system" ? (
          <Select
            onChange={(value) => setLevelFilter(value as LevelFilter)}
            optionList={[
              { label: t("All levels"), value: "all" },
              { label: t("Info"), value: "info" },
              { label: t("Warning"), value: "warning" },
              { label: t("Error"), value: "error" },
            ]}
            size="small"
            style={{ width: isMobile ? "100%" : 130 }}
            value={levelFilter}
          />
        ) : (
          <Select
            onChange={(value) => setResultFilter(value as ResultFilter)}
            optionList={[
              { label: t("All results"), value: "all" },
              { label: t("Success"), value: "success" },
              { label: t("Failure"), value: "failure" },
            ]}
            size="small"
            style={{ width: isMobile ? "100%" : 130 }}
            value={resultFilter}
          />
        )}
        <Input
          maxLength={200}
          onChange={(value) => setSearch(String(value))}
          onEnterPress={() => flushSearch()}
          placeholder={t(
            category === "system"
              ? "Search module, event, object, summary or request ID"
              : "Search operator, operation, resource, summary or request ID"
          )}
          prefix={<IconSearch />}
          showClear
          size="small"
          style={{ width: isMobile ? "100%" : 290 }}
          value={search}
        />
        <DatePicker
          dropdownClassName={DATE_RANGE_DROPDOWN_CLASS}
          format="yyyy-MM-dd HH:mm:ss"
          onChange={(value) => setRange(normalizeDateRangeValue(value))}
          placeholder={[t("Start time"), t("End time")]}
          presetPosition="bottom"
          presets={dateRangePresets}
          showClear
          size="small"
          style={{ width: isMobile ? "100%" : 380 }}
          type="dateTimeRange"
          value={range}
        />
        <Button className="w-full md:w-auto" onClick={resetFilters} size="small" type="tertiary">
          {t("Reset")}
        </Button>
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
    total,
    t,
  });

  const visibleColumns: Record<string, boolean> = category === "system"
    ? { time: true, level: true, object: true, summary: true }
    : { time: true, result: true, resource: true, summary: true };

  return (
    <div className="px-2 pt-5">
      <CardPro
        actionsArea={actionsArea}
        paginationArea={paginationArea}
        t={t}
        tabsArea={tabsArea}
        type="type3"
      >
        <CardTable
          className="overflow-hidden rounded-xl"
          columns={columns}
          dataSource={items.filter(
            (row) =>
              row.category === category &&
              (row.category === "system" ? "level" in row : "result" in row)
          )}
          empty={
            <Empty
              darkModeImage={<IllustrationNoResultDark style={{ height: 150, width: 150 }} />}
              description={loadError ? t("Failed to load logs") : t("No logs found")}
              image={<IllustrationNoResult style={{ height: 150, width: 150 }} />}
              style={{ padding: 30 }}
            />
          }
          expandedRowRender={renderDetails}
          hidePagination
          loading={loading}
          pagination={false}
          rowExpandable={() => true}
          rowKey="id"
          scroll={{ x: "max(100%, 1300px)", y: DESKTOP_TABLE_SCROLL_Y }}
          size="small"
          visibleColumns={visibleColumns}
        />
      </CardPro>

      <Modal
        confirmLoading={cleanupBusy}
        okButtonProps={{ disabled: !cleanupBefore, type: "danger" }}
        okText={t("Confirm cleanup")}
        onCancel={() => setCleanupOpen(false)}
        onOk={() => void runCleanup()}
        title={t("Manual log cleanup")}
        visible={cleanupOpen}
      >
        <div className="flex flex-col gap-4 py-2">
          <Banner
            closeIcon={null}
            description={t("Only {{type}} entries created before the cutoff will be permanently deleted.", {
              type: t(CATEGORY_META[cleanupScope].labelKey),
            })}
            fullMode={false}
            type="warning"
          />
          <div className="flex items-center justify-between rounded-lg bg-[var(--semi-color-fill-0)] px-3 py-2">
            <span className="text-sm text-[var(--semi-color-text-2)]">{t("Cleanup scope")}</span>
            <Tag color={CATEGORY_META[cleanupScope].color} shape="circle">
              {t(CATEGORY_META[cleanupScope].labelKey)}
            </Tag>
          </div>
          <div className="flex flex-col gap-1">
            <span className="text-sm text-[var(--semi-color-text-2)]">{t("Delete logs created before")}</span>
            <DatePicker
              format="yyyy-MM-dd HH:mm:ss"
              onChange={(value) => setCleanupBefore((value as Date) ?? null)}
              placeholder={t("Select a cutoff date")}
              showClear
              style={{ width: "100%" }}
              type="dateTime"
              value={cleanupBefore ?? undefined}
            />
          </div>
        </div>
      </Modal>
    </div>
  );
}
