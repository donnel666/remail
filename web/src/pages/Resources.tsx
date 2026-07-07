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
import type { TFunction } from "i18next";
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
import { useIsMobile } from "@/hooks/use-is-mobile";
import { useSharedPageSize } from "@/hooks/use-shared-page-size";
import { getIamErrorMessage } from "@/lib/iam-errors";
import {
  deleteMicrosoftResource,
  deleteMicrosoftResourcesByFilter,
  deleteMicrosoftResourcesBatch,
  getCurrentSupplierApplication,
  listOwnedMicrosoftResources,
  publishMicrosoftResource,
  publishMicrosoftResourcesByFilter,
  publishMicrosoftResourcesBatch,
  validateMicrosoftResourcesBatch,
  validateMicrosoftResourcesByFilter,
  type ResourceBulkFilter,
} from "@/lib/resources-api";

import { ImportMicrosoftEmailsModal } from "./resources/import-microsoft-emails-modal";
import {
  DATE_RANGE_DROPDOWN_CLASS,
  createDateRangePresets,
  createdFromISOString,
  createdToISOString,
  matchesCreatedAtRange,
  normalizeDateRangeValue,
  type DateRangeValue,
} from "./resources/date-range-filter";
import {
  getSuffix,
  getSuffixCounts,
  type EmailResource,
  type LifetimeType,
  type ResourceStatus,
  type UsageScope,
  isNormal,
  toEmailResource,
} from "./resources/model";
import { renderStatusTag } from "./resources/resource-status-tag";
import { SupplierApplicationModal } from "./resources/supplier-application-modal";
import { useSelectionNotification } from "./resources/use-selection-notification";

type StatusFilter = "all" | "normal" | "pending" | "abnormal" | "disabled";
type BooleanFilter = "all" | "yes" | "no";
const supplierRoleLevel = 20;

function hasSupplierRole(roleLevel?: number | null) {
  return (roleLevel ?? 0) >= supplierRoleLevel;
}

function matchesStatusFilter(status: ResourceStatus, filter: StatusFilter) {
  if (filter === "all") return true;
  return status === filter;
}

function matchesBooleanFilter(value: boolean, filter: BooleanFilter) {
  if (filter === "all") return true;
  return filter === "yes" ? value : !value;
}

function isEmailResource(item: EmailResource | null): item is EmailResource {
  return item !== null;
}

function useResources(t: TFunction) {
  const [items, setItems] = useState<EmailResource[]>([]);
  const [loading, setLoading] = useState(true);
  const refreshSeqRef = useRef(0);
  const locallyDeletedResourceIDsRef = useRef(new Set<number>());

  const refresh = useCallback(async () => {
    const refreshSeq = refreshSeqRef.current + 1;
    refreshSeqRef.current = refreshSeq;
    const isCurrentRefresh = () => refreshSeqRef.current === refreshSeq;

    locallyDeletedResourceIDsRef.current.clear();
    setLoading(true);
    setItems([]);
    try {
      let hasRenderedFirstPage = false;
      await listOwnedMicrosoftResources({
        onPage: (pageItems) => {
          if (!isCurrentRefresh()) return;
          const resources = pageItems
            .map(toEmailResource)
            .filter(isEmailResource)
            .filter(
              (resource) =>
                !locallyDeletedResourceIDsRef.current.has(resource.id)
            );
          if (resources.length === 0) return;
          setItems((previous) => [...previous, ...resources]);
          if (!hasRenderedFirstPage) {
            hasRenderedFirstPage = true;
            setLoading(false);
          }
        },
      });
    } catch (error) {
      if (isCurrentRefresh()) {
        Toast.error(getIamErrorMessage(t, error, "Resources load failed."));
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

  const removeResource = useCallback((resourceID: number) => {
    locallyDeletedResourceIDsRef.current.add(resourceID);
    setItems((previous) =>
      previous.filter((resource) => resource.id !== resourceID)
    );
  }, []);

  const removeResourcesByPredicate = useCallback(
    (predicate: (resource: EmailResource) => boolean) => {
      setItems((previous) => {
        const next: EmailResource[] = [];
        for (const resource of previous) {
          if (predicate(resource)) {
            locallyDeletedResourceIDsRef.current.add(resource.id);
            continue;
          }
          next.push(resource);
        }
        return next;
      });
    },
    []
  );

  const markResourcesPublishedForSale = useCallback((resourceIDs: number[]) => {
    const ids = new Set(resourceIDs);
    if (ids.size === 0) return;
    setItems((previous) =>
      previous.map((resource) =>
        ids.has(resource.id)
          ? { ...resource, forSale: true, usageScope: "public_sale" }
          : resource
      )
    );
  }, []);

  const markResourcesPublishedByPredicate = useCallback(
    (predicate: (resource: EmailResource) => boolean) => {
      setItems((previous) =>
        previous.map((resource) =>
          predicate(resource)
            ? { ...resource, forSale: true, usageScope: "public_sale" }
            : resource
        )
      );
    },
    []
  );

  useEffect(() => {
    void refresh();
  }, [refresh]);

  return {
    items,
    loading,
    refresh,
    removeResource,
    removeResourcesByPredicate,
    markResourcesPublishedForSale,
    markResourcesPublishedByPredicate,
    invalidateRefresh,
  };
}

export default function Resources() {
  const { t } = useTranslation();
  const { currentUser, refreshCurrentUser } = useAuth();
  const isMobile = useIsMobile();
  const {
    items,
    loading,
    refresh,
    removeResource,
    removeResourcesByPredicate,
    markResourcesPublishedForSale,
    markResourcesPublishedByPredicate,
    invalidateRefresh,
  } = useResources(t);
  const [activeSuffix, setActiveSuffix] = useState("all");
  const [searchKeyword, setSearchKeyword] = useState("");
  const [createdAtRange, setCreatedAtRange] = useState<DateRangeValue>([]);
  const [statusFilter, setStatusFilter] = useState<StatusFilter>("all");
  const [privateFilter, setPrivateFilter] = useState<BooleanFilter>("all");
  const [longLivedFilter, setLongLivedFilter] =
    useState<BooleanFilter>("all");
  const [graphFilter, setGraphFilter] = useState<BooleanFilter>("all");
  const [compactMode, setCompactMode] = useState(false);
  const [importOpen, setImportOpen] = useState(false);
  const [supplierApplicationOpen, setSupplierApplicationOpen] = useState(false);
  const [selectedKeys, setSelectedKeys] = useState<number[]>([]);
  const [activePage, setActivePage] = useState(1);
  const [pageSize, setPageSize] = useSharedPageSize();
  const [publishingResourceID, setPublishingResourceID] = useState<number | null>(
    null
  );
  const [deletingResourceID, setDeletingResourceID] = useState<number | null>(
    null
  );
  const [publishingBatch, setPublishingBatch] = useState(false);
  const [deletingBatch, setDeletingBatch] = useState(false);
  const dateRangePresets = useMemo(() => createDateRangePresets(t), [t]);
  const canPublishForSale = hasSupplierRole(currentUser?.roleLevel);

  const suffixCounts = useMemo(() => getSuffixCounts(items), [items]);
  const suffixSet = useMemo(
    () => new Set(suffixCounts.map(([suffix]) => suffix)),
    [suffixCounts]
  );

  const resourceStats = useMemo(
    () => ({
      longLived: {
        all: items.length,
        no: items.filter((item) => item.lifetimeType !== "long_lived").length,
        yes: items.filter((item) => item.lifetimeType === "long_lived").length,
      },
      graph: {
        all: items.length,
        no: items.filter((item) => !item.graphAvailable).length,
        yes: items.filter((item) => item.graphAvailable).length,
      },
      private: {
        all: items.length,
        no: items.filter((item) => item.usageScope !== "private").length,
        yes: items.filter((item) => item.usageScope === "private").length,
      },
      status: {
        all: items.length,
        abnormal: items.filter((item) => item.status === "abnormal").length,
        disabled: items.filter((item) => item.status === "disabled").length,
        normal: items.filter((item) => isNormal(item.status)).length,
        pending: items.filter((item) => item.status === "pending").length,
      },
    }),
    [items]
  );

  const activeStatisticFilterCount =
    Number(statusFilter !== "all") +
    Number(privateFilter !== "all") +
    Number(longLivedFilter !== "all") +
    Number(graphFilter !== "all");

  useEffect(() => {
    if (activeSuffix !== "all" && !suffixSet.has(activeSuffix)) {
      setActiveSuffix("all");
    }
  }, [activeSuffix, suffixSet]);

  const matchesCurrentFilters = useCallback((item: EmailResource) => {
    const keyword = searchKeyword.trim().toLowerCase();
    const suffix = getSuffix(item.emailAddress);
    const suffixMatched = activeSuffix === "all" || suffix === activeSuffix;
    const statusMatched = matchesStatusFilter(item.status, statusFilter);
    const privateMatched = matchesBooleanFilter(
      item.usageScope === "private",
      privateFilter
    );
    const longLivedMatched = matchesBooleanFilter(
      item.lifetimeType === "long_lived",
      longLivedFilter
    );
    const graphMatched = matchesBooleanFilter(
      item.graphAvailable,
      graphFilter
    );
    const keywordMatched =
      keyword.length === 0 ||
      item.emailAddress.toLowerCase().includes(keyword) ||
      item.emailType.toLowerCase().includes(keyword) ||
      suffix.includes(keyword);
    const createdAtMatched = matchesCreatedAtRange(
      item.createdAt,
      createdAtRange
    );

    return (
      suffixMatched &&
      statusMatched &&
      privateMatched &&
      longLivedMatched &&
      graphMatched &&
      keywordMatched &&
      createdAtMatched
    );
  }, [
    activeSuffix,
    createdAtRange,
    graphFilter,
    longLivedFilter,
    privateFilter,
    searchKeyword,
    statusFilter,
  ]);

  const filteredItems = useMemo(
    () => items.filter(matchesCurrentFilters),
    [items, matchesCurrentFilters]
  );

  const microsoftBulkFilter = useMemo<ResourceBulkFilter>(() => {
    const filter: ResourceBulkFilter = { resourceType: "microsoft" };
    const search = searchKeyword.trim();
    const createdFrom = createdFromISOString(createdAtRange);
    const createdTo = createdToISOString(createdAtRange);
    if (search) filter.search = search;
    if (activeSuffix !== "all") filter.suffix = activeSuffix;
    if (statusFilter !== "all") filter.status = statusFilter;
    if (privateFilter !== "all") {
      filter.forSale = privateFilter === "no";
    }
    if (longLivedFilter !== "all") {
      filter.longLived = longLivedFilter === "yes";
    }
    if (graphFilter !== "all") {
      filter.graphAvailable = graphFilter === "yes";
    }
    if (createdFrom) filter.createdFrom = createdFrom;
    if (createdTo) filter.createdTo = createdTo;
    return filter;
  }, [
    activeSuffix,
    createdAtRange,
    graphFilter,
    longLivedFilter,
    privateFilter,
    searchKeyword,
    statusFilter,
  ]);

  const totalPages = Math.max(1, Math.ceil(filteredItems.length / pageSize));
  const safePage = Math.min(activePage, totalPages);
  const pagedItems = filteredItems.slice(
    (safePage - 1) * pageSize,
    safePage * pageSize
  );

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
    setCreatedAtRange([]);
    setStatusFilter("all");
    setPrivateFilter("all");
    setLongLivedFilter("all");
    setGraphFilter("all");
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

  const applyGraphFilter = (value: BooleanFilter) => {
    setGraphFilter(value);
    setActivePage(1);
    setSelectedKeys([]);
  };

  const handleCheckResource = useCallback(async (record: EmailResource) => {
    try {
      await validateMicrosoftResourcesBatch([record.id]);
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
      const response = await validateMicrosoftResourcesBatch(ids);
      Toast.success(
        t("Resource validations submitted.", { count: response.queued })
      );
      setSelectedKeys([]);
    } catch (error) {
      Toast.error(getIamErrorMessage(t, error, "Resource validation failed."));
    }
  }, [t]);

  const queueFilteredResourceChecks = useCallback(async () => {
    if (filteredItems.length === 0) {
      Toast.info(t("No resources to check."));
      return;
    }

    try {
      const response = await validateMicrosoftResourcesByFilter(
        microsoftBulkFilter
      );
      Toast.success(
        t("Resource validations submitted.", { count: response.queued })
      );
      setSelectedKeys([]);
    } catch (error) {
      Toast.error(getIamErrorMessage(t, error, "Resource validation failed."));
    }
  }, [filteredItems.length, microsoftBulkFilter, t]);

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
          await publishMicrosoftResource(record.id);
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
          const response = await publishMicrosoftResourcesByFilter(
            microsoftBulkFilter
          );
          if (response.published === 0) {
            Toast.info(t("No private resources to publish."));
          } else {
            Toast.success(
              t("Resources published for sale.", { count: response.published })
            );
            invalidateRefresh();
            markResourcesPublishedByPredicate(
              (item) =>
                item.usageScope === "private" && matchesCurrentFilters(item)
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
    ensureCanPublishForSale,
    invalidateRefresh,
    markResourcesPublishedByPredicate,
    matchesCurrentFilters,
    microsoftBulkFilter,
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
      onOk: async () => {
        setPublishingBatch(true);
        try {
          const response = await publishMicrosoftResourcesBatch(
            selectedPrivateResourceIds
          );
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
      },
    });
  }, [
    ensureCanPublishForSale,
    markResourcesPublishedForSale,
    selectedPrivateResourceIds,
    t,
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
      const response = await deleteMicrosoftResourcesBatch(resourceIds, {
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
          const response = await deleteMicrosoftResourcesByFilter(
            microsoftBulkFilter
          );
          if (response.deleted === 0) {
            Toast.info(t("No private resources to delete."));
          } else {
            invalidateRefresh();
            removeResourcesByPredicate(
              (item) =>
                item.usageScope === "private" && matchesCurrentFilters(item)
            );
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
    matchesCurrentFilters,
    microsoftBulkFilter,
    privateFilter,
    invalidateRefresh,
    removeResourcesByPredicate,
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
          await deleteMicrosoftResource(record.id);
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
          key: "graph",
          title: t("Graph"),
          dataIndex: "graphAvailable",
          width: 90,
          render: (value: boolean) => (
            <Tag color={value ? "green" : "grey"} shape="circle">
              {value ? t("Yes") : t("No")}
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
              {items.length}
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

              <div className="px-2 pb-1 pt-2 text-xs font-medium text-[var(--semi-color-text-2)]">
                {t("Graph")}
              </div>
              <div className="space-y-1">
                <StatisticFilterOption
                  active={graphFilter === "all"}
                  count={resourceStats.graph.all}
                  label={t("All")}
                  onSelect={applyGraphFilter}
                  value="all"
                />
                <StatisticFilterOption
                  active={graphFilter === "yes"}
                  count={resourceStats.graph.yes}
                  label={t("Yes")}
                  onSelect={applyGraphFilter}
                  value="yes"
                />
                <StatisticFilterOption
                  active={graphFilter === "no"}
                  count={resourceStats.graph.no}
                  label={t("No")}
                  onSelect={applyGraphFilter}
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
          onChange={(value) => setSearchKeyword(String(value))}
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

      <ImportMicrosoftEmailsModal
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
