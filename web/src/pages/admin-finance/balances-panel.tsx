import { useCallback, useEffect, useMemo, useState, type ReactNode } from "react";
import { Button, Input, InputNumber, Modal, Space, Toast } from "@douyinfe/semi-ui";
import { IconSearch } from "@douyinfe/semi-icons";
import { useTranslation } from "react-i18next";

import { CardPro } from "@/components/semi/card-pro";
import { createCardProPagination } from "@/components/semi/card-pro-pagination";
import {
  CardTable,
  DESKTOP_TABLE_SCROLL_Y,
} from "@/components/semi/card-table";
import { CompactModeToggle } from "@/components/semi/compact-mode-toggle";
import { hasPermission, useAuth } from "@/context/auth-provider";
import { useBlockPagedList } from "@/hooks/use-block-paged-list";
import { useDebouncedValue } from "@/hooks/use-debounced-value";
import { useIsMobile } from "@/hooks/use-is-mobile";
import { useSharedPageSize } from "@/hooks/use-shared-page-size";
import { getIamErrorMessage } from "@/lib/iam-errors";
import { useSelectionNotification } from "../resources/use-selection-notification";
import {
  adjustMockFinanceUsersWallet,
  creditMockFinanceUserWallet,
  debitMockFinanceUserWallet,
  listMockFinanceUserBalances,
  withdrawMockFinanceUserWallet,
  type FinanceUserBalance,
} from "./admin-finance-mock";
import { formatDateTime, formatMoney } from "./finance-meta";
import { emptyNode } from "./finance-shared";
import { BalanceAccountCell } from "./balance-meta";

const QUICK_AMOUNTS = [10, 50, 100, -10, -50, -100];

function FinanceAdjustModal({
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
  onDone?: (wallet: FinanceUserBalance) => void | Promise<void>;
  onBulkDone?: (signedAmount: number) => void | Promise<void>;
}) {
  const { t } = useTranslation();
  const [amount, setAmount] = useState<number | string>("");
  const [reason, setReason] = useState("");
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    if (!open) return;
    setAmount("");
    setReason("");
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
        const result = await adjustMockFinanceUsersWallet(
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
          ? await creditMockFinanceUserWallet(
              userId,
              value.toFixed(2),
              reason.trim()
            )
          : await debitMockFinanceUserWallet(
              userId,
              Math.abs(value).toFixed(2),
              reason.trim()
            );
      Toast.success(
        value > 0 ? t("Balance credited.") : t("Balance debited.")
      );
      await onDone?.(result.wallet);
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

function FinanceWithdrawModal({
  open,
  user,
  onClose,
  onDone,
}: {
  open: boolean;
  user: FinanceUserBalance | null;
  onClose: () => void;
  onDone?: (wallet: FinanceUserBalance) => void | Promise<void>;
}) {
  const { t } = useTranslation();
  const [amount, setAmount] = useState<number | string>("");
  const [note, setNote] = useState("");
  const [saving, setSaving] = useState(false);

  const available = Number(user?.supplierAvailable ?? 0) || 0;

  useEffect(() => {
    if (!open) return;
    setAmount("");
    setNote("");
  }, [open]);

  const submit = async () => {
    if (!user) return;
    const value = Number(amount);
    if (!Number.isFinite(value) || value <= 0) {
      Toast.warning(t("Amount must be positive."));
      return;
    }
    if (value > available) {
      Toast.warning(t("Withdrawal exceeds withdrawable balance."));
      return;
    }
    setSaving(true);
    try {
      const result = await withdrawMockFinanceUserWallet(
        user.userId,
        value.toFixed(2),
        note.trim()
      );
      Toast.success(t("Withdrawal submitted."));
      await onDone?.(result.wallet);
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
      okText={t("Withdraw")}
      cancelText={t("Cancel")}
      size="small"
      title={t("Withdraw")}
      visible={open}
    >
      <div className="space-y-4 py-1">
        <div className="rounded-lg bg-[var(--semi-color-fill-0)] px-3 py-2 text-sm">
          <span className="text-[var(--semi-color-text-2)]">
            {t("Withdrawable balance")}
          </span>
          <span className="ml-2 font-mono-data font-semibold text-[var(--semi-color-text-0)]">
            ¥{formatMoney(user?.supplierAvailable ?? "0")}
          </span>
        </div>
        <label className="block">
          <span className="mb-1.5 block text-sm font-medium text-[var(--semi-color-text-0)]">
            {t("Amount")} *
          </span>
          <InputNumber
            max={available}
            min={0.01}
            onChange={setAmount}
            precision={2}
            prefix="¥"
            step={1}
            style={{ width: "100%" }}
            value={amount}
          />
          <div className="mt-2 flex flex-wrap gap-1.5">
            {[50, 100, 500].map((value) => (
              <Button
                disabled={value > available}
                key={value}
                onClick={() => setAmount(value)}
                size="small"
                type="tertiary"
              >
                ¥{value}
              </Button>
            ))}
            <Button
              disabled={available <= 0}
              onClick={() => setAmount(Number(available.toFixed(2)))}
              size="small"
              type="tertiary"
            >
              {t("Withdraw all")}
            </Button>
          </div>
        </label>
        <label className="block">
          <span className="mb-1.5 block text-sm font-medium text-[var(--semi-color-text-0)]">
            {t("Note")}
          </span>
          <Input
            maxLength={200}
            onChange={(value) => setNote(String(value))}
            placeholder={t("Withdrawal note placeholder")}
            value={note}
          />
        </label>
      </div>
    </Modal>
  );
}

export function BalancesPanel({ tabsArea }: { tabsArea: ReactNode }) {
  const { t } = useTranslation();
  const { currentUser } = useAuth();
  const canOperate = hasPermission(currentUser, "billing:wallet", "operate");
  const isMobile = useIsMobile();
  const [pageSize, setPageSize] = useSharedPageSize();
  const [activePage, setActivePage] = useState(1);

  useEffect(() => setActivePage(1), [pageSize]);
  const [searchKeyword, setSearchKeyword] = useState("");
  const [debouncedSearch, flushSearch] = useDebouncedValue(searchKeyword);
  const [compactMode, setCompactMode] = useState(false);
  const [selectedRowKeys, setSelectedRowKeys] = useState<Array<string | number>>(
    []
  );
  const [balanceTarget, setBalanceTarget] = useState<FinanceUserBalance | null>(
    null
  );
  const [withdrawTarget, setWithdrawTarget] =
    useState<FinanceUserBalance | null>(null);
  const [bulkOpen, setBulkOpen] = useState(false);

  const listFilter = useMemo(
    () => ({
      search: debouncedSearch.trim() || undefined,
    }),
    [debouncedSearch]
  );

  const loadBlock = useCallback(
    async (offset: number, limit: number) => {
      const result = await listMockFinanceUserBalances(
        listFilter,
        offset,
        limit
      );
      return { items: result.items, total: result.total };
    },
    [listFilter]
  );

  const { pagedItems, total, loading, refresh, updateLoadedItems } =
    useBlockPagedList<FinanceUserBalance>({
      activePage,
      filterKey: JSON.stringify(listFilter),
      loadBlock,
      onError: (error) =>
        Toast.error(getIamErrorMessage(t, error, "Operation failed.")),
      pageSize,
    });

  const safePage = Math.min(
    activePage,
    Math.max(1, Math.ceil(Math.max(total, 1) / pageSize))
  );

  useSelectionNotification({
    selectedCount: selectedRowKeys.length,
    onClear: () => setSelectedRowKeys([]),
    onCheck: () => setBulkOpen(true),
    checkLabelKey: "Adjust balance",
    selectionDescriptionKey: "Selected resources",
    t,
  });

  const columns = useMemo(
    () => [
      {
        title: t("User"),
        dataIndex: "email",
        width: 260,
        render: (_: string, record: FinanceUserBalance) => (
          <BalanceAccountCell
            email={record.email}
            groupName={record.groupName}
            nickname={record.nickname}
            role={record.role}
            t={t}
            userId={record.userId}
          />
        ),
      },
      {
        title: t("Consumer balance"),
        dataIndex: "consumerBalance",
        width: 150,
        render: (value: string) => (
          <span className="font-mono-data font-semibold">
            ¥{formatMoney(value)}
          </span>
        ),
      },
      {
        title: t("Supplier available"),
        dataIndex: "supplierAvailable",
        width: 150,
        render: (value: string) => (
          <span className="font-mono-data">¥{formatMoney(value)}</span>
        ),
      },
      {
        title: t("Supplier frozen"),
        dataIndex: "supplierFrozen",
        width: 140,
        render: (value: string) => (
          <span className="font-mono-data">¥{formatMoney(value)}</span>
        ),
      },
      {
        title: t("Updated at"),
        dataIndex: "updatedAt",
        width: 180,
        render: (value: string) => formatDateTime(value),
      },
      {
        title: t("Actions"),
        dataIndex: "operate",
        fixed: compactMode ? undefined : ("right" as const),
        width: 200,
        render: (_: unknown, record: FinanceUserBalance) => (
          <Space spacing={4} wrap={false}>
            <Button
              disabled={!canOperate}
              onClick={() => setBalanceTarget(record)}
              size="small"
              type="tertiary"
            >
              {t("Adjust balance")}
            </Button>
            <Button
              disabled={!canOperate || Number(record.supplierAvailable) <= 0}
              onClick={() => setWithdrawTarget(record)}
              size="small"
              type="tertiary"
            >
              {t("Withdraw")}
            </Button>
          </Space>
        ),
      },
    ] as any[],
    [canOperate, compactMode, t]
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
          disabled={!canOperate || !selectedRowKeys.length}
          onClick={() => setBulkOpen(true)}
          size="small"
          type="primary"
        >
          {t("Adjust balance")}
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
        <CompactModeToggle
          compactMode={compactMode}
          setCompactMode={setCompactMode}
          t={t}
        />
      </div>
      <div className="order-1 flex w-full flex-col items-center gap-2 md:order-2 md:w-auto md:flex-row">
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
          placeholder={t("Search user by email, nickname or ID")}
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
          empty={emptyNode(t("No users found"))}
          hidePagination
          loading={loading}
          pagination={false}
          rowKey="userId"
          rowSelection={
            canOperate
              ? {
                  selectedRowKeys,
                  onChange: (keys?: Array<string | number>) =>
                    setSelectedRowKeys(keys ?? []),
                }
              : undefined
          }
          scroll={{ x: "max(100%, 1180px)", y: DESKTOP_TABLE_SCROLL_Y }}
          size="middle"
        />
      </CardPro>

      <FinanceAdjustModal
        balance={balanceTarget?.consumerBalance ?? "0"}
        onClose={() => setBalanceTarget(null)}
        onDone={(wallet) => {
          updateLoadedItems((items) =>
            items.map((item) =>
              item.userId === wallet.userId ? wallet : item
            )
          );
        }}
        open={Boolean(balanceTarget)}
        userId={balanceTarget?.userId}
      />
      <FinanceAdjustModal
        balance="-"
        onBulkDone={async () => {
          setSelectedRowKeys([]);
          void refresh();
        }}
        onClose={() => setBulkOpen(false)}
        open={bulkOpen}
        userIds={selectedRowKeys.map(Number)}
      />
      <FinanceWithdrawModal
        onClose={() => setWithdrawTarget(null)}
        onDone={(wallet) => {
          updateLoadedItems((items) =>
            items.map((item) =>
              item.userId === wallet.userId ? wallet : item
            )
          );
        }}
        open={Boolean(withdrawTarget)}
        user={withdrawTarget}
      />
    </>
  );
}
