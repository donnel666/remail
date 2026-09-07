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
import { AdminUserSelect, ownersWithCurrentUserFirst } from "@/components/semi/admin-user-select";
import { useAuth } from "@/context/auth-provider";
import { useBlockPagedList } from "@/hooks/use-block-paged-list";
import { useDebouncedValue } from "@/hooks/use-debounced-value";
import { useIsMobile } from "@/hooks/use-is-mobile";
import { useSharedPageSize } from "@/hooks/use-shared-page-size";
import { getIamErrorMessage } from "@/lib/iam-errors";
import { IamApiError } from "@/lib/api-client";
import {
  deleteAdminProtoResource,
  deleteAdminProtoResourcesByFilter,
  deleteAdminProtoResourcesByIds,
  disableAdminProtoResource,
  disableAdminProtoResourcesByIds,
  enableAdminProtoResource,
  getAdminProtoResourceDetail,
  listAdminProtoOwners,
  listAdminProtoResources,
  publishAdminProtoResource,
  recoverAdminProtoResource,
  setAdminProtoResourcesForSaleByFilter,
  setAdminProtoResourcesForSaleByIds,
  unpublishAdminProtoResource,
  type AdminProtoBulkCommandResponse,
} from "@/lib/admin-proto-api";

import {
  DATE_RANGE_DROPDOWN_CLASS,
  createDateRangePresets,
  createdFromISOString,
  createdToISOString,
  normalizeDateRangeValue,
  type DateRangeValue,
} from "./resources/date-range-filter";
import { ProtoBulkTaskProgress } from "./resources/proto-bulk-task-progress";
import { useSelectionNotification } from "./resources/use-selection-notification";
import {
  OwnerIdentity,
  STATUS_META,
  formatTime,
  renderStatusTag,
} from "./admin-proto/proto-meta";
import {
  EditProtoModal,
  ImportProtoModal,
  ReplaceCredentialsModal,
} from "./admin-proto/proto-modals";
import { ProtoDetailSheet } from "./admin-proto/proto-detail-sheet";
import {
  ProtoBulkMaintenanceModal,
  type ProtoBulkMaintenanceTarget,
} from "./admin-proto/proto-bulk-maintenance-modal";
import { ProtoMaintenanceModal } from "./admin-proto/proto-maintenance-modal";
import type {
  AdminProtoFacets,
  AdminProtoListFilter,
  AdminProtoOwner,
  AdminProtoResourceDetail,
  AdminProtoResourceItem,
  AdminProtoResourceStatus,
} from "./admin-proto/admin-proto-types";

type StatusFilter = "all" | AdminProtoResourceStatus;
type BooleanFilter = "all" | "yes" | "no";

export default function AdminProtoEmails() {
  const { t } = useTranslation();
  const { currentUser } = useAuth();
  const isMobile = useIsMobile();
  const [bulkTaskId, setBulkTaskId] = useState<string | null>(null);

  const [activeSuffix, setActiveSuffix] = useState("all");
  const [ownerFilter, setOwnerFilter] = useState<number | undefined>();
  const [searchKeyword, setSearchKeyword] = useState("");
  const [createdAtRange, setCreatedAtRange] = useState<DateRangeValue>([]);
  const [statusFilter, setStatusFilter] = useState<StatusFilter>("all");
  const [privateFilter, setPrivateFilter] = useState<BooleanFilter>("all");
  const [longLivedFilter, setLongLivedFilter] =
    useState<BooleanFilter>("all");
  const [compactMode, setCompactMode] = useState(false);
  const [selectedKeys, setSelectedKeys] = useState<number[]>([]);
  const [activePage, setActivePage] = useState(1);
  const [pageSize, setPageSize] = useSharedPageSize();

  useEffect(() => setActivePage(1), [pageSize]);
  const [facets, setFacets] = useState<AdminProtoFacets | null>(null);
  const [owners, setOwners] = useState<AdminProtoOwner[]>([]);
  const importOwners = useMemo(
    () => ownersWithCurrentUserFirst(owners, currentUser),
    [currentUser, owners]
  );

  const [importOpen, setImportOpen] = useState(false);
  const [editTarget, setEditTarget] = useState<AdminProtoResourceItem | null>(null);
  const [maintenanceTarget, setMaintenanceTarget] =
    useState<AdminProtoResourceItem | null>(null);
  const [bulkMaintenanceTarget, setBulkMaintenanceTarget] =
    useState<ProtoBulkMaintenanceTarget | null>(null);
  const [credentialsTarget, setCredentialsTarget] =
    useState<AdminProtoResourceItem | null>(null);
  const [detail, setDetail] = useState<AdminProtoResourceDetail | null>(null);
  const [detailLoading, setDetailLoading] = useState(false);
  const [detailBusy, setDetailBusy] = useState(false);
  const detailRequestRef = useRef<AbortController | null>(null);
  const statsRequestRef = useRef<AbortController | null>(null);
  const [rowBusy, setRowBusy] = useState<{
    action: "check" | "delete" | "publish" | "recover" | "toggle";
    id: number;
  } | null>(null);
  const [bulkBusy, setBulkBusy] = useState<
    "disable" | "delete" | "publish" | "private" | null
  >(null);

  const [debouncedSearchKeyword, flushSearchKeyword] =
    useDebouncedValue(searchKeyword);
  const dateRangePresets = useMemo(() => createDateRangePresets(t), [t]);

  useEffect(() => {
    const controller = new AbortController();
    void listAdminProtoOwners("", controller.signal)
      .then((items) => {
        if (!controller.signal.aborted) setOwners(items);
      })
      .catch(() => {
        // Owner choices are optional UI data; the resource list remains usable.
      });
    return () => controller.abort();
  }, []);

  useEffect(
    () => () => {
      detailRequestRef.current?.abort();
    },
    []
  );

  const statsFilter = useMemo<AdminProtoListFilter>(() => {
    const filter: AdminProtoListFilter = {};
    const search = debouncedSearchKeyword.trim();
    const createdFrom = createdFromISOString(createdAtRange);
    const createdTo = createdToISOString(createdAtRange);
    if (search) filter.search = search;
    if (ownerFilter !== undefined) filter.ownerId = ownerFilter;
    if (statusFilter !== "all") filter.status = statusFilter;
    if (privateFilter !== "all") filter.forSale = privateFilter === "no";
    if (longLivedFilter !== "all") filter.longLived = longLivedFilter === "yes";
    if (createdFrom) filter.createdFrom = createdFrom;
    if (createdTo) filter.createdTo = createdTo;
    return filter;
  }, [
    createdAtRange,
    ownerFilter,
    debouncedSearchKeyword,
    longLivedFilter,
    privateFilter,
    statusFilter,
  ]);

  const listFilter = useMemo<AdminProtoListFilter>(() => {
    if (activeSuffix === "all") return statsFilter;
    return { ...statsFilter, suffix: activeSuffix };
  }, [activeSuffix, statsFilter]);
  const listFilterKey = JSON.stringify(listFilter);

  const loadProtoBlock = useCallback(
    async (
      offset: number,
      limit: number,
      cursor: { afterId?: number } | undefined,
      signal: AbortSignal
    ) => {
      const response = await listAdminProtoResources(
        listFilter,
        offset,
        limit,
        cursor?.afterId,
        { includeFacets: false, includeTotal: false, signal }
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
    loaded,
    loading,
    pagedItems,
    refresh: refreshList,
    setTotal: setListTotal,
    total,
  } = useBlockPagedList<AdminProtoResourceItem>({
    activePage,
    blockSize: 100,
    filterKey: listFilterKey,
    loadBlock: loadProtoBlock,
    onError: (error) => {
      Toast.error(getIamErrorMessage(t, error, "Admin Proto resources load failed."));
    },
    pageSize,
  });

  const refreshStats = useCallback(async () => {
    statsRequestRef.current?.abort();
    const controller = new AbortController();
    statsRequestRef.current = controller;
    try {
      const response = await listAdminProtoResources(
        listFilter,
        0,
        1,
        undefined,
        { includeFacets: true, signal: controller.signal }
      );
      if (controller.signal.aborted) return;
      setFacets(response.facets ?? null);
      if (response.total !== undefined) setListTotal(response.total);
    } catch {
      // The next refresh retries stats; aborted and failed requests never replace current data.
    } finally {
      if (statsRequestRef.current === controller) statsRequestRef.current = null;
    }
  }, [listFilter, setListTotal]);

  useEffect(() => {
    setFacets(null);
    if (!loaded) return;
    void refreshStats();
    return () => {
      statsRequestRef.current?.abort();
      statsRequestRef.current = null;
    };
  }, [loaded, refreshStats]);

  const refresh = useCallback(async () => {
    await refreshList();
  }, [refreshList]);

  const refreshOpenDetail = useCallback(async (resourceId?: number) => {
    const id = resourceId ?? detail?.id;
    if (!id) return;
    detailRequestRef.current?.abort();
    const controller = new AbortController();
    detailRequestRef.current = controller;
    try {
      const nextDetail = await getAdminProtoResourceDetail(id, controller.signal);
      if (!controller.signal.aborted) setDetail(nextDetail);
    } finally {
      if (detailRequestRef.current === controller) detailRequestRef.current = null;
    }
  }, [detail?.id]);

  const refreshAfterMutation = useCallback(
    async (resourceId?: number) => {
      try {
        await refresh();
        if (resourceId) await refreshOpenDetail(resourceId);
      } catch (error) {
        Toast.error(
          getIamErrorMessage(t, error, "Admin Proto resources load failed.")
        );
      }
    },
    [refresh, refreshOpenDetail, t]
  );

  const showBulkOutcome = useCallback(
    (response: AdminProtoBulkCommandResponse, successKey: string) => {
      if (response.taskId) setBulkTaskId(response.taskId);
      if (response.taskId && response.status !== "succeeded") {
        Toast.success(t("Proto resources bulk operation submitted."));
        return;
      }
      const outcome = { succeeded: response.affected, skipped: response.skipped, reasonCounts: response.reasonCounts ?? [] };
      Toast.success(t(successKey, { count: outcome.succeeded }));
      if (outcome.skipped === 0) return;
      const reasons = outcome.reasonCounts
        .map((item) => `${item.reason}: ${item.count}`)
        .join(", ");
      Toast.warning(
        `${t("Succeeded")}: ${outcome.succeeded}/${outcome.succeeded + outcome.skipped}` +
          (reasons ? ` · ${t("Reason")}: ${reasons}` : "")
      );
    },
    [t]
  );

  const openDetail = useCallback(async (resourceId: number) => {
    detailRequestRef.current?.abort();
    const controller = new AbortController();
    detailRequestRef.current = controller;
    setDetailLoading(true);
    setDetail(null);
    try {
      const nextDetail = await getAdminProtoResourceDetail(
        resourceId,
        controller.signal
      );
      if (!controller.signal.aborted) setDetail(nextDetail);
    } catch (error) {
      if (controller.signal.aborted) return;
      Toast.error(getIamErrorMessage(t, error, "Proto resource detail load failed."));
    } finally {
      if (detailRequestRef.current === controller) {
        detailRequestRef.current = null;
        setDetailLoading(false);
      }
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
      forSale: { all: total, no: 0, yes: 0 },
      longLived: { all: total, no: 0, yes: 0 },
      status: {
        abnormal: 0,
        all: total,
        deleted: 0,
        disabled: 0,
        normal: 0,
        pending: 0,
        validating: 0,
        identifying: 0,
      },
      suffixes: [],
    } satisfies AdminProtoFacets;
  }, [facets, total]);

  const allTabCount = suffixCounts.reduce((sum, [, count]) => sum + count, 0);
  const activeFilterCount =
    Number(statusFilter !== "all") +
    Number(privateFilter !== "all") +
    Number(longLivedFilter !== "all") + Number(ownerFilter !== undefined);

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
    setOwnerFilter(undefined);
    setLongLivedFilter("all");
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
      record: AdminProtoResourceItem,
      action: "delete" | "publish" | "recover" | "toggle",
      operation: () => Promise<unknown>,
      successKey: string
    ) => {
      setRowBusy({ action, id: record.id });
      try {
        await operation();
        Toast.success(t(successKey));
        await refreshAfterMutation(detail?.id === record.id ? record.id : undefined);
      } catch (error) {
        Toast.error(getIamErrorMessage(t, error, "Proto resource operation failed."));
        if (error instanceof IamApiError && error.code === "resource_version_conflict") await refreshAfterMutation(detail?.id === record.id ? record.id : undefined);
      } finally {
        setRowBusy(null);
      }
    },
    [detail?.id, refreshAfterMutation, t]
  );

  const handleToggleDisabled = useCallback(
    (record: AdminProtoResourceItem) =>
      runRowOperation(
        record,
        "toggle",
        () =>
          record.status === "disabled"
            ? enableAdminProtoResource(record.id, record.version)
            : disableAdminProtoResource(record.id, record.version),
        record.status === "disabled"
          ? "Proto resource enabled and queued for validation."
          : "Proto resource disabled."
      ),
    [runRowOperation]
  );

  const handleRecover = useCallback(
    (record: AdminProtoResourceItem) =>
      runRowOperation(
        record,
        "recover",
        () => recoverAdminProtoResource(record.id, record.version),
        "Proto resource recovered and queued for validation."
      ),
    [runRowOperation]
  );

  const handleTogglePublish = useCallback(
    (record: AdminProtoResourceItem) =>
      runRowOperation(
        record,
        "publish",
        () =>
          record.forSale
            ? unpublishAdminProtoResource(record.id, record.version)
            : publishAdminProtoResource(record.id, record.version),
        record.forSale
          ? "Proto resource converted to private."
          : "Proto resource published for public sale."
      ),
    [runRowOperation]
  );

  const confirmDelete = useCallback(
    (record: AdminProtoResourceItem) => {
      Modal.confirm({
        cancelText: t("Cancel"),
        content: t("Confirm delete Proto resource content", {
          email: record.emailAddress,
        }),
        okButtonProps: { type: "danger" },
        okText: t("Delete"),
        onOk: () =>
          runRowOperation(
            record,
            "delete",
            () => deleteAdminProtoResource(record.id, record.version),
            "Proto resource deleted."
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
        await refreshAfterMutation(detail.id);
      } catch (error) {
        Toast.error(getIamErrorMessage(t, error, "Proto resource operation failed."));
        if (error instanceof IamApiError && error.code === "resource_version_conflict") await refreshAfterMutation(detail.id);
      } finally {
        setDetailBusy(false);
      }
    },
    [detail, refreshAfterMutation, t]
  );

  const openSelectedMaintenance = useCallback(() => {
    if (selectedKeys.length === 0) return;
    setBulkMaintenanceTarget({
      count: selectedKeys.length,
      mode: "ids",
      resourceIds: [...selectedKeys],
    });
  }, [selectedKeys]);

  const confirmDisableSelected = useCallback(() => {
    if (selectedKeys.length === 0) return;
    Modal.confirm({
      cancelText: t("Cancel"),
      content: t("Confirm disable selected Proto resources", {
        count: selectedKeys.length,
      }),
      okText: t("Disable"),
      onOk: async () => {
        setBulkBusy("disable");
        try {
          const response = await disableAdminProtoResourcesByIds(selectedKeys);
          showBulkOutcome(response, "Proto resources disabled.");
          setSelectedKeys([]);
          await refreshAfterMutation();
        } catch (error) {
          Toast.error(getIamErrorMessage(t, error, "Proto resource operation failed."));
        } finally {
          setBulkBusy(null);
        }
      },
      title: t("Confirm disable selected"),
    });
  }, [refreshAfterMutation, selectedKeys, showBulkOutcome, t]);

  const confirmDeleteSelected = useCallback(() => {
    if (selectedKeys.length === 0) return;
    Modal.confirm({
      cancelText: t("Cancel"),
      content: t("Confirm delete selected Proto resources", {
        count: selectedKeys.length,
      }),
      okButtonProps: { type: "danger" },
      okText: t("Delete"),
      onOk: async () => {
        setBulkBusy("delete");
        try {
          const response = await deleteAdminProtoResourcesByIds(selectedKeys);
          showBulkOutcome(response, "Proto resources deleted.");
          setSelectedKeys([]);
          setActivePage(1);
          await refreshAfterMutation();
        } catch (error) {
          Toast.error(getIamErrorMessage(t, error, "Proto resource operation failed."));
        } finally {
          setBulkBusy(null);
        }
      },
      title: t("Confirm delete selected"),
    });
  }, [refreshAfterMutation, selectedKeys, showBulkOutcome, t]);

  const openAllMaintenance = useCallback(() => {
    if (total === 0) {
      Toast.info(t("No resources to check."));
      return;
    }
    setBulkMaintenanceTarget({
      count: total,
      filter: { ...listFilter },
      mode: "filter",
    });
  }, [listFilter, t, total]);

  const confirmDeleteAll = useCallback(() => {
    if (total === 0) {
      Toast.info(t("No resources to check."));
      return;
    }
    Modal.confirm({
      cancelText: t("Cancel"),
      content: t("Confirm delete all matching Proto resources", { count: total }),
      okButtonProps: { type: "danger" },
      okText: t("Delete"),
      onOk: async () => {
        setBulkBusy("delete");
        try {
          const response = await deleteAdminProtoResourcesByFilter(listFilter);
          showBulkOutcome(response, "Proto resources deleted.");
          setSelectedKeys([]);
          setActivePage(1);
          await refreshAfterMutation();
        } catch (error) {
          Toast.error(getIamErrorMessage(t, error, "Proto resource operation failed."));
        } finally {
          setBulkBusy(null);
        }
      },
      title: t("Confirm delete all"),
    });
  }, [listFilter, refreshAfterMutation, showBulkOutcome, t, total]);

  const runBulkForSale = useCallback(
    async (forSale: boolean) => {
      if (selectedKeys.length === 0) return;
      setBulkBusy(forSale ? "publish" : "private");
      try {
        const response = await setAdminProtoResourcesForSaleByIds(
          selectedKeys,
          forSale
        );
        showBulkOutcome(
          response,
          forSale
            ? "Proto resources published for public sale."
            : "Proto resources converted to private."
        );
        setSelectedKeys([]);
        await refreshAfterMutation();
      } catch (error) {
        Toast.error(getIamErrorMessage(t, error, "Proto resource operation failed."));
      } finally {
        setBulkBusy(null);
      }
    },
    [refreshAfterMutation, selectedKeys, showBulkOutcome, t]
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
            ? "Confirm put all matching Proto resources on sale"
            : "Confirm convert all matching Proto resources to private",
          { count: total }
        ),
        okText: forSale ? t("Put on sale") : t("Convert to private"),
        onOk: async () => {
          setBulkBusy(forSale ? "publish" : "private");
          try {
            const response = await setAdminProtoResourcesForSaleByFilter(
              listFilter,
              forSale
            );
            showBulkOutcome(
              response,
              forSale
                ? "Proto resources published for public sale."
                : "Proto resources converted to private."
            );
            setSelectedKeys([]);
            await refreshAfterMutation();
          } catch (error) {
            Toast.error(
              getIamErrorMessage(t, error, "Proto resource operation failed.")
            );
          } finally {
            setBulkBusy(null);
          }
        },
        title: forSale ? t("Confirm put all on sale") : t("Confirm convert all to private"),
      });
    },
    [listFilter, refreshAfterMutation, showBulkOutcome, t, total]
  );

  useSelectionNotification({
    checkLabelKey: "Maintenance",
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
    onCheck: openSelectedMaintenance,
    onClear: () => setSelectedKeys([]),
    onDelete: confirmDeleteSelected,
    onSell: confirmDisableSelected,
    selectedCount: selectedKeys.length,
    selectionDescriptionKey: "Selected Proto resources",
    sellLabelKey: "Disable",
    sellLoading: bulkBusy === "disable",
    t,
  });

  const renderRowActions = useCallback(
    (record: AdminProtoResourceItem) => {
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
            disabled={Boolean(busyAction)}
            onClick={() => setMaintenanceTarget(record)}
            size="small"
            type="tertiary"
          >
            {t("Maintenance")}
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
      openDetail,
      rowBusy,
      setEditTarget,
      setMaintenanceTarget,
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
          dataIndex: "ownerEmail",
          key: "owner",
          title: t("Owner"),
          width: 310,
          render: (_: unknown, record: AdminProtoResourceItem) => (
            <OwnerIdentity
              owner={record.owner}
              ownerId={record.ownerUserId}
              t={t}
            />
          ),
        },
        {
          dataIndex: "status",
          key: "status",
          title: t("Status"),
          width: 120,
          render: (value: unknown, record: AdminProtoResourceItem) =>
            renderStatusTag(
              value as AdminProtoResourceStatus,
              t,
              record.lastSafeError ?? undefined
            ),
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
          dataIndex: "operate",
          fixed: "right",
          key: "operate",
          title: t("Action"),
          width: 360,
          render: (_: unknown, record: AdminProtoResourceItem) => renderRowActions(record),
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
        <Tooltip content={t("Maintain all")} mouseEnterDelay={0} mouseLeaveDelay={0.05} position="top">
          <Button
            className="flex-1 md:flex-initial"
            onClick={openAllMaintenance}
            size="small"
            type="tertiary"
          >
            {t("Maintenance")}
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
                {(["all", "pending", "validating", "identifying", "normal", "abnormal", "disabled", "deleted"] as StatusFilter[]).map(
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

        <AdminUserSelect
          showClear
          value={ownerFilter}
          onChange={(value) => { setOwnerFilter(value); resetPageAndSelection(); }}
          options={owners.map((owner) => ({ value: owner.id, label: owner.email, data: owner }))}
          loadOptions={async (search) => (await listAdminProtoOwners(search)).map((owner) => ({ value: owner.id, label: owner.email, data: owner }))}
          placeholder={t("Owner")}
          emptyContent={t("No users found")}
          style={{ width: isMobile ? "100%" : 190 }}
        />
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
    <div className="console-content-width py-5">
      <ProtoBulkTaskProgress admin taskId={bulkTaskId} onCompleted={refresh} onClose={() => setBulkTaskId(null)} />
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
              description={t("No Proto resources found")}
              image={<IllustrationNoResult style={{ height: 150, width: 150 }} />}
              style={{ padding: 30 }}
            />
          }
          hidePagination
          loading={loading}
          pagination={false}
          rowKey="id"
          rowSelection={rowSelection}
          scroll={{ x: "max(100%, 1430px)", y: DESKTOP_TABLE_SCROLL_Y }}
          size="middle"
        />
      </CardPro>

      <ImportProtoModal
        onCancel={() => setImportOpen(false)}
        onImported={async () => {
          setActivePage(1);
          setSelectedKeys([]);
          await refreshAfterMutation();
        }}
        owners={importOwners}
        visible={importOpen}
      />

      <EditProtoModal
        onCancel={() => setEditTarget(null)}
        onSaved={async () => {
          await refreshAfterMutation(
            editTarget && detail?.id === editTarget.id ? editTarget.id : undefined
          );
        }}
        owners={owners}
        target={editTarget}
      />

      <ReplaceCredentialsModal
        onCancel={() => setCredentialsTarget(null)}
        onSaved={async (nextDetail) => {
          await refreshAfterMutation();
          if (detail?.id === nextDetail.id) setDetail(nextDetail);
        }}
        target={credentialsTarget}
      />

      <ProtoMaintenanceModal
        onCancel={() => setMaintenanceTarget(null)}
        onCompleted={async () => {
          await refreshAfterMutation(
            maintenanceTarget && detail?.id === maintenanceTarget.id
              ? maintenanceTarget.id
              : undefined
          );
        }}
        target={maintenanceTarget}
      />

      <ProtoBulkMaintenanceModal
        onCancel={() => setBulkMaintenanceTarget(null)}
        onCompleted={async (taskId) => {
          if (taskId) setBulkTaskId(taskId);
          setSelectedKeys([]);
          await refreshAfterMutation();
        }}
        target={bulkMaintenanceTarget}
      />

      <ProtoDetailSheet
        busy={detailBusy}
        detail={detail}
        loading={detailLoading}
        onCancel={() => {
          detailRequestRef.current?.abort();
          detailRequestRef.current = null;
          setDetail(null);
          setDetailLoading(false);
        }}
        onRefresh={async () => {
          await refreshAfterMutation(detail?.id);
        }}
        onDelete={() => {
          if (!detail) return;
          Modal.confirm({
            cancelText: t("Cancel"),
            content: t("Confirm delete Proto resource content", {
              email: detail.emailAddress,
            }),
            okButtonProps: { type: "danger" },
            okText: t("Delete"),
            onOk: () =>
              runDetailOperation(
            () => deleteAdminProtoResource(detail.id, detail.version),
                "Proto resource deleted."
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
            () => recoverAdminProtoResource(detail.id, detail.version),
            "Proto resource recovered and queued for validation."
          );
        }}
        onReplaceCredentials={() => {
          if (detail) setCredentialsTarget(detail);
        }}
        onTogglePublish={() => {
          if (!detail) return;
          void runDetailOperation(
            () =>
              detail.forSale
                ? unpublishAdminProtoResource(detail.id, detail.version)
                : publishAdminProtoResource(detail.id, detail.version),
            detail.forSale
              ? "Proto resource converted to private."
              : "Proto resource published for public sale."
          );
        }}
        onToggleDisabled={() => {
          if (!detail) return;
          void runDetailOperation(
            () =>
              detail.status === "disabled"
                ? enableAdminProtoResource(detail.id, detail.version)
                : disableAdminProtoResource(detail.id, detail.version),
            detail.status === "disabled"
              ? "Proto resource enabled and queued for validation."
              : "Proto resource disabled."
          );
        }}
        onMaintain={() => {
          if (detail) setMaintenanceTarget(detail);
        }}
      />
    </div>
  );
}
