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
  Toast,
  Tooltip,
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
import { useBlockPagedList } from "@/hooks/use-block-paged-list";
import { useDebouncedValue } from "@/hooks/use-debounced-value";
import { useIsMobile } from "@/hooks/use-is-mobile";
import { useSharedPageSize } from "@/hooks/use-shared-page-size";
import { getIamErrorMessage } from "@/lib/iam-errors";

import {
  DATE_RANGE_DROPDOWN_CLASS,
  createDateRangePresets,
  createdFromISOString,
  createdToISOString,
  normalizeDateRangeValue,
  type DateRangeValue,
} from "./resources/date-range-filter";
import { useSelectionNotification } from "./resources/use-selection-notification";
import {
  OwnerIdentity,
  STATUS_META,
  TOKEN_META,
  formatTime,
  renderStatusTag,
} from "./admin-microsoft/microsoft-meta";
import {
  EditMicrosoftModal,
  ImportMicrosoftModal,
  ReplaceCredentialsModal,
} from "./admin-microsoft/microsoft-modals";
import { MicrosoftDetailSheet } from "./admin-microsoft/microsoft-detail-sheet";
import {
  deleteAdminMicrosoftResource,
  deleteAdminMicrosoftResourcesByIds,
  deleteAdminMicrosoftResourcesByFilter,
  disableAdminMicrosoftResourcesByIds,
  getAdminMicrosoftResourceDetail,
  listAdminMicrosoftOwners,
  listAdminMicrosoftResources,
  recoverAdminMicrosoftResource,
  setAdminMicrosoftResourcesForSaleByFilter,
  setAdminMicrosoftResourcesForSaleByIds,
  updateAdminMicrosoftResource,
  validateAdminMicrosoftResource,
  validateAdminMicrosoftResourcesByFilter,
  validateAdminMicrosoftResourcesByIds,
  type AdminMicrosoftFacets,
  type AdminMicrosoftListFilter,
  type AdminMicrosoftOwner,
  type AdminMicrosoftResourceDetail,
  type AdminMicrosoftResourceItem,
  type AdminMicrosoftResourceStatus,
  type AdminMicrosoftTokenHealth,
} from "./admin-microsoft/admin-microsoft-mock";

type StatusFilter = "all" | AdminMicrosoftResourceStatus;
type BooleanFilter = "all" | "yes" | "no";
type TokenHealthFilter = "all" | AdminMicrosoftTokenHealth;

export default function AdminMicrosoftEmails() {
  const { t } = useTranslation();
  const isMobile = useIsMobile();

  const [activeSuffix, setActiveSuffix] = useState("all");
  const [searchKeyword, setSearchKeyword] = useState("");
  const [createdAtRange, setCreatedAtRange] = useState<DateRangeValue>([]);
  const [statusFilter, setStatusFilter] = useState<StatusFilter>("all");
  const [privateFilter, setPrivateFilter] = useState<BooleanFilter>("all");
  const [longLivedFilter, setLongLivedFilter] =
    useState<BooleanFilter>("all");
  const [graphFilter, setGraphFilter] = useState<BooleanFilter>("all");
  const [tokenHealthFilter, setTokenHealthFilter] =
    useState<TokenHealthFilter>("all");
  const [compactMode, setCompactMode] = useState(false);
  const [selectedKeys, setSelectedKeys] = useState<number[]>([]);
  const [activePage, setActivePage] = useState(1);
  const [pageSize, setPageSize] = useSharedPageSize();
  const [facets, setFacets] = useState<AdminMicrosoftFacets | null>(null);
  const [owners, setOwners] = useState<AdminMicrosoftOwner[]>([]);

  const [importOpen, setImportOpen] = useState(false);
  const [editTarget, setEditTarget] = useState<AdminMicrosoftResourceItem | null>(null);
  const [credentialsTarget, setCredentialsTarget] =
    useState<AdminMicrosoftResourceItem | null>(null);
  const [detail, setDetail] = useState<AdminMicrosoftResourceDetail | null>(null);
  const [detailLoading, setDetailLoading] = useState(false);
  const [detailBusy, setDetailBusy] = useState(false);
  const [rowBusy, setRowBusy] = useState<{
    action: "check" | "delete" | "publish" | "recover" | "toggle";
    id: number;
  } | null>(null);
  const [bulkBusy, setBulkBusy] = useState<
    "check" | "disable" | "delete" | "publish" | "private" | null
  >(null);

  const [debouncedSearchKeyword, flushSearchKeyword] =
    useDebouncedValue(searchKeyword);
  const dateRangePresets = useMemo(() => createDateRangePresets(t), [t]);

  useEffect(() => {
    let cancelled = false;
    void listAdminMicrosoftOwners("")
      .then((items) => {
        if (!cancelled) setOwners(items);
      })
      .catch(() => {
        // Owner choices are optional UI data; the resource list remains usable.
      });
    return () => {
      cancelled = true;
    };
  }, []);

  const statsFilter = useMemo<AdminMicrosoftListFilter>(() => {
    const filter: AdminMicrosoftListFilter = {};
    const search = debouncedSearchKeyword.trim();
    const createdFrom = createdFromISOString(createdAtRange);
    const createdTo = createdToISOString(createdAtRange);
    if (search) filter.search = search;
    if (statusFilter !== "all") filter.status = statusFilter;
    if (privateFilter !== "all") filter.forSale = privateFilter === "no";
    if (longLivedFilter !== "all") filter.longLived = longLivedFilter === "yes";
    if (graphFilter !== "all") filter.graphAvailable = graphFilter === "yes";
    if (tokenHealthFilter !== "all") filter.tokenHealth = tokenHealthFilter;
    if (createdFrom) filter.createdFrom = createdFrom;
    if (createdTo) filter.createdTo = createdTo;
    return filter;
  }, [
    createdAtRange,
    debouncedSearchKeyword,
    graphFilter,
    longLivedFilter,
    privateFilter,
    statusFilter,
    tokenHealthFilter,
  ]);

  const listFilter = useMemo<AdminMicrosoftListFilter>(() => {
    if (activeSuffix === "all") return statsFilter;
    return { ...statsFilter, suffix: activeSuffix };
  }, [activeSuffix, statsFilter]);

  const loadMicrosoftBlock = useCallback(
    async (offset: number, limit: number, cursor?: { afterId?: number }) => {
      const response = await listAdminMicrosoftResources(
        listFilter,
        offset,
        limit,
        cursor?.afterId
      );
      return {
        items: response.items,
        nextAfterId: response.nextAfterId,
        total: response.total,
      };
    },
    [listFilter]
  );

  const {
    loading,
    pagedItems,
    refresh: refreshList,
    total,
  } = useBlockPagedList<AdminMicrosoftResourceItem>({
    activePage,
    filterKey: JSON.stringify(listFilter),
    loadBlock: loadMicrosoftBlock,
    onError: (error) => {
      Toast.error(getIamErrorMessage(t, error, "Admin Microsoft resources load failed."));
    },
    pageSize,
  });

  const refreshStats = useCallback(async () => {
    try {
      const response = await listAdminMicrosoftResources(listFilter, 0, 1);
      setFacets(response.facets);
    } catch {
      // Preserve the current facets; a later refresh retries the mock query.
    }
  }, [listFilter]);

  useEffect(() => {
    void refreshStats();
  }, [refreshStats]);

  const refresh = useCallback(async () => {
    await Promise.all([refreshStats(), refreshList()]);
  }, [refreshList, refreshStats]);

  const refreshOpenDetail = useCallback(async (resourceId?: number) => {
    const id = resourceId ?? detail?.id;
    if (!id) return;
    const nextDetail = await getAdminMicrosoftResourceDetail(id);
    setDetail(nextDetail);
  }, [detail?.id]);

  const openDetail = useCallback(async (resourceId: number) => {
    setDetailLoading(true);
    setDetail(null);
    try {
      setDetail(await getAdminMicrosoftResourceDetail(resourceId));
    } catch (error) {
      Toast.error(getIamErrorMessage(t, error, "Microsoft resource detail load failed."));
      setDetailLoading(false);
    } finally {
      setDetailLoading(false);
    }
  }, [t]);

  const suffixCounts = useMemo(
    () => facets?.suffixes.map((item) => [item.key, item.count] as [string, number]) ?? [],
    [facets]
  );
  const suffixSet = useMemo(
    () => new Set(suffixCounts.map(([suffix]) => suffix)),
    [suffixCounts]
  );
  useEffect(() => {
    if (facets && activeSuffix !== "all" && !suffixSet.has(activeSuffix)) {
      setActiveSuffix("all");
    }
  }, [activeSuffix, facets, suffixSet]);

  const stats = useMemo(() => {
    if (facets) return facets;
    return {
      activeTasks: 0,
      failedTasks: 0,
      forSale: { all: total, no: 0, yes: 0 },
      graphAvailable: { all: total, no: 0, yes: 0 },
      longLived: { all: total, no: 0, yes: 0 },
      owners: [],
      status: {
        abnormal: 0,
        all: total,
        deleted: 0,
        disabled: 0,
        normal: 0,
        pending: 0,
      },
      suffixes: [],
      taskStatus: { all: total, failed: 0, idle: 0, queued: 0, running: 0 },
      tokenHealth: { all: total, expired: 0, expiring: 0, missing: 0, valid: 0 },
    } satisfies AdminMicrosoftFacets;
  }, [facets, total]);

  const allTabCount = suffixCounts.reduce((sum, [, count]) => sum + count, 0);
  const activeFilterCount =
    Number(statusFilter !== "all") +
    Number(privateFilter !== "all") +
    Number(longLivedFilter !== "all") +
    Number(graphFilter !== "all") +
    Number(tokenHealthFilter !== "all");

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
    setGraphFilter("all");
    setTokenHealthFilter("all");
    setActiveSuffix("all");
    setActivePage(1);
    setSelectedKeys([]);
  };

  const resetPageAndSelection = () => {
    setActivePage(1);
    setSelectedKeys([]);
  };

  const runRowOperation = useCallback(
    async (
      record: AdminMicrosoftResourceItem,
      action: "check" | "delete" | "publish" | "recover" | "toggle",
      operation: () => Promise<unknown>,
      successKey: string
    ) => {
      setRowBusy({ action, id: record.id });
      try {
        await operation();
        Toast.success(t(successKey));
        await refresh();
        if (detail?.id === record.id) await refreshOpenDetail(record.id);
      } catch (error) {
        Toast.error(getIamErrorMessage(t, error, "Microsoft resource operation failed."));
      } finally {
        setRowBusy(null);
      }
    },
    [detail?.id, refresh, refreshOpenDetail, t]
  );

  const handleValidate = useCallback(
    (record: AdminMicrosoftResourceItem) =>
      runRowOperation(
        record,
        "check",
        () => validateAdminMicrosoftResource(record.id),
        "Resource validation submitted."
      ),
    [runRowOperation]
  );

  const handleToggleDisabled = useCallback(
    (record: AdminMicrosoftResourceItem) =>
      runRowOperation(
        record,
        "toggle",
        () =>
          updateAdminMicrosoftResource(record.id, {
            status: record.status === "disabled" ? "pending" : "disabled",
          }),
        record.status === "disabled"
          ? "Microsoft resource enabled and queued for validation."
          : "Microsoft resource disabled."
      ),
    [runRowOperation]
  );

  const handleRecover = useCallback(
    (record: AdminMicrosoftResourceItem) =>
      runRowOperation(
        record,
        "recover",
        () => recoverAdminMicrosoftResource(record.id),
        "Microsoft resource recovered and queued for validation."
      ),
    [runRowOperation]
  );

  const handleTogglePublish = useCallback(
    (record: AdminMicrosoftResourceItem) =>
      runRowOperation(
        record,
        "publish",
        () => updateAdminMicrosoftResource(record.id, { forSale: !record.forSale }),
        record.forSale
          ? "Microsoft resource converted to private."
          : "Microsoft resource published for public sale."
      ),
    [runRowOperation]
  );

  const confirmDelete = useCallback(
    (record: AdminMicrosoftResourceItem) => {
      Modal.confirm({
        cancelText: t("Cancel"),
        content: t("Confirm delete Microsoft resource content", {
          email: record.emailAddress,
        }),
        okButtonProps: { type: "danger" },
        okText: t("Delete"),
        onOk: () =>
          runRowOperation(
            record,
            "delete",
            () => deleteAdminMicrosoftResource(record.id),
            "Microsoft resource deleted."
          ),
        title: t("Confirm delete"),
      });
    },
    [runRowOperation, t]
  );

  const runDetailOperation = useCallback(
    async (operation: () => Promise<unknown>, successKey: string) => {
      if (!detail) return;
      setDetailBusy(true);
      try {
        await operation();
        Toast.success(t(successKey));
        await refresh();
        await refreshOpenDetail(detail.id);
      } catch (error) {
        Toast.error(getIamErrorMessage(t, error, "Microsoft resource operation failed."));
      } finally {
        setDetailBusy(false);
      }
    },
    [detail, refresh, refreshOpenDetail, t]
  );

  const validateSelected = useCallback(async () => {
    if (selectedKeys.length === 0) return;
    setBulkBusy("check");
    try {
      const response = await validateAdminMicrosoftResourcesByIds(selectedKeys);
      Toast.success(t("Resource validations submitted.", { count: response.queued }));
      setSelectedKeys([]);
      await refresh();
    } catch (error) {
      Toast.error(getIamErrorMessage(t, error, "Resource validation failed."));
    } finally {
      setBulkBusy(null);
    }
  }, [refresh, selectedKeys, t]);

  const confirmDisableSelected = useCallback(() => {
    if (selectedKeys.length === 0) return;
    Modal.confirm({
      cancelText: t("Cancel"),
      content: t("Confirm disable selected Microsoft resources", {
        count: selectedKeys.length,
      }),
      okText: t("Disable"),
      onOk: async () => {
        setBulkBusy("disable");
        try {
          const response = await disableAdminMicrosoftResourcesByIds(selectedKeys);
          Toast.success(t("Microsoft resources disabled.", { count: response.affected }));
          setSelectedKeys([]);
          await refresh();
        } catch (error) {
          Toast.error(getIamErrorMessage(t, error, "Microsoft resource operation failed."));
        } finally {
          setBulkBusy(null);
        }
      },
      title: t("Confirm disable selected"),
    });
  }, [refresh, selectedKeys, t]);

  const confirmDeleteSelected = useCallback(() => {
    if (selectedKeys.length === 0) return;
    Modal.confirm({
      cancelText: t("Cancel"),
      content: t("Confirm delete selected Microsoft resources", {
        count: selectedKeys.length,
      }),
      okButtonProps: { type: "danger" },
      okText: t("Delete"),
      onOk: async () => {
        setBulkBusy("delete");
        try {
          const response = await deleteAdminMicrosoftResourcesByIds(selectedKeys);
          Toast.success(t("Microsoft resources deleted.", { count: response.affected }));
          setSelectedKeys([]);
          setActivePage(1);
          await refresh();
        } catch (error) {
          Toast.error(getIamErrorMessage(t, error, "Microsoft resource operation failed."));
        } finally {
          setBulkBusy(null);
        }
      },
      title: t("Confirm delete selected"),
    });
  }, [refresh, selectedKeys, t]);

  const confirmValidateAll = useCallback(() => {
    if (total === 0) {
      Toast.info(t("No resources to check."));
      return;
    }
    Modal.confirm({
      cancelText: t("Cancel"),
      content: t("Confirm validate all matching Microsoft resources", { count: total }),
      okText: t("Check"),
      onOk: async () => {
        setBulkBusy("check");
        try {
          const response = await validateAdminMicrosoftResourcesByFilter(listFilter);
          Toast.success(t("Resource validations submitted.", { count: response.queued }));
          setSelectedKeys([]);
          await refresh();
        } catch (error) {
          Toast.error(getIamErrorMessage(t, error, "Resource validation failed."));
        } finally {
          setBulkBusy(null);
        }
      },
      title: t("Confirm check all"),
    });
  }, [listFilter, refresh, t, total]);

  const confirmDeleteAll = useCallback(() => {
    if (total === 0) {
      Toast.info(t("No resources to check."));
      return;
    }
    Modal.confirm({
      cancelText: t("Cancel"),
      content: t("Confirm delete all matching Microsoft resources", { count: total }),
      okButtonProps: { type: "danger" },
      okText: t("Delete"),
      onOk: async () => {
        setBulkBusy("delete");
        try {
          const response = await deleteAdminMicrosoftResourcesByFilter(listFilter);
          Toast.success(t("Microsoft resources deleted.", { count: response.affected }));
          setSelectedKeys([]);
          setActivePage(1);
          await refresh();
        } catch (error) {
          Toast.error(getIamErrorMessage(t, error, "Microsoft resource operation failed."));
        } finally {
          setBulkBusy(null);
        }
      },
      title: t("Confirm delete all"),
    });
  }, [listFilter, refresh, t, total]);

  const runBulkForSale = useCallback(
    async (forSale: boolean) => {
      if (selectedKeys.length === 0) return;
      setBulkBusy(forSale ? "publish" : "private");
      try {
        const response = await setAdminMicrosoftResourcesForSaleByIds(
          selectedKeys,
          forSale
        );
        Toast.success(
          t(
            forSale
              ? "Microsoft resources published for public sale."
              : "Microsoft resources converted to private.",
            { count: response.affected }
          )
        );
        setSelectedKeys([]);
        await refresh();
      } catch (error) {
        Toast.error(getIamErrorMessage(t, error, "Microsoft resource operation failed."));
      } finally {
        setBulkBusy(null);
      }
    },
    [refresh, selectedKeys, t]
  );

  const confirmForSaleAll = useCallback(
    (forSale: boolean) => {
      if (total === 0) {
        Toast.info(t("No resources to check."));
        return;
      }
      Modal.confirm({
        cancelText: t("Cancel"),
        content: t(
          forSale
            ? "Confirm put all matching Microsoft resources on sale"
            : "Confirm convert all matching Microsoft resources to private",
          { count: total }
        ),
        okText: forSale ? t("Put on sale") : t("Convert to private"),
        onOk: async () => {
          setBulkBusy(forSale ? "publish" : "private");
          try {
            const response = await setAdminMicrosoftResourcesForSaleByFilter(
              listFilter,
              forSale
            );
            Toast.success(
              t(
                forSale
                  ? "Microsoft resources published for public sale."
                  : "Microsoft resources converted to private.",
                { count: response.affected }
              )
            );
            setSelectedKeys([]);
            await refresh();
          } catch (error) {
            Toast.error(
              getIamErrorMessage(t, error, "Microsoft resource operation failed.")
            );
          } finally {
            setBulkBusy(null);
          }
        },
        title: forSale ? t("Confirm put all on sale") : t("Confirm convert all to private"),
      });
    },
    [listFilter, refresh, t, total]
  );

  useSelectionNotification({
    checkLabelKey: "Check",
    checkLoading: bulkBusy === "check",
    deleteLabelKey: "Delete",
    deleteLoading: bulkBusy === "delete",
    extraActions: [
      {
        key: "publish",
        labelKey: "Put on sale",
        loading: bulkBusy === "publish",
        onClick: () => void runBulkForSale(true),
        type: "secondary",
      },
      {
        key: "private",
        labelKey: "Convert to private",
        loading: bulkBusy === "private",
        onClick: () => void runBulkForSale(false),
        type: "tertiary",
      },
    ],
    onCheck: () => void validateSelected(),
    onClear: () => setSelectedKeys([]),
    onDelete: confirmDeleteSelected,
    onSell: confirmDisableSelected,
    selectedCount: selectedKeys.length,
    selectionDescriptionKey: "Selected Microsoft resources",
    sellLabelKey: "Disable",
    sellLoading: bulkBusy === "disable",
    t,
  });

  const renderRowActions = useCallback(
    (record: AdminMicrosoftResourceItem) => {
      const busyAction = rowBusy?.id === record.id ? rowBusy.action : null;
      if (record.status === "deleted") {
        return (
          <Space spacing={4} wrap={false}>
            <Button
              disabled={Boolean(busyAction)}
              onClick={() => void openDetail(record.id)}
              size="small"
              type="tertiary"
            >
              {t("Details")}
            </Button>
            <Button
              disabled={Boolean(rowBusy && busyAction !== "recover")}
              loading={busyAction === "recover"}
              onClick={() => void handleRecover(record)}
              size="small"
              type="primary"
            >
              {t("Recover")}
            </Button>
          </Space>
        );
      }

      return (
        <Space spacing={4} wrap={false}>
          <Button
            disabled={Boolean(busyAction)}
            onClick={() => void openDetail(record.id)}
            size="small"
            type="tertiary"
          >
            {t("Details")}
          </Button>
          <Button
            disabled={Boolean(busyAction)}
            onClick={() => setEditTarget(record)}
            size="small"
            type="tertiary"
          >
            {t("Edit")}
          </Button>
          <Button
            disabled={Boolean(rowBusy && busyAction !== "check")}
            loading={busyAction === "check"}
            onClick={() => void handleValidate(record)}
            size="small"
            type="tertiary"
          >
            {t("Check")}
          </Button>
          <Button
            disabled={Boolean(rowBusy && busyAction !== "toggle")}
            loading={busyAction === "toggle"}
            onClick={() => void handleToggleDisabled(record)}
            size="small"
            type="tertiary"
          >
            {record.status === "disabled" ? t("Enable") : t("Disable")}
          </Button>
          <Button
            disabled={Boolean(rowBusy && busyAction !== "publish")}
            loading={busyAction === "publish"}
            onClick={() => void handleTogglePublish(record)}
            size="small"
            type="tertiary"
          >
            {record.forSale ? t("Convert to private") : t("Put on sale")}
          </Button>
          <Button
            disabled={Boolean(rowBusy && busyAction !== "delete")}
            loading={busyAction === "delete"}
            onClick={() => confirmDelete(record)}
            size="small"
            type="danger"
          >
            {t("Delete")}
          </Button>
        </Space>
      );
    },
    [
      confirmDelete,
      handleRecover,
      handleToggleDisabled,
      handleTogglePublish,
      handleValidate,
      openDetail,
      rowBusy,
      setEditTarget,
      t,
    ]
  );

  const columns = useMemo(
    () =>
      [
        {
          dataIndex: "suffix",
          key: "suffix",
          title: t("Suffix"),
          width: 120,
          render: (value: unknown) => (
            <Tag color="white" shape="circle">
              {String(value)}
            </Tag>
          ),
        },
        {
          dataIndex: "emailAddress",
          key: "email",
          title: t("Email"),
          width: 280,
          render: (value: unknown) => (
            <CopyableTableText copiedText={t("Copied")} text={String(value)} />
          ),
        },
        {
          dataIndex: "bindingAddress",
          key: "binding",
          title: t("Auxiliary email"),
          width: 240,
          render: (value: unknown) =>
            value ? (
              <CopyableTableText copiedText={t("Copied")} text={String(value)} />
            ) : (
              <span className="text-[var(--semi-color-text-3)]">{t("Not configured")}</span>
            ),
        },
        {
          dataIndex: "ownerEmail",
          key: "owner",
          title: t("Owner"),
          width: 310,
          render: (_: unknown, record: AdminMicrosoftResourceItem) => (
            <OwnerIdentity
              ownerEmail={record.ownerEmail}
              ownerGroupName={record.ownerGroupName}
              ownerId={record.ownerId}
              ownerNickname={record.ownerNickname}
              ownerRole={record.ownerRole}
              t={t}
            />
          ),
        },
        {
          dataIndex: "status",
          key: "status",
          title: t("Status"),
          width: 120,
          render: (value: unknown, record: AdminMicrosoftResourceItem) =>
            renderStatusTag(value as AdminMicrosoftResourceStatus, t, record.lastSafeError),
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
          dataIndex: "longLived",
          key: "longLived",
          title: t("Long-lived"),
          width: 120,
          render: (value: unknown) => (
            <Tag color={value ? "green" : "grey"} shape="circle">
              {value ? t("Yes") : t("No")}
            </Tag>
          ),
        },
        {
          dataIndex: "graphAvailable",
          key: "graph",
          title: t("Graph"),
          width: 90,
          render: (value: unknown) => (
            <Tag color={value ? "green" : "grey"} shape="circle">
              {value ? t("Yes") : t("No")}
            </Tag>
          ),
        },
        {
          dataIndex: "tokenHealth",
          key: "token",
          title: t("Expires at"),
          width: 180,
          render: (_: unknown, record: AdminMicrosoftResourceItem) => (
            <span
              className={`whitespace-nowrap text-sm font-medium tabular-nums ${
                record.tokenHealth === "valid"
                  ? "text-[var(--semi-color-success)]"
                  : record.tokenHealth === "expiring"
                    ? "text-[var(--semi-color-warning)]"
                    : record.tokenHealth === "expired"
                      ? "text-[var(--semi-color-danger)]"
                      : "text-[var(--semi-color-text-2)]"
              }`}
            >
              {formatTime(record.rtExpireAt)}
            </span>
          ),
        },
        {
          dataIndex: "operate",
          fixed: "right",
          key: "operate",
          title: t("Action"),
          width: 400,
          render: (_: unknown, record: AdminMicrosoftResourceItem) => renderRowActions(record),
        },
      ] as any[],
    [renderRowActions, t]
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
      activeKey={activeSuffix}
      className="mb-2"
      collapsible
      onChange={(key) => selectSuffix(String(key))}
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
      {suffixCounts.map(([suffix, count]) => (
        <Tabs.TabPane
          itemKey={suffix}
          key={suffix}
          tab={
            <span className="flex items-center gap-2">
              <Layers size={14} />
              {suffix}
              <Tag color={activeSuffix === suffix ? "red" : "grey"} shape="circle">
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
          className="flex-1 md:flex-initial"
          onClick={() => setImportOpen(true)}
          size="small"
          type="primary"
        >
          {t("Import")}
        </Button>
        <Button
          className="remail-toolbar-fixed-button flex-1 md:flex-none"
          loading={loading}
          onClick={() => void refresh()}
          size="small"
          type="tertiary"
        >
          {t("Refresh")}
        </Button>
        <Tooltip content={t("Check all")} mouseEnterDelay={0} mouseLeaveDelay={0.05} position="top">
          <Button
            className="flex-1 md:flex-initial"
            loading={bulkBusy === "check"}
            onClick={confirmValidateAll}
            size="small"
            type="tertiary"
          >
            {t("Check")}
          </Button>
        </Tooltip>
        <Tooltip content={t("Put all on sale")} mouseEnterDelay={0} mouseLeaveDelay={0.05} position="top">
          <Button
            className="flex-1 md:flex-initial"
            loading={bulkBusy === "publish"}
            onClick={() => confirmForSaleAll(true)}
            size="small"
            type="tertiary"
          >
            {t("Put on sale")}
          </Button>
        </Tooltip>
        <Tooltip content={t("Convert all to private")} mouseEnterDelay={0} mouseLeaveDelay={0.05} position="top">
          <Button
            className="flex-1 md:flex-initial"
            loading={bulkBusy === "private"}
            onClick={() => confirmForSaleAll(false)}
            size="small"
            type="tertiary"
          >
            {t("Convert to private")}
          </Button>
        </Tooltip>
        <Tooltip content={t("Delete all")} mouseEnterDelay={0} mouseLeaveDelay={0.05} position="top">
          <Button
            className="flex-1 md:flex-initial"
            loading={bulkBusy === "delete"}
            onClick={confirmDeleteAll}
            size="small"
            type="danger"
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
          render={
            <div className="max-h-[70vh] w-[280px] overflow-auto p-2">
              <div className="px-2 pb-1 text-xs font-medium text-[var(--semi-color-text-2)]">
                {t("Status")}
              </div>
              <div className="mb-2 space-y-1">
                {(["all", "normal", "pending", "abnormal", "disabled", "deleted"] as StatusFilter[]).map(
                  (value) => (
                    <StatisticFilterOption
                      active={statusFilter === value}
                      count={stats.status[value]}
                      key={value}
                      label={t(value === "all" ? "All" : STATUS_META[value].label)}
                      onSelect={(next) => {
                        setStatusFilter(next);
                        resetPageAndSelection();
                      }}
                      value={value}
                    />
                  )
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
                        ? stats.forSale.all
                        : value === "yes"
                          ? stats.forSale.no
                          : stats.forSale.yes
                    }
                    key={value}
                    label={t(value === "all" ? "All" : value === "yes" ? "Yes" : "No")}
                    onSelect={(next) => {
                      setPrivateFilter(next);
                      resetPageAndSelection();
                    }}
                    value={value}
                  />
                ))}
              </div>

              <div className="px-2 pb-1 text-xs font-medium text-[var(--semi-color-text-2)]">
                {t("Long-lived")}
              </div>
              <div className="mb-2 space-y-1">
                {(["all", "yes", "no"] as BooleanFilter[]).map((value) => (
                  <StatisticFilterOption
                    active={longLivedFilter === value}
                    count={stats.longLived[value]}
                    key={value}
                    label={t(value === "all" ? "All" : value === "yes" ? "Yes" : "No")}
                    onSelect={(next) => {
                      setLongLivedFilter(next);
                      resetPageAndSelection();
                    }}
                    value={value}
                  />
                ))}
              </div>

              <div className="px-2 pb-1 text-xs font-medium text-[var(--semi-color-text-2)]">
                {t("Graph")}
              </div>
              <div className="mb-2 space-y-1">
                {(["all", "yes", "no"] as BooleanFilter[]).map((value) => (
                  <StatisticFilterOption
                    active={graphFilter === value}
                    count={stats.graphAvailable[value]}
                    key={value}
                    label={t(value === "all" ? "All" : value === "yes" ? "Yes" : "No")}
                    onSelect={(next) => {
                      setGraphFilter(next);
                      resetPageAndSelection();
                    }}
                    value={value}
                  />
                ))}
              </div>

              <div className="px-2 pb-1 text-xs font-medium text-[var(--semi-color-text-2)]">
                {t("Refresh token")}
              </div>
              <div className="mb-2 space-y-1">
                {(["all", "valid", "expiring", "expired", "missing"] as TokenHealthFilter[]).map(
                  (value) => (
                    <StatisticFilterOption
                      active={tokenHealthFilter === value}
                      count={stats.tokenHealth[value]}
                      key={value}
                      label={t(value === "all" ? "All" : TOKEN_META[value].label)}
                      onSelect={(next) => {
                        setTokenHealthFilter(next);
                        resetPageAndSelection();
                      }}
                      value={value}
                    />
                  )
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
            resetPageAndSelection();
          }}
          placeholder={t("Search email, owner or ID")}
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
            resetPageAndSelection();
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
          columns={tableColumns}
          dataSource={pagedItems}
          empty={
            <Empty
              darkModeImage={
                <IllustrationNoResultDark style={{ height: 150, width: 150 }} />
              }
              description={t("No Microsoft resources found")}
              image={<IllustrationNoResult style={{ height: 150, width: 150 }} />}
              style={{ padding: 30 }}
            />
          }
          hidePagination
          loading={loading}
          pagination={false}
          rowKey="id"
          rowSelection={rowSelection}
          scroll={{ x: "max(100%, 2030px)", y: DESKTOP_TABLE_SCROLL_Y }}
          size="middle"
        />
      </CardPro>

      <ImportMicrosoftModal
        onCancel={() => setImportOpen(false)}
        onImported={async () => {
          setActivePage(1);
          setSelectedKeys([]);
          await refresh();
        }}
        owners={owners}
        visible={importOpen}
      />

      <EditMicrosoftModal
        onCancel={() => setEditTarget(null)}
        onSaved={async () => {
          await refresh();
          if (editTarget && detail?.id === editTarget.id) {
            await refreshOpenDetail(editTarget.id);
          }
        }}
        owners={owners}
        target={editTarget}
      />

      <ReplaceCredentialsModal
        onCancel={() => setCredentialsTarget(null)}
        onSaved={async (nextDetail) => {
          await refresh();
          if (detail?.id === nextDetail.id) setDetail(nextDetail);
        }}
        target={credentialsTarget}
      />

      <MicrosoftDetailSheet
        busy={detailBusy}
        detail={detail}
        loading={detailLoading}
        onCancel={() => {
          setDetail(null);
          setDetailLoading(false);
        }}
        onRefresh={async () => {
          await refresh();
          if (detail) await refreshOpenDetail(detail.id);
        }}
        onDelete={() => {
          if (!detail) return;
          Modal.confirm({
            cancelText: t("Cancel"),
            content: t("Confirm delete Microsoft resource content", {
              email: detail.emailAddress,
            }),
            okButtonProps: { type: "danger" },
            okText: t("Delete"),
            onOk: () =>
              runDetailOperation(
                () => deleteAdminMicrosoftResource(detail.id),
                "Microsoft resource deleted."
              ),
            title: t("Confirm delete"),
          });
        }}
        onEdit={() => {
          if (detail) setEditTarget(detail);
        }}
        onRecover={() => {
          if (!detail) return;
          void runDetailOperation(
            () => recoverAdminMicrosoftResource(detail.id),
            "Microsoft resource recovered and queued for validation."
          );
        }}
        onReplaceCredentials={() => {
          if (detail) setCredentialsTarget(detail);
        }}
        onTogglePublish={() => {
          if (!detail) return;
          void runDetailOperation(
            () =>
              updateAdminMicrosoftResource(detail.id, {
                forSale: !detail.forSale,
              }),
            detail.forSale
              ? "Microsoft resource converted to private."
              : "Microsoft resource published for public sale."
          );
        }}
        onToggleDisabled={() => {
          if (!detail) return;
          void runDetailOperation(
            () =>
              updateAdminMicrosoftResource(detail.id, {
                status: detail.status === "disabled" ? "pending" : "disabled",
              }),
            detail.status === "disabled"
              ? "Microsoft resource enabled and queued for validation."
              : "Microsoft resource disabled."
          );
        }}
        onValidate={() => {
          if (!detail) return;
          void runDetailOperation(
            () => validateAdminMicrosoftResource(detail.id),
            "Resource validation submitted."
          );
        }}
      />
    </div>
  );
}
