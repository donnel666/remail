import { useCallback, useEffect, useMemo, useState, type ReactNode } from "react";
import {
  Button,
  DatePicker,
  Dropdown,
  Input,
  Modal,
  Space,
  Tag,
  Toast,
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
import {
  DATE_RANGE_DROPDOWN_CLASS,
  createDateRangePresets,
  createdFromISOString,
  createdToISOString,
  normalizeDateRangeValue,
  type DateRangeValue,
} from "../resources/date-range-filter";
import {
  listFinanceTransactions,
  reverseFinanceTransaction,
  type FinanceTransaction,
  type FinanceTransactionDirection,
  type FinanceTransactionType,
} from "./admin-finance-api";
import {
  formatDateTime,
  formatMoney,
  moneyClassName,
  renderDirectionTag,
  renderTransactionTypeTag,
} from "./finance-meta";
import { emptyNode } from "./finance-shared";
import { TransactionAccountCell } from "./transaction-meta";
import { TransactionDetailSheet } from "./transaction-detail-sheet";

const TRANSACTION_TYPES: FinanceTransactionType[] = [
  "recharge",
  "debit",
  "refund",
  "credit",
  "card_redeem",
  "manual_adjustment",
  "transfer",
  "freeze",
  "withdrawal",
];

function renderStateTag(record: FinanceTransaction, t: (key: string) => string) {
  if (record.reversalOfNo) {
    return (
      <Tag color="violet" shape="circle" size="small">
        {t("Reversal entry")}
      </Tag>
    );
  }
  if (record.reversed) {
    return (
      <Tag color="grey" shape="circle" size="small">
        {t("Reversed")}
      </Tag>
    );
  }
  return (
    <Tag color="green" shape="circle" size="small">
      {t("Normal")}
    </Tag>
  );
}

export function TransactionsPanel({ tabsArea }: { tabsArea: ReactNode }) {
  const { t } = useTranslation();
  const { currentUser } = useAuth();
  const canOperate = hasPermission(currentUser, "billing:wallet", "operate");
  const isMobile = useIsMobile();
  const [pageSize, setPageSize] = useSharedPageSize();
  const [activePage, setActivePage] = useState(1);

  useEffect(() => setActivePage(1), [pageSize]);
  const [searchKeyword, setSearchKeyword] = useState("");
  const [debouncedSearch, flushSearch] = useDebouncedValue(searchKeyword);
  const [typeFilter, setTypeFilter] = useState<"all" | FinanceTransactionType>(
    "all"
  );
  const [directionFilter, setDirectionFilter] = useState<
    "all" | FinanceTransactionDirection
  >("all");
  const [createdAtRange, setCreatedAtRange] = useState<DateRangeValue>([]);
  const [compactMode, setCompactMode] = useState(false);
  const [detailTarget, setDetailTarget] = useState<FinanceTransaction | null>(
    null
  );
  const [reverseBusyId, setReverseBusyId] = useState<number | null>(null);
  const dateRangePresets = useMemo(() => createDateRangePresets(t), [t]);

  const listFilter = useMemo(
    () => ({
      search: debouncedSearch.trim() || undefined,
      transactionType: typeFilter === "all" ? undefined : typeFilter,
      direction: directionFilter === "all" ? undefined : directionFilter,
      createdFrom: createdFromISOString(createdAtRange),
      createdTo: createdToISOString(createdAtRange),
    }),
    [createdAtRange, debouncedSearch, directionFilter, typeFilter]
  );

  const loadBlock = useCallback(
    async (offset: number, limit: number) => {
      const result = await listFinanceTransactions(
        listFilter,
        offset,
        limit
      );
      return { items: result.items, total: result.total };
    },
    [listFilter]
  );

  const { pagedItems, total, loading, refresh } =
    useBlockPagedList<FinanceTransaction>({
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

  const confirmReverse = (record: FinanceTransaction) => {
    if (!canOperate) return;
    Modal.confirm({
      cancelText: t("Cancel"),
      content: t("Confirm reverse transaction", { no: record.transactionNo }),
      okButtonProps: { type: "danger" },
      okText: t("Reverse"),
      onOk: async () => {
        setReverseBusyId(record.id);
        try {
          await reverseFinanceTransaction(record.id);
          Toast.success(t("Transaction reversed."));
          setDetailTarget(null);
          void refresh();
        } catch (error) {
          Toast.error(getIamErrorMessage(t, error, "Operation failed."));
        } finally {
          setReverseBusyId(null);
        }
      },
      title: t("Confirm reverse"),
    });
  };

  const columns = useMemo(
    () => [
      {
        title: t("Transaction No"),
        dataIndex: "transactionNo",
        width: 200,
        render: (value: string, record: FinanceTransaction) => (
          <div className="min-w-0">
            <CopyableTableText copiedText={t("Copied")} text={value} />
            {record.reversed || record.reversalOfNo ? (
              <div className="mt-1">{renderStateTag(record, t)}</div>
            ) : null}
          </div>
        ),
      },
      {
        title: t("User"),
        dataIndex: "userEmail",
        width: 250,
        render: (_: string, record: FinanceTransaction) => (
          <TransactionAccountCell
            email={record.userEmail}
            groupName={record.userGroupName}
            nickname={record.userNickname}
            role={record.userRole}
            t={t}
            userId={record.userId}
          />
        ),
      },
      {
        title: t("Type"),
        dataIndex: "transactionType",
        width: 150,
        render: (value: FinanceTransactionType, record: FinanceTransaction) =>
          renderTransactionTypeTag(value, record.direction, t),
      },
      {
        title: t("Direction"),
        dataIndex: "direction",
        width: 110,
        render: (value: FinanceTransactionDirection) =>
          renderDirectionTag(value, t),
      },
      {
        title: t("Amount"),
        dataIndex: "amount",
        width: 120,
        render: (value: string, record: FinanceTransaction) => (
          <span className={moneyClassName(record.direction)}>
            {record.direction === "in" ? "+" : "-"}¥{formatMoney(value)}
          </span>
        ),
      },
      {
        title: t("Balance after"),
        dataIndex: "balanceAfter",
        width: 130,
        render: (value: string) => (
          <span className="font-mono-data">¥{formatMoney(value)}</span>
        ),
      },
      {
        title: t("Biz ID"),
        dataIndex: "bizId",
        width: 160,
        render: (value: string) => (
          <span className="text-[var(--semi-color-text-2)]">{value || "-"}</span>
        ),
      },
      {
        title: t("Created at"),
        dataIndex: "createdAt",
        width: 180,
        render: (value: string) => formatDateTime(value),
      },
      {
        title: t("Actions"),
        dataIndex: "operate",
        fixed: compactMode ? undefined : ("right" as const),
        width: 160,
        render: (_: unknown, record: FinanceTransaction) => {
          const canReverse =
            canOperate && !record.reversed && !record.reversalOfNo;
          return (
            <Space spacing={4} wrap={false}>
              <Button
                onClick={() => setDetailTarget(record)}
                size="small"
                type="tertiary"
              >
                {t("Details")}
              </Button>
              <Button
                disabled={!canReverse}
                loading={reverseBusyId === record.id}
                onClick={() => confirmReverse(record)}
                size="small"
                type="danger"
              >
                {t("Reverse")}
              </Button>
            </Space>
          );
        },
      },
    ] as any[],
    [canOperate, compactMode, reverseBusyId, t]
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
        <Dropdown
          position="bottomRight"
          render={
            <div className="w-[280px] p-2">
              <div className="px-2 pb-1 text-xs font-medium text-[var(--semi-color-text-2)]">
                {t("Type")}
              </div>
              <div className="mb-2 max-h-56 space-y-1 overflow-auto">
                <StatisticFilterOption
                  active={typeFilter === "all"}
                  count={total}
                  label={t("All")}
                  onSelect={() => {
                    setTypeFilter("all");
                    setActivePage(1);
                  }}
                  value="all"
                />
                {TRANSACTION_TYPES.map((type) => (
                  <StatisticFilterOption
                    active={typeFilter === type}
                    count={0}
                    key={type}
                    label={t(type)}
                    onSelect={() => {
                      setTypeFilter(type);
                      setActivePage(1);
                    }}
                    value={type}
                  />
                ))}
              </div>
              <div className="px-2 pb-1 text-xs font-medium text-[var(--semi-color-text-2)]">
                {t("Direction")}
              </div>
              <div className="space-y-1">
                {(
                  [
                    ["all", t("All")],
                    ["in", t("Inflow")],
                    ["out", t("Outflow")],
                  ] as const
                ).map(([value, label]) => (
                  <StatisticFilterOption
                    active={directionFilter === value}
                    count={0}
                    key={value}
                    label={label}
                    onSelect={() => {
                      setDirectionFilter(value);
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
          placeholder={t("Search transaction, user or biz id")}
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
          style={{ width: isMobile ? "100%" : 360 }}
          type="dateTimeRange"
          value={createdAtRange}
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
          onPageChange: setActivePage,
          onPageSizeChange: (size) => {
            setPageSize(size);
            setActivePage(1);
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
          empty={emptyNode(t("No transactions found"))}
          hidePagination
          loading={loading}
          pagination={false}
          rowKey="id"
          scroll={{ x: "max(100%, 1560px)", y: DESKTOP_TABLE_SCROLL_Y }}
          size="middle"
        />
      </CardPro>

      <TransactionDetailSheet
        onClose={() => setDetailTarget(null)}
        onReverse={canOperate ? (record) => confirmReverse(record) : undefined}
        reverseLoading={
          detailTarget ? reverseBusyId === detailTarget.id : false
        }
        transaction={detailTarget}
      />
    </>
  );
}
