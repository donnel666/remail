import { useCallback, useEffect, useMemo, useState } from "react";
import {
  Avatar,
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
import { useAuth } from "@/context/auth-provider";
import { useSelectionNotification } from "./resources/use-selection-notification";
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
import {
  CreateUserModal,
  EditUserModal,
} from "./admin-users/create-user-modal";
import {
  canMutateAdminUser,
  getAdminUserCapabilities,
} from "./admin-users/admin-user-access";
import {
  UserDetailSheet,
  WalletAdjustModal,
  type UserDetailTab,
} from "./admin-users/user-detail-sheet";
import {
  USER_GROUPS,
  deleteMockAdminUser,
  deleteMockAdminUsers,
  listMockAdminUsers,
  revokeMockAdminUserSessions,
  revokeMockAdminUsersSessions,
  setMockAdminUsersEnabled,
  updateMockAdminUser,
  type AdminUser,
  type AdminUserFacets,
  type AdminUserListFilter,
  type AdminUserRole,
  type AdminUserRoleFilter,
  type AdminUserStatusFilter,
} from "./admin-users/admin-users-mock";
import {
  avatarColor,
  formatDate,
  formatDateTime,
  formatMoney,
  formatRelativeTime,
  renderGroupTag,
  renderRoleTag,
  renderStatusTag,
  roleLabel,
  userInitial,
} from "./admin-users/user-meta";

const ROLE_TABS: AdminUserRole[] = ["user", "supplier", "admin", "super_admin"];
const SEARCH_DEBOUNCE_MS = 400;

interface DetailState {
  user: AdminUser;
  tab: UserDetailTab;
}

export default function AdminUsers() {
  const { t } = useTranslation();
  const { currentUser } = useAuth();
  const isMobile = useIsMobile();
  const capabilities = useMemo(
    () => getAdminUserCapabilities(currentUser?.permissions ?? []),
    [currentUser?.permissions]
  );
  const canSelectUsers =
    capabilities.canAdjustBalance || capabilities.canOperateUsers;

  const [activeRole, setActiveRole] = useState<AdminUserRoleFilter>("all");
  const [searchKeyword, setSearchKeyword] = useState("");
  const [createdAtRange, setCreatedAtRange] = useState<DateRangeValue>([]);
  const [statusFilter, setStatusFilter] = useState<AdminUserStatusFilter>("all");
  const [groupFilter, setGroupFilter] = useState<"all" | number>("all");
  const [compactMode, setCompactMode] = useState(false);
  const [activePage, setActivePage] = useState(1);
  const [pageSize, setPageSize] = useSharedPageSize();
  const [facets, setFacets] = useState<AdminUserFacets | null>(null);
  const [createOpen, setCreateOpen] = useState(false);
  const [editTarget, setEditTarget] = useState<AdminUser | null>(null);
  const [balanceTarget, setBalanceTarget] = useState<AdminUser | null>(null);
  const [bulkBalanceOpen, setBulkBalanceOpen] = useState(false);
  const [bulkBalanceTargetIDs, setBulkBalanceTargetIDs] = useState<number[]>([]);
  const [detailState, setDetailState] = useState<DetailState | null>(null);
  const [operatingUserID, setOperatingUserID] = useState<number | null>(null);
  const [selectedRowKeys, setSelectedRowKeys] = useState<Array<string | number>>([]);
  const [bulkAction, setBulkAction] = useState<
    "enable" | "disable" | "logout" | "delete" | "balance" | null
  >(null);
  const dateRangePresets = useMemo(() => createDateRangePresets(t), [t]);
  const [debouncedSearchKeyword, flushSearchKeyword] = useDebouncedValue(
    searchKeyword,
    SEARCH_DEBOUNCE_MS
  );

  const statsFilter = useMemo<AdminUserListFilter>(() => {
    const filter: AdminUserListFilter = {};
    const search = debouncedSearchKeyword.trim();
    const createdFrom = createdFromISOString(createdAtRange);
    const createdTo = createdToISOString(createdAtRange);
    if (search) filter.search = search;
    if (statusFilter !== "all") filter.enabled = statusFilter === "enabled";
    if (groupFilter !== "all") filter.userGroupId = groupFilter;
    if (createdFrom) filter.createdFrom = createdFrom;
    if (createdTo) filter.createdTo = createdTo;
    return filter;
  }, [createdAtRange, debouncedSearchKeyword, groupFilter, statusFilter]);

  const listFilter = useMemo<AdminUserListFilter>(() => {
    if (activeRole === "all") return statsFilter;
    return { ...statsFilter, role: activeRole };
  }, [activeRole, statsFilter]);
  const listFilterKey = JSON.stringify(listFilter);

  const loadUserBlock = useCallback(
    async (offset: number, limit: number) => {
      const response = await listMockAdminUsers(listFilter, offset, limit);
      return {
        items: response.users,
        meta: response.facets,
        total: response.total,
      };
    },
    [listFilter]
  );

  const acceptUserBlock = useCallback(
    (response: { meta?: AdminUserFacets }) => {
      if (response.meta) setFacets(response.meta);
    },
    []
  );

  const {
    loading,
    pagedItems,
    refresh,
    total,
    updateLoadedItems,
  } = useBlockPagedList<AdminUser, AdminUserFacets>({
    activePage,
    filterKey: listFilterKey,
    loadBlock: loadUserBlock,
    onError: (error) => {
      Toast.error(getIamErrorMessage(t, error, "Users load failed."));
    },
    onLoaded: acceptUserBlock,
    pageSize,
  });

  const roleCounts = useMemo<Record<AdminUserRoleFilter, number>>(() => {
    if (facets) return facets.role;
    return { all: total, user: 0, supplier: 0, admin: 0, super_admin: 0 };
  }, [facets, total]);

  const statusStats = useMemo(() => {
    if (facets) return facets.status;
    return { all: total, enabled: 0, disabled: 0 };
  }, [facets, total]);

  const groupStats = useMemo(() => {
    if (facets) return facets.group;
    return USER_GROUPS.map((group) => ({
      id: group.id,
      code: group.code,
      name: group.name,
      count: 0,
    }));
  }, [facets]);
  const groupTotal = useMemo(
    () =>
      facets
        ? groupStats.reduce((sum, group) => sum + group.count, 0)
        : total,
    [facets, groupStats, total]
  );

  const activeFilterCount =
    Number(statusFilter !== "all") + Number(groupFilter !== "all");

  const totalPages = Math.max(1, Math.ceil(total / pageSize));
  const safePage = Math.min(activePage, totalPages);
  const selectedUserIDs = useMemo(
    () => selectedRowKeys.map((key) => Number(key)).filter(Number.isFinite),
    [selectedRowKeys]
  );

  useEffect(() => {
    if (!loading && safePage !== activePage) setActivePage(safePage);
  }, [activePage, loading, safePage]);

  useEffect(() => {
    setFacets(null);
  }, [listFilterKey]);

  useEffect(() => {
    setSelectedRowKeys([]);
  }, [listFilter]);

  useEffect(() => {
    if (!canSelectUsers) setSelectedRowKeys([]);
  }, [canSelectUsers]);

  const selectRole = (role: AdminUserRoleFilter) => {
    setActiveRole(role);
    setActivePage(1);
    setSelectedRowKeys([]);
  };

  const resetFilters = () => {
    setSearchKeyword("");
    flushSearchKeyword("");
    setCreatedAtRange([]);
    setStatusFilter("all");
    setGroupFilter("all");
    setActiveRole("all");
    setActivePage(1);
    setSelectedRowKeys([]);
  };

  const applyStatusFilter = (value: AdminUserStatusFilter) => {
    setStatusFilter(value);
    setActivePage(1);
  };

  const applyGroupFilter = (value: string) => {
    setGroupFilter(value === "all" ? "all" : Number(value));
    setActivePage(1);
  };

  const openDetail = useCallback(
    (user: AdminUser, tab: UserDetailTab = "profile") => {
      setDetailState({ user, tab });
    },
    []
  );

  const handleUserChanged = useCallback(
    async (updated: AdminUser) => {
      setDetailState((previous) =>
        previous && previous.user.id === updated.id
          ? { ...previous, user: updated }
          : previous
      );
      await refresh();
    },
    [refresh]
  );

  const toggleEnabled = useCallback(
    async (record: AdminUser) => {
      if (!canMutateAdminUser(record.role, capabilities.canOperateUsers)) {
        return;
      }
      setOperatingUserID(record.id);
      try {
        const updated = await updateMockAdminUser(record.id, {
          enabled: !record.enabled,
        });
        await handleUserChanged(updated);
        Toast.success(record.enabled ? t("User disabled.") : t("User enabled."));
      } catch (error) {
        Toast.error(getIamErrorMessage(t, error, "User update failed."));
      } finally {
        setOperatingUserID(null);
      }
    },
    [capabilities.canOperateUsers, handleUserChanged, t]
  );

  const confirmForceLogout = useCallback(
    (record: AdminUser) => {
      if (!canMutateAdminUser(record.role, capabilities.canOperateUsers)) {
        return;
      }
      Modal.confirm({
        cancelText: t("Cancel"),
        content: t("Confirm revoke sessions content"),
        okButtonProps: { type: "danger" },
        okText: t("Exit"),
        onOk: async () => {
          try {
            await revokeMockAdminUserSessions(record.id);
            Toast.success(t("All sessions revoked."));
          } catch (error) {
            Toast.error(getIamErrorMessage(t, error, "Operation failed."));
            throw error;
          }
        },
        title: t("Exit"),
      });
    },
    [capabilities.canOperateUsers, t]
  );

  const confirmDelete = useCallback(
    (record: AdminUser) => {
      if (!canMutateAdminUser(record.role, capabilities.canOperateUsers)) {
        return;
      }
      Modal.confirm({
        cancelText: t("Cancel"),
        content: t("Confirm delete user content", { email: record.email }),
        okButtonProps: { type: "danger" },
        okText: t("Delete"),
        onOk: async () => {
          try {
            await deleteMockAdminUser(record.id);
            Toast.success(t("User deleted."));
            setDetailState((previous) =>
              previous && previous.user.id === record.id ? null : previous
            );
            await refresh();
          } catch (error) {
            Toast.error(getIamErrorMessage(t, error, "User delete failed."));
            throw error;
          }
        },
        title: t("Delete user"),
      });
    },
    [capabilities.canOperateUsers, refresh, t]
  );

  const confirmBulkAction = useCallback(
    (action: "enable" | "disable" | "logout" | "delete") => {
      if (!capabilities.canOperateUsers) return;
      if (selectedUserIDs.length === 0) {
        Toast.warning(t("No users selected."));
        return;
      }
      const label =
        action === "enable"
          ? t("Enable")
          : action === "disable"
            ? t("Disable")
            : action === "logout"
              ? t("Exit")
              : t("Delete");
      Modal.confirm({
        cancelText: t("Cancel"),
        content: t(
          action === "enable"
            ? "Confirm enable selected users content"
            : action === "disable"
              ? "Confirm disable selected users content"
              : action === "logout"
                ? "Confirm force logout selected users content"
                : "Confirm delete selected users content",
          { count: selectedUserIDs.length }
        ),
        okButtonProps: {
          type: action === "delete" || action === "logout" ? "danger" : "primary",
        },
        okText: label,
        onOk: async () => {
          setBulkAction(action);
          try {
            const result =
              action === "enable" || action === "disable"
                ? await setMockAdminUsersEnabled(
                    selectedUserIDs,
                    action === "enable"
                  )
                : action === "logout"
                  ? await revokeMockAdminUsersSessions(selectedUserIDs)
                  : await deleteMockAdminUsers(selectedUserIDs);

            if (action === "delete") {
              updateLoadedItems((items) =>
                items.filter((item) => !selectedUserIDs.includes(item.id))
              );
              setDetailState((previous) =>
                previous && selectedUserIDs.includes(previous.user.id)
                  ? null
                  : previous
              );
            } else if (action === "enable" || action === "disable") {
              const enabled = action === "enable";
              updateLoadedItems((items) =>
                items.map((item) =>
                  selectedUserIDs.includes(item.id) && item.role !== "super_admin"
                    ? { ...item, enabled }
                    : item
                )
              );
              setDetailState((previous) =>
                previous &&
                selectedUserIDs.includes(previous.user.id) &&
                previous.user.role !== "super_admin"
                  ? { ...previous, user: { ...previous.user, enabled } }
                  : previous
              );
            }

            setSelectedRowKeys([]);
            await refresh();
            Toast.success(
              t("Users bulk operation completed.", {
                count: result.affected,
                skipped: result.skipped,
              })
            );
          } catch (error) {
            Toast.error(getIamErrorMessage(t, error, "Operation failed."));
            throw error;
          } finally {
            setBulkAction(null);
          }
        },
        title: t("Batch user action", { action: label }),
      });
    },
    [
      capabilities.canOperateUsers,
      refresh,
      selectedUserIDs,
      t,
      updateLoadedItems,
    ]
  );

  const openBulkBalance = useCallback(() => {
    if (!capabilities.canAdjustBalance) return;
    if (selectedUserIDs.length === 0) {
      Toast.warning(t("No users selected."));
      return;
    }
    setBulkBalanceTargetIDs(selectedUserIDs);
    setBulkAction("balance");
    setBulkBalanceOpen(true);
  }, [capabilities.canAdjustBalance, selectedUserIDs, t]);

  const fetchAllFilteredUserIDs = useCallback(async () => {
    const response = await listMockAdminUsers(listFilter, 0, Math.max(total, 1));
    return response.users.map((user) => user.id);
  }, [listFilter, total]);

  const openBulkBalanceAll = useCallback(async () => {
    if (!capabilities.canAdjustBalance) return;
    if (total === 0) {
      Toast.warning(t("No users match the current filters."));
      return;
    }
    setBulkAction("balance");
    try {
      const ids = await fetchAllFilteredUserIDs();
      setBulkBalanceTargetIDs(ids);
      setBulkBalanceOpen(true);
    } catch (error) {
      Toast.error(getIamErrorMessage(t, error, "Users load failed."));
      setBulkAction(null);
    }
  }, [capabilities.canAdjustBalance, fetchAllFilteredUserIDs, t, total]);

  const confirmBulkActionAll = useCallback(
    (action: "disable" | "logout" | "delete") => {
      if (!capabilities.canOperateUsers) return;
      if (total === 0) {
        Toast.warning(t("No users match the current filters."));
        return;
      }
      const label =
        action === "disable"
          ? t("Disable")
          : action === "logout"
            ? t("Exit")
            : t("Delete");
      Modal.confirm({
        cancelText: t("Cancel"),
        content: t(
          action === "disable"
            ? "Confirm disable all matching users content"
            : action === "logout"
              ? "Confirm force logout all matching users content"
              : "Confirm delete all matching users content",
          { count: total }
        ),
        okButtonProps: { type: "danger" },
        okText: label,
        onOk: async () => {
          setBulkAction(action);
          try {
            const ids = await fetchAllFilteredUserIDs();
            const result =
              action === "disable"
                ? await setMockAdminUsersEnabled(ids, false)
                : action === "logout"
                  ? await revokeMockAdminUsersSessions(ids)
                  : await deleteMockAdminUsers(ids);

            if (action === "delete") {
              updateLoadedItems((items) =>
                items.filter((item) => !ids.includes(item.id))
              );
              setDetailState((previous) =>
                previous && ids.includes(previous.user.id) ? null : previous
              );
            } else {
              updateLoadedItems((items) =>
                items.map((item) =>
                  ids.includes(item.id) && item.role !== "super_admin"
                    ? { ...item, enabled: false }
                    : item
                )
              );
              setDetailState((previous) =>
                previous &&
                ids.includes(previous.user.id) &&
                previous.user.role !== "super_admin"
                  ? { ...previous, user: { ...previous.user, enabled: false } }
                  : previous
              );
            }

            setSelectedRowKeys([]);
            await refresh();
            Toast.success(
              t("Users bulk operation completed.", {
                count: result.affected,
                skipped: result.skipped,
              })
            );
          } catch (error) {
            Toast.error(getIamErrorMessage(t, error, "Operation failed."));
            throw error;
          } finally {
            setBulkAction(null);
          }
        },
        title: t("Batch user action", { action: label }),
      });
    },
    [
      capabilities.canOperateUsers,
      fetchAllFilteredUserIDs,
      refresh,
      t,
      total,
      updateLoadedItems,
    ]
  );

  const selectionExtraActions = useMemo(
    () => [
      ...(capabilities.canAdjustBalance
        ? [
            {
              key: "balance",
              labelKey: "Adjust balance",
              loading: bulkAction === "balance",
              onClick: openBulkBalance,
              type: "danger" as const,
            },
          ]
        : []),
      ...(capabilities.canOperateUsers
        ? [
            {
              key: "logout",
              labelKey: "Exit",
              loading: bulkAction === "logout",
              onClick: () => confirmBulkAction("logout"),
              type: "warning" as const,
            },
          ]
        : []),
    ],
    [
      bulkAction,
      capabilities.canAdjustBalance,
      capabilities.canOperateUsers,
      confirmBulkAction,
      openBulkBalance,
    ]
  );

  const clearSelectedUsers = useCallback(() => setSelectedRowKeys([]), []);
  const confirmEnableSelected = useCallback(
    () => confirmBulkAction("enable"),
    [confirmBulkAction]
  );
  const confirmDisableSelected = useCallback(
    () => confirmBulkAction("disable"),
    [confirmBulkAction]
  );
  const confirmDeleteSelected = useCallback(
    () => confirmBulkAction("delete"),
    [confirmBulkAction]
  );

  useSelectionNotification({
    checkLabelKey: "Enable",
    checkLoading: bulkAction === "enable",
    deleteLoading: bulkAction === "delete",
    extraActions: selectionExtraActions,
    onCheck: capabilities.canOperateUsers ? confirmEnableSelected : undefined,
    onClear: clearSelectedUsers,
    onDelete: capabilities.canOperateUsers ? confirmDeleteSelected : undefined,
    onSell: capabilities.canOperateUsers ? confirmDisableSelected : undefined,
    selectedCount: selectedUserIDs.length,
    selectionDescriptionKey: "Selected users",
    sellLabelKey: "Disable",
    sellLoading: bulkAction === "disable",
    t,
  });

  const columns = useMemo(
    () =>
      [
        {
          dataIndex: "id",
          key: "id",
          title: "ID",
          width: 72,
          render: (_: unknown, record: AdminUser) => (
            <span className="font-mono text-[var(--semi-color-text-1)]">
              #{record.id}
            </span>
          ),
        },
        {
          dataIndex: "email",
          key: "email",
          title: t("User"),
          width: 270,
          render: (_: unknown, record: AdminUser) => (
            <div className="flex min-w-0 items-center gap-2.5">
              <Avatar color={avatarColor(record.id)} size="extra-small">
                {userInitial(record)}
              </Avatar>
              <div className="min-w-0">
                <CopyableTableText copiedText={t("Copied")} text={record.email} />
                <div className="truncate text-xs text-[var(--semi-color-text-2)]">
                  {record.nickname || "-"}
                </div>
              </div>
            </div>
          ),
        },
        {
          dataIndex: "role",
          key: "role",
          title: t("Role"),
          width: 105,
          render: (role: AdminUserRole) => renderRoleTag(role, t),
        },
        {
          dataIndex: "userGroup",
          key: "userGroup",
          title: t("User Group"),
          width: 100,
          render: (_: unknown, record: AdminUser) =>
            renderGroupTag(record.userGroup.name),
        },
        {
          dataIndex: "consumerBalance",
          key: "consumerBalance",
          title: t("Current Balance"),
          width: 115,
          render: (value: string) => (
            <span className="font-mono-data text-[var(--semi-color-text-0)]">
              ¥{formatMoney(value)}
            </span>
          ),
        },
        {
          dataIndex: "enabled",
          key: "enabled",
          title: t("Status"),
          width: 90,
          render: (value: boolean) => renderStatusTag(value, t),
        },
        {
          dataIndex: "lastLoginAt",
          key: "lastLoginAt",
          title: t("Last login"),
          width: 120,
          render: (value: string | null) =>
            value ? (
              <Tooltip content={formatDateTime(value)}>
                <span className="text-[13px] text-[var(--semi-color-text-1)]">
                  {formatRelativeTime(value, t)}
                </span>
              </Tooltip>
            ) : (
              <span className="text-[13px] text-[var(--semi-color-text-2)]">
                {t("Never")}
              </span>
            ),
        },
        {
          dataIndex: "createdAt",
          key: "createdAt",
          title: t("Created At"),
          width: 115,
          render: (value: string) => (
            <Tooltip content={formatDateTime(value)}>
              <span className="text-[13px] text-[var(--semi-color-text-1)]">
                {formatDate(value)}
              </span>
            </Tooltip>
          ),
        },
        {
          dataIndex: "operate",
          fixed: "right",
          key: "operate",
          title: t("Actions"),
          width: 400,
          render: (_: unknown, record: AdminUser) => {
            const rowLoading = operatingUserID === record.id;
            const canEditRecord = canMutateAdminUser(
              record.role,
              capabilities.canWriteUsers
            );
            const canAdjustRecord = canMutateAdminUser(
              record.role,
              capabilities.canAdjustBalance
            );
            const canOperateRecord = canMutateAdminUser(
              record.role,
              capabilities.canOperateUsers
            );
            return (
              <Space spacing={4} wrap={false}>
                <Button
                  disabled={rowLoading}
                  onClick={() => openDetail(record)}
                  size="small"
                  type="tertiary"
                >
                  {t("Details")}
                </Button>
                <Button
                  disabled={!canEditRecord || rowLoading}
                  onClick={() => {
                    if (canEditRecord) setEditTarget(record);
                  }}
                  size="small"
                  type="tertiary"
                >
                  {t("Edit")}
                </Button>
                <Button
                  disabled={!canAdjustRecord || rowLoading}
                  onClick={() => {
                    if (canAdjustRecord) setBalanceTarget(record);
                  }}
                  size="small"
                  type="tertiary"
                >
                  {t("Adjust balance")}
                </Button>
                <Button
                  disabled={!canOperateRecord}
                  loading={rowLoading}
                  onClick={() => void toggleEnabled(record)}
                  size="small"
                  type="tertiary"
                >
                  {record.enabled ? t("Disable") : t("Enable")}
                </Button>
                <Button
                  disabled={!canOperateRecord || rowLoading}
                  onClick={() => confirmForceLogout(record)}
                  size="small"
                  type="tertiary"
                >
                  {t("Exit")}
                </Button>
                <Button
                  disabled={!canOperateRecord || rowLoading}
                  onClick={() => confirmDelete(record)}
                  size="small"
                  type="danger"
                >
                  {t("Delete")}
                </Button>
              </Space>
            );
          },
        }
      ] as any[],
    [
      capabilities.canAdjustBalance,
      capabilities.canOperateUsers,
      capabilities.canWriteUsers,
      confirmDelete,
      confirmForceLogout,
      openDetail,
      operatingUserID,
      t,
      toggleEnabled,
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
    selectedRowKeys,
    getCheckboxProps: (record?: AdminUser) => ({
      disabled: record?.role === "super_admin",
    }),
    onChange: (keys?: Array<string | number>) => {
      setSelectedRowKeys(keys ?? []);
    },
  };

  const tabsArea = (
    <Tabs
      activeKey={activeRole}
      className="mb-2"
      collapsible
      onChange={(key) => selectRole(key as AdminUserRoleFilter)}
      type="card"
    >
      <Tabs.TabPane
        itemKey="all"
        tab={
          <span className="flex items-center gap-2">
            {t("All")}
            <Tag color={activeRole === "all" ? "red" : "grey"} shape="circle">
              {roleCounts.all}
            </Tag>
          </span>
        }
      />
      {ROLE_TABS.map((role) => (
        <Tabs.TabPane
          itemKey={role}
          key={role}
          tab={
            <span className="flex items-center gap-2">
              {roleLabel(role, t)}
              <Tag color={activeRole === role ? "red" : "grey"} shape="circle">
                {roleCounts[role]}
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
        {capabilities.canWriteUsers ? (
          <Button
            className="flex-1 md:flex-initial"
            onClick={() => setCreateOpen(true)}
            size="small"
            type="primary"
          >
            {t("Create")}
          </Button>
        ) : null}
        <Button
          className="remail-toolbar-fixed-button flex-1 md:flex-none"
          loading={loading}
          onClick={() => void refresh()}
          size="small"
          type="tertiary"
        >
          {t("Refresh")}
        </Button>
        {capabilities.canAdjustBalance ? (
          <Button
            className="flex-1 md:flex-none"
            loading={bulkAction === "balance"}
            onClick={() => void openBulkBalanceAll()}
            size="small"
            type="tertiary"
          >
            {t("Adjust balance")}
          </Button>
        ) : null}
        {capabilities.canOperateUsers ? (
          <>
            <Button
              className="flex-1 md:flex-none"
              loading={bulkAction === "disable"}
              onClick={() => confirmBulkActionAll("disable")}
              size="small"
              type="tertiary"
            >
              {t("Disable")}
            </Button>
            <Button
              className="flex-1 md:flex-none"
              loading={bulkAction === "logout"}
              onClick={() => confirmBulkActionAll("logout")}
              size="small"
              type="tertiary"
            >
              {t("Exit")}
            </Button>
            <Button
              className="flex-1 md:flex-none"
              loading={bulkAction === "delete"}
              onClick={() => confirmBulkActionAll("delete")}
              size="small"
              type="danger"
            >
              {t("Delete")}
            </Button>
          </>
        ) : null}
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
            <div className="w-[280px] p-2">
              <div className="px-2 pb-1 text-xs font-medium text-[var(--semi-color-text-2)]">
                {t("Status")}
              </div>
              <div className="mb-2 space-y-1">
                <StatisticFilterOption
                  active={statusFilter === "all"}
                  count={statusStats.all}
                  label={t("All")}
                  onSelect={applyStatusFilter}
                  value="all"
                />
                <StatisticFilterOption
                  active={statusFilter === "enabled"}
                  count={statusStats.enabled}
                  label={t("Enabled")}
                  onSelect={applyStatusFilter}
                  value="enabled"
                />
                <StatisticFilterOption
                  active={statusFilter === "disabled"}
                  count={statusStats.disabled}
                  label={t("Disabled")}
                  onSelect={applyStatusFilter}
                  value="disabled"
                />
              </div>

              <div className="px-2 pb-1 text-xs font-medium text-[var(--semi-color-text-2)]">
                {t("User Group")}
              </div>
              <div className="space-y-1">
                <StatisticFilterOption
                  active={groupFilter === "all"}
                  count={groupTotal}
                  label={t("All")}
                  onSelect={applyGroupFilter}
                  value="all"
                />
                {groupStats.map((group) => (
                  <StatisticFilterOption
                    active={groupFilter === group.id}
                    count={group.count}
                    key={group.id}
                    label={group.name}
                    onSelect={applyGroupFilter}
                    value={String(group.id)}
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
        <Input
          className="resources-search-input w-full md:w-56"
          onChange={(value) => {
            setSearchKeyword(String(value));
            setActivePage(1);
          }}
          onEnterPress={() => {
            flushSearchKeyword();
            setActivePage(1);
          }}
          placeholder={t("Search user by email, nickname or ID")}
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
            setActivePage(1);
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
              description={t("No users found")}
              image={<IllustrationNoResult style={{ height: 150, width: 150 }} />}
              style={{ padding: 30 }}
            />
          }
          hidePagination
          loading={loading}
          pagination={false}
          rowKey="id"
          rowSelection={canSelectUsers ? rowSelection : undefined}
          scroll={{ x: "max(100%, 1510px)", y: DESKTOP_TABLE_SCROLL_Y }}
          size="middle"
        />
      </CardPro>

      <CreateUserModal
        canAssignSuperAdmin={capabilities.canAssignSuperAdmin}
        onCreated={async () => {
          setActivePage(1);
          await refresh();
        }}
        onOpenChange={(open) => {
          setCreateOpen(capabilities.canWriteUsers && open);
        }}
        open={capabilities.canWriteUsers && createOpen}
      />
      <EditUserModal
        canAssignSuperAdmin={capabilities.canAssignSuperAdmin}
        onClose={() => setEditTarget(null)}
        onSaved={handleUserChanged}
        user={
          editTarget &&
          canMutateAdminUser(editTarget.role, capabilities.canWriteUsers)
            ? editTarget
            : null
        }
      />
      <WalletAdjustModal
        balance={balanceTarget?.consumerBalance ?? "0"}
        onClose={() => setBalanceTarget(null)}
        onDone={async (wallet) => {
          if (!balanceTarget) return;
          await handleUserChanged({
            ...balanceTarget,
            consumerBalance: wallet.consumerBalance,
          });
        }}
        open={Boolean(
          balanceTarget &&
            canMutateAdminUser(
              balanceTarget.role,
              capabilities.canAdjustBalance
            )
        )}
        userId={balanceTarget?.id ?? 0}
      />
      <WalletAdjustModal
        balance="-"
        onBulkDone={async (signedAmount) => {
          updateLoadedItems((items) =>
            items.map((item) => {
              if (
                !bulkBalanceTargetIDs.includes(item.id) ||
                item.role === "super_admin"
              ) {
                return item;
              }
              const nextBalance = Number(item.consumerBalance) + signedAmount;
              return nextBalance < 0
                ? item
                : { ...item, consumerBalance: nextBalance.toFixed(2) };
            })
          );
          setDetailState((previous) => {
            if (
              !previous ||
              !bulkBalanceTargetIDs.includes(previous.user.id) ||
              previous.user.role === "super_admin"
            ) {
              return previous;
            }
            const nextBalance =
              Number(previous.user.consumerBalance) + signedAmount;
            return nextBalance < 0
              ? previous
              : {
                  ...previous,
                  user: {
                    ...previous.user,
                    consumerBalance: nextBalance.toFixed(2),
                  },
                };
          });
          setSelectedRowKeys([]);
          await refresh();
        }}
        onClose={() => {
          setBulkBalanceOpen(false);
          setBulkAction(null);
        }}
        open={capabilities.canAdjustBalance && bulkBalanceOpen}
        userIds={bulkBalanceTargetIDs}
      />
      <UserDetailSheet
        capabilities={capabilities}
        initialTab={detailState?.tab}
        onChanged={handleUserChanged}
        onClose={() => setDetailState(null)}
        onDeleted={async () => {
          setDetailState(null);
          await refresh();
        }}
        user={detailState?.user ?? null}
      />
    </div>
  );
}
