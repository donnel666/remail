import { useCallback, useEffect, useMemo, useState } from "react";
import {
  Button,
  DatePicker,
  Dropdown,
  Empty,
  Input,
  Modal,
  Space,
  Tabs,
  Tag,
  Tooltip,
  Toast,
} from "@douyinfe/semi-ui";
import { IconSearch } from "@douyinfe/semi-icons";
import {
  IllustrationNoResult,
  IllustrationNoResultDark,
} from "@douyinfe/semi-illustrations";
import { Globe, SlidersHorizontal } from "lucide-react";
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
import { useAuth } from "@/context/auth-provider";
import { useBlockPagedList } from "@/hooks/use-block-paged-list";
import { useDebouncedValue } from "@/hooks/use-debounced-value";
import { useIsMobile } from "@/hooks/use-is-mobile";
import { useSharedPageSize } from "@/hooks/use-shared-page-size";
import { getIamErrorMessage } from "@/lib/iam-errors";
import {
  deleteDomainResource,
  deleteDomainResourcesByFilter,
  deleteDomainResourcesBatch,
  getCurrentSupplierApplication,
  listOwnedDomainResources,
  publishDomainResource,
  publishDomainResourcesByFilter,
  publishDomainResourcesBatch,
  validateDomainResourcesBatch,
  validateDomainResourcesByFilter,
  type ResourceBulkFilter,
  type ResourceListResponse,
  type ResourceListFilter,
} from "@/lib/resources-api";

import { ImportDomainModal } from "./resources/domain-import-modal";
import {
  DATE_RANGE_DROPDOWN_CLASS,
  createDateRangePresets,
  createdFromISOString,
  createdToISOString,
  normalizeDateRangeValue,
  type DateRangeValue,
} from "./resources/date-range-filter";
import { SupplierApplicationModal } from "./resources/supplier-application-modal";
import {
  isDomainAvailable,
  toDomainResource,
  type DomainResource,
  type DomainStatus,
  type UsageScope,
} from "./resources/domain-model";
import { useSelectionNotification } from "./resources/use-selection-notification";

type StatusFilter = "all" | "normal" | "abnormal" | "disabled";
type BooleanFilter = "all" | "yes" | "no";

function hasSupplierRole(role?: string | null) {
  return role === "supplier" || role === "admin" || role === "super_admin";
}

function renderDomainStatusTag(
  status: DomainStatus,
  t: (key: string) => string,
  reason?: string
) {
  let color: "green" | "orange" | "grey";
  let label: string;

  if (isDomainAvailable(status)) {
    color = "green";
    label = t("Normal");
  } else if (status === "abnormal") {
    color = "orange";
    label = t("Abnormal");
  } else {
    color = "grey";
    label = t("Disabled");
  }

  const tag = (
    <Tag color={color} shape="circle" size="small">
      {label}
    </Tag>
  );

  if (status === "abnormal" && reason) {
    return (
      <span
        className="inline-flex"
        title={reason}
      >
        {tag}
      </span>
    );
  }

  return tag;
}

// ---------- Page component ----------

export default function DomainEmails() {
  const { t } = useTranslation();
  const { currentUser, refreshCurrentUser } = useAuth();
  const isMobile = useIsMobile();
  const [publishingBatch, setPublishingBatch] = useState(false);
  const [deletingBatch, setDeletingBatch] = useState(false);
  const [publishingResourceID, setPublishingResourceID] = useState<
    number | null
  >(null);
  const [deletingResourceID, setDeletingResourceID] = useState<number | null>(
    null
  );
  const [activeTld, setActiveTld] = useState("all");
  const [searchKeyword, setSearchKeyword] = useState("");
  const [createdAtRange, setCreatedAtRange] = useState<DateRangeValue>([]);
  const [statusFilter, setStatusFilter] = useState<StatusFilter>("all");
  const [privateFilter, setPrivateFilter] = useState<BooleanFilter>("all");
  const [compactMode, setCompactMode] = useState(false);
  const [importOpen, setImportOpen] = useState(false);
  const [supplierApplicationOpen, setSupplierApplicationOpen] = useState(false);
  const [selectedKeys, setSelectedKeys] = useState<number[]>([]);
  const [activePage, setActivePage] = useState(1);
  const [pageSize, setPageSize] = useSharedPageSize();
  const [resourceFacets, setResourceFacets] =
    useState<ResourceListResponse["facets"] | null>(null);
  const dateRangePresets = useMemo(() => createDateRangePresets(t), [t]);
  const canPublishForSale = hasSupplierRole(currentUser?.role);
  const [debouncedSearchKeyword, flushSearchKeyword] =
    useDebouncedValue(searchKeyword);

  const domainStatsFilter = useMemo<ResourceListFilter>(() => {
    const filter: ResourceListFilter = {};
    const search = debouncedSearchKeyword.trim();
    const createdFrom = createdFromISOString(createdAtRange);
    const createdTo = createdToISOString(createdAtRange);
    if (search) filter.search = search;
    if (statusFilter !== "all") filter.status = statusFilter;
    if (privateFilter !== "all") {
      filter.purpose = privateFilter === "yes" ? "not_sale" : "sale";
    }
    if (createdFrom) filter.createdFrom = createdFrom;
    if (createdTo) filter.createdTo = createdTo;
    return filter;
  }, [createdAtRange, debouncedSearchKeyword, privateFilter, statusFilter]);

  const domainListFilter = useMemo<ResourceListFilter>(() => {
    if (activeTld === "all") return domainStatsFilter;
    return { ...domainStatsFilter, tld: activeTld };
  }, [activeTld, domainStatsFilter]);

  const loadDomainBlock = useCallback(
    async (offset: number, limit: number) => {
      const response = await listOwnedDomainResources(
        domainListFilter,
        offset,
        limit
      );
      return {
        items: response.items
          .map(toDomainResource)
          .filter((item): item is DomainResource => item !== null),
        total: response.total,
      };
    },
    [domainListFilter]
  );

  const {
    loadedItems: items,
    loading,
    pagedItems,
    refresh: refreshList,
    total,
    updateLoadedItems,
  } = useBlockPagedList<DomainResource>({
    activePage,
    filterKey: JSON.stringify(domainListFilter),
    loadBlock: loadDomainBlock,
    onError: (error) => {
      Toast.error(getIamErrorMessage(t, error, "Domains load failed."));
    },
    pageSize,
  });

  const refreshStats = useCallback(async () => {
    try {
      const response = await listOwnedDomainResources(domainStatsFilter, 0, 1);
      setResourceFacets(response.facets ?? null);
    } catch {
      // Keep the previous tabs stable; the next refresh will retry stats.
    }
  }, [domainStatsFilter]);

  useEffect(() => {
    void refreshStats();
  }, [refreshStats]);

  const refresh = useCallback(async () => {
    await Promise.all([refreshStats(), refreshList()]);
  }, [refreshList, refreshStats]);

  const tldCounts = useMemo(
    () =>
      resourceFacets?.tlds?.map(
        (item) => [item.key, item.count] as [string, number]
      ) ?? [],
    [resourceFacets]
  );
  const tldSet = useMemo(
    () => new Set(tldCounts.map(([tld]) => tld)),
    [tldCounts]
  );

  const stats = useMemo(() => {
    if (resourceFacets) {
      return {
        status: resourceFacets.status,
        private: resourceFacets.private,
      };
    }
    return {
      status: {
        all: total,
        normal: 0,
        abnormal: 0,
        disabled: 0,
        pending: 0,
      },
      private: {
        all: total,
        yes: 0,
        no: 0,
      },
    };
  }, [resourceFacets, total]);

  const activeStatisticFilterCount =
    Number(statusFilter !== "all") + Number(privateFilter !== "all");

  useEffect(() => {
    if (resourceFacets && activeTld !== "all" && !tldSet.has(activeTld)) {
      setActiveTld("all");
    }
  }, [activeTld, resourceFacets, tldSet]);

  const domainBulkFilter = useMemo<ResourceBulkFilter>(() => {
    return { ...domainListFilter, resourceType: "domain" };
  }, [domainListFilter]);

  const selectedPrivateResourceIds = useMemo(() => {
    const selected = new Set(selectedKeys);
    return items
      .filter((item) => selected.has(item.id) && item.usageScope === "private")
      .map((item) => item.id);
  }, [items, selectedKeys]);

  const totalPages = Math.max(1, Math.ceil(total / pageSize));
  const safePage = Math.min(activePage, totalPages);

  useEffect(() => {
    if (safePage !== activePage) setActivePage(safePage);
  }, [activePage, safePage]);

  const selectTld = (tld: string) => {
    setActiveTld(tld);
    setActivePage(1);
    setSelectedKeys([]);
  };

  const resetFilters = () => {
    setSearchKeyword("");
    flushSearchKeyword("");
    setCreatedAtRange([]);
    setStatusFilter("all");
    setPrivateFilter("all");
    setActiveTld("all");
    setActivePage(1);
    setSelectedKeys([]);
  };

  const applyStatusFilter = (value: StatusFilter) => {
    setStatusFilter(value);
    setActivePage(1);
    setSelectedKeys([]);
  };

  const applyPrivateFilter = (value: BooleanFilter) => {
    setPrivateFilter(value);
    setActivePage(1);
    setSelectedKeys([]);
  };

  const clearSelection = useCallback(() => {
    setSelectedKeys([]);
  }, []);

  const handleCheckResource = useCallback(async (record: DomainResource) => {
    try {
      await validateDomainResourcesBatch([record.id]);
      Toast.success(t("Resource validation submitted."));
    } catch (error) {
      Toast.error(getIamErrorMessage(t, error, "Resource validation failed."));
    }
  }, [t]);

  const queueResourceChecks = useCallback(async (resourceIds: number[]) => {
    const ids = Array.from(new Set(resourceIds.filter((id) => id > 0)));
    if (ids.length === 0) {
      Toast.info(t("No resources to check."));
      return;
    }

    try {
      const response = await validateDomainResourcesBatch(ids);
      Toast.success(
        t("Resource validations submitted.", { count: response.queued })
      );
      setSelectedKeys([]);
    } catch (error) {
      Toast.error(getIamErrorMessage(t, error, "Resource validation failed."));
    }
  }, [t]);

  const queueFilteredResourceChecks = useCallback(async () => {
    if (total === 0) {
      Toast.info(t("No resources to check."));
      return;
    }

    try {
      const response = await validateDomainResourcesByFilter(domainBulkFilter);
      Toast.success(
        t("Resource validations submitted.", { count: response.queued })
      );
      setSelectedKeys([]);
    } catch (error) {
      Toast.error(getIamErrorMessage(t, error, "Resource validation failed."));
    }
  }, [domainBulkFilter, t, total]);

  const promptSupplierApplication = useCallback(async () => {
    try {
      const response = await getCurrentSupplierApplication();
      if (response.application?.status === "reviewing") {
        Toast.info(t("Supplier application is already under review."));
        return;
      }
      setSupplierApplicationOpen(true);
    } catch (error) {
      Toast.error(getIamErrorMessage(t, error, "Supplier application failed."));
    }
  }, [t]);

  const ensureCanPublishForSale = useCallback(async () => {
    if (canPublishForSale) return true;

    const latestUser = await refreshCurrentUser();
    if (hasSupplierRole(latestUser?.role)) return true;

    await promptSupplierApplication();
    return false;
  }, [canPublishForSale, promptSupplierApplication, refreshCurrentUser]);

  const markResourcesPublishedForSale = useCallback((resourceIds: number[]) => {
    const published = new Set(resourceIds);
    if (published.size === 0) return;
    updateLoadedItems((previous) =>
      previous.map((item) =>
        published.has(item.id) ? { ...item, usageScope: "public_sale" } : item
      )
    );
  }, [updateLoadedItems]);

  const publishResourceIds = useCallback(async (resourceIds: number[]) => {
    if (resourceIds.length === 0) {
      Toast.info(t("No private resources to publish."));
      return;
    }

    setPublishingBatch(true);
    try {
      const response = await publishDomainResourcesBatch(resourceIds);
      Toast.success(
        t("Resources published for sale.", { count: response.published })
      );
      markResourcesPublishedForSale(response.publishedResourceIds ?? []);
      setSelectedKeys([]);
    } catch (error) {
      Toast.error(getIamErrorMessage(t, error, "Publish failed."));
    } finally {
      setPublishingBatch(false);
    }
  }, [markResourcesPublishedForSale, t]);

  const confirmCheckAll = useCallback(() => {
    void queueFilteredResourceChecks();
  }, [queueFilteredResourceChecks]);

  const confirmSellAll = useCallback(async () => {
    if (privateFilter === "no") {
      Toast.info(t("No private resources to publish."));
      return;
    }

    if (!(await ensureCanPublishForSale())) return;

    Modal.confirm({
      title: t("Confirm sell all"),
      content: t("Confirm sell all matching content"),
      okText: t("Sell all"),
      cancelText: t("Cancel"),
      onOk: async () => {
        setPublishingBatch(true);
        try {
          const response = await publishDomainResourcesByFilter(domainBulkFilter);
          if (response.published === 0) {
            Toast.info(t("No private resources to publish."));
          } else {
            Toast.success(
              t("Resources published for sale.", { count: response.published })
            );
            setSelectedKeys([]);
            await refresh();
          }
        } catch (error) {
          Toast.error(getIamErrorMessage(t, error, "Publish failed."));
        } finally {
          setPublishingBatch(false);
        }
      },
    });
  }, [
    domainBulkFilter,
    ensureCanPublishForSale,
    privateFilter,
    refresh,
    t,
  ]);

  const confirmSellSelected = useCallback(async () => {
    if (selectedPrivateResourceIds.length === 0) {
      Toast.info(t("No private resources to publish."));
      return;
    }

    if (!(await ensureCanPublishForSale())) return;

    Modal.confirm({
      title: t("Confirm sell selected"),
      content: t("Confirm sell selected content", {
        count: selectedPrivateResourceIds.length,
      }),
      okText: t("Sell selected"),
      cancelText: t("Cancel"),
      onOk: () => publishResourceIds(selectedPrivateResourceIds),
    });
  }, [ensureCanPublishForSale, publishResourceIds, selectedPrivateResourceIds, t]);

  const deleteResourceIds = useCallback(async (resourceIds: number[]) => {
    if (resourceIds.length === 0) {
      Toast.info(t("No private resources to delete."));
      return;
    }

    setDeletingBatch(true);
    try {
      const response = await deleteDomainResourcesBatch(resourceIds);
      setSelectedKeys([]);
      await refresh();
      Toast.success(t("Resources deleted.", { count: response.deleted }));
    } catch (error) {
      Toast.error(getIamErrorMessage(t, error, "Delete failed."));
    } finally {
      setDeletingBatch(false);
    }
  }, [refresh, t]);

  const confirmDeleteSelected = useCallback(() => {
    if (selectedPrivateResourceIds.length === 0) {
      Toast.info(t("No private resources to delete."));
      return;
    }

    Modal.confirm({
      title: t("Confirm delete selected"),
      content: t("Confirm delete selected content", {
        count: selectedPrivateResourceIds.length,
      }),
      okText: t("Delete selected"),
      cancelText: t("Cancel"),
      onOk: () => deleteResourceIds(selectedPrivateResourceIds),
    });
  }, [deleteResourceIds, selectedPrivateResourceIds, t]);

  const confirmDeleteAll = useCallback(() => {
    if (privateFilter === "no") {
      Toast.info(t("No private resources to delete."));
      return;
    }

    Modal.confirm({
      title: t("Confirm delete all"),
      content: t("Confirm delete all matching content"),
      okText: t("Delete all"),
      cancelText: t("Cancel"),
      onOk: async () => {
        setDeletingBatch(true);
        try {
          const response = await deleteDomainResourcesByFilter(domainBulkFilter);
          if (response.deleted === 0) {
            Toast.info(t("No private resources to delete."));
          } else {
            setSelectedKeys([]);
            Toast.success(t("Resources deleted.", { count: response.deleted }));
            await refresh();
          }
        } catch (error) {
          Toast.error(getIamErrorMessage(t, error, "Delete failed."));
        } finally {
          setDeletingBatch(false);
        }
      },
    });
  }, [
    domainBulkFilter,
    privateFilter,
    refresh,
    t,
  ]);

  const handleSellResource = useCallback(async (record: DomainResource) => {
    if (record.usageScope !== "private") return;

    if (!(await ensureCanPublishForSale())) return;

    Modal.confirm({
      title: t("Confirm sell resource"),
      content: t("Confirm sell domain content", { domain: record.domain }),
      okText: t("Sell"),
      cancelText: t("Cancel"),
      onOk: async () => {
        setPublishingResourceID(record.id);
        try {
          await publishDomainResource(record.id);
          Toast.success(t("Resource published for sale."));
          markResourcesPublishedForSale([record.id]);
        } catch (error) {
          Toast.error(getIamErrorMessage(t, error, "Publish failed."));
        } finally {
          setPublishingResourceID(null);
        }
      },
    });
  }, [ensureCanPublishForSale, markResourcesPublishedForSale, t]);

  const handleDeleteResource = useCallback((record: DomainResource) => {
    if (record.usageScope !== "private") return;

    Modal.confirm({
      title: t("Confirm delete"),
      content: record.domain,
      okText: t("Delete"),
      cancelText: t("Cancel"),
      onOk: async () => {
        setDeletingResourceID(record.id);
        try {
          await deleteDomainResource(record.id);
          Toast.success(t("Resource deleted."));
          setSelectedKeys((previous) =>
            previous.filter((resourceID) => resourceID !== record.id)
          );
          await refresh();
        } catch (error) {
          Toast.error(getIamErrorMessage(t, error, "Delete failed."));
        } finally {
          setDeletingResourceID(null);
        }
      },
    });
  }, [refresh, t]);

  useSelectionNotification({
    selectedCount: selectedKeys.length,
    onCheck: () => void queueResourceChecks(selectedKeys),
    onClear: clearSelection,
    onDelete: confirmDeleteSelected,
    onSell: confirmSellSelected,
    deleteLoading: deletingBatch,
    sellLoading: publishingBatch,
    t,
  });

  const columns = useMemo(
    () =>
      [
        {
          key: "tld",
          title: t("TLD"),
          dataIndex: "domain",
          width: 110,
          render: (_: string, record: DomainResource) => (
            <Tag color="white" shape="circle">
              {record.domainTld}
            </Tag>
          ),
        },
        {
          key: "domain",
          title: t("Domain"),
          dataIndex: "domain",
          width: 280,
          render: (text: string) => (
            <CopyableTableText copiedText={t("Copied")} text={text} />
          ),
        },
        {
          title: t("Status"),
          dataIndex: "status",
          width: 120,
          render: (status: DomainStatus, record: DomainResource) =>
            renderDomainStatusTag(status, t, record.lastSafeError),
        },
        {
          title: t("Private only"),
          dataIndex: "usageScope",
          width: 120,
          render: (scope: UsageScope) => (
            <span
              className={`text-xs font-medium ${
                scope === "private"
                  ? "text-[var(--semi-color-primary)]"
                  : "text-[var(--semi-color-text-2)]"
              }`}
            >
              {scope === "private" ? t("Yes") : t("No")}
            </span>
          ),
        },
        {
          title: t("Mailboxes"),
          dataIndex: "mailboxCount",
          width: 110,
          render: (count: number) => (
            <span className="tabular-nums font-medium text-[var(--semi-color-text-1)]">
              {count}
            </span>
          ),
        },
        {
          title: t("Action"),
          dataIndex: "operate",
          width: 190,
          fixed: "right",
          render: (_: unknown, record: DomainResource) => (
            <Space spacing={4} wrap={false}>
              <Button
                disabled={record.usageScope !== "private"}
                loading={publishingResourceID === record.id}
                onClick={() => void handleSellResource(record)}
                type="tertiary"
                size="small"
              >
                {t("Sell")}
              </Button>
              <Button
                onClick={() => void handleCheckResource(record)}
                type="tertiary"
                size="small"
              >
                {t("Check")}
              </Button>
              <Button
                disabled={record.usageScope !== "private"}
                loading={deletingResourceID === record.id}
                onClick={() => handleDeleteResource(record)}
                type="danger"
                size="small"
              >
                {t("Delete")}
              </Button>
            </Space>
          ),
        },
      ] as any[],
    [
      deletingResourceID,
      handleDeleteResource,
      handleCheckResource,
      handleSellResource,
      publishingResourceID,
      t,
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
      activeKey={activeTld}
      type="card"
      collapsible
      onChange={(key) => selectTld(String(key))}
      className="mb-2"
    >
      <Tabs.TabPane
        itemKey="all"
        tab={
          <span className="flex items-center gap-2">
            {t("All")}
            <Tag color={activeTld === "all" ? "red" : "grey"} shape="circle">
              {stats.status.all}
            </Tag>
          </span>
        }
      />
      {tldCounts.map(([tld, count]) => (
        <Tabs.TabPane
          key={tld}
          itemKey={tld}
          tab={
            <span className="flex items-center gap-2">
              <Globe size={14} />
              {tld}
              <Tag color={activeTld === tld ? "red" : "grey"} shape="circle">
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
                  count={stats.status.all}
                  label={t("All")}
                  onSelect={applyStatusFilter}
                  value="all"
                />
                <StatisticFilterOption
                  active={statusFilter === "normal"}
                  count={stats.status.normal}
                  label={t("Normal")}
                  onSelect={applyStatusFilter}
                  value="normal"
                />
                <StatisticFilterOption
                  active={statusFilter === "abnormal"}
                  count={stats.status.abnormal}
                  label={t("Abnormal")}
                  onSelect={applyStatusFilter}
                  value="abnormal"
                />
                <StatisticFilterOption
                  active={statusFilter === "disabled"}
                  count={stats.status.disabled}
                  label={t("Disabled")}
                  onSelect={applyStatusFilter}
                  value="disabled"
                />
              </div>

              <div className="px-2 pb-1 text-xs font-medium text-[var(--semi-color-text-2)]">
                {t("Private")}
              </div>
              <div className="space-y-1">
                <StatisticFilterOption
                  active={privateFilter === "all"}
                  count={stats.private.all}
                  label={t("All")}
                  onSelect={applyPrivateFilter}
                  value="all"
                />
                <StatisticFilterOption
                  active={privateFilter === "yes"}
                  count={stats.private.yes}
                  label={t("Yes")}
                  onSelect={applyPrivateFilter}
                  value="yes"
                />
                <StatisticFilterOption
                  active={privateFilter === "no"}
                  count={stats.private.no}
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
          prefix={<IconSearch />}
          placeholder={t("Search domain or suffix")}
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
    onPageChange: (page) => setActivePage(page),
    onPageSizeChange: (size) => {
      setPageSize(size);
      setActivePage(1);
    },
    pageSize,
    total,
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
              description={t("No domain email resources yet")}
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
          scroll={{ x: "max(100%, 990px)", y: DESKTOP_TABLE_SCROLL_Y }}
          size="middle"
        />
      </CardPro>

      <ImportDomainModal
        open={importOpen}
        onOpenChange={setImportOpen}
        onSuccess={refresh}
      />
      <SupplierApplicationModal
        open={supplierApplicationOpen}
        onOpenChange={setSupplierApplicationOpen}
        onSuccess={() => {
          setSupplierApplicationOpen(false);
        }}
      />
    </div>
  );
}
