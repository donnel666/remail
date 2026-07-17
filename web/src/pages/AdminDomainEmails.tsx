import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
} from "react";
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
  DATE_RANGE_DROPDOWN_CLASS,
  createDateRangePresets,
  createdFromISOString,
  createdToISOString,
  normalizeDateRangeValue,
  type DateRangeValue,
} from "./resources/date-range-filter";
import { useSelectionNotification } from "./resources/use-selection-notification";
import { getAdminDomainCapabilities } from "./admin-domains/admin-domain-access";
import {
  createAdminDomain,
  deleteAdminDomain,
  deleteAdminDomainsByFilter,
  deleteAdminDomainsByIds,
  disableAdminDomainsByIds,
  getAdminDomainDetail,
  listAdminDomainOwners,
  listAdminDomains,
  listAdminMailServers,
  recoverAdminDomain,
  setAdminDomainsPurposeByFilter,
  setAdminDomainsPurposeByIds,
  updateAdminDomain,
  validateAdminDomain,
  validateAdminDomainsByFilter,
  validateAdminDomainsByIds,
  type AdminDomainDetail,
  type AdminDomainItem,
  type AdminDomainListFilter,
  type AdminDomainListResponse,
  type AdminDomainOwner,
  type AdminDomainPurpose,
  type AdminDomainStatus,
  type AdminMailServer,
  type CreateAdminDomainRequest,
} from "./admin-domains/admin-domains-api";
import { DomainDetailSheet as DomainDetailSheetView } from "./admin-domains/domain-detail-sheet";
import {
  DomainOwnerIdentity as DomainOwnerIdentityView,
  formatDomainTime,
  renderDomainPurposeTag,
  renderDomainStatusTag,
} from "./admin-domains/domain-meta";
import {
  DomainFormModal as DomainFormModalView,
  type DomainDraft,
  type DomainEditorMode,
} from "./admin-domains/domain-modals";

type StatusFilter = "all" | AdminDomainStatus;
type PurposeFilter = "all" | AdminDomainPurpose;

// ---------- Page ----------

export default function AdminDomainEmails() {
  const { t } = useTranslation();
  const { currentUser } = useAuth();
  const isMobile = useIsMobile();
  const { canOperateDomains, canWriteDomains } = useMemo(
    () => getAdminDomainCapabilities(currentUser?.permissions ?? []),
    [currentUser?.permissions]
  );
  const [activeTld, setActiveTld] = useState("all");
  const [searchKeyword, setSearchKeyword] = useState("");
  const [createdAtRange, setCreatedAtRange] = useState<DateRangeValue>([]);
  const [statusFilter, setStatusFilter] = useState<StatusFilter>("all");
  const [purposeFilter, setPurposeFilter] = useState<PurposeFilter>("all");
  const [compactMode, setCompactMode] = useState(false);
  const [selectedKeys, setSelectedKeys] = useState<number[]>([]);
  const [activePage, setActivePage] = useState(1);
  const [pageSize, setPageSize] = useSharedPageSize();

  useEffect(() => setActivePage(1), [pageSize]);
  const [facets, setFacets] = useState<AdminDomainListResponse["facets"] | null>(
    null
  );

  const [owners, setOwners] = useState<AdminDomainOwner[]>([]);
  const [mailServers, setMailServers] = useState<AdminMailServer[]>([]);

  const [editorOpen, setEditorOpen] = useState(false);
  const [editorMode, setEditorMode] = useState<DomainEditorMode>("import");
  const [editorTarget, setEditorTarget] = useState<AdminDomainItem | null>(null);

  const [detail, setDetail] = useState<AdminDomainDetail | null>(null);
  const [detailLoading, setDetailLoading] = useState(false);
  const [detailBusy, setDetailBusy] = useState(false);
  const detailRequestIdRef = useRef(0);

  const [rowBusyId, setRowBusyId] = useState<number | null>(null);
  const [bulkBusy, setBulkBusy] = useState<
    "check" | "disable" | "delete" | "sale" | "private" | null
  >(null);

  const [debouncedSearchKeyword, flushSearchKeyword] =
    useDebouncedValue(searchKeyword);
  const dateRangePresets = useMemo(() => createDateRangePresets(t), [t]);

  useEffect(
    () => () => {
      detailRequestIdRef.current += 1;
    },
    []
  );

  useEffect(() => {
    if (canOperateDomains) return;
    setSelectedKeys([]);
  }, [canOperateDomains]);

  useEffect(() => {
    if (canWriteDomains) return;
    setEditorOpen(false);
    setEditorTarget(null);
  }, [canWriteDomains]);

  useEffect(() => {
    let cancelled = false;
    void (async () => {
      try {
        const [ownerList, serverList] = await Promise.all([
          listAdminDomainOwners(""),
          listAdminMailServers(),
        ]);
        if (cancelled) return;
        setOwners(ownerList);
        setMailServers(serverList);
      } catch {
        // owners/servers are only needed for the editor; ignore load errors
      }
    })();
    return () => {
      cancelled = true;
    };
  }, []);

  const statsFilter = useMemo<AdminDomainListFilter>(() => {
    const filter: AdminDomainListFilter = {};
    const search = debouncedSearchKeyword.trim();
    const createdFrom = createdFromISOString(createdAtRange);
    const createdTo = createdToISOString(createdAtRange);
    if (search) filter.search = search;
    if (statusFilter !== "all") filter.status = statusFilter;
    if (purposeFilter !== "all") filter.purpose = purposeFilter;
    if (createdFrom) filter.createdFrom = createdFrom;
    if (createdTo) filter.createdTo = createdTo;
    return filter;
  }, [createdAtRange, debouncedSearchKeyword, purposeFilter, statusFilter]);

  const listFilter = useMemo<AdminDomainListFilter>(() => {
    if (activeTld === "all") return statsFilter;
    return { ...statsFilter, tld: activeTld };
  }, [activeTld, statsFilter]);

  const loadDomainBlock = useCallback(
    async (offset: number, limit: number, cursor?: { afterId?: number }) => {
      const response = await listAdminDomains(
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
    loadedItems: items,
    loading,
    pagedItems,
    refresh: refreshList,
    total,
  } = useBlockPagedList<AdminDomainItem>({
    activePage,
    blockSize: 100,
    filterKey: JSON.stringify(listFilter),
    loadBlock: loadDomainBlock,
    onError: (error) => {
      Toast.error(getIamErrorMessage(t, error, "Admin domains load failed."));
    },
    pageSize,
  });

  const refreshStats = useCallback(async () => {
    try {
      const response = await listAdminDomains(statsFilter, 0, 1);
      setFacets(response.facets);
    } catch {
      // keep previous facets; the next refresh retries
    }
  }, [statsFilter]);

  useEffect(() => {
    void refreshStats();
  }, [refreshStats]);

  const refresh = useCallback(async () => {
    await Promise.all([refreshStats(), refreshList()]);
  }, [refreshList, refreshStats]);

  const tldCounts = useMemo(
    () => facets?.tlds?.map((item) => [item.key, item.count] as [string, number]) ?? [],
    [facets]
  );
  const tldSet = useMemo(() => new Set(tldCounts.map(([tld]) => tld)), [tldCounts]);
  // "All" spans every TLD for the current status/purpose scope, so it stays
  // consistent with the per-TLD tab counts and the list total (which reflect
  // the selected status, including the deleted view).
  const allTabCount = useMemo(
    () => (facets ? tldCounts.reduce((sum, [, count]) => sum + count, 0) : total),
    [facets, tldCounts, total]
  );

  useEffect(() => {
    if (facets && activeTld !== "all" && !tldSet.has(activeTld)) {
      setActiveTld("all");
    }
  }, [activeTld, facets, tldSet]);

  const stats = useMemo(() => {
    if (facets) return { status: facets.status, purpose: facets.purpose };
    return {
      status: {
        all: total,
        pending: 0,
        validating: 0,
        normal: 0,
        abnormal: 0,
        disabled: 0,
        deleted: 0,
      },
      purpose: { all: total, not_sale: 0, sale: 0, binding: 0 },
    };
  }, [facets, total]);

  const activeFilterCount =
    Number(statusFilter !== "all") + Number(purposeFilter !== "all");

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
    setPurposeFilter("all");
    setActiveTld("all");
    setActivePage(1);
    setSelectedKeys([]);
  };

  const applyStatusFilter = (value: StatusFilter) => {
    setStatusFilter(value);
    setActivePage(1);
    setSelectedKeys([]);
  };

  const applyPurposeFilter = (value: PurposeFilter) => {
    setPurposeFilter(value);
    setActivePage(1);
    setSelectedKeys([]);
  };

  // ----- single-row operations -----

  const runRowOperation = useCallback(
    async (id: number, action: () => Promise<unknown>, successKey: string) => {
      if (!canOperateDomains) return;
      setRowBusyId(id);
      try {
        await action();
        Toast.success(t(successKey));
        await refresh();
      } catch (error) {
        Toast.error(getIamErrorMessage(t, error, "Domain operation failed."));
      } finally {
        setRowBusyId(null);
      }
    },
    [canOperateDomains, refresh, t]
  );

  const handleValidate = useCallback(
    (record: AdminDomainItem) =>
      runRowOperation(
        record.id,
        () => validateAdminDomain(record.id),
        "Domain validation submitted."
      ),
    [runRowOperation]
  );

  const handleToggleDisabled = useCallback(
    (record: AdminDomainItem) =>
      runRowOperation(
        record.id,
        () =>
          updateAdminDomain(record.id, {
            status: record.status === "disabled" ? "abnormal" : "disabled",
          }),
        record.status === "disabled" ? "Domain enabled." : "Domain disabled."
      ),
    [runRowOperation]
  );

  const handleTogglePurpose = useCallback(
    (record: AdminDomainItem) =>
      runRowOperation(
        record.id,
        () =>
          updateAdminDomain(record.id, {
            purpose: record.purpose === "sale" ? "not_sale" : "sale",
          }),
        record.purpose === "sale"
          ? "Domain converted to private."
          : "Domain published for public sale."
      ),
    [runRowOperation]
  );

  const handleRecover = useCallback(
    (record: AdminDomainItem) =>
      runRowOperation(
        record.id,
        () => recoverAdminDomain(record.id),
        "Domain recovered."
      ),
    [runRowOperation]
  );

  const confirmDelete = useCallback(
    (record: AdminDomainItem) => {
      if (!canOperateDomains) return;
      Modal.confirm({
        title: t("Confirm delete"),
        content: t("Confirm delete domain content", { domain: record.domain }),
        okText: t("Delete"),
        okButtonProps: { type: "danger" },
        cancelText: t("Cancel"),
        onOk: () =>
          runRowOperation(
            record.id,
            () => deleteAdminDomain(record.id),
            "Domain deleted."
          ),
      });
    },
    [canOperateDomains, runRowOperation, t]
  );

  const openDetail = useCallback(
    async (id: number) => {
      const requestId = ++detailRequestIdRef.current;
      setDetailLoading(true);
      setDetailBusy(false);
      setDetail(null);
      try {
        const response = await getAdminDomainDetail(id);
        if (detailRequestIdRef.current !== requestId) return;
        setDetail(response);
      } catch (error) {
        if (detailRequestIdRef.current !== requestId) return;
        Toast.error(getIamErrorMessage(t, error, "Domain detail load failed."));
      } finally {
        if (detailRequestIdRef.current === requestId) {
          setDetailLoading(false);
        }
      }
    },
    [t]
  );

  const runDetailOperation = useCallback(
    async (action: () => Promise<unknown>, successKey: string) => {
      if (!canOperateDomains || !detail) return;
      const requestId = detailRequestIdRef.current;
      const detailId = detail.id;
      setDetailBusy(true);
      try {
        await action();
        Toast.success(t(successKey));
        const refreshed = await getAdminDomainDetail(detailId);
        if (detailRequestIdRef.current === requestId) {
          setDetail(refreshed);
        }
        await refresh();
      } catch (error) {
        Toast.error(getIamErrorMessage(t, error, "Domain operation failed."));
      } finally {
        if (detailRequestIdRef.current === requestId) {
          setDetailBusy(false);
        }
      }
    },
    [canOperateDomains, detail, refresh, t]
  );

  const openImport = () => {
    if (!canWriteDomains) return;
    setEditorMode("import");
    setEditorTarget(null);
    setEditorOpen(true);
  };

  const openEdit = (record: AdminDomainItem) => {
    if (!canWriteDomains) return;
    setEditorMode("edit");
    setEditorTarget(record);
    setEditorOpen(true);
  };

  const handleEditorSubmit = async (draft: DomainDraft) => {
    if (!canWriteDomains) return;
    try {
      if (editorMode === "import") {
        const payload: CreateAdminDomainRequest = {
          domain: draft.domain,
          ownerId: draft.ownerId as number,
          purpose: draft.purpose,
          mailServerId: draft.mailServerId,
        };
        await createAdminDomain(payload);
        Toast.success(t("Domain imported."));
        setActivePage(1);
      } else if (editorTarget) {
        await updateAdminDomain(editorTarget.id, {
          ownerId: draft.ownerId,
          purpose: draft.purpose,
          status: draft.status,
          mailServerId: draft.mailServerId,
        });
        Toast.success(t("Domain updated."));
      }
      setEditorOpen(false);
      setEditorTarget(null);
      await refresh();
    } catch (error) {
      Toast.error(
        getIamErrorMessage(
          t,
          error,
          editorMode === "import" ? "Domain import failed." : "Domain update failed."
        )
      );
    }
  };

  // ----- batch operations -----

  const selectedActiveIds = useMemo(() => {
    if (!canOperateDomains) return [];
    const selected = new Set(selectedKeys);
    return items
      .filter((item) => selected.has(item.id) && item.status !== "deleted")
      .map((item) => item.id);
  }, [canOperateDomains, items, selectedKeys]);

  const clearSelection = useCallback(() => setSelectedKeys([]), []);

  const queueSelectedChecks = useCallback(async () => {
    if (!canOperateDomains) return;
    if (selectedActiveIds.length === 0) {
      Toast.info(t("No domains to check."));
      return;
    }
    setBulkBusy("check");
    try {
      const response = await validateAdminDomainsByIds(selectedActiveIds);
      Toast.success(t("Domain validations submitted.", { count: response.queued }));
      setSelectedKeys([]);
    } catch (error) {
      Toast.error(getIamErrorMessage(t, error, "Domain operation failed."));
    } finally {
      setBulkBusy(null);
    }
  }, [canOperateDomains, selectedActiveIds, t]);

  const confirmDisableSelected = useCallback(() => {
    if (!canOperateDomains) return;
    if (selectedActiveIds.length === 0) {
      Toast.info(t("No domains to disable."));
      return;
    }
    Modal.confirm({
      title: t("Confirm disable selected"),
      content: t("Confirm disable selected domains content", {
        count: selectedActiveIds.length,
      }),
      okText: t("Disable"),
      cancelText: t("Cancel"),
      onOk: async () => {
        if (!canOperateDomains) return;
        setBulkBusy("disable");
        try {
          const response = await disableAdminDomainsByIds(selectedActiveIds);
          Toast.success(
            t("Domains bulk operation completed.", { count: response.affected })
          );
          setSelectedKeys([]);
          await refresh();
        } catch (error) {
          Toast.error(getIamErrorMessage(t, error, "Domain operation failed."));
        } finally {
          setBulkBusy(null);
        }
      },
    });
  }, [canOperateDomains, refresh, selectedActiveIds, t]);

  const confirmDeleteSelected = useCallback(() => {
    if (!canOperateDomains) return;
    if (selectedActiveIds.length === 0) {
      Toast.info(t("No domains to delete."));
      return;
    }
    Modal.confirm({
      title: t("Confirm delete selected"),
      content: t("Confirm delete selected domains content", {
        count: selectedActiveIds.length,
      }),
      okText: t("Delete"),
      okButtonProps: { type: "danger" },
      cancelText: t("Cancel"),
      onOk: async () => {
        if (!canOperateDomains) return;
        setBulkBusy("delete");
        try {
          await deleteAdminDomainsByIds(selectedActiveIds);
          Toast.success(t("Domains bulk operation submitted."));
          setSelectedKeys([]);
          await refresh();
        } catch (error) {
          Toast.error(getIamErrorMessage(t, error, "Domain operation failed."));
        } finally {
          setBulkBusy(null);
        }
      },
    });
  }, [canOperateDomains, refresh, selectedActiveIds, t]);

  const runBulkPurpose = useCallback(
    async (purpose: "not_sale" | "sale") => {
      if (!canOperateDomains) return;
      if (selectedActiveIds.length === 0) {
        Toast.info(t("No domains selected."));
        return;
      }
      setBulkBusy(purpose === "sale" ? "sale" : "private");
      try {
        await setAdminDomainsPurposeByIds(selectedActiveIds, purpose);
        Toast.success(t("Domains bulk operation submitted."));
        setSelectedKeys([]);
        await refresh();
      } catch (error) {
        Toast.error(getIamErrorMessage(t, error, "Domain operation failed."));
      } finally {
        setBulkBusy(null);
      }
    },
    [canOperateDomains, refresh, selectedActiveIds, t]
  );

  const confirmCheckAll = useCallback(() => {
    if (!canOperateDomains) return;
    Modal.confirm({
      title: t("Confirm check all"),
      content: t("Confirm check all domains content"),
      okText: t("Check"),
      cancelText: t("Cancel"),
      onOk: async () => {
        if (!canOperateDomains) return;
        setBulkBusy("check");
        try {
          await validateAdminDomainsByFilter(listFilter);
          Toast.success(t("Resource validation submitted."));
          setSelectedKeys([]);
        } catch (error) {
          Toast.error(getIamErrorMessage(t, error, "Domain operation failed."));
        } finally {
          setBulkBusy(null);
        }
      },
    });
  }, [canOperateDomains, listFilter, t]);

  const confirmDeleteAll = useCallback(() => {
    if (!canOperateDomains) return;
    Modal.confirm({
      title: t("Confirm delete all"),
      content: t("Confirm delete all domains content"),
      okText: t("Delete"),
      okButtonProps: { type: "danger" },
      cancelText: t("Cancel"),
      onOk: async () => {
        if (!canOperateDomains) return;
        setBulkBusy("delete");
        try {
          await deleteAdminDomainsByFilter(listFilter);
          Toast.success(t("Domains bulk operation submitted."));
          setSelectedKeys([]);
          setActivePage(1);
          await refresh();
        } catch (error) {
          Toast.error(getIamErrorMessage(t, error, "Domain operation failed."));
        } finally {
          setBulkBusy(null);
        }
      },
    });
  }, [canOperateDomains, listFilter, refresh, t]);

  const confirmSetPurposeAll = useCallback(
    (purpose: "not_sale" | "sale") => {
      if (!canOperateDomains) return;
      if (total === 0) {
        Toast.info(t("No domains to update."));
        return;
      }
      const isPublic = purpose === "sale";
      Modal.confirm({
        title: t(
          isPublic ? "Confirm put all domains on sale" : "Confirm convert all domains to private"
        ),
        content: t(
          isPublic
            ? "Confirm put all matching domains on sale"
            : "Confirm convert all matching domains to private",
          { count: total }
        ),
        okText: isPublic ? t("Put on sale") : t("Convert to private"),
        cancelText: t("Cancel"),
        onOk: async () => {
          if (!canOperateDomains) return;
          setBulkBusy(isPublic ? "sale" : "private");
          try {
            await setAdminDomainsPurposeByFilter(listFilter, purpose);
            Toast.success(t("Domains bulk operation submitted."));
            setSelectedKeys([]);
            await refresh();
          } catch (error) {
            Toast.error(getIamErrorMessage(t, error, "Domain operation failed."));
          } finally {
            setBulkBusy(null);
          }
        },
      });
    },
    [canOperateDomains, listFilter, refresh, t, total]
  );

  useSelectionNotification({
    selectedCount: canOperateDomains ? selectedKeys.length : 0,
    onCheck: canOperateDomains ? () => void queueSelectedChecks() : undefined,
    onClear: clearSelection,
    onDelete: canOperateDomains ? confirmDeleteSelected : undefined,
    onSell: canOperateDomains ? confirmDisableSelected : undefined,
    extraActions: canOperateDomains
      ? [
          {
            key: "sale",
            labelKey: "Put on sale",
            loading: bulkBusy === "sale",
            onClick: () => void runBulkPurpose("sale"),
            type: "secondary",
          },
          {
            key: "private",
            labelKey: "Convert to private",
            loading: bulkBusy === "private",
            onClick: () => void runBulkPurpose("not_sale"),
            type: "tertiary",
          },
        ]
      : undefined,
    checkLabelKey: "Check",
    deleteLabelKey: "Delete",
    sellLabelKey: "Disable",
    checkLoading: bulkBusy === "check",
    deleteLoading: bulkBusy === "delete",
    sellLoading: bulkBusy === "disable",
    selectionDescriptionKey: "Selected domains",
    t,
  });

  // ----- row action menu -----

  const renderRowActions = useCallback(
    (record: AdminDomainItem) => {
      const rowLoading = rowBusyId === record.id;
      if (record.status === "deleted") {
        return (
          <Space spacing={4} wrap={false}>
            <Button onClick={() => void openDetail(record.id)} size="small" type="tertiary">
              {t("Details")}
            </Button>
            <Button
              disabled={!canOperateDomains}
              loading={rowLoading}
              onClick={
                canOperateDomains
                  ? () => void handleRecover(record)
                  : undefined
              }
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
            disabled={rowLoading}
            onClick={() => void openDetail(record.id)}
            size="small"
            type="tertiary"
          >
            {t("Details")}
          </Button>
          <Button
            disabled={!canWriteDomains || rowLoading}
            onClick={canWriteDomains ? () => openEdit(record) : undefined}
            size="small"
            type="tertiary"
          >
            {t("Edit")}
          </Button>
          <Button
            disabled={!canOperateDomains}
            loading={rowLoading}
            onClick={
              canOperateDomains
                ? () => void handleValidate(record)
                : undefined
            }
            size="small"
            type="tertiary"
          >
            {t("Check")}
          </Button>
          <Button
            disabled={!canOperateDomains}
            loading={rowLoading}
            onClick={
              canOperateDomains
                ? () => void handleToggleDisabled(record)
                : undefined
            }
            size="small"
            type="tertiary"
          >
            {record.status === "disabled" ? t("Enable") : t("Disable")}
          </Button>
          <Button
            disabled={!canOperateDomains || rowLoading}
            onClick={
              canOperateDomains
                ? () => void handleTogglePurpose(record)
                : undefined
            }
            size="small"
            type="tertiary"
          >
            {record.purpose === "sale"
              ? t("Convert to private")
              : t("Put on sale")}
          </Button>
          <Button
            disabled={!canOperateDomains}
            loading={rowLoading}
            onClick={
              canOperateDomains ? () => confirmDelete(record) : undefined
            }
            size="small"
            type="danger"
          >
            {t("Delete")}
          </Button>
        </Space>
      );
    },
    [
      canOperateDomains,
      canWriteDomains,
      confirmDelete,
      handleRecover,
      handleToggleDisabled,
      handleTogglePurpose,
      handleValidate,
      openDetail,
      rowBusyId,
      t,
    ]
  );

  const columns = useMemo(
    () =>
      [
        {
          dataIndex: "domainTld",
          key: "tld",
          title: t("TLD"),
          width: 90,
          render: (value: unknown) => (
            <Tag color="white" shape="circle">
              {String(value)}
            </Tag>
          ),
        },
        {
          dataIndex: "domain",
          key: "domain",
          title: t("Domain"),
          width: 220,
          render: (value: unknown) => (
            <CopyableTableText copiedText={t("Copied")} text={String(value)} />
          ),
        },
        {
          dataIndex: "mailServerId",
          key: "mailServer",
          title: t("Mail server"),
          width: 220,
          render: (value: unknown) => {
            const server = mailServers.find(
              (item) => item.id === Number(value)
            );
            return server ? (
              <div className="min-w-0">
                <div className="truncate text-sm text-[var(--semi-color-text-0)]">
                  {server.name}
                </div>
                <div className="truncate font-mono text-xs text-[var(--semi-color-text-2)]">
                  {server.mxRecord}
                </div>
              </div>
            ) : (
              <span className="text-[var(--semi-color-text-3)]">#{String(value)}</span>
            );
          },
        },
        {
          dataIndex: "ownerEmail",
          key: "owner",
          title: t("Owner"),
          width: 280,
          render: (_: unknown, record: AdminDomainItem) => (
            <DomainOwnerIdentityView owner={record} t={t} />
          ),
        },
        {
          dataIndex: "purpose",
          key: "purpose",
          title: t("Purpose"),
          width: 110,
          render: (value: unknown) =>
            renderDomainPurposeTag(value as AdminDomainPurpose, t),
        },
        {
          dataIndex: "status",
          key: "status",
          title: t("Status"),
          width: 100,
          render: (value: unknown, record: AdminDomainItem) =>
            renderDomainStatusTag(
              value as AdminDomainStatus,
              t,
              record.lastSafeError
            ),
        },
        {
          dataIndex: "mailboxCount",
          key: "mailboxCount",
          title: t("Mailboxes"),
          width: 100,
          render: (value: unknown) => (
            <span className="tabular-nums font-medium text-[var(--semi-color-text-1)]">
              {Number(value)}
            </span>
          ),
        },
        {
          dataIndex: "createdAt",
          key: "createdAt",
          title: t("Created at"),
          width: 170,
          render: (value: unknown) => (
            <span className="text-xs text-[var(--semi-color-text-2)]">
              {formatDomainTime(String(value))}
            </span>
          ),
        },
        {
          dataIndex: "operate",
          key: "operate",
          title: t("Action"),
          width: 360,
          fixed: "right",
          render: (_: unknown, record: AdminDomainItem) => renderRowActions(record),
        },
      ] as any[],
    [mailServers, renderRowActions, t]
  );

  const tableColumns = useMemo(() => {
    if (!compactMode) return columns;
    return columns.map((column) => {
      if (column.dataIndex !== "operate") return column;
      const { fixed: _fixed, ...rest } = column;
      return rest;
    });
  }, [columns, compactMode]);

  const rowSelection = canOperateDomains
    ? {
        selectedRowKeys: selectedKeys,
        onChange: (keys: Array<string | number>) => {
          if (!canOperateDomains) return;
          setSelectedKeys(keys.map((key) => Number(key)));
        },
      }
    : undefined;

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
              {allTabCount}
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
          disabled={!canWriteDomains}
          onClick={canWriteDomains ? openImport : undefined}
        >
          {t("Import")}
        </Button>
        <Button
          type="tertiary"
          size="small"
          className="remail-toolbar-fixed-button flex-1 md:flex-none"
          loading={loading}
          onClick={() => void refresh()}
        >
          {t("Refresh")}
        </Button>
        <Tooltip content={t("Check all")} mouseEnterDelay={0} mouseLeaveDelay={0.05} position="top">
          <Button
            type="tertiary"
            size="small"
            className="flex-1 md:flex-initial"
            disabled={!canOperateDomains}
            loading={bulkBusy === "check"}
            onClick={canOperateDomains ? confirmCheckAll : undefined}
          >
            {t("Check")}
          </Button>
        </Tooltip>
        <Tooltip content={t("Put all domains on sale")} mouseEnterDelay={0} mouseLeaveDelay={0.05} position="top">
          <Button
            type="tertiary"
            size="small"
            className="flex-1 md:flex-initial"
            disabled={!canOperateDomains}
            loading={bulkBusy === "sale"}
            onClick={
              canOperateDomains
                ? () => confirmSetPurposeAll("sale")
                : undefined
            }
          >
            {t("Put on sale")}
          </Button>
        </Tooltip>
        <Tooltip content={t("Convert all domains to private")} mouseEnterDelay={0} mouseLeaveDelay={0.05} position="top">
          <Button
            type="tertiary"
            size="small"
            className="flex-1 md:flex-initial"
            disabled={!canOperateDomains}
            loading={bulkBusy === "private"}
            onClick={
              canOperateDomains
                ? () => confirmSetPurposeAll("not_sale")
                : undefined
            }
          >
            {t("Convert to private")}
          </Button>
        </Tooltip>
        <Tooltip content={t("Delete all")} mouseEnterDelay={0} mouseLeaveDelay={0.05} position="top">
          <Button
            type="danger"
            size="small"
            className="flex-1 md:flex-initial"
            disabled={!canOperateDomains}
            loading={bulkBusy === "delete"}
            onClick={canOperateDomains ? confirmDeleteAll : undefined}
          >
            {t("Delete")}
          </Button>
        </Tooltip>
        <CompactModeToggle compactMode={compactMode} setCompactMode={setCompactMode} t={t} />
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
                  active={statusFilter === "pending"}
                  count={stats.status.pending}
                  label={t("Pending")}
                  onSelect={applyStatusFilter}
                  value="pending"
                />
                <StatisticFilterOption
                  active={statusFilter === "validating"}
                  count={stats.status.validating}
                  label={t("Validating")}
                  onSelect={applyStatusFilter}
                  value="validating"
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
                <StatisticFilterOption
                  active={statusFilter === "deleted"}
                  count={stats.status.deleted}
                  label={t("Deleted")}
                  onSelect={applyStatusFilter}
                  value="deleted"
                />
              </div>

              <div className="px-2 pb-1 text-xs font-medium text-[var(--semi-color-text-2)]">
                {t("Purpose")}
              </div>
              <div className="space-y-1">
                <StatisticFilterOption
                  active={purposeFilter === "all"}
                  count={stats.purpose.all}
                  label={t("All")}
                  onSelect={applyPurposeFilter}
                  value="all"
                />
                <StatisticFilterOption
                  active={purposeFilter === "not_sale"}
                  count={stats.purpose.not_sale}
                  label={t("Not for sale")}
                  onSelect={applyPurposeFilter}
                  value="not_sale"
                />
                <StatisticFilterOption
                  active={purposeFilter === "sale"}
                  count={stats.purpose.sale}
                  label={t("Sale")}
                  onSelect={applyPurposeFilter}
                  value="sale"
                />
                <StatisticFilterOption
                  active={purposeFilter === "binding"}
                  count={stats.purpose.binding}
                  label={t("Binding")}
                  onSelect={applyPurposeFilter}
                  value="binding"
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
            {activeFilterCount > 0 ? `${t("Filters")} (${activeFilterCount})` : t("Filters")}
          </Button>
        </Dropdown>
        <Input
          name="admin-domain-search"
          prefix={<IconSearch />}
          placeholder={t("Search domain, owner or ID")}
          showClear
          size="small"
          value={searchKeyword}
          style={{ width: isMobile ? "100%" : 240 }}
          onChange={(value) => {
            setSearchKeyword(String(value));
            setActivePage(1);
            setSelectedKeys([]);
          }}
          className="resources-search-input w-full md:w-60"
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
              darkModeImage={<IllustrationNoResultDark style={{ height: 150, width: 150 }} />}
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
          scroll={{ x: "max(100%, 1880px)", y: DESKTOP_TABLE_SCROLL_Y }}
          size="middle"
        />
      </CardPro>

      {canWriteDomains ? (
        <DomainFormModalView
          mailServers={mailServers}
          mode={editorMode}
          onCancel={() => {
            setEditorOpen(false);
            setEditorTarget(null);
          }}
          onSubmit={handleEditorSubmit}
          owners={owners}
          target={editorTarget}
          visible={editorOpen}
        />
      ) : null}

      <DomainDetailSheetView
        busy={detailBusy}
        detail={detail}
        loading={detailLoading}
        onCancel={() => {
          detailRequestIdRef.current += 1;
          setDetail(null);
          setDetailLoading(false);
          setDetailBusy(false);
        }}
        onDelete={
          canOperateDomains
            ? () => {
                if (!canOperateDomains || !detail) return;
                Modal.confirm({
                  title: t("Confirm delete"),
                  content: t("Confirm delete domain content", {
                    domain: detail.domain,
                  }),
                  okText: t("Delete"),
                  okButtonProps: { type: "danger" },
                  cancelText: t("Cancel"),
                  onOk: () =>
                    runDetailOperation(
                      () => deleteAdminDomain(detail.id),
                      "Domain deleted."
                    ),
                });
              }
            : undefined
        }
        onEdit={
          canWriteDomains
            ? () => {
                if (!canWriteDomains || !detail) return;
                openEdit(detail);
              }
            : undefined
        }
        onRecover={
          canOperateDomains
            ? () =>
                runDetailOperation(
                  () => recoverAdminDomain(detail!.id),
                  "Domain recovered."
                )
            : undefined
        }
        onTogglePurpose={
          canOperateDomains
            ? () =>
                runDetailOperation(
                  () =>
                    updateAdminDomain(detail!.id, {
                      purpose:
                        detail!.purpose === "sale" ? "not_sale" : "sale",
                    }),
                  detail!.purpose === "sale"
                    ? "Domain converted to private."
                    : "Domain published for public sale."
                )
            : undefined
        }
        onToggleDisabled={
          canOperateDomains
            ? () =>
                runDetailOperation(
                  () =>
                    updateAdminDomain(detail!.id, {
                      status:
                        detail!.status === "disabled"
                          ? "abnormal"
                          : "disabled",
                    }),
                  detail!.status === "disabled"
                    ? "Domain enabled."
                    : "Domain disabled."
                )
            : undefined
        }
        onValidate={
          canOperateDomains
            ? () =>
                runDetailOperation(
                  () => validateAdminDomain(detail!.id),
                  "Domain validation submitted."
                )
            : undefined
        }
      />
    </div>
  );
}
