import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Button, DatePicker, Dropdown, Empty, Input, Modal, Space, Tag, Toast } from "@douyinfe/semi-ui";
import { IconSearch } from "@douyinfe/semi-icons";
import { SlidersHorizontal } from "lucide-react";
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
import { applyICloudCommand, listICloudAliases, listICloudResources, type ICloudAliasList, type ICloudCommand, type ICloudListQuery, type ICloudResource, type ICloudResourceList } from "@/lib/icloud-api";
import { DATE_RANGE_DROPDOWN_CLASS, createDateRangePresets, createdFromISOString, createdToISOString, normalizeDateRangeValue, type DateRangeValue } from "./resources/date-range-filter";
import { ImportICloudEmailsModal } from "./resources/import-icloud-emails-modal";
import { renderStatusTag } from "./resources/resource-status-tag";
import { ensureSupplierRole, SupplierApplicationModal } from "./resources/supplier-application-modal";

const statuses = ["all", "normal", "pending", "validating", "abnormal", "disabled"] as const;
const statusLabels = { all: "All", normal: "Normal", pending: "Pending", validating: "Validating", abnormal: "Abnormal", disabled: "Disabled" };

export default function ICloudEmails() {
  const { t } = useTranslation();
  const { currentUser, refreshCurrentUser } = useAuth();
  const isMobile = useIsMobile();
  const [search, setSearch] = useState("");
  const [debouncedSearch, flushSearch] = useDebouncedValue(search);
  const [status, setStatus] = useState<(typeof statuses)[number]>("all");
  const [sale, setSale] = useState("all");
  const [dates, setDates] = useState<DateRangeValue>([]);
  const [activePage, setActivePage] = useState(1);
  const [pageSize, setPageSize] = useSharedPageSize();
  const [selectedKeys, setSelectedKeys] = useState<number[]>([]);
  const [facets, setFacets] = useState<ICloudResourceList["facets"]>();
  const [busy, setBusy] = useState(false);
  const [compact, setCompact] = useState(false);
  const [importOpen, setImportOpen] = useState(false);
  const [supplierOpen, setSupplierOpen] = useState(false);
  const [aliases, setAliases] = useState<ICloudResource | null>(null);
  const datePresets = useMemo(() => createDateRangePresets(t), [t]);
  const filter = useMemo<ICloudListQuery>(() => ({
    search: debouncedSearch.trim() || undefined,
    status: status === "all" ? undefined : status,
    forSale: sale === "all" ? undefined : sale === "yes",
    createdFrom: createdFromISOString(dates), createdTo: createdToISOString(dates),
  }), [debouncedSearch, status, sale, dates]);
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
    if (!loading && total > 0 && activePage > Math.ceil(total / pageSize)) setActivePage(Math.ceil(total / pageSize));
  }, [total, loading, activePage, pageSize]);
  const pending = loadedItems.some((item) => item.status === "pending" || item.status === "validating");
  useEffect(() => {
    if (!pending || busy) return;
    const timer = window.setInterval(() => void refresh(), 10_000);
    return () => window.clearInterval(timer);
  }, [pending, busy, refresh]);

  const run = async (command: ICloudCommand, resources: ICloudResource[]) => {
    if (busy) return;
    setBusy(true);
    try {
      const result = await applyICloudCommand(command, resources);
      const message = t("Apple resource action result", { affected: result.affected, skipped: result.skipped });
      if (result.skipped) Toast.warning(message); else Toast.success(message);
    } catch (error) {
      Toast.error(getIamErrorMessage(t, error, "Operation failed."));
    } finally {
      setSelectedKeys([]);
      await refreshRef.current();
      setBusy(false);
    }
  };
  const act = async (command: ICloudCommand, resources: ICloudResource[]) => {
    if (busy) return;
    if (!resources.length) { Toast.warning(t("Please select resources first.")); return; }
    try {
      if (command === "publish" && !(await ensureSupplierRole(currentUser?.role, refreshCurrentUser, () => setSupplierOpen(true)))) return;
      if (command === "validate") { await run(command, resources); return; }
      Modal.confirm({
        title: t(command === "delete" ? "Delete" : "Sell"),
        content: t(command === "delete" ? "Apple delete confirmation" : "Apple publish confirmation", { count: resources.length }),
        okText: t("Confirm"), cancelText: t("Cancel"),
        okButtonProps: { type: command === "delete" ? "danger" : "primary" },
        onOk: () => run(command, resources),
      });
    } catch (error) { Toast.error(getIamErrorMessage(t, error, "Operation failed.")); }
  };
  const selected = loadedItems.filter((item) => selectedKeys.includes(item.id));
  const reset = () => { setSearch(""); setStatus("all"); setSale("all"); setDates([]); };
  const columns = [
    { title: t("Email"), dataIndex: "email", width: 250, render: (_: unknown, row: ICloudResource) => <CopyableTableText copiedText={t("Copied")} text={row.email} /> },
    { title: t("Status"), width: 105, render: (_: unknown, row: ICloudResource) => renderStatusTag(row.status === "deleted" ? "disabled" : row.status, t, row.lastSafeError ?? undefined) },
    { title: t("Private"), width: 85, render: (_: unknown, row: ICloudResource) => <Tag color={row.forSale ? "blue" : "grey"}>{t(row.forSale ? "No" : "Yes")}</Tag> },
    { title: t("Aliases"), width: 85, render: (_: unknown, row: ICloudResource) => <Button theme="borderless" size="small" onClick={() => setAliases(row)} aria-label={`${t("Aliases")}: ${row.email}`}>{row.aliasCount}</Button> },
    ...(!compact ? [
      { title: t("Forwarding email"), width: 240, render: (_: unknown, row: ICloudResource) => row.selectedForwardTo ? <CopyableTableText copiedText={t("Copied")} text={row.selectedForwardTo} /> : "—" },
      { title: t("Expires at"), width: 170, render: (_: unknown, row: ICloudResource) => new Date(row.expireAt).toLocaleString() },
      { title: t("Created at"), width: 170, render: (_: unknown, row: ICloudResource) => new Date(row.createdAt).toLocaleString() },
    ] : []),
    { title: t("Actions"), width: 230, render: (_: unknown, row: ICloudResource) => <Space>
      <Button size="small" theme="borderless" disabled={busy || row.status === "disabled"} onClick={() => void act("validate", [row])}>{t("Validate")}</Button>
      <Button size="small" theme="borderless" disabled={busy || row.forSale || row.status !== "normal"} onClick={() => void act("publish", [row])}>{t("Sell")}</Button>
      <Button size="small" theme="borderless" type="danger" disabled={busy || row.forSale} onClick={() => void act("delete", [row])}>{t("Delete")}</Button>
    </Space> },
  ];
  return <div className="console-content-width py-5">
    <CardPro type="type3" t={t}
      descriptionArea={<div className="font-semibold">{t("Apple Emails")}</div>}
      actionsArea={<div className="flex flex-wrap items-center gap-2">
        <Button size="small" type="primary" onClick={() => setImportOpen(true)}>{t("Import")}</Button>
        <Button size="small" disabled={busy || !selected.length} onClick={() => void act("validate", selected)}>{t("Validate")}</Button>
        <Button size="small" disabled={busy || !selected.length} onClick={() => void act("publish", selected)}>{t("Sell")}</Button>
        <Button size="small" type="danger" disabled={busy || !selected.length} onClick={() => void act("delete", selected)}>{t("Delete")}</Button>
        <CompactModeToggle compactMode={compact} setCompactMode={setCompact} t={t} />
      </div>}
      searchArea={<div className="flex flex-wrap items-center gap-2">
        <Dropdown trigger="click" render={<div className="w-64 space-y-2 p-2">
          <div className="text-xs text-[var(--semi-color-text-2)]">{t("Status")}</div>
          {statuses.map((value) => <StatisticFilterOption key={value} active={status === value} label={t(statusLabels[value])} count={facets?.status[value] ?? 0} value={value} onSelect={setStatus} />)}
          <div className="pt-2 text-xs text-[var(--semi-color-text-2)]">{t("Private")}</div>
          {[{ value: "all", label: "All", count: facets?.forSale.all }, { value: "no", label: "Yes", count: facets?.forSale.no }, { value: "yes", label: "No", count: facets?.forSale.yes }].map((item) => <StatisticFilterOption key={item.value} active={sale === item.value} label={t(item.label)} count={item.count ?? 0} value={item.value} onSelect={setSale} />)}
        </div>}><Button size="small" icon={<SlidersHorizontal size={14} />}>{t("Filters")}</Button></Dropdown>
        <Input aria-label={t("Search email or suffix")} prefix={<IconSearch />} placeholder={t("Search email or suffix")} showClear size="small" value={search} onChange={setSearch} style={{ width: isMobile ? "100%" : 240 }} />
        <DatePicker type="dateTimeRange" size="small" format="yyyy-MM-dd HH:mm:ss" placeholder={[t("Start time"), t("End time")]} value={dates} showClear presets={datePresets} presetPosition="bottom" dropdownClassName={DATE_RANGE_DROPDOWN_CLASS} onChange={(value) => setDates(normalizeDateRangeValue(value))} style={{ width: isMobile ? "100%" : 380 }} />
        <Button size="small" loading={loading} onClick={() => { flushSearch(); void refresh(); }}>{t("Refresh")}</Button>
        <Button size="small" onClick={reset}>{t("Reset")}</Button>
      </div>}
      paginationArea={createCardProPagination({ currentPage: activePage, pageSize, total, isMobile, onPageChange: (page) => { setActivePage(page); setSelectedKeys([]); }, onPageSizeChange: setPageSize, t })}>
      <CardTable columns={columns} dataSource={pagedItems} loading={loading} rowKey="id" hidePagination className="overflow-hidden rounded-xl" scroll={{ x: "max(100%, 1080px)", y: DESKTOP_TABLE_SCROLL_Y }}
        empty={<Empty description={t("No email resources yet")} style={{ padding: 30 }} />}
        rowSelection={{ disabled: busy, selectedRowKeys: selectedKeys, onChange: (keys) => setSelectedKeys(keys.map(Number)) }} />
    </CardPro>
    {importOpen ? <ImportICloudEmailsModal onClose={() => setImportOpen(false)} onAccepted={() => void refresh()} /> : null}
    {aliases ? <ICloudAliases resource={aliases} onClose={() => setAliases(null)} /> : null}
    <SupplierApplicationModal open={supplierOpen} onOpenChange={setSupplierOpen} onSuccess={() => undefined} />
  </div>;
}

function ICloudAliases({ resource, onClose }: { resource: ICloudResource; onClose: () => void }) {
  const { t } = useTranslation();
  const [page, setPage] = useState(1);
  const [list, setList] = useState<ICloudAliasList>();
  const [loading, setLoading] = useState(true);
  useEffect(() => {
    const controller = new AbortController();
    setLoading(true);
    void listICloudAliases(resource.id, (page - 1) * 20, controller.signal).then((result) => {
      if (!controller.signal.aborted) setList(result);
    }).catch((error) => {
      if (!controller.signal.aborted) Toast.error(getIamErrorMessage(t, error, "Operation failed."));
    }).finally(() => { if (!controller.signal.aborted) setLoading(false); });
    return () => controller.abort();
  }, [resource.id, page, t]);
  return <Modal visible title={`${t("Aliases")} · ${resource.email}`} onCancel={onClose} footer={null} width="min(720px, calc(100vw - 32px))">
    <CardTable loading={loading} rowKey="id" dataSource={list?.items ?? []} columns={[
      { title: t("Email"), render: (_, row) => <CopyableTableText copiedText={t("Copied")} text={row.email} /> },
      { title: t("Status"), render: (_, row) => t(row.status === "normal" ? "Normal" : row.status === "disabled" ? "Disabled" : row.status === "deleted" ? "Deleted" : "Missing") },
    ]} pagination={{ currentPage: page, pageSize: 20, total: list?.total ?? 0, onPageChange: setPage }} />
  </Modal>;
}
