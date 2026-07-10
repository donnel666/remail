import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  Button,
  DatePicker,
  Dropdown,
  Empty,
  Input,
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
import { useNavigate } from "@tanstack/react-router";
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
import { useBlockPagedList } from "@/hooks/use-block-paged-list";
import { useDebouncedValue } from "@/hooks/use-debounced-value";
import { useIsMobile } from "@/hooks/use-is-mobile";
import { useSharedPageSize } from "@/hooks/use-shared-page-size";
import { copyText } from "@/lib/clipboard";
import { getIamErrorMessage } from "@/lib/iam-errors";
import { MailboxClientModal } from "@/pages/workbench/mailbox-client";
import { ProjectIcon } from "@/pages/workbench/project-icon";
import type { WorkbenchMessage } from "@/pages/workbench/types";
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
  orderStatusLabel,
  renderOrderStatusTag,
  renderServiceModeTag,
  serviceModeLabel,
} from "./orders/order-meta";
import { useSelectionNotification } from "./resources/use-selection-notification";
import {
  fetchMockOrderMail,
  getMockOrderMessages,
  getOrderDomain,
  listMockOrders,
  type MockOrder,
  type MockOrderFacets,
  type MockOrderListFilter,
  type MockOrderStatus,
  type MockServiceMode,
} from "./orders/orders-mock";

type StatusFilter = "all" | MockOrderStatus;
type ServiceModeFilter = "all" | MockServiceMode;

export default function Orders() {
  const { t } = useTranslation();
  const isMobile = useIsMobile();
  const navigate = useNavigate();

  const [activeDomain, setActiveDomain] = useState("all");
  const [searchKeyword, setSearchKeyword] = useState("");
  const [createdAtRange, setCreatedAtRange] = useState<DateRangeValue>([]);
  const [statusFilter, setStatusFilter] = useState<StatusFilter>("all");
  const [serviceModeFilter, setServiceModeFilter] =
    useState<ServiceModeFilter>("all");
  const [compactMode, setCompactMode] = useState(false);
  const [activePage, setActivePage] = useState(1);
  const [pageSize, setPageSize] = useSharedPageSize();
  const [orderFacets, setOrderFacets] = useState<MockOrderFacets | null>(null);
  const [detailOrder, setDetailOrder] = useState<MockOrder | null>(null);
  const [selectedKeys, setSelectedKeys] = useState<string[]>([]);
  const [mailboxOrder, setMailboxOrder] = useState<MockOrder | null>(null);
  const [mailboxMessages, setMailboxMessages] = useState<WorkbenchMessage[]>([]);
  const mailboxOrderNoRef = useRef<string | null>(null);
  const dateRangePresets = useMemo(() => createDateRangePresets(t), [t]);
  const [debouncedSearchKeyword, flushSearchKeyword] =
    useDebouncedValue(searchKeyword);

  const orderStatsFilter = useMemo<MockOrderListFilter>(() => {
    const filter: MockOrderListFilter = {};
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

  const orderListFilter = useMemo<MockOrderListFilter>(() => {
    if (activeDomain === "all") return orderStatsFilter;
    return { ...orderStatsFilter, domain: activeDomain };
  }, [activeDomain, orderStatsFilter]);

  const loadOrderBlock = useCallback(
    async (offset: number, limit: number, cursor?: { afterId?: number }) => {
      const response = await listMockOrders(
        orderListFilter,
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
    [orderListFilter]
  );

  const {
    loading,
    pagedItems,
    refresh: refreshList,
    total,
  } = useBlockPagedList<MockOrder>({
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
      const response = await listMockOrders(orderStatsFilter, 0, 1);
      setOrderFacets(response.facets);
    } catch {
      // Keep the previous tabs stable; the next refresh will retry stats.
    }
  }, [orderStatsFilter]);

  useEffect(() => {
    void refreshStats();
  }, [refreshStats]);

  const refresh = useCallback(async () => {
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
    setSelectedKeys([]);
  };

  const resetFilters = () => {
    setSearchKeyword("");
    flushSearchKeyword("");
    setCreatedAtRange([]);
    setStatusFilter("all");
    setServiceModeFilter("all");
    setActiveDomain("all");
    setActivePage(1);
    setSelectedKeys([]);
  };

  const applyStatusFilter = (value: StatusFilter) => {
    setStatusFilter(value);
    setActivePage(1);
    setSelectedKeys([]);
  };

  const applyServiceModeFilter = (value: ServiceModeFilter) => {
    setServiceModeFilter(value);
    setActivePage(1);
    setSelectedKeys([]);
  };

  const openOrderMailbox = useCallback(
    async (record: MockOrder) => {
      mailboxOrderNoRef.current = record.orderNo;
      setMailboxOrder(record);
      setMailboxMessages([]);
      try {
        const messages = await getMockOrderMessages(record.orderNo);
        if (mailboxOrderNoRef.current === record.orderNo) {
          setMailboxMessages(messages);
        }
      } catch (error) {
        Toast.error(getIamErrorMessage(t, error, "Mail load failed."));
      }
    },
    [t]
  );

  const closeOrderMailbox = useCallback(() => {
    mailboxOrderNoRef.current = null;
    setMailboxOrder(null);
    setMailboxMessages([]);
  }, []);

  const handleMailboxFetch = useCallback(async () => {
    const orderNo = mailboxOrderNoRef.current;
    if (!orderNo) return;
    const result = await fetchMockOrderMail(orderNo);
    if (mailboxOrderNoRef.current === orderNo) {
      setMailboxMessages(result.messages);
      setMailboxOrder(result.order);
    }
    if (result.delivered) {
      Toast.success(t("Code received"));
      void refresh();
    }
    return result.cooldownSeconds;
  }, [refresh, t]);

  const copyOrderPickupUrl = useCallback(
    async (record: MockOrder) => {
      if (!record.serviceToken) return;
      try {
        await copyText(buildPickupUrl(record.deliveryEmail, record.serviceToken));
        Toast.success(t("Copied"));
      } catch {
        Toast.error(t("Copy failed."));
      }
    },
    [t]
  );

  // Reserved entry: after-sales tickets are not wired to the backend yet.
  const submitTicket = useCallback(
    (orderNos: string[]) => {
      if (orderNos.length === 0) return;
      Toast.info(t("Ticket submission is coming soon."));
    },
    [t]
  );

  const clearSelection = useCallback(() => {
    setSelectedKeys([]);
  }, []);

  const submitTicketForSelection = useCallback(() => {
    submitTicket(selectedKeys);
  }, [selectedKeys, submitTicket]);

  useSelectionNotification({
    selectedCount: selectedKeys.length,
    checkLabelKey: "Ticket",
    onCheck: submitTicketForSelection,
    onClear: clearSelection,
    selectionDescriptionKey: "Selected orders",
    t,
  });

  const columns = useMemo(
    () =>
      [
        {
          key: "project",
          title: t("Project"),
          dataIndex: "projectName",
          width: 150,
          render: (name: string) => (
            <span className="flex min-w-0 items-center gap-2">
              <ProjectIcon name={name} size={18} />
              <OverflowTooltip className="truncate" content={name}>
                {name}
              </OverflowTooltip>
            </span>
          ),
        },
        {
          key: "domain",
          title: t("Domain"),
          dataIndex: "deliveryEmail",
          width: 130,
          render: (email: string) => (
            <Tag color="white" shape="circle">
              {getOrderDomain(email)}
            </Tag>
          ),
        },
        {
          key: "email",
          title: t("Delivery email"),
          dataIndex: "deliveryEmail",
          width: 240,
          render: (text: string) => (
            <CopyableTableText copiedText={t("Copied")} text={text} />
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
          render: (mode: MockServiceMode) => renderServiceModeTag(mode, t),
        },
        {
          key: "status",
          title: t("Status"),
          dataIndex: "status",
          width: 110,
          render: (status: MockOrderStatus) => renderOrderStatusTag(status, t),
        },
        {
          key: "code",
          title: t("Code"),
          dataIndex: "verificationCode",
          width: 130,
          render: (code: string | undefined, record: MockOrder) => {
            if (code) {
              return <CopyableTableText copiedText={t("Copied")} text={code} />;
            }
            if (record.status === "active") {
              return (
                <Tag color="grey" shape="circle">
                  {t("Waiting")}
                </Tag>
              );
            }
            return <span className="text-[var(--semi-color-text-3)]">-</span>;
          },
        },
        {
          key: "payAmount",
          title: t("Amount"),
          dataIndex: "payAmount",
          width: 100,
          render: (amount: number) => (
            <span className="font-mono-data">{formatLedgerAmount(amount)}</span>
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
          width: 240,
          fixed: "right",
          render: (_: unknown, record: MockOrder) => (
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
                  disabled={!record.serviceToken}
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
                  disabled={!record.serviceToken}
                  type="tertiary"
                  size="small"
                  onClick={() => void copyOrderPickupUrl(record)}
                >
                  {t("Pickup")}
                </Button>
              </Tooltip>
              <Button
                type="tertiary"
                size="small"
                onClick={() => submitTicket([record.orderNo])}
              >
                {t("Ticket")}
              </Button>
            </Space>
          ),
        },
      ] as any[],
    [copyOrderPickupUrl, openOrderMailbox, submitTicket, t]
  );

  const rowSelection = {
    selectedRowKeys: selectedKeys,
    onChange: (keys?: Array<string | number>) => {
      setSelectedKeys((keys ?? []).map(String));
    },
  };

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
          type="primary"
          size="small"
          className="flex-1 md:flex-initial"
          onClick={() => void navigate({ to: "/dashboard" })}
        >
          {t("Order")}
        </Button>
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
            setSelectedKeys([]);
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
          rowSelection={rowSelection}
          scroll={{ x: "max(100%, 1630px)", y: DESKTOP_TABLE_SCROLL_Y }}
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
        onFetch={handleMailboxFetch}
      />
    </div>
  );
}
