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
  Tooltip,
  Toast,
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
import { useAuth } from "@/context/auth-provider";
import { useBlockPagedList } from "@/hooks/use-block-paged-list";
import { useDebouncedValue } from "@/hooks/use-debounced-value";
import { useIsMobile } from "@/hooks/use-is-mobile";
import { useSharedPageSize } from "@/hooks/use-shared-page-size";
import { getIamErrorMessage } from "@/lib/iam-errors";
import { IamApiError } from "@/lib/api-client";
import {
  deleteProtoResource,
  deleteProtoResourcesByFilter,
  deleteProtoResourcesBatch,
  listOwnedProtoResources,
  publishProtoResource,
  publishProtoResourcesByFilter,
  publishProtoResourcesBatch,
  validateResource,
  validateProtoResourcesBatch,
  validateProtoResourcesByFilter,
  type ResourceBulkFilter,
  type ResourceListResponse,
  type ResourceListFilter,
} from "@/lib/proto-api";

import { ImportProtoEmailsModal } from "./resources/import-proto-emails-modal";
import {
  DATE_RANGE_DROPDOWN_CLASS,
  createDateRangePresets,
  createdFromISOString,
  createdToISOString,
  normalizeDateRangeValue,
  type DateRangeValue,
} from "./resources/date-range-filter";
import {
  getSuffix,
  type EmailResource,
  type LifetimeType,
  type ResourceStatus,
  type UsageScope,
  toEmailResource,
} from "./resources/proto-model";
import { renderStatusTag } from "./resources/proto-resource-status-tag";
import {
  ensureSupplierRole,
  SupplierApplicationModal,
} from "./resources/supplier-application-modal";
import { ProtoBulkTaskProgress } from "./resources/proto-bulk-task-progress";
import { useSelectionNotification } from "./resources/use-selection-notification";

type StatusFilter =
  | "all"
  | "pending"
  | "validating"
  | "identifying"
  | "normal"
  | "abnormal"
  | "disabled";
type BooleanFilter = "all" | "yes" | "no";

function isEmailResource(item: EmailResource | null): item is EmailResource {
  return item !== null;
}

export default function ProtoEmails() {
  const { t } = useTranslation();
  const { currentUser, refreshCurrentUser } = useAuth();
  const isMobile = useIsMobile();
  const [bulkTaskId, setBulkTaskId] = useState<string | null>(null);
  const validationControllersRef = useRef(new Set<AbortController>());
  const statsRequestRef = useRef<AbortController | null>(null);

  useEffect(() => {
    const controllers = validationControllersRef.current;
    return () => {
      for (const controller of controllers) controller.abort();
      controllers.clear();
    };
  }, []);
  const [activeSuffix, setActiveSuffix] = useState("all");
  const [searchKeyword, setSearchKeyword] = useState("");
  const [createdAtRange, setCreatedAtRange] = useState<DateRangeValue>([]);
  const [statusFilter, setStatusFilter] = useState<StatusFilter>("all");
  const [privateFilter, setPrivateFilter] = useState<BooleanFilter>("all");
  const [longLivedFilter, setLongLivedFilter] =
    useState<BooleanFilter>("all");
  const [compactMode, setCompactMode] = useState(false);
  const [importOpen, setImportOpen] = useState(false);
  const [supplierApplicationOpen, setSupplierApplicationOpen] = useState(false);
  const [selectedKeys, setSelectedKeys] = useState<number[]>([]);
  const [activePage, setActivePage] = useState(1);
  const [pageSize, setPageSize] = useSharedPageSize();

  useEffect(() => setActivePage(1), [pageSize]);
  const [resourceFacets, setResourceFacets] =
    useState<ResourceListResponse["facets"] | null>(null);
  const [publishingResourceID, setPublishingResourceID] = useState<number | null>(
    null
  );
  const [deletingResourceID, setDeletingResourceID] = useState<number | null>(
    null
  );
  const [publishingBatch, setPublishingBatch] = useState(false);
  const [deletingBatch, setDeletingBatch] = useState(false);
  const dateRangePresets = useMemo(() => createDateRangePresets(t), [t]);
  const [debouncedSearchKeyword, flushSearchKeyword] =
    useDebouncedValue(searchKeyword);

  const protoStatsFilter = useMemo<ResourceListFilter>(() => {
    const filter: ResourceListFilter = {};
    const search = debouncedSearchKeyword.trim();
    const createdFrom = createdFromISOString(createdAtRange);
    const createdTo = createdToISOString(createdAtRange);
    if (search) filter.search = search;
    if (statusFilter !== "all") filter.status = statusFilter;
    if (privateFilter !== "all") filter.forSale = privateFilter === "no";
    if (longLivedFilter !== "all") {
      filter.longLived = longLivedFilter === "yes";
    }
    if (createdFrom) filter.createdFrom = createdFrom;
    if (createdTo) filter.createdTo = createdTo;
    return filter;
  }, [
    createdAtRange,
    debouncedSearchKeyword,
    longLivedFilter,
    privateFilter,
    statusFilter,
  ]);

  const protoListFilter = useMemo<ResourceListFilter>(() => {
    if (activeSuffix === "all") return protoStatsFilter;
    return { ...protoStatsFilter, suffix: activeSuffix };
  }, [activeSuffix, protoStatsFilter]);

  const loadProtoBlock = useCallback(
    async (
      offset: number,
      limit: number,
      cursor: { afterId?: number } | undefined,
      signal: AbortSignal
    ) => {
      const response = await listOwnedProtoResources(
        protoListFilter,
        offset,
        limit,
        cursor?.afterId,
        { includeFacets: false, includeTotal: false, signal }
      );
      return {
        items: response.items.map(toEmailResource).filter(isEmailResource),
        nextAfterId: response.nextAfterId,
        total: response.total,
      };
    },
    [protoListFilter]
  );

  const {
    loadedItems: items,
    loading,
    pagedItems,
    refresh: refreshList,
    setTotal: setListTotal,
    total,
  } = useBlockPagedList<EmailResource>({
    activePage,
    blockSize: 100,
    filterKey: JSON.stringify(protoListFilter),
    loadBlock: loadProtoBlock,
    onError: (error) => {
      Toast.error(getIamErrorMessage(t, error, "Resources load failed."));
    },
    pageSize,
  });

  const refreshStats = useCallback(async () => {
    statsRequestRef.current?.abort();
    const controller = new AbortController();
    statsRequestRef.current = controller;
    try {
      const response = await listOwnedProtoResources(
        protoListFilter,
        0,
        1,
        undefined,
        { includeFacets: true, signal: controller.signal }
      );
      if (controller.signal.aborted) return;
      setResourceFacets(response.facets ?? null);
      if (response.total !== undefined) setListTotal(response.total);
    } catch {
      // The next refresh retries stats; aborted and failed requests never replace current data.
    } finally {
      if (statsRequestRef.current === controller) statsRequestRef.current = null;
    }
  }, [protoListFilter, setListTotal]);

  useEffect(() => {
    setResourceFacets(null);
    void refreshStats();
    return () => {
      statsRequestRef.current?.abort();
      statsRequestRef.current = null;
    };
  }, [refreshStats]);

  const refresh = useCallback(async () => {
    await Promise.all([refreshStats(), refreshList()]);
  }, [refreshList, refreshStats]);

  const suffixCounts = useMemo(
    () =>
      resourceFacets?.suffixes?.map(
        (item) => [item.key, item.count] as [string, number]
      ) ?? [],
    [resourceFacets]
  );
  const suffixSet = useMemo(
    () => new Set(suffixCounts.map(([suffix]) => suffix)),
    [suffixCounts]
  );

  const resourceStats = useMemo(() => {
    if (resourceFacets) {
      return {
        longLived: resourceFacets.longLived,
        private: { all: resourceFacets.forSale.all, yes: resourceFacets.forSale.no, no: resourceFacets.forSale.yes },
        status: resourceFacets.status,
      };
    }
    return {
      longLived: {
        all: total,
        no: 0,
        yes: 0,
      },
      private: {
        all: total,
        no: 0,
        yes: 0,
      },
      status: {
        all: total,
        abnormal: 0,
        disabled: 0,
        normal: 0,
        pending: 0,
        validating: 0,
        identifying: 0,
      },
    };
  }, [resourceFacets, total]);

  const activeStatisticFilterCount =
    Number(statusFilter !== "all") +
    Number(privateFilter !== "all") +
    Number(longLivedFilter !== "all");

  useEffect(() => {
    if (
      resourceFacets &&
      activeSuffix !== "all" &&
      !suffixSet.has(activeSuffix)
    ) {
      setActiveSuffix("all");
    }
  }, [activeSuffix, resourceFacets, suffixSet]);

  const protoBulkFilter = useMemo<ResourceBulkFilter>(() => {
    return { ...protoListFilter };
  }, [protoListFilter]);

  const totalPages = Math.max(1, Math.ceil(total / pageSize));
  const safePage = Math.min(activePage, totalPages);

  useEffect(() => {
    if (safePage !== activePage) setActivePage(safePage);
  }, [activePage, safePage]);

  const selectSuffix = (suffix: string) => {
    setActiveSuffix(suffix);
    setActivePage(1);
    setSelectedKeys([]);
  };

  const resetFilters = () => {
    setSearchKeyword("");
    flushSearchKeyword("");
    setCreatedAtRange([]);
    setStatusFilter("all");
    setPrivateFilter("all");
    setLongLivedFilter("all");
    setActiveSuffix("all");
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

  const applyLongLivedFilter = (value: BooleanFilter) => {
    setLongLivedFilter(value);
    setActivePage(1);
    setSelectedKeys([]);
  };

  const handleCheckResource = useCallback(async (record: EmailResource) => {
    const controller = new AbortController();
    validationControllersRef.current.add(controller);
    try {
      await validateResource(record.id, record.version, controller.signal);
      Toast.success(t("Resource validation submitted."));
      await refresh();
    } catch (error) {
      if (error instanceof DOMException && error.name === "AbortError") return;
      Toast.error(getIamErrorMessage(t, error, "Resource validation failed."));
      if (error instanceof IamApiError && error.code === "resource_version_conflict") await refresh();
    } finally {
      validationControllersRef.current.delete(controller);
    }
  }, [refresh, t]);

  const queueResourceChecks = useCallback(async (resourceIds: number[]) => {
    const ids = Array.from(new Set(resourceIds.filter((id) => id > 0)));
    if (ids.length === 0) {
      Toast.info(t("No resources to check."));
      return;
    }

    try {
      const response = await validateProtoResourcesBatch(ids);
          if (response.taskId) setBulkTaskId(response.taskId);
      Toast.success(
        t("Resource validations submitted.", { count: response.requested })
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
      const response = await validateProtoResourcesByFilter(protoBulkFilter);
      if (response.taskId) setBulkTaskId(response.taskId);
      Toast.success(t("Resource validation submitted."));
      setSelectedKeys([]);
    } catch (error) {
      Toast.error(getIamErrorMessage(t, error, "Resource validation failed."));
    }
  }, [protoBulkFilter, t, total]);

  const ensureCanPublishForSale = useCallback(
    () =>
      ensureSupplierRole(currentUser?.role, refreshCurrentUser, () => {
        setSupplierApplicationOpen(true);
      }),
    [currentUser?.role, refreshCurrentUser]
  );

  const handleSellResource = useCallback(async (record: EmailResource) => {
    if (record.forSale) return;

    if (!(await ensureCanPublishForSale())) return;

    Modal.confirm({
      title: t("Confirm sell resource"),
      content: t("Confirm sell resource content", {
        email: record.emailAddress,
      }),
      okText: t("Sell"),
      cancelText: t("Cancel"),
      onOk: async () => {
        setPublishingResourceID(record.id);
        try {
          await publishProtoResource(record.id, record.version);
          Toast.success(t("Resource published for sale."));
          await refresh();
        } catch (error) {
          Toast.error(getIamErrorMessage(t, error, "Publish failed."));
          if (error instanceof IamApiError && error.code === "resource_version_conflict") await refresh();
        } finally {
          setPublishingResourceID(null);
        }
      },
    });
  }, [ensureCanPublishForSale, t, refresh]);

  const selectedPrivateResourceIds = useMemo(() => {
    const selectedIDSet = new Set(selectedKeys);
    return items
      .filter(
        (item) =>
          selectedIDSet.has(item.id) && item.usageScope === "private"
      )
      .map((item) => item.id);
  }, [items, selectedKeys]);

  const clearSelection = useCallback(() => {
    setSelectedKeys([]);
  }, []);

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
          const response = await publishProtoResourcesByFilter(
            protoBulkFilter
          );
          if (response.taskId) setBulkTaskId(response.taskId);
          if (response.taskId) {
            Toast.success(t("Proto resources bulk operation submitted."));
            setSelectedKeys([]);
            await refresh();
          } else if (response.affected === 0) {
            Toast.info(t("No private resources to publish."));
          } else {
            Toast.success(
              response.taskId ? t("Proto resources bulk operation submitted.") : t("Resources published for sale.", { count: response.affected })
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
    ensureCanPublishForSale,
    protoBulkFilter,
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
      onOk: async () => {
        setPublishingBatch(true);
        try {
          const response = await publishProtoResourcesBatch(
            selectedPrivateResourceIds
          );
          if (response.taskId) setBulkTaskId(response.taskId);
          Toast.success(
            response.taskId ? t("Proto resources bulk operation submitted.") : t("Resources published for sale.", { count: response.affected })
          );
          setSelectedKeys([]);
          await refresh();
        } catch (error) {
          Toast.error(getIamErrorMessage(t, error, "Publish failed."));
        } finally {
          setPublishingBatch(false);
        }
      },
    });
  }, [
    ensureCanPublishForSale,
    selectedPrivateResourceIds,
    t,
    refresh,
  ]);

  const sellSelected = useCallback(() => {
    void confirmSellSelected();
  }, [confirmSellSelected]);

  const deleteResourceIds = useCallback(async (resourceIds: number[]) => {
    if (resourceIds.length === 0) {
      Toast.info(t("No private resources to delete."));
      return;
    }

    setDeletingBatch(true);
    try {
      const response = await deleteProtoResourcesBatch(resourceIds);
          if (response.taskId) setBulkTaskId(response.taskId);
      setSelectedKeys([]);
      await refresh();
      Toast.success(response.taskId ? t("Proto resources bulk operation submitted.") : t("Resources deleted.", { count: response.affected }));
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
          const response = await deleteProtoResourcesByFilter(
            protoBulkFilter
          );
          if (response.taskId) setBulkTaskId(response.taskId);
          if (response.taskId) {
            Toast.success(t("Proto resources bulk operation submitted."));
            setSelectedKeys([]);
            await refresh();
          } else if (response.affected === 0) {
            Toast.info(t("No private resources to delete."));
          } else {
            setSelectedKeys([]);
            Toast.success(t("Resources deleted.", { count: response.affected }));
            await refresh();
          }
        } catch (error) {
          Toast.error(getIamErrorMessage(t, error, "Delete failed."));
          if (error instanceof IamApiError && error.code === "resource_version_conflict") await refresh();
        } finally {
          setDeletingBatch(false);
        }
      },
    });
  }, [
    protoBulkFilter,
    privateFilter,
    refresh,
    t,
  ]);

  const deleteSelected = useCallback(() => {
    confirmDeleteSelected();
  }, [confirmDeleteSelected]);

  const handleImportSuccess = useCallback(async () => {
    setActivePage(1);
    setSelectedKeys([]);
    await refresh();
  }, [refresh]);

  const handleDeleteResource = useCallback((record: EmailResource) => {
    if (record.forSale) return;

    Modal.confirm({
      title: t("Confirm delete"),
      content: record.emailAddress,
      okText: t("Delete"),
      cancelText: t("Cancel"),
      onOk: async () => {
        setDeletingResourceID(record.id);
        try {
          await deleteProtoResource(record.id, record.version);
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
    onDelete: deleteSelected,
    onSell: sellSelected,
    deleteLoading: deletingBatch,
    sellLoading: publishingBatch,
    t,
  });

  const columns = useMemo(
    () =>
      [
        {
          key: "suffix",
          title: t("Suffix"),
          dataIndex: "emailAddress",
          width: 120,
          render: (_: string, record: EmailResource) => (
            <Tag color="white" shape="circle">
              {getSuffix(record.emailAddress)}
            </Tag>
          ),
        },
        {
          key: "email",
          title: t("Email"),
          dataIndex: "emailAddress",
          width: 280,
          render: (text: string) => (
            <CopyableTableText copiedText={t("Copied")} text={text} />
          ),
        },
        {
          key: "status",
          title: t("Status"),
          dataIndex: "status",
          width: 120,
          render: (status: ResourceStatus, record: EmailResource) =>
            renderStatusTag(status, t, record.lastSafeError),
        },
        {
          key: "private",
          title: t("Private"),
          dataIndex: "usageScope",
          width: 100,
          render: (scope: UsageScope) => (
            <Tag color={scope === "private" ? "green" : "grey"} shape="circle">
              {scope === "private" ? t("Yes") : t("No")}
            </Tag>
          ),
        },
        {
          key: "longLived",
          title: t("Long-lived"),
          dataIndex: "lifetimeType",
          width: 120,
          render: (value: LifetimeType) => (
            <Tag
              color={value === "long_lived" ? "green" : "grey"}
              shape="circle"
            >
              {value === "long_lived" ? t("Yes") : t("No")}
            </Tag>
          ),
        },
        {
          key: "operate",
          title: t("Action"),
          dataIndex: "operate",
          width: 190,
          fixed: "right",
          render: (_: unknown, record: EmailResource) => (
            <Space spacing={4} wrap={false}>
              <Button
                type="tertiary"
                size="small"
                onClick={() => void handleCheckResource(record)}
              >
                {t("Check")}
              </Button>
              <Button
                disabled={record.forSale}
                loading={publishingResourceID === record.id}
                onClick={() => void handleSellResource(record)}
                type="tertiary"
                size="small"
              >
                {t("Sell")}
              </Button>
              <Button
                disabled={record.forSale}
                loading={deletingResourceID === record.id}
                type="danger"
                size="small"
                onClick={() => handleDeleteResource(record)}
              >
                {t("Delete")}
              </Button>
            </Space>
          ),
        },
      ] as any[],
    [
      deletingResourceID,
      handleCheckResource,
      handleSellResource,
      handleDeleteResource,
      publishingResourceID,
      t,
    ]
  );

  const rowSelection = {
    selectedRowKeys: selectedKeys,
    onChange: (keys: Array<string | number>) => {
      setSelectedKeys(keys.map((key) => Number(key)));
    },
  };

  const tableColumns = useMemo(() => {
    if (!compactMode) return columns;
    return columns.map((column) => {
      if (column.dataIndex !== "operate") return column;
      const { fixed: _fixed, ...rest } = column;
      return rest;
    });
  }, [columns, compactMode]);

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
              {resourceStats.status.all}
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
                  active={statusFilter === "identifying"}
                  count={resourceStats.status.identifying}
                  label={t("Identifying")}
                  onSelect={applyStatusFilter}
                  value="identifying"
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

              <div className="px-2 pb-1 text-xs font-medium text-[var(--semi-color-text-2)]">
                {t("Long-lived")}
              </div>
              <div className="space-y-1">
                <StatisticFilterOption
                  active={longLivedFilter === "all"}
                  count={resourceStats.longLived.all}
                  label={t("All")}
                  onSelect={applyLongLivedFilter}
                  value="all"
                />
                <StatisticFilterOption
                  active={longLivedFilter === "yes"}
                  count={resourceStats.longLived.yes}
                  label={t("Yes")}
                  onSelect={applyLongLivedFilter}
                  value="yes"
                />
                <StatisticFilterOption
                  active={longLivedFilter === "no"}
                  count={resourceStats.longLived.no}
                  label={t("No")}
                  onSelect={applyLongLivedFilter}
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
    <div className="console-content-width py-5">
      <ProtoBulkTaskProgress taskId={bulkTaskId} onCompleted={refresh} onClose={() => setBulkTaskId(null)} />
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

      <ImportProtoEmailsModal
        open={importOpen}
        onOpenChange={setImportOpen}
        onSuccess={handleImportSuccess}
      />
      <SupplierApplicationModal
        open={supplierApplicationOpen}
        onOpenChange={setSupplierApplicationOpen}
        onSuccess={() => undefined}
      />
    </div>
  );
}
