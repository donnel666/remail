import { useCallback, useMemo, useState, type ReactNode } from "react";
import {
  Button,
  Dropdown,
  Input,
  Modal,
  Space,
  Toast,
  Tooltip,
} from "@douyinfe/semi-ui";
import { IconSearch } from "@douyinfe/semi-icons";
import { SlidersHorizontal } from "lucide-react";
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
import { hasPermission, useAuth } from "@/context/auth-provider";
import { useBlockPagedList } from "@/hooks/use-block-paged-list";
import { useDebouncedValue } from "@/hooks/use-debounced-value";
import { useIsMobile } from "@/hooks/use-is-mobile";
import { useSharedPageSize } from "@/hooks/use-shared-page-size";
import { getIamErrorMessage } from "@/lib/iam-errors";
import { useSelectionNotification } from "../resources/use-selection-notification";
import {
  FINANCE_USER_GROUP_NAMES,
  FINANCE_USER_ROLES,
  listMockFinanceInvites,
  setMockFinanceInviteEnabled,
  setMockFinanceInvitesEnabled,
  setMockFinanceInvitesEnabledByFilter,
  type FinanceEnabledFilter,
  type FinanceInvite,
  type FinanceInviteFacets,
  type FinanceOwnerRoleFilter,
  type FinanceUserRole,
} from "./admin-finance-mock";
import { InviteDetailSheet } from "./invite-detail-sheet";
import { InviteAccountCell } from "./invite-meta";
import {
  CreateInviteModal,
  EditInviteModal,
} from "./invite-modals";
import { formatDateTime, renderEnabledTag } from "./finance-meta";
import { emptyNode } from "./finance-shared";

const SEARCH_DEBOUNCE_MS = 400;

// Same role labels admin-microsoft uses in its owner cell.
const INVITE_ROLE_LABELS: Record<FinanceUserRole, string> = {
  user: "User",
  supplier: "Supplier",
  admin: "Admin",
  super_admin: "Super Admin",
};

export function InvitesPanel({ tabsArea }: { tabsArea: ReactNode }) {
  const { t } = useTranslation();
  const { currentUser } = useAuth();
  const canWrite = hasPermission(currentUser, "iam:invite", "write");
  const isMobile = useIsMobile();
  const [pageSize, setPageSize] = useSharedPageSize();
  const [activePage, setActivePage] = useState(1);
  const [searchKeyword, setSearchKeyword] = useState("");
  const [debouncedSearch, flushSearch] = useDebouncedValue(
    searchKeyword,
    SEARCH_DEBOUNCE_MS
  );
  const [roleFilter, setRoleFilter] =
    useState<FinanceOwnerRoleFilter>("all");
  const [groupFilter, setGroupFilter] = useState<string>("all");
  const [enabledFilter, setEnabledFilter] =
    useState<FinanceEnabledFilter>("all");
  const [compactMode, setCompactMode] = useState(false);
  const [selectedRowKeys, setSelectedRowKeys] = useState<Array<string | number>>(
    []
  );
  const [createOpen, setCreateOpen] = useState(false);
  const [editTarget, setEditTarget] = useState<FinanceInvite | null>(null);
  const [detailTarget, setDetailTarget] = useState<FinanceInvite | null>(null);
  const [rowBusyCode, setRowBusyCode] = useState<string | null>(null);
  const [bulkBusy, setBulkBusy] = useState<
    "enable-all" | "disable-all" | "enable-sel" | "disable-sel" | null
  >(null);
  const [facets, setFacets] = useState<FinanceInviteFacets>({
    role: { all: 0, user: 0, supplier: 0, admin: 0, super_admin: 0 },
    group: { all: 0 },
    enabled: { all: 0, enabled: 0, disabled: 0 },
  });

  const listFilter = useMemo(
    () => ({
      search: debouncedSearch.trim() || undefined,
      ownerRole: roleFilter === "all" ? undefined : roleFilter,
      ownerGroupName: groupFilter === "all" ? undefined : groupFilter,
      enabled:
        enabledFilter === "all" ? undefined : enabledFilter === "enabled",
    }),
    [debouncedSearch, enabledFilter, groupFilter, roleFilter]
  );

  const loadBlock = useCallback(
    async (offset: number, limit: number) => {
      const result = await listMockFinanceInvites(listFilter, offset, limit);
      return {
        items: result.items,
        meta: result.facets,
        total: result.total,
      };
    },
    [listFilter]
  );

  const { pagedItems, total, loading, refresh, updateLoadedItems } =
    useBlockPagedList<FinanceInvite, FinanceInviteFacets>({
      activePage,
      filterKey: JSON.stringify(listFilter),
      loadBlock,
      onError: (error) =>
        Toast.error(getIamErrorMessage(t, error, "Operation failed.")),
      onLoaded: (response) => {
        if (response.meta) setFacets(response.meta);
      },
      pageSize,
    });

  const safePage = Math.min(
    activePage,
    Math.max(1, Math.ceil(Math.max(total, 1) / pageSize))
  );

  // Selection bar acts on the checked rows (like admin-microsoft's bottom bar).
  const runEnableSelected = async (enabled: boolean) => {
    if (!canWrite || !selectedRowKeys.length) return;
    setBulkBusy(enabled ? "enable-sel" : "disable-sel");
    try {
      const result = await setMockFinanceInvitesEnabled(
        selectedRowKeys.map(String),
        enabled
      );
      Toast.success(
        t("Users bulk operation completed.", {
          count: result.affected,
          skipped: result.skipped,
        })
      );
      setSelectedRowKeys([]);
      void refresh();
    } catch (error) {
      Toast.error(getIamErrorMessage(t, error, "Operation failed."));
    } finally {
      setBulkBusy(null);
    }
  };

  // Toolbar acts on ALL filtered rows with a confirm (like confirmValidateAll).
  const confirmEnableAll = (enabled: boolean) => {
    if (!canWrite || total === 0) {
      Toast.info(t("No invite codes to update."));
      return;
    }
    Modal.confirm({
      cancelText: t("Cancel"),
      content: t(
        enabled
          ? "Confirm enable all matching invite codes"
          : "Confirm disable all matching invite codes",
        { count: total }
      ),
      okText: enabled ? t("Enable") : t("Disable"),
      onOk: async () => {
        setBulkBusy(enabled ? "enable-all" : "disable-all");
        try {
          const result = await setMockFinanceInvitesEnabledByFilter(
            listFilter,
            enabled
          );
          Toast.success(
            t("Users bulk operation completed.", {
              count: result.affected,
              skipped: result.skipped,
            })
          );
          setSelectedRowKeys([]);
          void refresh();
        } catch (error) {
          Toast.error(getIamErrorMessage(t, error, "Operation failed."));
        } finally {
          setBulkBusy(null);
        }
      },
      title: enabled ? t("Confirm enable all") : t("Confirm disable all"),
    });
  };

  const toggleInviteEnabled = async (record: FinanceInvite) => {
    if (!canWrite) return;
    setRowBusyCode(record.code);
    try {
      const updated = await setMockFinanceInviteEnabled(
        record.code,
        !record.enabled
      );
      updateLoadedItems((items) =>
        items.map((item) => (item.code === updated.code ? updated : item))
      );
      setDetailTarget((current) =>
        current?.code === updated.code ? updated : current
      );
      Toast.success(
        updated.enabled ? t("Invite code enabled.") : t("Invite code disabled.")
      );
    } catch (error) {
      Toast.error(getIamErrorMessage(t, error, "Operation failed."));
    } finally {
      setRowBusyCode(null);
    }
  };

  useSelectionNotification({
    selectedCount: selectedRowKeys.length,
    onClear: () => setSelectedRowKeys([]),
    onCheck: canWrite ? () => void runEnableSelected(true) : undefined,
    checkLabelKey: "Enable",
    checkLoading: bulkBusy === "enable-sel",
    onSell: canWrite ? () => void runEnableSelected(false) : undefined,
    sellLabelKey: "Disable",
    sellLoading: bulkBusy === "disable-sel",
    selectionDescriptionKey: "Selected resources",
    t,
  });

  const columns = useMemo(
    () => [
      {
        title: t("Invite code"),
        dataIndex: "code",
        width: 180,
        render: (value: string) => <CopyableTableText copiedText={t("Copied")} text={value} />,
      },
      {
        title: t("Status"),
        dataIndex: "enabled",
        width: 110,
        render: (value: boolean) => renderEnabledTag(value, t),
      },
      {
        title: t("Usage"),
        dataIndex: "used",
        width: 120,
        render: (_: number, record: FinanceInvite) => (
          <span className="font-mono-data">
            {record.used}/
            {record.maxUse >= 2147483647 ? "∞" : record.maxUse}
          </span>
        ),
      },
      {
        title: t("Owner"),
        dataIndex: "ownerEmail",
        width: 260,
        render: (_: string | null | undefined, record: FinanceInvite) => (
          <InviteAccountCell
            email={record.ownerEmail}
            groupName={record.ownerGroupName}
            nickname={record.ownerNickname}
            role={record.ownerRole}
            t={t}
            userId={record.ownerUserId}
          />
        ),
      },
      {
        title: t("Expire at"),
        dataIndex: "expireAt",
        width: 170,
        render: (value?: string | null) => formatDateTime(value),
      },
      {
        title: t("Created at"),
        dataIndex: "createdAt",
        width: 170,
        render: (value: string) => formatDateTime(value),
      },
      {
        title: t("Actions"),
        dataIndex: "operate",
        fixed: compactMode ? undefined : ("right" as const),
        width: 220,
        render: (_: unknown, record: FinanceInvite) => (
          <Space spacing={4} wrap={false}>
            <Button
              onClick={() => setDetailTarget(record)}
              size="small"
              type="tertiary"
            >
              {t("Details")}
            </Button>
            <Button
              disabled={!canWrite}
              loading={rowBusyCode === record.code}
              onClick={() => void toggleInviteEnabled(record)}
              size="small"
              type="tertiary"
            >
              {record.enabled ? t("Disable") : t("Enable")}
            </Button>
            <Button
              disabled={!canWrite}
              onClick={() => setEditTarget(record)}
              size="small"
              type="tertiary"
            >
              {t("Edit")}
            </Button>
          </Space>
        ),
      },
    ] as any[],
    [canWrite, compactMode, rowBusyCode, t]
  );

  const tableColumns = useMemo(() => {
    if (!compactMode) return columns;
    return columns.map((column) => {
      if (column.dataIndex !== "operate") return column;
      const { fixed: _fixed, ...rest } = column;
      return rest;
    });
  }, [columns, compactMode]);

  const actionsArea = (
    <div className="flex w-full flex-col items-center justify-between gap-2 md:flex-row">
      <div className="order-2 flex w-full flex-wrap gap-2 md:order-1 md:w-auto">
        <Button
          className="flex-1 md:flex-initial"
          disabled={!canWrite}
          onClick={() => setCreateOpen(true)}
          size="small"
          type="primary"
        >
          {t("Create")}
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
        <Tooltip
          content={t("Enable all")}
          mouseEnterDelay={0}
          mouseLeaveDelay={0.05}
          position="top"
        >
          <Button
            className="flex-1 md:flex-initial"
            disabled={!canWrite}
            loading={bulkBusy === "enable-all"}
            onClick={() => confirmEnableAll(true)}
            size="small"
            type="tertiary"
          >
            {t("Enable")}
          </Button>
        </Tooltip>
        <Tooltip
          content={t("Disable all")}
          mouseEnterDelay={0}
          mouseLeaveDelay={0.05}
          position="top"
        >
          <Button
            className="flex-1 md:flex-initial"
            disabled={!canWrite}
            loading={bulkBusy === "disable-all"}
            onClick={() => confirmEnableAll(false)}
            size="small"
            type="tertiary"
          >
            {t("Disable")}
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
                {t("Owner role")}
              </div>
              <div className="mb-2 space-y-1">
                <StatisticFilterOption
                  active={roleFilter === "all"}
                  count={facets.role.all}
                  label={t("All")}
                  onSelect={() => {
                    setRoleFilter("all");
                    setActivePage(1);
                  }}
                  value="all"
                />
                {FINANCE_USER_ROLES.map((role) => (
                  <StatisticFilterOption
                    active={roleFilter === role}
                    count={facets.role[role]}
                    key={role}
                    label={t(INVITE_ROLE_LABELS[role])}
                    onSelect={() => {
                      setRoleFilter(role);
                      setActivePage(1);
                    }}
                    value={role}
                  />
                ))}
              </div>
              <div className="px-2 pb-1 text-xs font-medium text-[var(--semi-color-text-2)]">
                {t("Owner group")}
              </div>
              <div className="mb-2 space-y-1">
                <StatisticFilterOption
                  active={groupFilter === "all"}
                  count={facets.group.all ?? 0}
                  label={t("All")}
                  onSelect={() => {
                    setGroupFilter("all");
                    setActivePage(1);
                  }}
                  value="all"
                />
                {FINANCE_USER_GROUP_NAMES.map((group) => (
                  <StatisticFilterOption
                    active={groupFilter === group}
                    count={facets.group[group] ?? 0}
                    key={group}
                    label={group}
                    onSelect={() => {
                      setGroupFilter(group);
                      setActivePage(1);
                    }}
                    value={group}
                  />
                ))}
              </div>
              <div className="px-2 pb-1 text-xs font-medium text-[var(--semi-color-text-2)]">
                {t("Status")}
              </div>
              <div className="space-y-1">
                {(
                  [
                    ["all", t("All")],
                    ["enabled", t("Enabled")],
                    ["disabled", t("Disabled")],
                  ] as const
                ).map(([value, label]) => (
                  <StatisticFilterOption
                    active={enabledFilter === value}
                    count={facets.enabled[value]}
                    key={value}
                    label={label}
                    onSelect={() => {
                      setEnabledFilter(value);
                      setActivePage(1);
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
            {t("Filters")}
          </Button>
        </Dropdown>
        <Input
          className="resources-search-input w-full md:w-56"
          onChange={(value) => {
            setSearchKeyword(String(value));
            setActivePage(1);
          }}
          onEnterPress={() => {
            flushSearch();
            setActivePage(1);
          }}
          placeholder={t("Search invite code or owner")}
          prefix={<IconSearch />}
          showClear
          size="small"
          style={{ width: isMobile ? "100%" : 224 }}
          value={searchKeyword}
        />
        <Button
          className="remail-toolbar-fixed-button flex-1 md:flex-none"
          onClick={() => {
            flushSearch();
            setActivePage(1);
          }}
          size="small"
          type="tertiary"
        >
          {t("Query")}
        </Button>
      </div>
    </div>
  );

  return (
    <>
      <CardPro
        actionsArea={actionsArea}
        paginationArea={createCardProPagination({
          currentPage: safePage,
          isMobile,
          onPageChange: (page) => {
            setActivePage(page);
            setSelectedRowKeys([]);
          },
          onPageSizeChange: (size) => {
            setPageSize(size);
            setActivePage(1);
            setSelectedRowKeys([]);
          },
          pageSize,
          total,
          t,
        })}
        t={t}
        tabsArea={tabsArea}
        type="type3"
      >
        <CardTable
          className="overflow-hidden rounded-xl"
          columns={tableColumns}
          dataSource={pagedItems}
          empty={emptyNode(t("No invite codes found"))}
          hidePagination
          loading={loading}
          pagination={false}
          rowKey="code"
          rowSelection={
            canWrite
              ? {
                  selectedRowKeys,
                  onChange: (keys?: Array<string | number>) =>
                    setSelectedRowKeys(keys ?? []),
                }
              : undefined
          }
          scroll={{ x: "max(100%, 1280px)", y: DESKTOP_TABLE_SCROLL_Y }}
          size="middle"
        />
      </CardPro>

      <CreateInviteModal
        onCreated={() => {
          setActivePage(1);
          void refresh();
        }}
        onOpenChange={setCreateOpen}
        open={createOpen}
      />
      <EditInviteModal
        invite={editTarget}
        onClose={() => setEditTarget(null)}
        onSaved={(updated) => {
          updateLoadedItems((items) =>
            items.map((item) =>
              item.code === updated.code ? updated : item
            )
          );
          if (detailTarget?.code === updated.code) {
            setDetailTarget(updated);
          }
          void refresh();
        }}
      />
      <InviteDetailSheet
        invite={detailTarget}
        onClose={() => setDetailTarget(null)}
        onEdit={
          canWrite
            ? (target) => {
                setDetailTarget(null);
                setEditTarget(target);
              }
            : undefined
        }
        onToggleEnabled={
          canWrite ? (target) => void toggleInviteEnabled(target) : undefined
        }
        toggleLoading={rowBusyCode === detailTarget?.code}
      />
    </>
  );
}
