import { useCallback, useEffect, useMemo, useState, type ReactNode } from "react";
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
  listMockFinanceCardKeys,
  setMockFinanceCardKeyStatus,
  setMockFinanceCardKeysStatus,
  setMockFinanceCardKeysStatusByFilter,
  type FinanceCardKey,
  type FinanceCardKeyFacets,
  type FinanceCardKeyStatus,
  type FinanceCardKeyStatusFilter,
  type FinanceOwnerRoleFilter,
  type FinanceUserRole,
} from "./admin-finance-mock";
import { CardKeyDetailSheet } from "./card-key-detail-sheet";
import { CardKeyAccountCell } from "./card-key-meta";
import {
  CreateCardKeyModal,
  EditCardKeyModal,
} from "./card-key-modals";
import {
  formatDateTime,
  formatMoney,
  renderCardKeyStatusTag,
} from "./finance-meta";
import { copyText, emptyNode } from "./finance-shared";

// Same role labels admin-microsoft uses in its owner cell.
const CARD_KEY_ROLE_LABELS: Record<FinanceUserRole, string> = {
  user: "User",
  supplier: "Supplier",
  admin: "Admin",
  super_admin: "Super Admin",
};

export function CardKeysPanel({ tabsArea }: { tabsArea: ReactNode }) {
  const { t } = useTranslation();
  const { currentUser } = useAuth();
  const canWrite = hasPermission(currentUser, "billing:card", "write");
  const isMobile = useIsMobile();
  const [pageSize, setPageSize] = useSharedPageSize();
  const [activePage, setActivePage] = useState(1);

  useEffect(() => setActivePage(1), [pageSize]);
  const [searchKeyword, setSearchKeyword] = useState("");
  const [debouncedSearch, flushSearch] = useDebouncedValue(searchKeyword);
  const [roleFilter, setRoleFilter] =
    useState<FinanceOwnerRoleFilter>("all");
  const [groupFilter, setGroupFilter] = useState<string>("all");
  const [statusFilter, setStatusFilter] =
    useState<FinanceCardKeyStatusFilter>("all");
  const [compactMode, setCompactMode] = useState(false);
  const [selectedRowKeys, setSelectedRowKeys] = useState<Array<string | number>>(
    []
  );
  const [createOpen, setCreateOpen] = useState(false);
  const [editTarget, setEditTarget] = useState<FinanceCardKey | null>(null);
  const [detailTarget, setDetailTarget] = useState<FinanceCardKey | null>(null);
  const [rowBusyKey, setRowBusyKey] = useState<string | null>(null);
  const [bulkBusy, setBulkBusy] = useState<
    "enable-all" | "disable-all" | "enable-sel" | "disable-sel" | null
  >(null);
  const [facets, setFacets] = useState<FinanceCardKeyFacets>({
    role: { all: 0, user: 0, supplier: 0, admin: 0, super_admin: 0 },
    group: { all: 0 },
    status: { all: 0, enabled: 0, disabled: 0 },
  });

  const listFilter = useMemo(
    () => ({
      search: debouncedSearch.trim() || undefined,
      ownerRole: roleFilter === "all" ? undefined : roleFilter,
      ownerGroupName: groupFilter === "all" ? undefined : groupFilter,
      status: statusFilter === "all" ? undefined : statusFilter,
    }),
    [debouncedSearch, groupFilter, roleFilter, statusFilter]
  );

  const loadBlock = useCallback(
    async (offset: number, limit: number) => {
      const result = await listMockFinanceCardKeys(listFilter, offset, limit);
      return {
        items: result.items,
        meta: result.facets,
        total: result.total,
      };
    },
    [listFilter]
  );

  const { pagedItems, total, loading, refresh, updateLoadedItems } =
    useBlockPagedList<FinanceCardKey, FinanceCardKeyFacets>({
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
  const runStatusSelected = async (status: FinanceCardKeyStatus) => {
    if (!canWrite || !selectedRowKeys.length) return;
    setBulkBusy(status === "enabled" ? "enable-sel" : "disable-sel");
    try {
      const result = await setMockFinanceCardKeysStatus(
        selectedRowKeys.map(String),
        status
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
  const confirmStatusAll = (status: FinanceCardKeyStatus) => {
    if (!canWrite || total === 0) {
      Toast.info(t("No card keys to update."));
      return;
    }
    Modal.confirm({
      cancelText: t("Cancel"),
      content: t(
        status === "enabled"
          ? "Confirm enable all matching card keys"
          : "Confirm disable all matching card keys",
        { count: total }
      ),
      okText: status === "enabled" ? t("Enable") : t("Disable"),
      onOk: async () => {
        setBulkBusy(status === "enabled" ? "enable-all" : "disable-all");
        try {
          const result = await setMockFinanceCardKeysStatusByFilter(
            listFilter,
            status
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
      title:
        status === "enabled" ? t("Confirm enable all") : t("Confirm disable all"),
    });
  };

  const toggleCardKeyStatus = async (record: FinanceCardKey) => {
    if (!canWrite) return;
    setRowBusyKey(record.key);
    try {
      const nextStatus: FinanceCardKeyStatus =
        record.status === "enabled" ? "disabled" : "enabled";
      const updated = await setMockFinanceCardKeyStatus(record.key, nextStatus);
      updateLoadedItems((items) =>
        items.map((item) => (item.key === updated.key ? updated : item))
      );
      setDetailTarget((current) =>
        current?.key === updated.key ? updated : current
      );
      Toast.success(
        updated.status === "enabled"
          ? t("Card key enabled.")
          : t("Card key disabled.")
      );
    } catch (error) {
      Toast.error(getIamErrorMessage(t, error, "Operation failed."));
    } finally {
      setRowBusyKey(null);
    }
  };

  useSelectionNotification({
    selectedCount: selectedRowKeys.length,
    onClear: () => setSelectedRowKeys([]),
    onCheck: canWrite ? () => void runStatusSelected("enabled") : undefined,
    checkLabelKey: "Enable",
    checkLoading: bulkBusy === "enable-sel",
    onSell: canWrite ? () => void runStatusSelected("disabled") : undefined,
    sellLabelKey: "Disable",
    sellLoading: bulkBusy === "disable-sel",
    selectionDescriptionKey: "Selected resources",
    t,
  });

  const columns = useMemo(
    () => [
      {
        title: t("Card key"),
        dataIndex: "key",
        width: 220,
        render: (value: string) => <CopyableTableText copiedText={t("Copied")} text={value} />,
      },
      {
        title: t("Amount"),
        dataIndex: "amount",
        width: 120,
        render: (value: string) => (
          <span className="font-mono-data">¥{formatMoney(value)}</span>
        ),
      },
      {
        title: t("Status"),
        dataIndex: "status",
        width: 110,
        render: (value: FinanceCardKey["status"]) =>
          renderCardKeyStatusTag(value, t),
      },
      {
        title: t("Redemptions"),
        dataIndex: "redeemedCount",
        width: 130,
        render: (_: number, record: FinanceCardKey) => (
          <span className="font-mono-data">
            {record.redeemedCount}/{record.maxRedemptions}
          </span>
        ),
      },
      {
        title: t("Owner"),
        dataIndex: "ownerEmail",
        width: 260,
        render: (_: string | null | undefined, record: FinanceCardKey) => (
          <CardKeyAccountCell
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
        render: (_: unknown, record: FinanceCardKey) => (
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
              loading={rowBusyKey === record.key}
              onClick={() => void toggleCardKeyStatus(record)}
              size="small"
              type="tertiary"
            >
              {record.status === "enabled" ? t("Disable") : t("Enable")}
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
    [canWrite, compactMode, rowBusyKey, t]
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
            onClick={() => confirmStatusAll("enabled")}
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
            onClick={() => confirmStatusAll("disabled")}
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
                    label={t(CARD_KEY_ROLE_LABELS[role])}
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
                    active={statusFilter === value}
                    count={facets.status[value]}
                    key={value}
                    label={label}
                    onSelect={() => {
                      setStatusFilter(value);
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
          placeholder={t("Search card key or amount")}
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
          empty={emptyNode(t("No card keys found"))}
          hidePagination
          loading={loading}
          pagination={false}
          rowKey="key"
          rowSelection={
            canWrite
              ? {
                  selectedRowKeys,
                  onChange: (keys?: Array<string | number>) =>
                    setSelectedRowKeys(keys ?? []),
                }
              : undefined
          }
          scroll={{ x: "max(100%, 1400px)", y: DESKTOP_TABLE_SCROLL_Y }}
          size="middle"
        />
      </CardPro>

      <CreateCardKeyModal
        onCreated={async (count, keys) => {
          setActivePage(1);
          void refresh();
          if (keys.length) {
            try {
              await copyText(keys.join("\n"));
              Toast.success(
                t("Card keys created and copied.", { count })
              );
            } catch {
              // copy is best-effort
            }
          }
        }}
        onOpenChange={setCreateOpen}
        open={createOpen}
      />
      <EditCardKeyModal
        card={editTarget}
        onClose={() => setEditTarget(null)}
        onSaved={(updated) => {
          updateLoadedItems((items) =>
            items.map((item) => (item.key === updated.key ? updated : item))
          );
          if (detailTarget?.key === updated.key) {
            setDetailTarget(updated);
          }
          void refresh();
        }}
      />
      <CardKeyDetailSheet
        card={detailTarget}
        onClose={() => setDetailTarget(null)}
        onEdit={
          canWrite
            ? (target) => {
                setDetailTarget(null);
                setEditTarget(target);
              }
            : undefined
        }
        onToggleStatus={
          canWrite ? (target) => void toggleCardKeyStatus(target) : undefined
        }
        toggleLoading={rowBusyKey === detailTarget?.key}
      />
    </>
  );
}
