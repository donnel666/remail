import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Button, DatePicker, Dropdown, Empty, Input, Modal, Space, Tabs, Tag, Tooltip, Toast } from "@douyinfe/semi-ui";
import { IconSearch } from "@douyinfe/semi-icons";
import { IllustrationNoResult, IllustrationNoResultDark } from "@douyinfe/semi-illustrations";
import { Layers, SlidersHorizontal } from "lucide-react";
import { useTranslation } from "react-i18next";

import { CardPro } from "@/components/semi/card-pro";
import { createCardProPagination } from "@/components/semi/card-pro-pagination";
import { CardTable, DESKTOP_TABLE_SCROLL_Y } from "@/components/semi/card-table";
import { CompactModeToggle } from "@/components/semi/compact-mode-toggle";
import { CopyableTableText } from "@/components/semi/copyable-table-text";
import { StatisticFilterOption } from "@/components/semi/statistic-filter-option";
import { useAuth } from "@/context/auth-provider";
import { useBlockPagedList } from "@/hooks/use-block-paged-list";
import { useDebouncedValue } from "@/hooks/use-debounced-value";
import { useIsMobile } from "@/hooks/use-is-mobile";
import { useSharedPageSize } from "@/hooks/use-shared-page-size";
import { getIamErrorMessage } from "@/lib/iam-errors";
import { applyICloudCommand, applyICloudCommandByFilter, listICloudAliases, listICloudResources, type ICloudAliasList, type ICloudCommand, type ICloudListQuery, type ICloudResource, type ICloudResourceList } from "@/lib/icloud-api";
import { DATE_RANGE_DROPDOWN_CLASS, createDateRangePresets, createdFromISOString, createdToISOString, normalizeDateRangeValue, type DateRangeValue } from "./resources/date-range-filter";
import { ImportICloudEmailsModal } from "./resources/import-icloud-emails-modal";
import { renderStatusTag } from "./resources/resource-status-tag";
import { ensureSupplierRole, SupplierApplicationModal } from "./resources/supplier-application-modal";

import { getSuffix } from "./resources/model";
import { useSelectionNotification } from "./resources/use-selection-notification";

type StatusFilter = "all" | "normal" | "pending" | "validating" | "abnormal" | "disabled";
type BooleanFilter = "all" | "yes" | "no";

export default function ICloudEmails() {
  const { t } = useTranslation();
  const { currentUser, refreshCurrentUser } = useAuth();
  const isMobile = useIsMobile();
  const [activeSuffix, setActiveSuffix] = useState("all");
  const [searchKeyword, setSearchKeyword] = useState("");
  const [debouncedSearchKeyword, flushSearchKeyword] = useDebouncedValue(searchKeyword);
  const [statusFilter, setStatusFilter] = useState<StatusFilter>("all");
  const [privateFilter, setPrivateFilter] = useState<BooleanFilter>("all");
  const [createdAtRange, setCreatedAtRange] = useState<DateRangeValue>([]);
  const [activePage, setActivePage] = useState(1);
  const [pageSize, setPageSize] = useSharedPageSize();
  const [selectedKeys, setSelectedKeys] = useState<number[]>([]);
  const [facets, setFacets] = useState<ICloudResourceList["facets"]>();
  const [busyCommand, setBusyCommand] = useState<ICloudCommand | null>(null);
  const actionInFlight = useRef(false);
  const busy = busyCommand !== null;
  const publishingBatch = busyCommand === "publish";
  const deletingBatch = busyCommand === "delete";
  const [compactMode, setCompactMode] = useState(false);
  const [importOpen, setImportOpen] = useState(false);
  const [supplierOpen, setSupplierOpen] = useState(false);
  const [aliases, setAliases] = useState<ICloudResource | null>(null);
  const dateRangePresets = useMemo(() => createDateRangePresets(t), [t]);
  const filter = useMemo<ICloudListQuery>(() => ({
    suffix: activeSuffix === "all" ? undefined : activeSuffix,
    search: debouncedSearchKeyword.trim() || undefined,
    status: statusFilter === "all" ? undefined : statusFilter,
    forSale: privateFilter === "all" ? undefined : privateFilter === "no",
    createdFrom: createdFromISOString(createdAtRange),
    createdTo: createdToISOString(createdAtRange),
  }), [activeSuffix, debouncedSearchKeyword, statusFilter, privateFilter, createdAtRange]);
  const filterKey = JSON.stringify(filter);
  const loadBlock = useCallback(async (offset: number, limit: number, _cursor: unknown, signal: AbortSignal) => {
    const result = await listICloudResources({ ...filter, offset, limit }, signal);
    return { items: result.items, total: result.total, meta: result.facets };
  }, [filter]);
  const { loadedItems, pagedItems, total, loading, refresh } = useBlockPagedList<ICloudResource, ICloudResourceList["facets"]>({
    activePage, pageSize, blockSize: 100, filterKey, loadBlock,
    onLoaded: (result) => setFacets(result.meta),
    onError: (error) => Toast.error(getIamErrorMessage(t, error, "Failed to load email resources.")),
  });
  const refreshRef = useRef(refresh);
  refreshRef.current = refresh;
  useEffect(() => { setActivePage(1); setSelectedKeys([]); }, [filterKey, pageSize]);
  useEffect(() => {
    if (!loading && activePage > Math.max(1, Math.ceil(total / pageSize))) {
      setActivePage(Math.max(1, Math.ceil(total / pageSize)));
    }
  }, [total, loading, activePage, pageSize]);
  const pending = loadedItems.some((item) => item.status === "pending" || item.status === "validating");
  useEffect(() => {
    if (!pending || busy) return;
    const timer = window.setInterval(() => void refresh(), 10_000);
    return () => window.clearInterval(timer);
  }, [pending, busy, refresh]);

  const suffixCounts = facets?.suffixes?.map(({ key, count }) => [key, count] as const) ?? [];
  useEffect(() => {
    if (facets?.suffixes && activeSuffix !== "all" && !facets.suffixes.some((item) => item.key === activeSuffix)) {
      setActiveSuffix("all");
    }
  }, [facets, activeSuffix]);
  const resourceStats = {
    status: facets?.status ?? { all: total, normal: 0, pending: 0, validating: 0, abnormal: 0, disabled: 0, deleted: 0 },
    private: { all: facets?.forSale.all ?? total, yes: facets?.forSale.no ?? 0, no: facets?.forSale.yes ?? 0 },
  };
  const activeStatisticFilterCount = Number(statusFilter !== "all") + Number(privateFilter !== "all");
  const resetFilters = () => {
    setSearchKeyword("");
    flushSearchKeyword("");
    setActiveSuffix("all");
    setStatusFilter("all");
    setPrivateFilter("all");
    setCreatedAtRange([]);
    setActivePage(1);
    setSelectedKeys([]);
  };
  const selectSuffix = (value: string) => { setActiveSuffix(value); setActivePage(1); setSelectedKeys([]); };
  const applyStatusFilter = (value: StatusFilter) => { setStatusFilter(value); setActivePage(1); setSelectedKeys([]); };
  const applyPrivateFilter = (value: BooleanFilter) => { setPrivateFilter(value); setActivePage(1); setSelectedKeys([]); };

  const run = async (command: ICloudCommand, target: ICloudResource[] | ICloudListQuery) => {
    if (actionInFlight.current) return;
    actionInFlight.current = true;
    setBusyCommand(command);
    try {
      const result = Array.isArray(target)
        ? await applyICloudCommand(command, target)
        : await applyICloudCommandByFilter(command, target);
      const message = t("Apple resource action result", { affected: result.affected, skipped: result.skipped });
      if (result.skipped) Toast.warning(message); else Toast.success(message);
    } catch (error) {
      Toast.error(getIamErrorMessage(t, error, "Operation failed."));
    } finally {
      setSelectedKeys([]);
      await refreshRef.current();
      actionInFlight.current = false;
      setBusyCommand(null);
    }
  };
  const act = async (command: ICloudCommand, resources?: ICloudResource[]) => {
    if (actionInFlight.current) return;
    if (resources ? !resources.length : total === 0) { Toast.warning(t("Please select resources first.")); return; }
    if (!resources && total > 1000) {
      Toast.warning(t("iCloud bulk selection limit exceeded.", { limit: 1000 }));
      return;
    }
    if (!resources && command !== "validate" && privateFilter === "no") {
      Toast.info(t(command === "publish" ? "No private resources to publish." : "No private resources to delete."));
      return;
    }
    const target = resources ?? { ...filter };
    try {
      if (command === "publish" && !(await ensureSupplierRole(currentUser?.role, refreshCurrentUser, () => setSupplierOpen(true)))) return;
      if (command === "validate") { await run(command, target); return; }
      Modal.confirm({
        title: t(command === "delete" ? (resources ? "Confirm delete selected" : "Confirm delete all") : (resources ? "Confirm sell selected" : "Confirm sell all")),
        content: resources
          ? t(command === "delete" ? "Apple delete confirmation" : "Apple publish confirmation", { count: resources.length })
          : t(command === "delete" ? "Confirm delete all matching content" : "Confirm sell all matching content"),
        okText: t(command === "delete" ? "Delete" : "Sell"), cancelText: t("Cancel"),
        okButtonProps: { type: command === "delete" ? "danger" : "primary" },
        onOk: () => run(command, target),
      });
    } catch (error) { Toast.error(getIamErrorMessage(t, error, "Operation failed.")); }
  };
  const selected = loadedItems.filter((item) => selectedKeys.includes(item.id));
  const confirmCheckAll = () => { void act("validate"); };
  const confirmSellAll = () => act("publish");
  const confirmDeleteAll = () => { void act("delete"); };
  useSelectionNotification({
    selectedCount: selectedKeys.length,
    onClear: () => setSelectedKeys([]),
    onCheck: () => void act("validate", selected),
    onSell: () => void act("publish", selected),
    onDelete: () => void act("delete", selected),
    checkLoading: busyCommand === "validate", sellLoading: publishingBatch, deleteLoading: deletingBatch, t,
  });
  const columns = [
    { key: "suffix", title: t("Suffix"), dataIndex: "email", width: 120, render: (_: unknown, row: ICloudResource) => <Tag color="white" shape="circle">{getSuffix(row.email)}</Tag> },
    { key: "email", title: t("Email"), dataIndex: "email", width: 280, render: (_: unknown, row: ICloudResource) => <CopyableTableText copiedText={t("Copied")} text={row.email} /> },
    { key: "status", title: t("Status"), dataIndex: "status", width: 120, render: (_: unknown, row: ICloudResource) => renderStatusTag(row.status === "deleted" ? "disabled" : row.status, t, row.lastSafeError ?? undefined) },
    { key: "private", title: t("Private"), dataIndex: "forSale", width: 100, render: (_: unknown, row: ICloudResource) => <Tag color={row.forSale ? "grey" : "green"} shape="circle">{t(row.forSale ? "No" : "Yes")}</Tag> },
    { key: "aliases", title: t("Aliases"), dataIndex: "aliasCount", width: 90, render: (_: unknown, row: ICloudResource) => <Button type="tertiary" theme="borderless" size="small" onClick={() => setAliases(row)} aria-label={`${t("Aliases")}: ${row.email}`}>{row.aliasCount}</Button> },
    { key: "forwarding", title: t("Forwarding email"), dataIndex: "selectedForwardTo", width: 260, render: (_: unknown, row: ICloudResource) => row.selectedForwardTo ? <CopyableTableText copiedText={t("Copied")} text={row.selectedForwardTo} /> : "—" },
    { key: "expiry", title: t("Expires at"), dataIndex: "expireAt", width: 180, render: (_: unknown, row: ICloudResource) => new Date(row.expireAt).toLocaleString() },
    { key: "created", title: t("Created at"), dataIndex: "createdAt", width: 180, render: (_: unknown, row: ICloudResource) => new Date(row.createdAt).toLocaleString() },
    { key: "operate", title: t("Action"), dataIndex: "operate", width: 190, fixed: "right", render: (_: unknown, row: ICloudResource) => <Space spacing={4} wrap={false}>
      <Button type="tertiary" size="small" disabled={busy || row.status === "disabled"} onClick={() => void act("validate", [row])}>{t("Check")}</Button>
      <Button type="tertiary" size="small" disabled={busy || row.forSale || row.status !== "normal"} onClick={() => void act("publish", [row])}>{t("Sell")}</Button>
      <Button type="danger" size="small" disabled={busy || row.forSale} onClick={() => void act("delete", [row])}>{t("Delete")}</Button>
    </Space> },
  ];
  const tableColumns = compactMode ? columns.map(({ fixed, ...column }) => column) : columns;
  const rowSelection = { disabled: busy, selectedRowKeys: selectedKeys, onChange: (keys: Array<string | number>) => setSelectedKeys(keys.map(Number)) };

  const tabsArea = (
    <Tabs
      activeKey={activeSuffix}
      type="card"
      collapsible
      onChange={(key) => selectSuffix(String(key))}
      className="mb-2"
    >
      <Tabs.TabPane
        itemKey="all"
        tab={
          <span className="flex items-center gap-2">
            {t("All")}
            <Tag color={activeSuffix === "all" ? "red" : "grey"} shape="circle">
              {facets?.suffixes?.reduce((sum, item) => sum + item.count, 0) ?? total}
            </Tag>
          </span>
        }
      />
      {suffixCounts.map(([suffix, count]) => (
        <Tabs.TabPane
          key={suffix}
          itemKey={suffix}
          tab={
            <span className="flex items-center gap-2">
              <Layers size={14} />
              {suffix}
              <Tag
                color={activeSuffix === suffix ? "red" : "grey"}
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
          onClick={refresh}
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
            disabled={busy || loading || total === 0}
            onClick={confirmCheckAll}
          >
            {t("Check")}
          </Button>
        </Tooltip>
        <Tooltip
          content={t("Sell all")}
          mouseEnterDelay={0}
          mouseLeaveDelay={0.05}
          position="top"
        >
          <Button
            type="tertiary"
            size="small"
            className="flex-1 md:flex-initial"
            disabled={busy || loading || total === 0}
            loading={publishingBatch}
            onClick={() => void confirmSellAll()}
          >
            {t("Sell")}
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
            disabled={busy || loading || total === 0}
            loading={deletingBatch}
            onClick={confirmDeleteAll}
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
                  count={resourceStats.status.all}
                  label={t("All")}
                  onSelect={applyStatusFilter}
                  value="all"
                />
                <StatisticFilterOption
                  active={statusFilter === "normal"}
                  count={resourceStats.status.normal}
                  label={t("Normal")}
                  onSelect={applyStatusFilter}
                  value="normal"
                />
                <StatisticFilterOption
                  active={statusFilter === "pending"}
                  count={resourceStats.status.pending}
                  label={t("Pending")}
                  onSelect={applyStatusFilter}
                  value="pending"
                />
                <StatisticFilterOption
                  active={statusFilter === "validating"}
                  count={resourceStats.status.validating}
                  label={t("Validating")}
                  onSelect={applyStatusFilter}
                  value="validating"
                />
                <StatisticFilterOption
                  active={statusFilter === "abnormal"}
                  count={resourceStats.status.abnormal}
                  label={t("Abnormal")}
                  onSelect={applyStatusFilter}
                  value="abnormal"
                />
                <StatisticFilterOption
                  active={statusFilter === "disabled"}
                  count={resourceStats.status.disabled}
                  label={t("Disabled")}
                  onSelect={applyStatusFilter}
                  value="disabled"
                />
              </div>

              <div className="px-2 pb-1 text-xs font-medium text-[var(--semi-color-text-2)]">
                {t("Private")}
              </div>
              <div className="mb-2 space-y-1">
                <StatisticFilterOption
                  active={privateFilter === "all"}
                  count={resourceStats.private.all}
                  label={t("All")}
                  onSelect={applyPrivateFilter}
                  value="all"
                />
                <StatisticFilterOption
                  active={privateFilter === "yes"}
                  count={resourceStats.private.yes}
                  label={t("Yes")}
                  onSelect={applyPrivateFilter}
                  value="yes"
                />
                <StatisticFilterOption
                  active={privateFilter === "no"}
                  count={resourceStats.private.no}
                  label={t("No")}
                  onSelect={applyPrivateFilter}
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
          aria-label={t("Search email or suffix")}
          prefix={<IconSearch />}
          placeholder={t("Search email or suffix")}
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
    currentPage: activePage,
    isMobile,
    onPageChange: (page) => { setActivePage(page); setSelectedKeys([]); },
    onPageSizeChange: (size) => {
      setPageSize(size);
      setActivePage(1);
    },
    pageSize,
    total,
    t,
  });

  return (
    <div className="console-content-width py-5">
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
              description={t("No email resources yet")}
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
          scroll={{ x: "max(100%, 1080px)", y: DESKTOP_TABLE_SCROLL_Y }}
          size="middle"
        />
      </CardPro>

      <ImportICloudEmailsModal open={importOpen} onOpenChange={setImportOpen} onSuccess={() => void refreshRef.current()} />
      <ICloudAliases resource={aliases} onClose={() => setAliases(null)} />
      <SupplierApplicationModal open={supplierOpen} onOpenChange={setSupplierOpen} onSuccess={() => undefined} />
    </div>
  );
}

function ICloudAliases({ resource, onClose }: { resource: ICloudResource | null; onClose: () => void }) {
  const { t } = useTranslation();
  const [page, setPage] = useState(1);
  const [list, setList] = useState<ICloudAliasList>();
  const [loading, setLoading] = useState(true);
  useEffect(() => { setPage(1); }, [resource?.id]);
  useEffect(() => {
    if (!resource) { setList(undefined); return; }
    const controller = new AbortController();
    setLoading(true);
    void listICloudAliases(resource.id, (page - 1) * 20, controller.signal).then((result) => {
      if (!controller.signal.aborted) setList(result);
    }).catch((error) => {
      if (!controller.signal.aborted) Toast.error(getIamErrorMessage(t, error, "Operation failed."));
    }).finally(() => { if (!controller.signal.aborted) setLoading(false); });
    return () => controller.abort();
  }, [resource?.id, page, t]);
  return <Modal visible={Boolean(resource)} title={`${t("Aliases")} · ${resource?.email ?? ""}`} onCancel={onClose} footer={null} width="min(720px, calc(100vw - 32px))">
    <CardTable loading={loading} rowKey="id" dataSource={list?.items ?? []} columns={[
      { title: t("Email"), render: (_, row) => <CopyableTableText copiedText={t("Copied")} text={row.email} /> },
      { title: t("Status"), render: (_, row) => t(row.status === "normal" ? "Normal" : row.status === "disabled" ? "Disabled" : row.status === "deleted" ? "Deleted" : "Missing") },
    ]} pagination={{ currentPage: page, pageSize: 20, total: list?.total ?? 0, onPageChange: setPage }} />
  </Modal>;
}
