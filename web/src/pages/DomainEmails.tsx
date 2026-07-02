import { useCallback, useEffect, useMemo, useRef, useState } from "react";
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
  Toast,
  Typography,
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
import { CardTable } from "@/components/semi/card-table";
import { CompactModeToggle } from "@/components/semi/compact-mode-toggle";
import { useAuth } from "@/context/auth-provider";
import { useIsMobile } from "@/hooks/use-is-mobile";
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
  type ResourceBulkFilter,
} from "@/lib/resources-api";

import { ImportDomainModal } from "./resources/domain-import-modal";
import {
  createdFromISOString,
  createdToISOString,
  matchesCreatedAtRange,
  normalizeDateRangeValue,
  type DateRangeValue,
} from "./resources/date-range-filter";
import { SupplierApplicationModal } from "./resources/supplier-application-modal";
import {
  getTldCounts,
  isDomainAvailable,
  isDomainDisabled,
  toDomainResource,
  type DomainResource,
  type DomainStatus,
  type UsageScope,
} from "./resources/domain-model";
import { useSelectionNotification } from "./resources/use-selection-notification";

const { Text } = Typography;

type StatusFilter = "all" | "normal" | "abnormal" | "disabled";
type BooleanFilter = "all" | "yes" | "no";
const supplierRoleLevel = 20;

function hasSupplierRole(roleLevel?: number | null) {
  return (roleLevel ?? 0) >= supplierRoleLevel;
}

function matchesStatusFilter(status: DomainStatus, filter: StatusFilter) {
  if (filter === "all") return true;
  if (filter === "normal") return isDomainAvailable(status);
  if (filter === "abnormal") return status === "abnormal";
  return isDomainDisabled(status);
}

function matchesBooleanFilter(value: boolean, filter: BooleanFilter) {
  if (filter === "all") return true;
  return filter === "yes" ? value : !value;
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

interface StatisticFilterOptionProps<T extends string> {
  active: boolean;
  count: number;
  label: string;
  onSelect: (value: T) => void;
  value: T;
}

function StatisticFilterOption<T extends string>({
  active,
  count,
  label,
  onSelect,
  value,
}: StatisticFilterOptionProps<T>) {
  return (
    <button
      className={`flex w-full items-center justify-between rounded-[10px] px-2 py-1.5 text-left text-sm transition-colors ${
        active
          ? "bg-[var(--semi-color-primary-light-default)] text-[var(--semi-color-primary)]"
          : "text-[var(--semi-color-text-1)] hover:bg-[var(--semi-color-fill-0)]"
      }`}
      onClick={() => onSelect(value)}
      type="button"
    >
      <span>{label}</span>
      <Tag color={active ? "orange" : "grey"} shape="circle" size="small">
        {count}
      </Tag>
    </button>
  );
}

// ---------- Page component ----------

export default function DomainEmails() {
  const { t } = useTranslation();
  const { currentUser, refreshCurrentUser } = useAuth();
  const isMobile = useIsMobile();
  const [items, setItems] = useState<DomainResource[]>([]);
  const [loading, setLoading] = useState(false);
  const [publishingBatch, setPublishingBatch] = useState(false);
  const [deletingBatch, setDeletingBatch] = useState(false);
  const [publishingResourceID, setPublishingResourceID] = useState<number | null>(null);
  const [deletingResourceID, setDeletingResourceID] = useState<number | null>(null);
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
  const [pageSize, setPageSize] = useState(10);
  const refreshSeqRef = useRef(0);
  const locallyDeletedResourceIDsRef = useRef(new Set<number>());
  const canPublishForSale = hasSupplierRole(currentUser?.roleLevel);

  const refresh = useCallback(async () => {
    const refreshSeq = refreshSeqRef.current + 1;
    refreshSeqRef.current = refreshSeq;
    const isCurrentRefresh = () => refreshSeqRef.current === refreshSeq;

    locallyDeletedResourceIDsRef.current.clear();
    setLoading(true);
    setItems([]);
    setSelectedKeys([]);
    try {
      let hasRenderedFirstPage = false;
      await listOwnedDomainResources({
        onPage: (pageItems) => {
          if (!isCurrentRefresh()) return;
          const mapped = pageItems
            .map(toDomainResource)
            .filter((item): item is DomainResource => item !== null)
            .filter(
              (item) => !locallyDeletedResourceIDsRef.current.has(item.id)
            );
          if (mapped.length === 0) return;
          setItems((previous) => [...previous, ...mapped]);
          if (!hasRenderedFirstPage) {
            hasRenderedFirstPage = true;
            setLoading(false);
          }
        },
      });
    } catch (error) {
      if (isCurrentRefresh()) {
        Toast.error(getIamErrorMessage(t, error, "Domains load failed."));
      }
    } finally {
      if (isCurrentRefresh()) {
        setLoading(false);
      }
    }
  }, [t]);

  const invalidateRefresh = useCallback(() => {
    refreshSeqRef.current += 1;
  }, []);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  const tldCounts = useMemo(() => getTldCounts(items), [items]);
  const tldSet = useMemo(
    () => new Set(tldCounts.map(([tld]) => tld)),
    [tldCounts]
  );

  const stats = useMemo(
    () => ({
      status: {
        all: items.length,
        normal: items.filter((i) => isDomainAvailable(i.status)).length,
        abnormal: items.filter((i) => i.status === "abnormal").length,
        disabled: items.filter((i) => isDomainDisabled(i.status)).length,
      },
      private: {
        all: items.length,
        yes: items.filter((i) => i.usageScope === "private").length,
        no: items.filter((i) => i.usageScope !== "private").length,
      },
    }),
    [items]
  );

  const activeStatisticFilterCount =
    Number(statusFilter !== "all") + Number(privateFilter !== "all");

  useEffect(() => {
    if (activeTld !== "all" && !tldSet.has(activeTld)) {
      setActiveTld("all");
    }
  }, [activeTld, tldSet]);

  const matchesCurrentFilters = useCallback((item: DomainResource) => {
    const keyword = searchKeyword.trim().toLowerCase();
    const tldMatched = activeTld === "all" || item.domainTld === activeTld;
    const statusMatched = matchesStatusFilter(item.status, statusFilter);
    const privateMatched = matchesBooleanFilter(
      item.usageScope === "private",
      privateFilter
    );
    const keywordMatched =
      keyword.length === 0 ||
      item.domain.toLowerCase().includes(keyword) ||
      item.domainTld.includes(keyword);
    const createdAtMatched = matchesCreatedAtRange(
      item.createdAt,
      createdAtRange
    );

    return (
      tldMatched &&
      statusMatched &&
      privateMatched &&
      keywordMatched &&
      createdAtMatched
    );
  }, [activeTld, createdAtRange, privateFilter, searchKeyword, statusFilter]);

  const filteredItems = useMemo(
    () => items.filter(matchesCurrentFilters),
    [items, matchesCurrentFilters]
  );

  const domainBulkFilter = useMemo<ResourceBulkFilter>(() => {
    const filter: ResourceBulkFilter = { resourceType: "domain" };
    const search = searchKeyword.trim();
    const createdFrom = createdFromISOString(createdAtRange);
    const createdTo = createdToISOString(createdAtRange);
    if (search) filter.search = search;
    if (activeTld !== "all") filter.tld = activeTld;
    if (statusFilter !== "all") filter.status = statusFilter;
    if (createdFrom) filter.createdFrom = createdFrom;
    if (createdTo) filter.createdTo = createdTo;
    return filter;
  }, [activeTld, createdAtRange, searchKeyword, statusFilter]);

  const selectedPrivateResourceIds = useMemo(() => {
    const selected = new Set(selectedKeys);
    return items
      .filter((item) => selected.has(item.id) && item.usageScope === "private")
      .map((item) => item.id);
  }, [items, selectedKeys]);

  const totalPages = Math.max(1, Math.ceil(filteredItems.length / pageSize));
  const safePage = Math.min(activePage, totalPages);
  const pagedItems = filteredItems.slice(
    (safePage - 1) * pageSize,
    safePage * pageSize
  );

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

  const showNotImplemented = useCallback(() => {
    Toast.info(t("Feature is not implemented yet."));
  }, [t]);

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
    if (hasSupplierRole(latestUser?.roleLevel)) return true;

    await promptSupplierApplication();
    return false;
  }, [canPublishForSale, promptSupplierApplication, refreshCurrentUser]);

  const markResourcesPublishedForSale = useCallback((resourceIds: number[]) => {
    const published = new Set(resourceIds);
    if (published.size === 0) return;
    setItems((previous) =>
      previous.map((item) =>
        published.has(item.id) ? { ...item, usageScope: "public_sale" } : item
      )
    );
  }, []);

  const markResourcesPublishedByPredicate = useCallback(
    (predicate: (item: DomainResource) => boolean) => {
      setItems((previous) =>
        previous.map((item) =>
          predicate(item) ? { ...item, usageScope: "public_sale" } : item
        )
      );
    },
    []
  );

  const removeResource = useCallback((resourceId: number) => {
    locallyDeletedResourceIDsRef.current.add(resourceId);
    setItems((previous) => previous.filter((item) => item.id !== resourceId));
  }, []);

  const removeResourcesByPredicate = useCallback(
    (predicate: (item: DomainResource) => boolean) => {
      setItems((previous) => {
        const next: DomainResource[] = [];
        for (const item of previous) {
          if (predicate(item)) {
            locallyDeletedResourceIDsRef.current.add(item.id);
            continue;
          }
          next.push(item);
        }
        return next;
      });
    },
    []
  );

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

  const confirmCheckAll = () => {
    showNotImplemented();
  };

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
            invalidateRefresh();
            markResourcesPublishedByPredicate(
              (item) => item.usageScope === "private" && matchesCurrentFilters(item)
            );
            setSelectedKeys([]);
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
    invalidateRefresh,
    markResourcesPublishedByPredicate,
    matchesCurrentFilters,
    privateFilter,
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
      const response = await deleteDomainResourcesBatch(resourceIds, {
        onDeleted: (resourceId) => {
          removeResource(resourceId);
          setSelectedKeys((previous) =>
            previous.filter((selectedId) => selectedId !== resourceId)
          );
        },
      });
      Toast.success(t("Resources deleted.", { count: response.deleted }));
    } catch (error) {
      Toast.error(getIamErrorMessage(t, error, "Delete failed."));
    } finally {
      setDeletingBatch(false);
    }
  }, [removeResource, t]);

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
            removeResourcesByPredicate(
              (item) => item.usageScope === "private" && matchesCurrentFilters(item)
            );
            invalidateRefresh();
            setSelectedKeys([]);
            Toast.success(t("Resources deleted.", { count: response.deleted }));
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
    invalidateRefresh,
    matchesCurrentFilters,
    privateFilter,
    removeResourcesByPredicate,
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
          removeResource(record.id);
          setSelectedKeys((previous) =>
            previous.filter((resourceID) => resourceID !== record.id)
          );
        } catch (error) {
          Toast.error(getIamErrorMessage(t, error, "Delete failed."));
        } finally {
          setDeletingResourceID(null);
        }
      },
    });
  }, [removeResource, t]);

  useSelectionNotification({
    selectedCount: selectedKeys.length,
    onCheck: showNotImplemented,
    onClear: () => setSelectedKeys([]),
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
          width: 100,
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
          width: 260,
          render: (text: string) => (
            <Text
              copyable={{
                content: text,
                onCopy: () => Toast.success(t("Copied")),
              }}
            >
              {text}
            </Text>
          ),
        },
        {
          title: t("Status"),
          dataIndex: "status",
          width: 130,
          render: (status: DomainStatus) => renderDomainStatusTag(status, t),
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
          width: 250,
          fixed: "right",
          render: (_: unknown, record: DomainResource) => (
            <Space wrap={false}>
              {record.usageScope === "private" ? (
                <Button
                  loading={publishingResourceID === record.id}
                  onClick={() => void handleSellResource(record)}
                  type="tertiary"
                  size="small"
                >
                  {t("Sell")}
                </Button>
              ) : null}
              <Button onClick={showNotImplemented} type="tertiary" size="small">
                {t("Check")}
              </Button>
              {record.usageScope === "private" ? (
                <Button
                  loading={deletingResourceID === record.id}
                  onClick={() => handleDeleteResource(record)}
                  type="danger"
                  size="small"
                >
                  {t("Delete")}
                </Button>
              ) : null}
            </Space>
          ),
        },
      ] as any[],
    [
      deletingResourceID,
      handleDeleteResource,
      handleSellResource,
      publishingResourceID,
      showNotImplemented,
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
              {items.length}
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
          className="flex-1 md:flex-initial"
          loading={loading}
          onClick={refresh}
        >
          {t("Refresh")}
        </Button>
        <Button
          type="tertiary"
          size="small"
          className="flex-1 md:flex-initial"
          onClick={confirmCheckAll}
        >
          {t("Check all")}
        </Button>
        <Button
          type="tertiary"
          size="small"
          className="flex-1 md:flex-initial"
          loading={publishingBatch}
          onClick={() => void confirmSellAll()}
        >
          {t("Sell all")}
        </Button>
        <Button
          type="danger"
          size="small"
          className="flex-1 md:flex-initial"
          loading={deletingBatch}
          onClick={confirmDeleteAll}
        >
          {t("Delete all")}
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
          onChange={(value) => setSearchKeyword(String(value))}
          className="resources-search-input w-full md:w-56"
        />
        <DatePicker
          type="dateTimeRange"
          format="yyyy-MM-dd HH:mm:ss"
          placeholder={[t("Start time"), t("End time")]}
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
            className="flex-1 md:flex-initial"
            onClick={() => setActivePage(1)}
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
    total: filteredItems.length,
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
          scroll={{ x: "max-content" }}
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
