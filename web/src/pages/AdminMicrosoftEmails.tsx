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
import { useBlockPagedList } from "@/hooks/use-block-paged-list";
import { useDebouncedValue } from "@/hooks/use-debounced-value";
import { useIsMobile } from "@/hooks/use-is-mobile";
import { useSharedPageSize } from "@/hooks/use-shared-page-size";
import { getIamErrorMessage } from "@/lib/iam-errors";
import {
  deleteAdminMicrosoftResource,
  deleteAdminMicrosoftResourcesByFilter,
  deleteAdminMicrosoftResourcesByIds,
  disableAdminMicrosoftResource,
  disableAdminMicrosoftResourcesByIds,
  enableAdminMicrosoftResource,
  getAdminMicrosoftResourceDetail,
  listAdminMicrosoftOwners,
  listAdminMicrosoftResources,
  publishAdminMicrosoftResource,
  recoverAdminMicrosoftResource,
  setAdminMicrosoftResourcesForSaleByFilter,
  setAdminMicrosoftResourcesForSaleByIds,
  unpublishAdminMicrosoftResource,
  type AdminMicrosoftBulkCommandResponse,
} from "@/lib/admin-microsoft-api";

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
  MicrosoftBulkMaintenanceModal,
  type MicrosoftBulkMaintenanceTarget,
} from "./admin-microsoft/microsoft-bulk-maintenance-modal";
import { MicrosoftMaintenanceModal } from "./admin-microsoft/microsoft-maintenance-modal";
import type {
  AdminMicrosoftFacets,
  AdminMicrosoftListFilter,
  AdminMicrosoftOwner,
  AdminMicrosoftResourceDetail,
  AdminMicrosoftResourceItem,
  AdminMicrosoftResourceStatus,
  AdminMicrosoftTokenHealth,
} from "./admin-microsoft/admin-microsoft-types";

type StatusFilter = "all" | AdminMicrosoftResourceStatus;
type BooleanFilter = "all" | "yes" | "no";
type TokenHealthFilter = "all" | AdminMicrosoftTokenHealth;

function bulkOutcome(response: AdminMicrosoftBulkCommandResponse) {
  if ("affected" in response) {
    return {
      reasonCounts: response.reasonCounts,
      skipped: response.skipped,
      succeeded: response.affected,
    };
  }
  return {
    reasonCounts: response.task.progress?.reasonCounts ?? [],
    skipped: response.task.progress?.skipped ?? 0,
    succeeded: response.task.progress?.succeeded ?? 0,
  };
}

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

  useEffect(() => setActivePage(1), [pageSize]);
  const [facets, setFacets] = useState<AdminMicrosoftFacets | null>(null);
  const [owners, setOwners] = useState<AdminMicrosoftOwner[]>([]);

  const [importOpen, setImportOpen] = useState(false);
  const [editTarget, setEditTarget] = useState<AdminMicrosoftResourceItem | null>(null);
  const [maintenanceTarget, setMaintenanceTarget] =
    useState<AdminMicrosoftResourceItem | null>(null);
  const [bulkMaintenanceTarget, setBulkMaintenanceTarget] =
    useState<MicrosoftBulkMaintenanceTarget | null>(null);
  const [credentialsTarget, setCredentialsTarget] =
    useState<AdminMicrosoftResourceItem | null>(null);
  const [detail, setDetail] = useState<AdminMicrosoftResourceDetail | null>(null);
  const [detailLoading, setDetailLoading] = useState(false);
  const [detailBusy, setDetailBusy] = useState(false);
  const detailRequestRef = useRef<AbortController | null>(null);
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
    void listAdminMicrosoftOwners("", controller.signal)
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
  const listFilterKey = JSON.stringify(listFilter);

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
        meta: response.facets,
        nextAfterId: response.nextAfterId,
        total: response.total,
      };
    },
    [listFilter]
  );

  const acceptMicrosoftBlock = useCallback(
    (response: { meta?: AdminMicrosoftFacets }) => {
      if (response.meta) setFacets(response.meta);
    },
    []
  );

  const {
    loading,
    pagedItems,
    refresh: refreshList,
    total,
  } = useBlockPagedList<AdminMicrosoftResourceItem, AdminMicrosoftFacets>({
    activePage,
    blockSize: 100,
    filterKey: listFilterKey,
    loadBlock: loadMicrosoftBlock,
    onError: (error) => {
      Toast.error(getIamErrorMessage(t, error, "Admin Microsoft resources load failed."));
    },
    onLoaded: acceptMicrosoftBlock,
    pageSize,
  });

  const refreshOpenDetail = useCallback(async (resourceId?: number) => {
    const id = resourceId ?? detail?.id;
    if (!id) return;
    detailRequestRef.current?.abort();
    const controller = new AbortController();
    detailRequestRef.current = controller;
    try {
      const nextDetail = await getAdminMicrosoftResourceDetail(id, controller.signal);
      if (!controller.signal.aborted) setDetail(nextDetail);
    } finally {
      if (detailRequestRef.current === controller) detailRequestRef.current = null;
    }
  }, [detail?.id]);

  const refreshAfterMutation = useCallback(
    async (resourceId?: number) => {
      try {
        await refreshList();
        if (resourceId) await refreshOpenDetail(resourceId);
      } catch (error) {
        Toast.error(
          getIamErrorMessage(t, error, "Admin Microsoft resources load failed.")
        );
      }
    },
    [refreshList, refreshOpenDetail, t]
  );

  const showBulkOutcome = useCallback(
    (response: AdminMicrosoftBulkCommandResponse, successKey: string) => {
      const outcome = bulkOutcome(response);
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
      const nextDetail = await getAdminMicrosoftResourceDetail(
        resourceId,
        controller.signal
      );
      if (!controller.signal.aborted) setDetail(nextDetail);
    } catch (error) {
      if (controller.signal.aborted) return;
      Toast.error(getIamErrorMessage(t, error, "Microsoft resource detail load failed."));
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
      graphAvailable: { all: total, no: 0, yes: 0 },
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
        Toast.error(getIamErrorMessage(t, error, "Microsoft resource operation failed."));
      } finally {
        setRowBusy(null);
      }
    },
    [detail?.id, refreshAfterMutation, t]
  );

  const handleToggleDisabled = useCallback(
    (record: AdminMicrosoftResourceItem) =>
      runRowOperation(
        record,
        "toggle",
        () =>
          record.status === "disabled"
            ? enableAdminMicrosoftResource(record.id, record.version)
            : disableAdminMicrosoftResource(record.id, record.version),
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
        () => recoverAdminMicrosoftResource(record.id, record.version),
        "Microsoft resource recovered and queued for validation."
      ),
    [runRowOperation]
  );

  const handleTogglePublish = useCallback(
    (record: AdminMicrosoftResourceItem) =>
      runRowOperation(
        record,
        "publish",
        () =>
          record.forSale
            ? unpublishAdminMicrosoftResource(record.id, record.version)
            : publishAdminMicrosoftResource(record.id, record.version),
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
            () => deleteAdminMicrosoftResource(record.id, record.version),
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
        await refreshAfterMutation(detail.id);
      } catch (error) {
        Toast.error(getIamErrorMessage(t, error, "Microsoft resource operation failed."));
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
      content: t("Confirm disable selected Microsoft resources", {
        count: selectedKeys.length,
      }),
      okText: t("Disable"),
      onOk: async () => {
        setBulkBusy("disable");
        try {
          const response = await disableAdminMicrosoftResourcesByIds(selectedKeys);
          showBulkOutcome(response, "Microsoft resources disabled.");
          setSelectedKeys([]);
          await refreshAfterMutation();
        } catch (error) {
          Toast.error(getIamErrorMessage(t, error, "Microsoft resource operation failed."));
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
      content: t("Confirm delete selected Microsoft resources", {
        count: selectedKeys.length,
      }),
      okButtonProps: { type: "danger" },
      okText: t("Delete"),
      onOk: async () => {
        setBulkBusy("delete");
        try {
          const response = await deleteAdminMicrosoftResourcesByIds(selectedKeys);
          showBulkOutcome(response, "Microsoft resources deleted.");
          setSelectedKeys([]);
          setActivePage(1);
          await refreshAfterMutation();
        } catch (error) {
          Toast.error(getIamErrorMessage(t, error, "Microsoft resource operation failed."));
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
      content: t("Confirm delete all matching Microsoft resources", { count: total }),
      okButtonProps: { type: "danger" },
      okText: t("Delete"),
      onOk: async () => {
        setBulkBusy("delete");
        try {
          const response = await deleteAdminMicrosoftResourcesByFilter(listFilter);
          showBulkOutcome(response, "Microsoft resources deleted.");
          setSelectedKeys([]);
          setActivePage(1);
          await refreshAfterMutation();
        } catch (error) {
          Toast.error(getIamErrorMessage(t, error, "Microsoft resource operation failed."));
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
        const response = await setAdminMicrosoftResourcesForSaleByIds(
          selectedKeys,
          forSale
        );
        showBulkOutcome(
          response,
          forSale
            ? "Microsoft resources published for public sale."
            : "Microsoft resources converted to private."
        );
        setSelectedKeys([]);
        await refreshAfterMutation();
      } catch (error) {
        Toast.error(getIamErrorMessage(t, error, "Microsoft resource operation failed."));
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
            showBulkOutcome(
              response,
              forSale
                ? "Microsoft resources published for public sale."
                : "Microsoft resources converted to private."
            );
            setSelectedKeys([]);
            await refreshAfterMutation();
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
              owner={record.owner}
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
            renderStatusTag(
              value as AdminMicrosoftResourceStatus,
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
          width: 360,
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
          onClick={() => void refreshList()}
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
    <div className="console-content-width py-5">
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
          await refreshAfterMutation();
        }}
        owners={owners}
        visible={importOpen}
      />

      <EditMicrosoftModal
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

      <MicrosoftMaintenanceModal
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

      <MicrosoftBulkMaintenanceModal
        onCancel={() => setBulkMaintenanceTarget(null)}
        onCompleted={async () => {
          setSelectedKeys([]);
          await refreshAfterMutation();
        }}
        target={bulkMaintenanceTarget}
      />

      <MicrosoftDetailSheet
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
            content: t("Confirm delete Microsoft resource content", {
              email: detail.emailAddress,
            }),
            okButtonProps: { type: "danger" },
            okText: t("Delete"),
            onOk: () =>
              runDetailOperation(
            () => deleteAdminMicrosoftResource(detail.id, detail.version),
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
            () => recoverAdminMicrosoftResource(detail.id, detail.version),
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
              detail.forSale
                ? unpublishAdminMicrosoftResource(detail.id, detail.version)
                : publishAdminMicrosoftResource(detail.id, detail.version),
            detail.forSale
              ? "Microsoft resource converted to private."
              : "Microsoft resource published for public sale."
          );
        }}
        onToggleDisabled={() => {
          if (!detail) return;
          void runDetailOperation(
            () =>
              detail.status === "disabled"
                ? enableAdminMicrosoftResource(detail.id, detail.version)
                : disableAdminMicrosoftResource(detail.id, detail.version),
            detail.status === "disabled"
              ? "Microsoft resource enabled and queued for validation."
              : "Microsoft resource disabled."
          );
        }}
        onMaintain={() => {
          if (detail) setMaintenanceTarget(detail);
        }}
      />
    </div>
  );
}
