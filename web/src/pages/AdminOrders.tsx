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
import { OverflowTooltip } from "@/components/semi/overflow-tooltip";
import { StatisticFilterOption } from "@/components/semi/statistic-filter-option";
import { hasPermissionKey, useAuth } from "@/context/auth-provider";
import { useBlockPagedList } from "@/hooks/use-block-paged-list";
import { useDebouncedValue } from "@/hooks/use-debounced-value";
import { useIsMobile } from "@/hooks/use-is-mobile";
import { useSharedPageSize } from "@/hooks/use-shared-page-size";
import { IamApiError } from "@/lib/api-client";
import { copyText } from "@/lib/clipboard";
import { getIamErrorMessage } from "@/lib/iam-errors";
import {
  readPickupMail,
  readPickupMessage,
  type OrderMailResponse,
} from "@/lib/mailmatch-api";
import {
  adminRefundOrder,
  getOrder,
  listOrders,
  type OrderListFacets,
  type OrderListFilter,
  type OrderResponse,
  type OrderServiceMode,
  type OrderStatus,
} from "@/lib/orders-api";
import { MailboxClientModal } from "@/pages/workbench/mailbox-client";
import { ProjectIcon } from "@/pages/workbench/project-icon";
import type { FetchSource, WorkbenchMessage } from "@/pages/workbench/types";
import { buildPickupUrl } from "@/pages/workbench/utils";

import {
  DATE_RANGE_DROPDOWN_CLASS,
  createDateRangePresets,
  createdFromISOString,
  createdToISOString,
  normalizeDateRangeValue,
  type DateRangeValue,
} from "./resources/date-range-filter";
import { OrderDetailModal } from "./orders/order-detail-modal";
import {
  ORDER_STATUS_VALUES,
  formatLedgerAmount,
  formatOrderDateTime,
  getOrderDomain,
  orderStatusLabel,
  renderOrderStatusTag,
  renderServiceModeTag,
  serviceModeLabel,
} from "./orders/order-meta";
import { OrderOwnerCell } from "./orders/order-requester-cell";

type StatusFilter = "all" | OrderStatus;
type ServiceModeFilter = "all" | OrderServiceMode;

const DEFAULT_FETCH_COOLDOWN_SECONDS = 5;

function isFutureTime(value?: string | null) {
  if (!value) return false;
  const time = Date.parse(value);
  return Number.isFinite(time) && time > Date.now();
}

function orderCanUseService(order: OrderResponse) {
  if (order.status === "active") return true;
  if (order.status !== "completed") return false;
  if (order.serviceMode === "purchase") return true;
  return isFutureTime(order.receiveUntil);
}

function toMailboxMessages(items: OrderMailResponse["items"]): WorkbenchMessage[] {
  return items
    .map<WorkbenchMessage>((item) => ({
      body: "",
      id: String(item.id),
      preview: item.bodyPreview,
      receivedAt: item.receivedAt,
      recipient: item.recipient,
      sender: item.sender,
      status: item.verificationCode ? "matched" : "received",
      subject: item.subject || "(No subject)",
      verificationCode: item.verificationCode,
    }))
    .sort((left, right) => {
      const leftTime = Date.parse(left.receivedAt);
      const rightTime = Date.parse(right.receivedAt);
      return (
        (Number.isFinite(rightTime) ? rightTime : 0) -
        (Number.isFinite(leftTime) ? leftTime : 0)
      );
    });
}

function cooldownSecondsFrom(nextFetchAllowedAt?: string | null) {
  if (!nextFetchAllowedAt) return DEFAULT_FETCH_COOLDOWN_SECONDS;
  const next = Date.parse(nextFetchAllowedAt);
  if (!Number.isFinite(next)) return DEFAULT_FETCH_COOLDOWN_SECONDS;
  return Math.max(1, Math.ceil((next - Date.now()) / 1000));
}

// Admin refunds target the standard active/completed lifecycle; other states
// (pending/paid/failed/closed) go through retry/terminate flows, and the
// backend rejects them with 422 as a backstop.
function orderCanRefund(order: OrderResponse) {
  return order.status === "active" || order.status === "completed";
}

export default function AdminOrders() {
  const { t } = useTranslation();
  const isMobile = useIsMobile();
  const { currentUser } = useAuth();
  const canRefund = hasPermissionKey(currentUser, "trade:order:operate");

  const [activeDomain, setActiveDomain] = useState("all");
  const [searchKeyword, setSearchKeyword] = useState("");
  const [createdAtRange, setCreatedAtRange] = useState<DateRangeValue>([]);
  const [statusFilter, setStatusFilter] = useState<StatusFilter>("all");
  const [serviceModeFilter, setServiceModeFilter] =
    useState<ServiceModeFilter>("all");
  const [compactMode, setCompactMode] = useState(false);
  const [activePage, setActivePage] = useState(1);
  const [pageSize, setPageSize] = useSharedPageSize();

  useEffect(() => setActivePage(1), [pageSize]);
  const [orderFacets, setOrderFacets] = useState<OrderListFacets | null>(null);
  const [detailOrder, setDetailOrder] = useState<OrderResponse | null>(null);
  const [refundingOrderNo, setRefundingOrderNo] = useState<string | null>(null);
  const [viewLoadingOrderNo, setViewLoadingOrderNo] = useState<string | null>(null);
  const [pickupLoadingOrderNo, setPickupLoadingOrderNo] = useState<string | null>(null);
  const [mailboxOrder, setMailboxOrder] = useState<OrderResponse | null>(null);
  const [mailboxMessages, setMailboxMessages] = useState<WorkbenchMessage[]>([]);
  const mailboxOrderNoRef = useRef<string | null>(null);
  const mailboxFetchInFlightRef = useRef(
    new Map<string, Promise<number | void>>()
  );
  const mailboxOpenSeqRef = useRef(0);
  const orderDetailCacheRef = useRef(new Map<string, OrderResponse>());
  const pickupInFlightRef = useRef(false);
  const dateRangePresets = useMemo(() => createDateRangePresets(t), [t]);
  const [debouncedSearchKeyword, flushSearchKeyword] =
    useDebouncedValue(searchKeyword);

  const orderStatsFilter = useMemo<OrderListFilter>(() => {
    const filter: OrderListFilter = { scope: "all" };
    const search = debouncedSearchKeyword.trim();
    const createdFrom = createdFromISOString(createdAtRange);
    const createdTo = createdToISOString(createdAtRange);
    if (search) filter.search = search;
    if (statusFilter !== "all") filter.status = statusFilter;
    if (serviceModeFilter !== "all") filter.serviceMode = serviceModeFilter;
    if (createdFrom) filter.createdFrom = createdFrom;
    if (createdTo) filter.createdTo = createdTo;
    return filter;
  }, [createdAtRange, debouncedSearchKeyword, serviceModeFilter, statusFilter]);

  const orderListFilter = useMemo<OrderListFilter>(() => {
    if (activeDomain === "all") return orderStatsFilter;
    return { ...orderStatsFilter, domain: activeDomain };
  }, [activeDomain, orderStatsFilter]);

  const loadOrderBlock = useCallback(
    async (offset: number, limit: number, cursor?: { afterId?: number }) => {
      const response = await listOrders({
        ...orderListFilter,
        offset,
        limit,
        afterId: cursor?.afterId,
      });
      return {
        items: response.items,
        nextAfterId: response.nextAfterId,
        total: response.total,
      };
    },
    [orderListFilter]
  );

  const {
    loading,
    pagedItems,
    refresh: refreshList,
    total,
  } = useBlockPagedList<OrderResponse>({
    activePage,
    filterKey: JSON.stringify(orderListFilter),
    loadBlock: loadOrderBlock,
    onError: (error) => {
      Toast.error(getIamErrorMessage(t, error, "Orders load failed."));
    },
    pageSize,
  });

  const refreshStats = useCallback(async () => {
    try {
      const response = await listOrders({ ...orderStatsFilter, limit: 1 });
      setOrderFacets(response.facets ?? null);
    } catch {
      // Keep the previous tabs stable; the next refresh will retry stats.
    }
  }, [orderStatsFilter]);

  useEffect(() => {
    void refreshStats();
  }, [refreshStats]);

  const refresh = useCallback(async () => {
    orderDetailCacheRef.current.clear();
    await Promise.all([refreshStats(), refreshList()]);
  }, [refreshList, refreshStats]);

  const domainCounts = useMemo(
    () =>
      orderFacets?.domains?.map(
        (item) => [item.key, item.count] as [string, number]
      ) ?? [],
    [orderFacets]
  );
  const domainSet = useMemo(
    () => new Set(domainCounts.map(([domain]) => domain)),
    [domainCounts]
  );

  const orderStats = useMemo(() => {
    if (orderFacets) {
      return { serviceMode: orderFacets.serviceMode, status: orderFacets.status };
    }
    return {
      serviceMode: { all: total, code: 0, purchase: 0 },
      status: {
        all: total,
        pending_payment: 0,
        paid: 0,
        active: 0,
        completed: 0,
        refunded: 0,
        failed: 0,
        closed: 0,
      },
    };
  }, [orderFacets, total]);

  const activeStatisticFilterCount =
    Number(statusFilter !== "all") + Number(serviceModeFilter !== "all");

  useEffect(() => {
    if (orderFacets && activeDomain !== "all" && !domainSet.has(activeDomain)) {
      setActiveDomain("all");
    }
  }, [activeDomain, domainSet, orderFacets]);

  const totalPages = Math.max(1, Math.ceil(total / pageSize));
  const safePage = Math.min(activePage, totalPages);

  useEffect(() => {
    if (safePage !== activePage) setActivePage(safePage);
  }, [activePage, safePage]);

  const selectDomain = (domain: string) => {
    setActiveDomain(domain);
    setActivePage(1);
  };

  const resetFilters = () => {
    setSearchKeyword("");
    flushSearchKeyword("");
    setCreatedAtRange([]);
    setStatusFilter("all");
    setServiceModeFilter("all");
    setActiveDomain("all");
    setActivePage(1);
  };

  const applyStatusFilter = (value: StatusFilter) => {
    setStatusFilter(value);
    setActivePage(1);
  };

  const applyServiceModeFilter = (value: ServiceModeFilter) => {
    setServiceModeFilter(value);
    setActivePage(1);
  };

  const resolveOrderDetail = useCallback(
    async (orderNo: string, options?: { force?: boolean }) => {
      if (!options?.force) {
        const cached = orderDetailCacheRef.current.get(orderNo);
        if (cached) return cached;
      }
      const detail = await getOrder(orderNo);
      orderDetailCacheRef.current.set(orderNo, detail);
      return detail;
    },
    []
  );

  const runMailboxFetch = useCallback(
    async (source: FetchSource) => {
      const orderNo = mailboxOrderNoRef.current;
      if (!orderNo) return;
      const existing = mailboxFetchInFlightRef.current.get(orderNo);
      if (existing) return existing;

      const request = (async (): Promise<number | void> => {
        try {
          const detail = await resolveOrderDetail(orderNo);
          if (!detail.serviceToken) {
            if (source === "manual") {
              Toast.error(t("Service credential is unavailable."));
            }
            return;
          }
          const result = await readPickupMail(detail.deliveryEmail, detail.serviceToken);
          if (mailboxOrderNoRef.current !== orderNo) return;
          const messages = toMailboxMessages(result.items);
          setMailboxMessages(messages);
          const latestCode = messages.find(
            (message) => message.verificationCode
          )?.verificationCode;
          if (latestCode && latestCode !== detail.verificationCode) {
            const refreshed = await resolveOrderDetail(orderNo, { force: true });
            if (mailboxOrderNoRef.current === orderNo) {
              setMailboxOrder(refreshed);
            }
            void refresh();
          }
          return cooldownSecondsFrom(result.fetch?.nextFetchAllowedAt);
        } catch (error) {
          if (error instanceof IamApiError && error.status === 429) {
            return error.retryAfterSeconds;
          }
          if (source === "manual") {
            Toast.error(getIamErrorMessage(t, error, "Mail load failed."));
          }
        } finally {
          mailboxFetchInFlightRef.current.delete(orderNo);
        }
      })();
      mailboxFetchInFlightRef.current.set(orderNo, request);
      return request;
    },
    [refresh, resolveOrderDetail, t]
  );

  const openOrderMailbox = useCallback(
    async (record: OrderResponse) => {
      const seq = mailboxOpenSeqRef.current + 1;
      mailboxOpenSeqRef.current = seq;
      setViewLoadingOrderNo(record.orderNo);
      try {
        const detail = await resolveOrderDetail(record.orderNo);
        if (mailboxOpenSeqRef.current !== seq) return;
        if (!detail.serviceToken) {
          Toast.error(t("Service credential is unavailable."));
          return;
        }
        mailboxOrderNoRef.current = record.orderNo;
        setMailboxOrder(detail);
        setMailboxMessages([]);
        void runMailboxFetch("auto");
      } catch (error) {
        if (mailboxOpenSeqRef.current === seq) {
          Toast.error(getIamErrorMessage(t, error, "Mail load failed."));
        }
      } finally {
        if (mailboxOpenSeqRef.current === seq) {
          setViewLoadingOrderNo(null);
        }
      }
    },
    [resolveOrderDetail, runMailboxFetch, t]
  );

  const closeOrderMailbox = useCallback(() => {
    mailboxOpenSeqRef.current += 1;
    mailboxOrderNoRef.current = null;
    setViewLoadingOrderNo(null);
    setMailboxOrder(null);
    setMailboxMessages([]);
  }, []);

  const loadMailboxMessageBody = useCallback(
    async (messageId: string) => {
      const orderNo = mailboxOrderNoRef.current;
      if (!orderNo) return "";
      const detail = await resolveOrderDetail(orderNo);
      if (!detail.serviceToken) return "";
      const response = await readPickupMessage(
        detail.deliveryEmail,
        detail.serviceToken,
        Number(messageId)
      );
      return response.body;
    },
    [resolveOrderDetail]
  );

  const copyOrderPickupUrl = useCallback(
    async (record: OrderResponse) => {
      if (pickupInFlightRef.current) return;
      pickupInFlightRef.current = true;
      setPickupLoadingOrderNo(record.orderNo);
      try {
        const detail = await resolveOrderDetail(record.orderNo);
        if (!detail.serviceToken) {
          Toast.error(t("Service credential is unavailable."));
          return;
        }
        await copyText(buildPickupUrl(detail.deliveryEmail, detail.serviceToken));
        Toast.success(t("Copied"));
      } catch (error) {
        Toast.error(getIamErrorMessage(t, error, "Copy failed."));
      } finally {
        pickupInFlightRef.current = false;
        setPickupLoadingOrderNo(null);
      }
    },
    [resolveOrderDetail, t]
  );

  const runRefund = useCallback(
    async (order: OrderResponse) => {
      setRefundingOrderNo(order.orderNo);
      try {
        await adminRefundOrder(order.orderNo);
        Toast.success(t("Order refunded."));
        await refresh();
      } catch (error) {
        Toast.error(getIamErrorMessage(t, error, "Order refund failed."));
      } finally {
        setRefundingOrderNo(null);
      }
    },
    [refresh, t]
  );

  const confirmRefund = useCallback(
    (order: OrderResponse) => {
      Modal.confirm({
        title: t("Refund order"),
        content: t("Refund order confirm", {
          amount: formatLedgerAmount(order.payAmount),
        }),
        okText: t("Refund"),
        cancelText: t("Cancel"),
        onOk: () => runRefund(order),
      });
    },
    [runRefund, t]
  );

  const columns = useMemo(
    () =>
      [
        {
          key: "owner",
          title: t("Owner"),
          dataIndex: "owner",
          width: 220,
          render: (_: unknown, record: OrderResponse) => (
            <OrderOwnerCell owner={record.owner} userId={record.userId} t={t} />
          ),
        },
        {
          key: "project",
          title: t("Project"),
          dataIndex: "projectName",
          width: 150,
          render: (name: string | undefined, record: OrderResponse) => (
            <span className="flex min-w-0 items-center gap-2">
              <ProjectIcon
                logoUrl={record.projectLogoUrl}
                name={name || "-"}
                size={18}
              />
              <OverflowTooltip className="truncate" content={name || "-"}>
                {name || "-"}
              </OverflowTooltip>
            </span>
          ),
        },
        {
          key: "domain",
          title: t("Domain"),
          dataIndex: "deliveryEmail",
          width: 130,
          render: (email: string) =>
            email ? (
              <Tag color="white" shape="circle">
                {getOrderDomain(email)}
              </Tag>
            ) : (
              <span className="text-[var(--semi-color-text-3)]">-</span>
            ),
        },
        {
          key: "email",
          title: t("Delivery email"),
          dataIndex: "deliveryEmail",
          width: 240,
          render: (text: string) =>
            text ? (
              <CopyableTableText copiedText={t("Copied")} text={text} />
            ) : (
              <span className="text-[var(--semi-color-text-3)]">-</span>
            ),
        },
        {
          key: "orderNo",
          title: t("Order No"),
          dataIndex: "orderNo",
          width: 220,
          render: (text: string) => (
            <CopyableTableText copiedText={t("Copied")} text={text} />
          ),
        },
        {
          key: "serviceMode",
          title: t("Service mode"),
          dataIndex: "serviceMode",
          width: 110,
          render: (mode: OrderServiceMode) => renderServiceModeTag(mode, t),
        },
        {
          key: "status",
          title: t("Status"),
          dataIndex: "status",
          width: 110,
          render: (status: OrderStatus) => renderOrderStatusTag(status, t),
        },
        {
          key: "payAmount",
          title: t("Amount"),
          dataIndex: "payAmount",
          width: 100,
          render: (amount: string) => (
            <span className="font-mono-data">{formatLedgerAmount(amount)}</span>
          ),
        },
        {
          key: "refundAmount",
          title: t("Refund amount"),
          dataIndex: "refundAmount",
          width: 110,
          render: (amount: string) =>
            Number(amount) > 0 ? (
              <span className="font-mono-data text-[var(--semi-color-danger)]">
                {formatLedgerAmount(amount)}
              </span>
            ) : (
              <span className="text-[var(--semi-color-text-3)]">-</span>
            ),
        },
        {
          key: "createdAt",
          title: t("Created at"),
          dataIndex: "createdAt",
          width: 150,
          render: (value: string) => (
            <span className="text-[13px] text-[var(--semi-color-text-1)]">
              {formatOrderDateTime(value)}
            </span>
          ),
        },
        {
          key: "operate",
          title: t("Action"),
          dataIndex: "operate",
          width: 300,
          fixed: "right",
          render: (_: unknown, record: OrderResponse) => (
            <Space spacing={4} wrap={false}>
              <Button
                type="tertiary"
                size="small"
                onClick={() => setDetailOrder(record)}
              >
                {t("Details")}
              </Button>
              <Tooltip
                content={t("Open mailbox")}
                mouseEnterDelay={0}
                mouseLeaveDelay={0.05}
                position="top"
              >
                <Button
                  disabled={!orderCanUseService(record)}
                  loading={viewLoadingOrderNo === record.orderNo}
                  type="tertiary"
                  size="small"
                  onClick={() => void openOrderMailbox(record)}
                >
                  {t("View")}
                </Button>
              </Tooltip>
              <Tooltip
                content={t("Copy pickup URL")}
                mouseEnterDelay={0}
                mouseLeaveDelay={0.05}
                position="top"
              >
                <Button
                  disabled={
                    pickupLoadingOrderNo !== null || !orderCanUseService(record)
                  }
                  loading={pickupLoadingOrderNo === record.orderNo}
                  type="tertiary"
                  size="small"
                  onClick={() => void copyOrderPickupUrl(record)}
                >
                  {t("Pickup")}
                </Button>
              </Tooltip>
              {canRefund ? (
                <Button
                  type="danger"
                  size="small"
                  disabled={!orderCanRefund(record)}
                  loading={refundingOrderNo === record.orderNo}
                  onClick={() => confirmRefund(record)}
                >
                  {t("Refund")}
                </Button>
              ) : null}
            </Space>
          ),
        },
      ] as any[],
    [
      canRefund,
      confirmRefund,
      copyOrderPickupUrl,
      openOrderMailbox,
      pickupLoadingOrderNo,
      refundingOrderNo,
      t,
      viewLoadingOrderNo,
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

  const tabsArea = (
    <Tabs
      activeKey={activeDomain}
      type="card"
      collapsible
      onChange={(key) => selectDomain(String(key))}
      className="mb-2"
    >
      <Tabs.TabPane
        itemKey="all"
        tab={
          <span className="flex items-center gap-2">
            {t("All")}
            <Tag color={activeDomain === "all" ? "red" : "grey"} shape="circle">
              {orderStats.status.all}
            </Tag>
          </span>
        }
      />
      {domainCounts.map(([domain, count]) => (
        <Tabs.TabPane
          key={domain}
          itemKey={domain}
          tab={
            <span className="flex items-center gap-2">
              <Layers size={14} />
              {domain}
              <Tag
                color={activeDomain === domain ? "red" : "grey"}
                shape="circle"
              >
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
          type="tertiary"
          size="small"
          className="remail-toolbar-fixed-button flex-1 md:flex-none"
          loading={loading}
          onClick={refresh}
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
          trigger="click"
          render={
            <div className="w-[280px] p-2">
              <div className="px-2 pb-1 text-xs font-medium text-[var(--semi-color-text-2)]">
                {t("Service mode")}
              </div>
              <div className="mb-2 space-y-1">
                <StatisticFilterOption
                  active={serviceModeFilter === "all"}
                  count={orderStats.serviceMode.all}
                  label={t("All")}
                  onSelect={applyServiceModeFilter}
                  value="all"
                />
                <StatisticFilterOption
                  active={serviceModeFilter === "code"}
                  count={orderStats.serviceMode.code}
                  label={serviceModeLabel("code", t)}
                  onSelect={applyServiceModeFilter}
                  value="code"
                />
                <StatisticFilterOption
                  active={serviceModeFilter === "purchase"}
                  count={orderStats.serviceMode.purchase}
                  label={serviceModeLabel("purchase", t)}
                  onSelect={applyServiceModeFilter}
                  value="purchase"
                />
              </div>

              <div className="px-2 pb-1 text-xs font-medium text-[var(--semi-color-text-2)]">
                {t("Status")}
              </div>
              <div className="space-y-1">
                <StatisticFilterOption
                  active={statusFilter === "all"}
                  count={orderStats.status.all}
                  label={t("All")}
                  onSelect={applyStatusFilter}
                  value="all"
                />
                {ORDER_STATUS_VALUES.map((status) => (
                  <StatisticFilterOption
                    active={statusFilter === status}
                    count={orderStats.status[status]}
                    key={status}
                    label={orderStatusLabel(status, t)}
                    onSelect={applyStatusFilter}
                    value={status}
                  />
                ))}
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
            {activeStatisticFilterCount > 0
              ? `${t("Filters")} (${activeStatisticFilterCount})`
              : t("Filters")}
          </Button>
        </Dropdown>
        <Input
          prefix={<IconSearch />}
          placeholder={t("Search order or email")}
          showClear
          size="small"
          value={searchKeyword}
          style={{ width: isMobile ? "100%" : 224 }}
          onChange={(value) => {
            setSearchKeyword(String(value));
            setActivePage(1);
          }}
          className="resources-search-input w-full md:w-56"
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
    onPageChange: (page) => setActivePage(page),
    onPageSizeChange: (size) => {
      setPageSize(size);
      setActivePage(1);
    },
    pageSize,
    total,
    t,
  });

  return (
    <div className="console-content-width py-5">
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
              darkModeImage={
                <IllustrationNoResultDark style={{ height: 150, width: 150 }} />
              }
              description={t("No orders yet")}
              image={<IllustrationNoResult style={{ height: 150, width: 150 }} />}
              style={{ padding: 30 }}
            />
          }
          hidePagination
          loading={loading}
          pagination={false}
          className="overflow-hidden rounded-xl"
          rowKey="orderNo"
          scroll={{ x: "max(100%, 1840px)", y: DESKTOP_TABLE_SCROLL_Y }}
          size="middle"
        />
      </CardPro>

      <OrderDetailModal
        onClose={() => setDetailOrder(null)}
        order={detailOrder}
      />
      <MailboxClientModal
        autoFetchEnabled={
          mailboxOrder?.status === "active" && !mailboxOrder.verificationCode
        }
        email={mailboxOrder?.deliveryEmail}
        fetchEnabled={mailboxOrder?.productType !== "domain"}
        fetchKey={mailboxOrder?.orderNo}
        messages={mailboxMessages}
        onClose={closeOrderMailbox}
        onFetch={runMailboxFetch}
        onLoadMessage={loadMailboxMessageBody}
      />
    </div>
  );
}
