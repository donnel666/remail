import { useCallback, useEffect, useMemo, useState } from "react";
import {
  Button,
  DatePicker,
  Dropdown,
  Empty,
  Input,
  Tabs,
  Tag,
  Toast,
} from "@douyinfe/semi-ui";
import { IconSearch } from "@douyinfe/semi-icons";
import {
  IllustrationNoResult,
  IllustrationNoResultDark,
} from "@douyinfe/semi-illustrations";
import { Plus, SlidersHorizontal } from "lucide-react";
import { useTranslation } from "react-i18next";

import { CardPro } from "@/components/semi/card-pro";
import { createCardProPagination } from "@/components/semi/card-pro-pagination";
import {
  CardTable,
  DESKTOP_TABLE_SCROLL_Y,
} from "@/components/semi/card-table";
import { CompactModeToggle } from "@/components/semi/compact-mode-toggle";
import { StatisticFilterOption } from "@/components/semi/statistic-filter-option";
import { useBlockPagedList } from "@/hooks/use-block-paged-list";
import { useDebouncedValue } from "@/hooks/use-debounced-value";
import { useIsMobile } from "@/hooks/use-is-mobile";
import { useSharedPageSize } from "@/hooks/use-shared-page-size";
import { getIamErrorMessage } from "@/lib/iam-errors";
import {
  TICKET_CREATE_ORDER_STORAGE_KEY,
  type TicketOrderRef,
} from "./orders/ticket-order-handoff";

import {
  DATE_RANGE_DROPDOWN_CLASS,
  createDateRangePresets,
  createdFromISOString,
  createdToISOString,
  normalizeDateRangeValue,
  type DateRangeValue,
} from "./resources/date-range-filter";
import { CreateTicketModal } from "./tickets/create-ticket-modal";
import { TicketDetailSheet } from "./tickets/ticket-detail-sheet";
import { TicketInboxRow } from "./tickets/ticket-inbox-row";
import { ticketTypeLabel } from "./tickets/ticket-meta";
import {
  listMyTickets,
  type Ticket,
  type TicketFacets,
  type TicketListFilter,
  type TicketStatus,
  type TicketType,
} from "./tickets/tickets-api";

type TypeFilter = "all" | TicketType;
type StatusFilter = "all" | TicketStatus;

const STATUS_OPTIONS: StatusFilter[] = ["all", "open", "processing", "closed"];

const STATUS_LABEL_KEY: Record<StatusFilter, string> = {
  all: "All",
  open: "Ticket status open",
  processing: "Ticket status processing",
  closed: "Ticket status closed",
};

function readPendingCreateOrder(): TicketOrderRef | null {
  if (typeof window === "undefined") return null;
  const raw = window.sessionStorage.getItem(TICKET_CREATE_ORDER_STORAGE_KEY);
  if (!raw) return null;
  window.sessionStorage.removeItem(TICKET_CREATE_ORDER_STORAGE_KEY);
  try {
    return JSON.parse(raw) as TicketOrderRef;
  } catch {
    return null;
  }
}

export default function Tickets() {
  const { t } = useTranslation();
  const isMobile = useIsMobile();

  const [typeFilter, setTypeFilter] = useState<TypeFilter>("all");
  const [statusFilter, setStatusFilter] = useState<StatusFilter>("all");
  const [searchKeyword, setSearchKeyword] = useState("");
  const [createdAtRange, setCreatedAtRange] = useState<DateRangeValue>([]);
  const [compactMode, setCompactMode] = useState(false);
  const [activePage, setActivePage] = useState(1);
  const [pageSize, setPageSize] = useSharedPageSize();

  useEffect(() => setActivePage(1), [pageSize]);
  const [facets, setFacets] = useState<TicketFacets | null>(null);
  const [createOpen, setCreateOpen] = useState(false);
  const [initialOrder, setInitialOrder] = useState<TicketOrderRef | null>(
    null
  );
  const [detailTicketNo, setDetailTicketNo] = useState<string | null>(null);

  const dateRangePresets = useMemo(() => createDateRangePresets(t), [t]);
  const [debouncedSearchKeyword, flushSearchKeyword] =
    useDebouncedValue(searchKeyword);

  // The Orders page hands an order over via sessionStorage to prefill creation.
  useEffect(() => {
    const pending = readPendingCreateOrder();
    if (pending) {
      setInitialOrder(pending);
      setCreateOpen(true);
    }
  }, []);

  const listFilter = useMemo<TicketListFilter>(() => {
    const filter: TicketListFilter = {};
    const search = debouncedSearchKeyword.trim();
    const createdFrom = createdFromISOString(createdAtRange);
    const createdTo = createdToISOString(createdAtRange);
    if (search) filter.search = search;
    if (typeFilter !== "all") filter.ticketType = typeFilter;
    if (statusFilter !== "all") filter.status = statusFilter;
    if (createdFrom) filter.createdFrom = createdFrom;
    if (createdTo) filter.createdTo = createdTo;
    return filter;
  }, [createdAtRange, debouncedSearchKeyword, statusFilter, typeFilter]);

  const loadTicketBlock = useCallback(
    async (offset: number, limit: number, cursor?: { afterId?: number }) => {
      const response = await listMyTickets(
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

  const {
    loading,
    pagedItems,
    refresh: refreshList,
    total,
    updateLoadedItems,
  } = useBlockPagedList<Ticket, TicketFacets>({
    activePage,
    filterKey: JSON.stringify(listFilter),
    loadBlock: loadTicketBlock,
    onError: (error) => {
      Toast.error(getIamErrorMessage(t, error, "Tickets load failed."));
    },
    onLoaded: (response) => {
      if (response.meta) setFacets(response.meta);
    },
    pageSize,
  });

  const totalPages = Math.max(1, Math.ceil(total / pageSize));
  const safePage = Math.min(activePage, totalPages);
  useEffect(() => {
    if (safePage !== activePage) setActivePage(safePage);
  }, [activePage, safePage]);

  const activeFilterCount = Number(statusFilter !== "all");

  const applyTypeFilter = (value: TypeFilter) => {
    setTypeFilter(value);
    setActivePage(1);
  };

  const resetFilters = () => {
    setSearchKeyword("");
    flushSearchKeyword("");
    setCreatedAtRange([]);
    setTypeFilter("all");
    setStatusFilter("all");
    setActivePage(1);
  };

  const openCreate = useCallback(() => {
    setInitialOrder(null);
    setCreateOpen(true);
  }, []);

  // Opening the detail marks the ticket read locally without a refetch.
  const openDetail = useCallback(
    (ticketNo: string) => {
      setDetailTicketNo(ticketNo);
      updateLoadedItems((items) =>
        items.map((item) =>
          item.ticketNo === ticketNo && item.requesterUnreadCount > 0
            ? { ...item, requesterUnreadCount: 0 }
            : item
        )
      );
    },
    [updateLoadedItems]
  );

  const handleTicketChanged = useCallback(() => {
    void refreshList();
  }, [refreshList]);

  const columns = useMemo(
    () =>
      [
        {
          key: "ticket",
          render: (_: unknown, record: Ticket) => (
            <TicketInboxRow
              onClick={() => openDetail(record.ticketNo)}
              showRequester={false}
              t={t}
              ticket={record}
              viewerRole="user"
            />
          ),
        },
      ] as Record<string, unknown>[],
    [openDetail, t]
  );

  const tabsArea = (
    <Tabs
      activeKey={typeFilter}
      collapsible
      onChange={(key) => applyTypeFilter(key as TypeFilter)}
      type="card"
    >
      <Tabs.TabPane
        itemKey="all"
        tab={
          <span className="flex items-center gap-2">
            {t("All")}
            <Tag color={typeFilter === "all" ? "red" : "grey"} shape="circle">
              {facets?.ticketType.all ?? total}
            </Tag>
          </span>
        }
      />
      <Tabs.TabPane
        itemKey="order"
        tab={
          <span className="flex items-center gap-2">
            {ticketTypeLabel("order", t)}
            <Tag color={typeFilter === "order" ? "red" : "grey"} shape="circle">
              {facets?.ticketType.order ?? 0}
            </Tag>
          </span>
        }
      />
      <Tabs.TabPane
        itemKey="general"
        tab={
          <span className="flex items-center gap-2">
            {ticketTypeLabel("general", t)}
            <Tag
              color={typeFilter === "general" ? "red" : "grey"}
              shape="circle"
            >
              {facets?.ticketType.general ?? 0}
            </Tag>
          </span>
        }
      />
    </Tabs>
  );

  const actionsArea = (
    <div className="flex w-full flex-col items-center justify-between gap-2 md:flex-row">
      <div className="order-2 flex w-full flex-wrap gap-2 md:order-1 md:w-auto">
        <Button
          className="flex-1 md:flex-initial"
          icon={<Plus size={14} />}
          onClick={openCreate}
          size="small"
          type="primary"
        >
          {t("Create ticket")}
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
            <div className="max-h-[70vh] w-[260px] overflow-auto p-2">
              <div className="px-2 pb-1 text-xs font-medium text-[var(--semi-color-text-2)]">
                {t("Status")}
              </div>
              <div className="space-y-1">
                {STATUS_OPTIONS.map((value) => (
                  <StatisticFilterOption
                    active={statusFilter === value}
                    count={facets?.status[value] ?? (value === "all" ? total : 0)}
                    key={value}
                    label={t(STATUS_LABEL_KEY[value])}
                    onSelect={(next) => {
                      setStatusFilter(next);
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
          placeholder={t("Search ticket, order or email")}
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
    onPageChange: (page) => setActivePage(page),
    onPageSizeChange: (size) => {
      setPageSize(size);
      setActivePage(1);
    },
    pageSize,
    t,
    total,
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
          className={`ticket-inbox-table overflow-hidden rounded-xl ${
            compactMode ? "is-compact" : ""
          }`}
          columns={columns as never}
          dataSource={pagedItems}
          empty={
            <Empty
              darkModeImage={
                <IllustrationNoResultDark style={{ height: 150, width: 150 }} />
              }
              description={
                <span className="flex flex-col items-center gap-3">
                  <span>{t("No tickets yet")}</span>
                  <Button
                    icon={<Plus size={14} />}
                    onClick={openCreate}
                    size="small"
                    type="primary"
                  >
                    {t("Create ticket")}
                  </Button>
                </span>
              }
              image={<IllustrationNoResult style={{ height: 150, width: 150 }} />}
              style={{ padding: 30 }}
            />
          }
          hidePagination
          loading={loading}
          pagination={false}
          rowKey="ticketNo"
          scroll={{ y: DESKTOP_TABLE_SCROLL_Y }}
          showHeader={false}
          size="middle"
        />
      </CardPro>

      <CreateTicketModal
        initialOrder={initialOrder}
        onCreated={handleTicketChanged}
        onOpenChange={setCreateOpen}
        onViewTicket={(ticket) => openDetail(ticket.ticketNo)}
        open={createOpen}
      />
      <TicketDetailSheet
        onChanged={handleTicketChanged}
        onClose={() => setDetailTicketNo(null)}
        ticketNo={detailTicketNo}
        viewerRole="user"
      />
    </div>
  );
}
