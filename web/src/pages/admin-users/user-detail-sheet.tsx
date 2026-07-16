import { useCallback, useEffect, useMemo, useState } from "react";
import type { ReactNode } from "react";
import {
  Avatar,
  Button,
  DatePicker,
  Empty,
  Input,
  InputNumber,
  Modal,
  Radio,
  RadioGroup,
  Select,
  SideSheet,
  Space,
  Spin,
  Switch,
  Table,
  Tabs,
  Tag,
  Toast,
  Typography,
} from "@douyinfe/semi-ui";
import {
  IconEdit,
  IconKey,
  IconPlus,
  IconRefresh,
} from "@douyinfe/semi-icons";
import type { TFunction } from "i18next";
import { useTranslation } from "react-i18next";

import { createCardProPagination } from "@/components/semi/card-pro-pagination";
import { createCopyableConfig } from "@/components/semi/copyable-config";
import { CopyableTableText } from "@/components/semi/copyable-table-text";
import { OverflowTooltip } from "@/components/semi/overflow-tooltip";
import { useIsMobile } from "@/hooks/use-is-mobile";
import { useSharedPageSize } from "@/hooks/use-shared-page-size";
import { getIamErrorMessage } from "@/lib/iam-errors";

import {
  canMutateAdminUser,
  type AdminUserCapabilities,
} from "./admin-user-access";
import {
  PERMISSION_CATALOG,
  USER_GROUPS,
  adjustMockAdminUsersWallet,
  creditMockAdminUserWallet,
  createMockAdminUserApiKey,
  debitMockAdminUserWallet,
  deleteMockAdminUser,
  deleteMockAdminUserApiKey,
  getMockAdminUserInvitations,
  getMockAdminUserPermissions,
  getMockAdminUserWallet,
  listMockAdminUserApiKeys,
  listMockAdminUserTransactions,
  putMockAdminUserPermissions,
  revokeMockAdminUserSessions,
  roleBaselinePermissions,
  updateMockAdminUser,
  updateMockAdminUserApiKey,
  type AdminApiKey,
  type AdminTransaction,
  type AdminUser,
  type AdminUserInvitationMember,
  type AdminUserInvitationOverview,
  type AdminUserRole,
  type AdminWallet,
  type PermissionEffect,
  type PermissionPolicy,
} from "./admin-users-mock";
import {
  avatarColor,
  formatDateTime,
  formatMoney,
  formatRelativeTime,
  permissionActionLabel,
  permissionResourceLabel,
  renderRoleTag,
  renderStatusTag,
  renderTransactionTypeTag,
  roleLabel,
  userInitial,
} from "./user-meta";

const { Text } = Typography;
const ROLES: AdminUserRole[] = ["user", "supplier", "admin", "super_admin"];
const DRAWER_PANEL_HEIGHT = "max(360px, calc(100vh - 237px))";
const DRAWER_TABLE_SCROLL_Y = "max(220px, calc(100vh - 337px))";

export type UserDetailTab =
  | "profile"
  | "invitations"
  | "wallet"
  | "apikeys"
  | "permissions";

type PermissionState = "inherit" | "allow" | "deny";

function InfoItem({ label, value }: { label: string; value: ReactNode }) {
  return (
    <div className="min-w-0 rounded-lg bg-[var(--semi-color-fill-0)] px-3 py-2">
      <div className="mb-1 text-xs text-[var(--semi-color-text-2)]">{label}</div>
      <div className="break-words text-sm text-[var(--semi-color-text-0)]">
        {value}
      </div>
    </div>
  );
}

function InvitationIdentity({
  member,
  copiedText,
}: {
  member: AdminUserInvitationMember;
  copiedText: string;
}) {
  return (
    <div className="flex min-w-0 items-center gap-2">
      <Avatar color={avatarColor(member.id)} size="extra-small">
        {userInitial(member)}
      </Avatar>
      <div className="min-w-0">
        <CopyableTableText copiedText={copiedText} text={member.email} />
        <div className="truncate text-xs text-[var(--semi-color-text-2)]">
          {member.nickname || "-"} · #{member.id}
        </div>
      </div>
    </div>
  );
}

function UserPagedTable({
  columns,
  items,
  emptyDescription,
  extraOffset = 0,
  page,
  pageSize,
  rowKey = "id",
  scrollX,
  setPage,
  setPageSize,
  t,
}: {
  columns: any[];
  items: any[];
  emptyDescription: string;
  extraOffset?: number;
  page: number;
  pageSize: number;
  rowKey?: string;
  scrollX?: number;
  setPage: (page: number) => void;
  setPageSize: (pageSize: number) => void;
  t: TFunction;
}) {
  const isMobile = useIsMobile();
  const totalPages = Math.max(1, Math.ceil(items.length / pageSize));
  const safePage = Math.min(page, totalPages);
  const pageItems = items.slice(
    (safePage - 1) * pageSize,
    safePage * pageSize
  );
  const panelHeight = extraOffset
    ? `calc(${DRAWER_PANEL_HEIGHT} - ${extraOffset}px)`
    : DRAWER_PANEL_HEIGHT;
  const tableScrollY = extraOffset
    ? `calc(${DRAWER_TABLE_SCROLL_Y} - ${extraOffset}px)`
    : DRAWER_TABLE_SCROLL_Y;

  useEffect(() => {
    if (page !== safePage) setPage(safePage);
  }, [page, safePage, setPage]);

  return (
    <div className="flex flex-col" style={{ height: panelHeight }}>
      <div className="min-h-0 flex-1 overflow-hidden">
        {items.length === 0 ? (
          <Empty description={emptyDescription} style={{ padding: 24 }} />
        ) : (
          <Table
            columns={columns}
            dataSource={pageItems}
            pagination={false}
            rowKey={rowKey}
            scroll={{ x: scrollX, y: tableScrollY }}
            size="small"
          />
        )}
      </div>
      {items.length > 0 ? (
        <div className="mt-3 flex flex-wrap items-center justify-end gap-3 border-t border-[var(--semi-color-border)] pt-3">
          {createCardProPagination({
            currentPage: safePage,
            isMobile,
            onPageChange: setPage,
            onPageSizeChange: (size) => {
              setPageSize(size);
              setPage(1);
            },
            pageSize,
            pageSizeOpts: [10, 20, 50, 100],
            showSizeChanger: true,
            t,
            total: items.length,
          })}
        </div>
      ) : null}
    </div>
  );
}

// ---------- Profile tab ----------

function ProfileTab({
  user,
  canAssignSuperAdmin,
  canEdit,
  onSaved,
}: {
  user: AdminUser;
  canAssignSuperAdmin: boolean;
  canEdit: boolean;
  onSaved: (user: AdminUser) => void | Promise<void>;
}) {
  const { t } = useTranslation();
  const [nickname, setNickname] = useState(user.nickname);
  const [role, setRole] = useState<AdminUserRole>(user.role);
  const [userGroupId, setUserGroupId] = useState<number>(user.userGroup.id);
  const [enabled, setEnabled] = useState(user.enabled);
  const [saving, setSaving] = useState(false);
  const [walletSummary, setWalletSummary] = useState<AdminWallet | null>(null);
  const [walletLoading, setWalletLoading] = useState(true);
  const isSuperAdmin = user.role === "super_admin";
  const editable = canEdit && !isSuperAdmin;
  const canWithdraw = user.role !== "user";

  useEffect(() => {
    setNickname(user.nickname);
    setRole(user.role);
    setUserGroupId(user.userGroup.id);
    setEnabled(user.enabled);
  }, [user]);

  useEffect(() => {
    let cancelled = false;
    setWalletSummary(null);
    if (!canWithdraw) {
      setWalletLoading(false);
      return () => {
        cancelled = true;
      };
    }
    setWalletLoading(true);
    void getMockAdminUserWallet(user.id)
      .then((wallet) => {
        if (cancelled) return;
        setWalletSummary(wallet);
      })
      .catch((error) => {
        if (!cancelled) {
          Toast.error(getIamErrorMessage(t, error, "Operation failed."));
        }
      })
      .finally(() => {
        if (!cancelled) setWalletLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [canWithdraw, t, user.id]);

  const dirty =
    nickname !== user.nickname ||
    role !== user.role ||
    userGroupId !== user.userGroup.id ||
    enabled !== user.enabled;

  const save = async () => {
    if (!editable) return;
    if (role === "super_admin" && !canAssignSuperAdmin) return;
    setSaving(true);
    try {
      const updated = await updateMockAdminUser(user.id, {
        nickname,
        role,
        userGroupId,
        enabled,
      });
      Toast.success(t("User updated."));
      await onSaved(updated);
    } catch (error) {
      Toast.error(getIamErrorMessage(t, error, "User update failed."));
    } finally {
      setSaving(false);
    }
  };

  return (
    <div className="space-y-5">
      <section className="rounded-xl border border-[var(--semi-color-border)] p-4">
        <div className="flex items-center gap-3">
          <Avatar color={avatarColor(user.id)} size="default">
            {userInitial(user)}
          </Avatar>
          <div className="min-w-0 flex-1">
            <Text
              className="block truncate text-base font-semibold"
              copyable={createCopyableConfig(user.email, t("Copied"))}
            >
              {user.email}
            </Text>
            <div className="truncate text-xs text-[var(--semi-color-text-2)]">
              {user.nickname || "-"} · #{user.id}
            </div>
          </div>
        </div>
        <div className="mt-3 grid grid-cols-1 gap-2 sm:grid-cols-3">
          <InfoItem label={t("Created At")} value={formatDateTime(user.createdAt)} />
          <InfoItem label={t("Last login")} value={formatDateTime(user.lastLoginAt)} />
          <InfoItem label={t("Updated at")} value={formatDateTime(user.updatedAt)} />
        </div>
      </section>

      <section>
        <div className="mb-3 text-sm font-semibold text-[var(--semi-color-text-0)]">
          {t("Management summary")}
        </div>
        <div
          className={`grid gap-3 sm:grid-cols-2 ${
            canWithdraw ? "lg:grid-cols-5" : "lg:grid-cols-4"
          }`}
        >
          <InfoItem label={t("Status")} value={renderStatusTag(user.enabled, t)} />
          <InfoItem label={t("Role")} value={renderRoleTag(user.role, t)} />
          <InfoItem label={t("User Group")} value={user.userGroup.name} />
          <InfoItem
            label={t("Current Balance")}
            value={<span className="font-mono-data">¥{formatMoney(user.consumerBalance)}</span>}
          />
          {canWithdraw ? (
            <InfoItem
              label={t("Withdrawable balance")}
              value={
                walletLoading ? (
                  <Spin size="small" />
                ) : (
                  <span className="font-mono-data font-semibold text-[var(--semi-color-primary)]">
                    ¥{formatMoney(walletSummary?.supplierAvailable ?? "0")}
                  </span>
                )
              }
            />
          ) : null}
        </div>
      </section>

      <section>
        <div className="mb-3 text-sm font-semibold text-[var(--semi-color-text-0)]">
          {t("Basic info")}
        </div>
        <div className="grid gap-3 sm:grid-cols-2">
          <label className="block">
            <span className="mb-1 block text-xs font-medium text-[var(--semi-color-text-1)]">
              {t("Nickname")}
            </span>
            <Input
              disabled={!editable}
              maxLength={60}
              onChange={(value) => setNickname(String(value))}
              value={nickname}
            />
          </label>
          <label className="block">
            <span className="mb-1 block text-xs font-medium text-[var(--semi-color-text-1)]">
              {t("Role")}
            </span>
            <Select
              disabled={!editable}
              onChange={(value) => setRole(String(value) as AdminUserRole)}
              style={{ width: "100%" }}
              value={role}
            >
              {ROLES.filter(
                (item) =>
                  item !== "super_admin" ||
                  canAssignSuperAdmin ||
                  user.role === "super_admin"
              ).map((item) => (
                <Select.Option key={item} value={item}>
                  {roleLabel(item, t)}
                </Select.Option>
              ))}
            </Select>
          </label>
          <label className="block">
            <span className="mb-1 block text-xs font-medium text-[var(--semi-color-text-1)]">
              {t("User Group")}
            </span>
            <Select
              disabled={!editable}
              onChange={(value) => setUserGroupId(Number(value))}
              style={{ width: "100%" }}
              value={userGroupId}
            >
              {USER_GROUPS.map((group) => (
                <Select.Option key={group.id} value={group.id}>
                  {group.name}
                </Select.Option>
              ))}
            </Select>
          </label>
          <div className="flex flex-col justify-center rounded-lg bg-[var(--semi-color-fill-0)] px-3 py-2">
            <div className="mb-1 text-xs text-[var(--semi-color-text-2)]">
              {t("Status")}
            </div>
            <div className="flex items-center gap-2">
              <Switch
                checked={enabled}
                disabled={!editable}
                onChange={setEnabled}
                size="small"
              />
              <span className="text-sm text-[var(--semi-color-text-0)]">
                {enabled ? t("Enabled") : t("Disabled")}
              </span>
            </div>
          </div>
        </div>
        <div className="mt-3 flex justify-end">
          <Button
            disabled={!editable || !dirty}
            loading={saving}
            onClick={() => void save()}
            theme="solid"
            type="primary"
          >
            {t("Save")}
          </Button>
        </div>
      </section>

    </div>
  );
}

// ---------- Invitations tab ----------

function InvitationsTab({ userId }: { userId: number }) {
  const { t } = useTranslation();
  const [pageSize, setPageSize] = useSharedPageSize();
  const [page, setPage] = useState(1);

  useEffect(() => setPage(1), [pageSize]);
  const [overview, setOverview] =
    useState<AdminUserInvitationOverview | null>(null);
  const [loading, setLoading] = useState(true);

  const load = useCallback(async () => {
    setLoading(true);
    try {
      setOverview(await getMockAdminUserInvitations(userId));
    } catch (error) {
      Toast.error(getIamErrorMessage(t, error, "Operation failed."));
    } finally {
      setLoading(false);
    }
  }, [t, userId]);

  useEffect(() => {
    setPage(1);
    void load();
  }, [load, userId]);

  const invitees = overview?.invitees ?? [];

  const columns = useMemo(
    () =>
      [
        {
          dataIndex: "id",
          title: "ID",
          width: 80,
          render: (value: number) => (
            <span className="font-mono text-[var(--semi-color-text-1)]">
              #{value}
            </span>
          ),
        },
        {
          dataIndex: "email",
          title: t("Invited user"),
          width: 280,
          render: (_: unknown, member: AdminUserInvitationMember) => (
            <InvitationIdentity member={member} copiedText={t("Copied")} />
          ),
        },
        {
          dataIndex: "role",
          title: t("Role"),
          width: 120,
          render: (value: AdminUserRole) => renderRoleTag(value, t),
        },
        {
          dataIndex: "enabled",
          title: t("Status"),
          width: 100,
          render: (value: boolean) => renderStatusTag(value, t),
        },
        {
          dataIndex: "joinedAt",
          title: t("Joined at"),
          width: 180,
          render: (value: string) => formatDateTime(value),
        },
      ] as any[],
    [t]
  );

  return (
    <div>
      <div className="mb-3 flex flex-wrap items-center justify-between gap-2">
        <div>
          <div className="text-sm font-semibold text-[var(--semi-color-text-0)]">
            {t("Invitation relationships")}
          </div>
          <div className="text-xs text-[var(--semi-color-text-2)]">
            {t("Invitation relationships hint")}
          </div>
        </div>
        <Button
          icon={<IconRefresh />}
          loading={loading}
          onClick={() => void load()}
          size="small"
          type="tertiary"
        >
          {t("Refresh")}
        </Button>
      </div>
      {loading && !overview ? (
        <div className="flex justify-center rounded-xl border border-[var(--semi-color-border)] py-16">
          <Spin />
        </div>
      ) : (
        <>
          <div className="mb-4 grid gap-3 sm:grid-cols-2">
            <InfoItem
              label={t("Invited by")}
              value={
                overview?.inviter ? (
                  <InvitationIdentity
                    copiedText={t("Copied")}
                    member={overview.inviter}
                  />
                ) : (
                  <span className="text-[var(--semi-color-text-2)]">
                    {t("No inviter")}
                  </span>
                )
              }
            />
            <InfoItem
              label={t("Direct invitees")}
              value={
                <span className="font-mono-data font-semibold">
                  {overview?.invitees.length ?? 0}
                </span>
              }
            />
          </div>
          <UserPagedTable
            columns={columns}
            emptyDescription={t("No invited users")}
            extraOffset={150}
            items={invitees}
            page={page}
            pageSize={pageSize}
            scrollX={760}
            setPage={setPage}
            setPageSize={setPageSize}
            t={t}
          />
        </>
      )}
    </div>
  );
}

// ---------- Wallet tab ----------

const QUICK_AMOUNTS = [10, 50, 100, -10, -50, -100];

export function WalletAdjustModal({
  open,
  userId,
  userIds,
  balance,
  onClose,
  onDone,
  onBulkDone,
}: {
  open: boolean;
  userId?: number;
  userIds?: number[];
  balance: string;
  onClose: () => void;
  onDone?: (wallet: AdminWallet) => void | Promise<void>;
  onBulkDone?: (signedAmount: number) => void | Promise<void>;
}) {
  const { t } = useTranslation();
  const [amount, setAmount] = useState<number | string>("");
  const [reason, setReason] = useState("");
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    if (open) {
      setAmount("");
      setReason("");
    }
  }, [open]);

  const submit = async () => {
    const value = Number(amount);
    if (!Number.isFinite(value) || value === 0) {
      Toast.warning(t("Amount cannot be zero."));
      return;
    }
    if (!reason.trim()) {
      Toast.warning(t("Reason is required."));
      return;
    }
    setSaving(true);
    try {
      if (userIds?.length) {
        const result = await adjustMockAdminUsersWallet(
          userIds,
          value,
          reason.trim()
        );
        await onBulkDone?.(value);
        Toast.success(
          t("Users bulk operation completed.", {
            count: result.affected,
            skipped: result.skipped,
          })
        );
        onClose();
        return;
      }
      if (!userId) return;
      const result =
        value > 0
          ? await creditMockAdminUserWallet(userId, value.toFixed(2), reason.trim())
          : await debitMockAdminUserWallet(
              userId,
              Math.abs(value).toFixed(2),
              reason.trim()
            );
      await onDone?.(result.wallet);
      Toast.success(
        value > 0 ? t("Balance credited.") : t("Balance debited.")
      );
      onClose();
    } catch (error) {
      Toast.error(getIamErrorMessage(t, error, "Operation failed."));
    } finally {
      setSaving(false);
    }
  };

  return (
    <Modal
      centered
      confirmLoading={saving}
      onCancel={onClose}
      onOk={() => void submit()}
      okText={t("Adjust balance")}
      cancelText={t("Cancel")}
      size="small"
      title={t("Adjust balance")}
      visible={open}
    >
      <div className="space-y-4 py-1">
        <div className="rounded-lg bg-[var(--semi-color-fill-0)] px-3 py-2 text-sm">
          <span className="text-[var(--semi-color-text-2)]">
            {t("Current Balance")}
          </span>
          <span className="ml-2 font-mono-data font-semibold text-[var(--semi-color-text-0)]">
            {balance === "-" ? "-" : `¥${formatMoney(balance)}`}
          </span>
        </div>
        <label className="block">
          <span className="mb-1.5 block text-sm font-medium text-[var(--semi-color-text-0)]">
            {t("Amount")} *
          </span>
          <InputNumber
            onChange={setAmount}
            precision={2}
            prefix="¥"
            step={1}
            style={{ width: "100%" }}
            value={amount}
          />
          <div className="mt-1 text-xs text-[var(--semi-color-text-2)]">
            {t("Signed amount hint")}
          </div>
          <div className="mt-2 flex flex-wrap gap-1.5">
            {QUICK_AMOUNTS.map((value) => (
              <Button
                key={value}
                onClick={() => setAmount(value)}
                size="small"
                type="tertiary"
              >
                {value > 0 ? "+" : "-"}¥{Math.abs(value)}
              </Button>
            ))}
          </div>
        </label>
        <label className="block">
          <span className="mb-1.5 block text-sm font-medium text-[var(--semi-color-text-0)]">
            {t("Reason")} *
          </span>
          <Input
            maxLength={200}
            onChange={(value) => setReason(String(value))}
            placeholder={t("Adjustment reason placeholder")}
            value={reason}
          />
        </label>
      </div>
    </Modal>
  );
}

function WalletTab({
  userId,
  role,
  canAdjust,
  onBalanceChanged,
}: {
  userId: number;
  role: AdminUserRole;
  canAdjust: boolean;
  onBalanceChanged: (balance: string) => void | Promise<void>;
}) {
  const { t } = useTranslation();
  const [wallet, setWallet] = useState<AdminWallet | null>(null);
  const [transactions, setTransactions] = useState<AdminTransaction[]>([]);
  const [loading, setLoading] = useState(true);
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = useSharedPageSize();

  useEffect(() => setPage(1), [pageSize]);
  const [adjustOpen, setAdjustOpen] = useState(false);

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const [walletResult, txResult] = await Promise.all([
        getMockAdminUserWallet(userId),
        listMockAdminUserTransactions(userId, undefined, 500),
      ]);
      setWallet(walletResult);
      setTransactions(txResult.items);
      setPage(1);
    } catch (error) {
      Toast.error(getIamErrorMessage(t, error, "Wallet load failed."));
    } finally {
      setLoading(false);
    }
  }, [t, userId]);

  useEffect(() => {
    void load();
  }, [load]);

  const onAdjusted = async (updated: AdminWallet) => {
    setWallet(updated);
    try {
      const txResult = await listMockAdminUserTransactions(
        userId,
        undefined,
        500
      );
      setTransactions(txResult.items);
      setPage(1);
    } catch (error) {
      Toast.error(getIamErrorMessage(t, error, "Wallet load failed."));
    }
    await onBalanceChanged(updated.consumerBalance);
  };

  const columns = useMemo(
    () =>
      [
        {
          dataIndex: "transactionNo",
          title: t("Transaction No"),
          width: 210,
          render: (value: string) => (
            <CopyableTableText copiedText={t("Copied")} text={value} />
          ),
        },
        {
          dataIndex: "transactionType",
          title: t("Type"),
          width: 135,
          render: (_: unknown, record: AdminTransaction) =>
            renderTransactionTypeTag(record.transactionType, record.direction, t),
        },
        {
          dataIndex: "amount",
          title: t("Amount"),
          width: 120,
          render: (value: string, record: AdminTransaction) => (
            <span
              className={`font-mono-data font-semibold ${
                record.direction === "in"
                  ? "text-[var(--semi-color-success)]"
                  : "text-[var(--semi-color-warning)]"
              }`}
            >
              {record.direction === "in" ? "+" : "-"}¥{formatMoney(value)}
            </span>
          ),
        },
        {
          dataIndex: "balanceAfter",
          title: t("Balance after"),
          width: 130,
          render: (value: string) => (
            <span className="font-mono-data">¥{formatMoney(value)}</span>
          ),
        },
        {
          dataIndex: "bizId",
          title: t("Reason"),
          width: 180,
          render: (value: string) => (
            <OverflowTooltip className="max-w-[160px]" content={value || "-"}>
              {value || "-"}
            </OverflowTooltip>
          ),
        },
        {
          dataIndex: "createdAt",
          title: t("Created at"),
          width: 180,
          render: (value: string) => formatDateTime(value),
        },
      ] as any[],
    [t]
  );

  if (loading || !wallet) {
    return (
      <div className="flex justify-center py-16">
        <Spin size="large" />
      </div>
    );
  }

  const showSupplierBuckets =
    role !== "user" ||
    Number(wallet.supplierAvailable) > 0 ||
    Number(wallet.supplierFrozen) > 0;

  return (
    <div>
      <div className="mb-4">
        <div className="mb-3 flex flex-wrap items-center justify-between gap-2">
          <div>
            <div className="text-sm font-semibold text-[var(--semi-color-text-0)]">
              {t("Wallet overview")}
            </div>
            <div className="text-xs text-[var(--semi-color-text-2)]">
              {t("Updated at")} · {formatRelativeTime(wallet.updatedAt, t)}
            </div>
          </div>
          <Button
            disabled={!canAdjust}
            icon={<IconEdit />}
            onClick={() => {
              if (canAdjust) setAdjustOpen(true);
            }}
            size="small"
            theme="solid"
            type="primary"
          >
            {t("Adjust balance")}
          </Button>
        </div>
        <div
          className={`grid gap-3 sm:grid-cols-2 ${
            showSupplierBuckets ? "lg:grid-cols-5" : "lg:grid-cols-3"
          }`}
        >
          <InfoItem
            label={t("Current Balance")}
            value={
              <span className="font-mono-data font-semibold text-[var(--semi-color-primary)]">
                ¥{formatMoney(wallet.consumerBalance)}
              </span>
            }
          />
          <InfoItem
            label={t("Historical Spend")}
            value={<span className="font-mono-data">¥{formatMoney(wallet.historicalSpend)}</span>}
          />
          <InfoItem
            label={t("Order Count")}
            value={<span className="font-mono-data">{wallet.orderCount}</span>}
          />
          {showSupplierBuckets ? (
            <>
              <InfoItem
                label={t("Supplier available")}
                value={<span className="font-mono-data">¥{formatMoney(wallet.supplierAvailable)}</span>}
              />
              <InfoItem
                label={t("Supplier frozen")}
                value={<span className="font-mono-data">¥{formatMoney(wallet.supplierFrozen)}</span>}
              />
            </>
          ) : null}
        </div>
      </div>

      <div className="mb-3 text-sm font-semibold text-[var(--semi-color-text-0)]">
        {t("Transaction history")}
      </div>
      <UserPagedTable
        columns={columns}
        emptyDescription={t("No transactions")}
        extraOffset={190}
        items={transactions}
        page={page}
        pageSize={pageSize}
        scrollX={955}
        setPage={setPage}
        setPageSize={setPageSize}
        t={t}
      />

      <WalletAdjustModal
        balance={wallet.consumerBalance}
        onClose={() => setAdjustOpen(false)}
        onDone={onAdjusted}
        open={canAdjust && adjustOpen}
        userId={userId}
      />
    </div>
  );
}

// ---------- API keys tab ----------

function maskApiKey(value: string) {
  if (value.length <= 18) return value;
  return `${value.slice(0, 7)}**********${value.slice(-4)}`;
}

function toDateInput(value?: string | null) {
  if (!value) return null;
  return value.slice(0, 10);
}

function toExpireAt(value: string | null) {
  if (!value) return null;
  return new Date(`${value}T23:59:59`).toISOString();
}

function normalizeDatePickerValue(value: unknown) {
  if (Array.isArray(value)) return normalizeDatePickerValue(value[0]);
  if (value instanceof Date) return value.toISOString().slice(0, 10);
  if (typeof value === "string" && value.trim()) return value.trim().slice(0, 10);
  return null;
}

function normalizeOptionalPositiveInteger(value: number | string | null | undefined) {
  if (value === "" || value == null) return null;
  const parsed = Number(value);
  if (!Number.isFinite(parsed) || parsed <= 0) return null;
  return Math.floor(parsed);
}

function ApiKeysTab({
  userId,
  canManage,
}: {
  userId: number;
  canManage: boolean;
}) {
  const { t } = useTranslation();
  const [apiKeys, setApiKeys] = useState<AdminApiKey[]>([]);
  const [loading, setLoading] = useState(true);
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = useSharedPageSize();

  useEffect(() => setPage(1), [pageSize]);
  const [modalOpen, setModalOpen] = useState(false);
  const [editing, setEditing] = useState<AdminApiKey | null>(null);
  const [name, setName] = useState("");
  const [expiresAt, setExpiresAt] = useState<string | null>(null);
  const [quota, setQuota] = useState<number | null>(null);
  const [rpm, setRpm] = useState<number | null>(null);
  const [saving, setSaving] = useState(false);
  const [operatingId, setOperatingId] = useState<number | null>(null);

  const load = useCallback(async () => {
    setLoading(true);
    try {
      setApiKeys(await listMockAdminUserApiKeys(userId));
    } catch (error) {
      Toast.error(getIamErrorMessage(t, error, "Failed to load API keys."));
    } finally {
      setLoading(false);
    }
  }, [t, userId]);

  useEffect(() => {
    void load();
  }, [load]);

  const openCreate = () => {
    if (!canManage) return;
    setEditing(null);
    setName("");
    setExpiresAt(null);
    setQuota(null);
    setRpm(null);
    setModalOpen(true);
  };

  const openEdit = (record: AdminApiKey) => {
    if (!canManage) return;
    setEditing(record);
    setName(record.name);
    setExpiresAt(toDateInput(record.expireAt));
    setQuota(record.quotaLimit ?? null);
    setRpm(record.rateLimitPerMinute ?? null);
    setModalOpen(true);
  };

  const save = async () => {
    if (!canManage) return;
    if (!name.trim()) {
      Toast.warning(t("Please enter API key name."));
      return;
    }
    setSaving(true);
    try {
      if (editing) {
        const updated = await updateMockAdminUserApiKey(userId, editing.id, {
          name: name.trim(),
          expireAt: toExpireAt(expiresAt),
          quotaLimit: quota,
          rateLimitPerMinute: rpm,
        });
        setApiKeys((items) =>
          items.map((item) => (item.id === editing.id ? updated : item))
        );
        Toast.success(t("API key updated."));
      } else {
        const created = await createMockAdminUserApiKey(userId, {
          name: name.trim(),
          expireAt: toExpireAt(expiresAt),
          quotaLimit: quota,
          rateLimitPerMinute: rpm,
        });
        setApiKeys((items) => [created, ...items]);
        Toast.success(t("API key created."));
      }
      setModalOpen(false);
    } catch (error) {
      Toast.error(getIamErrorMessage(t, error, "API key operation failed."));
    } finally {
      setSaving(false);
    }
  };

  const toggleEnabled = async (record: AdminApiKey) => {
    if (!canManage) return;
    setOperatingId(record.id);
    try {
      const updated = await updateMockAdminUserApiKey(userId, record.id, {
        enabled: !record.enabled,
      });
      setApiKeys((items) =>
        items.map((item) => (item.id === record.id ? updated : item))
      );
      Toast.success(t(record.enabled ? "API key disabled." : "API key enabled."));
    } catch (error) {
      Toast.error(getIamErrorMessage(t, error, "API key operation failed."));
    } finally {
      setOperatingId(null);
    }
  };

  const remove = (record: AdminApiKey) => {
    if (!canManage) return;
    Modal.confirm({
      cancelText: t("Cancel"),
      content: t("Confirm delete API key content", { name: record.name }),
      okButtonProps: { type: "danger" },
      okText: t("Delete"),
      onOk: async () => {
        setOperatingId(record.id);
        try {
          await deleteMockAdminUserApiKey(userId, record.id);
          setApiKeys((items) => items.filter((item) => item.id !== record.id));
          Toast.success(t("API key deleted."));
        } catch (error) {
          Toast.error(getIamErrorMessage(t, error, "API key operation failed."));
          throw error;
        } finally {
          setOperatingId(null);
        }
      },
      title: t("Confirm delete"),
    });
  };

  const columns = useMemo(
    () =>
      [
        {
          dataIndex: "name",
          title: t("API key"),
          width: 250,
          render: (_: unknown, record: AdminApiKey) => (
            <div className="min-w-0">
              <OverflowTooltip
                className="max-w-[220px] font-medium text-[var(--semi-color-text-0)]"
                content={record.name}
              >
                {record.name}
              </OverflowTooltip>
              <Text
                className="font-mono-data mt-0.5 block text-xs"
                copyable={createCopyableConfig(
                  record.keyPlain || record.keyPrefix,
                  t("Copied")
                )}
                type="tertiary"
              >
                {maskApiKey(record.keyPlain || record.keyPrefix)}
              </Text>
            </div>
          ),
        },
        {
          dataIndex: "enabled",
          title: t("Status"),
          width: 95,
          render: (value: boolean) => renderStatusTag(value, t),
        },
        {
          dataIndex: "quotaLimit",
          title: t("Quota usage"),
          width: 140,
          render: (_: unknown, record: AdminApiKey) => (
            <span className="font-mono-data">
              {record.quotaLimit == null
                ? t("Unlimited")
                : `${record.quotaUsed.toLocaleString()} / ${record.quotaLimit.toLocaleString()}`}
            </span>
          ),
        },
        {
          dataIndex: "rateLimitPerMinute",
          title: t("RPM limit"),
          width: 110,
          render: (value: number | null) =>
            value == null ? t("Unlimited") : value.toLocaleString(),
        },
        {
          dataIndex: "lastUsedAt",
          title: t("Last used"),
          width: 140,
          render: (value: string | null) =>
            value ? formatRelativeTime(value, t) : t("Never"),
        },
        {
          dataIndex: "expireAt",
          title: t("Expires at"),
          width: 170,
          render: (value: string | null) => (value ? formatDateTime(value) : t("No expiry")),
        },
        {
          dataIndex: "operate",
          fixed: "right",
          title: t("Actions"),
          width: 250,
          render: (_: unknown, record: AdminApiKey) => (
            <Space spacing={4} wrap={false}>
              <Button
                disabled={!canManage}
                loading={operatingId === record.id}
                onClick={() => void toggleEnabled(record)}
                size="small"
                type="tertiary"
              >
                {record.enabled ? t("Disable") : t("Enable")}
              </Button>
              <Button
                disabled={!canManage}
                onClick={() => openEdit(record)}
                size="small"
                type="tertiary"
              >
                {t("Edit")}
              </Button>
              <Button
                disabled={!canManage || operatingId === record.id}
                onClick={() => remove(record)}
                size="small"
                type="danger"
              >
                {t("Delete")}
              </Button>
            </Space>
          ),
        },
      ] as any[],
    [canManage, operatingId, t]
  );

  const activeCount = apiKeys.filter((item) => item.enabled).length;
  const usedQuota = apiKeys.reduce((sum, item) => sum + item.quotaUsed, 0);

  return (
    <div>
      <div className="mb-3 flex flex-wrap items-center justify-between gap-2">
        <div>
          <div className="text-sm font-semibold text-[var(--semi-color-text-0)]">
            {t("API key management")}
          </div>
          <div className="text-xs text-[var(--semi-color-text-2)]">
            {t("Manage API key status, limits and lifecycle.")}
          </div>
        </div>
        <Button
          disabled={!canManage}
          icon={<IconPlus />}
          onClick={openCreate}
          size="small"
          theme="solid"
          type="primary"
        >
          {t("Create API key")}
        </Button>
      </div>

      <div className="mb-4 grid gap-3 sm:grid-cols-3">
        <InfoItem
          label={t("Total API keys")}
          value={<span className="font-mono-data">{apiKeys.length}</span>}
        />
        <InfoItem
          label={t("Active API keys")}
          value={
            <span className="font-mono-data font-semibold text-[var(--semi-color-primary)]">
              {activeCount}
            </span>
          }
        />
        <InfoItem
          label={t("Quota used")}
          value={<span className="font-mono-data">{usedQuota.toLocaleString()}</span>}
        />
      </div>

      {loading ? (
        <div className="flex justify-center py-16">
          <Spin size="large" />
        </div>
      ) : (
        <UserPagedTable
          columns={columns}
          emptyDescription={t("No API keys")}
          extraOffset={150}
          items={apiKeys}
          page={page}
          pageSize={pageSize}
          scrollX={1155}
          setPage={setPage}
          setPageSize={setPageSize}
          t={t}
        />
      )}

      <Modal
        centered
        confirmLoading={saving}
        onCancel={() => setModalOpen(false)}
        onOk={() => void save()}
        okButtonProps={{ disabled: !canManage }}
        okText={t("Save")}
        cancelText={t("Cancel")}
        size="small"
        title={editing ? t("Edit API key") : t("Create API key")}
        visible={modalOpen}
      >
        <div className="space-y-4 py-1">
          <label className="block">
            <span className="mb-1.5 block text-sm font-medium">{t("Name")} *</span>
            <Input
              autoFocus
              maxLength={80}
              onChange={(value) => setName(String(value))}
              placeholder={t("API key name")}
              prefix={<IconKey />}
              value={name}
            />
          </label>
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
            <label className="block">
              <span className="mb-1.5 block text-sm font-medium">{t("Expires at")}</span>
              <DatePicker
                format="yyyy-MM-dd"
                inputReadOnly
                onChange={(value, valueText) =>
                  setExpiresAt(normalizeDatePickerValue(valueText ?? value))
                }
                placeholder={t("No expiry")}
                showClear
                style={{ width: "100%" }}
                type="date"
                value={expiresAt ?? undefined}
              />
            </label>
            <label className="block">
              <span className="mb-1.5 block text-sm font-medium">{t("RPM limit")}</span>
              <InputNumber
                min={1}
                onChange={(value) => setRpm(normalizeOptionalPositiveInteger(value))}
                placeholder={t("Unlimited")}
                precision={0}
                showClear
                step={10}
                style={{ width: "100%" }}
                value={rpm ?? ""}
              />
            </label>
          </div>
          <label className="block">
            <span className="mb-1.5 block text-sm font-medium">{t("Quota limit")}</span>
            <InputNumber
              min={1}
              onChange={(value) => setQuota(normalizeOptionalPositiveInteger(value))}
              placeholder={t("Unlimited")}
              precision={0}
              showClear
              step={100}
              style={{ width: "100%" }}
              value={quota ?? ""}
            />
          </label>
        </div>
      </Modal>
    </div>
  );
}

// ---------- Permissions tab ----------

function PermissionsTab({
  user,
  canEdit,
}: {
  user: AdminUser;
  canEdit: boolean;
}) {
  const { t } = useTranslation();
  const [overrides, setOverrides] = useState<Map<string, PermissionEffect>>(new Map());
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);

  const baseline = useMemo(
    () => new Set(roleBaselinePermissions(user.role)),
    [user.role]
  );

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const policies = await getMockAdminUserPermissions(user.id);
      const map = new Map<string, PermissionEffect>();
      for (const policy of policies) {
        map.set(`${policy.resource}:${policy.action}`, policy.effect);
      }
      setOverrides(map);
    } catch (error) {
      Toast.error(getIamErrorMessage(t, error, "Permissions load failed."));
    } finally {
      setLoading(false);
    }
  }, [t, user.id]);

  useEffect(() => {
    void load();
  }, [load]);

  const setState = (key: string, state: PermissionState) => {
    if (!canEdit) return;
    setOverrides((previous) => {
      const next = new Map(previous);
      if (state === "inherit") next.delete(key);
      else next.set(key, state);
      return next;
    });
  };

  const stateFor = (key: string): PermissionState => overrides.get(key) ?? "inherit";

  const effectiveAllowed = (key: string) => {
    const override = overrides.get(key);
    if (override) return override === "allow";
    return baseline.has(key);
  };

  const save = async () => {
    if (!canEdit) return;
    setSaving(true);
    try {
      const policies: PermissionPolicy[] = Array.from(overrides.entries()).map(
        ([key, effect]) => {
          const lastColon = key.lastIndexOf(":");
          return {
            resource: key.slice(0, lastColon),
            action: key.slice(lastColon + 1),
            effect,
          };
        }
      );
      await putMockAdminUserPermissions(user.id, policies);
      Toast.success(t("Permissions saved."));
    } catch (error) {
      Toast.error(getIamErrorMessage(t, error, "Permissions save failed."));
    } finally {
      setSaving(false);
    }
  };

  if (loading) {
    return (
      <div className="flex justify-center py-16">
        <Spin size="large" />
      </div>
    );
  }

  const overrideCount = overrides.size;
  const permissionKeys = PERMISSION_CATALOG.flatMap((item) =>
    item.actions.map((action) => `${item.resource}:${action}`)
  );
  const allowedCount = permissionKeys.filter(effectiveAllowed).length;

  return (
    <div className="space-y-4">
      <div className="rounded-lg bg-[var(--semi-color-fill-0)] px-3 py-2 text-xs text-[var(--semi-color-text-2)]">
        {t("Permissions hint", { role: roleLabel(user.role, t) })}
      </div>
      <div className="grid gap-3 sm:grid-cols-3">
        <InfoItem
          label={t("Total permissions")}
          value={<span className="font-mono-data">{permissionKeys.length}</span>}
        />
        <InfoItem
          label={t("Effective permissions")}
          value={
            <span className="font-mono-data font-semibold text-[var(--semi-color-primary)]">
              {allowedCount}
            </span>
          }
        />
        <InfoItem
          label={t("Permission overrides")}
          value={<span className="font-mono-data">{overrideCount}</span>}
        />
      </div>

      <div className="space-y-3">
        {PERMISSION_CATALOG.map((item) => (
          <div
            className="rounded-xl border border-[var(--semi-color-border)] p-3"
            key={item.resource}
          >
            <div className="mb-2 flex items-center gap-2">
              <span className="text-sm font-medium text-[var(--semi-color-text-0)]">
                {permissionResourceLabel(item.resource, t)}
              </span>
              <span className="font-mono-data text-xs text-[var(--semi-color-text-2)]">
                {item.resource}
              </span>
            </div>
            <div className="space-y-2">
              {item.actions.map((action) => {
                const key = `${item.resource}:${action}`;
                const state = stateFor(key);
                const allowed = effectiveAllowed(key);
                return (
                  <div
                    className="flex flex-wrap items-center justify-between gap-2"
                    key={key}
                  >
                    <div className="flex min-w-0 items-center gap-2">
                      <span className="text-sm text-[var(--semi-color-text-1)]">
                        {permissionActionLabel(action, t)}
                      </span>
                      <span className="font-mono-data text-xs text-[var(--semi-color-text-2)]">
                        {action}
                      </span>
                      <Tag
                        color={allowed ? "green" : "grey"}
                        shape="circle"
                        size="small"
                      >
                        {allowed ? t("Allowed") : t("Denied")}
                      </Tag>
                    </div>
                    <RadioGroup
                      buttonSize="small"
                      disabled={!canEdit}
                      onChange={(event) =>
                        setState(key, event.target.value as PermissionState)
                      }
                      type="button"
                      value={state}
                    >
                      <Radio value="inherit">{t("Inherit")}</Radio>
                      <Radio value="allow">{t("Allow")}</Radio>
                      <Radio value="deny">{t("Deny")}</Radio>
                    </RadioGroup>
                  </div>
                );
              })}
            </div>
          </div>
        ))}
      </div>

      <div className="sticky bottom-0 flex items-center justify-between gap-2 border-t border-[var(--semi-color-border)] bg-[var(--semi-color-bg-0)] py-3">
        <span className="text-xs text-[var(--semi-color-text-2)]">
          {t("Override count", { count: overrideCount })}
        </span>
        <Space>
          <Button
            disabled={!canEdit || overrideCount === 0}
            icon={<IconRefresh />}
            onClick={() => setOverrides(new Map())}
            type="tertiary"
          >
            {t("Clear overrides")}
          </Button>
          <Button
            disabled={!canEdit}
            loading={saving}
            onClick={() => void save()}
            theme="solid"
            type="primary"
          >
            {t("Save")}
          </Button>
        </Space>
      </div>
    </div>
  );
}

// ---------- Sheet shell ----------

export function UserDetailSheet({
  user,
  initialTab = "profile",
  capabilities,
  onClose,
  onChanged,
  onDeleted,
}: {
  user: AdminUser | null;
  initialTab?: UserDetailTab;
  capabilities: AdminUserCapabilities;
  onClose: () => void;
  onChanged: (user: AdminUser) => void | Promise<void>;
  onDeleted?: (userId: number) => void | Promise<void>;
}) {
  const { t } = useTranslation();
  const isMobile = useIsMobile();
  const [activeTab, setActiveTab] = useState<string>(initialTab);
  const [current, setCurrent] = useState<AdminUser | null>(user);
  const [balanceOpen, setBalanceOpen] = useState(false);
  const [managementAction, setManagementAction] = useState<
    "status" | "logout" | "delete" | null
  >(null);
  const userId = user?.id;

  useEffect(() => {
    setCurrent(user);
  }, [user]);

  useEffect(() => {
    if (userId) setActiveTab(initialTab);
  }, [initialTab, userId]);

  const handleSaved = async (updated: AdminUser) => {
    setCurrent(updated);
    await onChanged(updated);
  };

  const toggleStatus = async () => {
    if (
      !current ||
      !canMutateAdminUser(current.role, capabilities.canOperateUsers)
    ) {
      return;
    }
    setManagementAction("status");
    try {
      const updated = await updateMockAdminUser(current.id, {
        enabled: !current.enabled,
      });
      await handleSaved(updated);
      Toast.success(current.enabled ? t("User disabled.") : t("User enabled."));
    } catch (error) {
      Toast.error(getIamErrorMessage(t, error, "User update failed."));
    } finally {
      setManagementAction(null);
    }
  };

  const confirmForceLogout = () => {
    if (
      !current ||
      !canMutateAdminUser(current.role, capabilities.canOperateUsers)
    ) {
      return;
    }
    Modal.confirm({
      cancelText: t("Cancel"),
      content: t("Confirm revoke sessions content"),
      okButtonProps: { type: "danger" },
      okText: t("Force logout"),
      onOk: async () => {
        setManagementAction("logout");
        try {
          await revokeMockAdminUserSessions(current.id);
          Toast.success(t("All sessions revoked."));
        } catch (error) {
          Toast.error(getIamErrorMessage(t, error, "Operation failed."));
          throw error;
        } finally {
          setManagementAction(null);
        }
      },
      title: t("Force logout"),
    });
  };

  const confirmDelete = () => {
    if (
      !current ||
      !canMutateAdminUser(current.role, capabilities.canOperateUsers)
    ) {
      return;
    }
    Modal.confirm({
      cancelText: t("Cancel"),
      content: t("Confirm delete user content", { email: current.email }),
      okButtonProps: { type: "danger" },
      okText: t("Delete"),
      onOk: async () => {
        setManagementAction("delete");
        try {
          await deleteMockAdminUser(current.id);
          Toast.success(t("User deleted."));
          await onDeleted?.(current.id);
          onClose();
        } catch (error) {
          Toast.error(getIamErrorMessage(t, error, "User delete failed."));
          throw error;
        } finally {
          setManagementAction(null);
        }
      },
      title: t("Delete user"),
    });
  };

  const canEditCurrent = current
    ? canMutateAdminUser(current.role, capabilities.canWriteUsers)
    : false;
  const canOperateCurrent = current
    ? canMutateAdminUser(current.role, capabilities.canOperateUsers)
    : false;
  const canAdjustCurrent = current
    ? canMutateAdminUser(current.role, capabilities.canAdjustBalance)
    : false;
  const canManageCurrentApiKeys = current
    ? canMutateAdminUser(current.role, capabilities.canManageApiKeys)
    : false;
  const canEditCurrentPermissions = current
    ? canMutateAdminUser(current.role, capabilities.canEditPermissions)
    : false;

  return (
    <SideSheet
      bodyStyle={{ padding: 0 }}
      onCancel={onClose}
      placement="right"
      title={
        current ? (
          <div className="flex min-w-0 items-center gap-2.5">
            <Avatar color={avatarColor(current.id)} size="extra-small">
              {userInitial(current)}
            </Avatar>
            <span className="truncate">{current.email}</span>
            {renderRoleTag(current.role, t)}
            {renderStatusTag(current.enabled, t)}
          </div>
        ) : (
          t("User detail")
        )
      }
      visible={Boolean(user)}
      width={isMobile ? "100%" : 940}
    >
      {current ? (
        <div className="flex min-h-full flex-col">
          <div className="sticky top-0 z-10 bg-[var(--semi-color-bg-2)] px-5 pt-2">
            <Tabs activeKey={activeTab} collapsible onChange={setActiveTab} type="line">
              <Tabs.TabPane itemKey="profile" tab={t("Profile")} />
              <Tabs.TabPane itemKey="invitations" tab={t("Invitations")} />
              <Tabs.TabPane itemKey="wallet" tab={t("Wallet Management")} />
              <Tabs.TabPane itemKey="apikeys" tab={t("API KEY")} />
              <Tabs.TabPane itemKey="permissions" tab={t("Permissions")} />
            </Tabs>
          </div>
          <div className="flex-1 p-5">
            {activeTab === "profile" ? (
              <ProfileTab
                canAssignSuperAdmin={capabilities.canAssignSuperAdmin}
                canEdit={canEditCurrent}
                onSaved={handleSaved}
                user={current}
              />
            ) : null}
            {activeTab === "invitations" ? (
              <InvitationsTab userId={current.id} />
            ) : null}
            {activeTab === "wallet" ? (
              <WalletTab
                canAdjust={canAdjustCurrent}
                key={`${current.id}:${current.consumerBalance}`}
                onBalanceChanged={(balance) =>
                  handleSaved({ ...current, consumerBalance: balance })
                }
                role={current.role}
                userId={current.id}
              />
            ) : null}
            {activeTab === "apikeys" ? (
              <ApiKeysTab
                canManage={canManageCurrentApiKeys}
                userId={current.id}
              />
            ) : null}
            {activeTab === "permissions" ? (
              <PermissionsTab
                canEdit={canEditCurrentPermissions}
                user={current}
              />
            ) : null}
          </div>
          <div className="sticky bottom-0 flex flex-wrap items-center justify-end gap-2 border-t border-[var(--semi-color-border)] bg-[var(--semi-color-bg-0)] px-5 py-3">
            <Space wrap>
              <Button
                disabled={!canEditCurrent}
                onClick={() => setActiveTab("profile")}
                type="tertiary"
              >
                {t("Edit")}
              </Button>
              <Button
                disabled={!canAdjustCurrent}
                onClick={() => {
                  if (canAdjustCurrent) setBalanceOpen(true);
                }}
                type="tertiary"
              >
                {t("Adjust balance")}
              </Button>
              <Button
                disabled={!canOperateCurrent}
                loading={managementAction === "status"}
                onClick={() => void toggleStatus()}
                type="tertiary"
              >
                {current.enabled ? t("Disable") : t("Enable")}
              </Button>
              <Button
                disabled={!canOperateCurrent}
                loading={managementAction === "logout"}
                onClick={confirmForceLogout}
                type="tertiary"
              >
                {t("Exit")}
              </Button>
              <Button
                disabled={!canOperateCurrent}
                loading={managementAction === "delete"}
                onClick={confirmDelete}
                type="danger"
              >
                {t("Delete")}
              </Button>
            </Space>
          </div>
          <WalletAdjustModal
            balance={current.consumerBalance}
            onClose={() => setBalanceOpen(false)}
            onDone={(wallet) =>
              handleSaved({
                ...current,
                consumerBalance: wallet.consumerBalance,
              })
            }
            open={canAdjustCurrent && balanceOpen}
            userId={current.id}
          />
        </div>
      ) : null}
    </SideSheet>
  );
}
